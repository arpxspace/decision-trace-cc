package claude

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// Cut is where a branch starts: right after Claude logged a decision
// (PRD 17.2). Resuming at UUID keeps the chat up to the tool's answer to
// the record_decision call, and drops everything after it, including the
// rest of that turn. That matches the code snapshot, which was taken during
// the same call.
type Cut struct {
	UUID         string    // the chat entry to resume at: the tool result of the call
	At           time.Time // when the decision was logged
	PromptNumber int       // which of the user's typed messages it came after (1 = the first)
	Prompt       string    // that message
	Before       string    // what Claude said last before logging it
}

// Reasons FindCut can fail.
var (
	ErrCallNotFound   = errors.New("the decision's tool call is not in this chat (it may have come from a sub-agent)")
	ErrResultNotFound = errors.New("the decision's tool call has no result in the chat yet")
)

// One entry of a chat file, only the parts FindCut needs. The format is
// Claude Code's own and not documented; docs/findings.md part 9 shows a
// real example.
type entry struct {
	Type        string    `json:"type"`
	UUID        string    `json:"uuid"`
	Parent      string    `json:"parentUuid"`
	IsMeta      bool      `json:"isMeta"`
	IsSidechain bool      `json:"isSidechain"`
	IsSummary   bool      `json:"isCompactSummary"` // the summary /compact writes in the user's place
	Timestamp   time.Time `json:"timestamp"`
	Message     struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type block struct {
	Type      string `json:"type"`
	Text      string `json:"text"`
	ID        string `json:"id"`          // tool_use
	ToolUseID string `json:"tool_use_id"` // tool_result
}

// What an entry is, for walking back up the chat.
type step struct {
	parent string
	prompt string // a message the user typed
	said   string // text Claude wrote
	at     time.Time
}

// FindCut finds the cut for the record_decision call toolUseID in the chat
// file at path.
func FindCut(path, toolUseID string) (Cut, error) {
	f, err := os.Open(path)
	if err != nil {
		return Cut{}, err
	}
	defer f.Close()

	steps := map[string]step{}
	var call, result string
	callSidechain := false
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			var e entry
			if json.Unmarshal(line, &e) == nil && e.UUID != "" {
				s := step{parent: e.Parent, at: e.Timestamp}
				switch e.Type {
				case "user":
					blocks := content(e.Message.Content)
					for _, b := range blocks {
						if b.Type == "tool_result" && b.ToolUseID == toolUseID {
							result = e.UUID
						}
					}
					if !e.IsMeta && !e.IsSummary {
						s.prompt = typed(blocks)
					}
				case "assistant":
					for _, b := range content(e.Message.Content) {
						switch {
						case b.Type == "tool_use" && b.ID == toolUseID:
							call, callSidechain = e.UUID, e.IsSidechain
						case b.Type == "text":
							s.said = strings.TrimSpace(b.Text)
						}
					}
				}
				steps[e.UUID] = s
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return Cut{}, err
		}
	}
	if call == "" || callSidechain {
		return Cut{}, ErrCallNotFound
	}
	if result == "" {
		return Cut{}, ErrResultNotFound
	}

	cut := Cut{UUID: result, At: steps[result].at}
	// Walk back from the call to the start of the chat. Claude's text before
	// the call counts until the prompt is found, and the text of the turn
	// before that counts if this turn had none.
	seen := map[string]bool{}
	for id := steps[call].parent; id != "" && !seen[id]; id = steps[id].parent {
		seen[id] = true
		s, ok := steps[id]
		if !ok {
			break // the chain runs into entries that are not in this file
		}
		switch {
		case s.prompt != "":
			if cut.Prompt == "" {
				cut.Prompt = s.prompt
			}
			cut.PromptNumber++
		case s.said != "" && cut.Before == "" && cut.PromptNumber <= 1:
			cut.Before = s.said
		}
	}
	if cut.Prompt == "" {
		return cut, fmt.Errorf("no message from the user comes before the decision")
	}
	return cut, nil
}

// content reads message content, which is either a string (a typed
// message) or a list of blocks.
func content(raw json.RawMessage) []block {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []block{{Type: "text", Text: s}}
	}
	var blocks []block
	json.Unmarshal(raw, &blocks)
	return blocks
}

// typed is the text of a message the user typed, or "". Tool results, hook
// notes, and slash commands (wrapped in tags) are not typed messages.
func typed(blocks []block) string {
	for _, b := range blocks {
		if b.Type == "tool_result" {
			return ""
		}
	}
	for _, b := range blocks {
		if t := strings.TrimSpace(b.Text); b.Type == "text" && t != "" && !strings.HasPrefix(t, "<") {
			return t
		}
	}
	return ""
}
