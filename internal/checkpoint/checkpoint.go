// Package checkpoint saves what a branch needs to start from a decision
// (PRD 17.1): a git snapshot of the working folder and a copy of Claude's
// memory for the project. The chat side is just the tool-use id, which the
// caller stores.
//
// None of this may change the user's work. The snapshot is built in a
// temporary index and kept under a hidden ref, so the branch, the staging
// area, the stash, and the log stay exactly as they were
// (docs/findings.md part 9, test 5).
package checkpoint

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"decision-tree/internal/tree"
)

// Maker takes checkpoints.
type Maker struct {
	Dir     string        // where memory copies go: ~/.local/state/decision-tree/checkpoints
	Git     string        // path to git; "" = "git" from PATH
	Timeout time.Duration // the whole git snapshot must finish in this time
}

// RefPrefix is where snapshots are kept. Refs here are not branches or tags,
// so git log, git branch, and git stash never show them.
const RefPrefix = "refs/decision-tree/checkpoints/"

// Take saves a checkpoint for node in session. repo is the folder Claude is
// working in; memory is Claude's memory folder for the project (it may not
// exist). Whatever cannot be saved is said in Missing, in plain words; the
// rest is still saved.
func (m Maker) Take(repo, memory, session, node, toolUseID string, at time.Time) tree.Checkpoint {
	cp := tree.Checkpoint{ToolUseID: toolUseID, At: at}
	var missing []string
	if toolUseID == "" {
		missing = append(missing, "chat position unknown: the call had no tool-use id")
	}

	timeout := m.Timeout
	if timeout == 0 {
		timeout = 20 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	ref := RefPrefix + session + "/" + node
	commit, top, err := Snapshot(ctx, m.Git, repo, ref, "decision-tree checkpoint: session "+session+", node "+node)
	if err != nil {
		missing = append(missing, "code not saved: "+err.Error())
	} else {
		cp.Repo, cp.Commit, cp.Ref = top, commit, ref
	}

	dst := filepath.Join(m.Dir, session, node, "memory")
	switch n, err := copyDir(memory, dst); {
	case err != nil:
		missing = append(missing, "memory not saved: "+err.Error())
	case n > 0:
		cp.Memory = dst
	}
	cp.Missing = strings.Join(missing, "; ")
	return cp
}

// ErrNotRepo means the folder is not inside a git repository.
var ErrNotRepo = errors.New("the folder is not a git repository")

// Snapshot saves the working folder as a commit and points ref at it. It
// returns the commit and the repo's top folder.
//
// It works on a copy of the index, so git only has to look at files that
// changed. Files git ignores are left out. Claude Code's own worktrees under
// .claude/worktrees are left out too.
func Snapshot(ctx context.Context, git, dir, ref, message string) (commit, top string, err error) {
	if git == "" {
		git = "git"
	}
	run := func(env []string, args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, git, args...)
		cmd.Env = append(os.Environ(), env...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if ctx.Err() != nil {
			return "", errors.New("git took too long")
		}
		if err != nil {
			msg := strings.TrimSpace(stderr.String())
			if msg == "" {
				msg = err.Error()
			}
			return "", fmt.Errorf("git %s: %s", args[len(args)-1], msg)
		}
		return strings.TrimSpace(string(out)), nil
	}

	top, err = run(nil, "-C", dir, "rev-parse", "--show-toplevel")
	if err != nil {
		if ctx.Err() == nil {
			return "", "", ErrNotRepo
		}
		return "", "", err
	}
	index, err := run(nil, "-C", top, "rev-parse", "--path-format=absolute", "--git-path", "index")
	if err != nil {
		return "", "", err
	}

	tmp, err := os.CreateTemp("", "decision-tree-index-*")
	if err != nil {
		return "", "", err
	}
	tmpIndex := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpIndex)
	// A repo with no commits yet may have no index. Git then starts empty.
	if err := copyFile(index, tmpIndex); errors.Is(err, os.ErrNotExist) {
		os.Remove(tmpIndex)
	} else if err != nil {
		return "", "", err
	}

	withIndex := []string{"GIT_INDEX_FILE=" + tmpIndex}
	if _, err := run(withIndex, "-C", top, "add", "--all", "--", ".", ":(exclude).claude/worktrees"); err != nil {
		return "", "", err
	}
	treeID, err := run(withIndex, "-C", top, "write-tree")
	if err != nil {
		return "", "", err
	}

	// The snapshot has its own name and never signs, so it works whatever
	// the user's git settings are.
	args := []string{"-C", top, "-c", "user.name=decision-tree", "-c", "user.email=decision-tree@localhost",
		"commit-tree", "--no-gpg-sign", "-m", message}
	if head, err := run(nil, "-C", top, "rev-parse", "--verify", "--quiet", "HEAD^{commit}"); err == nil && head != "" {
		args = append(args, "-p", head)
	}
	commit, err = run(nil, append(args, treeID)...)
	if err != nil {
		return "", "", err
	}
	if _, err := run(nil, "-C", top, "update-ref", "-m", message, ref, commit); err != nil {
		return "", "", err
	}
	return commit, top, nil
}

// copyDir copies the plain files of src into dst (not sub-folders; Claude's
// memory folder is flat). A missing src is not an error: it copies nothing.
func copyDir(src, dst string) (int, error) {
	entries, err := os.ReadDir(src)
	if errors.Is(err, os.ErrNotExist) || src == "" {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		if n == 0 {
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return 0, err
			}
		}
		if err := copyFile(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
