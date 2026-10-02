// decision-trace: Claude logs the project decisions of a session as a tree,
// and a pane beside the chat draws it. Every decision is a checkpoint: a
// branch from it starts a new session with the chat, the code, and Claude's
// memory as they were.
//
// This is the hooks module, the one file that talks to Claude Code through
// `$`. It gives Claude the two tools and the rules for them, saves each call,
// and draws the pane. The other files do the work through an IO it hands
// them: tree.ts (the rules for changing a tree), files.ts (trees on disk),
// checkpoint.ts, branch.ts, chat.ts, and view.ts (the pane).
import { atom, read, update } from 'claude-code'
import type { EngineInterface, Register, ToolCallResult } from 'claude-code'

import type { Data, Overlay, ViewProps } from '../types'
import { create, cutOf, prepare, summary as planSummary } from './branch'
import { answers, chatPath, memoryOf, promptInfo } from './chat'
import { saveTreeCopy, take } from './checkpoint'
import type { IO, Places } from './files'
import { branchesOf, find, keepPrivate, load as loadTree, places, short, treePath, update as updateTree } from './files'
import { layout, plain } from './graph'
import { INSTRUCTIONS, TOOLS } from './rules'
import { showText, summary } from './show'
import { pickedBy, printable, when } from './text'
import type { Call } from './tree'
import { clean, node, record, ROOT, statement, TreeError } from './tree'

// The names Claude sees are mcp__<plugin>__<tool>. The matchers are
// patterns, since Claude Code's list of known tools only has these names
// once the plugin has registered them.
const RECORD_TOOL = /^mcp__decision-trace__record_decision$/
const SHOW_TOOL = /^mcp__decision-trace__show_decision_tree$/

const PANE = 'decision-trace'
const CHECK_EVERY = 250 // ms between looks at the tree file's time
const RELOAD_EVERY = 2000 // ms between full reloads, which find new branches

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
const shownAtom = atom({ plugin: 'decision-trace', key: 'shown' } as const, null)
const dataAtom = atom({ plugin: 'decision-trace', key: 'data' } as const, null)
const errorAtom = atom({ plugin: 'decision-trace', key: 'error' } as const, '')
const overlayAtom = atom({ plugin: 'decision-trace', key: 'overlay' } as const, null)

// What the hooks share. The module's own, so a reload starts it over.
const mod = {
  places: null as Places | null,
  isOpen: false,
  // A branch the person said yes to. Making one takes seconds, so the
  // session's timer makes it, past the hook that heard the yes.
  toCreate: null as BranchAsk | null,
  // What the pane loaded last, so the timer reads the tree again only when
  // its file changed, or now and then to find new branches.
  loaded: { session: '', path: '', mtime: -1, text: '', at: 0 },
  isLoading: false,
  // Picks waiting for the chat row that answers their record_decision call:
  // that row is where a branch cuts the chat.
  cuts: new Map<string, { session: string; node: string }>(),
}

const notice = (title: string, body: string[]): Overlay => ({ kind: 'notice', title, body, hint: 'any key closes this' })

const message = (err: unknown) => (err instanceof Error ? err.message : String(err))

const firstLine = (text: string) => text.split('\n')[0] ?? ''

function failed(text: string): ToolCallResult {
  return { isError: true, result: text, text }
}

// ---- the outside world ----

/** The mod's file tools and program runner, for the other files. */
function io($: EngineInterface): IO {
  return {
    read: path => $.fs.read(path),
    write: (path, text) => $.fs.write(path, text),
    list: async path => (await $.fs.list(path)).map(e => ({ name: e.name, kind: e.kind, mtimeMs: e.mtimeMs })),
    stat: async path => {
      try {
        const s = await $.fs.stat(path)
        return { kind: s.kind, mtimeMs: s.mtimeMs }
      } catch {
        return null
      }
    },
    run: async (argv, init) => {
      const ran = await $.process.run(argv, init)
      return { exitCode: ran.exitCode, stdout: ran.stdout, stderr: ran.stderr }
    },
  }
}

/** Where things are on this computer, worked out once. */
async function where($: EngineInterface): Promise<Places> {
  mod.places ??= places({
    OS: await $.env.get('OS'),
    HOME: await $.env.get('HOME'),
    USERPROFILE: await $.env.get('USERPROFILE'),
    XDG_STATE_HOME: await $.env.get('XDG_STATE_HOME'),
    LOCALAPPDATA: await $.env.get('LOCALAPPDATA'),
    CLAUDE_CONFIG_DIR: await $.env.get('CLAUDE_CONFIG_DIR'),
    TERM_PROGRAM: await $.env.get('TERM_PROGRAM'),
    WT_SESSION: await $.env.get('WT_SESSION'),
    WEZTERM_PANE: await $.env.get('WEZTERM_PANE'),
    KITTY_WINDOW_ID: await $.env.get('KITTY_WINDOW_ID'),
    GNOME_TERMINAL_SCREEN: await $.env.get('GNOME_TERMINAL_SCREEN'),
    KONSOLE_VERSION: await $.env.get('KONSOLE_VERSION'),
  })
  return mod.places
}

// ---- the tools ----

/** Saves one record_decision call in this session's tree, with a checkpoint for a new pick. */
async function recordCall($: EngineInterface, input: Omit<Call, 'at'>, toolUseId: string, agentId: string | undefined): Promise<string> {
  if (clean(input.picked) === '') {
    // The schema asks for it; this also catches "picked": "".
    throw new TreeError('picked is needed: only log a decision once one option has won. Do not log options that are only being discussed.')
  }
  const x = io($)
  const p = await where($)
  await keepPrivate(x, p)
  const session = await $.session.id()
  const cwd = await $.session.cwd()
  const at = new Date().toISOString()
  let said = ''
  await updateTree(x, p, session, async tree => {
    tree.folder ||= cwd
    const { tree: t, result } = record(tree, { ...input, at })
    const n = node(t, result.picked || undefined)
    if (n) {
      // A new pick is a moment to go back to: save the code, the memory,
      // where it came from in the chat, and the tree.
      const chat = await chatPath(x, p, session)
      const cp = await take(x, p, { repo: cwd, memory: chat ? memoryOf(chat) : '', session, node: n.id, toolUseId, at })
      if (agentId === undefined) {
        const info = promptInfo(await $.session.messages())
        Object.assign(cp, { prompt: info.prompt, prompt_number: info.promptNumber, ...(info.before ? { before: info.before } : {}) })
        if (toolUseId !== '') {
          mod.cuts.set(toolUseId, { session, node: n.id })
        }
      }
      n.checkpoint = cp
      try {
        // The copy includes this checkpoint, so a branch of a branch can find it.
        cp.tree = await saveTreeCopy(x, p, session, n.id, t)
      } catch (err) {
        cp.missing = [cp.missing, `tree not saved: ${message(err)}`].filter(Boolean).join('; ')
      }
    }
    said = summary(t, result)
    return t
  })
  return said
}

/** Notes the chat row that answers a decision's call: where a branch from it cuts the chat. */
async function saveCut($: EngineInterface, session: string, id: string, row: string): Promise<void> {
  await updateTree(io($), await where($), session, t => {
    const n = node(t, id)
    if (n?.checkpoint) {
      n.checkpoint.cut = row
    }
    return t
  })
}

// ---- loading the tree for the pane ----

/** Loads the tree the pane shows, when its file changed, when it shows another session, now and then for new branches, or when forced. */
async function load($: EngineInterface, force = false): Promise<void> {
  if (mod.isLoading) {
    return
  }
  mod.isLoading = true
  try {
    const x = io($)
    const p = await where($)
    const shown = await read($, shownAtom)
    const session = shown?.session ?? (await $.session.id())
    const path = treePath(p, session)
    const now = Date.now()
    const last = mod.loaded
    const isNew = session !== last.session
    const stamp = (await x.stat(path))?.mtimeMs ?? 0
    if (!force && !isNew && now - last.at < RELOAD_EVERY && stamp === last.mtime) {
      return
    }
    const tree = await loadTree(x, p, session)
    const data: Data = { session, path, tree, branches: tree ? await branchesOf(x, p, session) : {} }
    const text = JSON.stringify(data)
    mod.loaded = { session, path, mtime: stamp, text, at: now }
    if (isNew || text !== last.text) {
      await update($, dataAtom, () => data)
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

/** The source screen: who picked it and when, the user's message, what Claude said just before, and what a branch from here would start with. */
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
  let chatError = ''
  try {
    const cut = await cutOf(io($), await where($), data.session, n)
    body.push(`Your message #${cut.promptNumber}:`, `  ${cut.prompt}`)
    if (cut.before) {
      body.push('', 'Claude said just before:', `  ${cut.before}`)
    }
  } catch (err) {
    chatError = message(err)
    body.push(`The chat: ${chatError}`)
  }
  const cp = n.checkpoint ?? {}
  body.push('', 'What a branch from here starts with:')
  body.push(chatError ? '  Chat: not available, so no branch can be made from here' : '  Chat: up to right after this decision')
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
  try {
    const p = await prepare(io($), await where($), ask.session, ask.node)
    return {
      kind: 'confirm',
      title: 'Create a branch?',
      body: planSummary(p).split('\n'),
      hint: 'y creates it · n cancels',
      plan: { session: ask.session, node: ask.node, focus: ask.focus },
    }
  } catch (err) {
    return notice("Can't branch from here", [message(err)])
  }
}

/** Makes the branch, then says what was made, or why not. */
async function makeBranch($: EngineInterface, ask: BranchAsk): Promise<void> {
  let done: Overlay
  try {
    const x = io($)
    const p = await where($)
    await keepPrivate(x, p)
    const planned = await prepare(x, p, ask.session, ask.node)
    const made = await create(x, p, planned, ask.focus, await $.session.id())
    const body = [`Session ${short(planned.session)}`, `Folder: ${planned.dir}`]
    if (made.pane) {
      body.push(`tmux: a new window named ${planned.name}.`, 'The first start in a new folder may ask you to trust it.')
    } else if (made.tab) {
      body.push(`It runs in a new ${made.tab}.`, 'The first start in a new folder may ask you to trust it.')
    } else if (made.windowError) {
      body.push(`tmux could not open a window: ${made.windowError}. To start the branch:`, `  ${made.command}`)
    } else {
      body.push('No tmux, and no tab could be opened in this terminal. To start the branch:', `  ${made.command}`)
    }
    if (made.copied.length > 0) {
      body.push(`Copied from .worktreeinclude: ${made.copied.join(', ')}`)
    }
    done = notice(`Created branch ${planned.name}`, body)
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
  const openAtStart = options.openAtStart !== false

  on('session.start', async ($, e, next) => {
    for (const tool of TOOLS) {
      await $.tool.register(tool)
    }
    await $.command.register({
      name: 'decision-trace',
      description: "Show this session's decision tree in a pane, or another session's: /decision-trace <session id>",
    })
    // The timer keeps the pane's tree fresh and makes branches, which take
    // longer than one hook may.
    $.clock.every(CHECK_EVERY, () => {
      const ask = mod.toCreate
      if (ask) {
        mod.toCreate = null
        void makeBranch($, ask)
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

  // The rules go in the system prompt, so Claude reads them in every session.
  on('prompt.compose', async ($, e, next) => {
    const composed = await next(e)
    return { sections: [...composed.sections, { id: 'decision-trace:rules', text: INSTRUCTIONS, scope: 'session' }] }
  })

  // Keep both tools in Claude's list, not behind ToolSearch.
  on('tool.describe', { tool: RECORD_TOOL }, async ($, e, next) => ({ ...(await next(e)), isDeferred: false }))
  on('tool.describe', { tool: SHOW_TOOL }, async ($, e, next) => ({ ...(await next(e)), isDeferred: false }))

  on('tool.call', { tool: RECORD_TOOL }, async ($, e) => {
    // The rest of e is the call itself, as Claude sent it.
    const { tool, tool_use_id, consent, agentId, ...call } = e as unknown as Record<string, unknown>
    try {
      const id = typeof tool_use_id === 'string' ? tool_use_id : ''
      const said = await recordCall($, call as Omit<Call, 'at'>, id, typeof agentId === 'string' ? agentId : undefined)
      if (mod.isOpen) {
        await load($, true) // the pane shows the new node at once
      }
      return { result: said }
    } catch (err) {
      return failed(message(err))
    }
  })

  on('tool.call', { tool: SHOW_TOOL }, async $ => {
    try {
      const t = await loadTree(io($), await where($), await $.session.id())
      return { result: t ? showText(t) : 'The tree is empty. No decisions yet.' }
    } catch (err) {
      return failed(message(err))
    }
  })

  // The chat row that answers a record_decision call is where a branch from
  // its pick cuts the chat. Claude Code says each row's id as it saves it.
  on('session.append', async ($, e, next) => {
    const stored = await next(e)
    if (e.door === 'tool-result' && e.agentId === undefined && mod.cuts.size > 0) {
      for (const id of answers(e.message.content)) {
        const waiting = mod.cuts.get(id)
        if (waiting) {
          mod.cuts.delete(id)
          await saveCut($, waiting.session, waiting.node, e.uuid).catch(() => undefined)
        }
      }
    }
    return stored
  })

  // /decision-trace opens the pane on this session; /decision-trace <id> on
  // another one (the start of its id is enough).
  on('command.run', { command: 'decision-trace' }, async ($, e) => {
    const arg = (e.args ?? '').trim()
    let session: string | null = null
    if (arg !== '') {
      try {
        session = await find(io($), await where($), arg)
      } catch (err) {
        return { text: message(err) }
      }
    }
    await open($, session, true)
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
        <Text>{printable(error || text)}</Text>
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
