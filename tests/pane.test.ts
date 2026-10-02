// The pane: drawing the tree, the keys, the screens over the graph, and a
// branch made from it.
import type { On } from 'claude-code'
import { describe, expect, mock, test } from 'claude-code/testing'

import type { Tree } from '../types'
import { treePath } from '../hooks/files'
import { newTree, record } from '../hooks/tree'
import { HOME, ok, wire, World } from './fake'

const at = '2026-09-29T15:00:00.000Z'

/** crm: the index was set aside for the cache, and a question is still open. The cache (n2) has a full checkpoint. */
function crm(label = 'Cache'): Tree {
  let t = newTree('s1', at)
  t.folder = '/w/crm'
  for (const c of [
    { topic: 'Fix', options: ['Rust rewrite', label, 'Index'] },
    { decision_id: 'd1', picked: 'Index', reason: 'fixes the query', by: 'both' },
    { topic: 'Index on', options: ['email', 'company + date'], picked: 'company + date', reason: 'matches the search', by: 'claude' },
    { decision_id: 'd1', picked: label, reason: 'the index did not help', by: 'user', drop_later: true },
    { topic: 'Cache time', options: ['5 minutes', '1 hour'] },
  ]) {
    t = record(t, { at, ...c }).tree
  }
  t.nodes[2]!.checkpoint = {
    tool_use_id: 'toolu_2', cut: 'row-7', prompt: 'Search is slow. Add a cache.', prompt_number: 1,
    before: 'An index or a cache would both help.', repo: '/w/crm', commit: 'abc123def4567890', at,
  }
  return t
}

const PANE = {
  plugin: 'decision-trace',
  component: 'Pane',
  requestId: 'decision-trace',
  props: { title: 'Decision', isFocused: false, bodyColumns: 60, placement: 'dock', scroll: { offset: 0, bodyRows: 24 }, view: {} },
} as const

const start = { cwd: '/w/crm', surface: 'terminal', isInteractive: true } as const

const command = (args = '') => ({ command: 'decision-trace', args, origin: { kind: 'composer' as const }, presentation: { isFullscreen: true, columns: 160 } })

/** Session s1 with the crm tree, a git repo whose git answers, and a claude that forks. */
function session(on: On, tree: Tree | null = crm()): World {
  const w = new World({ '/home/u/.claude/sessions/41.json': '{"sessionId":"s1","tmux":"work:@1.%5"}' })
  if (tree) {
    w.put(treePath(HOME, 's1'), JSON.stringify(tree))
  }
  w.script = argv => {
    const a = argv.join(' ')
    if (a.includes('rev-parse --verify --quiet abc123def4567890^')) return ok('head1\n')
    if (argv[0] === 'claude') {
      w.put(`/home/u/.claude/projects/-branch/${argv[argv.indexOf('--session-id') + 1]}.jsonl`, '')
      return ok('Branch ready.')
    }
    if (argv[0] === 'tmux' && argv[1] === 'new-window') return ok('%9\n')
    return undefined
  }
  wire(on, w)
  on('session.id', () => ({ value: 's1' }))
  on('session.cwd', () => ({ value: '/w/crm' }))
  on('session.start', (_$, e) => ({ cwd: e.cwd }))
  on('tool.register', (_$, e) => ({ value: { tool: `mcp__decision-trace__${e.name}` } }))
  on('command.register', (_$, e) => ({ value: { command: e.name } }))
  on('ui.open', () => ({ value: { isPlaced: true } }))
  on('ui.toast', () => ({ value: undefined }))
  return w
}

async function shown(ui: { findAll: (q: { in: string; type: string }) => Promise<{ text: string }[]> }): Promise<string> {
  return (await ui.findAll({ in: 'tree', type: 'Text' })).map(l => l.text).join('\n')
}

describe('pane', () => {
  for (const surface of ['terminal', 'desktop'] as const) {
    test(`draws the tree with the cursor on "you are here" (${surface})`, async ($, on) => {
      session(on)
      await $.session.start(start)
      await $.command.run(command())
      const ui = await $.ui.mount({ ...PANE, surface })
      await ui.resize({ columns: 60, rows: 24, in: 'tree' })
      const text = await shown(ui)
      expect(text).toMatch(/^ Decision *$/m)
      expect(text).toContain('●  Start')
      expect(text).toContain('│ ↺  Fix: Index ▸ 2 more')
      expect(text).toContain('› ●  Fix: Cache  ◀')
      expect(text).toContain('Why: the index did not help')
      expect(text).toContain('click for keys')
    })
  }

  test('the pane is black, edge to edge', async ($, on) => {
    session(on)
    await $.session.start(start)
    await $.command.run(command())
    const ui = await $.ui.mount({ ...PANE, surface: 'terminal' })
    await ui.resize({ columns: 60, rows: 24, in: 'tree' })
    const root = (await ui.drawn({ in: 'tree' })) as { props: Record<string, unknown> }
    expect(root.props).toMatchObject({ backgroundColor: '#000000', width: 60, height: 24 })
    const texts = await ui.findAll({ in: 'tree', type: 'Text' })
    expect(texts.every(t => t.props.backgroundColor === '#000000')).toBe(true)
  })

  test('a long row wraps, with the tree line carried on beside it', async ($, on) => {
    session(on, crm('Cache the search results in memory for five minutes'))
    await $.session.start(start)
    await $.command.run(command())
    const ui = await $.ui.mount({ ...PANE, surface: 'terminal' })
    await ui.resize({ columns: 30, rows: 24, in: 'tree' })
    const text = await shown(ui)
    expect(text).toContain('› ●  Fix: Cache the search')
    expect(text).toContain('  │  results in memory for')
    expect(text).toContain('  │  five minutes  ◀')
    expect(text).not.toContain('…')
    // A click on a wrapped line picks its row.
    await ui.key({ key: 'g', in: 'tree' })
    await ui.pointer({ type: 'down', x: 10, y: 8, button: 'left', in: 'tree' })
    expect(await shown(ui)).toContain('› ●  Fix: Cache the search')
  })

  test('keys move the cursor and fold', async ($, on) => {
    session(on)
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
    expect(await shown(ui)).toContain('│ ●  Index on: company + date')
    await ui.key({ key: 'g', in: 'tree' })
    expect(await shown(ui)).toContain('› ●  Start')
    await ui.key({ key: '.', in: 'tree' })
    expect(await shown(ui)).toContain('› ●  Fix: Cache  ◀')
  })

  test('enter shows where the pick came from; q closes it', async ($, on) => {
    session(on)
    await $.session.start(start)
    await $.command.run(command())
    const ui = await $.ui.mount({ ...PANE, surface: 'terminal' })
    await ui.resize({ columns: 60, rows: 24, in: 'tree' })
    await ui.key({ key: 'return', in: 'tree' })
    let text = await shown(ui)
    expect(text).toContain('Your message #1:')
    expect(text).toContain('Search is slow. Add a cache.')
    expect(text).toContain('Claude said just before:')
    expect(text).toContain('Code: as it was then (abc123def456)')
    expect(text).toContain('q closes · b branches from here')
    await ui.key({ key: 'q', in: 'tree' })
    text = await shown(ui)
    expect(text).not.toContain('Your message #1:')
    expect(text).toContain('› ●  Fix: Cache  ◀')
  })

  test('b asks first, y makes the branch, and the pane says what was made', async ($, on) => {
    const w = session(on)
    const clock = mock.clock(on)
    await $.session.start(start)
    await $.command.run(command())
    const ui = await $.ui.mount({ ...PANE, surface: 'terminal' })
    await ui.resize({ columns: 60, rows: 24, in: 'tree' })
    await ui.key({ key: 'b', in: 'tree' })
    let text = await shown(ui)
    expect(text).toContain('Create a branch?')
    expect(text).toContain('Name:         fix-cache')
    expect(w.runs).toHaveLength(0) // asking changes nothing
    await ui.key({ key: 'y', in: 'tree' })
    expect(await shown(ui)).toContain('Making the branch…')
    await clock.advance(250)
    text = await shown(ui)
    expect(w.ran('claude', '-p')).toHaveLength(1)
    expect(text).toContain('Created branch fix-cache')
    expect(text).toContain('tmux: a new window named fix-cache.')
    await ui.key({ key: 'x', in: 'tree' })
    expect(await shown(ui)).not.toContain('Created branch')
  })

  test('n cancels a branch, and nothing is made', async ($, on) => {
    const w = session(on)
    await $.session.start(start)
    await $.command.run(command())
    const ui = await $.ui.mount({ ...PANE, surface: 'terminal' })
    await ui.resize({ columns: 60, rows: 24, in: 'tree' })
    await ui.key({ key: 'b', in: 'tree' })
    await ui.key({ key: 'n', in: 'tree' })
    expect(await shown(ui)).not.toContain('Create a branch?')
    expect(w.runs).toHaveLength(0)
  })

  test('a change to the tree file shows within a tick', async ($, on) => {
    const w = session(on)
    const clock = mock.clock(on)
    await $.session.start(start)
    await $.command.run(command())
    const ui = await $.ui.mount({ ...PANE, surface: 'terminal' })
    await ui.resize({ columns: 60, rows: 24, in: 'tree' })
    const t = record(crm(), { topic: 'Cache store', options: ['Redis'], picked: 'Redis', reason: 'shared cache', by: 'claude', at }).tree
    w.put(treePath(HOME, 's1'), JSON.stringify(t))
    await clock.advance(250)
    expect(await shown(ui)).toContain('› ●  Cache store: Redis  ◀')
  })

  test('a session with no decisions says so', async ($, on) => {
    session(on, null)
    await $.session.start(start)
    await $.command.run(command())
    const ui = await $.ui.mount({ ...PANE, surface: 'terminal' })
    await ui.resize({ columns: 60, rows: 24, in: 'tree' })
    expect(await shown(ui)).toContain('No decisions yet in this session.')
  })

  test('/decision-trace <id> shows another session; f comes back', async ($, on) => {
    const w = session(on)
    const other = crm()
    other.session_id = '24859e5a-0000'
    w.put(treePath(HOME, '24859e5a-0000'), JSON.stringify(other))
    await $.session.start(start)
    await $.command.run(command('2485'))
    const ui = await $.ui.mount({ ...PANE, surface: 'terminal' })
    await ui.resize({ columns: 60, rows: 24, in: 'tree' })
    expect(await shown(ui)).toContain('Decision · session 24859e5a')
    await ui.key({ key: 'f', in: 'tree' })
    expect(await shown(ui)).not.toContain('Decision · session')
  })
})
