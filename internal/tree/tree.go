// Package tree holds one session's decision tree and the rules for changing
// it. Claude changes it through Record. Amir changes it through the Fix
// functions. Nothing here touches the disk; package store does that.
package tree

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Version is the tree file format this code writes.
const Version = 1

// State is where an option stands.
type State string

const (
	Weighing  State = "weighing"   // on the table, nobody has picked yet
	Picked    State = "picked"     // won; the talk went on from here
	NotPicked State = "not_picked" // talked about and lost
	Dropped   State = "dropped"    // picked, then given up later
)

// Who made a call.
const (
	ByUser   = "user"
	ByClaude = "claude"
	ByBoth   = "both"
)

// Node is the root or one option of a decision.
type Node struct {
	ID         string    `json:"id"`
	Decision   string    `json:"decision,omitempty"` // "" only for the root
	Label      string    `json:"label"`
	State      State     `json:"state"`
	Reason     string    `json:"reason,omitempty"` // why it was picked
	By         string    `json:"by,omitempty"`     // who picked it
	At         time.Time `json:"at"`               // when it was added, or last changed state
	DropReason string    `json:"drop_reason,omitempty"`
	Locked     bool      `json:"locked,omitempty"` // Amir fixed it; Record must not change it
	Hidden     bool      `json:"hidden,omitempty"` // Amir deleted it

	// Checkpoint is what a branch needs to start from the moment this
	// option was picked (PRD 17.1). Package checkpoint fills it in.
	Checkpoint Checkpoint `json:"checkpoint,omitzero"`
}

// Checkpoint is the state at the moment an option was picked.
type Checkpoint struct {
	ToolUseID string    `json:"tool_use_id,omitempty"` // the record_decision call: finds where to cut the chat
	Repo      string    `json:"repo,omitempty"`        // the git repo's top folder
	Commit    string    `json:"commit,omitempty"`      // snapshot of the working folder, uncommitted work included
	Ref       string    `json:"ref,omitempty"`         // the hidden ref that keeps the snapshot
	Memory    string    `json:"memory,omitempty"`      // copy of Claude's memory for the project
	Tree      string    `json:"tree,omitempty"`        // copy of the whole tree at that moment
	At        time.Time `json:"at,omitzero"`
	Missing   string    `json:"missing,omitempty"` // what could not be saved, in plain words; "" = nothing
}

// Decision is what is being decided and its options. It grows from one node.
type Decision struct {
	ID      string    `json:"id"`
	Topic   string    `json:"topic"`   // a short statement, like "Database used"
	Parent  string    `json:"parent"`  // the node it grows from
	Options []string  `json:"options"` // node ids, in the order they came up
	At      time.Time `json:"at"`
	Hidden  bool      `json:"hidden,omitempty"`
}

// UnmarshalJSON also reads trees saved before 30 Sep 2026, which called the
// topic "question".
func (d *Decision) UnmarshalJSON(b []byte) error {
	type plain Decision
	var v struct {
		plain
		Question string `json:"question"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*d = Decision(v.plain)
	if d.Topic == "" {
		d.Topic = v.Question
	}
	return nil
}

// Statement is how a picked or dropped option reads: "Database used: SQLite".
func (t *Tree) Statement(n *Node) string {
	if n.Decision == "" {
		return n.Label
	}
	return t.Decision(n.Decision).Topic + ": " + n.Label
}

// Tree is one session's tree. Nothing is ever removed from it, only hidden,
// so ids never change meaning.
type Tree struct {
	Version   int         `json:"version"`
	SessionID string      `json:"session_id"`
	Folder    string      `json:"folder,omitempty"` // where the session runs
	Here      string      `json:"here"`             // "you are here": the newest pick on the live branch
	Nodes     []*Node     `json:"nodes"`
	Decisions []*Decision `json:"decisions"`
	Fixes     []Change    `json:"fixes,omitempty"`  // Amir's fixes, newest last, for Undo
	Branch    *Branch     `json:"branch,omitempty"` // set when this session is a branch of another
}

// Branch says where a branched session came from (PRD 17.3).
type Branch struct {
	Name         string    `json:"name"`
	FromSession  string    `json:"from_session"`
	FromNode     string    `json:"from_node"`
	FromDecision string    `json:"from_decision"`
	Inherited    int       `json:"inherited"`   // decisions it started with, copied from the original
	Statement    string    `json:"statement"`   // "API framework: FastAPI"
	CutMessage   string    `json:"cut_message"` // the chat entry the branch resumed at
	Commit       string    `json:"commit,omitempty"`
	Repo         string    `json:"repo,omitempty"`     // the original folder
	Worktree     string    `json:"worktree,omitempty"` // "" = it shares the original folder
	At           time.Time `json:"at"`
	Missing      string    `json:"missing,omitempty"` // what is not exact, in plain words
}

// UpTo is a copy of the tree as far as node id: everything that grew after
// it is hidden, and id is "you are here". Node and decision ids stay the
// same, so the chat's references to them still work.
//
// It is only a fallback for a branch when no copy of the tree was saved at
// the time: a decision above id that was changed later shows its later pick.
func (t *Tree) UpTo(id string) *Tree {
	c := t.clone()
	path := c.pathTo(id)
	for _, d := range c.Decisions {
		onPath := false
		for _, o := range d.Options {
			onPath = onPath || path[o]
		}
		if onPath {
			continue
		}
		d.Hidden = true
		for _, o := range d.Options {
			c.Node(o).Hidden = true
		}
	}
	c.Here, c.Fixes = id, nil
	return c
}

// pathTo is every node from id up to the start.
func (t *Tree) pathTo(id string) map[string]bool {
	path := map[string]bool{}
	for ; id != "" && !path[id]; id = t.ParentOf(t.Node(id)) {
		path[id] = true
	}
	return path
}

// RootID is the "start" node every tree has.
const RootID = "n0"

// New makes an empty tree: just the start node, with no label yet.
func New(sessionID string, at time.Time) *Tree {
	return &Tree{
		Version:   Version,
		SessionID: sessionID,
		Here:      RootID,
		Nodes:     []*Node{{ID: RootID, Label: "", State: Picked, At: at}},
	}
}

// Root is the start node.
func (t *Tree) Root() *Node { return t.Nodes[0] }

// Node finds a node by id, or returns nil.
func (t *Tree) Node(id string) *Node {
	for _, n := range t.Nodes {
		if n.ID == id {
			return n
		}
	}
	return nil
}

// Decision finds a decision by id, or returns nil.
func (t *Tree) Decision(id string) *Decision {
	for _, d := range t.Decisions {
		if d.ID == id {
			return d
		}
	}
	return nil
}

// ParentOf is the node that n's decision grows from. The root has none.
func (t *Tree) ParentOf(n *Node) string {
	if n.Decision == "" {
		return ""
	}
	return t.Decision(n.Decision).Parent
}

// LivePath is every node from "you are here" up to the start.
func (t *Tree) LivePath() map[string]bool {
	live := map[string]bool{}
	for id := t.Here; id != ""; id = t.ParentOf(t.Node(id)) {
		live[id] = true
	}
	return live
}

// PickOf is the decision's picked option, or nil.
func (t *Tree) PickOf(d *Decision) *Node {
	for _, id := range d.Options {
		if n := t.Node(id); !n.Hidden && n.State == Picked {
			return n
		}
	}
	return nil
}

// hasChildren reports whether a visible decision grows from node id.
func (t *Tree) hasChildren(id string) bool {
	for _, d := range t.Decisions {
		if d.Parent == id && !d.Hidden {
			return true
		}
	}
	return false
}

// decided reports whether one of d's options was ever picked.
func (t *Tree) decided(d *Decision) bool {
	for _, id := range d.Options {
		if n := t.Node(id); !n.Hidden && (n.State == Picked || n.State == Dropped) {
			return true
		}
	}
	return false
}

// IsOpen reports whether a decision is still being weighed: it has options
// on the table and none was ever picked.
func (t *Tree) IsOpen(d *Decision) bool {
	if d.Hidden || t.decided(d) {
		return false
	}
	for _, id := range d.Options {
		if n := t.Node(id); !n.Hidden && n.State == Weighing {
			return true
		}
	}
	return false
}

// Open lists the decisions still being weighed on the live branch.
func (t *Tree) Open() []*Decision {
	live := t.LivePath()
	var open []*Decision
	for _, d := range t.Decisions {
		if live[d.Parent] && t.IsOpen(d) {
			open = append(open, d)
		}
	}
	return open
}

// float moves open decisions on the live branch down to "you are here".
// Choices on the table stay at the bottom of the tree until one is picked.
func (t *Tree) float() {
	for _, d := range t.Open() {
		d.Parent = t.Here
	}
}

// childOnPath is the node right below p on the way down to "you are here".
func (t *Tree) childOnPath(p string) *Node {
	for id := t.Here; id != ""; {
		n := t.Node(id)
		parent := t.ParentOf(n)
		if parent == p {
			return n
		}
		id = parent
	}
	return nil
}

// dropBranch gives up everything below p on the live branch, and moves
// "you are here" back up to p. p must be on the live branch.
func (t *Tree) dropBranch(p, reason string, at time.Time, fix bool) error {
	if t.Here == p {
		return nil
	}
	c := t.childOnPath(p)
	if c.Locked && !fix {
		return lockedErr(c)
	}
	c.State, c.DropReason, c.At = Dropped, reason, at
	t.Here = p
	return nil
}

func (t *Tree) addDecision(topic, parent string, at time.Time) *Decision {
	d := &Decision{ID: fmt.Sprintf("d%d", len(t.Decisions)+1), Topic: topic, Parent: parent, At: at}
	t.Decisions = append(t.Decisions, d)
	return d
}

func (t *Tree) addOption(d *Decision, label string, s State, at time.Time) *Node {
	n := &Node{ID: fmt.Sprintf("n%d", len(t.Nodes)), Decision: d.ID, Label: label, State: s, At: at}
	t.Nodes = append(t.Nodes, n)
	d.Options = append(d.Options, n.ID)
	return n
}

// optionByLabel finds an option of d by its label, ignoring case and spaces.
func (t *Tree) optionByLabel(d *Decision, label string) *Node {
	for _, id := range d.Options {
		if n := t.Node(id); sameLabel(n.Label, label) {
			return n
		}
	}
	return nil
}

func (t *Tree) clone() *Tree {
	c := *t
	c.Nodes = make([]*Node, len(t.Nodes))
	for i, n := range t.Nodes {
		nn := *n
		c.Nodes[i] = &nn
	}
	c.Decisions = make([]*Decision, len(t.Decisions))
	for i, d := range t.Decisions {
		dd := *d
		dd.Options = slices.Clone(d.Options)
		c.Decisions[i] = &dd
	}
	c.Fixes = slices.Clone(t.Fixes)
	return &c
}

// clean trims a label and squeezes its inner spaces.
func clean(s string) string { return strings.Join(strings.Fields(s), " ") }

func sameLabel(a, b string) bool { return strings.EqualFold(clean(a), clean(b)) }
