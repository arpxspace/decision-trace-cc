package claude

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// chat builds a chat file shaped like a real one (docs/findings.md part 9):
// every entry points at the one before it.
type chat struct {
	lines []string
	last  string
	n     int
}

func (c *chat) add(typ string, fields map[string]any) string {
	c.n++
	id := fmt.Sprintf("u%02d", c.n)
	e := map[string]any{"type": typ, "uuid": id, "parentUuid": c.last, "isSidechain": false,
		"timestamp": fmt.Sprintf("2026-09-29T14:%02d:00Z", c.n)}
	for k, v := range fields {
		e[k] = v
	}
	b, _ := json.Marshal(e)
	c.lines = append(c.lines, string(b))
	c.last = id
	return id
}

func msg(content any) map[string]any {
	return map[string]any{"message": map[string]any{"content": content}}
}

func (c *chat) prompt(text string) string { return c.add("user", msg(text)) }
func (c *chat) text(text string) string {
	return c.add("assistant", msg([]any{map[string]any{"type": "text", "text": text}}))
}
func (c *chat) think() string {
	return c.add("assistant", msg([]any{map[string]any{"type": "thinking", "thinking": "hmm"}}))
}
func (c *chat) attach() string { return c.add("attachment", nil) }
func (c *chat) call(id string) string {
	return c.add("assistant", msg([]any{map[string]any{"type": "tool_use", "id": id, "name": "mcp__decision-tree__record_decision"}}))
}
func (c *chat) result(id string) string {
	return c.add("user", msg([]any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": "Saved as d1."}}))
}

func (c *chat) save(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(c.lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// The spec's example (PRD 17, spec section 6): Build an API, "Use FastAPI",
// Yes, then later Redis.
func example() (c *chat, cutA, cutB string) {
	c = &chat{}
	c.add("user", map[string]any{"isMeta": true, "message": map[string]any{"content": "<system-reminder>SessionStart hook</system-reminder>"}})
	c.prompt("Build an API")
	c.attach()
	c.think()
	c.text("FastAPI or Flask? I would pick FastAPI.")
	c.prompt("Yes")
	c.attach()
	c.think()
	c.call("toolu_A")
	c.attach()
	cutA = c.result("toolu_A")
	c.text("Now implementing FastAPI.")
	c.add("assistant", msg([]any{map[string]any{"type": "tool_use", "id": "toolu_W", "name": "Write"}}))
	c.result("toolu_W")
	c.text(strings.Repeat("a very long reply ", 20000)) // a line far over 64 KB
	c.prompt("Add Redis for caching")
	c.text("Logging that.")
	c.call("toolu_B")
	cutB = c.result("toolu_B")
	c.prompt("<command-name>/effort</command-name>")
	return c, cutA, cutB
}

func TestFindCut(t *testing.T) {
	c, cutA, cutB := example()
	path := c.save(t)

	got, err := FindCut(path, "toolu_A")
	if err != nil {
		t.Fatal(err)
	}
	// Cut after the tool's answer, so the branch keeps "Yes" and the
	// decision, but not "Now implementing FastAPI." or anything later.
	if got.UUID != cutA || got.Prompt != "Yes" || got.PromptNumber != 2 {
		t.Fatalf("cut A = %+v, want uuid %s after prompt 2 \"Yes\"", got, cutA)
	}
	// Nothing was said in the turn before the call, so "before" is the end
	// of the previous turn: the recommendation the user agreed to.
	if got.Before != "FastAPI or Flask? I would pick FastAPI." {
		t.Fatalf("before = %q", got.Before)
	}
	if got.At.IsZero() {
		t.Fatal("no time on the cut")
	}

	got, err = FindCut(path, "toolu_B")
	if err != nil {
		t.Fatal(err)
	}
	if got.UUID != cutB || got.Prompt != "Add Redis for caching" || got.PromptNumber != 3 || got.Before != "Logging that." {
		t.Fatalf("cut B = %+v", got)
	}
}

func TestFindCutProblems(t *testing.T) {
	c, _, _ := example()
	c.call("toolu_NoResult")
	c.add("assistant", map[string]any{"isSidechain": true, "message": map[string]any{"content": []any{
		map[string]any{"type": "tool_use", "id": "toolu_Side", "name": "mcp__decision-tree__record_decision"}}}})
	path := c.save(t)

	if _, err := FindCut(path, "toolu_Nope"); !errors.Is(err, ErrCallNotFound) {
		t.Errorf("unknown call: %v", err)
	}
	if _, err := FindCut(path, "toolu_NoResult"); !errors.Is(err, ErrResultNotFound) {
		t.Errorf("call without a result: %v", err)
	}
	if _, err := FindCut(path, "toolu_Side"); !errors.Is(err, ErrCallNotFound) {
		t.Errorf("sub-agent call: %v", err)
	}
	if _, err := FindCut(filepath.Join(t.TempDir(), "none.jsonl"), "toolu_A"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing file: %v", err)
	}
}

func TestCompactSummaryIsNotATypedMessage(t *testing.T) {
	c := &chat{}
	c.prompt("Build an API")
	c.add("user", map[string]any{"isCompactSummary": true, "message": map[string]any{"content": "This session is being continued from a previous conversation."}})
	c.call("toolu_A")
	c.result("toolu_A")
	got, err := FindCut(c.save(t), "toolu_A")
	if err != nil || got.Prompt != "Build an API" || got.PromptNumber != 1 {
		t.Fatalf("cut = %+v, %v", got, err)
	}
}
