package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"decision-tree/internal/server"
	"decision-tree/internal/store"
	"decision-tree/internal/tree"
)

// TestEndToEnd runs the real binary the way the Claude Code mod does: a
// record_decision call goes to `record` as JSON on stdin, with the session,
// its folder, and the call's tool-use id. Then the pane's commands read it.
func TestEndToEnd(t *testing.T) {
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "decision-tree")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	cfg, state := filepath.Join(tmp, "claude"), filepath.Join(tmp, "state")
	const sid = "8533417c-1b9d-4c3c-a771-f0df5b76dae2"
	env := append(os.Environ(), "CLAUDE_CONFIG_DIR="+cfg, "XDG_STATE_HOME="+state,
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")

	// The session works in a git repo with uncommitted work, and Claude has
	// a memory file for the project.
	repo := filepath.Join(tmp, "crm")
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	os.MkdirAll(repo, 0o755)
	git("init", "-q")
	writeFile(t, filepath.Join(repo, "app.txt"), "v1\n")
	git("add", ".")
	git("commit", "-qm", "init")
	writeFile(t, filepath.Join(repo, "app.txt"), "v2, not committed\n")
	// The chat, as Claude Code writes it: the user's message, Claude's call
	// to record_decision, and the tool's answer (docs/findings.md part 9).
	writeFile(t, filepath.Join(cfg, "projects", "-w-crm", sid+".jsonl"), strings.Join([]string{
		`{"type":"ai-title","aiTitle":"CRM search is slow","sessionId":"` + sid + `"}`,
		`{"type":"user","uuid":"u1","parentUuid":null,"message":{"role":"user","content":"Search is slow. Add an index."}}`,
		`{"type":"assistant","uuid":"u2","parentUuid":"u1","message":{"content":[{"type":"tool_use","id":"toolu_e2e","name":"mcp__decision-tree__record_decision"}]}}`,
		`{"type":"user","uuid":"u3","parentUuid":"u2","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_e2e","content":"Saved as d1."}]}}`,
	}, "\n")+"\n")
	writeFile(t, filepath.Join(cfg, "projects", "-w-crm", "memory", "MEMORY.md"), "- the user likes short answers\n")

	cmd := exec.Command(bin, "record", "--session", sid, "--cwd", repo, "--tool-use-id", "toolu_e2e")
	cmd.Env = env
	cmd.Stdin = strings.NewReader(`{"topic":"Fix","options":["add a cache","add a database index"],"picked":"add a database index","reason":"fixes the query itself","by":"both"}`)
	if out, err := cmd.CombinedOutput(); err != nil || !strings.HasPrefix(string(out), "Saved as d1.") {
		t.Fatalf("record: %v\n%s", err, out)
	}

	out := runBin(t, bin, env, "print", "8533")
	for _, want := range []string{
		"crm · session 8533417c · 1 decisions",
		"●  CRM search is slow\n│\n├─×  add a cache\n●  Fix: add a database index  ◀",
		"Why\n  Fix: add a database index — fixes the query itself (both)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("print is missing %q:\n%s", want, out)
		}
	}
	if out := runBin(t, bin, env, "list"); !strings.Contains(out, "8533417c  crm     1          CRM search is slow") {
		t.Errorf("list:\n%s", out)
	}

	// The pick got a checkpoint: chat position, code, and memory (PRD 17.1).
	tr, err := store.Store{Dir: filepath.Join(state, "decision-tree")}.Load(sid)
	if err != nil {
		t.Fatal(err)
	}
	cp := tr.Node("n2").Checkpoint
	if cp.ToolUseID != "toolu_e2e" || cp.Commit == "" || cp.Missing != "" {
		t.Fatalf("checkpoint = %+v", cp)
	}
	if got := git("rev-parse", "refs/decision-tree/checkpoints/"+sid+"/n2"); got != cp.Commit {
		t.Fatalf("ref points at %s, checkpoint says %s", got, cp.Commit)
	}
	if got := git("show", cp.Commit+":app.txt"); got != "v2, not committed" {
		t.Fatalf("snapshot has app.txt = %q", got)
	}
	if got := git("status", "--porcelain"); got != "M app.txt" {
		t.Fatalf("the repo changed: status = %q", got)
	}
	if b, err := os.ReadFile(filepath.Join(cp.Memory, "MEMORY.md")); err != nil || string(b) != "- the user likes short answers\n" {
		t.Fatalf("memory copy: %q, %v", b, err)
	}

	// A branch made from the pick shows in print, under the pick.
	_, err = store.Store{Dir: filepath.Join(state, "decision-tree")}.Update("bbbbbbbb-2222-4222-8222-222222222222", func(bt *tree.Tree) error {
		bt.Root().Label = "CRM search is slow"
		bt.Branch = &tree.Branch{Name: "try-a-cache", FromSession: sid, FromNode: "n2", Statement: "Fix: add a database index"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if out := runBin(t, bin, env, "print", "8533"); !strings.Contains(out, "●  Fix: add a database index  ◀\n╰─⎇  try-a-cache · 0 decisions") {
		t.Errorf("print should show the branch under the pick:\n%s", out)
	}
	if out := runBin(t, bin, env, "print", "bbbb"); !strings.Contains(out, `Branch of session 8533417c, from "Fix: add a database index"`) {
		t.Errorf("print of a branch should say where it came from:\n%s", out)
	}

	// source shows where the pick came from, and where a branch would cut.
	out = runBin(t, bin, env, "source", "8533")
	for _, want := range []string{
		"Fix: add a database index (n2)",
		`After your message #1: "Search is slow. Add an index."`,
		"A branch would cut the chat at: u3",
		"Code: " + cp.Commit[:12],
		"Memory: " + cp.Memory,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("source is missing %q:\n%s", want, out)
		}
	}

	// The mod's pane (PRD 19) reads the same things as JSON.
	var data struct {
		Session  string
		Path     string
		Tree     *tree.Tree
		Branches map[string][]struct {
			Session   string
			Name      string
			Decisions int
		}
	}
	if err := json.Unmarshal([]byte(runBin(t, bin, env, "data", "--session", "8533")), &data); err != nil {
		t.Fatal(err)
	}
	if data.Session != sid || data.Path != filepath.Join(state, "decision-tree", sid+".json") ||
		data.Tree == nil || data.Tree.Here != "n2" || len(data.Branches["n2"]) != 1 || data.Branches["n2"][0].Name != "try-a-cache" {
		t.Errorf("data = %+v", data)
	}
	var cut struct {
		PromptNumber int `json:"prompt_number"`
		Prompt       string
		Error        string
	}
	if err := json.Unmarshal([]byte(runBin(t, bin, env, "source", "8533", "n2", "--json")), &cut); err != nil ||
		cut.PromptNumber != 1 || cut.Prompt != "Search is slow. Add an index." || cut.Error != "" {
		t.Errorf("source --json = %+v, %v", cut, err)
	}
	var plan struct{ Name, Summary string }
	if err := json.Unmarshal([]byte(runBin(t, bin, env, "branch", sid, "n2", "--plan", "--json")), &plan); err != nil ||
		plan.Name != "fix-add-a-database-index" || !strings.Contains(plan.Summary, "Branch from:  Fix: add a database index") {
		t.Errorf("branch --plan --json = %+v, %v", plan, err)
	}
}

// TestDataWithNoTree: a session that has made no decision yet has no tree
// file. data says where the file will be, so the pane can wait for it.
func TestDataWithNoTree(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	var out, errOut bytes.Buffer
	if code := run([]string{"data", "--session", "s1"}, nil, &out, &errOut); code != 0 {
		t.Fatalf("code %d, stderr %q", code, errOut.String())
	}
	want := `{"session":"s1","path":"` + filepath.Join(state, "decision-tree", "s1.json") + `","tree":null,"branches":{}}` + "\n"
	if out.String() != want {
		t.Errorf("data = %s, want %s", out.String(), want)
	}
	if code := run([]string{"data", "--session", "../x"}, nil, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "bad session id") {
		t.Errorf("a bad id: code %d, stderr %q", code, errOut.String())
	}
}

func TestPrintWithNoTrees(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	var out, errOut bytes.Buffer
	if code := run([]string{"print"}, nil, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "no trees saved yet") {
		t.Fatalf("code %d, stderr %q", code, errOut.String())
	}
	out.Reset()
	if code := run([]string{"list"}, nil, &out, &errOut); code != 0 || !strings.Contains(out.String(), "No trees saved yet") {
		t.Fatalf("code %d, stdout %q", code, out.String())
	}
}

// TestToolCommands is the mod's side of the tools (PRD 18): record and show
// for the session the mod names, and describe with the rules and both tools.
func TestToolCommands(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	cwd := t.TempDir() // not a git repo, so this repo is never snapshotted
	session := []string{"--session", "s1", "--cwd", cwd}
	call := func(stdin string, args ...string) (string, string, int) {
		t.Helper()
		var out, errOut bytes.Buffer
		code := run(args, strings.NewReader(stdin), &out, &errOut)
		return out.String(), errOut.String(), code
	}

	out, errOut, code := call(`{"topic":"Database","options":["SQLite","PostgreSQL"],"picked":"PostgreSQL","reason":"many writers","by":"user"}`,
		append([]string{"record", "--tool-use-id", "toolu_1"}, session...)...)
	if code != 0 || !strings.HasPrefix(out, "Saved as d1. You are here: PostgreSQL (n2).") {
		t.Fatalf("record: code %d, stdout %q, stderr %q", code, out, errOut)
	}
	tr, err := store.Store{Dir: filepath.Join(state, "decision-tree")}.Load("s1")
	if err != nil {
		t.Fatal(err)
	}
	if cp := tr.Node("n2").Checkpoint; cp.ToolUseID != "toolu_1" {
		t.Errorf("the pick's checkpoint should keep the tool use id: %+v", cp)
	}
	if tr.Folder != cwd {
		t.Errorf("folder = %q, want %q", tr.Folder, cwd)
	}

	if out, _, code := call("", append([]string{"show"}, session...)...); code != 0 || !strings.Contains(out, "d1: Database\n    × n1 SQLite\n    ● n2 PostgreSQL ◀") {
		t.Errorf("show: code %d, stdout %q", code, out)
	}

	// Bad calls fail with a message Claude can act on, and change nothing.
	if _, errOut, code := call(`{"topic":"Cache","options":["Redis"],"picked":"","reason":"-","by":"user"}`, append([]string{"record"}, session...)...); code != 1 || !strings.Contains(errOut, "picked is needed") {
		t.Errorf("no pick: code %d, stderr %q", code, errOut)
	}
	if _, errOut, code := call(`{}`, "record"); code != 1 || !strings.Contains(errOut, "--session is needed") {
		t.Errorf("no session: code %d, stderr %q", code, errOut)
	}

	out, _, code = call("", "describe")
	var spec server.Spec
	if err := json.Unmarshal([]byte(out), &spec); err != nil || code != 0 {
		t.Fatalf("describe: code %d, %v\n%s", code, err, out)
	}
	if spec.Instructions != server.Instructions || len(spec.Tools) != 2 ||
		spec.Tools[0].Name != "record_decision" || spec.Tools[1].Name != "show_decision_tree" {
		t.Fatalf("describe = %+v", spec)
	}
	if !strings.Contains(out, `"required": [`) || !strings.Contains(out, `"picked"`) {
		t.Errorf("record_decision's schema should require picked:\n%s", out)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runBin(t *testing.T, bin string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
	return string(out)
}
