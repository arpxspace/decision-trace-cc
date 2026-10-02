// The pane's view, ported from internal/ui: one session's tree drawn as a
// git graph, centered in the pane, with a cursor, folding, sideways
// scrolling, a details panel, and the screens shown over the graph.
//
// It runs on the drawing thread as the Client's surface module. It keeps
// the cursor, the folds and the scroll itself, so keys feel instant, and
// posts to the hooks module for anything that needs the Go program: where a
// pick came from, making a branch, opening another session's tree.
import type { ClientKeyEvent, ClientPointerEvent, ClientSurface, RenderElement } from 'claude-code'
import type { Tree, ViewProps } from '../types'
import type { Row } from './graph'
import { decision, defaultFolded, hasChildren, layout, node, pickOf, ROOT, statement } from './graph'
import { charWidth, pickedBy, short, when, width, wrap } from './text'

const DETAIL_LINES = 4 // the details panel's height, unless its text needs more
const HERE_WIDTH = 3 // room for "  ◀" on every row, so the graph does not shift when "you are here" moves
const BACKGROUND = '#000000' // the pane's body is black, edge to edge

type Style = { color?: string; bold?: boolean; dim?: boolean; underline?: boolean }

const PLAIN: Style = {}
const DIM: Style = { dim: true }
const TITLE: Style = { color: 'cyanBright', bold: true }
const HERE: Style = { color: 'greenBright', bold: true }
const BRANCH: Style = { color: 'cyanBright' }
const BOLD: Style = { bold: true }
const ERR: Style = { color: 'redBright' }
const BY_STATE: Record<string, Style> = {
  picked: PLAIN,
  not_picked: DIM,
  weighing: { color: 'yellowBright' },
  dropped: { color: 'redBright' },
}

/** A piece of a line with one style. */
type Seg = { text: string; style: Style }
type Line = Seg[]

/** What the person did to the view: kept across redraws, reset when it
 * shows another session. */
type Local = {
  session: string
  folds: Record<string, boolean> // what was folded or opened by hand
  cur: string // the row under the cursor: "n:<node>" or "b:<session>"
  idx: number // where that row was, for when it goes away
  atHere: boolean // the cursor rides along with "you are here"
  offset: number // first graph line on screen (a wrapped row takes several)
  goToN: number // the last goTo the view followed
  used: boolean // a key or a click has reached the view
}

/** One drawing's worked-out state, which the key and pointer handlers act on. */
type View = {
  props: ViewProps
  local: Local
  tree: Tree | null
  rows: Row[]
  screen: ScreenLine[] // the graph's lines, wrapped to the pane
  block: number // how wide the graph is
  info: Line[] // the details panel
  gh: number // how many lines the graph has on screen
  w: number
  h: number
}

// The latest drawing of each instance, for its handlers.
const views = new WeakMap<object, View>()
const wired = new WeakSet<object>()

function fresh(session: string, before?: Local): Local {
  return {
    session,
    folds: {},
    cur: '',
    idx: 0,
    atHere: true,
    offset: 0,
    goToN: before?.goToN ?? 0,
    used: before?.used ?? false,
  }
}

function rowKey(r: Row | undefined): string {
  if (!r) {
    return ''
  }
  return r.kind === 'branch' ? `b:${r.session}` : r.kind === 'node' ? `n:${r.nodeId}` : ''
}

function find(rows: Row[], key: string): number {
  return key === '' ? -1 : rows.findIndex(r => rowKey(r) === key)
}

/** Whether the cursor can rest on a row: nodes and branches. */
const stop = (r: Row | undefined) => r?.kind === 'node' || r?.kind === 'branch'

/** The next row the cursor can rest on in direction d (+1 down, -1 up);
 * with d = 0, the nearest one. */
function step(rows: Row[], idx: number, d: number): number {
  if (rows.length === 0) {
    return 0
  }
  idx = Math.max(0, Math.min(idx, rows.length - 1))
  if (d !== 0) {
    for (let i = idx + d; i >= 0 && i < rows.length; i += d) {
      if (stop(rows[i])) {
        return i
      }
    }
    return idx // nothing further that way: stay put
  }
  for (const dir of [1, -1]) {
    for (let i = idx; i >= 0 && i < rows.length; i += dir) {
      if (stop(rows[i])) {
        return i
      }
    }
  }
  return idx
}

// Screen parts, top to bottom: header, gap, graph, rule, details, key help.
// A short pane drops the gap and the help.
function graphHeight(h: number, info: number): number {
  return h < 12 ? Math.max(h - 2 - info, 1) : Math.max(h - 4 - info, 1)
}

/** The rows a screen over the graph has: everything between the header and the hint. */
const overlayHeight = (h: number) => (h < 12 ? h - 1 : h - 3)

/** How many graph columns fit: the pane, less the cursor mark and a margin. */
const viewWidth = (w: number) => Math.max(w - 3, 10)

/** Works out what to draw from the props and what the person did. */
function resolve(props: ViewProps, before: Local | undefined, w: number, h: number): View {
  const session = props.data?.session ?? ''
  let l = before && before.session === session ? { ...before } : fresh(session, before)
  const tree = props.data?.tree ?? null
  // The tree is the session's own, so its start reads "Start", not the
  // session's title.
  const rows = (tree ? layout(tree, id => l.folds[id] ?? defaultFolded(tree, id), props.data?.branches ?? {}) : []).map(
    r => (r.kind === 'node' && r.nodeId === ROOT ? { ...r, text: 'Start' } : r),
  )

  let idx: number
  const goTo = props.goTo && props.goTo.n !== l.goToN ? props.goTo : null
  if (goTo && find(rows, `n:${goTo.node}`) >= 0) {
    idx = find(rows, `n:${goTo.node}`)
    l.atHere = rows[idx]?.here ?? false
  } else if (l.atHere && tree && find(rows, `n:${tree.here}`) >= 0) {
    idx = find(rows, `n:${tree.here}`)
  } else if (find(rows, l.cur) >= 0) {
    idx = find(rows, l.cur)
  } else {
    idx = step(rows, Math.min(l.idx, rows.length - 1), 0)
  }
  if (goTo && rows.length > 0) {
    l.goToN = goTo.n
  }
  l.cur = rowKey(rows[idx])
  l.idx = idx

  // The graph's lines on screen: a row too wide for the pane wraps.
  const { screen, block } = wrapRows(rows, idx, w)

  // The details panel shows all of its text, up to half the pane; the
  // graph gets the rest.
  const all = details(tree, rows[idx], w)
  const most = Math.max(DETAIL_LINES, Math.floor(h / 2) - 2)
  const info = all.length > most ? [...all.slice(0, most - 1), say('  …', DIM)] : all
  while (info.length < DETAIL_LINES) {
    info.push([])
  }
  const gh = graphHeight(h, info.length)

  // Keep the cursor's row on screen, with a little room above and below.
  const margin = Math.min(2, Math.floor((gh - 1) / 2))
  const first = screen.findIndex(line => line.row === idx)
  let last = first
  while (screen[last + 1]?.row === idx) {
    last++
  }
  let offset = l.offset
  if (last + margin >= offset + gh) {
    offset = last + margin - gh + 1
  }
  if (first - margin < offset) {
    offset = first - margin
  }
  l.offset = Math.max(0, Math.min(offset, screen.length - gh))
  return { props, local: l, tree, rows, screen, block, info, gh, w, h }
}

// ---- drawing ----

/** One line of the graph on screen: part of row `row`, either its first
 * line or one its text wrapped onto. */
type ScreenLine = { row: number; first: boolean; line: Line }

/** How a row's text looks. */
function textStyle(r: Row, selected: boolean): Style {
  let text = PLAIN
  if (r.kind === 'open') {
    text = BY_STATE.weighing ?? PLAIN
  } else if (r.here) {
    text = HERE
  } else if (r.kind === 'node' && r.state) {
    text = BY_STATE[r.state] ?? PLAIN
  }
  return selected ? { ...text, bold: true, underline: true } : text
}

/** What comes before a row's text: its lines and its symbol. */
function prefix(r: Row): Line {
  const s: Line = [{ text: r.graph, style: DIM }]
  if (r.symbol !== '') {
    const sym = r.kind === 'branch' ? BRANCH : (BY_STATE[r.state ?? 'picked'] ?? PLAIN)
    s.push({ text: r.symbol, style: sym }, { text: '  ', style: PLAIN })
  } else if (r.text !== '') {
    s.push({ text: '  ', style: PLAIN })
  }
  return s
}

/** What comes after a row's text: "▸ 2 more" and "◀". */
function suffix(r: Row): Line {
  const s: Line = []
  if (r.folded > 0) {
    s.push({ text: ` ▸ ${r.folded} more`, style: DIM })
  }
  if (r.here) {
    s.push({ text: '  ', style: PLAIN }, { text: '◀', style: HERE })
  }
  return s
}

/** One graph row on one line, with no margin. */
function segs(r: Row, selected: boolean): Line {
  return [...prefix(r), { text: r.text, style: textStyle(r, selected) }, ...suffix(r)]
}

const lineWidth = (line: Line) => line.reduce((n, p) => n + width(p.text), 0)

/** Lays the rows out as lines on screen. A row too wide for the pane wraps
 * its text onto more lines, with the graph's lines carried on beside it, so
 * the whole text can always be read. The block is how wide the graph is,
 * over all rows, so it does not shift as it scrolls; it keeps room for
 * "  ◀" on every row, so it does not shift when "you are here" moves. */
function wrapRows(rows: Row[], selected: number, w: number): { screen: ScreenLine[]; block: number } {
  const view = viewWidth(w)
  const block = Math.min(view, rows.reduce((b, r) => Math.max(b, lineWidth(segs(r, false)) + (r.here ? 0 : HERE_WIDTH)), 0))
  const screen: ScreenLine[] = []
  rows.forEach((r, i) => {
    const one = segs(r, i === selected)
    if (lineWidth(one) <= view) {
      screen.push({ row: i, first: true, line: one })
      return
    }
    const head = prefix(r)
    const indent = lineWidth(head)
    const room = Math.max(view - indent, 8)
    const style = textStyle(r, i === selected)
    const under: Line = [
      { text: r.cont, style: DIM },
      { text: ' '.repeat(Math.max(indent - width(r.cont), 0)), style: PLAIN },
    ]
    const parts = wrap(r.text, room, Number.POSITIVE_INFINITY)
    if (parts.length === 0) {
      parts.push('')
    }
    // "▸ 2 more" and "◀" end the last line, or get a line of their own.
    const tail = suffix(r)
    const isTailAlone = tail.length > 0 && width(parts.at(-1) ?? '') + lineWidth(tail) > room
    parts.forEach((part, k) => {
      const line: Line = [...(k === 0 ? head : under), { text: part, style }]
      if (k === parts.length - 1 && !isTailAlone) {
        line.push(...tail)
      }
      screen.push({ row: i, first: k === 0, line })
    })
    if (isTailAlone) {
      const [lead, ...rest] = tail
      const trimmed = lead ? [{ ...lead, text: lead.text.trimStart() }, ...rest] : rest
      screen.push({ row: i, first: false, line: [...under, ...trimmed.filter(p => p.text !== '')] })
    }
  })
  return { screen, block }
}

/** The columns from..from+n of a line. A wide character split by an edge
 * becomes a space. */
function cut(line: Line, from: number, n: number): Line {
  const out: Line = []
  let col = 0
  for (const p of line) {
    let part = ''
    for (const ch of p.text) {
      const cw = charWidth(ch)
      if (col >= from && col + cw <= from + n) {
        part += ch
      } else if (col < from + n && col + cw > from) {
        part += ' '
      }
      col += cw
    }
    if (part !== '') {
      out.push({ text: part, style: p.style })
    }
  }
  return out
}

// Text from outside the view may hold line breaks; a line here never does.
const say = (text: string, style: Style = PLAIN): Line => [{ text: text.replace(/\s*\n\s*/g, ' '), style }]

/** The top line: just "Decision". Another session's tree also says whose
 * it is, and an error shows after it. */
function header(v: View): Line {
  const line: Line = [
    { text: ' ', style: PLAIN },
    { text: 'Decision', style: TITLE },
  ]
  if (!v.props.isOwn && v.props.data) {
    line.push({ text: ` · session ${short(v.props.data.session)}`, style: DIM })
  }
  if (v.props.error !== '') {
    line.push(...say(' · ' + v.props.error, ERR))
  }
  return line
}

function centered(w: number, ...lines: string[]): Line[] {
  return [[], ...lines.map(l => [{ text: ' '.repeat(Math.max(Math.floor((w - width(l)) / 2), 0)) + l, style: DIM }])]
}

/** One line of the graph. The graph sits in the middle of the pane; the
 * cursor mark is on the first line of its row. */
function graphLine(v: View, k: number): Line {
  const s = v.screen[k] as ScreenLine
  const isCursor = s.first && s.row === v.local.idx
  const mark: Line = isCursor ? [{ text: '›', style: TITLE }, { text: ' ', style: PLAIN }] : say('  ')
  const left = Math.max(Math.floor((v.w - v.block) / 2), 2)
  return [...say(' '.repeat(left - 2)), ...mark, ...s.line]
}

function graphLines(v: View): Line[] {
  const gh = v.gh
  let out: Line[]
  if (!v.props.data) {
    out = centered(v.w, 'Loading…')
  } else if (!v.tree) {
    out = v.props.isOwn
      ? centered(v.w, 'No decisions yet in this session.', 'They show up here once one is made.')
      : centered(v.w, 'That session has no decisions.')
  } else {
    out = []
    for (let k = v.local.offset; k < v.screen.length && k < v.local.offset + gh; k++) {
      out.push(graphLine(v, k))
    }
  }
  while (out.length < gh) {
    out.push([])
  }
  return out.slice(0, gh)
}

const why = (label: string, reason: string | undefined) => (reason ? `${label}: ${reason}` : '')

/** The lines about the row under the cursor, all of its text wrapped, so
 * the whole reason can be read here. */
function details(t: Tree | null, r: Row | undefined, columns: number): Line[] {
  if (!t || !r) {
    return []
  }
  const w = Math.max(columns - 4, 10)
  const text = (s: string, style: Style): Line[] =>
    wrap(s, w, Number.POSITIVE_INFINITY).map(l => [{ text: '  ' + l, style }])
  if (r.kind === 'branch') {
    return [
      ...text(`⎇ ${r.text}`, BOLD),
      ...text(`A branch made from the pick above, in session ${short(r.session)}`, DIM),
      ...text('Enter opens its tree. There, p comes back here.', PLAIN),
    ]
  }
  const n = r.kind === 'node' ? node(t, r.nodeId) : undefined
  if (!n) {
    return []
  }
  let head = ''
  let meta = ''
  let body = ''
  const d = n.decision ? decision(t, n.decision) : undefined
  const pick = d ? pickOf(t, d) : undefined
  if (n.id === ROOT) {
    head = 'Start'
    meta = `Started ${when(n.at)}`
    if (t.branch) {
      body = `Branch of session ${short(t.branch.from_session)}, from ${t.branch.statement}`
    }
  } else if (n.state === 'picked') {
    head = statement(t, n)
    meta = `${pickedBy(n.by)} · ${when(n.at)}`
    if (t.branch?.from_node === n.id) {
      meta += ' · this branch starts here'
    }
    body = why('Why', n.reason)
  } else if (n.state === 'dropped') {
    head = statement(t, n)
    meta = pick ? `Changed to ${pick.label} · ${when(n.at)}` : `Set aside · ${when(n.at)}`
    body = why('Why', n.drop_reason) || why('First picked because', n.reason)
  } else if (n.state === 'not_picked') {
    head = statement(t, n)
    meta = pick ? `Rejected. ${pick.label} was picked.` : 'Rejected.'
    body = pick ? why(`Why ${pick.label}`, pick.reason) : ''
  } else {
    head = statement(t, n)
    meta = 'Still open. Nothing picked yet.'
    const others = (d?.options ?? []).map(id => node(t, id)).filter(o => o && !o.hidden && o !== n)
    if (others.length > 0) {
      body = 'Other options: ' + others.map(o => o?.label).join(', ')
    }
  }
  return [...text(head, BOLD), ...text(meta, DIM), ...text(body, PLAIN)]
}

/** The keys that matter right now come first; what does not fit is left off. */
function help(v: View): string {
  const items: string[] = []
  if (!v.local.used) {
    items.push('click for keys')
  }
  items.push('j/k move')
  if (v.tree?.branch) {
    items.push('p parent')
  }
  if (!v.props.isOwn) {
    items.push('f this session')
  }
  items.push('enter open', 'b branch', 'space fold', '. here', 'q close', 'esc prompt')
  let h = ''
  for (const it of items) {
    const next = h === '' ? it : `${h} · ${it}`
    if (width(next) > v.w - 2) {
      break
    }
    h = next
  }
  return h
}

/** The screen shown over the graph and the details. */
function overlayLines(v: View, height: number): Line[] {
  const o = v.props.overlay
  if (!o) {
    return []
  }
  const w = Math.max(v.w - 4, 10)
  const out: Line[] = [...wrap(o.title, w, Number.POSITIVE_INFINITY).map(l => [{ text: '  ' + l, style: BOLD }]), []]
  for (const line of o.body) {
    if (line.trim() === '') {
      out.push([])
    } else if (width(line) <= w) {
      // A line that fits stays as it is, so lined-up columns stay lined up.
      out.push(say('  ' + line))
    } else {
      const indent = line.length - line.trimStart().length
      for (const l of wrap(line.trim(), Math.max(w - indent, 10), 50)) {
        out.push(say('  ' + ' '.repeat(indent) + l))
      }
    }
  }
  if (out.length > height) {
    out.length = height
    out[height - 1] = say('  …', DIM)
  }
  while (out.length < height) {
    out.push([])
  }
  return out
}

function lines(v: View): Line[] {
  const out: Line[] = [header(v)]
  if (v.h >= 12) {
    out.push([])
  }
  if (v.props.overlay) {
    out.push(...overlayLines(v, overlayHeight(v.h)))
    if (v.h >= 12) {
      out.push(say(' ' + v.props.overlay.hint, DIM))
    }
  } else {
    out.push(...graphLines(v))
    out.push(say(' ' + '─'.repeat(Math.max(v.w - 2, 1)), DIM))
    out.push(...v.info)
    if (v.h >= 12) {
      out.push(say(' ' + help(v), DIM))
    }
  }
  // Every line runs the full width, so the background has no gaps.
  return out.map(l => {
    const line = cut(l, 0, v.w)
    const room = v.w - lineWidth(line)
    return room > 0 ? [...line, { text: ' '.repeat(room), style: PLAIN }] : line
  })
}

// ---- keys and clicks ----

type Post = (data: Parameters<ClientSurface['post']>[0]) => void

/** What a key does. It answers the new local state, or undefined when only
 * a post went out. */
function onKey(v: View, k: ClientKeyEvent, post: Post): Local | undefined {
  const key = k.key
  const l: Local = { ...v.local, used: true }
  const o = v.props.overlay
  if (o) {
    switch (o.kind) {
      case 'confirm':
        if (key === 'y' || key === 'Y') {
          post({ type: 'create' })
        } else if (key === 'n' || key === 'N' || key === 'q') {
          post({ type: 'close' })
        }
        break
      case 'working':
        break // wait for it; the branch is being made
      case 'source':
        if ((key === 'b' || key === 'B') && o.node) {
          post({ type: 'branch', session: v.props.data?.session ?? '', node: o.node, focus: key === 'B' })
        } else if (key === 'q' || key === 'return' || key === 'space' || key === ' ') {
          post({ type: 'close' })
        }
        break
      default:
        post({ type: 'close' })
    }
    return l
  }
  const r = v.rows[l.idx]
  switch (key) {
    case 'j':
    case 'down':
      l.idx = step(v.rows, l.idx, 1)
      break
    case 'k':
    case 'up':
      l.idx = step(v.rows, l.idx, -1)
      break
    case 'g':
    case 'home':
      l.idx = step(v.rows, 0, 0)
      break
    case 'G':
    case 'end':
      l.idx = step(v.rows, v.rows.length - 1, 0)
      break
    case '.':
      if (v.tree && find(v.rows, `n:${v.tree.here}`) >= 0) {
        l.idx = find(v.rows, `n:${v.tree.here}`)
      }
      break
    case 'space':
    case ' ':
      if (v.tree && r?.kind === 'node' && (hasChildren(v.tree, r.nodeId) || (v.props.data?.branches[r.nodeId]?.length ?? 0) > 0)) {
        l.folds = { ...l.folds, [r.nodeId]: !(l.folds[r.nodeId] ?? defaultFolded(v.tree, r.nodeId)) }
      }
      break
    case 'return':
      if (r?.kind === 'branch') {
        post({ type: 'open', session: r.session })
      } else if (r?.kind === 'node') {
        post({ type: 'source', node: r.nodeId })
      }
      break
    case 'b':
    case 'B':
      post({ type: 'branch', session: v.props.data?.session ?? '', node: r?.kind === 'node' ? r.nodeId : '', focus: key === 'B' })
      break
    case 'p':
      if (v.tree?.branch) {
        post({ type: 'open', session: v.tree.branch.from_session, node: v.tree.branch.from_node })
      } else {
        post({ type: 'notice', title: 'Not a branch', body: ['This session did not branch from another one, so it has no parent.'] })
      }
      break
    case 'f':
      if (!v.props.isOwn) {
        post({ type: 'own' })
      }
      break
    case 'q':
      post({ type: 'quit' })
      break
    default:
      return v.local.used ? undefined : l
  }
  l.cur = rowKey(v.rows[l.idx])
  l.atHere = v.rows[l.idx]?.here ?? false
  return l
}

/** A click on a row puts the cursor there. Any click hands the view the keys. */
function onPointer(v: View, e: ClientPointerEvent): Local | undefined {
  if (e.type !== 'down' || (e.button !== undefined && e.button !== 'left')) {
    return undefined
  }
  const l: Local = { ...v.local, used: true }
  if (v.props.overlay) {
    return l
  }
  const top = v.h >= 12 ? 2 : 1
  const i = v.screen[v.local.offset + (e.y - top)]?.row ?? -1
  if (e.y >= top && e.y < top + v.gh && stop(v.rows[i])) {
    l.idx = i
    l.cur = rowKey(v.rows[i])
    l.atHere = v.rows[i]?.here ?? false
  }
  return l
}

// ---- the module ----

function draw(s: ClientSurface<Local>, line: Line): RenderElement {
  const { Text } = s.elements
  if (line.length === 0) {
    return Text({ backgroundColor: BACKGROUND, children: ' ' })
  }
  return Text({
    wrap: 'truncate',
    backgroundColor: BACKGROUND,
    children: line.map(p => {
      // Only the styles a piece has: a Text refuses a color that is not one.
      const { color, bold, dim, underline } = p.style
      return Text({
        backgroundColor: BACKGROUND,
        ...(color ? { color } : {}),
        ...(bold ? { bold } : {}),
        ...(dim ? { dimColor: true } : {}),
        ...(underline ? { underline } : {}),
        children: p.text,
      })
    }),
  })
}

export default function TreeView(props: ViewProps, s: ClientSurface<Local>): RenderElement {
  const w = s.columns > 0 ? s.columns : props.columns
  const h = s.rows > 0 ? s.rows : props.rows
  const v = resolve(props, s.state, w, h)
  views.set(s, v)
  if (!wired.has(s)) {
    wired.add(s)
    s.onKey(k => {
      const cur = views.get(s)
      const next = cur && onKey(cur, k, s.post)
      if (next) {
        s.setState(next)
      }
    })
    s.onPointer(e => {
      const cur = views.get(s)
      const next = cur && onPointer(cur, e)
      if (next) {
        s.setState(next)
      }
    })
  }
  const { Box } = s.elements
  return Box({
    flexDirection: 'column',
    width: w,
    height: h,
    backgroundColor: BACKGROUND,
    children: lines(v).map(l => draw(s, l)),
  })
}
