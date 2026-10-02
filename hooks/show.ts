// The tree as short text for Claude: show_decision_tree's answer, and the
// one-line reply to record_decision.
import type { Tree, TreeNode } from '../types'
import type { Result } from './tree'
import { decision, node, ROOT } from './tree'

/** Explains the symbols. */
export const LEGEND = '● picked  × rejected  ↺ changed later  ◌ still open  ◀ you are here'

const SYMBOL: Record<string, string> = { picked: '●', not_picked: '×', weighing: '◌', dropped: '↺' }

/** A node's label, or "start" for the unnamed start node. */
export function label(n: TreeNode): string {
  return n.label === '' && n.id === ROOT ? 'start' : n.label
}

/** The visible tree, oldest at the top, with node and decision ids. */
export function showText(t: Tree): string {
  const lines: string[] = []
  const draw = (n: TreeNode, depth: number) => {
    lines.push(`${'  '.repeat(depth)}${SYMBOL[n.state] ?? '?'} ${n.id} ${label(n)}${n.id === t.here ? ' ◀' : ''}`)
    for (const d of t.decisions) {
      if (d.parent !== n.id || d.hidden) {
        continue
      }
      lines.push(`${'  '.repeat(depth + 1)}${d.id}: ${d.topic}`)
      for (const id of d.options) {
        const opt = node(t, id)
        if (opt && !opt.hidden) {
          draw(opt, depth + 2)
        }
      }
    }
  }
  const root = t.nodes[0]
  if (root) {
    draw(root, 0)
  }
  return `${LEGEND}\n\n${lines.join('\n')}`
}

/** Lists decisions as: d2 "Front end", d5 "Deploy time". */
function quoted(t: Tree, ids: string[]): string {
  return ids.map(id => `${id} "${decision(t, id)?.topic ?? ''}"`).join(', ')
}

/** The one-line reply to record_decision. */
export function summary(t: Tree, r: Result): string {
  const here = node(t, r.here)
  let s = `Saved as ${r.decisionId}. You are here: ${here ? label(here) : r.here} (${r.here}).`
  s += r.open.length === 0 ? ' Still open: none.' : ` Still open: ${quoted(t, r.open)}.`
  if (r.left.length > 0) {
    s +=
      ` Left behind on the dropped branch, still unanswered: ${quoted(t, r.left)}.` +
      ' If one still matters, answer it with its decision_id (add new options if needed) and it moves here. Do not log it again as a new decision.'
  }
  if (r.skipped.length > 0) {
    s += ` Left out because the user deleted them: ${r.skipped.join(', ')}.`
  }
  return s
}
