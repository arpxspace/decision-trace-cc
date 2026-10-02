// Starts a new Claude session from a decision. The original session, its
// chat, and its files are never changed.
//
// prepare works out what a branch would start with, and what is not exact.
// It changes nothing. create makes it:
//
//  1. the code: a git worktree from the checkpoint's snapshot
//  2. the chat: Claude Code's own fork of the original, cut right after the decision
//  3. the memory: Claude's memory as it was then, in the branch's folder
//  4. the tree: the tree as it was then, marked as a branch
//  5. a window to run it in: a tmux window when Claude runs in tmux, else a
//     new tab in the terminal Claude Code runs in, else a command to paste
import type { Tree, TreeNode } from '../types'
import type { Cut } from './chat'
import { chatPath, findCut, memoryOf, tmuxSessionOf } from './chat'
import { git } from './checkpoint'
import type { IO, Places, RunResult } from './files'
import { basename, copy, find, join, load, parseTree, save, short } from './files'
import { when } from './text'
import { decisionCount, node, statement, upTo } from './tree'

/** A branch that is ready to be made. */
export type Plan = {
  parent: Tree
  node: TreeNode
  cut: Cut
  name: string
  /** The new session's id. */
  session: string
  /** The original folder. */
  repo: string
  /** The branch's own folder; "" when it shares the original folder. */
  worktree: string
  /** Where the branch session runs. */
  dir: string
  /** How many memory files it starts with. */
  memory: number
  /** The branch's starting tree. */
  tree: Tree
  /** What is not exact, in plain words. */
  warnings: string[]
}

/** What create made. */
export type Made = {
  /** The new tmux pane, or "". */
  pane: string
  /** Where the branch opened when it is not in tmux, like "iTerm2 tab", or "". */
  tab: string
  /** When no window or tab could be opened: the command that starts the branch. */
  command: string
  /** Files copied from .worktreeinclude. */
  copied: string[]
  /** Why no tmux window opened, when tmux was running but refused. */
  windowError: string
}

/** A branch name from a decision: "API framework: FastAPI" becomes "api-framework-fastapi". */
export function slug(s: string): string {
  let out = s.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '')
  if (out.length > 40) {
    out = out.slice(0, 40)
    const i = out.lastIndexOf('-')
    if (i > 20) {
      out = out.slice(0, i)
    }
  }
  return out || 'branch'
}

/** path, or path-2, path-3, … if it is taken. */
async function freePath(io: IO, path: string): Promise<string> {
  if (!(await io.stat(path))) {
    return path
  }
  for (let i = 2; ; i++) {
    if (!(await io.stat(`${path}-${i}`))) {
      return `${path}-${i}`
    }
  }
}

function clip(s: string, n: number): string {
  const chars = [...s]
  return chars.length > n ? chars.slice(0, n - 1).join('') + '…' : s
}

/** Where a pick came from in the chat: saved with the checkpoint, or read back from the chat file. */
export async function cutOf(io: IO, p: Places, session: string, n: TreeNode): Promise<Cut> {
  const cp = n.checkpoint ?? {}
  if (cp.cut) {
    return { uuid: cp.cut, promptNumber: cp.prompt_number ?? 0, prompt: cp.prompt ?? '', before: cp.before ?? '' }
  }
  if (!cp.tool_use_id) {
    throw new Error('not saved: this was logged before checkpoints existed')
  }
  const chat = await chatPath(io, p, session)
  if (!chat) {
    throw new Error('the chat file is not on this computer')
  }
  return findCut(await io.read(chat), cp.tool_use_id)
}

/** Works out a branch from node in session (or the start of its id). It changes nothing. */
export async function prepare(io: IO, p: Places, session: string, id: string, name = ''): Promise<Plan> {
  const sid = await find(io, p, session)
  const parent = await load(io, p, sid)
  if (!parent) {
    throw new Error(`no tree for session ${short(sid)}`)
  }
  const n = node(parent, id)
  if (!n || n.hidden) {
    throw new Error(`there is no node ${id} in session ${short(sid)}`)
  }
  if (!n.decision) {
    throw new Error('the start node is not a decision; branch from a pick')
  }
  if (n.state !== 'picked' && n.state !== 'dropped') {
    throw new Error(`"${statement(parent, n)}" was never picked, so there is no moment to go back to`)
  }
  const cp = n.checkpoint ?? {}
  if (!cp.tool_use_id && !cp.cut) {
    throw new Error(`"${statement(parent, n)}" has no checkpoint: it was logged before checkpoints existed`)
  }
  const cut = await cutOf(io, p, sid, n)

  const plan: Plan = {
    parent,
    node: n,
    cut,
    name: name || slug(statement(parent, n)),
    session: crypto.randomUUID(),
    repo: cp.repo || parent.folder || '',
    worktree: '',
    dir: '',
    memory: 0,
    tree: parent,
    warnings: [],
  }
  if (cp.commit) {
    plan.worktree = await freePath(io, join(p.state, 'worktrees', basename(plan.repo) || 'project', plan.name))
    plan.name = basename(plan.worktree) // "-2" and so on, if the name was taken
    plan.dir = plan.worktree
    if (cp.memory) {
      plan.memory = await io.list(cp.memory).then(es => es.filter(e => e.kind === 'file').length, () => 0)
    }
  } else {
    if (plan.repo === '') {
      throw new Error('there is no code checkpoint and no folder to fall back on')
    }
    plan.dir = plan.repo
    plan.warnings.push(
      `Exact code state unavailable. The current folder will be used: ${plan.repo}.`,
      "Both sessions will work on the same files, and share Claude's memory, including anything saved later.",
    )
  }

  let start: Tree | null = null
  if (cp.tree) {
    start = await io.read(cp.tree).then(text => parseTree(cp.tree as string, text), () => null)
  }
  if (!start) {
    start = upTo(parent, n.id)
    plan.warnings.push('The tree was rebuilt from today\'s, so a decision above this one that changed later shows its later pick.')
  }
  start.session_id = plan.session
  start.folder = plan.dir
  delete start.fixes
  // A saved cut is a row of the original's chat. The branch's chat is a
  // copy, so a branch of the branch finds its cut by the tool-use id.
  for (const m of start.nodes) {
    delete m.checkpoint?.cut
  }
  start.branch = {
    name: plan.name,
    from_session: sid,
    from_node: n.id,
    from_decision: n.decision,
    inherited: decisionCount(start),
    statement: statement(parent, n),
    cut_message: cut.uuid,
    at: new Date().toISOString(),
    ...(cp.commit ? { commit: cp.commit } : {}),
    ...(plan.repo ? { repo: plan.repo } : {}),
    ...(plan.worktree ? { worktree: plan.worktree } : {}),
    ...(cp.commit ? {} : { missing: 'not exact: code' }),
  }
  plan.tree = start
  return plan
}

/** What to show before asking "Create a branch?". */
export function summary(plan: Plan): string {
  const lines = [
    `Branch from:  ${statement(plan.parent, plan.node)} · ${when(plan.node.at)}`,
    `Session:      ${short(plan.parent.session_id)} (${basename(plan.repo)})`,
    `Chat:         up to your message #${plan.cut.promptNumber}, "${clip(plan.cut.prompt, 60)}"`,
  ]
  if (plan.worktree) {
    lines.push(`Code:         as it was then, in ${plan.worktree}`)
    lines.push(
      plan.memory > 0
        ? `Memory:       as it was then (${plan.memory} files)`
        : 'Memory:       none was saved then, so the branch starts with none',
    )
  } else {
    lines.push('Code:         not available')
  }
  lines.push(`Name:         ${plan.name}`)
  for (const w of plan.warnings) {
    lines.push('', w)
  }
  return lines.join('\n')
}

/**
 * The branch's folder: the files as they were, including work that was not
 * committed, which shows as not committed again. HEAD is the commit the
 * original was on.
 */
async function worktree(io: IO, repo: string, path: string, snapshot: string): Promise<void> {
  const parent = await git(io, ['-C', repo, 'rev-parse', '--verify', '--quiet', `${snapshot}^`]).catch(() => '')
  if (parent === '') {
    // A repo with no commits yet: the snapshot is all there is.
    await git(io, ['-C', repo, 'worktree', 'add', '--detach', path, snapshot])
    return
  }
  await git(io, ['-C', repo, 'worktree', 'add', '--detach', path, parent])
  try {
    // Put the snapshot's files in place (deleted files too), then let the
    // index match HEAD again, so the old work is not committed or staged.
    await git(io, ['-C', path, 'read-tree', '-u', '--reset', snapshot])
    await git(io, ['-C', path, 'reset', '--quiet'])
  } catch (err) {
    await removeWorktree(io, repo, path)
    throw err
  }
}

async function removeWorktree(io: IO, repo: string, path: string): Promise<void> {
  if (repo !== '' && path !== '') {
    await git(io, ['-C', repo, 'worktree', 'remove', '--force', path]).catch(() => '')
  }
}

/**
 * Copies files that git ignores (like .env) into the worktree, when the
 * repo's .worktreeinclude names them (Claude Code's own convention, in
 * .gitignore syntax). They are copied as they are now: git ignores them, so
 * no snapshot has them.
 */
async function include(io: IO, p: Places, repo: string, wt: string): Promise<string[]> {
  const list = join(repo, '.worktreeinclude')
  if (!(await io.stat(list))) {
    return []
  }
  const named = await git(io, ['-C', repo, 'ls-files', '--others', '--ignored', `--exclude-from=${list}`, '--directory'])
  const copied: string[] = []
  for (const rel of named.split('\n').map(l => l.trim().replace(/\/$/, '')).filter(Boolean)) {
    // Only files git ignores: everything else is in the snapshot already.
    const ignored = await io.run(['git', '-C', repo, 'check-ignore', '--quiet', '--', rel])
    if (ignored.exitCode !== 0) {
      continue
    }
    await copy(io, p, join(repo, ...rel.split('/')), join(wt, ...rel.split('/')))
    copied.push(rel)
  }
  return copied
}

/**
 * Makes the branch's chat with Claude Code's own fork: a new session, cut
 * right after the decision, in the branch's folder. Hooks and MCP servers
 * are off for this one short run. --resume-session-at is a flag Claude Code
 * does not list in its help; it keeps the chat up to that row.
 */
async function fork(io: IO, p: Places, plan: Plan): Promise<void> {
  let note =
    `[decision-trace] This is a branch named ${plan.name}. It continues session ${short(plan.parent.session_id)}` +
    ` from right after the decision "${statement(plan.parent, plan.node)}". Nothing said after that point is part of this branch.`
  if (plan.worktree) {
    note +=
      ` The project's files are now in ${plan.worktree}, as they were at that moment. The folder ${plan.repo}` +
      ` belongs to the other session: do not change files there; use the same paths inside ${plan.worktree} instead.`
  }
  note += ' Reply with only: Branch ready.'
  const ran = await io.run(
    ['claude', '-p', '--model', 'haiku', '--strict-mcp-config', '--settings', '{"disableAllHooks":true}',
      '--resume', plan.parent.session_id, '--fork-session', '--resume-session-at', plan.cut.uuid,
      '--session-id', plan.session, note],
    { cwd: plan.dir, timeoutMs: 180_000 },
  )
  const out = clip((ran.stdout + ran.stderr).trim(), 300)
  if (ran.exitCode !== 0) {
    throw new Error(`claude: exit ${ran.exitCode}: ${out}`)
  }
  if (!(await chatPath(io, p, plan.session))) {
    throw new Error(`claude ran, but the branch's chat was not saved: ${out}`)
  }
}

// ---- tmux ----

/** Runs tmux; null when tmux is not installed or not running. */
async function tmux(io: IO, args: string[]): Promise<RunResult | null> {
  try {
    const ran = await io.run(['tmux', ...args])
    if (/no server running|error connecting to/.test(ran.stderr)) {
      return null
    }
    return ran
  } catch {
    return null
  }
}

/** The tmux client with the newest activity (the last key press or output). */
async function newestClient(io: IO): Promise<{ tty: string; session: string }> {
  const ran = await tmux(io, ['list-clients', '-F', '#{client_activity}\t#{client_tty}\t#{client_session}'])
  let best = { tty: '', session: '' }
  let bestAt = -1
  for (const line of ran?.exitCode === 0 ? ran.stdout.split('\n') : []) {
    const [at, tty, session] = line.split('\t')
    if (session !== undefined && Number(at) > bestAt) {
      best = { tty: tty ?? '', session }
      bestAt = Number(at)
    }
  }
  return best
}

/** An AppleScript string. */
const appleString = (s: string) => `"${s.replace(/\\/g, '\\\\').replace(/"/g, '\\"')}"`

/**
 * Opens the branch in a new tab of the terminal Claude Code runs in. It
 * answers where, like "iTerm2 tab", or "" when the terminal is not one it
 * knows, or the tab did not open.
 */
async function openTab(io: IO, p: Places, dir: string, name: string, args: string[]): Promise<string> {
  const line = `cd ${shellLine([dir], false)} && ${shellLine(args, false)}`
  let where: string
  let argv: string[]
  switch (p.terminal) {
    case 'iterm':
      where = 'iTerm2 tab'
      argv = ['osascript', '-e', [
        'tell application "iTerm2"',
        '  activate',
        '  if (count of windows) is 0 then',
        '    set w to (create window with default profile)',
        '  else',
        '    set w to current window',
        '    tell w to create tab with default profile',
        '  end if',
        `  tell current session of w to write text ${appleString(line)}`,
        'end tell',
      ].join('\n')]
      break
    case 'apple-terminal':
      // Terminal has no script command for a tab, so this is a new window.
      where = 'Terminal window'
      argv = ['osascript', '-e', `tell application "Terminal"\n  activate\n  do script ${appleString(line)}\nend tell`]
      break
    case 'windows-terminal':
      where = 'Windows Terminal tab'
      // wt reads a ";" as the start of another command.
      argv = ['wt', '-w', '0', 'new-tab', '--title', name, '-d', dir, 'cmd', '/k', ...args.map(a => a.replace(/;/g, '\\;'))]
      break
    case 'wezterm':
      where = 'WezTerm tab'
      argv = ['wezterm', 'cli', 'spawn', '--cwd', dir, '--', ...args]
      break
    case 'kitty':
      // Only works with kitty's remote control turned on (allow_remote_control).
      where = 'kitty tab'
      argv = ['kitty', '@', 'launch', '--type=tab', '--tab-title', name, '--cwd', dir, ...args]
      break
    case 'gnome-terminal':
      where = 'GNOME Terminal tab'
      argv = ['gnome-terminal', '--tab', `--working-directory=${dir}`, '--', ...args]
      break
    case 'konsole':
      where = 'Konsole tab'
      argv = ['konsole', '--new-tab', '--workdir', dir, '-e', ...args]
      break
    default:
      return ''
  }
  try {
    return (await io.run(argv, { cwd: dir })).exitCode === 0 ? where : ''
  } catch {
    return '' // the terminal's own program is not there
  }
}

/** Joins args into one shell command, each quoted: for tmux, and for a person to paste. */
export function shellLine(args: string[], windows: boolean): string {
  if (windows) {
    return args.map(a => `"${a.replace(/"/g, '\\"')}"`).join(' ')
  }
  return args.map(a => `'${a.replace(/'/g, `'\\''`)}'`).join(' ')
}

/**
 * Makes the branch. If a step fails, the branch's new folder is removed
 * again; the original is never touched. here is the session the person
 * asked from, which may be another than the one branched from.
 */
export async function create(io: IO, p: Places, plan: Plan, focus: boolean, here = ''): Promise<Made> {
  const made: Made = { pane: '', tab: '', command: '', copied: [], windowError: '' }
  const cp = plan.node.checkpoint ?? {}
  if (plan.worktree && cp.repo && cp.commit) {
    try {
      await worktree(io, cp.repo, plan.worktree, cp.commit)
    } catch (err) {
      throw new Error(`the code: ${err instanceof Error ? err.message : String(err)}`)
    }
    try {
      made.copied = await include(io, p, cp.repo, plan.worktree)
    } catch (err) {
      await removeWorktree(io, cp.repo, plan.worktree)
      throw new Error(`copying .worktreeinclude files: ${err instanceof Error ? err.message : String(err)}`)
    }
  }

  try {
    await fork(io, p, plan)
  } catch (err) {
    await removeWorktree(io, cp.repo ?? '', plan.worktree)
    throw new Error(`the chat: ${err instanceof Error ? err.message : String(err)}`)
  }

  if (plan.worktree && cp.memory) {
    const chat = await chatPath(io, p, plan.session)
    try {
      if (!chat) {
        throw new Error("the branch's chat is missing")
      }
      await copy(io, p, cp.memory, memoryOf(chat))
    } catch (err) {
      throw new Error(`the memory: ${err instanceof Error ? err.message : String(err)}`)
    }
  }

  await save(io, p, plan.tree)

  const args = ['claude', '--resume', plan.session, '-n', plan.name]
  if (plan.worktree) {
    args.push(
      '--append-system-prompt',
      `This session is a branch named ${plan.name}. Its files are in ${plan.worktree}. The folder ${plan.repo} belongs to the original session: never change files there.`,
    )
  }
  const cd = p.windows ? `cd /d "${plan.dir}" && ` : `cd ${shellLine([plan.dir], false)} && `
  made.command = cd + shellLine(args, p.windows)
  // tmux, when the original Claude or the one the person asked from runs in it.
  const target = (await tmuxSessionOf(io, p, plan.parent.session_id)) || (here ? await tmuxSessionOf(io, p, here) : '')
  if (target === '') {
    made.tab = await openTab(io, p, plan.dir, plan.name, args)
    if (made.tab !== '') {
      made.command = ''
    }
    return made // with no tab, the person starts it with the command
  }
  const ran = await tmux(io, [
    'new-window', ...(focus ? [] : ['-d']), '-t', `${target}:`, '-c', plan.dir, '-n', plan.name,
    '-P', '-F', '#{pane_id}', shellLine(args, false),
  ])
  if (!ran || ran.exitCode !== 0) {
    // The branch is made; only its window is not. The person starts it with the command.
    made.windowError = ran?.stderr.trim() || 'tmux did not run'
    return made
  }
  made.pane = ran.stdout.trim()
  made.command = ''
  if (focus) {
    // Point the newest client at the new pane: its session, window, and the pane itself.
    const c = await newestClient(io)
    if (c.tty) {
      await tmux(io, ['switch-client', '-c', c.tty, '-t', made.pane, ';', 'select-window', '-t', made.pane, ';', 'select-pane', '-t', made.pane])
    }
  }
  return made
}
