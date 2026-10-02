// A checkpoint: the git commands that snapshot the working folder without
// touching the user's work, and the memory copy. dev/real-git.spec.ts runs the
// same code against real git.
import { describe, expect, test } from 'claude-code/testing'

import { REF_PREFIX, saveTreeCopy, take } from '../hooks/checkpoint'
import { newTree } from '../hooks/tree'
import { fail, HOME, ok, World } from './fake'

const at = '2026-09-29T15:00:00.000Z'

/** A repo at /w/crm whose git answers like a real one. */
function repo(files: Record<string, string> = {}): World {
  const w = new World({ '/w/crm/.git/index': 'DIRC', ...files })
  w.script = argv => {
    const a = argv.join(' ')
    if (a.includes('--show-toplevel')) return ok('/w/crm\n')
    if (a.includes('--git-path index')) return ok('/w/crm/.git/index\n')
    if (a.includes('write-tree')) return ok('tree1\n')
    if (a.includes('HEAD^{commit}')) return ok('head1\n')
    if (a.includes('commit-tree')) return ok('commit1\n')
    return undefined
  }
  return w
}

describe('checkpoint', () => {
  test('snapshots the folder from a copy of the index, under a hidden ref', async () => {
    const w = repo({ '/home/u/.claude/projects/-w-crm/memory/MEMORY.md': '- likes short answers' })
    const cp = await take(w.io, HOME, { repo: '/w/crm/src', memory: '/home/u/.claude/projects/-w-crm/memory', session: 's1', node: 'n2', toolUseId: 'toolu_1', at })
    expect(cp).toEqual({
      at,
      tool_use_id: 'toolu_1',
      repo: '/w/crm',
      commit: 'commit1',
      ref: `${REF_PREFIX}s1/n2`,
      memory: '/home/u/.local/state/decision-trace/checkpoints/s1/n2/memory',
    })
    // The index copy goes to a folder of its own, which is removed after.
    const cp1 = w.ran('cp', '-R', '/w/crm/.git/index')[0]
    const tmpIndex = cp1?.argv[3] ?? ''
    expect(tmpIndex).toMatch(/^\/home\/u\/\.local\/state\/decision-trace\/tmp\/[0-9a-f-]+\/index$/)
    const add = w.runs.find(r => r.argv.includes('add'))
    expect(add?.argv).toEqual(['git', '-C', '/w/crm', 'add', '--all', '--', '.', ':(exclude).claude/worktrees'])
    expect(add?.init?.env).toEqual({ GIT_INDEX_FILE: tmpIndex })
    expect(w.runs.find(r => r.argv.includes('commit-tree'))?.argv).toEqual([
      'git', '-C', '/w/crm', '-c', 'user.name=decision-trace', '-c', 'user.email=decision-trace@localhost',
      'commit-tree', '--no-gpg-sign', '-m', 'decision-trace checkpoint: session s1, node n2', '-p', 'head1', 'tree1',
    ])
    expect(w.runs.find(r => r.argv.includes('update-ref'))?.argv.slice(-2)).toEqual([`${REF_PREFIX}s1/n2`, 'commit1'])
    expect(w.ran('rm', '-rf').map(r => r.argv[2])).toContain(tmpIndex.replace(/\/index$/, ''))
    // The memory folder is copied whole.
    expect(w.ran('cp', '-R', '/home/u/.claude/projects/-w-crm/memory')).toHaveLength(1)
  })

  test('says what it could not save, and saves the rest', async () => {
    const w = new World()
    w.script = argv => (argv.includes('--show-toplevel') ? fail('fatal: not a git repository') : undefined)
    const cp = await take(w.io, HOME, { repo: '/tmp/x', memory: '', session: 's1', node: 'n1', toolUseId: '', at })
    expect(cp).toEqual({ at, missing: 'chat position unknown: the call had no tool-use id; code not saved: the folder is not a git repository' })
  })

  test('a repo with no commits yet has no index and no parent', async () => {
    const w = repo()
    w.files.delete('/w/crm/.git/index')
    const base = w.script
    w.script = (argv, init) => (argv.join(' ').includes('HEAD^{commit}') ? fail('', 1) : base(argv, init))
    const cp = await take(w.io, HOME, { repo: '/w/crm', memory: '', session: 's1', node: 'n1', toolUseId: 't', at })
    expect(cp.commit).toBe('commit1')
    expect(w.ran('cp')).toHaveLength(0)
    expect(w.runs.find(r => r.argv.includes('commit-tree'))?.argv).not.toContain('-p')
  })

  test('keeps a copy of the tree', async () => {
    const w = new World()
    const path = await saveTreeCopy(w.io, HOME, 's1', 'n1', newTree('s1', at))
    expect(path).toBe('/home/u/.local/state/decision-trace/checkpoints/s1/n1/tree.json')
    expect(w.json(path).session_id).toBe('s1')
  })
})
