package checkpoint

import (
	"crypto/sha256"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"decision-tree/internal/tree"
)

// isolate keeps the user's own git settings out of the tests.
func isolate(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "init.defaultBranch=main"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, path, body string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	write(t, filepath.Join(dir, "app.txt"), "v1\n")
	write(t, filepath.Join(dir, ".gitignore"), "secret.env\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-qm", "init")
	return dir
}

// state is everything about the user's repo that a checkpoint must not
// change.
func state(t *testing.T, dir string) []string {
	t.Helper()
	index := git(t, dir, "rev-parse", "--path-format=absolute", "--git-path", "index")
	b, _ := os.ReadFile(index)
	sum := sha256.Sum256(b)
	return []string{
		"status: " + git(t, dir, "status", "--porcelain=v1", "--untracked-files=all"),
		"index: " + string(sum[:]),
		"stash: " + git(t, dir, "stash", "list"),
		"head: " + git(t, dir, "rev-parse", "HEAD"),
		"branch: " + git(t, dir, "branch", "--show-current"),
		"refs/heads: " + git(t, dir, "for-each-ref", "refs/heads", "refs/tags"),
	}
}

func files(t *testing.T, dir, commit string) []string {
	t.Helper()
	return strings.Split(git(t, dir, "ls-tree", "-r", "--name-only", commit), "\n")
}

func TestSnapshotKeepsUncommittedWorkAndTouchesNothing(t *testing.T) {
	isolate(t)
	dir := newRepo(t)
	write(t, filepath.Join(dir, "app.txt"), "v2, not committed\n") // changed
	write(t, filepath.Join(dir, "staged.txt"), "staged\n")         // new, staged
	git(t, dir, "add", "staged.txt")
	write(t, filepath.Join(dir, "new.txt"), "untracked\n")            // new, not staged
	write(t, filepath.Join(dir, "secret.env"), "ignored\n")           // ignored by git
	write(t, filepath.Join(dir, ".claude/worktrees/b/x.txt"), "no\n") // Claude Code's worktrees
	before := state(t, dir)

	m := Maker{Dir: t.TempDir()}
	cp := m.Take(filepath.Join(dir), "", "s1", "n4", "toolu_1", time.Now())

	if cp.Missing != "" {
		t.Fatalf("missing: %s", cp.Missing)
	}
	if after := state(t, dir); !slices.Equal(before, after) {
		t.Fatalf("the user's repo changed:\nbefore %q\nafter  %q", before, after)
	}
	if got := files(t, dir, cp.Commit); !slices.Equal(got, []string{".gitignore", "app.txt", "new.txt", "staged.txt"}) {
		t.Fatalf("snapshot holds %v", got)
	}
	if got := git(t, dir, "show", cp.Commit+":app.txt"); got != "v2, not committed" {
		t.Fatalf("app.txt in snapshot = %q", got)
	}
	if got := git(t, dir, "rev-parse", cp.Commit+"^"); got != git(t, dir, "rev-parse", "HEAD") {
		t.Fatalf("snapshot's parent = %s, want HEAD", got)
	}
	if cp.Ref != "refs/decision-tree/checkpoints/s1/n4" || git(t, dir, "rev-parse", cp.Ref) != cp.Commit {
		t.Fatalf("ref %q does not point at the snapshot", cp.Ref)
	}
	if real, _ := filepath.EvalSymlinks(dir); cp.Repo != real && cp.Repo != dir {
		t.Fatalf("repo = %q, want %q", cp.Repo, dir)
	}
	// Not in the normal log, and not a branch or a tag.
	if strings.Contains(git(t, dir, "log", "--oneline"), "checkpoint") {
		t.Fatal("the snapshot shows up in the normal log")
	}
	// Known and accepted: views of every ref (git log --all, gitk --all,
	// lazygit's full graph) do show it, the same way they show the stash.
	if !strings.Contains(git(t, dir, "log", "--all", "--oneline"), "decision-tree checkpoint") {
		t.Fatal("expected git log --all to show the snapshot; if git changed, update PRD 17.1")
	}
}

func TestSnapshotFromASubfolder(t *testing.T) {
	isolate(t)
	dir := newRepo(t)
	write(t, filepath.Join(dir, "sub/deep.txt"), "deep\n")
	cp := Maker{Dir: t.TempDir()}.Take(filepath.Join(dir, "sub"), "", "s1", "n1", "toolu_1", time.Now())
	if cp.Missing != "" || !slices.Contains(files(t, dir, cp.Commit), "sub/deep.txt") {
		t.Fatalf("missing %q; the whole repo should be saved, not just the subfolder", cp.Missing)
	}
}

func TestRepoWithNoCommitsYet(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	write(t, filepath.Join(dir, "first.txt"), "hello\n")
	cp := Maker{Dir: t.TempDir()}.Take(dir, "", "s1", "n1", "toolu_1", time.Now())
	if cp.Missing != "" {
		t.Fatalf("missing: %s", cp.Missing)
	}
	if got := files(t, dir, cp.Commit); !slices.Equal(got, []string{"first.txt"}) {
		t.Fatalf("snapshot holds %v", got)
	}
	if _, err := exec.Command("git", "-C", dir, "rev-parse", "--verify", "--quiet", cp.Commit+"^").Output(); err == nil {
		t.Fatal("a snapshot in an empty repo should have no parent")
	}
}

func TestSigningSettingsDoNotGetInTheWay(t *testing.T) {
	isolate(t)
	dir := newRepo(t)
	git(t, dir, "config", "commit.gpgsign", "true")
	git(t, dir, "config", "gpg.program", "false") // signing would fail
	cp := Maker{Dir: t.TempDir()}.Take(dir, "", "s1", "n1", "toolu_1", time.Now())
	if cp.Missing != "" || cp.Commit == "" {
		t.Fatalf("missing: %s", cp.Missing)
	}
}

func TestNotARepo(t *testing.T) {
	isolate(t)
	cp := Maker{Dir: t.TempDir()}.Take(t.TempDir(), "", "s1", "n1", "toolu_1", time.Now())
	if cp.Commit != "" || !strings.Contains(cp.Missing, "code not saved: the folder is not a git repository") {
		t.Fatalf("checkpoint = %+v", cp)
	}
}

func TestTooSlow(t *testing.T) {
	isolate(t)
	dir := newRepo(t)
	cp := Maker{Dir: t.TempDir(), Timeout: time.Nanosecond}.Take(dir, "", "s1", "n1", "toolu_1", time.Now())
	if cp.Commit != "" || !strings.Contains(cp.Missing, "git took too long") {
		t.Fatalf("checkpoint = %+v", cp)
	}
}

func TestMemoryIsCopied(t *testing.T) {
	isolate(t)
	mem := t.TempDir()
	write(t, filepath.Join(mem, "MEMORY.md"), "- [a](a.md)\n")
	write(t, filepath.Join(mem, "a.md"), "fact a\n")
	os.Mkdir(filepath.Join(mem, "sub"), 0o755)
	store := t.TempDir()

	cp := Maker{Dir: store}.Take(t.TempDir(), mem, "s1", "n7", "toolu_1", time.Now())
	if cp.Memory != filepath.Join(store, "s1", "n7", "memory") {
		t.Fatalf("memory copy at %q", cp.Memory)
	}
	got, _ := os.ReadFile(filepath.Join(cp.Memory, "a.md"))
	entries, _ := os.ReadDir(cp.Memory)
	if string(got) != "fact a\n" || len(entries) != 2 {
		t.Fatalf("copied %d entries, a.md = %q", len(entries), got)
	}

	// Changing the memory later does not change the copy.
	write(t, filepath.Join(mem, "a.md"), "fact a, changed later\n")
	if got, _ := os.ReadFile(filepath.Join(cp.Memory, "a.md")); string(got) != "fact a\n" {
		t.Fatalf("the copy changed: %q", got)
	}
}

func TestNoMemoryFolderIsFine(t *testing.T) {
	isolate(t)
	dir := newRepo(t)
	cp := Maker{Dir: t.TempDir()}.Take(dir, filepath.Join(t.TempDir(), "none"), "s1", "n1", "toolu_1", time.Now())
	if cp.Memory != "" || cp.Missing != "" {
		t.Fatalf("checkpoint = %+v", cp)
	}
}

func TestNoToolUseID(t *testing.T) {
	isolate(t)
	cp := Maker{Dir: t.TempDir()}.Take(newRepo(t), "", "s1", "n1", "", time.Now())
	if !strings.Contains(cp.Missing, "chat position unknown") || cp.Commit == "" {
		t.Fatalf("checkpoint = %+v", cp)
	}
}

func TestSaveTree(t *testing.T) {
	tr := tree.New("s1", time.Now())
	if _, err := tr.Record(tree.Call{Topic: "API", Options: []string{"REST"}, Picked: "REST", Reason: "r", By: tree.ByUser}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path, err := SaveTree(dir, "s1", "n1", tr)
	if err != nil || path != filepath.Join(dir, "s1", "n1", "tree.json") {
		t.Fatalf("path %q, err %v", path, err)
	}
	b, _ := os.ReadFile(path)
	var back tree.Tree
	if err := json.Unmarshal(b, &back); err != nil || back.Here != "n1" || back.Node("n1").Label != "REST" {
		t.Fatalf("saved tree = %+v, %v", back, err)
	}
}
