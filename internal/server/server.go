// Package server is the MCP server Claude Code starts once per session. It
// gives Claude two tools, record_decision and show_decision_tree, plus the
// rules for when to use them (PRD sections 6 and 7).
package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"decision-tree/internal/claude"
	"decision-tree/internal/render"
	"decision-tree/internal/store"
	"decision-tree/internal/tree"
)

// Instructions are the rules from PRD section 6. Claude Code shows them in
// every session, even before the tools are loaded. It cuts them off at
// 2,048 characters, so they must stay shorter (a test checks).
const Instructions = `decision-tree draws the big choices of this session as a tree. The user watches it in a side pane, so keep it right by calling record_decision.

Call record_decision when:
- options for a project-level choice are laid out (leave picked empty),
- one of them is picked (decision_id + picked),
- a choice is made at once (question, options, and picked in one call),
- you or the user go back on an earlier choice (decision_id of that decision + the new picked).

Project-level means it changes what gets built or how:
- a tool or technology ("Postgres, not SQLite")
- an approach ("add an index, not a cache")
- scope ("login is out of version 1")
- a design direction ("oldest on top, not newest on top")

Do not log small choices: names, the order of work, which command to run, or one step inside a bigger topic. Many turns about one topic, and answers to AskUserQuestion, are one decision: log it once when the options are laid out, and once when one wins. When unsure, leave it out.

Log quietly. Do not mention the tree or this tool in your replies unless the user asks about it.

Only the main conversation logs decisions, not sub-agents. Keep labels short (under 8 words). If you lose track of the ids, call show_decision_tree.`

const recordDescription = `Log a project-level decision in this session's decision tree. The server instructions say what counts.

- New decision, still being weighed: question + options.
- New decision, already made: question + options + picked + reason + by.
- Pick on an open decision: decision_id + picked + reason + by.
- More options came up: decision_id + options.
- Going back to an option that was not picked: decision_id of that earlier decision + picked + reason + by. The branch it replaces is marked dropped.
- A new decision from an earlier point, dropping everything after it: question + options + after (the node id to grow from).

The reply gives the decision id, where "you are here" is, and the decisions still open.`

const showDescription = `Show this session's decision tree as text, with decision ids (d1) and node ids (n1).`

// alwaysLoad asks Claude Code not to hold the tool back (docs/findings.md, part 2).
var alwaysLoad = mcp.Meta{"anthropic/alwaysLoad": true}

// RecordInput is what Claude sends to record_decision. See tree.Call.
type RecordInput struct {
	Question   string   `json:"question,omitempty" jsonschema:"The decision as a short question. Needed for a new decision."`
	Options    []string `json:"options,omitempty" jsonschema:"Every option talked about, the winner included. Short labels."`
	Picked     string   `json:"picked,omitempty" jsonschema:"The option that won. Leave it out while still weighing."`
	Reason     string   `json:"reason,omitempty" jsonschema:"One line on why picked won. Needed with picked."`
	By         string   `json:"by,omitempty" jsonschema:"Who made the call. Needed with picked."`
	DecisionID string   `json:"decision_id,omitempty" jsonschema:"Id of a decision already in the tree (like d3), to update it."`
	After      string   `json:"after,omitempty" jsonschema:"Only for a new decision: the node id (like n4) to grow it from. Everything after that node is marked dropped."`
}

// Server answers the tool calls.
type Server struct {
	Store  store.Store
	Caller func() (claude.Session, error) // which session is calling; asked on every call
	Title  func(sessionID string) string  // names the start node of a new tree
	Now    func() time.Time
}

// New makes a Server for real use.
func New() *Server {
	dir := claude.Dir()
	return &Server{
		Store:  store.Default(),
		Caller: func() (claude.Session, error) { return claude.Caller(dir) },
		Title: func(id string) string {
			path, err := claude.ChatPath(dir, id)
			if err != nil {
				return ""
			}
			title, _ := claude.Title(path)
			return title
		},
		Now: time.Now,
	}
}

// MCP builds the MCP server with both tools.
func (s *Server) MCP(version string) *mcp.Server {
	m := mcp.NewServer(&mcp.Implementation{Name: "decision-tree", Version: version},
		&mcp.ServerOptions{Instructions: Instructions})

	schema, err := jsonschema.For[RecordInput](nil)
	if err != nil {
		panic(err) // RecordInput is fixed; this cannot fail at run time
	}
	schema.Properties["by"].Enum = []any{tree.ByUser, tree.ByClaude, tree.ByBoth}
	mcp.AddTool(m, &mcp.Tool{Name: "record_decision", Description: recordDescription, InputSchema: schema, Meta: alwaysLoad}, s.record)
	mcp.AddTool(m, &mcp.Tool{Name: "show_decision_tree", Description: showDescription, Meta: alwaysLoad}, s.show)
	return m
}

func (s *Server) record(_ context.Context, _ *mcp.CallToolRequest, in RecordInput) (*mcp.CallToolResult, any, error) {
	sess, err := s.Caller()
	if err != nil {
		return nil, nil, fmt.Errorf("decision-tree cannot tell which session this is: %v", err)
	}
	var res tree.Result
	t, err := s.Store.Update(sess.ID, func(t *tree.Tree) error {
		s.fill(t, sess)
		var err error
		res, err = t.Record(tree.Call{
			Question: in.Question, Options: in.Options, Picked: in.Picked,
			Reason: in.Reason, By: in.By, DecisionID: in.DecisionID, After: in.After,
			At: s.Now(),
		})
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	return text(Summary(t, res)), nil, nil
}

func (s *Server) show(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
	sess, err := s.Caller()
	if err != nil {
		return nil, nil, fmt.Errorf("decision-tree cannot tell which session this is: %v", err)
	}
	t, err := s.Store.Load(sess.ID)
	if err != nil {
		return text("The tree is empty. No decisions yet."), nil, nil
	}
	return text(render.Legend + "\n\n" + render.Text(t, render.Options{IDs: true})), nil, nil
}

// fill names a new tree's start node and notes its folder.
func (s *Server) fill(t *tree.Tree, sess claude.Session) {
	if t.Folder == "" {
		t.Folder = sess.Cwd
	}
	if t.Root().Label == "" && s.Title != nil {
		t.Root().Label = s.Title(sess.ID)
	}
}

// Summary is the one-line reply to record_decision.
func Summary(t *tree.Tree, r tree.Result) string {
	here := t.Node(r.Here)
	var b strings.Builder
	fmt.Fprintf(&b, "Saved as %s. You are here: %s (%s).", r.DecisionID, render.Label(here), here.ID)
	if len(r.Open) == 0 {
		b.WriteString(" Still open: none.")
	} else {
		b.WriteString(" Still open: " + quoted(t, r.Open) + ".")
	}
	if len(r.Left) > 0 {
		b.WriteString(" Left behind on the dropped branch, still unanswered: " + quoted(t, r.Left) +
			". If one still matters, answer it with its decision_id (add new options if needed) and it moves here. Do not log it again as a new decision.")
	}
	if len(r.Skipped) > 0 {
		b.WriteString(" Left out because the user deleted them: " + strings.Join(r.Skipped, ", ") + ".")
	}
	return b.String()
}

// quoted lists decisions as: d2 "Which front end?", d5 "When to deploy?"
func quoted(t *tree.Tree, ids []string) string {
	var parts []string
	for _, id := range ids {
		parts = append(parts, fmt.Sprintf("%s %q", id, t.Decision(id).Question))
	}
	return strings.Join(parts, ", ")
}

func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}
