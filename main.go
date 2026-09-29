// decision-tree draws the big choices of a Claude Code session as a tree.
// Claude logs them through an MCP tool; see PRD.md.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"decision-tree/internal/claude"
	"decision-tree/internal/graph"
	"decision-tree/internal/render"
	"decision-tree/internal/server"
	"decision-tree/internal/store"
	"decision-tree/internal/tmux"
	"decision-tree/internal/tree"
	"decision-tree/internal/ui"
)

const version = "0.1.0"

const usage = `decision-tree: the big choices of a Claude Code session, as a tree

Usage:
  decision-tree view [session]    the live view. With no session, it follows
                                  the tmux pane you are in. Keys: j/k move,
                                  space fold, . you-are-here, f follow, q quit
  decision-tree print [session]   print a tree: the newest one, or the session
                                  given (the start of its id is enough)
  decision-tree list              list saved trees, newest first
  decision-tree source <session> [node]
                                  where each pick came from in the chat, and
                                  its checkpoint (one node, or every pick)
  decision-tree mcp               run the MCP server (Claude Code starts this)
  decision-tree version
`

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	var err error
	switch args[0] {
	case "mcp":
		err = server.New().MCP(version).Run(context.Background(), &mcp.StdioTransport{})
	case "view":
		err = view(args[1:])
	case "print":
		err = printTree(store.Default(), args[1:], stdout)
	case "list":
		err = list(store.Default(), stdout)
	case "source":
		err = source(store.Default(), claude.Dir(), args[1:], stdout)
	case "version":
		fmt.Fprintln(stdout, version)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", args[0], usage)
		return 2
	}
	if err != nil {
		fmt.Fprintln(stderr, "decision-tree:", err)
		return 1
	}
	return 0
}

func view(args []string) error {
	s := store.Default()
	pinned := ""
	if len(args) > 0 {
		id, err := s.Find(args[0])
		if err != nil {
			return err
		}
		pinned = id
	}
	dir, tm := claude.Dir(), tmux.Tmux{Bin: tmux.FindBin()}
	active := func() (claude.Session, bool) {
		pane, err := tm.ActivePane()
		if err != nil {
			return claude.Session{}, false
		}
		return claude.InPane(dir, pane)
	}
	p := tea.NewProgram(ui.New(ui.Deps{Store: s, Active: active, Now: time.Now}, pinned),
		tea.WithAltScreen(), tea.WithFPS(20))
	// Closing the iTerm2 pane sends SIGHUP; quit cleanly then.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGHUP)
	go func() { <-sig; p.Quit() }()
	_, err := p.Run()
	return err
}

func printTree(s store.Store, args []string, w io.Writer) error {
	session := ""
	if len(args) > 0 {
		session = args[0]
	}
	id, err := s.Find(session)
	if err != nil {
		return err
	}
	t, err := s.Load(id)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "%s · session %s · %d decisions\n\n%s\n", folderName(t), short(id), countDecisions(t),
		graph.Plain(graph.Layout(t, nil)))
	var why []string
	for _, n := range t.Nodes {
		switch {
		case n.Hidden:
		case n.State == tree.Picked && n.Reason != "":
			why = append(why, fmt.Sprintf("  %s — %s (%s)", t.Statement(n), n.Reason, n.By))
		case n.State == tree.Dropped && n.DropReason != "":
			why = append(why, fmt.Sprintf("  %s — dropped: %s", t.Statement(n), n.DropReason))
		}
	}
	if len(why) > 0 {
		fmt.Fprintf(w, "\nWhy\n%s\n", strings.Join(why, "\n"))
	}
	return nil
}

// source prints where a decision came from, and what a branch from it
// would start with (PRD 17).
func source(s store.Store, claudeDir string, args []string, w io.Writer) error {
	if len(args) < 1 || len(args) > 2 {
		return fmt.Errorf("usage: decision-tree source <session> [node]")
	}
	id, err := s.Find(args[0])
	if err != nil {
		return err
	}
	t, err := s.Load(id)
	if err != nil {
		return err
	}
	if len(args) == 2 {
		n := t.Node(args[1])
		if n == nil || n.Hidden {
			return fmt.Errorf("there is no node %s in session %s", args[1], short(id))
		}
		return sourceOf(t, n, claudeDir, w)
	}
	first := true
	for _, n := range t.Nodes {
		if n.Hidden || n.Decision == "" || (n.State != tree.Picked && n.State != tree.Dropped) {
			continue
		}
		if !first {
			fmt.Fprintln(w)
		}
		first = false
		if err := sourceOf(t, n, claudeDir, w); err != nil {
			return err
		}
	}
	return nil
}

func sourceOf(t *tree.Tree, n *tree.Node, claudeDir string, w io.Writer) error {
	id := t.SessionID
	cp := n.Checkpoint
	fmt.Fprintf(w, "%s (%s) · %s\n", t.Statement(n), n.ID, n.At.Local().Format("02 Jan 15:04"))
	if cp.ToolUseID == "" {
		fmt.Fprintln(w, "No checkpoint: this was logged before checkpoints existed, or it was never picked.")
		return nil
	}
	chat, err := claude.ChatPath(claudeDir, id)
	var cut claude.Cut
	if err == nil {
		cut, err = claude.FindCut(chat, cp.ToolUseID)
	}
	if err != nil {
		fmt.Fprintf(w, "Chat: not found (%v)\n", err)
	} else {
		fmt.Fprintf(w, "After your message #%d: %q\n", cut.PromptNumber, cut.Prompt)
		if cut.Before != "" {
			fmt.Fprintf(w, "Claude said before: %q\n", cut.Before)
		}
		fmt.Fprintf(w, "A branch would cut the chat at: %s\n", cut.UUID)
	}
	switch {
	case cp.Commit != "":
		fmt.Fprintf(w, "Code: %s in %s (%s)\n", cp.Commit[:min(12, len(cp.Commit))], cp.Repo, cp.Ref)
	default:
		fmt.Fprintln(w, "Code: not available")
	}
	if cp.Memory != "" {
		fmt.Fprintf(w, "Memory: %s\n", cp.Memory)
	} else {
		fmt.Fprintln(w, "Memory: none saved")
	}
	if cp.Missing != "" {
		fmt.Fprintf(w, "Missing: %s\n", cp.Missing)
	}
	return nil
}

func list(s store.Store, w io.Writer) error {
	infos, err := s.List()
	if err != nil {
		return err
	}
	if len(infos) == 0 {
		fmt.Fprintln(w, "No trees saved yet.")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "UPDATED\tSESSION\tFOLDER\tDECISIONS\tSTART")
	for _, in := range infos {
		t, err := s.Load(in.SessionID)
		if err != nil {
			fmt.Fprintf(tw, "%s\t%s\t?\t?\t(cannot read: %v)\n", in.Updated.Format("02 Jan 15:04"), short(in.SessionID), err)
			continue
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\n", in.Updated.Format("02 Jan 15:04"), short(in.SessionID),
			folderName(t), countDecisions(t), render.Label(t.Root()))
	}
	return tw.Flush()
}

func countDecisions(t *tree.Tree) int {
	n := 0
	for _, d := range t.Decisions {
		if !d.Hidden {
			n++
		}
	}
	return n
}

func folderName(t *tree.Tree) string {
	if t.Folder == "" {
		return "?"
	}
	return filepath.Base(t.Folder)
}

// short is the first 8 characters of a session id, like a short git hash.
func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
