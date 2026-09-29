package ui

import (
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"decision-tree/internal/branch"
	"decision-tree/internal/claude"
	"decision-tree/internal/graph"
	"decision-tree/internal/store"
	"decision-tree/internal/tree"
)

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// fake is a store in a temp folder, a clock, and a tmux pane we control.
type fake struct {
	st     store.Store
	now    time.Time
	active claude.Session
	ok     bool
}

func newFake(t *testing.T) *fake {
	return &fake{st: store.Store{Dir: t.TempDir()}, now: time.Date(2026, 9, 29, 16, 30, 0, 0, time.Local)}
}

func (f *fake) deps() Deps {
	return Deps{Store: f.st, Active: func() (claude.Session, bool) { return f.active, f.ok }, Now: func() time.Time { return f.now }}
}

func (f *fake) record(t *testing.T, session string, calls ...tree.Call) {
	t.Helper()
	_, err := f.st.Update(session, func(tr *tree.Tree) error {
		tr.Root().Label = "Notes app"
		tr.Folder = "/w/" + session
		for _, c := range calls {
			c.At = f.now
			if _, err := tr.Record(c); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Make sure the file's time moves on, as it would a moment later.
	path, _ := f.st.Path(session)
	f.now = f.now.Add(time.Second)
	os.Chtimes(path, f.now, f.now)
}

func send(m Model, msgs ...tea.Msg) Model {
	for _, msg := range msgs {
		next, _ := m.Update(msg)
		m = next.(Model)
	}
	return m
}

func key(s string) tea.Msg {
	switch s {
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case " ":
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune(" ")}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func screen(m Model) string { return ansi.ReplaceAllString(m.View(), "") }

func line(t *testing.T, m Model, contains string) string {
	t.Helper()
	for _, l := range strings.Split(screen(m), "\n") {
		if strings.Contains(l, contains) {
			return l
		}
	}
	t.Fatalf("no line with %q on screen:\n%s", contains, screen(m))
	return ""
}

var (
	pickDB   = tree.Call{Topic: "Database used", Options: []string{"SQLite", "Postgres"}, Picked: "SQLite", Reason: "a small app", By: tree.ByUser}
	pickWho  = tree.Call{Topic: "Who uses it", Options: []string{"Just me", "Many users"}, Picked: "Just me", Reason: "personal app", By: tree.ByUser}
	openTime = tree.Call{Topic: "Deploy time", Options: []string{"Tonight", "Now"}}
)

func TestDrawsTheGraphCentered(t *testing.T) {
	f := newFake(t)
	f.active, f.ok = claude.Session{ID: "s1", Cwd: "/w/notes"}, true
	f.record(t, "s1", pickDB, pickWho)
	m := send(New(f.deps(), ""), tea.WindowSizeMsg{Width: 60, Height: 24})

	got := screen(m)
	for _, want := range []string{"s1 · 2 decisions · following", "├─×  Postgres", "●  Database used: SQLite", "●  Who uses it: Just me"} {
		if !strings.Contains(got, want) {
			t.Errorf("screen is missing %q:\n%s", want, got)
		}
	}
	// The cursor starts on "you are here", and the details show it.
	if l := line(t, m, "Who uses it: Just me"); !strings.HasPrefix(strings.TrimLeft(l, " "), "›") || !strings.HasSuffix(l, "◀") {
		t.Errorf("cursor and ◀ should be on the newest pick: %q", l)
	}
	line(t, m, "Picked by you · 16:30")
	line(t, m, "Why: personal app")

	// Centered: the start node is indented from the left edge.
	root := line(t, m, "●  Notes app")
	if indent := len(root) - len(strings.TrimLeft(root, " ")); indent < 8 {
		t.Errorf("graph is not centered (indent %d): %q", indent, root)
	}
	// Nothing is wider than the pane.
	for _, l := range strings.Split(got, "\n") {
		if w := len([]rune(l)); w > 60 {
			t.Errorf("line is %d wide, pane is 60: %q", w, l)
		}
	}
}

func TestMoveAndDetails(t *testing.T) {
	f := newFake(t)
	f.record(t, "s1", pickDB)
	m := send(New(f.deps(), "s1"), tea.WindowSizeMsg{Width: 60, Height: 24})

	m = send(m, key("k")) // up from SQLite to Postgres
	line(t, m, "Rejected. SQLite was picked.")
	line(t, m, "Why SQLite: a small app")
	if l := line(t, m, "Postgres"); !strings.Contains(l, "›") {
		t.Fatalf("cursor not on Postgres: %q", l)
	}

	m = send(m, key("k")) // up to the start
	line(t, m, "Session s1")
	m = send(m, key("k")) // already at the top: stays
	line(t, m, "Session s1")

	m = send(m, key("G"), key("."))
	line(t, m, "Picked by you")
	if !m.atHere {
		t.Fatal("'.' should put the cursor on you-are-here")
	}
}

func TestFoldAndUnfold(t *testing.T) {
	f := newFake(t)
	f.record(t, "s1", pickDB, pickWho)
	m := send(New(f.deps(), "s1"), tea.WindowSizeMsg{Width: 60, Height: 24})

	m = send(m, key("g"), key(" ")) // fold the start
	if l := line(t, m, "Notes app"); !strings.Contains(l, "▸ 4 more") {
		t.Fatalf("folded start should say 4 more: %q", l)
	}
	if strings.Contains(screen(m), "SQLite") {
		t.Fatal("folded nodes are still drawn")
	}
	m = send(m, key(" ")) // open it again
	line(t, m, "Database used: SQLite")
}

func TestLiveUpdateMovesCursorAlong(t *testing.T) {
	f := newFake(t)
	f.active, f.ok = claude.Session{ID: "s1", Cwd: "/w/notes"}, true
	f.record(t, "s1", pickDB)
	m := send(New(f.deps(), ""), tea.WindowSizeMsg{Width: 60, Height: 24})

	// Claude logs two more decisions while the view is open.
	f.record(t, "s1", pickWho, openTime)
	m = send(m, tickMsg{})
	line(t, m, "Who uses it: Just me")
	line(t, m, "┊  Deploy time: ?")
	line(t, m, "╰─◌  Now")
	if l := line(t, m, "Who uses it: Just me"); !strings.Contains(l, "›") {
		t.Fatalf("cursor should follow you-are-here: %q", l)
	}

	// If Amir moved the cursor away, an update leaves it where he put it.
	m = send(m, key("g"))
	f.record(t, "s1", tree.Call{DecisionID: "d3", Picked: "Tonight", Reason: "fewer users", By: tree.ByUser})
	m = send(m, tickMsg{})
	if l := line(t, m, "Notes app"); !strings.Contains(l, "›") {
		t.Fatalf("cursor should stay on the start: %q", l)
	}
	line(t, m, "Deploy time: Tonight")
}

func TestGraphDoesNotShiftWhenHereMoves(t *testing.T) {
	f := newFake(t)
	f.record(t, "s1", pickDB, openTime)
	m := send(New(f.deps(), "s1"), tea.WindowSizeMsg{Width: 70, Height: 24})
	indent := func() int { // in columns, not bytes: "›" is 3 bytes wide
		l := line(t, m, "Database used: SQLite")
		return len([]rune(l[:strings.Index(l, "●")]))
	}
	before := indent()
	f.record(t, "s1", tree.Call{DecisionID: "d2", Picked: "Now", Reason: "r", By: tree.ByUser})
	m = send(m, tickMsg{})
	if after := indent(); after != before {
		t.Fatalf("graph moved from column %d to %d when ◀ moved", before, after)
	}
}

func TestUnchangedFileMeansSameFrame(t *testing.T) {
	f := newFake(t)
	f.record(t, "s1", pickDB)
	m := send(New(f.deps(), "s1"), tea.WindowSizeMsg{Width: 60, Height: 24})
	before := m.View()
	m = send(m, tickMsg{}, tickMsg{}, tickMsg{})
	if m.View() != before {
		t.Fatal("frame changed with nothing new: that would redraw for no reason")
	}
}

func TestFollowsTheTmuxPane(t *testing.T) {
	f := newFake(t)
	f.record(t, "s1", pickDB)
	f.record(t, "s2", pickWho)
	f.active, f.ok = claude.Session{ID: "s1", Cwd: "/w/one"}, true
	m := send(New(f.deps(), ""), tea.WindowSizeMsg{Width: 60, Height: 24})
	line(t, m, "Database used: SQLite")

	// Amir moves to the other Claude pane.
	f.active = claude.Session{ID: "s2", Cwd: "/w/two"}
	f.now = f.now.Add(2 * time.Second)
	m = send(m, tickMsg{})
	line(t, m, "Who uses it: Just me")
	line(t, m, "s2 · 1 decision · following")

	// Then to a pane that is not Claude: keep the tree, say so.
	f.ok = false
	f.now = f.now.Add(2 * time.Second)
	m = send(m, tickMsg{})
	line(t, m, "this pane is not Claude")
	line(t, m, "Who uses it: Just me")
}

func TestPinnedIgnoresThePane(t *testing.T) {
	f := newFake(t)
	f.record(t, "s1", pickDB)
	f.active, f.ok = claude.Session{ID: "s2"}, true
	m := send(New(f.deps(), "s1"), tea.WindowSizeMsg{Width: 60, Height: 24})
	f.now = f.now.Add(2 * time.Second)
	m = send(m, tickMsg{})
	line(t, m, "Database used: SQLite")
	line(t, m, "pinned")
	line(t, m, "f follow")
}

func TestEmptyStates(t *testing.T) {
	f := newFake(t)
	m := send(New(f.deps(), ""), tea.WindowSizeMsg{Width: 60, Height: 24})
	line(t, m, "No trees yet.")

	f.active, f.ok = claude.Session{ID: "fresh", Cwd: "/w/new"}, true
	m = send(New(f.deps(), ""), tea.WindowSizeMsg{Width: 60, Height: 24})
	line(t, m, "No decisions yet in this session.")
	line(t, m, "new · following")
}

func TestLongLabelsScrollSideways(t *testing.T) {
	f := newFake(t)
	long := "SQLite with a very long explanation that goes on and on"
	f.record(t, "s1", tree.Call{Topic: "Database used", Options: []string{long, "Postgres"}, Picked: long, Reason: "r", By: tree.ByUser})
	m := send(New(f.deps(), "s1"), tea.WindowSizeMsg{Width: 40, Height: 20})
	noWrap := func() {
		t.Helper()
		for _, l := range strings.Split(screen(m), "\n") {
			if w := len([]rune(l)); w > 40 {
				t.Errorf("line is %d wide, pane is 40: %q", w, l)
			}
		}
	}
	noWrap()
	// Too wide: the row is cut at the edge with "…", and the help says how to scroll.
	if l := line(t, m, "Database used: SQLite"); !strings.HasSuffix(l, "…") {
		t.Fatalf("cut row should end in …: %q", l)
	}
	line(t, m, "←/→ scroll")

	// Scroll right until the end of the label and ◀ show up.
	for i := 0; i < 10; i++ {
		m = send(m, key("right"))
	}
	noWrap()
	if l := line(t, m, "goes on and on"); !strings.HasSuffix(l, "◀") || !strings.Contains(l, "…") {
		t.Fatalf("after scrolling right, want the end of the label, ◀, and a … on the left: %q", l)
	}
	// Scrolling stops at the end: more presses change nothing.
	before := screen(m)
	m = send(m, key("l"))
	if screen(m) != before {
		t.Fatal("scrolled past the end of the graph")
	}
	// 0 jumps back to the left edge.
	m = send(m, key("0"))
	line(t, m, "Database used: SQLite")

	// The details panel always has the whole label, wrapped.
	line(t, m, "Database used: SQLite with a very")
	line(t, m, "explanation that goes on and on")
}

func TestNarrowGraphDoesNotScroll(t *testing.T) {
	f := newFake(t)
	f.record(t, "s1", pickDB)
	m := send(New(f.deps(), "s1"), tea.WindowSizeMsg{Width: 60, Height: 24})
	before := screen(m)
	m = send(m, key("right"), key("right"))
	if screen(m) != before {
		t.Fatal("a graph that fits should not move sideways")
	}
	if strings.Contains(before, "←/→ scroll") {
		t.Fatal("help mentions scrolling, but there is nothing to scroll")
	}
}

func TestDetailsWording(t *testing.T) {
	f := newFake(t)
	f.record(t, "s1",
		tree.Call{Topic: "API framework", Options: []string{"FastAPI"}, Picked: "FastAPI", Reason: "recommended, and you agreed", By: tree.ByBoth},
		tree.Call{Topic: "API", Options: []string{"REST"}, Picked: "REST", Reason: "simple", By: tree.ByUser},
		tree.Call{DecisionID: "d2", Options: []string{"GraphQL"}, Picked: "GraphQL", Reason: "the client needs flexible queries", By: tree.ByUser},
	)
	m := send(New(f.deps(), "s1"), tea.WindowSizeMsg{Width: 70, Height: 24})
	line(t, m, "├─↺  REST")
	line(t, m, "●  API: GraphQL")

	m = send(m, key("k")) // REST, changed later
	line(t, m, "Changed to GraphQL · 16:30")
	line(t, m, "Why: the client needs flexible queries")

	m = send(m, key("k")) // FastAPI, agreed by both
	line(t, m, "Agreed by you and Claude · 16:30")
}

func TestScrollKeepsCursorOnScreen(t *testing.T) {
	f := newFake(t)
	var calls []tree.Call
	for i := 0; i < 12; i++ {
		topic := "Step " + string(rune('A'+i))
		calls = append(calls, tree.Call{Topic: topic, Options: []string{"yes", "no"}, Picked: "yes", Reason: "r", By: tree.ByUser})
	}
	f.record(t, "s1", calls...)
	m := send(New(f.deps(), "s1"), tea.WindowSizeMsg{Width: 60, Height: 16})
	// The newest pick is at the bottom of a long graph; it must be on screen.
	line(t, m, "Step L: yes")
	m = send(m, key("g"))
	line(t, m, "●  Notes app")
	if strings.Contains(screen(m), "Step L: yes") {
		t.Fatal("after g the bottom should have scrolled away")
	}
}

// branchWorld: session s1 picked FastAPI (with a checkpoint), and branch b1
// was made from that pick and changed it to Flask.
func branchWorld(t *testing.T) (*fake, Deps) {
	t.Helper()
	f := newFake(t)
	f.record(t, "s1", tree.Call{Topic: "API framework", Options: []string{"FastAPI", "Flask"}, Picked: "FastAPI", Reason: "async", By: tree.ByBoth})
	f.st.Update("s1", func(tr *tree.Tree) error {
		tr.Node("n1").Checkpoint = tree.Checkpoint{ToolUseID: "toolu_1", Commit: "abcdef1234567890", Memory: "/m"}
		return nil
	})
	f.st.Update("b1", func(tr *tree.Tree) error {
		tr.Root().Label, tr.Folder = "Notes app", "/w/flask-instead"
		tr.Record(tree.Call{Topic: "API framework", Options: []string{"FastAPI", "Flask"}, Picked: "FastAPI", Reason: "async", By: tree.ByBoth})
		tr.Record(tree.Call{DecisionID: "d1", Picked: "Flask", Reason: "simpler", By: tree.ByUser})
		tr.Record(tree.Call{Topic: "Templates", Options: []string{"Jinja"}, Picked: "Jinja", Reason: "comes with Flask", By: tree.ByClaude})
		tr.Branch = &tree.Branch{Name: "flask-instead", FromSession: "s1", FromNode: "n1", Inherited: 1, At: f.now}
		return nil
	})
	d := f.deps()
	d.Branches = func(session string) map[string][]graph.Stub { return branch.Children(f.st, session) }
	return f, d
}

func TestBranchStubOpensAndComesBack(t *testing.T) {
	_, d := branchWorld(t)
	m := send(New(d, "s1"), tea.WindowSizeMsg{Width: 70, Height: 24})
	line(t, m, "⎇  flask-instead · 1 decision")

	m = send(m, key("j")) // from FastAPI down to the branch
	line(t, m, "A branch made from the pick above")
	m = send(m, key("enter"))
	line(t, m, "flask-instead · branch of s1 · 2 decisions · pinned")
	line(t, m, "●  API framework: Flask")
	line(t, m, "p parent")

	m = send(m, key("p")) // back to the original, on the pick the branch came from
	line(t, m, "s1 · 1 decision · pinned")
	if l := line(t, m, "API framework: FastAPI"); !strings.Contains(l, "›") {
		t.Fatalf("cursor should be on the pick the branch came from: %q", l)
	}
	m = send(m, key("p"))
	line(t, m, "This session did not branch from another one")
}

func TestSourceScreen(t *testing.T) {
	_, d := branchWorld(t)
	d.Source = func(session, node string) (claude.Cut, error) {
		return claude.Cut{PromptNumber: 2, Prompt: "Yes, go with FastAPI", Before: "I would pick FastAPI for async support."}, nil
	}
	m := send(New(d, "s1"), tea.WindowSizeMsg{Width: 70, Height: 30})
	m = send(m, key("enter"))
	for _, want := range []string{"API framework: FastAPI", "Your message #2:", "Yes, go with FastAPI",
		"Claude said just before:", "Code: as it was then (abcdef123456)", "Memory: as it was then", "esc closes · b branches from here"} {
		line(t, m, want)
	}
	next, cmd := m.Update(key("esc"))
	m = next.(Model)
	if cmd != nil || strings.Contains(screen(m), "Your message #2:") {
		t.Fatal("esc should close the source screen, not quit")
	}

	// When the chat is gone, the screen says a branch can't be made, instead
	// of promising a chat it no longer has.
	d.Source = func(string, string) (claude.Cut, error) {
		return claude.Cut{}, errors.New("the chat file is not on this computer")
	}
	gone := send(New(d, "s1"), tea.WindowSizeMsg{Width: 70, Height: 30}, key("enter"))
	line(t, gone, "The chat: the chat file is not on this computer")
	line(t, gone, "Chat: not available, so no branch can be made from here")

	m = send(m, key("k"), key("enter")) // Flask, drawn as a stub above FastAPI: rejected
	line(t, m, "This option was not picked")
	m = send(m, key("x"), key("k"), key("enter")) // any key closes; then the start node
	line(t, m, "Nothing was decided here")
}

func TestBranchFromTheView(t *testing.T) {
	f, d := branchWorld(t)
	var made []bool
	d.Prepare = func(session, node string) (*branch.Plan, error) {
		tr, _ := f.st.Load(session)
		return &branch.Plan{Parent: tr, Node: tr.Node(node), Cut: claude.Cut{PromptNumber: 1, Prompt: "Build an API"},
			Name: "api-framework-fastapi", Session: "b2", Repo: "/w/app", Worktree: "/wt/api", Dir: "/wt/api", Memory: 1}, nil
	}
	d.Create = func(p *branch.Plan, focus bool) (branch.Result, error) {
		made = append(made, focus)
		return branch.Result{Pane: "%9"}, nil
	}
	m := send(New(d, "s1"), tea.WindowSizeMsg{Width: 80, Height: 30})

	m = send(m, key("b"))
	line(t, m, "Create a branch?")
	line(t, m, "Branch from:  API framework: FastAPI")
	line(t, m, "y creates it · n cancels")
	m = send(m, key("n"))
	if strings.Contains(screen(m), "Create a branch?") || len(made) != 0 {
		t.Fatal("n should cancel")
	}

	m = send(m, key("B"))
	next, cmd := m.Update(key("y"))
	m = next.(Model)
	line(t, m, "Making the branch…")
	if cmd == nil {
		t.Fatal("y should start making the branch")
	}
	m = send(m, cmd())
	line(t, m, "Created branch api-framework-fastapi")
	line(t, m, "tmux: a new window named api-framework-fastapi.")
	if len(made) != 1 || !made[0] {
		t.Fatalf("Create calls: %v, want one with focus (B)", made)
	}

	d.Prepare = func(string, string) (*branch.Plan, error) {
		return nil, errors.New("it was logged before checkpoints existed")
	}
	m = send(New(d, "s1"), tea.WindowSizeMsg{Width: 80, Height: 30}, key("b"))
	line(t, m, "Can't branch from here")
	line(t, m, "it was logged before checkpoints existed")
}

func TestNewBranchesShowUpOnTheirOwn(t *testing.T) {
	f, d := branchWorld(t)
	stubs := map[string][]graph.Stub{}
	d.Branches = func(string) map[string][]graph.Stub { return stubs }
	m := send(New(d, "s1"), tea.WindowSizeMsg{Width: 70, Height: 24})
	if strings.Contains(screen(m), "⎇") {
		t.Fatal("no branches yet")
	}
	stubs = map[string][]graph.Stub{"n1": {{Session: "b9", Name: "late-branch", Decisions: 0}}}
	f.now = f.now.Add(2 * time.Second)
	m = send(m, tickMsg{})
	line(t, m, "⎇  late-branch · 0 decisions")
}
