package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestEndToEnd runs the real binary as an MCP server over stdin/stdout, the
// way Claude Code does. This test process plays Claude: it is the parent,
// so it writes the session file for its own pid.
func TestEndToEnd(t *testing.T) {
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "decision-tree")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	cfg, state := filepath.Join(tmp, "claude"), filepath.Join(tmp, "state")
	const sid = "8533417c-1b9d-4c3c-a771-f0df5b76dae2"
	writeFile(t, filepath.Join(cfg, "sessions", strconv.Itoa(os.Getpid())+".json"),
		`{"pid":1,"sessionId":"`+sid+`","cwd":"/w/crm","status":"idle"}`)
	writeFile(t, filepath.Join(cfg, "projects", "-w-crm", sid+".jsonl"),
		`{"type":"ai-title","aiTitle":"CRM search is slow","sessionId":"`+sid+`"}`+"\n")
	env := append(os.Environ(), "CLAUDE_CONFIG_DIR="+cfg, "XDG_STATE_HOME="+state, "CLAUDE_CODE_SESSION_ID=stale")

	cmd := exec.Command(bin, "mcp")
	cmd.Env = env
	ctx := context.Background()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "fake-claude", Version: "0"}, nil).
		Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range []map[string]any{
		{"topic": "Fix", "options": []string{"add a cache", "add a database index"}},
		{"decision_id": "d1", "picked": "add a database index", "reason": "fixes the query itself", "by": "both"},
	} {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "record_decision", Arguments: args})
		if err != nil || res.IsError {
			t.Fatalf("record_decision(%v) = %+v, %v", args, res, err)
		}
	}
	cs.Close()

	out := runBin(t, bin, env, "print", "8533")
	for _, want := range []string{
		"crm · session 8533417c · 1 decisions",
		"●  CRM search is slow\n│\n├─○  add a cache\n●  Fix: add a database index  ◀",
		"Why\n  Fix: add a database index — fixes the query itself (both)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("print is missing %q:\n%s", want, out)
		}
	}
	if out := runBin(t, bin, env, "list"); !strings.Contains(out, "8533417c  crm     1          CRM search is slow") {
		t.Errorf("list:\n%s", out)
	}
}

func TestPrintWithNoTrees(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	var out, errOut bytes.Buffer
	if code := run([]string{"print"}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "no trees saved yet") {
		t.Fatalf("code %d, stderr %q", code, errOut.String())
	}
	out.Reset()
	if code := run([]string{"list"}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "No trees saved yet") {
		t.Fatalf("code %d, stdout %q", code, out.String())
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
