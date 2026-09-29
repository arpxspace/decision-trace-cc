// Package ui is the live view: one session's tree drawn as a git graph,
// centered in the pane, with a cursor, folding, sideways scrolling, and a
// details panel.
//
// It never clears the screen. Four times a second it checks whether the
// tree file changed, and only then draws again. Bubble Tea skips a frame
// that is the same as the last one, so an idle view writes nothing.
package ui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"decision-tree/internal/branch"
	"decision-tree/internal/claude"
	"decision-tree/internal/graph"
	"decision-tree/internal/store"
	"decision-tree/internal/tree"
)

const (
	checkEvery  = 250 * time.Millisecond // how often to look at the tree file
	followEvery = time.Second            // how often to ask tmux which pane is in use
	scrollStep  = 8                      // columns per ←/→ press
	detailLines = 4                      // height of the details panel
)

var (
	plain     = lipgloss.NewStyle()
	dim       = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	title     = lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Bold(true)
	cursorMk  = lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Bold(true)
	hereStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)
	branchSym = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	bold      = lipgloss.NewStyle().Bold(true)
	errStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	byState   = map[tree.State]lipgloss.Style{
		tree.Picked:    plain,
		tree.NotPicked: dim,
		tree.Weighing:  lipgloss.NewStyle().Foreground(lipgloss.Color("11")),
		tree.Dropped:   lipgloss.NewStyle().Foreground(lipgloss.Color("9")),
	}
)

// Deps are the outside world, so tests can fake it.
type Deps struct {
	Store store.Store
	// Active is the Claude session in the tmux pane in use now. ok is false
	// when that pane is not running Claude.
	Active func() (s claude.Session, ok bool)
	Now    func() time.Time

	// The rest is for branches (PRD 17). Any of them may be nil, which
	// turns that part off.
	Branches func(session string) map[string][]graph.Stub            // branches made from a session, by node
	Source   func(session, node string) (claude.Cut, error)          // where a pick came from in the chat
	Prepare  func(session, node string) (*branch.Plan, error)        // what a branch would start with
	Create   func(p *branch.Plan, focus bool) (branch.Result, error) // make it
}

// Model is the view's state.
type Model struct {
	deps   Deps
	follow bool // follow the tmux pane in use, or stay on one session

	session   string
	folder    string
	tree      *tree.Tree
	stamp     time.Time // when the loaded tree file was last changed
	err       error
	notClaude bool // following, but the pane in use is not Claude
	followed  time.Time

	folds  map[string]bool // what Amir folded or opened by hand
	rows   []graph.Row
	cursor int    // index into rows; always a node row when there is one
	atHere bool   // the cursor rides along with "you are here"
	offset int    // first graph row on screen
	xoff   int    // columns scrolled to the right, when the graph is wider than the pane
	goTo   string // put the cursor on this node once the tree is loaded

	branches   map[string][]graph.Stub // sessions that branched from this one, by node
	branchesAt time.Time               // when they were last looked up
	over       overlay                 // a screen shown over the graph, if any

	w, h int
}

type tickMsg struct{}

// createdMsg comes back when making a branch is done.
type createdMsg struct {
	plan *branch.Plan
	res  branch.Result
	err  error
}

// What an overlay shows.
const (
	overNone    = iota
	overSource  // where a pick came from
	overConfirm // "Create branch?"
	overWorking // a branch is being made
	overNotice  // a message; any key closes it
)

// overlay is a screen shown over the graph.
type overlay struct {
	kind  int
	title string
	body  []string // lines; long ones wrap, and leading spaces indent them
	hint  string   // the keys, at the bottom
	plan  *branch.Plan
	focus bool
	node  string // for overSource: the node it is about
}

// New starts on the pinned session, or, if pinned is "", follows the tmux
// pane in use. With no Claude pane in use, it starts on the newest tree.
func New(d Deps, pinned string) Model {
	m := Model{deps: d, follow: pinned == "", atHere: true, folds: map[string]bool{}, w: 80, h: 24}
	switch {
	case pinned != "":
		m.session = pinned
	default:
		if s, ok := d.Active(); ok {
			m.session, m.folder = s.ID, s.Cwd
		} else if id, err := d.Store.Find(""); err == nil {
			m.session = id
		}
		m.followed = d.Now()
	}
	m.reload()
	return m
}

func (m Model) Init() tea.Cmd { return tick() }

func tick() tea.Cmd { return tea.Tick(checkEvery, func(time.Time) tea.Msg { return tickMsg{} }) }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.scroll()
	case tickMsg:
		if m.follow && m.deps.Now().Sub(m.followed) >= followEvery {
			m.followed = m.deps.Now()
			m.checkPane()
		}
		m.reload()
		// Branches live in other sessions' files, so look them up now and then.
		if m.tree != nil && m.deps.Now().Sub(m.branchesAt) >= followEvery && m.refreshBranches() {
			m.layout()
		}
		return m, tick()
	case createdMsg:
		m.created(msg)
	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

// refreshBranches looks up the branches made from this session. It reports
// whether they changed.
func (m *Model) refreshBranches() bool {
	if m.deps.Branches == nil || m.session == "" {
		return false
	}
	m.branchesAt = m.deps.Now()
	b := m.deps.Branches(m.session)
	if len(b) == 0 && len(m.branches) == 0 || reflect.DeepEqual(b, m.branches) {
		return false
	}
	m.branches = b
	return true
}

// open shows another session, and stays on it (no following). With node
// set, the cursor starts there.
func (m *Model) open(session, node string) {
	m.follow = false
	m.session, m.folder = session, ""
	m.tree, m.stamp, m.err, m.rows, m.branches = nil, time.Time{}, nil, nil, nil
	m.folds, m.cursor, m.offset, m.xoff = map[string]bool{}, 0, 0, 0
	m.atHere, m.goTo = node == "", node
	m.reload()
}

// checkPane switches to the session in the tmux pane in use, if it changed.
func (m *Model) checkPane() {
	s, ok := m.deps.Active()
	m.notClaude = !ok
	if ok && s.ID != m.session {
		m.session, m.folder = s.ID, s.Cwd
		m.tree, m.stamp, m.err, m.rows, m.branches = nil, time.Time{}, nil, nil, nil
		m.folds, m.cursor, m.offset, m.xoff, m.atHere = map[string]bool{}, 0, 0, 0, true
	}
}

// reload reads the tree again if its file changed since the last read.
func (m *Model) reload() {
	if m.session == "" {
		return
	}
	path, err := m.deps.Store.Path(m.session)
	if err != nil {
		m.err = err
		return
	}
	fi, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		m.tree, m.rows, m.err = nil, nil, nil
		return
	}
	if err != nil {
		m.err = err
		return
	}
	if m.tree != nil && fi.ModTime().Equal(m.stamp) {
		return
	}
	t, err := m.deps.Store.Load(m.session)
	if err != nil {
		m.err = err // keep showing the last good tree
		return
	}
	m.tree, m.stamp, m.err = t, fi.ModTime(), nil
	if t.Folder != "" {
		m.folder = t.Folder
	}
	m.refreshBranches()
	m.layout()
}

// layout rebuilds the rows and puts the cursor back where it belongs.
func (m *Model) layout() {
	var keep, keepBranch string
	if m.cursor < len(m.rows) {
		if r := m.rows[m.cursor]; r.Kind == graph.Branch {
			keepBranch = r.Session
		} else {
			keep = r.NodeID
		}
	}
	m.rows = graph.Layout(m.tree, m.folded, m.branches)
	switch {
	case m.goTo != "" && m.find(m.goTo) >= 0:
		m.cursor, m.goTo = m.find(m.goTo), ""
		m.atHere = m.rows[m.cursor].Here
	case m.atHere && m.find(m.tree.Here) >= 0:
		m.cursor = m.find(m.tree.Here)
	case keepBranch != "" && m.findBranch(keepBranch) >= 0:
		m.cursor = m.findBranch(keepBranch)
	case m.find(keep) >= 0:
		m.cursor = m.find(keep)
	default:
		m.cursor = min(m.cursor, len(m.rows)-1)
		m.step(0)
	}
	m.scroll()
}

func (m Model) folded(id string) bool {
	if v, ok := m.folds[id]; ok {
		return v
	}
	return graph.DefaultFolded(m.tree, id)
}

func (m Model) findBranch(session string) int {
	for i, r := range m.rows {
		if r.Kind == graph.Branch && r.Session == session {
			return i
		}
	}
	return -1
}

func (m Model) find(id string) int {
	for i, r := range m.rows {
		if r.Kind == graph.Node && r.NodeID == id && id != "" {
			return i
		}
	}
	return -1
}

func (m Model) key(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	k := msg.String()
	if k == "ctrl+c" {
		return m, tea.Quit
	}
	if m.over.kind != overNone {
		return m.overlayKey(k)
	}
	switch k {
	case "q", "esc":
		return m, tea.Quit
	case "j", "down":
		m.step(+1)
	case "k", "up":
		m.step(-1)
	case "g", "home":
		m.cursor = 0
		m.step(0)
	case "G", "end":
		m.cursor = len(m.rows) - 1
		m.step(0)
	case ".":
		if m.tree != nil && m.find(m.tree.Here) >= 0 {
			m.cursor = m.find(m.tree.Here)
		}
	case " ":
		m.toggleFold()
	case "enter":
		if r, ok := m.current(); ok && r.Kind == graph.Branch {
			m.open(r.Session, "")
		} else {
			m.showSource()
		}
	case "b", "B":
		m.startBranch(k == "B")
	case "p":
		if m.tree != nil && m.tree.Branch != nil {
			m.open(m.tree.Branch.FromSession, m.tree.Branch.FromNode)
		} else {
			m.notice("Not a branch", "This session did not branch from another one, so it has no parent.")
		}
	case "l", "right":
		m.xoff += scrollStep
	case "h", "left":
		m.xoff -= scrollStep
	case "0":
		m.xoff = 0
	case "f":
		if !m.follow {
			m.follow, m.followed = true, time.Time{}
		}
	default:
		return m, nil
	}
	if m.cursor >= 0 && m.cursor < len(m.rows) {
		m.atHere = m.rows[m.cursor].Here
	}
	m.scroll()
	return m, nil
}

// step moves the cursor to the next node row in direction d (+1 down,
// -1 up). With d = 0 it settles on the nearest node row.
func (m *Model) step(d int) {
	if len(m.rows) == 0 {
		m.cursor = 0
		return
	}
	m.cursor = max(0, min(m.cursor, len(m.rows)-1))
	if d != 0 {
		for i := m.cursor + d; i >= 0 && i < len(m.rows); i += d {
			if stop(m.rows[i]) {
				m.cursor = i
				return
			}
		}
		return // nothing further that way: stay put
	}
	for _, dir := range []int{+1, -1} {
		for i := m.cursor; i >= 0 && i < len(m.rows); i += dir {
			if stop(m.rows[i]) {
				m.cursor = i
				return
			}
		}
	}
}

// stop reports whether the cursor can rest on a row: nodes and branches.
func stop(r graph.Row) bool { return r.Kind == graph.Node || r.Kind == graph.Branch }

// current is the row under the cursor.
func (m Model) current() (graph.Row, bool) {
	if m.tree == nil || m.cursor < 0 || m.cursor >= len(m.rows) {
		return graph.Row{}, false
	}
	return m.rows[m.cursor], true
}

func (m *Model) toggleFold() {
	r, ok := m.current()
	if !ok || r.Kind != graph.Node {
		return
	}
	if !graph.HasChildren(m.tree, r.NodeID) && len(m.branches[r.NodeID]) == 0 {
		return
	}
	m.folds[r.NodeID] = !m.folded(r.NodeID)
	m.layout()
}

// overlayKey handles a key while an overlay is shown.
func (m Model) overlayKey(k string) (tea.Model, tea.Cmd) {
	switch m.over.kind {
	case overConfirm:
		switch k {
		case "y", "Y":
			p, focus := m.over.plan, m.over.focus
			m.over = overlay{kind: overWorking, title: "Making the branch…",
				body: []string{"The chat fork takes a few seconds. The original is not changed."}}
			create := m.deps.Create
			return m, func() tea.Msg {
				res, err := create(p, focus)
				return createdMsg{plan: p, res: res, err: err}
			}
		case "n", "N", "esc", "q":
			m.over = overlay{}
		}
	case overWorking:
		// Wait for it; the branch is being made.
	case overSource:
		switch k {
		case "b", "B":
			m.over = overlay{}
			m.startBranch(k == "B")
		case "esc", "q", "enter", " ":
			m.over = overlay{}
		}
	default:
		m.over = overlay{}
	}
	return m, nil
}

func (m *Model) notice(title string, body ...string) {
	m.over = overlay{kind: overNotice, title: title, body: body, hint: "any key closes this"}
}

// showSource shows where the pick under the cursor came from: your
// message, what Claude said just before, and what its checkpoint saved.
func (m *Model) showSource() {
	r, ok := m.current()
	if !ok || r.Kind != graph.Node {
		return
	}
	n := m.tree.Node(r.NodeID)
	switch {
	case n.ID == tree.RootID:
		m.notice(r.Text, "The start of the session. Nothing was decided here.")
		return
	case n.State != tree.Picked && n.State != tree.Dropped:
		m.notice(m.tree.Statement(n), "This option was not picked, so it has no source of its own. The pick of its decision does.")
		return
	}
	o := overlay{kind: overSource, title: m.tree.Statement(n), node: n.ID, hint: "esc closes · b branches from here"}
	o.body = append(o.body, pickedBy(n.By)+" · "+m.when(n.At), "")
	var chatErr error
	switch {
	case m.deps.Source == nil:
	default:
		if cut, err := m.deps.Source(m.session, n.ID); err != nil {
			chatErr = err
			o.body = append(o.body, "The chat: "+err.Error())
		} else {
			o.body = append(o.body, fmt.Sprintf("Your message #%d:", cut.PromptNumber), "  "+cut.Prompt)
			if cut.Before != "" {
				o.body = append(o.body, "", "Claude said just before:", "  "+cut.Before)
			}
		}
	}
	cp := n.Checkpoint
	o.body = append(o.body, "", "What a branch from here starts with:")
	switch {
	case chatErr != nil:
		o.body = append(o.body, "  Chat: not available, so no branch can be made from here")
	case cp.ToolUseID != "":
		o.body = append(o.body, "  Chat: up to right after this decision")
	default:
		o.body = append(o.body, "  Chat: not saved (logged before checkpoints existed)")
	}
	if cp.Commit != "" {
		o.body = append(o.body, "  Code: as it was then ("+cp.Commit[:min(12, len(cp.Commit))]+")")
	} else {
		o.body = append(o.body, "  Code: not available")
	}
	if cp.Memory != "" {
		o.body = append(o.body, "  Memory: as it was then")
	} else {
		o.body = append(o.body, "  Memory: none saved")
	}
	if cp.Missing != "" {
		o.body = append(o.body, "  Missing: "+cp.Missing)
	}
	m.over = o
}

// startBranch works out a branch from the pick under the cursor and asks
// first. focus jumps to the new tmux window once it is made.
func (m *Model) startBranch(focus bool) {
	r, ok := m.current()
	switch {
	case !ok || r.Kind != graph.Node:
		m.notice("Pick a decision", "Put the cursor on a pick to branch from it.")
		return
	case m.deps.Prepare == nil || m.deps.Create == nil:
		m.notice("Branching is off", "This view was started without branching.")
		return
	}
	p, err := m.deps.Prepare(m.session, r.NodeID)
	if err != nil {
		m.notice("Can't branch from here", err.Error())
		return
	}
	m.over = overlay{kind: overConfirm, title: "Create a branch?", body: strings.Split(p.Summary(), "\n"),
		hint: "y creates it · n cancels", plan: p, focus: focus}
}

// created shows how making a branch went.
func (m *Model) created(msg createdMsg) {
	if msg.err != nil {
		body := []string{msg.err.Error()}
		if msg.res.Command != "" {
			body = append(body, "", "To start it yourself:", "  "+msg.res.Command)
		}
		m.notice("The branch was not made", body...)
		return
	}
	body := []string{"Session " + short(msg.plan.Session), "Folder: " + msg.plan.Dir}
	switch {
	case msg.res.Pane != "":
		body = append(body, "tmux: a new window named "+msg.plan.Name+".",
			"The first start in a new folder may ask you to trust it.")
	case msg.res.Command != "":
		body = append(body, "tmux is not running. To start it:", "  "+msg.res.Command)
	}
	if len(msg.res.Copied) > 0 {
		body = append(body, "Copied from .worktreeinclude: "+strings.Join(msg.res.Copied, ", "))
	}
	m.notice("Created branch "+msg.plan.Name, body...)
	if m.refreshBranches() && m.tree != nil {
		m.layout()
	}
}

// Screen parts, top to bottom: header, gap, graph, rule, details, key help.
// A short pane drops the gap and the help.
func (m Model) graphHeight() int {
	if m.h < 12 {
		return max(m.h-2-detailLines, 1)
	}
	return max(m.h-4-detailLines, 1)
}

// scroll keeps the cursor on screen, with a little room above and below,
// and keeps the sideways scroll inside the graph.
func (m *Model) scroll() {
	gh := m.graphHeight()
	margin := min(2, (gh-1)/2)
	if m.cursor-margin < m.offset {
		m.offset = m.cursor - margin
	}
	if m.cursor+margin >= m.offset+gh {
		m.offset = m.cursor + margin - gh + 1
	}
	m.offset = max(0, min(m.offset, len(m.rows)-gh))
	m.xoff = max(0, min(m.xoff, m.blockWidth()-m.viewWidth()))
}

func (m Model) View() string {
	var lines []string
	lines = append(lines, m.header())
	if m.h >= 12 {
		lines = append(lines, "")
	}
	if m.over.kind != overNone {
		lines = append(lines, m.overlayLines(m.graphHeight()+1+detailLines)...)
		if m.h >= 12 {
			lines = append(lines, dim.Render(" "+m.over.hint))
		}
	} else {
		lines = append(lines, m.graphLines()...)
		lines = append(lines, dim.Render(" "+strings.Repeat("─", max(m.w-2, 1))))
		lines = append(lines, m.details()...)
		if m.h >= 12 {
			lines = append(lines, dim.Render(" "+m.help()))
		}
	}
	for i, l := range lines {
		lines[i] = fit(l, m.w)
	}
	return strings.Join(lines, "\n")
}

// overlayLines draws the overlay in the space of the graph and the details.
func (m Model) overlayLines(height int) []string {
	w := max(m.w-4, 10)
	out := []string{"  " + bold.Render(runewidth.Truncate(m.over.title, w, "…")), ""}
	for _, line := range m.over.body {
		if strings.TrimSpace(line) == "" {
			out = append(out, "")
			continue
		}
		// A line that fits stays as it is, so lined-up columns stay lined up.
		if runewidth.StringWidth(line) <= w {
			out = append(out, "  "+line)
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		for _, l := range wrap(strings.TrimSpace(line), max(w-indent, 10), 50) {
			out = append(out, "  "+strings.Repeat(" ", indent)+l)
		}
	}
	if len(out) > height {
		out = out[:height]
		out[height-1] = "  " + dim.Render("…")
	}
	for len(out) < height {
		out = append(out, "")
	}
	return out
}

func (m Model) header() string {
	name := "decision-tree"
	if m.folder != "" {
		name = filepath.Base(m.folder)
	}
	var parts []string
	if m.tree != nil && m.tree.Branch != nil {
		parts = append(parts, "branch of "+short(m.tree.Branch.FromSession))
	}
	if m.tree != nil {
		parts = append(parts, plural(decisions(m.tree), "decision"))
	}
	switch {
	case !m.follow:
		parts = append(parts, "pinned")
	case m.notClaude:
		parts = append(parts, "this pane is not Claude")
	default:
		parts = append(parts, "following")
	}
	h := " " + title.Render(name) + dim.Render(" · "+strings.Join(parts, " · "))
	if m.err != nil {
		h += errStyle.Render(" · " + m.err.Error())
	}
	return h
}

// A seg is a piece of a row with one style.
type seg struct {
	text  string
	style lipgloss.Style
}

// segs are the pieces of one graph row, left to right, with no margin.
func (m Model) segs(r graph.Row, selected bool) []seg {
	text := plain
	switch {
	case r.Kind == graph.Open:
		text = byState[tree.Weighing]
	case r.Here:
		text = hereStyle
	case r.Kind == graph.Node:
		text = byState[r.State]
	}
	if selected {
		text = text.Bold(true).Underline(true)
	}
	s := []seg{{r.Graph, dim}}
	switch {
	case r.Symbol != "":
		sym := byState[r.State]
		if r.Kind == graph.Branch {
			sym = branchSym
		}
		s = append(s, seg{r.Symbol, sym}, seg{"  ", plain})
	case r.Text != "":
		s = append(s, seg{"  ", plain})
	}
	s = append(s, seg{r.Text, text})
	if r.Folded > 0 {
		s = append(s, seg{" ▸ " + strconv.Itoa(r.Folded) + " more", dim})
	}
	if r.Here {
		s = append(s, seg{"  ", plain}, seg{"◀", hereStyle})
	}
	return s
}

func width(s []seg) int {
	w := 0
	for _, p := range s {
		w += runewidth.StringWidth(p.text)
	}
	return w
}

// cut draws the columns from..from+n of a row. A wide character split by
// the edge becomes a space.
func cut(s []seg, from, n int) string {
	var b strings.Builder
	col := 0
	for _, p := range s {
		var part strings.Builder
		for _, r := range p.text {
			w := runewidth.RuneWidth(r)
			switch {
			case col >= from && col+w <= from+n:
				part.WriteRune(r)
			case col < from+n && col+w > from:
				part.WriteString(" ")
			}
			col += w
		}
		if part.Len() > 0 {
			b.WriteString(p.style.Render(part.String()))
		}
	}
	return b.String()
}

// hereWidth is room for "  ◀" on every row, not just the one that has it.
// Otherwise the graph would shift sideways each time "you are here" moves
// to a shorter row.
const hereWidth = 3

// blockWidth is how wide the graph is. It is measured over all rows, so
// the graph does not shift sideways as it scrolls up and down.
func (m Model) blockWidth() int {
	w := 0
	for _, r := range m.rows {
		rw := width(m.segs(r, false))
		if !r.Here {
			rw += hereWidth
		}
		w = max(w, rw)
	}
	return w
}

// viewWidth is how many graph columns fit: the pane, less the cursor mark.
func (m Model) viewWidth() int { return max(m.w-3, 10) }

func (m Model) scrollable() bool { return m.tree != nil && m.blockWidth() > m.viewWidth() }

func (m Model) graphLines() []string {
	gh := m.graphHeight()
	var out []string
	switch {
	case m.session == "":
		out = centered(m.w, "No trees yet.", "Start Claude in tmux and make a decision.")
	case m.tree == nil:
		out = centered(m.w, "No decisions yet in this session.", "They show up here once one is made.")
	default:
		block, view := m.blockWidth(), m.viewWidth()
		for i := m.offset; i < len(m.rows) && i < m.offset+gh; i++ {
			out = append(out, m.row(i, block, view))
		}
	}
	for len(out) < gh {
		out = append(out, "")
	}
	return out[:gh]
}

// row draws one graph row. A graph that fits sits in the middle of the
// pane. A wider one starts at the left, scrolls sideways, and marks cut
// ends with "…".
func (m Model) row(i, block, view int) string {
	r := m.rows[i]
	mark := "  "
	if i == m.cursor {
		mark = cursorMk.Render("›") + " "
	}
	s := m.segs(r, i == m.cursor)
	if block <= view {
		left := max((m.w-block)/2, 2)
		return strings.Repeat(" ", left-2) + mark + cut(s, 0, block)
	}
	w := width(s)
	from, n := m.xoff, view
	lead, trail := "", ""
	if from > 0 && w > from {
		lead, from, n = dim.Render("…"), from+1, n-1
	}
	if w > m.xoff+view {
		trail, n = dim.Render("…"), n-1
	}
	return mark + lead + cut(s, from, n) + trail
}

// details are the lines about the node under the cursor. Long text wraps,
// so the whole reason can be read here.
func (m Model) details() []string {
	lines := make([]string, detailLines)
	if m.tree == nil || m.cursor >= len(m.rows) {
		return lines
	}
	if r := m.rows[m.cursor]; r.Kind == graph.Branch {
		w := max(m.w-4, 10)
		lines[0] = "  " + bold.Render(runewidth.Truncate("⎇ "+r.Text, w, "…"))
		lines[1] = "  " + dim.Render(runewidth.Truncate("A branch made from the pick above, in session "+short(r.Session), w, "…"))
		lines[2] = "  " + runewidth.Truncate("Enter opens its tree. There, p comes back here.", w, "…")
		return lines
	}
	if m.rows[m.cursor].Kind != graph.Node {
		return lines
	}
	n := m.tree.Node(m.rows[m.cursor].NodeID)
	var head, meta, body string
	switch {
	case n.ID == tree.RootID:
		head = m.rows[m.cursor].Text
		meta = "Session " + short(m.tree.SessionID)
		if m.folder != "" {
			meta = filepath.Base(m.folder) + " · " + meta
		}
		body = plural(decisions(m.tree), "decision") + " · started " + m.when(n.At)
	case n.State == tree.Picked:
		head = m.tree.Statement(n)
		meta = pickedBy(n.By) + " · " + m.when(n.At)
		if b := m.tree.Branch; b != nil && b.FromNode == n.ID {
			meta += " · this branch starts here"
		}
		body = why("Why", n.Reason)
	case n.State == tree.Dropped:
		head = m.tree.Statement(n)
		if p := m.tree.PickOf(m.tree.Decision(n.Decision)); p != nil {
			meta = "Changed to " + p.Label + " · " + m.when(n.At)
		} else {
			meta = "Set aside · " + m.when(n.At)
		}
		body = why("Why", n.DropReason)
		if body == "" {
			body = why("First picked because", n.Reason)
		}
	case n.State == tree.NotPicked:
		head = m.tree.Statement(n)
		if p := m.tree.PickOf(m.tree.Decision(n.Decision)); p != nil {
			meta = "Rejected. " + p.Label + " was picked."
			body = why("Why "+p.Label, p.Reason)
		} else {
			meta = "Rejected."
		}
	case n.State == tree.Weighing:
		head = m.tree.Statement(n)
		meta = "Still open. Nothing picked yet."
		var others []string
		for _, id := range m.tree.Decision(n.Decision).Options {
			if o := m.tree.Node(id); !o.Hidden && o != n {
				others = append(others, o.Label)
			}
		}
		if len(others) > 0 {
			body = "Other options: " + strings.Join(others, ", ")
		}
	}
	w := max(m.w-4, 10)
	var out []string
	for _, l := range wrap(head, w, 2) {
		out = append(out, "  "+bold.Render(l))
	}
	out = append(out, "  "+dim.Render(runewidth.Truncate(meta, w, "…")))
	for _, l := range wrap(body, w, detailLines-len(out)) {
		out = append(out, "  "+l)
	}
	copy(lines, out)
	return lines
}

func (m Model) help() string {
	// The keys that matter right now come first; what does not fit the pane
	// is left off.
	items := []string{"j/k move"}
	if m.scrollable() {
		items = append(items, "←/→ scroll")
	}
	if m.tree != nil && m.tree.Branch != nil {
		items = append(items, "p parent")
	}
	if !m.follow {
		items = append(items, "f follow")
	}
	items = append(items, "enter open", "b branch", "space fold", ". here", "q quit")
	h := ""
	for _, it := range items {
		next := it
		if h != "" {
			next = h + " · " + it
		}
		if runewidth.StringWidth(next) > m.w-2 {
			break
		}
		h = next
	}
	return h
}

// when is a clock time today, or a date and time on other days.
func (m Model) when(t time.Time) string {
	if t.IsZero() {
		return "time unknown"
	}
	t = t.Local()
	if now := m.deps.Now().Local(); t.YearDay() == now.YearDay() && t.Year() == now.Year() {
		return t.Format("15:04")
	}
	return t.Format("02 Jan 15:04")
}

func pickedBy(by string) string {
	switch by {
	case tree.ByUser:
		return "Picked by you"
	case tree.ByClaude:
		return "Picked by Claude"
	case tree.ByBoth:
		return "Agreed by you and Claude"
	}
	return "Picked"
}

func why(label, reason string) string {
	if reason == "" {
		return ""
	}
	return label + ": " + reason
}

// wrap fills lines of width w with whole words, at most max lines. If the
// text is longer, the last line ends with "…".
func wrap(text string, w, max int) []string {
	if text == "" || max <= 0 {
		return nil
	}
	var lines []string
	cur := ""
	for _, word := range strings.Fields(text) {
		switch {
		case cur == "":
			cur = word
		case runewidth.StringWidth(cur)+1+runewidth.StringWidth(word) <= w:
			cur += " " + word
		default:
			lines = append(lines, cur)
			cur = word
		}
	}
	lines = append(lines, cur)
	for i := range lines {
		lines[i] = runewidth.Truncate(lines[i], w, "…")
	}
	if len(lines) > max {
		lines = lines[:max]
		lines[max-1] = runewidth.Truncate(lines[max-1]+" …", w, "…")
	}
	return lines
}

func decisions(t *tree.Tree) int {
	n := 0
	for _, d := range t.Decisions {
		if !d.Hidden {
			n++
		}
	}
	return n
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func centered(w int, lines ...string) []string {
	out := []string{""}
	for _, l := range lines {
		pad := max((w-runewidth.StringWidth(l))/2, 0)
		out = append(out, strings.Repeat(" ", pad)+dim.Render(l))
	}
	return out
}

// fit cuts a styled line to the pane width so nothing wraps.
func fit(line string, w int) string {
	if lipgloss.Width(line) <= w {
		return line
	}
	return lipgloss.NewStyle().MaxWidth(w).Render(line)
}
