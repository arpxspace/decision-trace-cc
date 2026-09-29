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
	c, err := t.newestClient()
	return c.pane, err
}

// ActiveSession is the tmux session shown by the client used most recently,
// or "" when there is none.
func (t Tmux) ActiveSession() (string, error) {
	c, err := t.newestClient()
	return c.session, err
}

// NewWindow opens a window in tmux session target, in folder dir, named
// name, running args. It returns the new pane's id. With focus false, the
// window opens in the background.
func (t Tmux) NewWindow(target, dir, name string, args []string, focus bool) (string, error) {
	cmd := []string{"new-window"}
	if !focus {
		cmd = append(cmd, "-d")
	}
	cmd = append(cmd, "-t", target+":", "-c", dir, "-n", name, "-P", "-F", "#{pane_id}", ShellQuote(args))
	return t.run(cmd...)
}

// Focus points the tmux client used most recently at pane: its session,
// its window, and the pane itself.
func (t Tmux) Focus(pane string) error {
	c, err := t.newestClient()
	if err != nil {
		return err
	}
	if c.tty == "" {
		return errors.New("no terminal is showing tmux")
	}
	_, err = t.run("switch-client", "-c", c.tty, "-t", pane, ";", "select-window", "-t", pane, ";", "select-pane", "-t", pane)
	return err
}

type client struct{ tty, session, pane string }

// newestClient is the client with the newest activity (the last key press
// or output). A zero client means tmux is not running or nobody is attached.
func (t Tmux) newestClient() (client, error) {
	out, err := t.run("list-clients", "-F", "#{client_activity}\t#{client_tty}\t#{client_session}\t#{pane_id}")
	if errors.Is(err, ErrNoServer) {
		return client{}, nil
	}
	if err != nil {
		return client{}, err
	}
	var best client
	bestAt := int64(-1)
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 4 {
			continue
		}
		if at, err := strconv.ParseInt(f[0], 10, 64); err == nil && at > bestAt {
			best, bestAt = client{tty: f[1], session: f[2], pane: f[3]}, at
		}
	}
	return best, nil
}

// ErrNoServer means tmux is not running.
var ErrNoServer = errors.New("tmux is not running")

func (t Tmux) run(args ...string) (string, error) {
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
	cmd := exec.Command(bin, append(pre, args...)...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if strings.Contains(msg, "no server running") || strings.Contains(msg, "error connecting to") {
			return "", ErrNoServer
		}
		return "", errors.New("tmux " + args[0] + ": " + msg)
	}
	return strings.TrimSpace(string(out)), nil
}

// ShellQuote joins args into one shell command, each quoted, as tmux wants
// a command.
func ShellQuote(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(q, " ")
}
