// Files and programs, on every system Claude Code runs on. The hooks module
// hands in an IO: the mod's own file tools and program runner. Everything
// here works through it, so tests can fake the outside world.
//
// Trees live one file per session, in the state folder:
//   macOS and Linux: ~/.local/state/decision-trace (or $XDG_STATE_HOME/decision-trace)
//   Windows:         %LOCALAPPDATA%\decision-trace
import type { Stub, Tree } from '../types'
import { decisionCount, newTree, VERSION } from './tree'

export type RunResult = { exitCode: number; stdout: string; stderr: string }

export type RunInit = { cwd?: string; env?: Record<string, string>; stdin?: string; timeoutMs?: number }

export type Entry = { name: string; kind: 'file' | 'dir' | 'other'; mtimeMs: number }

/** The outside world: what the hooks module gives these functions. */
export type IO = {
  /** A file's text; rejects when it is missing, or over 4 MiB. */
  read(path: string): Promise<string>
  /** Writes a whole file, making its folders as needed. */
  write(path: string, text: string): Promise<void>
  /** A folder's entries; rejects when it is missing. */
  list(path: string): Promise<Entry[]>
  /** What a path is, or null when there is nothing there. */
  stat(path: string): Promise<{ kind: 'file' | 'dir' | 'other'; mtimeMs: number } | null>
  /** Runs a program; rejects when it cannot start or runs too long. */
  run(argv: string[], init?: RunInit): Promise<RunResult>
}

/** The environment variables that say where things are. */
export type Env = {
  OS?: string
  HOME?: string
  USERPROFILE?: string
  XDG_STATE_HOME?: string
  LOCALAPPDATA?: string
  CLAUDE_CONFIG_DIR?: string
  // Which terminal Claude Code runs in: each one sets some of these.
  TERM_PROGRAM?: string
  WT_SESSION?: string
  WEZTERM_PANE?: string
  KITTY_WINDOW_ID?: string
  GNOME_TERMINAL_SCREEN?: string
  KONSOLE_VERSION?: string
}

/** A terminal that a branch can open a tab in, or "" for one this does not know. */
export type Terminal = 'iterm' | 'apple-terminal' | 'windows-terminal' | 'wezterm' | 'kitty' | 'gnome-terminal' | 'konsole' | ''

/** Where things are on this computer. */
export type Places = {
  windows: boolean
  home: string
  /** Where trees, checkpoints, and branch folders go. */
  state: string
  /** Claude Code's own folder: ~/.claude, or $CLAUDE_CONFIG_DIR. */
  claude: string
  /** The terminal Claude Code runs in. */
  terminal: Terminal
}

/** The terminal Claude Code runs in, from the variables each one sets. */
export function terminalOf(env: Env): Terminal {
  if (env.WT_SESSION) return 'windows-terminal'
  if (env.WEZTERM_PANE || env.TERM_PROGRAM === 'WezTerm') return 'wezterm'
  if (env.KITTY_WINDOW_ID) return 'kitty'
  if (env.TERM_PROGRAM === 'iTerm.app') return 'iterm'
  if (env.TERM_PROGRAM === 'Apple_Terminal') return 'apple-terminal'
  if (env.GNOME_TERMINAL_SCREEN) return 'gnome-terminal'
  if (env.KONSOLE_VERSION) return 'konsole'
  return ''
}

export function places(env: Env): Places {
  const windows = env.OS === 'Windows_NT'
  const home = env.HOME || env.USERPROFILE || ''
  const local = windows ? env.LOCALAPPDATA : undefined
  const state = env.XDG_STATE_HOME
    ? join(env.XDG_STATE_HOME, 'decision-trace')
    : local
      ? join(local, 'decision-trace')
      : join(home, '.local', 'state', 'decision-trace')
  const claude = env.CLAUDE_CONFIG_DIR || join(home, '.claude')
  return { windows, home, state, claude, terminal: terminalOf(env) }
}

// ---- paths ----

/** The separator a path already uses: "\" for a Windows path, else "/". */
const sepOf = (path: string) => (/^[A-Za-z]:\\|^\\\\/.test(path) || (path.includes('\\') && !path.includes('/')) ? '\\' : '/')

/** Joins path parts with the separator the first part uses. */
export function join(first: string, ...rest: string[]): string {
  const sep = sepOf(first)
  let out = first
  for (const part of rest) {
    out = out.replace(/[\\/]+$/, '') + sep + part.replace(/^[\\/]+/, '')
  }
  return out
}

export function dirname(path: string): string {
  const i = Math.max(path.lastIndexOf('/'), path.lastIndexOf('\\'))
  return i <= 0 ? path.slice(0, i + 1) : path.slice(0, i)
}

export function basename(path: string): string {
  return path.slice(Math.max(path.lastIndexOf('/'), path.lastIndexOf('\\')) + 1)
}

/** The first 8 characters of a session id, like a short git hash. */
export const short = (id: string) => id.slice(0, 8)

// ---- copying and removing, any kind of file ----

// The mod's own file tools read and write text. Copies of other files (git's
// index, a .env, node_modules) use the copy tool every system ships with:
// cp on macOS and Linux, robocopy on Windows.

/** Copies a file or a folder to dst, which must not exist yet. On Windows the names must match. */
export async function copy(io: IO, p: Places, src: string, dst: string): Promise<void> {
  const what = await io.stat(src)
  if (!what) {
    throw new Error(`${src} is missing`)
  }
  if (p.windows) {
    // robocopy copies folders, or files by name; it makes the folders it needs.
    const argv =
      what.kind === 'dir'
        ? ['robocopy', src, dst, '/E']
        : ['robocopy', dirname(src), dirname(dst), basename(src)]
    const ran = await io.run([...argv, '/NFL', '/NDL', '/NJH', '/NJS', '/NP', '/R:0', '/W:0'])
    if (ran.exitCode >= 8) {
      throw new Error(`robocopy could not copy ${src}: ${(ran.stdout + ran.stderr).trim()}`)
    }
    return
  }
  for (const argv of [['mkdir', '-p', dirname(dst)], ['cp', '-R', src, dst]]) {
    const ran = await io.run(argv)
    if (ran.exitCode !== 0) {
      throw new Error(`${argv[0]}: ${ran.stderr.trim()}`)
    }
  }
}

/** Removes a file or folder this program made itself. */
export async function remove(io: IO, p: Places, path: string): Promise<void> {
  const what = await io.stat(path)
  if (!what) {
    return
  }
  const argv = !p.windows
    ? ['rm', '-rf', path]
    : what.kind === 'dir'
      ? ['cmd', '/c', 'rmdir', '/s', '/q', path]
      : ['cmd', '/c', 'del', '/f', '/q', path]
  await io.run(argv)
}

// ---- trees on disk ----

// Session ids are UUIDs. Anything else could point outside the folder.
const VALID_ID = /^[A-Za-z0-9_-]{1,128}$/

/** Where a session's tree lives. */
export function treePath(p: Places, session: string): string {
  if (!VALID_ID.test(session)) {
    throw new Error(`bad session id "${session}"`)
  }
  return join(p.state, `${session}.json`)
}

/** Reads a tree file. A file that cannot be read is an error, never an empty tree. */
export function parseTree(path: string, text: string): Tree {
  let t: Tree
  try {
    t = JSON.parse(text) as Tree
  } catch (err) {
    throw new Error(`${path}: ${err instanceof Error ? err.message : String(err)}`)
  }
  if (t.version > VERSION) {
    throw new Error(`${path} was written by a newer decision-trace (format ${t.version})`)
  }
  if (!Array.isArray(t.nodes) || t.nodes.length === 0) {
    throw new Error(`${path} has no start node`)
  }
  // Trees saved before 30 Sep 2026 call the topic "question".
  for (const d of t.decisions ?? []) {
    const old = d as typeof d & { question?: string }
    if (!d.topic && old.question) {
      d.topic = old.question
    }
    delete old.question
  }
  return t
}

/** A session's tree, or null when it has none yet. */
export async function load(io: IO, p: Places, session: string): Promise<Tree | null> {
  const path = treePath(p, session)
  if (!(await io.stat(path))) {
    return null
  }
  return parseTree(path, await io.read(path))
}

export async function save(io: IO, p: Places, t: Tree): Promise<void> {
  await io.write(treePath(p, t.session_id), JSON.stringify(t, null, 2) + '\n')
}

// One session's tree is written by that session's mod alone, so a queue
// here keeps two changes from wiping out each other.
const queues = new Map<string, Promise<unknown>>()

/** Changes a session's tree: reads it fresh (or starts it empty), applies fn, and saves what fn returns. */
export function update(io: IO, p: Places, session: string, fn: (t: Tree) => Promise<Tree> | Tree): Promise<Tree> {
  const run = async () => {
    const t = (await load(io, p, session)) ?? newTree(session, new Date().toISOString())
    const next = await fn(t)
    await save(io, p, next)
    return next
  }
  const done = (queues.get(session) ?? Promise.resolve()).then(run, run)
  queues.set(session, done.catch(() => undefined))
  return done
}

/** The saved trees, newest first. */
export async function list(io: IO, p: Places): Promise<{ session: string; mtimeMs: number }[]> {
  let entries: Entry[]
  try {
    entries = await io.list(p.state)
  } catch {
    return []
  }
  return entries
    .filter(e => e.kind === 'file' && e.name.endsWith('.json') && VALID_ID.test(e.name.slice(0, -5)))
    .map(e => ({ session: e.name.slice(0, -5), mtimeMs: e.mtimeMs }))
    .sort((a, b) => b.mtimeMs - a.mtimeMs)
}

/** Turns a session id, or the start of one, into a saved tree's id. */
export async function find(io: IO, p: Places, prefix: string): Promise<string> {
  const found = (await list(io, p)).filter(t => t.session.startsWith(prefix))
  if (found.length === 0) {
    throw new Error(prefix === '' ? 'no trees saved yet' : `no tree for session "${prefix}"`)
  }
  if (found.length > 1 && prefix !== '') {
    throw new Error(`"${prefix}" matches ${found.length} sessions; give more of the id`)
  }
  return (found[0] as { session: string }).session
}

// Every tree's "branch of" note, kept by file time, so finding branches
// reads only the trees that changed.
const origins = new Map<string, { mtimeMs: number; from?: string; node?: string; stub?: Stub; at?: string }>()

/** The branches made from a session, by the node each came from, oldest first. Trees that cannot be read are skipped. */
export async function branchesOf(io: IO, p: Places, session: string): Promise<Record<string, Stub[]>> {
  const found: { node: string; at: string; stub: Stub }[] = []
  for (const t of await list(io, p)) {
    let o = origins.get(t.session)
    if (!o || o.mtimeMs !== t.mtimeMs) {
      o = { mtimeMs: t.mtimeMs }
      try {
        const tree = await load(io, p, t.session)
        if (tree?.branch) {
          // Decisions the branch made itself, not the ones it started with.
          const made = Math.max(decisionCount(tree) - (tree.branch.inherited ?? 0), 0)
          o = {
            mtimeMs: t.mtimeMs,
            from: tree.branch.from_session,
            node: tree.branch.from_node,
            at: tree.branch.at,
            stub: { session: tree.session_id, name: tree.branch.name, decisions: made },
          }
        }
      } catch {
        // skipped, and tried again once the file changes
      }
      origins.set(t.session, o)
    }
    if (o.from === session && o.node && o.stub) {
      found.push({ node: o.node, at: o.at ?? '', stub: o.stub })
    }
  }
  found.sort((a, b) => a.at.localeCompare(b.at))
  const out: Record<string, Stub[]> = {}
  for (const f of found) {
    ;(out[f.node] ??= []).push(f.stub)
  }
  return out
}
