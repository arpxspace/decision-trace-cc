// What a branch needs from Claude Code's own files: a session's chat file,
// where to cut it, and which tmux session a Claude runs in. None of these
// formats are documented; they were read from Claude Code 2.1.28x.
import type { SessionMessage } from 'claude-code'
import type { IO, Places } from './files'
import { join } from './files'

/** Where a branch starts: right after Claude logged a decision. */
export type Cut = {
  /** The chat row to resume at: the tool's answer to the record_decision call. */
  uuid: string
  /** Which of the user's typed messages it came after (1 = the first), and that message. */
  promptNumber: number
  prompt: string
  /** What Claude said last before logging it. */
  before: string
}

/** A session's chat file: <claude>/projects/<folder>/<id>.jsonl, or null. */
export async function chatPath(io: IO, p: Places, session: string): Promise<string | null> {
  const projects = join(p.claude, 'projects')
  let folders
  try {
    folders = await io.list(projects)
  } catch {
    return null
  }
  for (const f of folders) {
    if (f.kind !== 'dir') {
      continue
    }
    const path = join(projects, f.name, `${session}.jsonl`)
    if (await io.stat(path)) {
      return path
    }
  }
  return null
}

/** Claude's memory folder for the project a session runs in: next to its chat. */
export function memoryOf(chat: string): string {
  const i = Math.max(chat.lastIndexOf('/'), chat.lastIndexOf('\\'))
  return join(chat.slice(0, i), 'memory')
}

// ---- the user's message before a decision ----

/** Text the user typed, not a tool result or a slash command (those are wrapped in tags). */
function typed(m: SessionMessage): string {
  const text = m.text.trim()
  if (m.role !== 'user' || (m.toolResults?.length ?? 0) > 0 || text === '' || text.startsWith('<')) {
    return ''
  }
  return text
}

/**
 * The user's message a decision came after, which one it was, and what
 * Claude said last before it: read from the chat as Claude Code holds it
 * while the record_decision call runs. Claude's text counts from this turn,
 * or from the turn before when this one has none.
 */
export function promptInfo(messages: readonly SessionMessage[]): Omit<Cut, 'uuid'> {
  let promptNumber = 0
  let prompt = ''
  let before = ''
  let seen = 0
  for (let i = messages.length - 1; i >= 0; i--) {
    const m = messages[i] as SessionMessage
    const t = typed(m)
    if (t !== '') {
      seen++
      prompt ||= t
    } else if (m.role === 'assistant' && m.text.trim() !== '' && before === '' && seen <= 1) {
      before = m.text.trim()
    }
  }
  promptNumber = messages.filter(m => typed(m) !== '').length
  return { promptNumber, prompt, before }
}

/** The tool-use ids a chat row answers: the ids of its tool_result blocks. */
export function answers(content: readonly unknown[]): string[] {
  const ids: string[] = []
  for (const b of content) {
    const block = b as { type?: unknown; tool_use_id?: unknown }
    if (block.type === 'tool_result' && typeof block.tool_use_id === 'string') {
      ids.push(block.tool_use_id)
    }
  }
  return ids
}

// ---- the cut, read back from a chat file ----

// For decisions logged before the mod saved the cut itself. One row of a
// chat file, only the parts needed here.
type Row = {
  type?: string
  uuid?: string
  parentUuid?: string | null
  isMeta?: boolean
  isSidechain?: boolean
  isCompactSummary?: boolean
  message?: { content?: unknown }
}

type Block = { type?: string; text?: string; id?: string; tool_use_id?: string }

/** Message content is either a string (a typed message) or a list of blocks. */
function blocks(content: unknown): Block[] {
  if (typeof content === 'string') {
    return [{ type: 'text', text: content }]
  }
  return Array.isArray(content) ? (content as Block[]) : []
}

/** Finds the cut for the record_decision call toolUseId in a chat file's text. */
export function findCut(text: string, toolUseId: string): Cut {
  type Step = { parent: string; prompt: string; said: string }
  const steps = new Map<string, Step>()
  let call = ''
  let callSidechain = false
  let result = ''
  for (const line of text.split('\n')) {
    let e: Row
    try {
      e = JSON.parse(line) as Row
    } catch {
      continue
    }
    if (!e.uuid) {
      continue
    }
    const s: Step = { parent: e.parentUuid ?? '', prompt: '', said: '' }
    const bs = blocks(e.message?.content)
    if (e.type === 'user') {
      if (bs.some(b => b.type === 'tool_result' && b.tool_use_id === toolUseId)) {
        result = e.uuid
      }
      if (!e.isMeta && !e.isCompactSummary && !bs.some(b => b.type === 'tool_result')) {
        s.prompt = bs.map(b => (b.type === 'text' ? (b.text ?? '').trim() : '')).find(t => t !== '' && !t.startsWith('<')) ?? ''
      }
    } else if (e.type === 'assistant') {
      for (const b of bs) {
        if (b.type === 'tool_use' && b.id === toolUseId) {
          call = e.uuid
          callSidechain = !!e.isSidechain
        } else if (b.type === 'text') {
          s.said = (b.text ?? '').trim()
        }
      }
    }
    steps.set(e.uuid, s)
  }
  if (call === '' || callSidechain) {
    throw new Error("the decision's tool call is not in this chat (it may have come from a sub-agent)")
  }
  if (result === '') {
    throw new Error("the decision's tool call has no result in the chat yet")
  }
  const cut: Cut = { uuid: result, promptNumber: 0, prompt: '', before: '' }
  // Walk back from the call to the start of the chat.
  const seen = new Set<string>()
  for (let id = steps.get(call)?.parent ?? ''; id !== '' && !seen.has(id); id = steps.get(id)?.parent ?? '') {
    seen.add(id)
    const s = steps.get(id)
    if (!s) {
      break // the chain runs into rows that are not in this file
    }
    if (s.prompt !== '') {
      cut.prompt ||= s.prompt
      cut.promptNumber++
    } else if (s.said !== '' && cut.before === '' && cut.promptNumber <= 1) {
      cut.before = s.said
    }
  }
  if (cut.prompt === '') {
    throw new Error('no message from the user comes before the decision')
  }
  return cut
}

// ---- which tmux session a Claude runs in ----

/** The tmux session of a running Claude session, from its session file, or "". */
export async function tmuxSessionOf(io: IO, p: Places, session: string): Promise<string> {
  const dir = join(p.claude, 'sessions')
  let files
  try {
    files = await io.list(dir)
  } catch {
    return ''
  }
  for (const f of files) {
    if (f.kind !== 'file' || !f.name.endsWith('.json')) {
      continue
    }
    try {
      const s = JSON.parse(await io.read(join(dir, f.name))) as { sessionId?: string; tmux?: string }
      // tmux is "session:@window.%pane".
      if (s.sessionId === session && s.tmux) {
        return s.tmux.split(':')[0] ?? ''
      }
    } catch {
      // a Claude that is starting may have only half written its file
    }
  }
  return ''
}
