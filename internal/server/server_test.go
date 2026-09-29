package server

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"decision-tree/internal/claude"
	"decision-tree/internal/store"
)

// fake is a server wired to a temp folder, with a session we can switch,
// like /clear does.
type fake struct {
	srv     *Server
	session claude.Session
	err     error
}

func newFake(t *testing.T) (*fake, *mcp.ClientSession) {
	t.Helper()
	f := &fake{session: claude.Session{ID: "s1", Cwd: "/w/proj"}}
	f.srv = &Server{
		Store:  store.Store{Dir: t.TempDir()},
		Caller: func() (claude.Session, error) { return f.session, f.err },
		Title:  func(id string) string { return "title of " + id },
		Now:    func() time.Time { return time.Date(2026, 9, 29, 15, 0, 0, 0, time.UTC) },
	}
	ctx := context.Background()
	ct, st := mcp.NewInMemoryTransports()
	ss, err := f.srv.MCP("test").Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return f, cs
}

// call runs a tool and returns its text and whether it was an error.
func call(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	var parts []string
	for _, c := range res.Content {
		parts = append(parts, c.(*mcp.TextContent).Text)
	}
	return strings.Join(parts, "\n"), res.IsError
}

func TestTextFitsClaudeCodeLimit(t *testing.T) {
	// Claude Code cuts these at 2,048 characters (docs/findings.md, part 2).
	for name, s := range map[string]string{"instructions": Instructions, "record_decision": recordDescription, "show_decision_tree": showDescription} {
		if n := len([]rune(s)); n > 2048 {
			t.Errorf("%s is %d characters; Claude Code cuts at 2,048", name, n)
		}
	}
}

func TestInitializeAndListTools(t *testing.T) {
	_, cs := newFake(t)
	if got := cs.InitializeResult().Instructions; got != Instructions {
		t.Fatalf("instructions not sent: %q", got)
	}
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
		if tool.Meta["anthropic/alwaysLoad"] != true {
			t.Errorf("%s is missing anthropic/alwaysLoad", tool.Name)
		}
		if tool.Name == "record_decision" {
			b, _ := json.Marshal(tool.InputSchema)
			var schema struct {
				Required   []string
				Properties map[string]struct{ Enum []string }
			}
			json.Unmarshal(b, &schema)
			if len(schema.Required) != 0 {
				t.Errorf("no field should be required by the schema, got %v", schema.Required)
			}
			if !slices.Equal(schema.Properties["by"].Enum, []string{"user", "claude", "both"}) {
				t.Errorf("by enum = %v", schema.Properties["by"].Enum)
			}
		}
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"record_decision", "show_decision_tree"}) {
		t.Fatalf("tools = %v", names)
	}
}

func TestRecordAndShow(t *testing.T) {
	f, cs := newFake(t)
	if got, _ := call(t, cs, "show_decision_tree", map[string]any{}); !strings.Contains(got, "No decisions yet") {
		t.Fatalf("empty show = %q", got)
	}

	got, isErr := call(t, cs, "record_decision", map[string]any{
		"question": "Which database?", "options": []string{"Postgres", "SQLite"},
	})
	if isErr || got != `Saved as d1. You are here: title of s1 (n0). Still open: d1 "Which database?".` {
		t.Fatalf("record = %q (error: %v)", got, isErr)
	}
	got, _ = call(t, cs, "record_decision", map[string]any{
		"decision_id": "d1", "picked": "Postgres", "reason": "many writers", "by": "user",
	})
	if got != "Saved as d1. You are here: Postgres (n1). Still open: none." {
		t.Fatalf("pick = %q", got)
	}

	got, _ = call(t, cs, "show_decision_tree", map[string]any{})
	want := `● n0 title of s1
  d1: Which database?
    ● n1 Postgres ◀
    ○ n2 SQLite`
	if !strings.HasSuffix(got, want) {
		t.Fatalf("show =\n%s\n\nwant it to end with\n%s", got, want)
	}

	tr, err := f.srv.Store.Load("s1")
	if err != nil {
		t.Fatal(err)
	}
	if tr.Folder != "/w/proj" || tr.Root().Label != "title of s1" {
		t.Fatalf("tree folder = %q, start = %q", tr.Folder, tr.Root().Label)
	}
}

// The live test on 29 Sep: after going back, Claude was told "Still open:
// none", thought the front-end question was gone, and asked it again.
func TestGoingBackNamesQuestionsLeftBehind(t *testing.T) {
	_, cs := newFake(t)
	for _, args := range []map[string]any{
		{"question": "Which database?", "options": []string{"SQLite", "Postgres"}, "picked": "SQLite", "reason": "one user", "by": "user"},
		{"question": "Which front end?", "options": []string{"HTML", "React"}},
	} {
		call(t, cs, "record_decision", args)
	}
	got, _ := call(t, cs, "record_decision", map[string]any{"decision_id": "d1", "picked": "Postgres", "reason": "Vercel", "by": "user"})
	if !strings.Contains(got, `Left behind on the dropped branch, still unanswered: d2 "Which front end?".`) {
		t.Fatalf("reply does not name the question left behind:\n%s", got)
	}
	// Answering it with its id brings it to "you are here".
	got, _ = call(t, cs, "record_decision", map[string]any{"decision_id": "d2", "options": []string{"HTML + htmx"}, "picked": "HTML + htmx", "reason": "less code", "by": "user"})
	if !strings.HasPrefix(got, "Saved as d2. You are here: HTML + htmx") || strings.Contains(got, "Left behind") {
		t.Fatalf("got %q", got)
	}
}

func TestBadCallIsAToolError(t *testing.T) {
	_, cs := newFake(t)
	got, isErr := call(t, cs, "record_decision", map[string]any{"options": []string{"a"}})
	if !isErr || !strings.Contains(got, "question is needed") {
		t.Fatalf("got %q (error: %v)", got, isErr)
	}
	got, isErr = call(t, cs, "record_decision", map[string]any{"question": "Q", "options": []string{"a"}, "picked": "a", "reason": "r", "by": "me"})
	if !isErr {
		t.Fatalf("by=me was accepted: %q", got)
	}
}

func TestClearStartsANewTree(t *testing.T) {
	f, cs := newFake(t)
	call(t, cs, "record_decision", map[string]any{"question": "Q1", "options": []string{"a", "b"}})
	f.session.ID = "s2" // what /clear does; the server keeps running
	got, _ := call(t, cs, "record_decision", map[string]any{"question": "Q2", "options": []string{"c"}})
	if !strings.HasPrefix(got, "Saved as d1.") {
		t.Fatalf("after /clear, got %q; want a fresh tree starting at d1", got)
	}
	for id, want := range map[string]string{"s1": "Q1", "s2": "Q2"} {
		tr, err := f.srv.Store.Load(id)
		if err != nil || len(tr.Decisions) != 1 || tr.Decisions[0].Question != want {
			t.Fatalf("tree %s = %+v, %v", id, tr, err)
		}
	}
}

func TestUnknownSession(t *testing.T) {
	f, cs := newFake(t)
	f.err = errors.New("no session file")
	got, isErr := call(t, cs, "record_decision", map[string]any{"question": "Q", "options": []string{"a"}})
	if !isErr || !strings.Contains(got, "cannot tell which session") {
		t.Fatalf("got %q (error: %v)", got, isErr)
	}
}
