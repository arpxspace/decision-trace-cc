// Package branch starts a new Claude session from a decision (PRD 17.2).
// The original session, its chat, and its files are never changed.
//
// Prepare works out what a branch would start with and what is not exact.
// It changes nothing. Create does it:
//
//  1. the code: a git worktree from the checkpoint's snapshot
//  2. the chat: a fork of the original, cut right after the decision
//  3. the memory: Claude's memory as it was then, in the branch's folder
//  4. the tree: the tree as it was then, marked as a branch
//  5. a new tmux window running the branch
package branch

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"decision-tree/internal/claude"
	"decision-tree/internal/graph"
	"decision-tree/internal/store"
	"decision-tree/internal/tmux"
	"decision-tree/internal/tree"
)

// Deps are the outside world, so tests can use fakes.
type Deps struct {
	Store     store.Store
	ClaudeDir string // ~/.claude
	Claude    string // path to the claude program
	Git       string // path to git; "" = from PATH
	Tmux      tmux.Tmux
	Worktrees string // ~/.local/state/decision-tree/worktrees
	Now       func() time.Time
}

// Plan is a branch that is ready to be made.
type Plan struct {
	Parent   *tree.Tree
	Node     *tree.Node
	Cut      claude.Cut
	Name     string
	Session  string     // the new session's id
	Repo     string     // the original folder
	Worktree string     // the branch's own folder; "" = it shares the original folder
	Dir      string     // where the branch session runs
	Memory   int        // how many memory files it starts with
	Tree     *tree.Tree // the branch's starting tree
	Warnings []string   // what is not exact, in plain words
}

// Prepare works out a branch from node in session. It changes nothing.
func Prepare(d Deps, session, node, name string) (*Plan, error) {
	id, err := d.Store.Find(session)
	if err != nil {
		return nil, err
	}
	parent, err := d.Store.Load(id)
	if err != nil {
		return nil, err
	}
	n := parent.Node(node)
	switch {
	case n == nil || n.Hidden:
		return nil, fmt.Errorf("there is no node %s in session %s", node, short(id))
	case n.Decision == "":
		return nil, errors.New("the start node is not a decision; branch from a pick")
	case n.State != tree.Picked && n.State != tree.Dropped:
		return nil, fmt.Errorf("%q was never picked, so there is no moment to go back to", parent.Statement(n))
	}
	cp := n.Checkpoint
	if cp.ToolUseID == "" {
		return nil, fmt.Errorf("%q has no checkpoint: it was logged before checkpoints existed", parent.Statement(n))
	}
	chat, err := claude.ChatPath(d.ClaudeDir, id)
	if err != nil {
		return nil, fmt.Errorf("the chat of session %s is not on this computer", short(id))
	}
	cut, err := claude.FindCut(chat, cp.ToolUseID)
	if err != nil {
		return nil, err
	}

	p := &Plan{Parent: parent, Node: n, Cut: cut, Session: newID(), Repo: cp.Repo}
	if p.Repo == "" {
		p.Repo = parent.Folder
	}
	project := filepath.Base(p.Repo)
	if name == "" {
		name = Slug(parent.Statement(n))
	}
	p.Name = name

	if cp.Commit != "" {
		p.Worktree = freePath(filepath.Join(d.Worktrees, project, name))
		p.Name = filepath.Base(p.Worktree) // "-2" and so on, if the name was taken
		p.Dir = p.Worktree
		if entries, err := os.ReadDir(cp.Memory); err == nil && cp.Memory != "" {
			p.Memory = len(entries)
		}
	} else {
		if p.Repo == "" {
			return nil, errors.New("there is no code checkpoint and no folder to fall back on")
		}
		p.Dir = p.Repo
		p.Warnings = append(p.Warnings,
			"Exact code state unavailable. The current folder will be used: "+p.Repo+".",
			"Both sessions will work on the same files, and share Claude's memory, including anything saved later.")
	}

	if cp.Tree != "" {
		if b, err := os.ReadFile(cp.Tree); err == nil {
			var t tree.Tree
			if json.Unmarshal(b, &t) == nil && len(t.Nodes) > 0 {
				p.Tree = &t
			}
		}
	}
	if p.Tree == nil {
		p.Tree = parent.UpTo(n.ID)
		p.Warnings = append(p.Warnings, "The tree was rebuilt from today's, so a decision above this one that changed later shows its later pick.")
	}
	var missing []string
	if cp.Commit == "" {
		missing = append(missing, "code")
	}
	p.Tree.SessionID, p.Tree.Folder, p.Tree.Fixes = p.Session, p.Dir, nil
	inherited := 0
	for _, d := range p.Tree.Decisions {
		if !d.Hidden {
			inherited++
		}
	}
	p.Tree.Branch = &tree.Branch{
		Name: p.Name, FromSession: id, FromNode: n.ID, FromDecision: n.Decision, Inherited: inherited,
		Statement: parent.Statement(n), CutMessage: cut.UUID, Commit: cp.Commit,
		Repo: p.Repo, Worktree: p.Worktree, At: d.Now(),
	}
	if len(missing) > 0 {
		p.Tree.Branch.Missing = "not exact: " + strings.Join(missing, ", ")
	}
	return p, nil
}

// Summary is what to show before asking "Create branch?".
func (p *Plan) Summary() string {
	var b strings.Builder
	at := p.Node.At.Local().Format("02 Jan 15:04")
	fmt.Fprintf(&b, "Branch from:  %s · %s\n", p.Parent.Statement(p.Node), at)
	fmt.Fprintf(&b, "Session:      %s (%s)\n", short(p.Parent.SessionID), filepath.Base(p.Repo))
	fmt.Fprintf(&b, "Chat:         up to your message #%d, %q\n", p.Cut.PromptNumber, clip(p.Cut.Prompt, 60))
	if p.Worktree != "" {
		fmt.Fprintf(&b, "Code:         as it was then, in %s\n", p.Worktree)
		if p.Memory > 0 {
			fmt.Fprintf(&b, "Memory:       as it was then (%d files)\n", p.Memory)
		} else {
			fmt.Fprintf(&b, "Memory:       none was saved then, so the branch starts with none\n")
		}
	} else {
		fmt.Fprintf(&b, "Code:         not available\n")
	}
	fmt.Fprintf(&b, "Name:         %s\n", p.Name)
	for _, w := range p.Warnings {
		fmt.Fprintf(&b, "\n%s", w)
	}
	return strings.TrimRight(b.String(), "\n")
}

// Result is what Create made.
type Result struct {
	Pane    string   // the new tmux pane, or ""
	Command string   // when tmux is not running: what to run to start the branch
	Copied  []string // files copied from .worktreeinclude
}

// Create makes the branch. If a step fails, the branch's new folder is
// removed again; the original is never touched either way.
func Create(ctx context.Context, d Deps, p *Plan, focus bool) (Result, error) {
	var res Result
	cp := p.Node.Checkpoint
	if p.Worktree != "" {
		if err := os.MkdirAll(filepath.Dir(p.Worktree), 0o755); err != nil {
			return res, err
		}
		if err := d.worktree(ctx, cp.Repo, p.Worktree, cp.Commit); err != nil {
			return res, fmt.Errorf("the code: %w", err)
		}
		copied, err := d.include(cp.Repo, p.Worktree)
		if err != nil {
			d.removeWorktree(cp.Repo, p.Worktree)
			return res, fmt.Errorf("copying .worktreeinclude files: %w", err)
		}
		res.Copied = copied
	}

	if err := d.fork(ctx, p); err != nil {
		d.removeWorktree(cp.Repo, p.Worktree)
		return res, fmt.Errorf("the chat: %w", err)
	}

	if p.Worktree != "" && cp.Memory != "" {
		chat, err := claude.ChatPath(d.ClaudeDir, p.Session)
		if err == nil {
			err = copyFiles(cp.Memory, filepath.Join(filepath.Dir(chat), "memory"))
		}
		if err != nil {
			return res, fmt.Errorf("the memory: %w", err)
		}
	}

	if _, err := d.Store.Update(p.Session, func(t *tree.Tree) error {
		*t = *p.Tree
		return nil
	}); err != nil {
		return res, fmt.Errorf("the tree: %w", err)
	}

	args := []string{d.claude(), "--resume", p.Session, "-n", p.Name}
	if p.Worktree != "" {
		args = append(args, "--append-system-prompt", "This session is a branch named "+p.Name+". Its files are in "+p.Worktree+
			". The folder "+p.Repo+" belongs to the original session: never change files there.")
	}
	target := d.parentTmuxSession(p.Parent.SessionID)
	if target == "" {
		res.Command = "cd " + tmux.ShellQuote([]string{p.Dir}) + " && " + tmux.ShellQuote(args)
		return res, nil
	}
	pane, err := d.Tmux.NewWindow(target, p.Dir, p.Name, args, focus)
	if err != nil {
		res.Command = "cd " + tmux.ShellQuote([]string{p.Dir}) + " && " + tmux.ShellQuote(args)
		return res, fmt.Errorf("the tmux window: %w", err)
	}
	res.Pane = pane
	if focus {
		d.Tmux.Focus(pane) // best effort: the window is open either way
	}
	return res, nil
}

// worktree makes the branch's folder: the files as they were, including
// work that was not committed, which shows as not committed again. HEAD is
// the commit the original was on.
func (d Deps) worktree(ctx context.Context, repo, path, snapshot string) error {
	parent, err := d.git(ctx, repo, "rev-parse", "--verify", "--quiet", snapshot+"^")
	if err != nil || parent == "" {
		// A repo with no commits yet: the snapshot is all there is.
		_, err := d.git(ctx, repo, "worktree", "add", "--detach", path, snapshot)
		return err
	}
	if _, err := d.git(ctx, repo, "worktree", "add", "--detach", path, parent); err != nil {
		return err
	}
	// Put the snapshot's files in place (deleted files too), then let the
	// index match HEAD again, so the old work is not committed or staged.
	if _, err := d.git(ctx, path, "read-tree", "-u", "--reset", snapshot); err != nil {
		d.removeWorktree(repo, path)
		return err
	}
	if _, err := d.git(ctx, path, "reset", "--quiet"); err != nil {
		d.removeWorktree(repo, path)
		return err
	}
	return nil
}

func (d Deps) removeWorktree(repo, path string) {
	if path != "" && repo != "" {
		d.git(context.Background(), repo, "worktree", "remove", "--force", path)
	}
}

// include copies files that git ignores (like .env) into the worktree, when
// the repo's .worktreeinclude names them (Claude Code's own convention).
// They are copied as they are now: git ignores them, so no snapshot has them.
func (d Deps) include(repo, worktree string) ([]string, error) {
	b, err := os.ReadFile(filepath.Join(repo, ".worktreeinclude"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var copied []string
	for _, line := range strings.Split(string(b), "\n") {
		pattern := strings.TrimSpace(line)
		if pattern == "" || strings.HasPrefix(pattern, "#") {
			continue
		}
		matches, _ := filepath.Glob(filepath.Join(repo, strings.TrimPrefix(pattern, "/")))
		for _, m := range matches {
			rel, err := filepath.Rel(repo, m)
			if err != nil || strings.HasPrefix(rel, "..") {
				continue
			}
			// Only ignored files: everything else is in the snapshot already.
			if _, err := d.git(context.Background(), repo, "check-ignore", "--quiet", "--", rel); err != nil {
				continue
			}
			if err := copyTree(m, filepath.Join(worktree, rel)); err != nil {
				return copied, err
			}
			copied = append(copied, rel)
		}
	}
	return copied, nil
}

// fork makes the branch's chat with Claude Code's own fork (docs/findings.md
// part 9): a new session, cut right after the decision. It runs in the
// branch's folder, so the chat is saved there. Hooks and MCP servers are
// off for this one short run.
func (d Deps) fork(ctx context.Context, p *Plan) error {
	note := "[decision-tree] This is a branch named " + p.Name + ". It continues session " + short(p.Parent.SessionID) +
		" from right after the decision \"" + p.Parent.Statement(p.Node) + "\". Nothing said after that point is part of this branch."
	if p.Worktree != "" {
		note += " The project's files are now in " + p.Worktree + ", as they were at that moment. The folder " + p.Repo +
			" belongs to the other session: do not change files there; use the same paths inside " + p.Worktree + " instead."
	}
	note += " Reply with only: Branch ready."

	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, d.claude(), "-p", "--model", "haiku",
		"--strict-mcp-config", "--settings", `{"disableAllHooks":true}`,
		"--resume", p.Parent.SessionID, "--fork-session", "--resume-session-at", p.Cut.UUID,
		"--session-id", p.Session, note)
	cmd.Dir = p.Dir
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("claude: %v: %s", err, clip(strings.TrimSpace(out.String()), 300))
	}
	if _, err := claude.ChatPath(d.ClaudeDir, p.Session); err != nil {
		return fmt.Errorf("claude ran, but the branch's chat was not saved: %s", clip(strings.TrimSpace(out.String()), 300))
	}
	return nil
}

// parentTmuxSession is the tmux session of the original Claude, if it is
// still running in tmux, or else the one you are looking at.
func (d Deps) parentTmuxSession(session string) string {
	for _, s := range claude.ReadAll(d.ClaudeDir) {
		if s.ID == session && s.Tmux != "" {
			if name, _, ok := strings.Cut(s.Tmux, ":"); ok && name != "" {
				return name
			}
		}
	}
	name, _ := d.Tmux.ActiveSession()
	return name
}

func (d Deps) claude() string {
	if d.Claude != "" {
		return d.Claude
	}
	if p, err := exec.LookPath("claude"); err == nil {
		return p
	}
	return "claude"
}

func (d Deps) git(ctx context.Context, dir string, args ...string) (string, error) {
	bin := d.Git
	if bin == "" {
		bin = "git"
	}
	cmd := exec.CommandContext(ctx, bin, append([]string{"-C", dir}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", errors.New("git " + args[0] + ": " + msg)
	}
	return strings.TrimSpace(string(out)), nil
}

var notWord = regexp.MustCompile(`[^a-z0-9]+`)

// Slug makes a branch name from a decision: "API framework: FastAPI"
// becomes "api-framework-fastapi".
func Slug(s string) string {
	s = strings.Trim(notWord.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(s) > 40 {
		s = s[:40]
		if i := strings.LastIndex(s, "-"); i > 20 {
			s = s[:i]
		}
	}
	if s == "" {
		return "branch"
	}
	return s
}

// freePath is path, or path-2, path-3, … if it is taken.
func freePath(path string) string {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return path
	}
	for i := 2; ; i++ {
		p := path + "-" + strconv.Itoa(i)
		if _, err := os.Stat(p); errors.Is(err, os.ErrNotExist) {
			return p
		}
	}
}

// newID is a random UUID, as Claude Code wants for --session-id.
func newID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func copyFiles(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, e := range entries {
		if e.Type().IsRegular() {
			if err := copyFile(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

// copyTree copies a file, or a folder and everything in it.
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		switch {
		case fi.IsDir():
			return os.MkdirAll(target, 0o755)
		case fi.Mode().IsRegular():
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			return copyFile(path, target)
		}
		return nil // links and other special files are skipped
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fi.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// Children are the branches made from session, by the node each came from,
// oldest first. Trees that cannot be read are skipped.
func Children(st store.Store, session string) map[string][]graph.Stub {
	infos, _ := st.List()
	type found struct {
		node string
		at   time.Time
		stub graph.Stub
	}
	var all []found
	for _, in := range infos {
		t, err := st.Load(in.SessionID)
		if err != nil || t.Branch == nil || t.Branch.FromSession != session {
			continue
		}
		n := 0
		for _, d := range t.Decisions {
			if !d.Hidden {
				n++
			}
		}
		// Decisions the branch made itself, not the ones it started with.
		n -= t.Branch.Inherited
		all = append(all, found{t.Branch.FromNode, t.Branch.At, graph.Stub{Session: t.SessionID, Name: t.Branch.Name, Decisions: max(n, 0)}})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].at.Before(all[j].at) })
	out := map[string][]graph.Stub{}
	for _, f := range all {
		out[f.node] = append(out[f.node], f.stub)
	}
	return out
}
