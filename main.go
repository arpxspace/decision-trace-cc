// decision-tree draws the big choices of a Claude Code session as a tree.
// Claude logs them through an MCP tool; see PRD.md.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"decision-tree/internal/render"
	"decision-tree/internal/server"
	"decision-tree/internal/store"
	"decision-tree/internal/tree"
)

const version = "0.1.0"

const usage = `decision-tree: the big choices of a Claude Code session, as a tree

Usage:
  decision-tree mcp               run the MCP server (Claude Code starts this)
  decision-tree print [session]   print a tree: the newest one, or the session
                                  given (the start of its id is enough)
        --ids                     also show decision and node ids
  decision-tree list              list saved trees, newest first
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
	case "print":
		err = printTree(store.Default(), args[1:], stdout)
	case "list":
		err = list(store.Default(), stdout)
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
	var o render.Options
	o.Reasons = true
	session := ""
	for _, a := range args {
		switch a {
		case "--ids":
			o.IDs = true
		default:
			session = a
		}
	}
	id, err := s.Find(session)
	if err != nil {
		return err
	}
	t, err := s.Load(id)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "%s · session %s · %d decisions\n%s\n\n%s\n",
		folderName(t), short(id), countDecisions(t), render.Legend, render.Text(t, o))
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
