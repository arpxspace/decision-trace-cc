// Package graph lays a tree out as a git-style graph, one row per line,
// oldest at the top:
//
//	●  Notes app
//	│
//	├─○  Postgres                  not picked: a stub off the main line
//	●  Database used: SQLite       picked: the main line goes on from here
//	│
//	├─╮                            a dropped branch gets its own lane
//	│ ✗  Front end: React
//	●  Front end: HTML + htmx
//	│
//	┊  Deploy time: ?              a decision still being weighed
//	├─◌  Tonight
//	╰─◌  Now
//
// A stub hangs from the node its decision grows from, so the graph reads
// like git: the fork is at the parent, and the pick carries the line on.
package graph

import (
	"strconv"
	"strings"

	"decision-tree/internal/tree"
)

// Kind is what a row shows.
type Kind int

const (
	Node Kind = iota // the start, or an option; the cursor stops here
	Line             // only lines: "│" between decisions, "├─╮" into a lane
	Open             // the heading of a decision still being weighed
)

// Row is one line of the graph.
type Row struct {
	Kind   Kind
	Graph  string     // the lines left of the symbol, like "│ ├─"
	Symbol string     // ● ○ ◌ ✗ for nodes
	Text   string     // "Database used: SQLite"
	NodeID string     // for Node rows
	State  tree.State // for Node rows
	Here   bool       // "you are here"
	Folded int        // how many nodes are folded away under this one
}

// Symbols for each state.
var Symbols = map[tree.State]string{
	tree.Picked:    "●",
	tree.NotPicked: "○",
	tree.Weighing:  "◌",
	tree.Dropped:   "✗",
}

// Layout draws the visible tree. folded says which nodes hide what grows
// from them; nil folds nothing.
func Layout(t *tree.Tree, folded func(id string) bool) []Row {
	if folded == nil {
		folded = func(string) bool { return false }
	}
	l := &layout{t: t, folded: folded}
	l.node(t.Root(), "", "")
	return l.rows
}

// HasChildren reports whether any visible decision grows from the node, so
// there is something to fold.
func HasChildren(t *tree.Tree, id string) bool {
	for _, d := range t.Decisions {
		if d.Parent == id && !d.Hidden && visibleOptions(t, d) > 0 {
			return true
		}
	}
	return false
}

// DefaultFolded is how a node starts: a dropped branch is folded, so the
// live path stays short. Everything else is open.
func DefaultFolded(t *tree.Tree, id string) bool {
	n := t.Node(id)
	return n != nil && n.State == tree.Dropped && HasChildren(t, id)
}

type layout struct {
	t      *tree.Tree
	folded func(string) bool
	rows   []Row
}

// node draws n on the lane that starts with pre, then everything that grows
// from it. lead is the connector right before n's symbol: "" on the main
// line of a lane, "├─" or "╰─" for a stub.
func (l *layout) node(n *tree.Node, pre, lead string) {
	r := Row{Kind: Node, Graph: pre + lead, Symbol: l.symbol(n), Text: l.text(n, lead != ""), NodeID: n.ID, State: n.State, Here: n.ID == l.t.Here}
	if !HasChildren(l.t, n.ID) {
		l.rows = append(l.rows, r)
		return
	}
	if l.folded(n.ID) {
		r.Folded = l.count(n.ID)
		l.rows = append(l.rows, r)
		return
	}
	l.rows = append(l.rows, r)
	l.children(n.ID, pre)
}

// children draws the decisions that grow from node id, on the lane pre.
func (l *layout) children(id, pre string) {
	var decs []*tree.Decision
	for _, d := range l.t.Decisions {
		if d.Parent == id && !d.Hidden && visibleOptions(l.t, d) > 0 {
			decs = append(decs, d)
		}
	}
	// The last decision with a pick carries the lane on; draw it last.
	cont := -1
	for i, d := range decs {
		if l.t.PickOf(d) != nil {
			cont = i
		}
	}
	if cont >= 0 {
		decs = append(append(decs[:cont:cont], decs[cont+1:]...), decs[cont])
	}

	for i, d := range decs {
		carries := cont >= 0 && i == len(decs)-1
		pick := l.t.PickOf(d)
		l.rows = append(l.rows, Row{Kind: Line, Graph: pre + "│"})
		if l.t.IsOpen(d) {
			l.rows = append(l.rows, Row{Kind: Open, Graph: pre + "┊", Text: d.Topic + ": ?"})
		}
		var side []*tree.Node
		for _, oid := range d.Options {
			if o := l.t.Node(oid); !o.Hidden && !(carries && o == pick) {
				side = append(side, o)
			}
		}
		for j, o := range side {
			// The last stub closes the lane with "╰─", unless the lane goes on.
			last := j == len(side)-1 && i == len(decs)-1 && !carries
			fork, down := "├─", "│ "
			if last {
				fork, down = "╰─", "  "
			}
			if HasChildren(l.t, o.ID) {
				l.rows = append(l.rows, Row{Kind: Line, Graph: pre + fork + "╮"})
				l.node(o, pre+down, "")
			} else {
				l.node(o, pre, fork)
			}
		}
		if carries {
			l.node(pick, pre, "")
		}
	}
}

func (l *layout) symbol(n *tree.Node) string {
	if n.ID == tree.RootID {
		return "●"
	}
	return Symbols[n.State]
}

// text is how a node reads. A pick, or a node heading its own lane, reads
// as a statement: "Database used: SQLite". A stub is just its label, since
// the pick next to it names the topic.
func (l *layout) text(n *tree.Node, stub bool) string {
	switch {
	case n.ID == tree.RootID:
		if n.Label == "" {
			return "start"
		}
		return n.Label
	case n.State == tree.Picked || n.State == tree.Dropped || !stub:
		return l.t.Statement(n)
	}
	return n.Label
}

// count is how many visible nodes grow from id, all the way down.
func (l *layout) count(id string) int {
	n := 0
	for _, d := range l.t.Decisions {
		if d.Parent != id || d.Hidden {
			continue
		}
		for _, oid := range d.Options {
			if o := l.t.Node(oid); !o.Hidden {
				n += 1 + l.count(o.ID)
			}
		}
	}
	return n
}

func visibleOptions(t *tree.Tree, d *tree.Decision) int {
	n := 0
	for _, id := range d.Options {
		if !t.Node(id).Hidden {
			n++
		}
	}
	return n
}

// Plain draws rows as text with no colors, for `decision-tree print`.
func Plain(rows []Row) string {
	var b strings.Builder
	for _, r := range rows {
		b.WriteString(r.Graph)
		if r.Symbol != "" {
			b.WriteString(r.Symbol + "  ")
		} else if r.Text != "" {
			b.WriteString("  ")
		}
		b.WriteString(r.Text)
		if r.Folded > 0 {
			b.WriteString(" ▸ " + strconv.Itoa(r.Folded) + " more")
		}
		if r.Here {
			b.WriteString("  ◀")
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
