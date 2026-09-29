package ui

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"decision-tree/internal/claude"
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
