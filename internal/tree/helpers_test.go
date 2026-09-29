package tree

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 9, 29, 15, 0, 0, 0, time.UTC)

// at is t0 plus some minutes.
func at(min int) time.Time { return t0.Add(time.Duration(min) * time.Minute) }

// rec runs a call that must work.
func rec(t *testing.T, tr *Tree, c Call) Result {
	t.Helper()
	r, err := tr.Record(c)
	if err != nil {
		t.Fatalf("Record(%+v): %v", c, err)
	}
	return r
}

// recErr runs a call that must fail with a message containing want, and
// checks that the tree did not change at all.
func recErr(t *testing.T, tr *Tree, c Call, want string) {
	t.Helper()
	before, _ := json.Marshal(tr)
	_, err := tr.Record(c)
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("Record(%+v) = %v, want an *Error", c, err)
	}
	if !strings.Contains(e.Msg, want) {
		t.Fatalf("error %q does not say %q", e.Msg, want)
	}
	if after, _ := json.Marshal(tr); !bytes.Equal(before, after) {
		t.Fatalf("a failed call changed the tree")
	}
}

// want compares the tree's shape with a picture of it.
func want(t *testing.T, tr *Tree, picture string) {
	t.Helper()
	got := shape(tr)
	if exp := strings.TrimSpace(picture); got != exp {
		t.Fatalf("tree is\n%s\n\nwant\n%s", got, exp)
	}
}

// shape draws the visible tree as indented text:
//
//	● picked   ○ not picked   ◌ being weighed   ✗ dropped   ◀ you are here
//
// A decision is a line with its id and question, above its options.
func shape(tr *Tree) string {
	var b strings.Builder
	sym := map[State]string{Picked: "●", NotPicked: "○", Weighing: "◌", Dropped: "✗"}
	var node func(n *Node, depth int)
	node = func(n *Node, depth int) {
		label := n.Label
		if n.ID == RootID && label == "" {
			label = "start"
		}
		b.WriteString(strings.Repeat("  ", depth) + sym[n.State] + " " + label)
		if n.ID == tr.Here {
			b.WriteString(" ◀")
		}
		b.WriteString("\n")
		for _, d := range tr.Decisions {
			if d.Parent != n.ID || d.Hidden {
				continue
			}
			b.WriteString(strings.Repeat("  ", depth+1) + d.ID + " " + d.Question + "\n")
			for _, id := range d.Options {
				if o := tr.Node(id); !o.Hidden {
					node(o, depth+2)
				}
			}
		}
	}
	node(tr.Root(), 0)
	return strings.TrimSpace(b.String())
}

// crm builds the PRD's example up to "index on company + date":
//
//	d1 which fix? → index (n3)
//	d2 which index? → company + date (n5)
func crm(t *testing.T) *Tree {
	t.Helper()
	tr := New("s1", t0)
	rec(t, tr, Call{Question: "Which fix for slow search?", Options: []string{"Rewrite in Rust", "add a cache", "add a database index"}, At: at(1)})
	rec(t, tr, Call{DecisionID: "d1", Picked: "add a database index", Reason: "fixes the query itself", By: ByBoth, At: at(2)})
	rec(t, tr, Call{Question: "Which index?", Options: []string{"on email", "on company + date"}, Picked: "on company + date", Reason: "matches the search", By: ByClaude, At: at(3)})
	return tr
}
