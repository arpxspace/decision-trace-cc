// What a branch needs to start from a decision: a git snapshot of the
// working folder, a copy of Claude's memory for the project, and a copy of
// the tree, all as they were when the option was picked.
//
// None of this may change the user's work. The snapshot is built in a
// temporary index and kept under a hidden ref, so the branch, the staging
// area, the stash, and the log stay exactly as they were. Views of every ref
// (git log --all) do show it, as "decision-trace checkpoint: …".
import type { Checkpoint, Tree } from '../types'
import type { IO, Places, RunInit } from './files'
import { copy, join, remove } from './files'

/** Where snapshots are kept. Refs here are not branches or tags. */
export const REF_PREFIX = 'refs/decision-trace/checkpoints/'

/** The whole snapshot must finish in this time. */
const SNAPSHOT_MS = 20_000

/** The git command a call runs, past "-C dir" and "-c key=value". */
function subcommand(args: string[]): string {
  for (let i = 0; i < args.length; i++) {
    if (args[i] === '-C' || args[i] === '-c') {
      i++
    } else {
      return args[i] ?? ''
    }
  }
  return ''
}

/** Runs git and gives its output, or throws with what git said. */
export async function git(io: IO, args: string[], init?: RunInit): Promise<string> {
  const ran = await io.run(['git', ...args], { timeoutMs: SNAPSHOT_MS, ...init })
  if (ran.exitCode !== 0) {
    throw new Error(`git ${subcommand(args)}: ${ran.stderr.trim() || `exit ${ran.exitCode}`}`)
  }
  return ran.stdout.trim()
}

/**
 * Saves the working folder as a commit and points ref at it. It works on a
 * copy of the index, so git only looks at files that changed. Files git
 * ignores are left out, and so are Claude Code's own worktrees.
 */
export async function snapshot(io: IO, p: Places, dir: string, ref: string, message: string): Promise<{ commit: string; top: string }> {
  let top: string
  try {
    top = await git(io, ['-C', dir, 'rev-parse', '--show-toplevel'])
  } catch {
    throw new Error('the folder is not a git repository')
  }
  const index = await git(io, ['-C', top, 'rev-parse', '--path-format=absolute', '--git-path', 'index'])
  const tmp = join(p.state, 'tmp', crypto.randomUUID())
  const tmpIndex = join(tmp, 'index')
  try {
    if (await io.stat(index)) {
      await copy(io, p, index, tmpIndex)
    } else {
      // A repo with no commits yet may have no index: git starts an empty one.
      await io.write(join(tmp, '.keep'), '')
    }
    const withIndex = { env: { GIT_INDEX_FILE: tmpIndex } }
    await git(io, ['-C', top, 'add', '--all', '--', '.', ':(exclude).claude/worktrees'], withIndex)
    const tree = await git(io, ['-C', top, 'write-tree'], withIndex)
    // The snapshot has its own name and never signs, so it works whatever
    // the user's git settings are.
    const args = ['-C', top, '-c', 'user.name=decision-trace', '-c', 'user.email=decision-trace@localhost',
      'commit-tree', '--no-gpg-sign', '-m', message]
    const head = await git(io, ['-C', top, 'rev-parse', '--verify', '--quiet', 'HEAD^{commit}']).catch(() => '')
    if (head !== '') {
      args.push('-p', head)
    }
    const commit = await git(io, [...args, tree])
    await git(io, ['-C', top, 'update-ref', '-m', message, ref, commit])
    return { commit, top }
  } finally {
    await remove(io, p, tmp).catch(() => undefined)
  }
}

/** How many plain files a folder has; 0 when it is missing. */
async function fileCount(io: IO, dir: string): Promise<number> {
  try {
    return (await io.list(dir)).filter(e => e.kind === 'file').length
  } catch {
    return 0
  }
}

/**
 * Saves a checkpoint for node in session. repo is the folder Claude works
 * in; memory is Claude's memory folder for the project (it may not exist).
 * Whatever cannot be saved is said in missing, in plain words; the rest is
 * still saved.
 */
export async function take(
  io: IO,
  p: Places,
  w: { repo: string; memory: string; session: string; node: string; toolUseId: string; at: string },
): Promise<Checkpoint> {
  const cp: Checkpoint = { at: w.at }
  const missing: string[] = []
  if (w.toolUseId === '') {
    missing.push('chat position unknown: the call had no tool-use id')
  } else {
    cp.tool_use_id = w.toolUseId
  }

  const ref = `${REF_PREFIX}${w.session}/${w.node}`
  try {
    const s = await snapshot(io, p, w.repo, ref, `decision-trace checkpoint: session ${w.session}, node ${w.node}`)
    Object.assign(cp, { repo: s.top, commit: s.commit, ref })
  } catch (err) {
    missing.push(`code not saved: ${err instanceof Error ? err.message : String(err)}`)
  }

  if (w.memory !== '' && (await fileCount(io, w.memory)) > 0) {
    const dst = join(p.state, 'checkpoints', w.session, w.node, 'memory')
    try {
      await remove(io, p, dst) // a pick made again replaces its old copy
      await copy(io, p, w.memory, dst)
      cp.memory = dst
    } catch (err) {
      missing.push(`memory not saved: ${err instanceof Error ? err.message : String(err)}`)
    }
  }
  if (missing.length > 0) {
    cp.missing = missing.join('; ')
  }
  return cp
}

/** Keeps a copy of the whole tree as it is at a checkpoint, so a branch from it starts with the tree as it was then. */
export async function saveTreeCopy(io: IO, p: Places, session: string, node: string, t: Tree): Promise<string> {
  const path = join(p.state, 'checkpoints', session, node, 'tree.json')
  await io.write(path, JSON.stringify(t, null, 2) + '\n')
  return path
}
