// The graph layout: a tree as a git-style graph, one row per line, oldest
// at the top.
//
//	●  Notes app
//	│
//	├─×  SQLite                    rejected: a stub off the main line
//	●  Database: PostgreSQL        picked: the main line goes on from here
//	│
//	├─↺  REST                      picked, then changed later
//	●  API: GraphQL
//
// A stub hangs from the node its decision grows from, so the graph reads
// like git: the fork is at the parent, and the pick carries the line on.
import type { Decision, State, Stub, Tree, TreeNode } from '../types'
import { plural } from './text'
import { isOpen, node, pickOf, ROOT, statement } from './tree'

export const SYMBOLS: Record<State, string> = {
  picked: '●',
  not_picked: '×', // rejected
  weighing: '◌',
  dropped: '↺', // picked, then changed later
}

/** What a row shows: a node (the cursor stops here), only lines, the heading
 * of a decision still open, or a session that branched from the node above. */
export type Kind = 'node' | 'line' | 'open' | 'branch'

export type Row = {
  kind: Kind
  graph: string // the lines left of the symbol, like "│ ├─"
  cont: string // what goes left of the text when it wraps: the lines that carry on below, as wide as graph + symbol
  symbol: string
  text: string
  nodeId: string // for node and branch rows
  state?: State // for node rows
  here: boolean
  folded: number // how many rows are folded away under this one
  session: string // for branch rows: the branch's session
}

function visibleOptions(t: Tree, d: Decision): number {
  return d.options.filter(id => !node(t, id)?.hidden).length
}

/** Whether any visible decision grows from the node, so there is something to fold. */
export function hasChildren(t: Tree, id: string): boolean {
  return t.decisions.some(d => d.parent === id && !d.hidden && visibleOptions(t, d) > 0)
}

/** How a node starts: a dropped branch is folded, so the live path stays short. */
export function defaultFolded(t: Tree, id: string): boolean {
  const n = node(t, id)
  return !!n && n.state === 'dropped' && hasChildren(t, id)
}

/** Lays out the visible tree. folded says which nodes hide what grows from
 * them; branches are the sessions that branched from each node, by node id. */
export function layout(
  t: Tree,
  folded: (id: string) => boolean = () => false,
  branches: Record<string, Stub[]> = {},
): Row[] {
  const rows: Row[] = []
  const stubsOf = (id: string) => branches[id] ?? []
  const kids = (id: string) => hasChildren(t, id) || stubsOf(id).length > 0

  // count is how many visible nodes and branches grow from id, all the way down.
  const count = (id: string): number => {
    let n = stubsOf(id).length
    for (const d of t.decisions) {
      if (d.parent !== id || d.hidden) {
        continue
      }
      for (const oid of d.options) {
        const o = node(t, oid)
        if (o && !o.hidden) {
          n += 1 + count(o.id)
        }
      }
    }
    return n
  }

  // text is how a node reads. A pick, or a node heading its own lane, reads
  // as a statement; a stub is just its label, since the pick below names the topic.
  const text = (n: TreeNode, stub: boolean): string => {
    if (n.id === ROOT) {
      return n.label === '' ? 'start' : n.label
    }
    return stub ? n.label : statement(t, n)
  }

  const row = (r: Partial<Row> & Pick<Row, 'kind' | 'graph'>): Row => ({
    cont: r.graph,
    symbol: '',
    text: '',
    nodeId: '',
    here: false,
    folded: 0,
    session: '',
    ...r,
  })

  // draw puts n on the lane that starts with pre, then everything that grows
  // from it. lead is the connector right before n's symbol: "" on the main
  // line of a lane, "├─" or "╰─" for a stub.
  const draw = (n: TreeNode, pre: string, lead: string): void => {
    const isOpenBelow = kids(n.id) && !folded(n.id)
    const r = row({
      kind: 'node',
      graph: pre + lead,
      // On its lane, the line goes on below a node with something under it;
      // a stub has only its lane going on, under "├" and not under "╰".
      cont: lead === '' ? pre + (isOpenBelow ? '│' : ' ') : pre + (lead === '├─' ? '│' : ' ') + '  ',
      symbol: n.id === ROOT ? '●' : SYMBOLS[n.state],
      text: text(n, lead !== ''),
      nodeId: n.id,
      state: n.state,
      here: n.id === t.here,
    })
    if (!kids(n.id)) {
      rows.push(r)
      return
    }
    if (folded(n.id)) {
      rows.push({ ...r, folded: count(n.id) })
      return
    }
    rows.push(r)
    // Branches first, right under the node they came from.
    const stubs = stubsOf(n.id)
    stubs.forEach((b, i) => {
      const fork = i === stubs.length - 1 && !hasChildren(t, n.id) ? '╰─' : '├─'
      rows.push(
        row({
          kind: 'branch',
          graph: pre + fork,
          cont: pre + (fork === '├─' ? '│' : ' ') + '  ',
          symbol: '⎇',
          text: `${b.name} · ${plural(b.decisions, 'decision')}`,
          session: b.session,
          nodeId: n.id,
        }),
      )
    })
    children(n.id, pre)
  }

  // children draws the decisions that grow from node id, on the lane pre.
  const children = (id: string, pre: string): void => {
    let decs = t.decisions.filter(d => d.parent === id && !d.hidden && visibleOptions(t, d) > 0)
    // The last decision with a pick carries the lane on; draw it last.
    let carry = -1
    decs.forEach((d, i) => {
      if (pickOf(t, d)) {
        carry = i
      }
    })
    if (carry >= 0) {
      const carrier = decs[carry] as Decision
      decs = [...decs.slice(0, carry), ...decs.slice(carry + 1), carrier]
    }

    decs.forEach((d, i) => {
      const carries = carry >= 0 && i === decs.length - 1
      const pick = pickOf(t, d)
      rows.push(row({ kind: 'line', graph: pre + '│' }))
      if (isOpen(t, d)) {
        rows.push(row({ kind: 'open', graph: pre + '┊', text: `${d.topic}: ?` }))
      }
      const side = d.options
        .map(oid => node(t, oid))
        .filter((o): o is TreeNode => !!o && !o.hidden && !(carries && o === pick))
      side.forEach((o, j) => {
        // The last stub closes the lane with "╰─", unless the lane goes on.
        const last = j === side.length - 1 && i === decs.length - 1 && !carries
        const [fork, down] = last ? ['╰─', '  '] : ['├─', '│ ']
        if (kids(o.id)) {
          rows.push(row({ kind: 'line', graph: pre + fork + '╮' }))
          draw(o, pre + down, '')
        } else {
          draw(o, pre, fork)
        }
      })
      if (carries && pick) {
        draw(pick, pre, '')
      }
    })
  }

  const root = t.nodes[0]
  if (root) {
    draw(root, '', '')
  }
  return rows
}

/** Rows as text with no colors, as `decision-tree print` draws them. */
export function plain(rows: Row[]): string {
  return rows
    .map(r => {
      let s = r.graph
      if (r.symbol !== '') {
        s += r.symbol + '  '
      } else if (r.text !== '') {
        s += '  '
      }
      s += r.text
      if (r.folded > 0) {
        s += ` ▸ ${r.folded} more`
      }
      if (r.here) {
        s += '  ◀'
      }
      return s
    })
    .join('\n')
}
