// Package claude reads the files Claude Code keeps about its sessions.
// None of these formats are documented; docs/findings.md says what was
// found, and when.
package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Dir is ~/.claude, or $CLAUDE_CONFIG_DIR if set.
func Dir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

// Session is what ~/.claude/sessions/<pid>.json says about a running Claude.
type Session struct {
	PID  int    `json:"pid"`
	ID   string `json:"sessionId"`
	Cwd  string `json:"cwd"`
	Tmux string `json:"tmux"` // "session:@window.%pane"
}

// SessionOf reads the session file of the Claude process pid. The file
// changes when the user runs /clear, so read it fresh each time.
func SessionOf(dir string, pid int) (Session, error) {
	b, err := os.ReadFile(filepath.Join(dir, "sessions", strconv.Itoa(pid)+".json"))
	if err != nil {
		return Session{}, err
	}
	var s Session
	if err := json.Unmarshal(b, &s); err != nil {
		return Session{}, fmt.Errorf("session file for pid %d: %w", pid, err)
	}
	if s.ID == "" {
		return Session{}, fmt.Errorf("session file for pid %d has no sessionId", pid)
	}
	return s, nil
}

// Caller finds the session of the Claude process that started this
// program. Claude Code starts MCP servers directly, so that is our parent.
// If the parent has no session file, CLAUDE_CODE_SESSION_ID is used. It is
// set when the server starts, so it goes stale after /clear, but it is
// better than nothing.
func Caller(dir string) (Session, error) {
	s, err := SessionOf(dir, os.Getppid())
	if err == nil {
		return s, nil
	}
	if id := os.Getenv("CLAUDE_CODE_SESSION_ID"); id != "" {
		return Session{ID: id, Cwd: os.Getenv("CLAUDE_PROJECT_DIR")}, nil
	}
	return Session{}, fmt.Errorf("not started by Claude Code (%v)", err)
}

// ChatPath finds a session's chat file: ~/.claude/projects/<folder>/<id>.jsonl.
func ChatPath(dir, sessionID string) (string, error) {
	matches, _ := filepath.Glob(filepath.Join(dir, "projects", "*", sessionID+".jsonl"))
	if len(matches) == 0 {
		return "", os.ErrNotExist
	}
	return matches[0], nil
}

// TitleMax is the longest title Title returns, in characters.
const TitleMax = 60

// Title names a session, from its chat file. It uses, in order: the name
// the user gave it with /rename, the name Claude Code made up for it, or
// the start of the user's first message. It returns "" if there is none.
func Title(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	var custom, ai, first string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		line := sc.Bytes()
		// Most lines are long messages. Only parse the few that can help.
		switch {
		case contains(line, `"type":"custom-title"`):
			var e struct{ CustomTitle string }
			if json.Unmarshal(line, &e) == nil && e.CustomTitle != "" {
				custom = e.CustomTitle
			}
		case contains(line, `"type":"ai-title"`):
			var e struct{ AiTitle string }
			if json.Unmarshal(line, &e) == nil && e.AiTitle != "" {
				ai = e.AiTitle
			}
		case first == "" && contains(line, `"type":"user"`):
			first = firstPrompt(line)
		}
	}
	if err := sc.Err(); err != nil && !errors.Is(err, bufio.ErrTooLong) {
		return "", err
	}
	for _, t := range []string{custom, ai, first} {
		if t = strings.Join(strings.Fields(t), " "); t != "" {
			return cut(t, TitleMax), nil
		}
	}
	return "", nil
}

// firstPrompt returns the text of a message the user typed, or "" if the
// line is something else: a tool result, a hook note, a command.
func firstPrompt(line []byte) string {
	var e struct {
		Type    string
		IsMeta  bool
		Message struct {
			Role    string
			Content json.RawMessage
		}
	}
	if json.Unmarshal(line, &e) != nil || e.Type != "user" || e.IsMeta || e.Message.Role != "user" {
		return ""
	}
	var text string
	if json.Unmarshal(e.Message.Content, &text) != nil {
		var blocks []struct{ Type, Text string }
		if json.Unmarshal(e.Message.Content, &blocks) != nil {
			return ""
		}
		for _, b := range blocks {
			if b.Type == "text" {
				text = b.Text
				break
			}
		}
	}
	text = strings.TrimSpace(text)
	// System notes, hook output, and slash commands come wrapped in tags.
	if strings.HasPrefix(text, "<") {
		return ""
	}
	return text
}

// cut shortens s to at most max characters, at a word break, with "…".
func cut(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	s = string(r[:max-1])
	if i := strings.LastIndex(s, " "); i > max/2 {
		s = s[:i]
	}
	return strings.TrimRight(s, " ,.;:-") + "…"
}

func contains(b []byte, s string) bool { return bytes.Contains(b, []byte(s)) }
