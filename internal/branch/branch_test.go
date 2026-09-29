package branch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"decision-tree/internal/checkpoint"
	"decision-tree/internal/store"
	"decision-tree/internal/tmux"
	"decision-tree/internal/tree"
)

const sessionA = "aaaaaaaa-1111-4111-8111-111111111111"

// world is a repo, a Claude config folder, a store, a fake claude, and a
// private tmux server. Nothing real is touched.
type world struct {
	repo, cfg, log string
	deps           Deps
	git            func(dir string, args ...string) string
}

// The fake claude: a fork (-p) saves the new session's chat file, like the
// real one does, and a resume just waits. Every call is logged.
const fakeClaude = `#!/bin/sh
printf '%s|' "$PWD" "$@" >> "LOG"; echo >> "LOG"
case " $* " in
  *" -p "*)
    sid=""; prev=""
    for a in "$@"; do [ "$prev" = "--session-id" ] && sid="$a"; prev="$a"; done
    mkdir -p "$CLAUDE_CONFIG_DIR/projects/fake-branch"
    echo '{"type":"user"}' > "$CLAUDE_CONFIG_DIR/projects/fake-branch/$sid.jsonl"
    echo "Branch ready." ;;
  *) sleep 60 ;;
esac
`

func newWorld(t *testing.T) *world {
	t.Helper()
	for _, bin := range []string{"git", "tmux"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " not installed")
		}
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	tmp := t.TempDir()
	tmp, _ = filepath.EvalSymlinks(tmp) // macOS: /var is /private/var
	w := &world{repo: filepath.Join(tmp, "app"), cfg: filepath.Join(tmp, "claude"), log: filepath.Join(tmp, "claude.log")}
	t.Setenv("CLAUDE_CONFIG_DIR", w.cfg)
	w.git = func(dir string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	fake := filepath.Join(tmp, "fake-claude")
	os.WriteFile(fake, []byte(strings.ReplaceAll(fakeClaude, "LOG", w.log)), 0o755)

	tm := tmux.Tmux{Socket: "decision-tree-branch-test-" + strings.ReplaceAll(t.Name(), "/", "-")}
	if out, err := exec.Command("tmux", "-L", tm.Socket, "-f", "/dev/null", "new-session", "-d", "-s", "work", "sleep 120").CombinedOutput(); err != nil {
		t.Fatalf("start tmux: %v %s", err, out)
	}
	t.Cleanup(func() { exec.Command("tmux", "-L", tm.Socket, "kill-server").Run() })

	w.deps = Deps{
		Store: store.Store{Dir: filepath.Join(tmp, "state")}, ClaudeDir: w.cfg, Claude: fake, Tmux: tm,
		Worktrees: filepath.Join(tmp, "worktrees"),
		Now:       func() time.Time { return time.Date(2026, 9, 29, 15, 0, 0, 0, time.UTC) },
	}
	return w
}

func put(t *testing.T, path, body string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// spec builds the spec's acceptance test (section 26): "Build an API",
// FastAPI chosen and checkpointed, then more work, then Redis.
func (w *world) spec(t *testing.T) {
	t.Helper()
	os.MkdirAll(w.repo, 0o755)
	w.git(w.repo, "init", "-q")
	put(t, filepath.Join(w.repo, "app.txt"), "v1\n")
	put(t, filepath.Join(w.repo, ".gitignore"), ".env\n")
	put(t, filepath.Join(w.repo, ".worktreeinclude"), "# secrets the branch needs\n.env\n")
	w.git(w.repo, "add", ".")
	w.git(w.repo, "commit", "-qm", "init")
	put(t, filepath.Join(w.repo, ".env"), "SECRET=1\n")
	put(t, filepath.Join(w.repo, "app.txt"), "v2: FastAPI\n") // not committed
	put(t, filepath.Join(w.repo, "new.txt"), "new file\n")    // not tracked

	// The chat, as Claude Code writes it.
	chat := filepath.Join(w.cfg, "projects", "-w-app")
	put(t, filepath.Join(chat, sessionA+".jsonl"), strings.Join([]string{
		`{"type":"user","uuid":"u1","parentUuid":null,"message":{"content":"Build an API. Use FastAPI."}}`,
		`{"type":"assistant","uuid":"u2","parentUuid":"u1","message":{"content":[{"type":"tool_use","id":"toolu_1","name":"mcp__decision-tree__record_decision"}]}}`,
		`{"type":"user","uuid":"u3","parentUuid":"u2","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"Saved as d1."}]}}`,
		`{"type":"assistant","uuid":"u4","parentUuid":"u3","message":{"content":[{"type":"text","text":"Implementing FastAPI."}]}}`,
		`{"type":"user","uuid":"u5","parentUuid":"u4","message":{"content":"Add Redis."}}`,
	}, "\n")+"\n")
	put(t, filepath.Join(chat, "memory", "MEMORY.md"), "- likes FastAPI\n")
	// The original Claude is running in tmux session "work".
	put(t, filepath.Join(w.cfg, "sessions", "99999.json"), `{"pid":99999,"sessionId":"`+sessionA+`","cwd":"`+w.repo+`","tmux":"work:@0.%0"}`)

	// Session A's tree: FastAPI, checkpointed the way the MCP server does it.
	maker := checkpoint.Maker{Dir: filepath.Join(filepath.Dir(w.repo), "checkpoints")}
	_, err := w.deps.Store.Update(sessionA, func(tr *tree.Tree) error {
		tr.Root().Label, tr.Folder = "Build an API", w.repo
		if _, err := tr.Record(tree.Call{Topic: "API framework", Options: []string{"FastAPI", "Flask"}, Picked: "FastAPI", Reason: "async, typed", By: tree.ByBoth}); err != nil {
			return err
		}
		cp := maker.Take(w.repo, filepath.Join(chat, "memory"), sessionA, "n1", "toolu_1", time.Now())
		tr.Node("n1").Checkpoint = cp
		path, err := checkpoint.SaveTree(maker.Dir, sessionA, "n1", tr)
		cp.Tree = path
		tr.Node("n1").Checkpoint = cp
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	// Later, in the original: more code, and Redis.
	put(t, filepath.Join(w.repo, "app.txt"), "v3: FastAPI + Redis\n")
	put(t, filepath.Join(chat, "memory", "redis.md"), "- uses Redis\n")
	w.deps.Store.Update(sessionA, func(tr *tree.Tree) error {
		_, err := tr.Record(tree.Call{Topic: "Cache", Options: []string{"Redis"}, Picked: "Redis", Reason: "speed", By: tree.ByClaude})
		return err
	})
}

func TestBranchFromFastAPI(t *testing.T) {
	w := newWorld(t)
	w.spec(t)

	p, err := Prepare(w.deps, "aaaa", "n1", "")
	if err != nil {
		t.Fatal(err)
	}
	wantWT := filepath.Join(w.deps.Worktrees, "app", "api-framework-fastapi")
	if p.Name != "api-framework-fastapi" || p.Worktree != wantWT || p.Memory != 1 || len(p.Warnings) != 0 {
		t.Fatalf("plan: name %q, worktree %q, memory %d, warnings %v", p.Name, p.Worktree, p.Memory, p.Warnings)
	}
	for _, want := range []string{
		"Branch from:  API framework: FastAPI",
		`Chat:         up to your message #1, "Build an API. Use FastAPI."`,
		"Code:         as it was then, in " + wantWT,
		"Memory:       as it was then (1 files)",
	} {
		if !strings.Contains(p.Summary(), want) {
			t.Errorf("summary is missing %q:\n%s", want, p.Summary())
		}
	}

	res, err := Create(context.Background(), w.deps, p, false)
	if err != nil {
		t.Fatal(err)
	}

	// The code, as it was at the decision: v2, the new file, and .env.
	read := func(rel string) string { b, _ := os.ReadFile(filepath.Join(p.Worktree, rel)); return string(b) }
	if read("app.txt") != "v2: FastAPI\n" || read("new.txt") != "new file\n" || read(".env") != "SECRET=1\n" {
		t.Fatalf("worktree: app.txt %q, new.txt %q, .env %q", read("app.txt"), read("new.txt"), read(".env"))
	}
	if !slices.Equal(res.Copied, []string{".env"}) {
		t.Fatalf("copied %v", res.Copied)
	}
	// The old work shows as not committed again, on the same HEAD.
	if got := w.git(p.Worktree, "status", "--porcelain"); got != "M app.txt\n?? new.txt" {
		t.Fatalf("worktree status %q", got)
	}
	if w.git(p.Worktree, "rev-parse", "HEAD") != w.git(w.repo, "rev-parse", "HEAD") {
		t.Fatal("the worktree should start on the original's HEAD")
	}
	// The original is untouched.
	if b, _ := os.ReadFile(filepath.Join(w.repo, "app.txt")); string(b) != "v3: FastAPI + Redis\n" {
		t.Fatalf("original app.txt = %q", b)
	}

	// The chat: forked at the cut, in the worktree, with hooks and MCP off.
	log, _ := os.ReadFile(w.log)
	fork := strings.Split(strings.TrimSpace(string(log)), "\n")[0]
	for _, want := range []string{p.Worktree + "|-p|--model|haiku|--strict-mcp-config|--settings|{\"disableAllHooks\":true}|",
		"|--resume|" + sessionA + "|--fork-session|--resume-session-at|u3|--session-id|" + p.Session + "|", "Branch ready."} {
		if !strings.Contains(fork, want) {
			t.Errorf("fork call is missing %q:\n%s", want, fork)
		}
	}

	// The memory as it was then: FastAPI, but not the later Redis note.
	mem := filepath.Join(w.cfg, "projects", "fake-branch", "memory")
	if _, err := os.Stat(filepath.Join(mem, "MEMORY.md")); err != nil {
		t.Fatalf("memory not copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(mem, "redis.md")); err == nil {
		t.Fatal("the branch got a memory saved after the decision")
	}

	// The tree as it was then, marked as a branch.
	bt, err := w.deps.Store.Load(p.Session)
	if err != nil {
		t.Fatal(err)
	}
	if bt.Branch == nil || bt.Branch.FromSession != sessionA || bt.Branch.FromNode != "n1" || bt.Branch.CutMessage != "u3" || bt.Branch.Name != p.Name {
		t.Fatalf("branch = %+v", bt.Branch)
	}
	if len(bt.Decisions) != 1 || bt.Here != "n1" || bt.Folder != p.Worktree || bt.SessionID != p.Session {
		t.Fatalf("branch tree: %d decisions, here %s, folder %s", len(bt.Decisions), bt.Here, bt.Folder)
	}
	at, _ := w.deps.Store.Load(sessionA)
	if len(at.Decisions) != 2 || at.Branch != nil {
		t.Fatal("the original tree changed")
	}

	// A new tmux window in the original's tmux session, running the branch.
	if !strings.HasPrefix(res.Pane, "%") {
		t.Fatalf("pane %q", res.Pane)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		log, _ = os.ReadFile(w.log)
		if strings.Contains(string(log), "|--resume|"+p.Session+"|-n|"+p.Name+"|--append-system-prompt|") || time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.Contains(string(log), p.Worktree+"|--resume|"+p.Session+"|-n|"+p.Name+"|--append-system-prompt|") {
		t.Fatalf("the tmux window did not start the branch:\n%s", log)
	}

	// A second branch from the same decision gets its own name and folder.
	p2, err := Prepare(w.deps, sessionA, "n1", "")
	if err != nil || p2.Name != "api-framework-fastapi-2" || p2.Session == p.Session {
		t.Fatalf("second plan: %+v, %v", p2, err)
	}
}

func TestPrepareRefuses(t *testing.T) {
	w := newWorld(t)
	w.spec(t)
	for node, want := range map[string]string{
		"n9": "there is no node n9",
		"n0": "the start node is not a decision",
		"n2": "was never picked",  // Flask
		"n3": "has no checkpoint", // Redis: logged with no checkpoint in this test
	} {
		if _, err := Prepare(w.deps, sessionA, node, ""); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Prepare(%s) = %v, want %q", node, err, want)
		}
	}
}

func TestNoCodeCheckpointSaysSo(t *testing.T) {
	w := newWorld(t)
	w.spec(t)
	w.deps.Store.Update(sessionA, func(tr *tree.Tree) error {
		cp := tr.Node("n1").Checkpoint
		cp.Commit, cp.Repo, cp.Missing = "", "", "code not saved: the folder is not a git repository"
		tr.Node("n1").Checkpoint = cp
		return nil
	})
	p, err := Prepare(w.deps, sessionA, "n1", "")
	if err != nil {
		t.Fatal(err)
	}
	if p.Worktree != "" || p.Dir != w.repo || len(p.Warnings) == 0 ||
		!strings.Contains(p.Summary(), "Exact code state unavailable. The current folder will be used: "+w.repo) {
		t.Fatalf("plan: dir %q, summary:\n%s", p.Dir, p.Summary())
	}
	if p.Tree.Branch.Missing != "not exact: code" {
		t.Fatalf("branch missing = %q", p.Tree.Branch.Missing)
	}
}

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"API framework: FastAPI":  "api-framework-fastapi",
		"Database: PostgreSQL 16": "database-postgresql-16",
		"  ":                      "branch",
		"A very long decision topic that keeps going: and a long pick too": "a-very-long-decision-topic-that-keeps",
	}
	for in, want := range cases {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}
