// The git-style picture of a tree, and the text Claude gets back from the tools.
import { describe, expect, test } from 'claude-code/testing'

import type { Tree } from '../types'
import { parseTree } from '../hooks/files'
import { defaultFolded, layout, plain } from '../hooks/graph'
import { showText, summary } from '../hooks/show'
import { newTree, record } from '../hooks/tree'

const at = '2026-09-29T15:00:00.000Z'

/** Builds a tree from calls, as Claude would make them. */
function build(title: string, ...calls: Record<string, unknown>[]): Tree {
  let t = newTree('s1', at)
  t.nodes[0]!.label = title
  for (const c of calls) {
    t = record(t, { at, ...c }).tree
  }
  return t
}

const picture = (s: string) => s.replace(/^\n/, '')

// The CRM example: the index was set aside for the cache (drop_later), and a
// question is still open.
const crm = () =>
  build(
    'CRM search is slow',
    { topic: 'Fix', options: ['Rust rewrite', 'Cache', 'Index'] },
    { decision_id: 'd1', picked: 'Index', reason: 'fixes the query', by: 'both' },
    { topic: 'Index on', options: ['email', 'company + date'], picked: 'company + date', reason: 'matches the search', by: 'claude' },
    { decision_id: 'd1', picked: 'Cache', reason: 'the index did not help', by: 'user', drop_later: true },
    { topic: 'Cache time', options: ['5 minutes', '1 hour'] },
  )

describe('graph', () => {
  test('the main line, with a decision still open', () => {
    const t = build(
      'Notes app database choice',
      { topic: 'Database used', options: ['SQLite', 'Postgres'], picked: 'SQLite', reason: 'small app', by: 'user' },
      { topic: 'Who uses it', options: ['Just me', 'Multiple users'], picked: 'Just me', reason: 'personal app', by: 'user' },
      { topic: 'Deploy time', options: ['Tonight', 'Now'] },
    )
    expect(plain(layout(t))).toBe(picture(`
●  Notes app database choice
│
├─×  Postgres
●  Database used: SQLite
│
├─×  Multiple users
●  Who uses it: Just me  ◀
│
┊  Deploy time: ?
├─◌  Tonight
╰─◌  Now`))
  })

  test('a branch set aside gets its own lane, folded at first', () => {
    const t = crm()
    expect(plain(layout(t))).toBe(picture(`
●  CRM search is slow
│
├─×  Rust rewrite
├─╮
│ ↺  Fix: Index
│ │
│ ├─×  email
│ ●  Index on: company + date
●  Fix: Cache  ◀
│
┊  Cache time: ?
├─◌  5 minutes
╰─◌  1 hour`))
    expect(plain(layout(t, id => defaultFolded(t, id)))).toContain('│ ↺  Fix: Index ▸ 2 more\n●  Fix: Cache  ◀')
    expect(plain(layout(t, id => id === 'n0'))).toBe('●  CRM search is slow ▸ 7 more')
  })

  test('a lane that ends the graph closes it', () => {
    const t = build(
      'App',
      { topic: 'Language', options: ['Go'], picked: 'Go', reason: 'one file', by: 'user' },
      { topic: 'CLI library', options: ['cobra'], picked: 'cobra', reason: 'common', by: 'claude' },
    )
    t.nodes[1]!.state = 'dropped'
    t.here = 'n0'
    expect(plain(layout(t))).toBe(picture(`
●  App  ◀
│
╰─╮
  ↺  Language: Go
  │
  ●  CLI library: cobra`))
  })

  test('branches hang under the pick they came from', () => {
    const t = build(
      'Build an API',
      { topic: 'API framework', options: ['FastAPI', 'Flask'], picked: 'FastAPI', reason: 'r', by: 'both' },
      { topic: 'Cache', options: ['Redis'], picked: 'Redis', reason: 'r', by: 'claude' },
      { decision_id: 'd1', options: ['Django'], picked: 'Django', reason: 'r', by: 'user' },
    )
    const branches = {
      n1: [
        { session: 'b1', name: 'flask-instead', decisions: 1 },
        { session: 'b2', name: 'try-litestar', decisions: 3 },
      ],
      n3: [{ session: 'b3', name: 'no-cache', decisions: 0 }],
    }
    expect(plain(layout(t, undefined, branches))).toBe(picture(`
●  Build an API
│
├─╮
│ ↺  API framework: FastAPI
│ ├─⎇  flask-instead · 1 decision
│ ╰─⎇  try-litestar · 3 decisions
├─×  Flask
●  API framework: Django
│
●  Cache: Redis  ◀
╰─⎇  no-cache · 0 decisions`))
    const rows = layout(t, undefined, branches).filter(r => r.kind === 'branch')
    expect(rows.map(r => `${r.session}@${r.nodeId}`)).toEqual(['b1@n1', 'b2@n1', 'b3@n3'])
    expect(plain(layout(t, id => id === 'n1', branches))).toContain('│ ↺  API framework: FastAPI ▸ 2 more')
  })

  test('a tree saved before 30 Sep 2026 still loads', () => {
    const old = '{"version":1,"session_id":"s","here":"n1","nodes":[{"id":"n0","label":"App","state":"picked","at":""},{"id":"n1","decision":"d1","label":"SQLite","state":"picked","at":""}],"decisions":[{"id":"d1","question":"Database for the app?","parent":"n0","options":["n1"],"at":""}]}'
    expect(plain(layout(parseTree('old.json', old)))).toBe(picture(`
●  App
│
●  Database for the app?: SQLite  ◀`))
  })
})

describe('what the tools answer', () => {
  test('show_decision_tree: the tree with ids', () => {
    const t = build('', { topic: 'Database', options: ['Postgres', 'SQLite'], picked: 'Postgres', reason: 'many writers', by: 'user' })
    expect(showText(t)).toBe(
      '● picked  × rejected  ↺ changed later  ◌ still open  ◀ you are here\n\n' +
        '● n0 start\n  d1: Database\n    ● n1 Postgres ◀\n    × n2 SQLite',
    )
  })

  test('record_decision: one line, naming questions left behind', () => {
    let t = build('', { topic: 'API', options: ['REST'], picked: 'REST', reason: 'simple', by: 'both' })
    const r = record(t, { decision_id: 'd1', options: ['GraphQL'], picked: 'GraphQL', reason: 'the user changed it', by: 'user', at })
    expect(summary(r.tree, r.result)).toBe('Saved as d1. You are here: GraphQL (n2). Still open: none.')

    t = build('', { topic: 'Database', options: ['SQLite', 'Postgres'], picked: 'SQLite', reason: 'one user', by: 'user' }, { topic: 'Front end', options: ['HTML', 'React'] })
    const back = record(t, { decision_id: 'd1', picked: 'Postgres', reason: 'Vercel', by: 'user', drop_later: true, at })
    expect(summary(back.tree, back.result)).toContain('Left behind on the dropped branch, still unanswered: d2 "Front end".')
  })
})
