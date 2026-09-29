package tree

import (
	"errors"
	"slices"
	"time"
)

// Change is one of Amir's fixes, kept so it can be undone. It holds the
// nodes and decisions the fix changed, as they were before it.
type Change struct {
	What      string     `json:"what"`       // e.g. "rename n4"
	Here      string     `json:"here"`       // "you are here" before the fix
	HereAfter string     `json:"here_after"` // "you are here" right after it
	Nodes     []Node     `json:"nodes,omitempty"`
	Decisions []Decision `json:"decisions,omitempty"`
}

// MaxUndo is how many fixes are kept for undo.
const MaxUndo = 50

// ErrNothingToUndo means there is no fix left to undo.
var ErrNothingToUndo = errors.New("nothing to undo")

// FixPick makes an option the winner, even if nodes around it are locked.
// It locks the option, so Claude cannot pick something else instead.
func (t *Tree) FixPick(id string, at time.Time) error {
	return t.fix("pick "+id, func(w *Tree) error {
		x := w.Node(id)
		if x == nil || x.Hidden || x.Decision == "" {
			return errf("%s is not an option.", id)
		}
		if err := w.pick(w.Decision(x.Decision), x, "", ByUser, at, true); err != nil {
			return err
		}
		x.Locked = true
		w.float()
		return nil
	})
}

// FixRename gives a node a new label and locks it.
func (t *Tree) FixRename(id, label string) error {
	return t.fix("rename "+id, func(w *Tree) error {
		n := w.Node(id)
		if n == nil || n.Hidden {
			return errf("there is no node %s.", id)
		}
		if label = clean(label); label == "" {
			return errf("the new label is empty.")
		}
		n.Label, n.Locked = label, true
		return nil
	})
}

// FixDelete hides a node and everything that grows from it. Hidden nodes
// stay in the file, locked, so Claude cannot add them back.
func (t *Tree) FixDelete(id string) error {
	return t.fix("delete "+id, func(w *Tree) error {
		n := w.Node(id)
		if n == nil || n.Hidden {
			return errf("there is no node %s.", id)
		}
		if n.ID == RootID {
			return errf("the start node cannot be deleted.")
		}
		w.hide(n)
		d := w.Decision(n.Decision)
		if !slices.ContainsFunc(d.Options, func(o string) bool { return !w.Node(o).Hidden }) {
			d.Hidden = true
		}
		// If "you are here" was deleted, move it up to the nearest node left.
		for w.Node(w.Here).Hidden {
			w.Here = w.ParentOf(w.Node(w.Here))
		}
		w.float()
		return nil
	})
}

func (t *Tree) hide(n *Node) {
	n.Hidden, n.Locked = true, true
	for _, d := range t.Decisions {
		if d.Parent == n.ID {
			d.Hidden = true
			for _, o := range d.Options {
				t.hide(t.Node(o))
			}
		}
	}
}

// Undo takes back Amir's last fix. Changes Claude made since then stay,
// unless the fix is what they grew from.
func (t *Tree) Undo() error {
	if len(t.Fixes) == 0 {
		return ErrNothingToUndo
	}
	ch := t.Fixes[len(t.Fixes)-1]
	t.Fixes = t.Fixes[:len(t.Fixes)-1]
	for _, n := range ch.Nodes {
		*t.Node(n.ID) = n
	}
	for _, d := range ch.Decisions {
		*t.Decision(d.ID) = d
	}
	if t.Here == ch.HereAfter || !t.pathOK(t.Here) {
		t.Here = ch.Here
	}
	return nil
}

// pathOK reports whether every node from id up to the start is picked and
// not hidden, so id can be "you are here".
func (t *Tree) pathOK(id string) bool {
	for ; id != ""; id = t.ParentOf(t.Node(id)) {
		n := t.Node(id)
		if n.Hidden || n.State != Picked {
			return false
		}
	}
	return true
}

// fix runs one fix on a copy of the tree. If it works, the copy replaces
// the tree, and what changed is saved for Undo.
func (t *Tree) fix(what string, f func(w *Tree) error) error {
	w := t.clone()
	if err := f(w); err != nil {
		return err
	}
	ch := Change{What: what, Here: t.Here, HereAfter: w.Here}
	for i, n := range w.Nodes {
		if *n != *t.Nodes[i] {
			ch.Nodes = append(ch.Nodes, *t.Nodes[i])
		}
	}
	for i, d := range w.Decisions {
		if !sameDecision(d, t.Decisions[i]) {
			old := *t.Decisions[i]
			old.Options = slices.Clone(old.Options)
			ch.Decisions = append(ch.Decisions, old)
		}
	}
	w.Fixes = append(w.Fixes, ch)
	if len(w.Fixes) > MaxUndo {
		w.Fixes = w.Fixes[len(w.Fixes)-MaxUndo:]
	}
	*t = *w
	return nil
}

func sameDecision(a, b *Decision) bool {
	return a.ID == b.ID && a.Topic == b.Topic && a.Parent == b.Parent &&
		a.At.Equal(b.At) && a.Hidden == b.Hidden && slices.Equal(a.Options, b.Options)
}
