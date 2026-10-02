import type { On } from 'claude-code'
import { describe, expect, test } from 'claude-code/testing'

const RECORD = 'mcp__decision-tree__record_decision'
const SHOW = 'mcp__decision-tree__show_decision_tree'

// What the fake Go program answers to `describe`.
const SPEC = {
  instructions: 'Log a decision only once it is made.',
  tools: [
    {
      name: 'record_decision',
      description: 'Log a decision once it is made, or change one.',
      inputSchema: { type: 'object', properties: { picked: { type: 'string' } }, required: ['picked'] },
    },
    { name: 'show_decision_tree', description: 'Show the tree.', inputSchema: { type: 'object' } },
  ],
}

const COMPOSE = {
  model: 'claude-opus-5-5',
  promptModel: 'claude-opus-5-5',
  surfaces: ['terminal'] as const,
  tools: [],
  outputStyle: null,
  traits: [],
}

type Run = { argv: readonly string[]; stdin?: string }
type Reply = { exitCode: number; stdout: string; stderr: string }

// Stands in for the engine: a fake Go program that notes each run, and a
// session in /w/todo. `describe` answers SPEC; every other command, `reply`.
// 'missing' is a program that is not installed.
const engine = (on: On, reply: Reply | 'missing' = { exitCode: 0, stdout: 'Saved as d1.\n', stderr: '' }) => {
  const seen = { runs: [] as Run[], tools: [] as string[], toasts: [] as string[] }
  on('process.run', (_$, e) => {
    seen.runs.push({ argv: e.argv, stdin: e.init?.stdin })
    if (reply === 'missing') {
      throw new Error(`no such file: ${e.argv[0]}`)
    }
    const answer = e.argv[1] === 'describe' ? { exitCode: 0, stdout: JSON.stringify(SPEC), stderr: '' } : reply
    return { value: { ...answer, isStdoutTruncated: false, isStderrTruncated: false } }
  })
  on('session.id', () => ({ value: 'sess-1' }))
  on('session.cwd', () => ({ value: '/w/todo' }))
  on('session.start', (_$, e) => ({ cwd: e.cwd }))
  on('tool.register', (_$, e) => {
    seen.tools.push(e.name)
    return { value: { tool: `mcp__decision-tree__${e.name}` } }
  })
  on('ui.toast', (_$, e) => {
    seen.toasts.push(e.text)
    return { value: undefined }
  })
  on('prompt.compose', () => ({ sections: [{ id: 'intro', text: 'You are Claude Code.', scope: 'shared' }] }))
  return seen
}

const start = { cwd: '/w/todo', surface: 'terminal', isInteractive: true } as const

describe('session start', () => {
  test('registers both tools and adds the rules to the system prompt', async ($, on) => {
    const seen = engine(on)
    const before = await $.prompt.compose(COMPOSE)
    expect(before.sections.map(s => s.id)).toEqual(['intro'])

    await $.session.start(start)
    expect(seen.runs[0]?.argv).toEqual(['decision-tree', 'describe'])
    expect(seen.tools).toEqual(['record_decision', 'show_decision_tree'])
    const after = await $.prompt.compose(COMPOSE)
    expect(after.sections.at(-1)).toEqual({ id: 'decision-tree:rules', text: SPEC.instructions, scope: 'session' })
  })

  test('says so, and adds nothing, when the Go program is missing', async ($, on) => {
    const seen = engine(on, 'missing')
    await $.session.start(start)
    expect(seen.tools).toEqual([])
    expect(seen.toasts[0]).toContain('decision-tree is off')
    expect((await $.prompt.compose(COMPOSE)).sections.map(s => s.id)).toEqual(['intro'])
  })
})

describe('tools', () => {
  test('keeps both tools out from behind ToolSearch', async ($, on) => {
    on('tool.describe', (_$, e) => ({ description: e.description, isDeferred: true }))
    for (const tool of [RECORD, SHOW]) {
      const described = await $.tool.describe({
        tool,
        description: 'd',
        isDeferred: true,
        provider: { plugin: 'decision-tree', tier: 'user' },
      })
      expect(described.isDeferred).toBe(false)
    }
  })

  test('record_decision hands the call to the Go program, with the session', async ($, on) => {
    const seen = engine(on)
    const call = { topic: 'Database', options: ['SQLite', 'PostgreSQL'], picked: 'PostgreSQL', reason: 'many writers', by: 'user' }
    const answered = await $.tool.call({ tool: RECORD, tool_use_id: 'toolu_1', ...call })

    expect(answered.result).toBe('Saved as d1.')
    const run = seen.runs.at(-1)
    expect(run?.argv).toEqual(['decision-tree', 'record', '--session', 'sess-1', '--cwd', '/w/todo', '--tool-use-id', 'toolu_1'])
    expect(JSON.parse(run?.stdin ?? '')).toEqual(call)
  })

  test('a refused call comes back as an error Claude can read', async ($, on) => {
    engine(on, { exitCode: 1, stdout: '', stderr: 'decision-tree: picked is needed: only log a decision once one option has won.\n' })
    const answered = await $.tool.call({ tool: RECORD, tool_use_id: 'toolu_2', topic: 'Cache', picked: '', reason: '-', by: 'user' })

    expect(answered.isError).toBe(true)
    expect(answered.text).toBe('picked is needed: only log a decision once one option has won.')
  })

  test('show_decision_tree prints the tree for this session', async ($, on) => {
    const seen = engine(on, { exitCode: 0, stdout: '● n0 start\n  d1: Database\n', stderr: '' })
    const answered = await $.tool.call({ tool: SHOW, tool_use_id: 'toolu_3' })

    expect(answered.result).toBe('● n0 start\n  d1: Database')
    expect(seen.runs.at(-1)?.argv).toEqual(['decision-tree', 'show', '--session', 'sess-1', '--cwd', '/w/todo'])
  })
})
