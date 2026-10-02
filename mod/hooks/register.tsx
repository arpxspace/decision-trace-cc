// The decision-tree mod: Claude Code's side of decision-tree. It gives
// Claude record_decision and show_decision_tree and the rules for them
// (PRD 18), and draws the tree in a pane inside Claude Code (PRD 19). Every
// call, and everything the pane reads from disk, goes to the Go program,
// which keeps the tree.
import { atom, read, update } from 'claude-code'
import type { EngineInterface, ProcessRunResult, Register, ToolCallResult } from 'claude-code'

import type { Checkpoint, Data, Overlay, ViewProps } from '../types'
import { layout, node, plain, ROOT, statement } from './graph'
import { pickedBy, short, when } from './text'

// The names Claude sees are mcp__<plugin>__<tool>, the same as the MCP
// server's, so the allow rules in settings.json still match.
const RECORD = 'mcp__decision-tree__record_decision'
const SHOW = 'mcp__decision-tree__show_decision_tree'

const PANE = 'decision-tree'
const CHECK_EVERY = 250 // ms between looks at the tree file's time
const RELOAD_EVERY = 2000 // ms between full reloads, which find new branches

// What `decision-tree describe` prints.
type Spec = {
  instructions: string
  tools: { name: string; description: string; inputSchema: Record<string, unknown> }[]
}

type BranchAsk = { session: string; node: string; focus: boolean }

// What the pane's view posts.
type Message =
  | { type: 'source'; node: string }
  | ({ type: 'branch' } & BranchAsk)
  | { type: 'create' }
  | { type: 'close' }
  | { type: 'open'; session: string; node?: string }
  | { type: 'own' }
  | { type: 'quit' }
  | { type: 'notice'; title: string; body: string[] }

// What the pane draws from. The host keeps these across reloads.
const shownAtom = atom({ plugin: 'decision-tree', key: 'shown' } as const, null)
const dataAtom = atom({ plugin: 'decision-tree', key: 'data' } as const, null)
const errorAtom = atom({ plugin: 'decision-tree', key: 'error' } as const, '')
const overlayAtom = atom({ plugin: 'decision-tree', key: 'overlay' } as const, null)

// What the hooks share. The module's own, so a reload starts it over.
const mod = {
  binary: 'decision-tree',
  // Empty until the tools are registered, so Claude never reads rules for
  // tools it does not have.
  rules: '',
  isOpen: false,
  // A branch the person said yes to. Making one takes seconds, so the
  // session's timer makes it, past the hook that heard the yes.
  toCreate: null as BranchAsk | null,
  // What was loaded last, so the timer only runs the Go program when the
  // tree file changed, or now and then to find new branches.
  loaded: { session: '', path: '', mtime: -1, text: '', at: 0 },
  isLoading: false,
}

const notice = (title: string, body: string[]): Overlay => ({ kind: 'notice', title, body, hint: 'any key closes this' })

// The Go program's error, without its "decision-tree: " start. A tool
// call gets all of it; the pane shows its first line.
const errorText = (ran: ProcessRunResult) => ran.stderr.trim().replace(/^decision-tree: /, '')
const firstLine = (text: string) => text.split('\n')[0] ?? ''

const message = (err: unknown) => (err instanceof Error ? err.message : String(err))

// The Go program is told the session; the MCP server had to look it up.
async function sessionFlags($: EngineInterface): Promise<string[]> {
  return ['--session', await $.session.id(), '--cwd', await $.session.cwd()]
}

function answer(ran: ProcessRunResult): ToolCallResult {
  if (ran.exitCode === 0) {
    return { result: ran.stdout.trim() }
  }
  return failed(errorText(ran))
}

function failed(text: string): ToolCallResult {
  return { isError: true, result: text, text }
}

/** Only what the view uses of a checkpoint, so the pane's props stay small. */
function slim(data: Data): Data {
  if (!data.tree) {
    return data
  }
  const keep = (cp: Checkpoint | undefined): Checkpoint | undefined =>
    cp && { tool_use_id: cp.tool_use_id, commit: cp.commit, memory: cp.memory, missing: cp.missing }
  return { ...data, tree: { ...data.tree, nodes: data.tree.nodes.map(n => ({ ...n, checkpoint: keep(n.checkpoint) })) } }
}

// ---- loading the tree ----

async function mtime($: EngineInterface, path: string): Promise<number> {
  try {
    return (await $.fs.stat(path)).mtimeMs
  } catch {
    return 0 // no tree yet
  }
}

/** Loads the tree the pane shows, when its file changed, when it shows
 * another session, now and then for new branches, or when forced. */
async function load($: EngineInterface, force = false): Promise<void> {
  if (mod.isLoading) {
    return
  }
  mod.isLoading = true
  try {
    const shown = await read($, shownAtom)
    const session = shown?.session ?? (await $.session.id())
    const now = Date.now()
    const last = mod.loaded
    const isNew = session !== last.session
    const stamp = isNew || last.path === '' ? -1 : await mtime($, last.path)
    if (!force && !isNew && now - last.at < RELOAD_EVERY && stamp === last.mtime) {
      return
    }
    const ran = await $.process.run([mod.binary, 'data', '--session', session])
    if (ran.exitCode !== 0) {
      mod.loaded = { ...last, session, at: now }
      await update($, errorAtom, () => firstLine(errorText(ran)))
      return
    }
    const data = JSON.parse(ran.stdout) as Data
    mod.loaded = { session, path: data.path, mtime: stamp === -1 ? await mtime($, data.path) : stamp, text: ran.stdout, at: now }
    if (isNew || ran.stdout !== last.text) {
      await update($, dataAtom, () => slim(data))
    }
    if ((await read($, errorAtom)) !== '') {
      await update($, errorAtom, () => '')
    }
  } catch (err) {
    await update($, errorAtom, () => firstLine(message(err)))
  } finally {
    mod.isLoading = false
  }
}

/** Opens the pane on this session (null) or another one. */
async function open($: EngineInterface, session: string | null, focus: boolean): Promise<void> {
  await update($, shownAtom, () => (session === null ? null : { session }))
  await update($, overlayAtom, () => null)
  mod.isOpen = true
  await $.ui.open({ id: PANE, title: 'Decision', ...(focus ? { focus: true as const } : {}) })
  await load($, true)
}

// ---- what the pane's keys ask for ----

/** The source screen: who picked it and when, the user's message, what
 * Claude said just before, and what a branch from here would start with. */
async function source($: EngineInterface, id: string): Promise<Overlay | null> {
  const data = await read($, dataAtom)
  const t = data?.tree
  const n = t ? node(t, id) : undefined
  if (!data || !t || !n) {
    return null
  }
  if (n.id === ROOT) {
    return notice('Start', ['The start of the session. Nothing was decided here.'])
  }
  if (n.state !== 'picked' && n.state !== 'dropped') {
    return notice(statement(t, n), ['This option was not picked, so it has no source of its own. The pick of its decision does.'])
  }
  const body = [`${pickedBy(n.by)} · ${when(n.at)}`, '']
  const ran = await $.process.run([mod.binary, 'source', data.session, n.id, '--json'])
  const cut: { prompt_number?: number; prompt?: string; before?: string; error?: string } =
    ran.exitCode === 0 ? JSON.parse(ran.stdout) : { error: errorText(ran) }
  if (cut.error) {
    body.push(`The chat: ${cut.error}`)
  } else {
    body.push(`Your message #${cut.prompt_number}:`, `  ${cut.prompt}`)
    if (cut.before) {
      body.push('', 'Claude said just before:', `  ${cut.before}`)
    }
  }
  const cp = n.checkpoint ?? {}
  body.push('', 'What a branch from here starts with:')
  if (cut.error) {
    body.push('  Chat: not available, so no branch can be made from here')
  } else if (cp.tool_use_id) {
    body.push('  Chat: up to right after this decision')
  } else {
    body.push('  Chat: not saved (logged before checkpoints existed)')
  }
  body.push(cp.commit ? `  Code: as it was then (${cp.commit.slice(0, 12)})` : '  Code: not available')
  body.push(cp.memory ? '  Memory: as it was then' : '  Memory: none saved')
  if (cp.missing) {
    body.push(`  Missing: ${cp.missing}`)
  }
  return { kind: 'source', title: statement(t, n), body, hint: 'q closes · b branches from here', node: n.id }
}

/** Works out a branch, to ask before making it. */
async function plan($: EngineInterface, ask: BranchAsk): Promise<Overlay> {
  if (ask.node === '') {
    return notice('Pick a decision', ['Put the cursor on a pick to branch from it.'])
  }
  const ran = await $.process.run([mod.binary, 'branch', ask.session, ask.node, '--plan', '--json'])
  if (ran.exitCode !== 0) {
    return notice("Can't branch from here", [errorText(ran)])
  }
  const p = JSON.parse(ran.stdout) as { summary: string }
  return {
    kind: 'confirm',
    title: 'Create a branch?',
    body: p.summary.split('\n'),
    hint: 'y creates it · n cancels',
    plan: { session: ask.session, node: ask.node, focus: ask.focus },
  }
}

/** Makes the branch, then says what was made, or why not. */
async function create($: EngineInterface, ask: BranchAsk): Promise<void> {
  let done: Overlay
  try {
    const argv = [mod.binary, 'branch', ask.session, ask.node, '--yes', '--json', ...(ask.focus ? ['--focus'] : [])]
    const ran = await $.process.run(argv, { timeoutMs: 180_000 })
    let res: { name?: string; session?: string; dir?: string; pane?: string; command?: string; copied?: string[] | null; error?: string }
    try {
      res = JSON.parse(ran.stdout)
    } catch {
      res = { error: errorText(ran) || 'the Go program printed nothing' }
    }
    if (ran.exitCode !== 0 || res.error) {
      const body = [res.error || errorText(ran)]
      if (res.command) {
        body.push('', 'To start it yourself:', `  ${res.command}`)
      }
      done = notice('The branch was not made', body)
    } else {
      const body = [`Session ${short(res.session ?? '')}`, `Folder: ${res.dir}`]
      if (res.pane) {
        body.push(`tmux: a new window named ${res.name}.`, 'The first start in a new folder may ask you to trust it.')
      } else if (res.command) {
        body.push('tmux is not running. To start it:', `  ${res.command}`)
      }
      if (res.copied?.length) {
        body.push(`Copied from .worktreeinclude: ${res.copied.join(', ')}`)
      }
      done = notice(`Created branch ${res.name}`, body)
    }
  } catch (err) {
    done = notice('The branch was not made', [message(err)])
  }
  await update($, overlayAtom, () => done)
  await load($, true)
}

/** Does what the pane's view asked for. */
async function handle($: EngineInterface, m: Message): Promise<void> {
  switch (m.type) {
    case 'source': {
      const o = await source($, m.node)
      if (o) {
        await update($, overlayAtom, () => o)
      }
      return
    }
    case 'branch': {
      const o = await plan($, m)
      await update($, overlayAtom, () => o)
      return
    }
    case 'create': {
      const o = await read($, overlayAtom)
      if (o?.kind !== 'confirm' || !o.plan) {
        return
      }
      mod.toCreate = o.plan
      await update($, overlayAtom, () => ({
        kind: 'working',
        title: 'Making the branch…',
        body: ['The chat fork takes a few seconds. The original is not changed.'],
        hint: '',
      }))
      return
    }
    case 'close':
      await update($, overlayAtom, () => null)
      return
    case 'open': {
      const goTo = m.node ? { goTo: { node: m.node, n: Date.now() } } : {}
      await update($, shownAtom, () => ({ session: m.session, ...goTo }))
      await update($, overlayAtom, () => null)
      await load($, true)
      return
    }
    case 'own':
      await open($, null, false)
      return
    case 'quit':
      mod.isOpen = false
      await $.ui.close({ id: PANE })
      return
    case 'notice':
      await update($, overlayAtom, () => notice(m.title, m.body))
      return
  }
}

export const register: Register = (on, options) => {
  mod.binary = typeof options.binary === 'string' && options.binary !== '' ? options.binary : 'decision-tree'
  const openAtStart = options.openAtStart !== false

  on('session.start', async ($, e, next) => {
    try {
      const ran = await $.process.run([mod.binary, 'describe'])
      if (ran.exitCode !== 0) {
        throw new Error(ran.stderr.trim())
      }
      const spec = JSON.parse(ran.stdout) as Spec
      for (const tool of spec.tools) {
        await $.tool.register(tool)
      }
      mod.rules = spec.instructions
    } catch (err) {
      $.ui.toast(`decision-tree is off: ${message(err)}`)
      return next(e)
    }

    await $.command.register({
      name: 'decision-tree',
      description: "Show this session's decision tree in a pane, or another session's: /decision-tree <session id>",
    })
    // The timer keeps the pane's tree fresh and makes branches, which take
    // longer than one hook may.
    $.clock.every(CHECK_EVERY, () => {
      const ask = mod.toCreate
      if (ask) {
        mod.toCreate = null
        void create($, ask)
      }
      if (mod.isOpen) {
        void load($)
      }
    })
    if (openAtStart) {
      // Unasked, the pane shows once the terminal is wide enough for it.
      mod.isOpen = true
      void $.ui.open({ id: PANE, title: 'Decision' })
    }
    return next(e)
  })

  // The rules go in the system prompt, where the MCP server's instructions were.
  on('prompt.compose', async ($, e, next) => {
    const composed = await next(e)
    if (mod.rules === '') {
      return composed
    }

    return {
      sections: [...composed.sections, { id: 'decision-tree:rules', text: mod.rules, scope: 'session' }],
    }
  })

  // Keep both tools in Claude's list, not behind ToolSearch, as the MCP
  // server's alwaysLoad did.
  on('tool.describe', { tool: RECORD }, async ($, e, next) => ({ ...(await next(e)), isDeferred: false }))
  on('tool.describe', { tool: SHOW }, async ($, e, next) => ({ ...(await next(e)), isDeferred: false }))

  on('tool.call', { tool: RECORD }, async ($, e) => {
    // The rest of e is the call itself, as Claude sent it.
    const { tool, tool_use_id, consent, agentId, ...call } = e
    // The tool use id marks where a branch would cut the chat (PRD 17.1).
    const marked = tool_use_id === undefined ? [] : ['--tool-use-id', tool_use_id]
    try {
      const argv = [mod.binary, 'record', ...(await sessionFlags($)), ...marked]
      const answered = answer(await $.process.run(argv, { stdin: JSON.stringify(call) }))
      if (mod.isOpen) {
        await load($, true) // the pane shows the new node at once
      }
      return answered
    } catch (err) {
      return failed(`decision-tree could not run: ${message(err)}`)
    }
  })

  on('tool.call', { tool: SHOW }, async $ => {
    try {
      return answer(await $.process.run([mod.binary, 'show', ...(await sessionFlags($))]))
    } catch (err) {
      return failed(`decision-tree could not run: ${message(err)}`)
    }
  })

  // /decision-tree opens the pane on this session; /decision-tree <id> on
  // another one (the start of its id is enough).
  on('command.run', { command: 'decision-tree' }, async ($, e) => {
    const arg = (e.args ?? '').trim()
    await open($, arg === '' ? null : arg, true)
    return { text: 'Decision tree opened. Click it to use its keys; esc gives them back.' }
  })

  on('ui.close', { id: PANE }, async ($, e, next) => {
    mod.isOpen = false
    return next(e)
  })

  on('ui.render', { component: 'Pane', requestId: PANE }, async ($, e) => {
    const data = await read($, dataAtom)
    const error = await read($, errorAtom)
    const overlay = await read($, overlayAtom)
    const shown = await read($, shownAtom)
    const columns = e.props.bodyColumns
    const rows = e.props.scroll.bodyRows
    if (e.surface === 'terminal' || e.surface === 'desktop') {
      const { Client } = $.ui.resolve(e)
      const props: ViewProps = { data, isOwn: shown === null, error, overlay, goTo: shown?.goTo ?? null, columns, rows }
      return <Client key="tree" module="./view.ts" props={props} width={columns} height={rows} />
    }
    // A surface with no Client: the graph as plain text.
    const { Box, Text } = $.ui.resolve(e)
    const text = data?.tree ? plain(layout(data.tree, undefined, data.branches)) : 'No decisions yet.'
    return (
      <Box flexDirection="column">
        <Text>{error || text}</Text>
      </Box>
    )
  })

  on('ui.message', { component: 'Pane', requestId: PANE }, async ($, e) => {
    try {
      await handle($, e.data as Message)
    } catch (err) {
      await update($, overlayAtom, () => notice('Something went wrong', [message(err)]))
    }
    return {}
  })
}
