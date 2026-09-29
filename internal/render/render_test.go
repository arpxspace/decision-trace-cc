package render

import (
	"testing"
	"time"

	"decision-tree/internal/tree"
)

func example(t *testing.T) *tree.Tree {
	t.Helper()
	tr := tree.New("s1", time.Now())
	tr.Root().Label = "CRM search is slow"
	calls := []tree.Call{
		{Question: "Which fix?", Options: []string{"Rewrite in Rust", "add a cache", "add a database index"}},
		{DecisionID: "d1", Picked: "add a database index", Reason: "fixes the query itself", By: tree.ByBoth},
		{Question: "Which index?", Options: []string{"on email", "on company + date"}, Picked: "on company + date", Reason: "matches the search", By: tree.ByClaude},
		{DecisionID: "d1", Picked: "add a cache", Reason: "the index did not help", By: tree.ByUser},
		{Question: "Cache for how long?", Options: []string{"5 minutes", "1 hour"}},
	}
	for _, c := range calls {
		if _, err := tr.Record(c); err != nil {
			t.Fatal(err)
		}
	}
	return tr
}

func TestTextForAmir(t *testing.T) {
	got := Text(example(t), Options{Reasons: true})
	want := `● CRM search is slow
  Which fix?
    ○ Rewrite in Rust
    ● add a cache ◀ — the index did not help (user)
      Cache for how long?
        ◌ 5 minutes
        ◌ 1 hour
    ✗ add a database index — dropped: the index did not help
      Which index?
        ○ on email
        ● on company + date — matches the search (claude)`
	if got != want {
		t.Fatalf("got\n%s\n\nwant\n%s", got, want)
	}
}

func TestTextForClaude(t *testing.T) {
	tr := example(t)
	if err := tr.FixDelete("n1"); err != nil {
		t.Fatal(err)
	}
	got := Text(tr, Options{IDs: true})
	want := `● n0 CRM search is slow
  d1: Which fix?
    ● n2 add a cache ◀
      d3: Cache for how long?
        ◌ n6 5 minutes
        ◌ n7 1 hour
    ✗ n3 add a database index
      d2: Which index?
        ○ n4 on email
        ● n5 on company + date`
	if got != want {
		t.Fatalf("got\n%s\n\nwant\n%s", got, want)
	}
}

func TestUnnamedStart(t *testing.T) {
	if got := Text(tree.New("s1", time.Now()), Options{}); got != "● start ◀" {
		t.Fatalf("got %q", got)
	}
}
