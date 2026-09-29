// Package server is the MCP server Claude Code starts once per session. It
// gives Claude two tools, record_decision and show_decision_tree, plus the
// rules for when to use them (PRD sections 6 and 7).
package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"decision-tree/internal/checkpoint"
	"decision-tree/internal/claude"
	"decision-tree/internal/render"
	"decision-tree/internal/store"
	"decision-tree/internal/tree"
)

// Instructions are the rules from PRD section 6. Claude Code shows them in
// every session, even before the tools are loaded. It cuts them off at
// 2,048 characters, so they must stay shorter (a test checks).
const Instructions = `decision-tree draws the choices made in this session as a tree. The user watches it in a side pane, so keep it right with record_decision.

Log a decision only once it is made. Never log options that are only being discussed ("Redis, Postgres, or an in-memory cache are all possible" is not a decision). Made means:
- the user states it ("Use PostgreSQL rather than SQLite"): by user
- you recommend and the user agrees ("Agreed"): by both
- the user leaves it to you and you choose: by claude
A suggestion is not a decision. If you suggest Redis and the user says "No, use Postgres", log one decision: options Redis and PostgreSQL, picked PostgreSQL, by user.

In options, list the alternatives that were talked about, so the tree shows what was rejected.

Changing an earlier decision ("Actually change this to GraphQL"): call with that decision_id and the new picked, and add the new option to options if it is new. Set drop_later only if the decisions made after it depended on the old choice.

The topic is a short statement of what was decided, not a question: "Database", "API framework", "State storage". The tree shows "Database: PostgreSQL".

Project-level only: a tool or technology, an approach, scope, a design direction. Not names, the order of work, or which command to run. Many turns about one topic, and answers to AskUserQuestion, are one decision. When unsure, leave it out.

Log quietly. Do not mention the tree or this tool in your replies unless the user asks about it.

Only the main conversation logs decisions, not sub-agents. Keep labels short (under 8 words). If you lose track of the ids, call show_decision_tree.`

const recordDescription = `Log a decision once it is made, or change one. The server instructions say what counts.

- New decision: topic + options (every option talked about, the winner included) + picked + reason + by.
- Change an earlier decision: decision_id + picked + reason + by, plus options if the new pick was not an option before. The old pick is marked as changed. Decisions made after it stay, unless drop_later is set: then they are set aside with the old pick, for when they depended on it.
- New decision from an earlier point, setting aside everything after that point: topic + options + picked + reason + by + after (the node id to grow from).

The reply gives the decision id and where "you are here" is.`

const showDescription = `Show this session's decision tree as text, with decision ids (d1) and node ids (n1).`

// alwaysLoad asks Claude Code not to hold the tool back (docs/findings.md, part 2).
var alwaysLoad = mcp.Meta{"anthropic/alwaysLoad": true}

// RecordInput is what Claude sends to record_decision. See tree.Call.
type RecordInput struct {
	Topic      string   `json:"topic,omitempty" jsonschema:"What was decided, as a short statement, not a question: Database. Needed for a new decision."`
	Options    []string `json:"options,omitempty" jsonschema:"Every option talked about, the winner included. Short labels."`
	Picked     string   `json:"picked" jsonschema:"The option that won. Only log a decision once one has won."`
	Reason     string   `json:"reason" jsonschema:"One line on why picked won."`
	By         string   `json:"by" jsonschema:"Who made the call: the user stated it, claude chose it, or both agreed."`
	DecisionID string   `json:"decision_id,omitempty" jsonschema:"Id of a decision already in the tree (like d3), to change its pick."`
	DropLater  bool     `json:"drop_later,omitempty" jsonschema:"When changing a pick: also set aside the decisions made after the old pick, because they depended on it."`
	After      string   `json:"after,omitempty" jsonschema:"Only for a new decision: the node id (like n4) to grow it from. Everything after that node is set aside."`
}

// Server answers the tool calls.
type Server struct {
	Store  store.Store
	Caller func() (claude.Session, error) // which session is calling; asked on every call
	Title  func(sessionID string) string  // names the start node of a new tree
	Now    func() time.Time
	// Checkpoint saves what a branch needs to start from a new pick (PRD
	// 17.1). nil takes no checkpoints.
	Checkpoint func(sess claude.Session, node, toolUseID string, at time.Time) tree.Checkpoint
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
		Checkpoint: func(sess claude.Session, node, toolUseID string, at time.Time) tree.Checkpoint {
			repo := sess.Cwd
			if repo == "" {
				repo, _ = os.Getwd()
			}
			// Claude's memory for the project sits next to the session's chat.
			memory := ""
			if chat, err := claude.ChatPath(dir, sess.ID); err == nil {
				memory = filepath.Join(filepath.Dir(chat), "memory")
			}
			m := checkpoint.Maker{Dir: filepath.Join(store.Default().Dir, "checkpoints")}
			return m.Take(repo, memory, sess.ID, node, toolUseID, at)
		},
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

func (s *Server) record(_ context.Context, req *mcp.CallToolRequest, in RecordInput) (*mcp.CallToolResult, any, error) {
	if strings.TrimSpace(in.Picked) == "" {
		// The schema already asks for it; this catches "picked": "".
		return nil, nil, fmt.Errorf("picked is needed: only log a decision once one option has won. Do not log options that are only being discussed.")
	}
	sess, err := s.Caller()
	if err != nil {
		return nil, nil, fmt.Errorf("decision-tree cannot tell which session this is: %v", err)
	}
	// Claude Code sends the id of this tool use with each call
	// (docs/findings.md part 1). It later finds where to cut the chat.
	var toolUseID string
	if req != nil && req.Params != nil {
		toolUseID, _ = req.Params.Meta["claudecode/toolUseId"].(string)
	}
	now := s.Now()
	var res tree.Result
	t, err := s.Store.Update(sess.ID, func(t *tree.Tree) error {
		s.fill(t, sess)
		var err error
		res, err = t.Record(tree.Call{
			Topic: in.Topic, Options: in.Options, Picked: in.Picked,
			Reason: in.Reason, By: in.By, DecisionID: in.DecisionID, DropLater: in.DropLater, After: in.After,
			At: now,
		})
		if err == nil && res.Picked != "" && s.Checkpoint != nil {
			t.Node(res.Picked).Checkpoint = s.Checkpoint(sess, res.Picked, toolUseID, now)
		}
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
		parts = append(parts, fmt.Sprintf("%s %q", id, t.Decision(id).Topic))
	}
	return strings.Join(parts, ", ")
}

func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}
