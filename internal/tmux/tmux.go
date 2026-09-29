// Package tmux asks tmux which pane you are looking at. The view runs in an
// iTerm2 split outside tmux (like claude-sidebar), so it cannot just ask
// for "the current pane": it asks each tmux client instead.
package tmux

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// Tmux runs tmux, optionally on a socket other than the default.
type Tmux struct {
	Bin    string // path to tmux; "" = "tmux" from PATH
	Socket string // "" = default; contains "/" = -S path; else -L name
}

// FindBin finds tmux even when PATH is short, as it is for programs that
// iTerm2 starts.
func FindBin() string {
	if p, err := exec.LookPath("tmux"); err == nil {
		return p
	}
	for _, p := range []string{"/opt/homebrew/bin/tmux", "/usr/local/bin/tmux", "/usr/bin/tmux"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "tmux"
}

// ActivePane is the pane shown by the tmux client used most recently, like
// "%23". It is "" when tmux is not running or no terminal shows it.
func (t Tmux) ActivePane() (string, error) {
	var pre []string
	switch {
	case t.Socket == "":
	case strings.Contains(t.Socket, "/"):
		pre = []string{"-S", t.Socket}
	default:
		pre = []string{"-L", t.Socket}
	}
	bin := t.Bin
	if bin == "" {
		bin = "tmux"
	}
	var stderr bytes.Buffer
	cmd := exec.Command(bin, append(pre, "list-clients", "-F", "#{client_activity} #{pane_id}")...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := stderr.String()
		if strings.Contains(msg, "no server running") || strings.Contains(msg, "error connecting to") {
			return "", nil
		}
		return "", errors.New("tmux list-clients: " + strings.TrimSpace(msg))
	}
	return newestPane(string(out)), nil
}

// newestPane picks the pane of the client with the newest activity from
// lines like "1790692370 %23".
func newestPane(out string) string {
	best, bestAt := "", int64(-1)
	for _, line := range strings.Split(out, "\n") {
		at, pane, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		n, err := strconv.ParseInt(at, 10, 64)
		if err == nil && n > bestAt && strings.HasPrefix(pane, "%") {
			best, bestAt = pane, n
		}
	}
	return best
}
