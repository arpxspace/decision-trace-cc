// decision-tree keeps the big choices of a Claude Code session as a tree.
// Claude logs them through a tool that the Claude Code mod in mod/ gives it;
// the mod runs this program to save them, and draws the tree in a pane.
// See PRD.md.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"decision-tree/internal/branch"
	"decision-tree/internal/claude"
	"decision-tree/internal/graph"
	"decision-tree/internal/render"
	"decision-tree/internal/server"
	"decision-tree/internal/store"
	"decision-tree/internal/tmux"
	"decision-tree/internal/tree"
)

const version = "0.1.0"

const usage = `decision-tree: the big choices of a Claude Code session, as a tree

Usage:
  decision-tree print [session]   print a tree: the newest one, or the session
                                  given (the start of its id is enough)
  decision-tree list              list saved trees, newest first
  decision-tree source <session> [node]
                                  where each pick came from in the chat, and
                                  its checkpoint (one node, or every pick)
  decision-tree branch <session> <node> [--name N] [--focus] [--yes]
                                  start a new Claude session from a decision,
                                  with the chat, code, and memory as they were
                                  then (node ids: see source). The original is
                                  never changed.
        --claude <path>           the claude program to run
        --tmux-socket <name>      a tmux server other than the default
  decision-tree record --session ID [--cwd DIR] [--tool-use-id ID]
                                  save one record_decision call, read as JSON
                                  from stdin, and print the reply. The
                                  Claude Code mod in mod/ runs this, and the
                                  next two.
  decision-tree show --session ID print the tree as show_decision_tree does
  decision-tree describe          print the rules and both tools as JSON
  decision-tree data --session ID print a tree, the branches made from it,
                                  and its file's path as JSON, for the mod's
                                  pane. source and branch take --json too:
                                  source <session> <node> --json, and
                                  branch ... --json with --plan (ask nothing,
                                  make nothing) or --yes (make it)
  decision-tree version
`

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	var err error
	switch args[0] {
	case "print":
		err = printTree(store.Default(), args[1:], stdout)
	case "list":
		err = list(store.Default(), stdout)
	case "source":
		err = source(store.Default(), claude.Dir(), args[1:], stdout)
	case "branch":
		err = branchCmd(args[1:], stdin, stdout)
	case "record", "show":
		err = toolCmd(server.New(), args[0], args[1:], stdin, stdout)
	case "describe":
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		err = enc.Encode(server.Describe())
	case "data":
		err = dataCmd(store.Default(), args[1:], stdout)
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
	fmt.Fprintf(w, "%s · session %s · %d decisions\n", folderName(t), short(id), countDecisions(t))
	if b := t.Branch; b != nil {
		fmt.Fprintf(w, "Branch of session %s, from %q (%s)\n", short(b.FromSession), b.Statement, b.At.Local().Format("02 Jan 15:04"))
	}
	fmt.Fprintf(w, "\n%s\n", graph.Plain(graph.Layout(t, nil, branch.Children(s, id))))
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

// branchCmd makes a branch from a decision (PRD 17.2), after showing what
// it will start with and asking.
func branchCmd(args []string, in io.Reader, w io.Writer) error {
	d := branchDeps(store.Default())
	var pos []string
	var name string
	var focus, yes, plan, asJSON bool
	for i := 0; i < len(args); i++ {
		switch a := args[i]; a {
		case "--focus":
			focus = true
		case "--yes", "-y":
			yes = true
		case "--plan":
			plan = true
		case "--json":
			asJSON = true
		case "--name", "--claude", "--tmux-socket":
			if i+1 >= len(args) {
				return fmt.Errorf("%s needs a value", a)
			}
			i++
			switch a {
			case "--name":
				name = branch.Slug(args[i])
			case "--claude":
				d.Claude = args[i]
			case "--tmux-socket":
				d.Tmux.Socket = args[i]
			}
		default:
			pos = append(pos, a)
		}
	}
	if len(pos) != 2 {
		return fmt.Errorf("usage: decision-tree branch <session> <node> [--name N] [--focus] [--yes]")
	}
	if asJSON {
		if plan == yes {
			return errors.New("--json needs --plan or --yes")
		}
		return branchJSON(d, pos[0], pos[1], name, plan, focus, w)
	}
	p, err := branch.Prepare(d, pos[0], pos[1], name)
	if err != nil {
		return err
	}
	fmt.Fprintln(w, p.Summary())
	if !yes {
		fmt.Fprint(w, "\nCreate branch? [y/N] ")
		answer, _ := bufio.NewReader(in).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
			fmt.Fprintln(w, "No branch made.")
			return nil
		}
	}
	fmt.Fprintln(w, "\nMaking the branch (the chat fork takes a few seconds)…")
	res, err := branch.Create(context.Background(), d, p, focus)
	if err != nil {
		if res.Command != "" {
			fmt.Fprintf(w, "To start it yourself:\n  %s\n", res.Command)
		}
		return err
	}
	fmt.Fprintf(w, "\nCreated branch: %s\nSession: %s\nFolder: %s\n", p.Name, p.Session, p.Dir)
	if len(res.Copied) > 0 {
		fmt.Fprintf(w, "Copied from .worktreeinclude: %s\n", strings.Join(res.Copied, ", "))
	}
	switch {
	case res.Pane != "":
		fmt.Fprintf(w, "tmux: a new window named %s (pane %s)\n", p.Name, res.Pane)
		fmt.Fprintln(w, "The first start in a new folder may ask you to trust it.")
	case res.Command != "":
		fmt.Fprintf(w, "tmux is not running. To start the branch:\n  %s\n", res.Command)
	}
	return nil
}

// toolCmd answers record_decision or show_decision_tree for the mod (PRD
// 18). The mod knows the session, so it says which one; the MCP server has
// to look it up.
func toolCmd(srv *server.Server, name string, args []string, in io.Reader, w io.Writer) error {
	sess := claude.Session{}
	var toolUseID string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a != "--session" && a != "--cwd" && a != "--tool-use-id" {
			return fmt.Errorf("unknown flag %q", a)
		}
		if i+1 >= len(args) {
			return fmt.Errorf("%s needs a value", a)
		}
		i++
		switch a {
		case "--session":
			sess.ID = args[i]
		case "--cwd":
			sess.Cwd = args[i]
		case "--tool-use-id":
			toolUseID = args[i]
		}
	}
	if sess.ID == "" {
		return fmt.Errorf("--session is needed")
	}
	if name == "show" {
		fmt.Fprintln(w, srv.Show(sess.ID))
		return nil
	}
	var call server.RecordInput
	if err := json.NewDecoder(in).Decode(&call); err != nil {
		return fmt.Errorf("the call should come as JSON on stdin: %v", err)
	}
	reply, err := srv.Record(sess, call, toolUseID)
	if err != nil {
		return err
	}
	fmt.Fprintln(w, reply)
	return nil
}

// paneData is what the mod's pane draws for one session (PRD 19).
type paneData struct {
	Session  string                  `json:"session"`
	Path     string                  `json:"path"` // the tree file, so the pane can tell when it changes
	Tree     *tree.Tree              `json:"tree"` // nil when the session has no tree yet
	Branches map[string][]graph.Stub `json:"branches"`
}

// dataCmd prints a session's tree and the branches made from it as JSON.
// The session may be the start of an id, as for print.
func dataCmd(s store.Store, args []string, w io.Writer) error {
	if len(args) != 2 || args[0] != "--session" || args[1] == "" {
		return errors.New("usage: decision-tree data --session ID")
	}
	id := args[1]
	path, err := s.Path(id)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if found, err := s.Find(id); err == nil {
			id = found
			path, _ = s.Path(id)
		}
	}
	out := paneData{Session: id, Path: path, Branches: map[string][]graph.Stub{}}
	t, err := s.Load(id)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return err
	default:
		t.Fixes = nil // only the view's own undo needs them
		out.Tree = t
		out.Branches = branch.Children(s, id)
	}
	return json.NewEncoder(w).Encode(out)
}

// branchJSON is branch for the mod's pane: --plan prints what a branch
// would start with and makes nothing; otherwise it makes the branch and
// prints what it made. A failure prints the error, and the command to start
// the branch by hand when there is one.
func branchJSON(d branch.Deps, session, node, name string, plan, focus bool, w io.Writer) error {
	enc := json.NewEncoder(w)
	p, err := branch.Prepare(d, session, node, name)
	if err != nil {
		return err
	}
	if plan {
		return enc.Encode(map[string]string{"name": p.Name, "summary": p.Summary()})
	}
	res, err := branch.Create(context.Background(), d, p, focus)
	out := map[string]any{"name": p.Name, "session": p.Session, "dir": p.Dir,
		"pane": res.Pane, "command": res.Command, "copied": res.Copied}
	if err != nil {
		out["error"] = err.Error()
	}
	if encErr := enc.Encode(out); encErr != nil {
		return encErr
	}
	return err
}

func branchDeps(st store.Store) branch.Deps {
	return branch.Deps{
		Store: st, ClaudeDir: claude.Dir(), Tmux: tmux.Tmux{Bin: tmux.FindBin()},
		Worktrees: filepath.Join(st.Dir, "worktrees"), Now: time.Now,
	}
}

// findSource finds where the pick node of session came from in the chat.
func findSource(st store.Store, claudeDir, session, node string) (claude.Cut, error) {
	t, err := st.Load(session)
	if err != nil {
		return claude.Cut{}, err
	}
	n := t.Node(node)
	if n == nil || n.Checkpoint.ToolUseID == "" {
		return claude.Cut{}, errors.New("not saved: this was logged before checkpoints existed")
	}
	chat, err := claude.ChatPath(claudeDir, session)
	if err != nil {
		return claude.Cut{}, errors.New("the chat file is not on this computer")
	}
	return claude.FindCut(chat, n.Checkpoint.ToolUseID)
}

// source prints where a decision came from, and what a branch from it
// would start with (PRD 17).
func source(s store.Store, claudeDir string, args []string, w io.Writer) error {
	asJSON := len(args) > 0 && args[len(args)-1] == "--json"
	if asJSON {
		args = args[:len(args)-1]
	}
	if len(args) < 1 || len(args) > 2 || asJSON && len(args) != 2 {
		return fmt.Errorf("usage: decision-tree source <session> [node], or source <session> <node> --json")
	}
	id, err := s.Find(args[0])
	if err != nil {
		return err
	}
	if asJSON {
		// For the mod's pane: where one pick came from in the chat, or why
		// that is not known.
		out := map[string]any{}
		if cut, err := findSource(s, claudeDir, id, args[1]); err != nil {
			out["error"] = err.Error()
		} else {
			out["prompt_number"], out["prompt"], out["before"] = cut.PromptNumber, cut.Prompt, cut.Before
		}
		return json.NewEncoder(w).Encode(out)
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
