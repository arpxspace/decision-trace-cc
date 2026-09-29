package tree

import (
	"fmt"
	"time"
)

// Call is one record_decision call from Claude. See PRD section 7.1.
type Call struct {
	Topic      string   // needed for a new decision
	Options    []string // every option talked about, winner included
	Picked     string   // the winner; "" while still weighing
	Reason     string   // why Picked won; needed with Picked
	By         string   // who made the call; needed with Picked
	DecisionID string   // update this decision instead of adding one
	DropLater  bool     // when changing a pick: set aside what was decided after it
	After      string   // grow a new decision from this node, not from "you are here"
	At         time.Time
}

// Result tells Claude what the call did.
type Result struct {
	DecisionID string
	Here       string   // node id of "you are here"
	Open       []string // decisions still being weighed on the live branch
	Left       []string // open decisions this call left behind on a dropped branch
	Skipped    []string // options left out because Amir deleted them
}

// Error is a mistake in a call. Its message is written for Claude to read,
// so it says what to do instead.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

func errf(format string, a ...any) error { return &Error{fmt.Sprintf(format, a...)} }

func lockedErr(n *Node) error {
	return errf("The user fixed %q (%s) by hand. Leave it as it is.", n.Label, n.ID)
}

// Record applies one call from Claude. If the call is wrong, it returns an
// Error and the tree does not change at all.
func (t *Tree) Record(c Call) (Result, error) {
	w := t.clone()
	res, err := w.record(c)
	if err != nil {
		return Result{}, err
	}
	*t = *w
	return res, nil
}

func (t *Tree) record(c Call) (Result, error) {
	openBefore := t.Open()
	var opts []string
	for _, o := range c.Options {
		if o = clean(o); o != "" {
			opts = append(opts, o)
		}
	}
	picked := clean(c.Picked)
	if picked != "" {
		if clean(c.Reason) == "" {
			return Result{}, errf("reason is needed with picked: one line on why %q won.", picked)
		}
		switch c.By {
		case ByUser, ByClaude, ByBoth:
		default:
			return Result{}, errf(`by is needed with picked: "user", "claude", or "both".`)
		}
	}

	var d *Decision
	if c.DecisionID == "" {
		if clean(c.Topic) == "" {
			return Result{}, errf(`topic is needed for a new decision: a short statement like "Database used".`)
		}
		if len(opts) == 0 {
			return Result{}, errf("options is needed: list every option that was talked about.")
		}
		if picked != "" && !hasLabel(opts, picked) {
			return Result{}, errf("picked %q is not one of the options. Add it to options.", picked)
		}
		parent := t.Here
		if c.After != "" {
			n := t.Node(c.After)
			if n == nil || n.Hidden {
				return Result{}, errf("after: there is no node %q. Call show_decision_tree to see the ids.", c.After)
			}
			if !t.LivePath()[n.ID] {
				return Result{}, errf("after: %q (%s) is not on the live branch. To go back to an option that was not picked, call with its decision_id and picked instead.", n.Label, n.ID)
			}
			if err := t.dropBranch(n.ID, clean(c.Reason), c.At, false); err != nil {
				return Result{}, err
			}
			parent = n.ID
		}
		d = t.addDecision(clean(c.Topic), parent, c.At)
	} else {
		d = t.Decision(c.DecisionID)
		if d == nil {
			return Result{}, errf("there is no decision %q. Call show_decision_tree to see the ids.", c.DecisionID)
		}
		if d.Hidden {
			return Result{}, errf("decision %s was deleted by the user. Leave it out.", d.ID)
		}
	}

	// New options join as "being weighed", or as "not picked" when the
	// decision was already made.
	state := Weighing
	if t.decided(d) {
		state = NotPicked
	}
	var skipped []string
	for _, o := range opts {
		if n := t.optionByLabel(d, o); n != nil {
			if n.Hidden {
				skipped = append(skipped, o)
			}
			continue
		}
		t.addOption(d, o, state, c.At)
	}

	if picked != "" {
		x := t.optionByLabel(d, picked)
		if x == nil {
			return Result{}, errf("picked %q is not an option of %s. Add it to options.", picked, d.ID)
		}
		if err := t.pick(d, x, clean(c.Reason), c.By, c.At, false, c.DropLater); err != nil {
			return Result{}, err
		}
	}
	t.float()

	res := Result{DecisionID: d.ID, Here: t.Here, Skipped: skipped}
	openNow := map[string]bool{}
	for _, o := range t.Open() {
		res.Open = append(res.Open, o.ID)
		openNow[o.ID] = true
	}
	// Still open, but no longer on the live branch: going back left it
	// behind. Claude should hear about it, or it may ask the question again.
	for _, o := range openBefore {
		if !openNow[o.ID] && t.IsOpen(o) {
			res.Left = append(res.Left, o.ID)
		}
	}
	return res, nil
}

// pick makes x the winner of d.
//
// First pick: d moves down to "you are here", so the tree reads in the
// order things were decided.
//
// Changing an earlier pick (d was decided before) happens in place: the old
// pick is marked changed (Dropped), and the decisions made after it still
// stand, so they move over to x. With dropLater, they are set aside instead,
// in their own lane under the old pick, for when they depended on it.
//
// fix is true for Amir's own fixes, which may change locked nodes.
func (t *Tree) pick(d *Decision, x *Node, reason, by string, at time.Time, fix, dropLater bool) error {
	if x.Hidden {
		return errf("%q was deleted by the user. Leave it out.", x.Label)
	}
	old := t.PickOf(d)
	if old == x {
		if !x.Locked || fix {
			x.Reason, x.By, x.At = reason, by, at
		}
		return nil
	}
	if x.Locked && !fix {
		return lockedErr(x)
	}
	if x.State == Dropped && t.hasChildren(x.ID) && !fix {
		return errf("%q heads a branch that was set aside. To try it again, start a new decision.", x.Label)
	}

	switch {
	case !t.decided(d):
		d.Parent = t.Here
	case !t.LivePath()[d.Parent]:
		return errf("decision %s is on a branch that was set aside. Start a new decision instead.", d.ID)
	case old != nil && old.Locked && !fix:
		return lockedErr(old)
	case dropLater:
		if err := t.dropBranch(d.Parent, reason, at, fix); err != nil {
			return err
		}
		if old != nil && old.State == Picked {
			old.State, old.DropReason, old.At = Dropped, reason, at
		}
	case old != nil:
		old.State, old.DropReason, old.At = Dropped, reason, at
		for _, c := range t.Decisions {
			if c.Parent == old.ID {
				c.Parent = x.ID
			}
		}
	}

	for _, id := range d.Options {
		n := t.Node(id)
		if n != x && !n.Hidden && n.State == Weighing && (!n.Locked || fix) {
			n.State, n.At = NotPicked, at
		}
	}
	x.State, x.By, x.At, x.DropReason = Picked, by, at, ""
	if reason != "" {
		x.Reason = reason
	}
	// "You are here" moves to x, unless it is already further down x's line
	// (a change in place keeps it where it was).
	if !t.LivePath()[x.ID] {
		t.Here = x.ID
	}
	return nil
}

func hasLabel(labels []string, l string) bool {
	for _, o := range labels {
		if sameLabel(o, l) {
			return true
		}
	}
	return false
}
