// Package ui is the live view: one session's tree drawn as a git graph,
// centered in the pane, with a cursor, folding, and a details panel.
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
)

var (
	dim       = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	title     = lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Bold(true)
	cursorMk  = lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Bold(true)
	hereStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)
	bold      = lipgloss.NewStyle().Bold(true)
	errStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	byState   = map[tree.State]lipgloss.Style{
		tree.Picked:    lipgloss.NewStyle(),
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
		m.folds, m.cursor, m.offset, m.atHere = map[string]bool{}, 0, 0, true
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
		m.setFold(func(now bool) bool { return !now })
	case "h", "left":
		m.setFold(func(bool) bool { return true })
	case "l", "right":
		m.setFold(func(bool) bool { return false })
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

func (m *Model) setFold(f func(now bool) bool) {
	if m.tree == nil || m.cursor >= len(m.rows) {
		return
	}
	id := m.rows[m.cursor].NodeID
	if !graph.HasChildren(m.tree, id) {
		return
	}
	m.folds[id] = f(m.folded(id))
	m.layout()
}

// Screen parts, top to bottom: header, gap, graph, rule, 3 detail lines,
// key help. A short pane drops the gap and the help.
func (m Model) graphHeight() int {
	if m.h < 12 {
		return max(m.h-5, 1)
	}
	return max(m.h-7, 1)
}

// scroll keeps the cursor on screen, with a little room above and below.
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

func (m Model) graphLines() []string {
	gh := m.graphHeight()
	var out []string
	switch {
	case m.session == "":
		out = centered(m.w, "No trees yet.", "Start Claude in tmux and talk through a choice.")
	case m.tree == nil:
		out = centered(m.w, "No decisions yet in this session.", "They show up here as Claude logs them.")
	default:
		width := m.blockWidth()
		left := max((m.w-width)/2, 2)
		for i := m.offset; i < len(m.rows) && i < m.offset+gh; i++ {
			out = append(out, m.row(m.rows[i], i == m.cursor, left, width))
		}
	}
	for len(out) < gh {
		out = append(out, "")
	}
	return out[:gh]
}

// blockWidth is how wide the graph is, so it can sit in the middle. It is
// measured over all rows, so the graph does not shift sideways as it scrolls.
func (m Model) blockWidth() int {
	w := 0
	for _, r := range m.rows {
		w = max(w, rowWidth(r))
	}
	return min(w, max(m.w-4, 10))
}

// hereWidth is room for "  ◀" at the end of every row, not just the one
// that has it. Otherwise the graph would shift sideways each time "you are
// here" moves to a shorter row.
const hereWidth = 3

func rowWidth(r graph.Row) int {
	w := runewidth.StringWidth(r.Graph) + runewidth.StringWidth(r.Text)
	if r.Symbol != "" || r.Text != "" {
		w += runewidth.StringWidth(r.Symbol) + 2
	}
	return w + runewidth.StringWidth(suffix(r)) + hereWidth
}

func suffix(r graph.Row) string {
	if r.Folded > 0 {
		return " ▸ " + strconv.Itoa(r.Folded) + " more"
	}
	return ""
}

// row draws one graph row, with the cursor mark just left of the block and
// "◀" at the block's right edge.
func (m Model) row(r graph.Row, selected bool, left, width int) string {
	mark := "  "
	if selected {
		mark = cursorMk.Render("›") + " "
	}
	graphW := runewidth.StringWidth(r.Graph)
	textW := width - graphW - runewidth.StringWidth(suffix(r)) - hereWidth
	if r.Symbol != "" || r.Text != "" {
		textW -= runewidth.StringWidth(r.Symbol) + 2
	}
	text := runewidth.Truncate(r.Text, max(textW, 1), "…")

	style := lipgloss.NewStyle()
	switch {
	case r.Kind == graph.Open:
		style = byState[tree.Weighing]
	case r.Here:
		style = hereStyle
	case r.Kind == graph.Node:
		style = byState[r.State]
	}
	if selected {
		style = style.Bold(true).Underline(true)
	}

	var b strings.Builder
	b.WriteString(strings.Repeat(" ", left-2) + mark + dim.Render(r.Graph))
	used := graphW
	if r.Symbol != "" {
		b.WriteString(byState[r.State].Render(r.Symbol) + "  ")
		used += runewidth.StringWidth(r.Symbol) + 2
	} else if r.Text != "" {
		b.WriteString("  ")
		used += 2
	}
	b.WriteString(style.Render(text))
	used += runewidth.StringWidth(text)
	if s := suffix(r); s != "" {
		b.WriteString(dim.Render(s))
		used += runewidth.StringWidth(s)
	}
	if r.Here {
		b.WriteString(strings.Repeat(" ", max(width-used-1, 2)) + hereStyle.Render("◀"))
	}
	return b.String()
}

// details are the 3 lines about the node under the cursor.
func (m Model) details() []string {
	lines := make([]string, 3)
	if m.tree == nil || m.cursor >= len(m.rows) || m.rows[m.cursor].Kind != graph.Node {
		return lines
	}
	n := m.tree.Node(m.rows[m.cursor].NodeID)
	w := max(m.w-4, 10)
	cut := func(s string) string { return runewidth.Truncate(s, w, "…") }
	var a, b, c string
	switch {
	case n.ID == tree.RootID:
		a = m.rows[m.cursor].Text
		b = "Session " + short(m.tree.SessionID)
		if m.folder != "" {
			b = filepath.Base(m.folder) + " · " + b
		}
		c = plural(decisions(m.tree), "decision") + " · started " + m.when(n.At)
	case n.State == tree.Picked:
		a = m.tree.Statement(n)
		b = "Picked by " + who(n.By) + " · " + m.when(n.At)
		c = why("Why", n.Reason)
	case n.State == tree.Dropped:
		a = m.tree.Statement(n)
		b = "Dropped · " + m.when(n.At) + " · first picked by " + who(n.By)
		c = why("Why dropped", n.DropReason)
	case n.State == tree.NotPicked:
		a = m.tree.Statement(n)
		d := m.tree.Decision(n.Decision)
		if p := m.tree.PickOf(d); p != nil {
			b = "Not picked. " + p.Label + " won."
			c = why("Why "+p.Label, p.Reason)
		} else {
			b = "Not picked."
		}
	case n.State == tree.Weighing:
		a = m.tree.Statement(n)
		b = "Still being weighed."
		var others []string
		for _, id := range m.tree.Decision(n.Decision).Options {
			if o := m.tree.Node(id); !o.Hidden && o != n {
				others = append(others, o.Label)
			}
		}
		if len(others) > 0 {
			c = "Other options: " + strings.Join(others, ", ")
		}
	}
	lines[0] = "  " + bold.Render(cut(a))
	lines[1] = "  " + dim.Render(cut(b))
	lines[2] = "  " + cut(c)
	return lines
}

func (m Model) help() string {
	h := "j/k move · space fold · . here · q quit"
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

func who(by string) string {
	switch by {
	case tree.ByUser:
		return "you"
	case tree.ByClaude:
		return "Claude"
	case tree.ByBoth:
		return "you and Claude"
	}
	return "someone"
}

func why(label, reason string) string {
	if reason == "" {
		return ""
	}
	return label + ": " + reason
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
