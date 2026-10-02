import type { On } from 'claude-code'
import { describe, expect, mock, test } from 'claude-code/testing'

import { defaultFolded, layout, plain } from '../hooks/graph'
import type { Data, Tree } from '../types'

const at = '2026-09-29T15:00:00Z'

// crm, as internal/graph's test builds it: the index was set aside for the
// cache (drop_later), and a question is still open (a tree from before 29 Sep).
const crm: Tree = {
  session_id: 's1',
  folder: '/w/crm',
  here: 'n2',
  nodes: [
    { id: 'n0', label: 'CRM search is slow', state: 'picked', at },
    { id: 'n1', decision: 'd1', label: 'Rust rewrite', state: 'not_picked', at },
    { id: 'n2', decision: 'd1', label: 'Cache', state: 'picked', reason: 'the index did not help', by: 'user', at, checkpoint: { tool_use_id: 'toolu_2', commit: 'abc123def4567890' } },
    { id: 'n3', decision: 'd1', label: 'Index', state: 'dropped', reason: 'fixes the query', by: 'both', drop_reason: 'the index did not help', at },
    { id: 'n4', decision: 'd2', label: 'email', state: 'not_picked', at },
    { id: 'n5', decision: 'd2', label: 'company + date', state: 'picked', reason: 'matches the search', by: 'claude', at },
    { id: 'n6', decision: 'd3', label: '5 minutes', state: 'weighing', at },
    { id: 'n7', decision: 'd3', label: '1 hour', state: 'weighing', at },
  ],
  decisions: [
    { id: 'd1', topic: 'Fix', parent: 'n0', options: ['n1', 'n2', 'n3'], at },
    { id: 'd2', topic: 'Index on', parent: 'n3', options: ['n4', 'n5'], at },
    { id: 'd3', topic: 'Cache time', parent: 'n2', options: ['n6', 'n7'], at },
  ],
}

// FastAPI was later changed to Django; branches were made from FastAPI
// (twice) and from Redis.
const api: Tree = {
  session_id: 's2',
  here: 'n3',
  nodes: [
    { id: 'n0', label: 'Build an API', state: 'picked', at },
    { id: 'n1', decision: 'd1', label: 'FastAPI', state: 'dropped', at },
    { id: 'n2', decision: 'd1', label: 'Flask', state: 'not_picked', at },
    { id: 'n3', decision: 'd2', label: 'Redis', state: 'picked', at },
    { id: 'n4', decision: 'd1', label: 'Django', state: 'picked', at },
  ],
  decisions: [
    { id: 'd1', topic: 'API framework', parent: 'n0', options: ['n1', 'n2', 'n4'], at },
    { id: 'd2', topic: 'Cache', parent: 'n4', options: ['n3'], at },
  ],
}
const apiBranches = {
  n1: [
    { session: 'b1', name: 'flask-instead', decisions: 1 },
    { session: 'b2', name: 'try-litestar', decisions: 3 },
  ],
  n3: [{ session: 'b3', name: 'no-cache', decisions: 0 }],
}

const picture = (s: string) => s.replace(/^\n/, '')

// The same pictures as internal/graph's tests, so the two layouts agree.
describe('graph', () => {
  test('a branch set aside gets its own lane', () => {
    expect(plain(layout(crm))).toBe(picture(`
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
  })

  test('a dropped branch starts folded, and the start folds everything', () => {
    expect(plain(layout(crm, id => defaultFolded(crm, id)))).toContain('│ ↺  Fix: Index ▸ 2 more\n●  Fix: Cache  ◀')
    expect(plain(layout(crm, id => id === 'n0'))).toBe('●  CRM search is slow ▸ 7 more')
  })

  test('branches hang under the pick they came from', () => {
    expect(plain(layout(api, undefined, apiBranches))).toBe(picture(`
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
    const rows = layout(api, undefined, apiBranches).filter(r => r.kind === 'branch')
    expect(rows.map(r => `${r.session}@${r.nodeId}`)).toEqual(['b1@n1', 'b2@n1', 'b3@n3'])
    expect(plain(layout(api, id => id === 'n1', apiBranches))).toContain('│ ↺  API framework: FastAPI ▸ 2 more')
  })
})

// ---- the pane ----

const SPEC = {
  instructions: 'Log a decision only once it is made.',
  tools: [{ name: 'show_decision_tree', description: 'Show the tree.', inputSchema: { type: 'object' } }],
}

const PANE = {
  plugin: 'decision-tree',
  component: 'Pane',
  requestId: 'decision-tree',
  props: {
    title: 'Decisions',
    isFocused: false,
    bodyColumns: 60,
    placement: 'dock',
    scroll: { offset: 0, bodyRows: 24 },
    view: {},
  },
} as const

type Seen = { runs: string[][]; data: Data; mtime: number }

// Stands in for the engine: a fake Go program over a fake tree file.
const engine = (on: On, tree: Tree | null = crm) => {
  const seen: Seen = { runs: [], data: { session: 's1', path: '/state/s1.json', tree, branches: {} }, mtime: 1 }
  on('process.run', (_$, e) => {
    const argv = [...e.argv]
    seen.runs.push(argv)
    const ok = (out: unknown) => ({ value: { exitCode: 0, stdout: JSON.stringify(out), stderr: '', isStdoutTruncated: false, isStderrTruncated: false } })
    switch (argv[1]) {
      case 'describe':
        return ok(SPEC)
      case 'data':
        return ok(seen.data)
      case 'source':
        return ok({ prompt_number: 1, prompt: 'Search is slow. Add a cache.', before: 'An index or a cache would both help.' })
      case 'branch':
        return argv.includes('--plan')
          ? ok({ name: 'fix-cache', summary: 'Branch from:  Fix: Cache · 29 Sep 16:00\nName:         fix-cache' })
          : ok({ name: 'fix-cache', session: 'b4b4b4b4-0000', dir: '/w/worktrees/crm/fix-cache', pane: '%9', command: '', copied: null })
    }
    return ok('')
  })
  on('session.id', () => ({ value: 's1' }))
  on('session.cwd', () => ({ value: '/w/crm' }))
  on('fs.stat', () => ({ value: { kind: 'file', size: 1, mtimeMs: seen.mtime, isLink: false } }))
  on('ui.open', () => ({ value: { isPlaced: true } }))
  on('session.start', (_$, e) => ({ cwd: e.cwd }))
  on('tool.register', (_$, e) => ({ value: { tool: `mcp__decision-tree__${e.name}` } }))
  on('ui.toast', () => ({ value: undefined }))
  on('command.register', (_$, e) => ({ value: { command: e.name } }))
  return seen
}

const start = { cwd: '/w/crm', surface: 'terminal', isInteractive: true } as const

// /decision-tree as the person types it, in a fullscreen terminal.
const command = (args = '') => ({
  command: 'decision-tree',
  args,
  origin: { kind: 'composer' as const },
  presentation: { isFullscreen: true, columns: 160 },
})

async function shown(ui: { findAll: (q: { in: string; type: string }) => Promise<{ text: string }[]> }): Promise<string> {
  const lines = await ui.findAll({ in: 'tree', type: 'Text' })
  return lines.map(l => l.text).join('\n')
}

describe('pane', () => {
  for (const surface of ['terminal', 'desktop'] as const) {
    test(`draws the tree with the cursor on "you are here" (${surface})`, async ($, on) => {
      engine(on)
      await $.session.start(start)
      await $.command.run(command())
      const ui = await $.ui.mount({ ...PANE, surface })
      await ui.resize({ columns: 60, rows: 24, in: 'tree' })
      const text = await shown(ui)
      expect(text).toMatch(/^ Decision *$/m)
      expect(text).not.toContain('decisions')
      expect(text).toContain('●  Start')
      expect(text).toContain('│ ↺  Fix: Index ▸ 2 more')
      expect(text).toContain('› ●  Fix: Cache  ◀')
      expect(text).toContain('Picked by you')
      expect(text).toContain('Why: the index did not help')
      expect(text).toContain('click for keys')
    })
  }

  test('the pane is black, edge to edge', async ($, on) => {
    engine(on)
    await $.session.start(start)
    await $.command.run(command())
    const ui = await $.ui.mount({ ...PANE, surface: 'terminal' })
    await ui.resize({ columns: 60, rows: 24, in: 'tree' })
    const root = (await ui.drawn({ in: 'tree' })) as { props: Record<string, unknown> }
    expect(root.props).toMatchObject({ backgroundColor: '#000000', width: 60, height: 24 })
    const texts = await ui.findAll({ in: 'tree', type: 'Text' })
    expect(texts.every(t => t.props.backgroundColor === '#000000')).toBe(true)
    // Each line fills the width, so no cell is left without the background.
    const lines = texts.filter(t => t.text.length >= 60)
    expect(lines.length).toBeGreaterThanOrEqual(24)
  })

  test('a long row wraps, with the tree line carried on beside it', async ($, on) => {
    const long = {
      ...crm,
      nodes: crm.nodes.map(n => (n.id === 'n2' ? { ...n, label: 'Cache the search results in memory for five minutes' } : n)),
    }
    engine(on, long)
    await $.session.start(start)
    await $.command.run(command())
    const ui = await $.ui.mount({ ...PANE, surface: 'terminal' })
    await ui.resize({ columns: 30, rows: 24, in: 'tree' })
    const text = await shown(ui)
    expect(text).toContain('› ●  Fix: Cache the search')
    expect(text).toContain('  │  results in memory for')
    expect(text).toContain('  │  five minutes  ◀')
    // The details panel wraps too, so the whole text can be read.
    expect(text).toContain('results in memory for five')
    expect(text).not.toContain('…')

    // A click on a wrapped line picks its row. Lines: header, gap, Start,
    // │, Rust rewrite, ├─╮, Fix: Index, then the three lines of the pick.
    await ui.key({ key: 'g', in: 'tree' })
    expect(await shown(ui)).toContain('› ●  Start')
    await ui.pointer({ type: 'down', x: 10, y: 8, button: 'left', in: 'tree' })
    expect(await shown(ui)).toContain('› ●  Fix: Cache the search')
  })

  test('keys move the cursor and fold', async ($, on) => {
    engine(on)
    await $.session.start(start)
    await $.command.run(command())
    const ui = await $.ui.mount({ ...PANE, surface: 'terminal' })
    await ui.resize({ columns: 60, rows: 24, in: 'tree' })

    await ui.key({ key: 'k', in: 'tree' })
    let text = await shown(ui)
    expect(text).toContain('› │ ↺  Fix: Index ▸ 2 more')
    expect(text).toContain('Changed to Cache')
    expect(text).not.toContain('click for keys')

    await ui.key({ key: 'space', in: 'tree' })
    text = await shown(ui)
    expect(text).toContain('│ ●  Index on: company + date')

    await ui.key({ key: 'g', in: 'tree' })
    expect(await shown(ui)).toContain('› ●  Start')
    await ui.key({ key: '.', in: 'tree' })
    expect(await shown(ui)).toContain('› ●  Fix: Cache  ◀')
  })

  test('a click puts the cursor on the row', async ($, on) => {
    engine(on)
    await $.session.start(start)
    await $.command.run(command())
    const ui = await $.ui.mount({ ...PANE, surface: 'terminal' })
    await ui.resize({ columns: 60, rows: 24, in: 'tree' })
    // Rows: the header, a gap, then the graph from its first row.
    await ui.pointer({ type: 'down', x: 20, y: 2, button: 'left', in: 'tree' })
    expect(await shown(ui)).toContain('› ●  Start')
  })

  test('enter shows where the pick came from; q closes it', async ($, on) => {
    const seen = engine(on)
    await $.session.start(start)
    await $.command.run(command())
    const ui = await $.ui.mount({ ...PANE, surface: 'terminal' })
    await ui.resize({ columns: 60, rows: 24, in: 'tree' })

    await ui.key({ key: 'return', in: 'tree' })
    let text = await shown(ui)
    expect(seen.runs.at(-1)).toEqual(['decision-tree', 'source', 's1', 'n2', '--json'])
    expect(text).toContain('Your message #1:')
    expect(text).toContain('Search is slow. Add a cache.')
    expect(text).toContain('Code: as it was then (abc123def456)')
    expect(text).toContain('q closes · b branches from here')

    await ui.key({ key: 'q', in: 'tree' })
    text = await shown(ui)
    expect(text).not.toContain('Your message #1:')
    expect(text).toContain('› ●  Fix: Cache  ◀')
  })

  test('b asks first, y makes the branch, and the pane says what was made', async ($, on) => {
    const seen = engine(on)
    const clock = mock.clock(on)
    await $.session.start(start)
    await $.command.run(command())
    const ui = await $.ui.mount({ ...PANE, surface: 'terminal' })
    await ui.resize({ columns: 60, rows: 24, in: 'tree' })

    await ui.key({ key: 'b', in: 'tree' })
    let text = await shown(ui)
    expect(seen.runs.at(-1)).toEqual(['decision-tree', 'branch', 's1', 'n2', '--plan', '--json'])
    expect(text).toContain('Create a branch?')
    expect(text).toContain('Name:         fix-cache')

    await ui.key({ key: 'y', in: 'tree' })
    expect(await shown(ui)).toContain('Making the branch…')
    await clock.advance(250)
    text = await shown(ui)
    expect(seen.runs.some(r => r.join(' ') === 'decision-tree branch s1 n2 --yes --json')).toBe(true)
    expect(text).toContain('Created branch fix-cache')
    expect(text).toContain('tmux: a new window named fix-cache.')

    await ui.key({ key: 'x', in: 'tree' })
    expect(await shown(ui)).not.toContain('Created branch')
  })

  test('n cancels a branch, and nothing is made', async ($, on) => {
    const seen = engine(on)
    await $.session.start(start)
    await $.command.run(command())
    const ui = await $.ui.mount({ ...PANE, surface: 'terminal' })
    await ui.resize({ columns: 60, rows: 24, in: 'tree' })
    await ui.key({ key: 'b', in: 'tree' })
    await ui.key({ key: 'n', in: 'tree' })
    expect(await shown(ui)).not.toContain('Create a branch?')
    expect(seen.runs.some(r => r.includes('--yes'))).toBe(false)
  })

  test('a change to the tree file shows within a tick', async ($, on) => {
    const seen = engine(on)
    const clock = mock.clock(on)
    await $.session.start(start)
    await $.command.run(command())
    const ui = await $.ui.mount({ ...PANE, surface: 'terminal' })
    await ui.resize({ columns: 60, rows: 24, in: 'tree' })

    const tree = seen.data.tree as Tree
    seen.data = {
      ...seen.data,
      tree: {
        ...tree,
        here: 'n8',
        nodes: [...tree.nodes, { id: 'n8', decision: 'd4', label: 'Redis', state: 'picked', reason: 'shared cache', by: 'claude', at }],
        decisions: [...tree.decisions, { id: 'd4', topic: 'Cache store', parent: 'n2', options: ['n8'], at }],
      },
    }
    seen.mtime = 2
    await clock.advance(250)
    const text = await shown(ui)
    expect(text).toContain('› ●  Cache store: Redis  ◀')
  })

  test('a session with no decisions says so', async ($, on) => {
    engine(on, null)
    await $.session.start(start)
    await $.command.run(command())
    const ui = await $.ui.mount({ ...PANE, surface: 'terminal' })
    await ui.resize({ columns: 60, rows: 24, in: 'tree' })
    expect(await shown(ui)).toContain('No decisions yet in this session.')
  })

  test('/decision-tree <id> shows another session; f comes back', async ($, on) => {
    const seen = engine(on)
    await $.session.start(start)
    await $.command.run(command('2485'))
    expect(seen.runs.at(-1)).toEqual(['decision-tree', 'data', '--session', '2485'])
    const ui = await $.ui.mount({ ...PANE, surface: 'terminal' })
    await ui.resize({ columns: 60, rows: 24, in: 'tree' })
    expect(await shown(ui)).toContain('Decision · session s1')
    await ui.key({ key: 'f', in: 'tree' })
    expect(seen.runs.at(-1)).toEqual(['decision-tree', 'data', '--session', 's1'])
    expect(await shown(ui)).not.toContain('Decision · session')
  })
})
