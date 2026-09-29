package tmux

import (
	"os/exec"
	"strings"
	"testing"
)

func TestShellQuote(t *testing.T) {
	got := ShellQuote([]string{"claude", "--resume", "abc", "-n", "it's a name"})
	want := `'claude' '--resume' 'abc' '-n' 'it'\''s a name'`
	if got != want {
		t.Fatalf("got %s\nwant %s", got, want)
	}
	// The shell reads it back as the same words.
	out, err := exec.Command("sh", "-c", "printf '%s|' "+got).Output()
	if err != nil || string(out) != "claude|--resume|abc|-n|it's a name|" {
		t.Fatalf("sh read %q, %v", out, err)
	}
}

func TestNoServerIsNotAnError(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	// A private socket nobody started: the same as tmux not running.
	tm := Tmux{Socket: "decision-tree-test-nobody"}
	if pane, err := tm.ActivePane(); pane != "" || err != nil {
		t.Fatalf("ActivePane = %q, %v; want \"\", nil", pane, err)
	}
	if s, err := tm.ActiveSession(); s != "" || err != nil {
		t.Fatalf("ActiveSession = %q, %v", s, err)
	}
}

// A private tmux server, so the real one is never touched.
func private(t *testing.T) Tmux {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	tm := Tmux{Socket: "decision-tree-test-" + strings.ReplaceAll(t.Name(), "/", "-")}
	if out, err := exec.Command("tmux", "-L", tm.Socket, "-f", "/dev/null", "new-session", "-d", "-s", "work", "sleep 60").CombinedOutput(); err != nil {
		t.Fatalf("start tmux: %v %s", err, out)
	}
	t.Cleanup(func() { exec.Command("tmux", "-L", tm.Socket, "kill-server").Run() })
	return tm
}

func TestNewWindow(t *testing.T) {
	tm := private(t)
	dir := t.TempDir()
	pane, err := tm.NewWindow("work", dir, "my-branch", []string{"sleep", "60"}, false)
	if err != nil || !strings.HasPrefix(pane, "%") {
		t.Fatalf("pane %q, err %v", pane, err)
	}
	out, _ := tm.run("list-panes", "-a", "-F", "#{pane_id} #{window_name} #{pane_current_path} #{window_active}")
	var line string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, pane+" ") {
			line = l
		}
	}
	// macOS temp folders live under /private; tmux may report either form.
	if !strings.Contains(line, " my-branch ") || !strings.Contains(line, strings.TrimPrefix(dir, "/private")) {
		t.Fatalf("new pane: %q (all: %s)", line, out)
	}
	if !strings.HasSuffix(line, " 0") {
		t.Fatalf("a background window should not become the active one: %q", line)
	}
	// No terminal shows this private server, so there is nobody to focus.
	if err := tm.Focus(pane); err == nil {
		t.Fatal("Focus with no client should fail")
	}
}
