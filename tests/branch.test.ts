// A branch from a decision: what it would start with, and the commands that
// make it. dev/real-git.spec.ts makes one against real git.
import { describe, expect, test } from 'claude-code/testing'

import type { Tree } from '../types'
import { create, prepare, slug, summary } from '../hooks/branch'
import { treePath } from '../hooks/files'
import { newTree, record } from '../hooks/tree'
import { HOME, ok, World } from './fake'

const at = '2026-09-29T15:00:00.000Z'
const STATE = HOME.state

/** Session s1 in /w/crm: "API framework: FastAPI" (n1) with a full checkpoint, then "Cache: Redis". */
function world(): World {
  let t: Tree = newTree('s1', at)
  t.folder = '/w/crm'
  t = record(t, { topic: 'API framework', options: ['FastAPI', 'Flask'], picked: 'FastAPI', reason: 'r', by: 'both', at }).tree
  t.nodes[1]!.checkpoint = {
    tool_use_id: 'toolu_1', cut: 'row-7', prompt: 'Use FastAPI', prompt_number: 1,
    repo: '/w/crm', commit: 'snap1', ref: 'refs/decision-trace/checkpoints/s1/n1',
    memory: `${STATE}/checkpoints/s1/n1/memory`, tree: `${STATE}/checkpoints/s1/n1/tree.json`, at,
  }
  const then = JSON.parse(JSON.stringify(t)) as Tree
  t = record(t, { topic: 'Cache', options: ['Redis'], picked: 'Redis', reason: 'r', by: 'claude', at }).tree
  const w = new World({
    [treePath(HOME, 's1')]: JSON.stringify(t),
    [`${STATE}/checkpoints/s1/n1/tree.json`]: JSON.stringify(then),
    [`${STATE}/checkpoints/s1/n1/memory/MEMORY.md`]: '- likes short answers',
    '/w/crm/.worktreeinclude': '.env\n',
    '/w/crm/.env': 'SECRET=1\n',
    '/home/u/.claude/sessions/41.json': '{"sessionId":"s1","tmux":"work:@1.%5"}',
  })
  w.script = (argv, init) => {
    const a = argv.join(' ')
    if (a.includes('rev-parse --verify --quiet snap1^')) return ok('head1\n')
    if (a.includes('ls-files')) return ok('.env\n')
    if (argv[0] === 'claude' && argv[1] === '-p') {
      // The fork saves the branch's chat in the branch's own project folder.
      const id = argv[argv.indexOf('--session-id') + 1]
      w.put(`/home/u/.claude/projects/-branch/${id}.jsonl`, '')
      return ok('Branch ready.')
    }
    if (argv[0] === 'tmux' && argv[1] === 'new-window') return ok('%9\n')
    return undefined
  }
  return w
}

describe('branch', () => {
  test('names come from the decision', () => {
    expect(slug('API framework: FastAPI')).toBe('api-framework-fastapi')
    expect(slug('!!!')).toBe('branch')
  })

  test('prepare works out what the branch starts with, and changes nothing', async () => {
    const w = world()
    const files = w.files.size
    const plan = await prepare(w.io, HOME, 's1', 'n1')
    expect(plan).toMatchObject({
      name: 'api-framework-fastapi',
      repo: '/w/crm',
      worktree: `${STATE}/worktrees/crm/api-framework-fastapi`,
      dir: `${STATE}/worktrees/crm/api-framework-fastapi`,
      memory: 1,
      cut: { uuid: 'row-7', prompt: 'Use FastAPI', promptNumber: 1 },
      warnings: [],
    })
    // It starts with the tree as it was then: FastAPI is "you are here", and
    // Redis, decided later, is not there.
    expect(plan.tree.here).toBe('n1')
    expect(plan.tree.decisions.map(d => d.topic)).toEqual(['API framework'])
    expect(plan.tree.branch).toMatchObject({ name: 'api-framework-fastapi', from_session: 's1', from_node: 'n1', inherited: 1, cut_message: 'row-7' })
    expect(plan.tree.nodes[1]?.checkpoint?.cut).toBeUndefined()
    expect(summary(plan)).toContain('Chat:         up to your message #1, "Use FastAPI"')
    expect(w.files.size).toBe(files)
    expect(w.runs).toHaveLength(0)
  })

  test('prepare refuses what has no moment to go back to', async () => {
    const w = world()
    await expect(prepare(w.io, HOME, 's1', 'n0')).rejects.toThrow('the start node is not a decision')
    await expect(prepare(w.io, HOME, 's1', 'n2')).rejects.toThrow('was never picked')
    await expect(prepare(w.io, HOME, 's1', 'n3')).rejects.toThrow('has no checkpoint')
    await expect(prepare(w.io, HOME, 's1', 'n9')).rejects.toThrow('there is no node n9')
  })

  test('create makes the worktree, forks the chat, copies memory, saves the tree, and opens a tmux window', async () => {
    const w = world()
    const plan = await prepare(w.io, HOME, 's1', 'n1')
    const made = await create(w.io, HOME, plan, false)
    const wt = plan.worktree
    expect(made).toEqual({ pane: '%9', tab: '', command: '', copied: ['.env'], windowError: '' })
    const git = w.ran('git').map(r => r.argv.slice(1).join(' '))
    expect(git).toContain(`-C /w/crm worktree add --detach ${wt} head1`)
    expect(git).toContain(`-C ${wt} read-tree -u --reset snap1`)
    expect(git).toContain(`-C ${wt} reset --quiet`)
    expect(w.ran('cp', '-R', '/w/crm/.env', `${wt}/.env`)).toHaveLength(1)
    const fork = w.ran('claude', '-p')[0]
    expect(fork?.argv).toContain('--fork-session')
    // The fork re-reads the whole chat, so it runs with no tools at all.
    expect(fork?.argv.slice(fork.argv.indexOf('--tools'), fork.argv.indexOf('--tools') + 2)).toEqual(['--tools', ''])
    expect(fork?.argv.slice(fork.argv.indexOf('--resume-session-at'), fork.argv.indexOf('--resume-session-at') + 2)).toEqual(['--resume-session-at', 'row-7'])
    expect(fork?.init?.cwd).toBe(wt)
    expect(w.ran('cp', '-R', `${STATE}/checkpoints/s1/n1/memory`, '/home/u/.claude/projects/-branch/memory')).toHaveLength(1)
    expect(w.json(treePath(HOME, plan.session)).branch.from_node).toBe('n1')
    const win = w.ran('tmux', 'new-window')[0]?.argv ?? []
    expect(win.slice(0, 9)).toEqual(['tmux', 'new-window', '-d', '-t', 'work:', '-c', wt, '-n', 'api-framework-fastapi'])
    expect(win.at(-1)).toContain(`'claude' '--resume' '${plan.session}'`)
  })

  test('a session asked from inside tmux opens the branch there too', async () => {
    const w = world()
    w.files.delete('/home/u/.claude/sessions/41.json') // the original Claude is not running
    w.put('/home/u/.claude/sessions/77.json', '{"sessionId":"s2","tmux":"other:@3.%1"}')
    const plan = await prepare(w.io, HOME, 's1', 'n1')
    expect((await create(w.io, HOME, plan, false, 's2')).pane).toBe('%9')
    expect(w.ran('tmux', 'new-window')[0]?.argv.slice(3, 5)).toEqual(['-t', 'other:'])
  })

  test('with no tmux, it opens a tab in iTerm2', async () => {
    const w = world()
    w.files.delete('/home/u/.claude/sessions/41.json')
    const plan = await prepare(w.io, HOME, 's1', 'n1')
    const made = await create(w.io, { ...HOME, terminal: 'iterm' }, plan, false)
    expect(made).toMatchObject({ pane: '', tab: 'iTerm2 tab', command: '' })
    const script = w.ran('osascript')[0]?.argv[2] ?? ''
    expect(script).toContain('tell w to create tab with default profile')
    expect(script).toContain(`write text "cd '${plan.dir}' && 'claude' '--resume' '${plan.session}'`)
    expect(w.ran('tmux', 'new-window')).toHaveLength(0)
  })

  test('with no tmux, it opens a tab in Windows Terminal, WezTerm, GNOME Terminal, or Konsole', async () => {
    for (const [terminal, first] of [
      ['windows-terminal', ['wt', '-w', '0', 'new-tab']],
      ['wezterm', ['wezterm', 'cli', 'spawn', '--cwd']],
      ['gnome-terminal', ['gnome-terminal', '--tab']],
      ['konsole', ['konsole', '--new-tab']],
    ] as const) {
      const w = world()
      w.files.delete('/home/u/.claude/sessions/41.json')
      const plan = await prepare(w.io, HOME, 's1', 'n1')
      const made = await create(w.io, { ...HOME, terminal }, plan, false)
      expect(made.tab).not.toBe('')
      const run = w.runs.find(r => r.argv[0] === first[0])
      expect(run?.argv.slice(0, first.length)).toEqual([...first])
      expect(run?.argv).toContain(plan.session)
      expect(run?.argv).not.toContain('cmd') // nothing reads the folder names as commands
    }
  })

  test('with no tmux and a tab that does not open, it says how to start the branch', async () => {
    const w = world()
    w.files.delete('/home/u/.claude/sessions/41.json')
    const base = w.script
    w.script = (argv, init) => (argv[0] === 'kitty' ? { exitCode: 1, stderr: 'Remote control is disabled' } : base(argv, init))
    const plan = await prepare(w.io, HOME, 's1', 'n1')
    const made = await create(w.io, { ...HOME, terminal: 'kitty' }, plan, false)
    expect(made.tab).toBe('')
    expect(made.pane).toBe('')
    expect(made.command).toBe(`cd '${plan.dir}' && 'claude' '--resume' '${plan.session}' '-n' 'api-framework-fastapi' '--append-system-prompt' '${plan.tree.branch ? `This session is a branch named api-framework-fastapi. Its files are in ${plan.worktree}. The folder /w/crm belongs to the original session: never change files there.` : ''}'`)
  })

  test('a failed fork removes the new folder again', async () => {
    const w = world()
    const base = w.script
    w.script = (argv, init) => (argv[0] === 'claude' ? { exitCode: 1, stderr: 'No conversation found' } : base(argv, init))
    const plan = await prepare(w.io, HOME, 's1', 'n1')
    await expect(create(w.io, HOME, plan, false)).rejects.toThrow('the chat: claude: exit 1: No conversation found')
    expect(w.ran('git', '-C', '/w/crm', 'worktree', 'remove', '--force', plan.worktree)).toHaveLength(1)
  })
})
