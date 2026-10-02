// One session's decision tree and the rules for changing it. Claude changes
// it through record; the user changes it through the fix functions. Nothing
// here touches the disk. Every change works on a copy, so a call that fails
// changes nothing.
import type { By, Change, Decision, State, Tree, TreeNode } from '../types'

/** The tree file format this code writes. */
export const VERSION = 1

/** The start node every tree has. */
export const ROOT = 'n0'

/** How many fixes are kept for undo. */
export const MAX_UNDO = 50

/** A mistake in a call. Its message is written for Claude to read, so it says what to do instead. */
export class TreeError extends Error {}

/** One record_decision call. */
export type Call = {
  topic?: string
  options?: string[]
  picked?: string
  reason?: string
  by?: string
  decision_id?: string
  drop_later?: boolean
  after?: string
  at: string
}

/** What a call did. */
export type Result = {
  decisionId: string
  here: string // node id of "you are here"
  picked: string // the option this call picked, if the pick is new or changed
  open: string[] // decisions still being weighed on the live branch
  left: string[] // open decisions this call left behind on a set-aside branch
  skipped: string[] // options left out because the user deleted them
}

/** An empty tree: just the start node, with no label yet. */
export function newTree(sessionId: string, at: string): Tree {
  return {
    version: VERSION,
    session_id: sessionId,
    here: ROOT,
    nodes: [{ id: ROOT, label: '', state: 'picked', at }],
    decisions: [],
  }
}

const clone = (t: Tree): Tree => JSON.parse(JSON.stringify(t)) as Tree

/**
 * Trims a label, squeezes its inner spaces, and drops control characters:
 * Claude Code will not draw text that holds one, so one bad label would
 * blank the whole pane.
 */
export const clean = (s: string | undefined): string =>
  (s ?? '').replace(/[\u0000-\u001f\u007f-\u009f]/g, ' ').split(/\s+/).filter(Boolean).join(' ')

const sameLabel = (a: string, b: string) => clean(a).toLowerCase() === clean(b).toLowerCase()

export function node(t: Tree, id: string | undefined): TreeNode | undefined {
  return id === undefined ? undefined : t.nodes.find(n => n.id === id)
}

export function decision(t: Tree, id: string | undefined): Decision | undefined {
  return id === undefined ? undefined : t.decisions.find(d => d.id === id)
}

/** How a picked or changed option reads: "Database: PostgreSQL". */
export function statement(t: Tree, n: TreeNode): string {
  const d = decision(t, n.decision)
  return d ? `${d.topic}: ${n.label}` : n.label
}

/** The node that n's decision grows from; "" for the start. */
export function parentOf(t: Tree, n: TreeNode | undefined): string {
  return n?.decision ? (decision(t, n.decision)?.parent ?? '') : ''
}

/** Every node from "you are here" up to the start. */
export function livePath(t: Tree): Set<string> {
  const live = new Set<string>()
  for (let id = t.here; id !== '' && !live.has(id); id = parentOf(t, node(t, id))) {
    live.add(id)
  }
  return live
}

/** The decision's picked option, if any. */
export function pickOf(t: Tree, d: Decision): TreeNode | undefined {
  return d.options.map(id => node(t, id)).find(n => n && !n.hidden && n.state === 'picked')
}

/** Whether a visible decision grows from the node. */
function grows(t: Tree, id: string): boolean {
  return t.decisions.some(d => d.parent === id && !d.hidden)
}

/** Whether one of d's options was ever picked. */
function decided(t: Tree, d: Decision): boolean {
  return d.options.some(id => {
    const n = node(t, id)
    return n && !n.hidden && (n.state === 'picked' || n.state === 'dropped')
  })
}

/** A decision with options on the table and none ever picked. */
export function isOpen(t: Tree, d: Decision): boolean {
  if (d.hidden || decided(t, d)) {
    return false
  }
  return d.options.some(id => {
    const n = node(t, id)
    return n && !n.hidden && n.state === 'weighing'
  })
}

/** The decisions still being weighed on the live branch. */
export function openDecisions(t: Tree): Decision[] {
  const live = livePath(t)
  return t.decisions.filter(d => live.has(d.parent) && isOpen(t, d))
}

/** How many decisions show. */
export function decisionCount(t: Tree): number {
  return t.decisions.filter(d => !d.hidden).length
}

/** Moves open decisions on the live branch down to "you are here". */
function float(t: Tree): void {
  for (const d of openDecisions(t)) {
    d.parent = t.here
  }
}

/** The node right below p on the way down to "you are here". */
function childOnPath(t: Tree, p: string): TreeNode | undefined {
  for (let id = t.here; id !== ''; ) {
    const n = node(t, id)
    const parent = parentOf(t, n)
    if (parent === p) {
      return n
    }
    id = parent
  }
  return undefined
}

const lockedError = (n: TreeNode) => new TreeError(`The user fixed "${n.label}" (${n.id}) by hand. Leave it as it is.`)

/** Gives up everything below p on the live branch, and moves "you are here" up to p. */
function dropBranch(t: Tree, p: string, reason: string, at: string, fix: boolean): void {
  if (t.here === p) {
    return
  }
  const c = childOnPath(t, p)
  if (!c) {
    return
  }
  if (c.locked && !fix) {
    throw lockedError(c)
  }
  c.state = 'dropped'
  c.drop_reason = reason || undefined
  c.at = at
  t.here = p
}

function addDecision(t: Tree, topic: string, parent: string, at: string): Decision {
  const d: Decision = { id: `d${t.decisions.length + 1}`, topic, parent, options: [], at }
  t.decisions.push(d)
  return d
}

function addOption(t: Tree, d: Decision, label: string, state: State, at: string): TreeNode {
  const n: TreeNode = { id: `n${t.nodes.length}`, decision: d.id, label, state, at }
  t.nodes.push(n)
  d.options.push(n.id)
  return n
}

function optionByLabel(t: Tree, d: Decision, label: string): TreeNode | undefined {
  return d.options.map(id => node(t, id)).find(n => n && sameLabel(n.label, label))
}

/** Applies one call from Claude to a copy of the tree. A wrong call throws a TreeError. */
export function record(tree: Tree, c: Call): { tree: Tree; result: Result } {
  const t = clone(tree)
  const openBefore = openDecisions(t)
  const opts = (c.options ?? []).map(clean).filter(o => o !== '')
  const picked = clean(c.picked)
  const reason = clean(c.reason)
  if (picked !== '') {
    if (reason === '') {
      throw new TreeError(`reason is needed with picked: one line on why "${picked}" won.`)
    }
    if (c.by !== 'user' && c.by !== 'claude' && c.by !== 'both') {
      throw new TreeError('by is needed with picked: "user", "claude", or "both".')
    }
  }

  let d: Decision | undefined
  if (!c.decision_id) {
    if (clean(c.topic) === '') {
      throw new TreeError('topic is needed for a new decision: a short statement like "Database used".')
    }
    if (opts.length === 0) {
      throw new TreeError('options is needed: list every option that was talked about.')
    }
    if (picked !== '' && !opts.some(o => sameLabel(o, picked))) {
      throw new TreeError(`picked "${picked}" is not one of the options. Add it to options.`)
    }
    let parent = t.here
    if (c.after) {
      const n = node(t, c.after)
      if (!n || n.hidden) {
        throw new TreeError(`after: there is no node "${c.after}". Call show_decision_tree to see the ids.`)
      }
      if (!livePath(t).has(n.id)) {
        throw new TreeError(
          `after: "${n.label}" (${n.id}) is not on the live branch. To go back to an option that was not picked, call with its decision_id and picked instead.`,
        )
      }
      dropBranch(t, n.id, reason, c.at, false)
      parent = n.id
    }
    d = addDecision(t, clean(c.topic), parent, c.at)
  } else {
    d = decision(t, c.decision_id)
    if (!d) {
      throw new TreeError(`there is no decision "${c.decision_id}". Call show_decision_tree to see the ids.`)
    }
    if (d.hidden) {
      throw new TreeError(`decision ${d.id} was deleted by the user. Leave it out.`)
    }
  }

  // New options join as "being weighed", or as "not picked" when the
  // decision was already made.
  const state: State = decided(t, d) ? 'not_picked' : 'weighing'
  const skipped: string[] = []
  for (const o of opts) {
    const n = optionByLabel(t, d, o)
    if (n) {
      if (n.hidden) {
        skipped.push(o)
      }
      continue
    }
    addOption(t, d, o, state, c.at)
  }

  let newPick = ''
  if (picked !== '') {
    const x = optionByLabel(t, d, picked)
    if (!x) {
      throw new TreeError(`picked "${picked}" is not an option of ${d.id}. Add it to options.`)
    }
    const before = pickOf(t, d)
    pick(t, d, x, reason, c.by as By, c.at, false, !!c.drop_later)
    if (x !== before) {
      newPick = x.id
    }
  }
  float(t)

  const openNow = openDecisions(t)
  const result: Result = {
    decisionId: d.id,
    here: t.here,
    picked: newPick,
    open: openNow.map(o => o.id),
    // Still open, but no longer on the live branch: going back left it
    // behind. Claude should hear about it, or it may ask the question again.
    left: openBefore.filter(o => !openNow.includes(o) && isOpen(t, o)).map(o => o.id),
    skipped,
  }
  return { tree: t, result }
}

/**
 * Makes x the winner of d.
 *
 * First pick: d moves down to "you are here", so the tree reads in the order
 * things were decided. Changing an earlier pick happens in place: the old
 * pick is marked changed, and the decisions made after it move over to x.
 * With dropLater they are set aside instead, in their own lane under the old
 * pick. fix is true for the user's own fixes, which may change locked nodes.
 */
function pick(t: Tree, d: Decision, x: TreeNode, reason: string, by: string, at: string, fix: boolean, dropLater: boolean): void {
  if (x.hidden) {
    throw new TreeError(`"${x.label}" was deleted by the user. Leave it out.`)
  }
  const old = pickOf(t, d)
  if (old === x) {
    if (!x.locked || fix) {
      x.reason = reason || undefined
      x.by = by
      x.at = at
    }
    return
  }
  if (x.locked && !fix) {
    throw lockedError(x)
  }
  if (x.state === 'dropped' && grows(t, x.id) && !fix) {
    throw new TreeError(`"${x.label}" heads a branch that was set aside. To try it again, start a new decision.`)
  }

  if (!decided(t, d)) {
    d.parent = t.here
  } else if (!livePath(t).has(d.parent)) {
    throw new TreeError(`decision ${d.id} is on a branch that was set aside. Start a new decision instead.`)
  } else if (old && old.locked && !fix) {
    throw lockedError(old)
  } else if (dropLater) {
    dropBranch(t, d.parent, reason, at, fix)
    if (old && old.state === 'picked') {
      Object.assign(old, { state: 'dropped', drop_reason: reason || undefined, at })
    }
  } else if (old) {
    Object.assign(old, { state: 'dropped', drop_reason: reason || undefined, at })
    for (const c of t.decisions) {
      if (c.parent === old.id) {
        c.parent = x.id
      }
    }
  }

  for (const id of d.options) {
    const n = node(t, id)
    if (n && n !== x && !n.hidden && n.state === 'weighing' && (!n.locked || fix)) {
      n.state = 'not_picked'
      n.at = at
    }
  }
  x.state = 'picked'
  x.by = by
  x.at = at
  delete x.drop_reason
  if (reason !== '') {
    x.reason = reason
  }
  // "You are here" moves to x, unless it is already further down x's line
  // (a change in place keeps it where it was).
  if (!livePath(t).has(x.id)) {
    t.here = x.id
  }
}

/**
 * A copy of the tree as far as node id: everything that grew after it is
 * hidden, and id is "you are here". Ids stay the same. Only a fallback for
 * a branch when no copy of the tree was saved at the time.
 */
export function upTo(tree: Tree, id: string): Tree {
  const t = clone(tree)
  const path = new Set<string>()
  for (let at = id; at !== '' && !path.has(at); at = parentOf(t, node(t, at))) {
    path.add(at)
  }
  for (const d of t.decisions) {
    if (d.options.some(o => path.has(o))) {
      continue
    }
    d.hidden = true
    for (const o of d.options) {
      const n = node(t, o)
      if (n) {
        n.hidden = true
      }
    }
  }
  t.here = id
  delete t.fixes
  return t
}

// ---- the user's fixes ----

/** Makes an option the winner, even if nodes around it are locked, and locks it. */
export function fixPick(tree: Tree, id: string, at: string): Tree {
  return fix(tree, `pick ${id}`, w => {
    const x = node(w, id)
    const d = decision(w, x?.decision)
    if (!x || x.hidden || !d) {
      throw new TreeError(`${id} is not an option.`)
    }
    pick(w, d, x, '', 'user', at, true, false)
    x.locked = true
    float(w)
  })
}

/** Gives a node a new label and locks it. */
export function fixRename(tree: Tree, id: string, label: string): Tree {
  return fix(tree, `rename ${id}`, w => {
    const n = node(w, id)
    if (!n || n.hidden) {
      throw new TreeError(`there is no node ${id}.`)
    }
    if (clean(label) === '') {
      throw new TreeError('the new label is empty.')
    }
    n.label = clean(label)
    n.locked = true
  })
}

/** Hides a node and everything that grows from it. Hidden nodes stay, locked, so Claude cannot add them back. */
export function fixDelete(tree: Tree, id: string): Tree {
  return fix(tree, `delete ${id}`, w => {
    const n = node(w, id)
    if (!n || n.hidden) {
      throw new TreeError(`there is no node ${id}.`)
    }
    if (n.id === ROOT) {
      throw new TreeError('the start node cannot be deleted.')
    }
    hide(w, n)
    const d = decision(w, n.decision)
    if (d && !d.options.some(o => !node(w, o)?.hidden)) {
      d.hidden = true
    }
    // If "you are here" was deleted, move it up to the nearest node left.
    while (node(w, w.here)?.hidden) {
      w.here = parentOf(w, node(w, w.here))
    }
    float(w)
  })
}

function hide(t: Tree, n: TreeNode): void {
  n.hidden = true
  n.locked = true
  for (const d of t.decisions) {
    if (d.parent === n.id) {
      d.hidden = true
      for (const o of d.options) {
        const opt = node(t, o)
        if (opt) {
          hide(t, opt)
        }
      }
    }
  }
}

/** Takes back the user's last fix. Changes Claude made since then stay, unless the fix is what they grew from. */
export function undo(tree: Tree): Tree {
  const t = clone(tree)
  const ch = t.fixes?.pop()
  if (!ch) {
    throw new TreeError('nothing to undo')
  }
  for (const n of ch.nodes ?? []) {
    const i = t.nodes.findIndex(m => m.id === n.id)
    t.nodes[i] = n
  }
  for (const d of ch.decisions ?? []) {
    const i = t.decisions.findIndex(m => m.id === d.id)
    t.decisions[i] = d
  }
  if (t.here === ch.here_after || !pathOK(t, t.here)) {
    t.here = ch.here
  }
  return t
}

/** Whether every node from id up to the start is picked and not hidden. */
function pathOK(t: Tree, id: string): boolean {
  for (let at = id; at !== ''; at = parentOf(t, node(t, at))) {
    const n = node(t, at)
    if (!n || n.hidden || n.state !== 'picked') {
      return false
    }
  }
  return true
}

/** Runs one fix on a copy. If it works, what changed is saved for undo. */
function fix(tree: Tree, what: string, f: (w: Tree) => void): Tree {
  const w = clone(tree)
  f(w)
  const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b)
  const ch: Change = { what, here: tree.here, here_after: w.here }
  const nodes = tree.nodes.filter((n, i) => !same(n, w.nodes[i]))
  const decisions = tree.decisions.filter((d, i) => !same(d, w.decisions[i]))
  if (nodes.length > 0) {
    ch.nodes = clone({ ...tree, nodes }).nodes
  }
  if (decisions.length > 0) {
    ch.decisions = clone({ ...tree, decisions }).decisions
  }
  w.fixes = [...(w.fixes ?? []), ch].slice(-MAX_UNDO)
  return w
}
