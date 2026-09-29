package claude

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCallerReadsParentSessionFile(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "sessions", strconv.Itoa(os.Getppid())+".json"),
		`{"pid":1,"sessionId":"8533417c","cwd":"/w/proj","tmux":"probe:@0.%0","status":"idle"}`)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "stale-id")
	s, err := Caller(dir)
	if err != nil {
		t.Fatal(err)
	}
	// The file wins over the variable, which goes stale after /clear.
	if s.ID != "8533417c" || s.Cwd != "/w/proj" || s.Tmux != "probe:@0.%0" {
		t.Fatalf("session = %+v", s)
	}
}

func TestCallerFallsBackToVariable(t *testing.T) {
	t.Setenv("CLAUDE_CODE_SESSION_ID", "from-env")
	t.Setenv("CLAUDE_PROJECT_DIR", "/w/proj")
	s, err := Caller(t.TempDir())
	if err != nil || s.ID != "from-env" || s.Cwd != "/w/proj" {
		t.Fatalf("session = %+v, err = %v", s, err)
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	if _, err := Caller(t.TempDir()); err == nil {
		t.Fatal("no file and no variable should be an error")
	}
}

func TestSessionFileWithoutID(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "sessions", "42.json"), `{"pid":42}`)
	if _, err := SessionOf(dir, 42); err == nil {
		t.Fatal("a file without sessionId was accepted")
	}
}

func TestChatPath(t *testing.T) {
	dir := t.TempDir()
	want := filepath.Join(dir, "projects", "-w-proj", "abc.jsonl")
	write(t, want, "")
	if got, err := ChatPath(dir, "abc"); err != nil || got != want {
		t.Fatalf("ChatPath = %q, %v", got, err)
	}
	if _, err := ChatPath(dir, "nope"); err == nil {
		t.Fatal("found a chat that does not exist")
	}
}

// Lines as Claude Code writes them (trimmed).
const (
	hookNote   = `{"type":"user","isMeta":true,"message":{"role":"user","content":"<system-reminder>SessionStart hook</system-reminder>"}}`
	command    = `{"type":"user","message":{"role":"user","content":"<command-name>/effort</command-name>"}}`
	toolResult = `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"ok"}]}}`
	prompt     = `{"type":"user","message":{"role":"user","content":"so recently we build   claude-sidebar which acts like a sidepanel showing live claude code sessions"}}`
	blocks     = `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"a prompt in blocks"}]}}`
	aiTitle    = `{"type":"ai-title","aiTitle":"Decision tree visualizer TUI","sessionId":"s"}`
	aiTitle2   = `{"type":"ai-title","aiTitle":"Decision tree TUI","sessionId":"s"}`
	custom     = `{"type":"custom-title","customTitle":"my name","sessionId":"s"}`
)

func TestTitle(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		want  string
	}{
		{"custom title wins", []string{hookNote, prompt, aiTitle, custom, aiTitle2}, "my name"},
		{"newest ai title", []string{hookNote, prompt, aiTitle, aiTitle2}, "Decision tree TUI"},
		{"first typed prompt, cut short", []string{hookNote, command, toolResult, prompt}, "so recently we build claude-sidebar which acts like a…"},
		{"prompt in blocks", []string{blocks, prompt}, "a prompt in blocks"},
		{"nothing typed yet", []string{hookNote, command}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "s.jsonl")
			write(t, path, strings.Join(c.lines, "\n")+"\n")
			got, err := Title(path)
			if err != nil || got != c.want {
				t.Fatalf("Title = %q, %v; want %q", got, err, c.want)
			}
			if len([]rune(got)) > TitleMax {
				t.Fatalf("title is %d characters, max %d", len([]rune(got)), TitleMax)
			}
		})
	}
}
