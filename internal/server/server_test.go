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
	"decision-tree/internal/graph"
	"decision-tree/internal/store"
	"decision-tree/internal/tree"
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
			// A decision is only logged once made, so these are always needed.
			slices.Sort(schema.Required)
			if !slices.Equal(schema.Required, []string{"by", "picked", "reason"}) {
				t.Errorf("required = %v, want by, picked, reason", schema.Required)
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

// ok runs a record_decision call that must work, and returns the reply.
func ok(t *testing.T, cs *mcp.ClientSession, args map[string]any) string {
	t.Helper()
	got, isErr := call(t, cs, "record_decision", args)
	if isErr {
		t.Fatalf("record_decision(%v) failed: %s", args, got)
	}
	return got
}

// graphOf is the tree for session s1, drawn the way `print` draws it.
func graphOf(t *testing.T, f *fake) string {
	t.Helper()
	tr, err := f.srv.Store.Load("s1")
	if err != nil {
		t.Fatal(err)
	}
	return graph.Plain(graph.Layout(tr, nil, nil))
}

func wantGraph(t *testing.T, f *fake, picture string) {
	t.Helper()
	if got, exp := graphOf(t, f), strings.Trim(picture, "\n"); got != exp {
		t.Fatalf("tree is\n%s\n\nwant\n%s", got, exp)
	}
}

// Amir's acceptance tests (29 Sep 2026). Each gives the call the rules
// tell Claude to make for that conversation, and the tree that must result.
// Whether Claude really makes that call is checked live; see
// docs/findings.md.

// Scenario 1. User: "Use PostgreSQL rather than SQLite."
func TestScenario1SimpleDecision(t *testing.T) {
	f, cs := newFake(t)
	ok(t, cs, map[string]any{"topic": "Database", "options": []string{"SQLite", "PostgreSQL"}, "picked": "PostgreSQL", "reason": "the user asked for it", "by": "user"})
	wantGraph(t, f, `
●  title of s1
│
├─×  SQLite
●  Database: PostgreSQL  ◀`)
}

// Scenario 2. Claude: "I think FastAPI is the better choice here." User: "Agreed."
func TestScenario2RecommendationAccepted(t *testing.T) {
	f, cs := newFake(t)
	ok(t, cs, map[string]any{"topic": "API framework", "options": []string{"FastAPI"}, "picked": "FastAPI", "reason": "Claude recommended it and the user agreed", "by": "both"})
	wantGraph(t, f, `
●  title of s1
│
●  API framework: FastAPI  ◀`)
	tr, _ := f.srv.Store.Load("s1")
	if n := tr.Node(tr.Here); n.By != "both" {
		t.Fatalf("source = %q, want both (jointly agreed)", n.By)
	}
}

// Scenario 3. Claude: "Let's add Redis." User: "No. Keep the architecture
// simple and use Postgres." The suggestion was never a decision, so Redis
// shows as rejected, not as changed.
func TestScenario3RecommendationRejected(t *testing.T) {
	f, cs := newFake(t)
	ok(t, cs, map[string]any{"topic": "State storage", "options": []string{"Redis", "PostgreSQL"}, "picked": "PostgreSQL", "reason": "keep the architecture simple", "by": "user"})
	wantGraph(t, f, `
●  title of s1
│
├─×  Redis
●  State storage: PostgreSQL  ◀`)
}

// Scenario 4. Earlier: REST. Later, user: "Actually change this to GraphQL."
func TestScenario4DecisionReversal(t *testing.T) {
	f, cs := newFake(t)
	ok(t, cs, map[string]any{"topic": "API", "options": []string{"REST"}, "picked": "REST", "reason": "simple", "by": "both"})
	got := ok(t, cs, map[string]any{"decision_id": "d1", "options": []string{"GraphQL"}, "picked": "GraphQL", "reason": "the user changed it", "by": "user"})
	if got != "Saved as d1. You are here: GraphQL (n2). Still open: none." {
		t.Fatalf("reply = %q", got)
	}
	wantGraph(t, f, `
●  title of s1
│
├─↺  REST
●  API: GraphQL  ◀`)
}

// Scenario 5. Claude: "Redis, Postgres, or an in-memory cache are all
// possible." Nothing is picked, so there is nothing to log, and the tool
// refuses a call without a pick.
func TestScenario5DiscussionWithoutDecision(t *testing.T) {
	f, cs := newFake(t)
	for _, args := range []map[string]any{
		{"topic": "Cache", "options": []string{"Redis", "Postgres", "in memory"}},
		{"topic": "Cache", "options": []string{"Redis", "Postgres", "in memory"}, "picked": " ", "reason": "r", "by": "claude"},
	} {
		if got, isErr := call(t, cs, "record_decision", args); !isErr {
			t.Fatalf("a call without a pick was accepted: %q", got)
		}
	}
	if _, err := f.srv.Store.Load("s1"); err == nil {
		t.Fatal("a tree was saved, but no decision was made")
	}
}

func TestChangeInPlaceKeepsLaterDecisions(t *testing.T) {
	f, cs := newFake(t)
	ok(t, cs, map[string]any{"topic": "API", "options": []string{"REST"}, "picked": "REST", "reason": "simple", "by": "both"})
	ok(t, cs, map[string]any{"topic": "Auth", "options": []string{"JWT", "sessions"}, "picked": "JWT", "reason": "stateless", "by": "claude"})
	ok(t, cs, map[string]any{"decision_id": "d1", "options": []string{"GraphQL"}, "picked": "GraphQL", "reason": "the user changed it", "by": "user"})
	wantGraph(t, f, `
●  title of s1
│
├─↺  REST
●  API: GraphQL
│
├─×  sessions
●  Auth: JWT  ◀`)
}

func TestDropLaterSetsLaterDecisionsAside(t *testing.T) {
	f, cs := newFake(t)
	ok(t, cs, map[string]any{"topic": "Speed fix", "options": []string{"Index", "Cache"}, "picked": "Index", "reason": "root cause", "by": "both"})
	ok(t, cs, map[string]any{"topic": "Index on", "options": []string{"email", "date"}, "picked": "date", "reason": "matches the search", "by": "claude"})
	ok(t, cs, map[string]any{"decision_id": "d1", "picked": "Cache", "reason": "the index did not help", "by": "user", "drop_later": true})
	wantGraph(t, f, `
●  title of s1
│
├─╮
│ ↺  Speed fix: Index
│ │
│ ├─×  email
│ ●  Index on: date
●  Speed fix: Cache  ◀`)
}

func TestRecordAndShow(t *testing.T) {
	f, cs := newFake(t)
	if got, _ := call(t, cs, "show_decision_tree", map[string]any{}); !strings.Contains(got, "No decisions yet") {
		t.Fatalf("empty show = %q", got)
	}
	got := ok(t, cs, map[string]any{"topic": "Database", "options": []string{"Postgres", "SQLite"}, "picked": "Postgres", "reason": "many writers", "by": "user"})
	if got != "Saved as d1. You are here: Postgres (n1). Still open: none." {
		t.Fatalf("record = %q", got)
	}
	got, _ = call(t, cs, "show_decision_tree", map[string]any{})
	want := `● n0 title of s1
  d1: Database
    ● n1 Postgres ◀
    × n2 SQLite`
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
// none", thought the front-end question was gone, and asked it again. Open
// questions can only come from trees saved before picks were required, so
// this one is seeded straight into the store.
func TestSettingAsideNamesQuestionsLeftBehind(t *testing.T) {
	f, cs := newFake(t)
	_, err := f.srv.Store.Update("s1", func(tr *tree.Tree) error {
		for _, c := range []tree.Call{
			{Topic: "Database", Options: []string{"SQLite", "Postgres"}, Picked: "SQLite", Reason: "one user", By: "user"},
			{Topic: "Front end", Options: []string{"HTML", "React"}},
		} {
			if _, err := tr.Record(c); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got := ok(t, cs, map[string]any{"decision_id": "d1", "picked": "Postgres", "reason": "Vercel", "by": "user", "drop_later": true})
	if !strings.Contains(got, `Left behind on the dropped branch, still unanswered: d2 "Front end".`) {
		t.Fatalf("reply does not name the question left behind:\n%s", got)
	}
	got = ok(t, cs, map[string]any{"decision_id": "d2", "options": []string{"HTML + htmx"}, "picked": "HTML + htmx", "reason": "less code", "by": "user"})
	if !strings.HasPrefix(got, "Saved as d2. You are here: HTML + htmx") || strings.Contains(got, "Left behind") {
		t.Fatalf("got %q", got)
	}
}

func TestBadCallIsAToolError(t *testing.T) {
	_, cs := newFake(t)
	got, isErr := call(t, cs, "record_decision", map[string]any{"options": []string{"a"}, "picked": "a", "reason": "r", "by": "user"})
	if !isErr || !strings.Contains(got, "topic is needed") {
		t.Fatalf("got %q (error: %v)", got, isErr)
	}
	got, isErr = call(t, cs, "record_decision", map[string]any{"topic": "Q", "options": []string{"a"}, "picked": "a", "reason": "r", "by": "me"})
	if !isErr {
		t.Fatalf("by=me was accepted: %q", got)
	}
}

func TestClearStartsANewTree(t *testing.T) {
	f, cs := newFake(t)
	ok(t, cs, map[string]any{"topic": "Q1", "options": []string{"a", "b"}, "picked": "a", "reason": "r", "by": "user"})
	f.session.ID = "s2" // what /clear does; the server keeps running
	got := ok(t, cs, map[string]any{"topic": "Q2", "options": []string{"c"}, "picked": "c", "reason": "r", "by": "user"})
	if !strings.HasPrefix(got, "Saved as d1.") {
		t.Fatalf("after /clear, got %q; want a fresh tree starting at d1", got)
	}
	for id, want := range map[string]string{"s1": "Q1", "s2": "Q2"} {
		tr, err := f.srv.Store.Load(id)
		if err != nil || len(tr.Decisions) != 1 || tr.Decisions[0].Topic != want {
			t.Fatalf("tree %s = %+v, %v", id, tr, err)
		}
	}
}

func TestUnknownSession(t *testing.T) {
	f, cs := newFake(t)
	f.err = errors.New("no session file")
	got, isErr := call(t, cs, "record_decision", map[string]any{"topic": "Q", "options": []string{"a"}, "picked": "a", "reason": "r", "by": "user"})
	if !isErr || !strings.Contains(got, "cannot tell which session") {
		t.Fatalf("got %q (error: %v)", got, isErr)
	}
}

// PRD 17.1: every new pick gets a checkpoint, with the tool-use id Claude
// Code sends in _meta. The same pick again (only a new reason) does not.
func TestCheckpointOnNewPicks(t *testing.T) {
	f, cs := newFake(t)
	var calls []string
	f.srv.Checkpoint = func(sess claude.Session, t *tree.Tree, node, toolUseID string, at time.Time) {
		calls = append(calls, sess.ID+" "+node+" "+toolUseID)
		t.Node(node).Checkpoint = tree.Checkpoint{ToolUseID: toolUseID, Commit: "abc123", At: at}
	}
	record := func(id string, args map[string]any) {
		t.Helper()
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "record_decision", Arguments: args, Meta: mcp.Meta{"claudecode/toolUseId": id},
		})
		if err != nil || res.IsError {
			t.Fatalf("record_decision(%v) = %+v, %v", args, res, err)
		}
	}
	record("toolu_1", map[string]any{"topic": "API", "options": []string{"REST", "GraphQL"}, "picked": "REST", "reason": "simple", "by": "both"})
	record("toolu_2", map[string]any{"decision_id": "d1", "picked": "GraphQL", "reason": "flexible queries", "by": "user"})
	record("toolu_3", map[string]any{"decision_id": "d1", "picked": "GraphQL", "reason": "a better reason", "by": "user"})

	if want := []string{"s1 n1 toolu_1", "s1 n2 toolu_2"}; !slices.Equal(calls, want) {
		t.Fatalf("checkpoints taken: %v, want %v", calls, want)
	}
	tr, err := f.srv.Store.Load("s1")
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"n1": "toolu_1", "n2": "toolu_2"} {
		if cp := tr.Node(id).Checkpoint; cp.ToolUseID != want || cp.Commit != "abc123" {
			t.Fatalf("%s checkpoint = %+v", id, cp)
		}
	}
}
