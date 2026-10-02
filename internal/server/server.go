// Package server answers Claude's two tools, record_decision and
// show_decision_tree, and holds the rules for when to use them (PRD sections
// 6 and 7). The Claude Code mod in mod/ offers the tools to Claude and runs
// the record, show, and describe commands, which call Record, Show, and
// Describe (PRD 18).
package server

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"

	"decision-tree/internal/checkpoint"
	"decision-tree/internal/claude"
	"decision-tree/internal/render"
	"decision-tree/internal/store"
	"decision-tree/internal/tree"
)

// Instructions are the rules from PRD section 6. The mod adds them to
// Claude's system prompt in every session. They were kept under 2,048
// characters, Claude Code's limit for an MCP server's instructions, and a
// test still keeps them there.
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

const recordDescription = `Log a decision once it is made, or change one. The decision-tree rules say what counts.

- New decision: topic + options (every option talked about, the winner included) + picked + reason + by.
- Change an earlier decision: decision_id + picked + reason + by, plus options if the new pick was not an option before. The old pick is marked as changed. Decisions made after it stay, unless drop_later is set: then they are set aside with the old pick, for when they depended on it.
- New decision from an earlier point, setting aside everything after that point: topic + options + picked + reason + by + after (the node id to grow from).

The reply gives the decision id and where "you are here" is.`

const showDescription = `Show this session's decision tree as text, with decision ids (d1) and node ids (n1).`

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
	Store store.Store
	Title func(sessionID string) string // names the start node of a new tree
	Now   func() time.Time
	// Checkpoint saves what a branch needs to start from a new pick (PRD
	// 17.1) and puts it on the node. nil takes no checkpoints.
	Checkpoint func(sess claude.Session, t *tree.Tree, node, toolUseID string, at time.Time)
}

// New makes a Server for real use.
func New() *Server {
	dir := claude.Dir()
	return &Server{
		Store: store.Default(),
		Title: func(id string) string {
			path, err := claude.ChatPath(dir, id)
			if err != nil {
				return ""
			}
			title, _ := claude.Title(path)
			return title
		},
		Now: time.Now,
		Checkpoint: func(sess claude.Session, t *tree.Tree, node, toolUseID string, at time.Time) {
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
			cp := m.Take(repo, memory, sess.ID, node, toolUseID, at)
			n := t.Node(node)
			n.Checkpoint = cp
			// The copy of the tree includes this checkpoint, so a branch of a
			// branch can find it.
			if path, err := checkpoint.SaveTree(m.Dir, sess.ID, node, t); err == nil {
				cp.Tree = path
			} else {
				cp.Missing = strings.TrimPrefix(cp.Missing+"; tree not saved: "+err.Error(), "; ")
			}
			n.Checkpoint = cp
		},
	}
}

func recordSchema() *jsonschema.Schema {
	schema, err := jsonschema.For[RecordInput](nil)
	if err != nil {
		panic(err) // RecordInput is fixed; this cannot fail at run time
	}
	schema.Properties["by"].Enum = []any{tree.ByUser, tree.ByClaude, tree.ByBoth}
	return schema
}

// Tool is one of the two tools, as the mod registers it.
type Tool struct {
	Name        string             `json:"name"`
	Description string             `json:"description"`
	InputSchema *jsonschema.Schema `json:"inputSchema"`
}

// Spec is what the mod needs to offer the tools: the rules and both tools.
type Spec struct {
	Instructions string `json:"instructions"`
	Tools        []Tool `json:"tools"`
}

// Describe gives the rules and both tools, so the mod never keeps a copy of
// its own.
func Describe() Spec {
	return Spec{Instructions: Instructions, Tools: []Tool{
		{Name: "record_decision", Description: recordDescription, InputSchema: recordSchema()},
		{Name: "show_decision_tree", Description: showDescription, InputSchema: &jsonschema.Schema{Type: "object"}},
	}}
}

// Record saves one record_decision call in the session's tree and returns
// the one-line reply. toolUseID is the call's id in the chat; it marks where
// a branch would cut.
func (s *Server) Record(sess claude.Session, in RecordInput, toolUseID string) (string, error) {
	if strings.TrimSpace(in.Picked) == "" {
		// The schema asks for it; this also catches "picked": "".
		return "", fmt.Errorf("picked is needed: only log a decision once one option has won. Do not log options that are only being discussed.")
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
			s.Checkpoint(sess, t, res.Picked, toolUseID, now)
		}
		return err
	})
	if err != nil {
		return "", err
	}
	return Summary(t, res), nil
}

// Show is the session's tree as short text with ids.
func (s *Server) Show(sessionID string) string {
	t, err := s.Store.Load(sessionID)
	if err != nil {
		return "The tree is empty. No decisions yet."
	}
	return render.Legend + "\n\n" + render.Text(t, render.Options{IDs: true})
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
