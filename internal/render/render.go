// Package render draws a tree as indented plain text, for
// `decision-tree print` and for the show_decision_tree tool. The split
// draws its own git-style picture (build step 5).
package render

import (
	"strings"

	"decision-tree/internal/tree"
)

// Options picks what to show besides the labels.
type Options struct {
	IDs     bool // node and decision ids, so Claude can refer to them
	Reasons bool // why each option won or was given up, for Amir
}

// Legend explains the symbols.
const Legend = "● picked  ○ not picked  ◌ being weighed  ✗ dropped  ◀ you are here"

var symbol = map[tree.State]string{
	tree.Picked:    "●",
	tree.NotPicked: "○",
	tree.Weighing:  "◌",
	tree.Dropped:   "✗",
}

// Label is a node's label, or "start" for an unnamed start node.
func Label(n *tree.Node) string {
	if n.Label == "" && n.ID == tree.RootID {
		return "start"
	}
	return n.Label
}

// Text draws the visible part of the tree, oldest at the top. Hidden
// (deleted) nodes and decisions are left out.
func Text(t *tree.Tree, o Options) string {
	var b strings.Builder
	var node func(n *tree.Node, depth int)
	node = func(n *tree.Node, depth int) {
		b.WriteString(strings.Repeat("  ", depth) + symbol[n.State] + " ")
		if o.IDs {
			b.WriteString(n.ID + " ")
		}
		b.WriteString(Label(n))
		if n.ID == t.Here {
			b.WriteString(" ◀")
		}
		if o.Reasons {
			switch {
			case n.State == tree.Picked && n.Reason != "":
				b.WriteString(" — " + n.Reason + " (" + n.By + ")")
			case n.State == tree.Dropped && n.DropReason != "":
				b.WriteString(" — dropped: " + n.DropReason)
			}
		}
		b.WriteString("\n")
		for _, d := range t.Decisions {
			if d.Parent != n.ID || d.Hidden {
				continue
			}
			b.WriteString(strings.Repeat("  ", depth+1))
			if o.IDs {
				b.WriteString(d.ID + ": ")
			}
			b.WriteString(d.Topic + "\n")
			for _, id := range d.Options {
				if opt := t.Node(id); !opt.Hidden {
					node(opt, depth+2)
				}
			}
		}
	}
	node(t.Root(), 0)
	return strings.TrimRight(b.String(), "\n")
}
