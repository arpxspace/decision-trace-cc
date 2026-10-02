// The plugin as Claude Code runs it: the two tools and their rules, and the
// checkpoint each new pick gets.
import type { On, SessionMessage } from 'claude-code'
import { describe, expect, test } from 'claude-code/testing'

import { answers } from '../hooks/chat'
import { treePath } from '../hooks/files'
import { INSTRUCTIONS, TOOLS } from '../hooks/rules'
import { HOME, ok, wire, World } from './fake'

const RECORD = 'mcp__decision-trace__record_decision'
const SHOW = 'mcp__decision-trace__show_decision_tree'

const COMPOSE = {
  model: 'claude-opus-5-5',
  promptModel: 'claude-opus-5-5',
  surfaces: ['terminal'] as const,
  tools: [],
  outputStyle: null,
  traits: [],
}

const start = { cwd: '/w/crm', surface: 'terminal', isInteractive: true } as const

/** A session s1 in /w/crm, a git repo, with a chat in Claude Code's folder. */
function session(on: On, messages: SessionMessage[] = []) {
  const w = new World({ '/home/u/.claude/projects/-w-crm/s1.jsonl': '', '/w/crm/.git/index': 'DIRC' })
  w.script = argv => {
    const a = argv.join(' ')
    if (a.includes('--show-toplevel')) return ok('/w/crm\n')
    if (a.includes('--git-path index')) return ok('/w/crm/.git/index\n')
    if (a.includes('write-tree')) return ok('tree1\n')
    if (a.includes('commit-tree')) return ok('commit1\n')
    return undefined
  }
  wire(on, w)
  const seen = { tools: [] as string[] }
  on('session.id', () => ({ value: 's1' }))
  on('session.cwd', () => ({ value: '/w/crm' }))
  on('session.messages', () => ({ value: messages }))
  on('session.start', (_$, e) => ({ cwd: e.cwd }))
  on('tool.register', (_$, e) => {
    seen.tools.push(e.name)
    return { value: { tool: `mcp__decision-trace__${e.name}` } }
  })
  on('command.register', (_$, e) => ({ value: { command: e.name } }))
  on('ui.open', () => ({ value: { isPlaced: true } }))
  on('ui.toast', () => ({ value: undefined }))
  on('prompt.compose', () => ({ sections: [{ id: 'intro', text: 'You are Claude Code.', scope: 'shared' }] }))
  return { w, seen }
}

const call = { topic: 'Database', options: ['SQLite', 'PostgreSQL'], picked: 'PostgreSQL', reason: 'many writers', by: 'user' }

describe('session start', () => {
  test('registers both tools and adds the rules to the system prompt', async ($, on) => {
    const { seen } = session(on)
    await $.session.start(start)
    expect(seen.tools).toEqual(TOOLS.map(t => t.name))
    expect((await $.prompt.compose(COMPOSE)).sections.at(-1)).toEqual({ id: 'decision-trace:rules', text: INSTRUCTIONS, scope: 'session' })
  })

  test('the rules and descriptions fit in 2,048 characters', () => {
    for (const text of [INSTRUCTIONS, ...TOOLS.map(t => t.description)]) {
      expect([...text].length).toBeLessThanOrEqual(2048)
    }
  })

  test('keeps both tools out from behind ToolSearch', async ($, on) => {
    session(on)
    on('tool.describe', (_$, e) => ({ description: e.description, isDeferred: true }))
    for (const tool of [RECORD, SHOW]) {
      const described = await $.tool.describe({ tool, description: 'd', isDeferred: true, provider: { plugin: 'decision-trace', tier: 'user' } })
      expect(described.isDeferred).toBe(false)
    }
  })
})

describe('record_decision', () => {
  test('saves the decision, with a checkpoint and where it came from', async ($, on) => {
    const messages: SessionMessage[] = [
      { role: 'user', text: 'Use PostgreSQL rather than SQLite.', toolUses: [] },
      { role: 'assistant', text: 'Logging that.', toolUses: [] },
    ]
    const { w } = session(on, messages)
    await $.session.start(start)
    const answered = await $.tool.call({ tool: RECORD, tool_use_id: 'toolu_1', ...call } as never)
    expect(answered.result).toBe('Saved as d1. You are here: PostgreSQL (n2). Still open: none.')

    const t = w.json(treePath(HOME, 's1'))
    expect(t.folder).toBe('/w/crm')
    // The state folder was made private before anything was written to it.
    expect(w.runs[1]?.argv).toEqual(['chmod', '700', HOME.state])
    expect(t.nodes[2].checkpoint).toMatchObject({
      tool_use_id: 'toolu_1',
      repo: '/w/crm',
      commit: 'commit1',
      prompt: 'Use PostgreSQL rather than SQLite.',
      prompt_number: 1,
      before: 'Logging that.',
      tree: '/home/u/.local/state/decision-trace/checkpoints/s1/n2/tree.json',
    })
    // The chat row that answers the call comes later, through
    // session.append, which a test cannot raise; answers() is tested below.
  })

  test('a saved chat row answers the calls of its tool_result blocks', () => {
    expect(answers([{ type: 'text', text: 'hi' }, { type: 'tool_result', tool_use_id: 'toolu_1' }, { type: 'tool_result' }])).toEqual(['toolu_1'])
  })

  test('a call without a pick is refused, and nothing is saved', async ($, on) => {
    const { w } = session(on)
    const answered = await $.tool.call({ tool: RECORD, tool_use_id: 'toolu_2', topic: 'Cache', options: ['Redis'], picked: ' ', reason: '-', by: 'user' } as never)
    expect(answered.isError).toBe(true)
    expect(answered.text).toContain('picked is needed')
    expect(w.text(treePath(HOME, 's1'))).toBeUndefined()
  })

  test('show_decision_tree prints the tree with ids', async ($, on) => {
    session(on)
    expect((await $.tool.call({ tool: SHOW, tool_use_id: 't0' } as never)).result).toBe('The tree is empty. No decisions yet.')
    await $.tool.call({ tool: RECORD, tool_use_id: 'toolu_1', ...call } as never)
    expect((await $.tool.call({ tool: SHOW, tool_use_id: 't1' } as never)).result).toContain('d1: Database\n    × n1 SQLite\n    ● n2 PostgreSQL ◀')
  })
})
