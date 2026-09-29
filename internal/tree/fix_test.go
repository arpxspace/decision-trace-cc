package tree

import (
	"errors"
	"slices"
	"testing"
)

func TestFixRenameLocks(t *testing.T) {
	tr := crm(t)
	if err := tr.FixRename("n1", "  Rust   rewrite "); err != nil {
		t.Fatal(err)
	}
	if n := tr.Node("n1"); n.Label != "Rust rewrite" || !n.Locked {
		t.Fatalf("n1 = %+v", n)
	}
	recErr(t, tr, Call{DecisionID: "d1", Picked: "Rust rewrite", Reason: "r", By: ByUser}, "fixed")
	if err := tr.FixRename("n1", " "); err == nil {
		t.Fatal("empty label was accepted")
	}
}

func TestFixPick(t *testing.T) {
	tr := crm(t)
	if err := tr.FixPick("n2", at(5)); err != nil {
		t.Fatal(err)
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
	if n := tr.Node("n2"); !n.Locked || n.By != ByUser {
		t.Fatalf("n2 = %+v", n)
	}
	// Claude cannot move the pick away from Amir's choice.
	recErr(t, tr, Call{DecisionID: "d1", Picked: "Rewrite in Rust", Reason: "r", By: ByClaude}, "fixed")
	// But Claude can keep going from it.
	rec(t, tr, Call{Topic: "Cache for how long?", Options: []string{"5 minutes"}, Picked: "5 minutes", Reason: "r", By: ByClaude, At: at(6)})
}

func TestFixPickPassesLocks(t *testing.T) {
	tr := crm(t)
	if err := tr.FixPick("n2", at(5)); err != nil {
		t.Fatal(err)
	}
	// Amir can change his own fix, even though n2 is locked.
	if err := tr.FixPick("n1", at(6)); err != nil {
		t.Fatal(err)
	}
	if tr.Here != "n1" || tr.Node("n2").State != Dropped {
		t.Fatalf("here = %s, n2 = %+v", tr.Here, tr.Node("n2"))
	}
}

func TestFixDelete(t *testing.T) {
	tr := crm(t)
	if err := tr.FixDelete("n1"); err != nil {
		t.Fatal(err)
	}
	// Claude brings it up again. It stays deleted, and Claude is told.
	r := rec(t, tr, Call{DecisionID: "d1", Options: []string{"rewrite in rust", "rewrite in Go"}, At: at(5)})
	if !slices.Equal(r.Skipped, []string{"rewrite in rust"}) {
		t.Fatalf("skipped = %v", r.Skipped)
	}
	want(t, tr, `
● start
  d1 Which fix for slow search?
    ○ add a cache
    ● add a database index
      d2 Which index?
        ○ on email
        ● on company + date ◀
    ○ rewrite in Go`)
	recErr(t, tr, Call{DecisionID: "d1", Picked: "Rewrite in Rust", Reason: "r", By: ByUser}, "deleted")
}

func TestFixDeleteUnderHere(t *testing.T) {
	tr := crm(t)
	// Deleting the index deletes everything under it, and "you are here"
	// moves up to the nearest node left.
	if err := tr.FixDelete("n3"); err != nil {
		t.Fatal(err)
	}
	want(t, tr, `
● start ◀
  d1 Which fix for slow search?
    ○ Rewrite in Rust
    ○ add a cache`)
	recErr(t, tr, Call{DecisionID: "d2", Picked: "on email", Reason: "r", By: ByUser}, "deleted")
	if err := tr.FixDelete(RootID); err == nil {
		t.Fatal("the start node was deleted")
	}
}

func TestFixDeleteLastOptionHidesDecision(t *testing.T) {
	tr := New("s1", t0)
	rec(t, tr, Call{Topic: "Q", Options: []string{"a"}, At: at(1)})
	if err := tr.FixDelete("n1"); err != nil {
		t.Fatal(err)
	}
	if !tr.Decision("d1").Hidden {
		t.Fatal("d1 should be hidden with no options left")
	}
	want(t, tr, `● start ◀`)
}

func TestUndo(t *testing.T) {
	tr := crm(t)
	start := shape(tr)

	if err := tr.FixRename("n1", "Rust"); err != nil {
		t.Fatal(err)
	}
	if err := tr.FixDelete("n3"); err != nil {
		t.Fatal(err)
	}
	if err := tr.Undo(); err != nil {
		t.Fatal(err)
	}
	if err := tr.Undo(); err != nil {
		t.Fatal(err)
	}
	if got := shape(tr); got != start {
		t.Fatalf("after undoing both fixes:\n%s\n\nwant\n%s", got, start)
	}
	if tr.Node("n1").Locked || tr.Node("n3").Hidden {
		t.Fatal("undo left a lock or a hidden node behind")
	}
	if err := tr.Undo(); !errors.Is(err, ErrNothingToUndo) {
		t.Fatalf("undo with nothing left = %v", err)
	}
}

func TestUndoKeepsClaudesLaterWork(t *testing.T) {
	tr := crm(t)
	if err := tr.FixRename("n4", "email index"); err != nil {
		t.Fatal(err)
	}
	rec(t, tr, Call{Topic: "Run it when?", Options: []string{"now"}, Picked: "now", Reason: "r", By: ByUser, At: at(5)})
	if err := tr.Undo(); err != nil {
		t.Fatal(err)
	}
	want(t, tr, `
● start
  d1 Which fix for slow search?
    ○ Rewrite in Rust
    ○ add a cache
    ● add a database index
      d2 Which index?
        ○ on email
        ● on company + date
          d3 Run it when?
            ● now ◀`)
}

func TestUndoPickAfterClaudeMovedOn(t *testing.T) {
	tr := crm(t)
	if err := tr.FixPick("n2", at(5)); err != nil {
		t.Fatal(err)
	}
	rec(t, tr, Call{Topic: "Cache for how long?", Options: []string{"5 minutes"}, Picked: "5 minutes", Reason: "r", By: ByClaude, At: at(6)})
	if err := tr.Undo(); err != nil {
		t.Fatal(err)
	}
	// The cache is grey again, so "you are here" goes back to where it was.
	// Claude's question stays, under the grey cache.
	if tr.Here != "n5" {
		t.Fatalf("here = %s, want n5", tr.Here)
	}
	want(t, tr, `
● start
  d1 Which fix for slow search?
    ○ Rewrite in Rust
    ○ add a cache
      d3 Cache for how long?
        ● 5 minutes
    ● add a database index
      d2 Which index?
        ○ on email
        ● on company + date ◀`)
}

func TestUndoListIsCapped(t *testing.T) {
	tr := crm(t)
	for i := 0; i < MaxUndo+10; i++ {
		if err := tr.FixRename("n1", "name"+string(rune('a'+i%26))+string(rune('a'+i/26))); err != nil {
			t.Fatal(err)
		}
	}
	if len(tr.Fixes) != MaxUndo {
		t.Fatalf("kept %d fixes, want %d", len(tr.Fixes), MaxUndo)
	}
}
