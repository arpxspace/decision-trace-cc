// What a branch needs from the chat: where to cut it, and the user's
// message a decision came after.
import type { SessionMessage } from 'claude-code'
import { describe, expect, test } from 'claude-code/testing'

import { chatPath, findCut, memoryOf, promptInfo, tmuxSessionOf } from '../hooks/chat'
import { HOME, World } from './fake'

/** A chat file shaped like a real one: every row points at the one before it. */
class Chat {
  lines: string[] = []
  last: string | null = null
  n = 0

  add(type: string, fields: Record<string, unknown> = {}): string {
    this.n++
    const uuid = `u${String(this.n).padStart(2, '0')}`
    this.lines.push(JSON.stringify({ type, uuid, parentUuid: this.last, isSidechain: false, ...fields }))
    this.last = uuid
    return uuid
  }

  prompt = (text: string) => this.add('user', { message: { content: text } })
  text = (text: string) => this.add('assistant', { message: { content: [{ type: 'text', text }] } })
  think = () => this.add('assistant', { message: { content: [{ type: 'thinking', thinking: 'hmm' }] } })
  call = (id: string) => this.add('assistant', { message: { content: [{ type: 'tool_use', id, name: 'mcp__decision-trace__record_decision' }] } })
  result = (id: string) => this.add('user', { message: { content: [{ type: 'tool_result', tool_use_id: id, content: 'Saved as d1.' }] } })
  toString = () => this.lines.join('\n') + '\n'
}

/** "Build an API", "Use FastAPI", Yes; then later Redis. */
function example() {
  const c = new Chat()
  c.add('user', { isMeta: true, message: { content: '<system-reminder>SessionStart hook</system-reminder>' } })
  c.prompt('Build an API')
  c.add('attachment')
  c.think()
  c.text('FastAPI or Flask? I would pick FastAPI.')
  c.prompt('Yes')
  c.think()
  c.call('toolu_A')
  const cutA = c.result('toolu_A')
  c.text('Now implementing FastAPI.')
  c.prompt('Add Redis for caching')
  c.text('Logging that.')
  c.call('toolu_B')
  const cutB = c.result('toolu_B')
  c.prompt('<command-name>/effort</command-name>')
  return { c, cutA, cutB }
}

describe('the cut, read from a chat file', () => {
  test('cuts after the tool answers the call', () => {
    const { c, cutA, cutB } = example()
    // The branch keeps "Yes" and the decision, not what came after. Nothing
    // was said in that turn before the call, so "before" is the end of the
    // turn before: the recommendation the user agreed to.
    expect(findCut(c.toString(), 'toolu_A')).toEqual({
      uuid: cutA,
      promptNumber: 2,
      prompt: 'Yes',
      before: 'FastAPI or Flask? I would pick FastAPI.',
    })
    expect(findCut(c.toString(), 'toolu_B')).toEqual({ uuid: cutB, promptNumber: 3, prompt: 'Add Redis for caching', before: 'Logging that.' })
  })

  test('says why when it cannot cut', () => {
    const { c } = example()
    c.call('toolu_NoResult')
    c.add('assistant', { isSidechain: true, message: { content: [{ type: 'tool_use', id: 'toolu_Side' }] } })
    expect(() => findCut(c.toString(), 'toolu_Nope')).toThrow('not in this chat')
    expect(() => findCut(c.toString(), 'toolu_NoResult')).toThrow('no result in the chat yet')
    expect(() => findCut(c.toString(), 'toolu_Side')).toThrow('not in this chat')
  })

  test('the summary /compact writes is not a typed message', () => {
    const c = new Chat()
    c.prompt('Build an API')
    c.add('user', { isCompactSummary: true, message: { content: 'This session is being continued.' } })
    c.call('toolu_A')
    c.result('toolu_A')
    expect(findCut(c.toString(), 'toolu_A')).toMatchObject({ prompt: 'Build an API', promptNumber: 1 })
  })
})

describe('the chat as Claude Code holds it', () => {
  const m = (role: 'user' | 'assistant', text: string, extra: Partial<SessionMessage> = {}): SessionMessage => ({ role, text, toolUses: [], ...extra })

  test("the user's message a decision came after, and what Claude said before", () => {
    const messages = [
      m('user', 'Build an API'),
      m('assistant', 'FastAPI or Flask? I would pick FastAPI.'),
      m('user', '<command-name>/effort</command-name>'),
      m('user', 'Yes'),
      m('user', '', { toolResults: [{ tool_use_id: 't', text: 'ok', isError: false } as never] }),
      m('assistant', ''),
    ]
    expect(promptInfo(messages)).toEqual({ promptNumber: 2, prompt: 'Yes', before: 'FastAPI or Flask? I would pick FastAPI.' })
    expect(promptInfo([...messages.slice(0, 4), m('assistant', 'Logging that.')]).before).toBe('Logging that.')
  })
})

describe("Claude Code's files", () => {
  test('finds a chat file and its memory folder; reads which tmux session a Claude runs in', async () => {
    const w = new World({
      '/home/u/.claude/projects/-w-crm/s1.jsonl': '',
      '/home/u/.claude/projects/-w-other/s2.jsonl': '',
      '/home/u/.claude/sessions/41.json': '{"pid":41,"sessionId":"s1","tmux":"work:@1.%5"}',
      '/home/u/.claude/sessions/42.json': '{"pid":42',
    })
    expect(await chatPath(w.io, HOME, 's1')).toBe('/home/u/.claude/projects/-w-crm/s1.jsonl')
    expect(await chatPath(w.io, HOME, 's9')).toBeNull()
    expect(memoryOf('/home/u/.claude/projects/-w-crm/s1.jsonl')).toBe('/home/u/.claude/projects/-w-crm/memory')
    expect(await tmuxSessionOf(w.io, HOME, 's1')).toBe('work')
    expect(await tmuxSessionOf(w.io, HOME, 's2')).toBe('')
  })
})
