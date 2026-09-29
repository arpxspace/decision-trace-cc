package tree

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestNewTree(t *testing.T) {
	tr := New("s1", t0)
	if tr.Version != Version || tr.SessionID != "s1" || tr.Here != RootID || tr.Root().State != Picked {
		t.Fatalf("new tree = %+v", tr)
	}
	want(t, tr, `● start ◀`)
}

func TestWeighThenPick(t *testing.T) {
	tr := New("s1", t0)
	r := rec(t, tr, Call{Question: "Which fix for slow search?", Options: []string{"Rewrite in Rust", "add a cache", "add a database index"}, At: at(1)})
	if r.DecisionID != "d1" || r.Here != RootID || !slices.Equal(r.Open, []string{"d1"}) {
		t.Fatalf("result = %+v", r)
	}
	want(t, tr, `
● start ◀
  d1 Which fix for slow search?
    ◌ Rewrite in Rust
    ◌ add a cache
    ◌ add a database index`)

	r = rec(t, tr, Call{DecisionID: "d1", Picked: "Add a Database Index", Reason: "fixes the query itself", By: ByUser, At: at(2)})
	if r.Here != "n3" || len(r.Open) != 0 {
		t.Fatalf("result = %+v", r)
	}
	want(t, tr, `
● start
  d1 Which fix for slow search?
    ○ Rewrite in Rust
    ○ add a cache
    ● add a database index ◀`)
	if n := tr.Node("n3"); n.Reason != "fixes the query itself" || n.By != ByUser || !n.At.Equal(at(2)) {
		t.Fatalf("picked node = %+v", n)
	}
}

func TestPickedAtOnce(t *testing.T) {
	tr := crm(t)
	want(t, tr, `
● start
  d1 Which fix for slow search?
    ○ Rewrite in Rust
    ○ add a cache
    ● add a database index
      d2 Which index?
        ○ on email
        ● on company + date ◀`)
}

func TestGoBackToGreyOption(t *testing.T) {
	tr := crm(t)
	r := rec(t, tr, Call{DecisionID: "d1", Picked: "add a cache", Reason: "the index did not help", By: ByBoth, At: at(4)})
	if r.Here != "n2" {
		t.Fatalf("here = %s, want n2", r.Here)
	}
	want(t, tr, `
● start
  d1 Which fix for slow search?
    ○ Rewrite in Rust
    ● add a cache ◀
    ✗ add a database index
      d2 Which index?
        ○ on email
        ● on company + date`)
	if n := tr.Node("n3"); n.DropReason != "the index did not help" || n.Reason != "fixes the query itself" {
		t.Fatalf("dropped node = %+v (pick reason must stay)", n)
	}
}

func TestAfterDropsTheBranch(t *testing.T) {
	tr := crm(t)
	// Back up to "add a database index" and ask a new question there.
	r := rec(t, tr, Call{Question: "Which column order?", Options: []string{"date first", "company first"}, After: "n3", At: at(4)})
	if r.Here != "n3" {
		t.Fatalf("here = %s, want n3", r.Here)
	}
	want(t, tr, `
● start
  d1 Which fix for slow search?
    ○ Rewrite in Rust
    ○ add a cache
    ● add a database index ◀
      d2 Which index?
        ○ on email
        ✗ on company + date
      d3 Which column order?
        ◌ date first
        ◌ company first`)
}

func TestAfterMustBeOnLiveBranch(t *testing.T) {
	tr := crm(t)
	recErr(t, tr, Call{Question: "Q", Options: []string{"a"}, After: "n2"}, "not on the live branch")
	recErr(t, tr, Call{Question: "Q", Options: []string{"a"}, After: "n99"}, "there is no node")
}

func TestOpenDecisionsFloatDown(t *testing.T) {
	tr := New("s1", t0)
	rec(t, tr, Call{Question: "When to deploy?", Options: []string{"tonight", "now"}, At: at(1)})
	r := rec(t, tr, Call{Question: "Which database?", Options: []string{"Postgres", "SQLite"}, Picked: "Postgres", Reason: "we need many writers", By: ByUser, At: at(2)})
	if !slices.Equal(r.Open, []string{"d1"}) || len(r.Left) != 0 {
		t.Fatalf("open = %v, left = %v; want [d1] open, none left (floating down is not being left behind)", r.Open, r.Left)
	}
	// d1 is still on the table, so it moves down under the new pick.
	want(t, tr, `
● start
  d2 Which database?
    ● Postgres ◀
      d1 When to deploy?
        ◌ tonight
        ◌ now
    ○ SQLite`)
	rec(t, tr, Call{DecisionID: "d1", Picked: "tonight", Reason: "fewer users", By: ByUser, At: at(3)})
	want(t, tr, `
● start
  d2 Which database?
    ● Postgres
      d1 When to deploy?
        ● tonight ◀
        ○ now
    ○ SQLite`)
}

func TestStrandedOpenDecision(t *testing.T) {
	tr := New("s1", t0)
	rec(t, tr, Call{Question: "Q1", Options: []string{"A", "B"}, Picked: "A", Reason: "r", By: ByUser, At: at(1)})
	rec(t, tr, Call{Question: "Q2", Options: []string{"x", "y"}, At: at(2)})
	r := rec(t, tr, Call{DecisionID: "d1", Picked: "B", Reason: "A failed", By: ByUser, At: at(3)})
	// Q2 was asked under A. A was given up, so Q2 is no longer open on the
	// live branch, and Claude is told it was left behind.
	if len(r.Open) != 0 || !slices.Equal(r.Left, []string{"d2"}) {
		t.Fatalf("open = %v, left = %v; want none open, d2 left", r.Open, r.Left)
	}
	// Answering it anyway brings it to "you are here".
	rec(t, tr, Call{DecisionID: "d2", Picked: "x", Reason: "r", By: ByUser, At: at(4)})
	want(t, tr, `
● start
  d1 Q1
    ✗ A
    ● B
      d2 Q2
        ● x ◀
        ○ y`)
}

func TestAddOptions(t *testing.T) {
	tr := New("s1", t0)
	rec(t, tr, Call{Question: "Q", Options: []string{"a", "b"}, At: at(1)})
	rec(t, tr, Call{DecisionID: "d1", Options: []string{" A ", "c"}, At: at(2)})
	want(t, tr, `
● start ◀
  d1 Q
    ◌ a
    ◌ b
    ◌ c`)
	rec(t, tr, Call{DecisionID: "d1", Picked: "b", Reason: "r", By: ByUser, At: at(3)})
	rec(t, tr, Call{DecisionID: "d1", Options: []string{"d"}, At: at(4)})
	// Options that come up after the pick were not picked.
	want(t, tr, `
● start
  d1 Q
    ○ a
    ● b ◀
    ○ c
    ○ d`)
}

func TestSamePickAgainUpdatesReason(t *testing.T) {
	tr := crm(t)
	rec(t, tr, Call{DecisionID: "d2", Picked: "on company + date", Reason: "new reason", By: ByUser, At: at(9)})
	if n := tr.Node("n5"); n.Reason != "new reason" || n.By != ByUser || tr.Here != "n5" {
		t.Fatalf("node = %+v, here = %s", n, tr.Here)
	}
}

func TestBadCalls(t *testing.T) {
	tr := crm(t)
	cases := []struct {
		name string
		call Call
		want string
	}{
		{"no question", Call{Options: []string{"a"}}, "question is needed"},
		{"no options", Call{Question: "Q", Options: []string{" ", ""}}, "options is needed"},
		{"picked not an option", Call{Question: "Q", Options: []string{"a"}, Picked: "b", Reason: "r", By: ByUser}, "not one of the options"},
		{"no reason", Call{Question: "Q", Options: []string{"a"}, Picked: "a", By: ByUser}, "reason is needed"},
		{"bad by", Call{Question: "Q", Options: []string{"a"}, Picked: "a", Reason: "r", By: "me"}, "by is needed"},
		{"unknown decision", Call{DecisionID: "d9"}, "there is no decision"},
		{"picked not in decision", Call{DecisionID: "d1", Picked: "zzz", Reason: "r", By: ByUser}, "not an option of d1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { recErr(t, tr, c.call, c.want) })
	}
}

func TestCannotPickDroppedOption(t *testing.T) {
	tr := crm(t)
	rec(t, tr, Call{DecisionID: "d1", Picked: "add a cache", Reason: "r", By: ByUser, At: at(4)})
	recErr(t, tr, Call{DecisionID: "d1", Picked: "add a database index", Reason: "r", By: ByUser}, "dropped earlier")
}

func TestCannotGoBackInsideDroppedBranch(t *testing.T) {
	tr := crm(t)
	rec(t, tr, Call{DecisionID: "d1", Picked: "add a cache", Reason: "r", By: ByUser, At: at(4)})
	// d2 lives under the dropped index branch.
	recErr(t, tr, Call{DecisionID: "d2", Picked: "on email", Reason: "r", By: ByUser}, "dropped branch")
}

func TestJSONRoundTrip(t *testing.T) {
	tr := crm(t)
	b, err := json.Marshal(tr)
	if err != nil {
		t.Fatal(err)
	}
	var back Tree
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if shape(&back) != shape(tr) {
		t.Fatalf("round trip changed the tree:\n%s", shape(&back))
	}
	// And it still works after loading.
	rec(t, &back, Call{DecisionID: "d1", Picked: "add a cache", Reason: "r", By: ByUser, At: at(4)})
}
