package graph

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"decision-tree/internal/tree"
)

func build(t *testing.T, title string, calls ...tree.Call) *tree.Tree {
	t.Helper()
	tr := tree.New("s1", time.Now())
	tr.Root().Label = title
	for _, c := range calls {
		if _, err := tr.Record(c); err != nil {
			t.Fatalf("%+v: %v", c, err)
		}
	}
	return tr
}

func want(t *testing.T, got, picture string) {
	t.Helper()
	if exp := strings.Trim(picture, "\n"); got != exp {
		t.Fatalf("got\n%s\n\nwant\n%s", got, exp)
	}
}

// Amir's first real tree (29 Sep 2026), plus one open decision.
func notesApp(t *testing.T) *tree.Tree {
	return build(t, "Notes app database choice",
		tree.Call{Topic: "Database used", Options: []string{"SQLite", "Postgres"}, Picked: "SQLite", Reason: "small app", By: tree.ByUser},
		tree.Call{Topic: "Who uses it", Options: []string{"Just me", "Multiple users"}, Picked: "Just me", Reason: "personal app", By: tree.ByUser},
		tree.Call{Topic: "Deleting a note", Options: []string{"Trash bin", "Gone forever"}, Picked: "Trash bin", Reason: "undo deletes", By: tree.ByUser},
		tree.Call{Topic: "Deploy time", Options: []string{"Tonight", "Now"}},
	)
}

func TestMainLine(t *testing.T) {
	want(t, Plain(Layout(notesApp(t), nil)), `
●  Notes app database choice
│
├─×  Postgres
●  Database used: SQLite
│
├─×  Multiple users
●  Who uses it: Just me
│
├─×  Gone forever
●  Deleting a note: Trash bin  ◀
│
┊  Deploy time: ?
├─◌  Tonight
╰─◌  Now`)
}

// crm: the index is set aside for the cache (drop_later), with a question
// still open.
func crm(t *testing.T) *tree.Tree {
	return build(t, "CRM search is slow",
		tree.Call{Topic: "Fix", Options: []string{"Rust rewrite", "Cache", "Index"}},
		tree.Call{DecisionID: "d1", Picked: "Index", Reason: "fixes the query", By: tree.ByBoth},
		tree.Call{Topic: "Index on", Options: []string{"email", "company + date"}, Picked: "company + date", Reason: "matches the search", By: tree.ByClaude},
		tree.Call{DecisionID: "d1", Picked: "Cache", Reason: "the index did not help", By: tree.ByUser, DropLater: true},
		tree.Call{Topic: "Cache time", Options: []string{"5 minutes", "1 hour"}},
	)
}

func TestDroppedBranchGetsALane(t *testing.T) {
	want(t, Plain(Layout(crm(t), nil)), `
●  CRM search is slow
│
├─×  Rust rewrite
├─╮
│ ↺  Fix: Index
│ │
│ ├─×  email
│ ●  Index on: company + date
●  Fix: Cache  ◀
│
┊  Cache time: ?
├─◌  5 minutes
╰─◌  1 hour`)
}

func TestFolding(t *testing.T) {
	tr := crm(t)
	// A dropped branch starts folded.
	fold := func(id string) bool { return DefaultFolded(tr, id) }
	want(t, Plain(Layout(tr, fold)), `
●  CRM search is slow
│
├─×  Rust rewrite
├─╮
│ ↺  Fix: Index ▸ 2 more
●  Fix: Cache  ◀
│
┊  Cache time: ?
├─◌  5 minutes
╰─◌  1 hour`)

	// Folding the start hides everything.
	want(t, Plain(Layout(tr, func(id string) bool { return id == tree.RootID })), `●  CRM search is slow ▸ 7 more`)

	if !HasChildren(tr, tree.RootID) || HasChildren(tr, "n1") || DefaultFolded(tr, "n2") {
		t.Fatal("HasChildren or DefaultFolded is wrong")
	}
}

func TestDroppedLeafIsAStub(t *testing.T) {
	// Going back with "after" drops cobra. Nothing grows from cobra, so it
	// is a stub, not a lane.
	tr := build(t, "App",
		tree.Call{Topic: "Language", Options: []string{"Go"}, Picked: "Go", Reason: "one file", By: tree.ByUser},
		tree.Call{Topic: "CLI library", Options: []string{"cobra"}, Picked: "cobra", Reason: "common", By: tree.ByClaude},
		tree.Call{Topic: "Config format", Options: []string{"TOML", "YAML"}, After: "n1", Reason: "rethink"},
	)
	want(t, Plain(Layout(tr, nil)), `
●  App
│
●  Language: Go  ◀
│
├─↺  cobra
│
┊  Config format: ?
├─◌  TOML
╰─◌  YAML`)
}

func TestLaneThatEndsTheGraph(t *testing.T) {
	// A branch was given up and nothing replaced it yet. Nothing carries the
	// main line on, so the lane closes it with "╰─╮".
	tr := build(t, "App",
		tree.Call{Topic: "Language", Options: []string{"Go"}, Picked: "Go", Reason: "one file", By: tree.ByUser},
		tree.Call{Topic: "CLI library", Options: []string{"cobra"}, Picked: "cobra", Reason: "common", By: tree.ByClaude},
	)
	tr.Node("n1").State, tr.Here = tree.Dropped, tree.RootID
	want(t, Plain(Layout(tr, nil)), `
●  App  ◀
│
╰─╮
  ↺  Language: Go
  │
  ●  CLI library: cobra`)
}

func TestDeletedNodesAreLeftOut(t *testing.T) {
	tr := notesApp(t)
	if err := tr.FixDelete("n2"); err != nil { // Postgres
		t.Fatal(err)
	}
	got := Plain(Layout(tr, nil))
	if strings.Contains(got, "Postgres") {
		t.Fatalf("deleted node still drawn:\n%s", got)
	}
	if !strings.Contains(got, "│\n●  Database used: SQLite") {
		t.Fatalf("pick should follow the line directly:\n%s", got)
	}
}

func TestRowsForTheCursor(t *testing.T) {
	rows := Layout(notesApp(t), nil)
	var nodes, here int
	for _, r := range rows {
		if r.Kind == Node {
			nodes++
			if r.NodeID == "" {
				t.Fatalf("node row without an id: %+v", r)
			}
		}
		if r.Here {
			here++
		}
	}
	if nodes != 9 || here != 1 {
		t.Fatalf("%d node rows and %d here rows; want 9 and 1", nodes, here)
	}
}

func TestOldTreeFilesStillLoad(t *testing.T) {
	// Before 30 Sep 2026 the topic was saved as "question".
	var tr tree.Tree
	old := `{"version":1,"session_id":"s","here":"n1","nodes":[{"id":"n0","label":"App","state":"picked"},{"id":"n1","decision":"d1","label":"SQLite","state":"picked"}],"decisions":[{"id":"d1","question":"Database for the app?","parent":"n0","options":["n1"]}]}`
	if err := json.Unmarshal([]byte(old), &tr); err != nil {
		t.Fatal(err)
	}
	want(t, Plain(Layout(&tr, nil)), `
●  App
│
●  Database for the app?: SQLite  ◀`)
}
