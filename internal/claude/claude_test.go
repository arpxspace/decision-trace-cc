package claude

import (
	"os"
	"path/filepath"
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

func TestReadAll(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "sessions", "41.json"), `{"pid":41,"sessionId":"a","cwd":"/w","tmux":"main:@1.%5"}`)
	write(t, filepath.Join(dir, "sessions", "42.json"), `{"pid":42}`) // half written: no sessionId
	write(t, filepath.Join(dir, "sessions", "123.abc.key"), "{}")
	all := ReadAll(dir)
	if len(all) != 1 || all[0].ID != "a" || all[0].PID != 41 || all[0].Tmux != "main:@1.%5" {
		t.Fatalf("ReadAll = %+v", all)
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
