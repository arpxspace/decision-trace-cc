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
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

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
	cursor int  // index into rows; always a node row when there is one
	atHere bool // the cursor rides along with "you are here"
	offset int  // first graph row on screen
	xoff   int  // columns scrolled to the right, when the graph is wider than the pane

	w, h int
}

type tickMsg struct{}

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
		return m, tick()
	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

// checkPane switches to the session in the tmux pane in use, if it changed.
func (m *Model) checkPane() {
	s, ok := m.deps.Active()
	m.notClaude = !ok
	if ok && s.ID != m.session {
		m.session, m.folder = s.ID, s.Cwd
		m.tree, m.stamp, m.err, m.rows = nil, time.Time{}, nil, nil
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
	m.layout()
}

// layout rebuilds the rows and puts the cursor back where it belongs.
func (m *Model) layout() {
	var keep string
	if m.cursor < len(m.rows) {
		keep = m.rows[m.cursor].NodeID
	}
	m.rows = graph.Layout(m.tree, m.folded)
	switch {
	case m.atHere && m.find(m.tree.Here) >= 0:
		m.cursor = m.find(m.tree.Here)
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

func (m Model) find(id string) int {
	for i, r := range m.rows {
		if r.Kind == graph.Node && r.NodeID == id && id != "" {
			return i
		}
	}
	return -1
}

func (m Model) key(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c", "esc":
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
	case " ", "enter":
		m.toggleFold()
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
			if m.rows[i].Kind == graph.Node {
				m.cursor = i
				return
			}
		}
		return // nothing further that way: stay put
	}
	for _, dir := range []int{+1, -1} {
		for i := m.cursor; i >= 0 && i < len(m.rows); i += dir {
			if m.rows[i].Kind == graph.Node {
				m.cursor = i
				return
			}
		}
	}
}

func (m *Model) toggleFold() {
	if m.tree == nil || m.cursor >= len(m.rows) {
		return
	}
	id := m.rows[m.cursor].NodeID
	if !graph.HasChildren(m.tree, id) {
		return
	}
	m.folds[id] = !m.folded(id)
	m.layout()
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
	lines = append(lines, m.graphLines()...)
	lines = append(lines, dim.Render(" "+strings.Repeat("─", max(m.w-2, 1))))
	lines = append(lines, m.details()...)
	if m.h >= 12 {
		lines = append(lines, dim.Render(" "+m.help()))
	}
	for i, l := range lines {
		lines[i] = fit(l, m.w)
	}
	return strings.Join(lines, "\n")
}

func (m Model) header() string {
	name := "decision-tree"
	if m.folder != "" {
		name = filepath.Base(m.folder)
	}
	var parts []string
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
		s = append(s, seg{r.Symbol, byState[r.State]}, seg{"  ", plain})
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
	if m.tree == nil || m.cursor >= len(m.rows) || m.rows[m.cursor].Kind != graph.Node {
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
	h := "j/k move"
	if m.scrollable() {
		h += " · ←/→ scroll"
	}
	h += " · space fold · . here · q quit"
	if !m.follow {
		h += " · f follow"
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
