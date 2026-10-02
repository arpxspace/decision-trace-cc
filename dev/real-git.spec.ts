// The checkpoint and branch code against real git and real files, which a
// plugin test cannot reach. Run with: bun test dev/
import { expect, test } from 'bun:test'
import { spawnSync } from 'node:child_process'
import { mkdir, mkdtemp, readdir, readFile, stat, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'

import { create, prepare } from '../hooks/branch'
import { take } from '../hooks/checkpoint'
import type { IO, Places } from '../hooks/files'
import { save } from '../hooks/files'
import { newTree, record } from '../hooks/tree'

process.env.GIT_CONFIG_GLOBAL = '/dev/null'
process.env.GIT_CONFIG_NOSYSTEM = '1'

/** The real outside world, as the mod's own tools give it. */
const real: IO = {
  read: p => readFile(p, 'utf8'),
  write: async (p, text) => {
    await mkdir(dirname(p), { recursive: true })
    await writeFile(p, text)
  },
  list: async dir =>
    (await readdir(dir, { withFileTypes: true })).map(e => ({ name: e.name, kind: e.isDirectory() ? 'dir' : e.isFile() ? 'file' : 'other', mtimeMs: 0 })),
  stat: async p => {
    try {
      const s = await stat(p)
      return { kind: s.isDirectory() ? 'dir' : s.isFile() ? 'file' : 'other', mtimeMs: s.mtimeMs }
    } catch {
      return null
    }
  },
  run: async (argv, init) => {
    const r = spawnSync(argv[0] as string, argv.slice(1), { cwd: init?.cwd, env: { ...process.env, ...init?.env }, input: init?.stdin, encoding: 'utf8' })
    if (r.error) {
      throw r.error
    }
    return { exitCode: r.status ?? 1, stdout: r.stdout, stderr: r.stderr }
  },
}

/** A scratch home with a repo at work/crm that has uncommitted work. */
async function scratch() {
  const root = await mkdtemp(join(tmpdir(), 'decision-trace-'))
  const p: Places = { windows: false, home: root, state: join(root, 'state'), claude: join(root, 'claude') }
  const repo = join(root, 'work', 'crm')
  const git = (...args: string[]) => {
    const r = spawnSync('git', ['-C', repo, '-c', 'user.name=t', '-c', 'user.email=t@t', ...args], { encoding: 'utf8' })
    if (r.status !== 0) {
      throw new Error(`git ${args.join(' ')}: ${r.stderr}`)
    }
    return r.stdout.trim()
  }
  await mkdir(join(repo, 'src'), { recursive: true })
  git('init', '-q')
  await writeFile(join(repo, 'app.txt'), 'v1\n')
  await writeFile(join(repo, '.gitignore'), '.env\n')
  git('add', '.')
  git('commit', '-qm', 'init')
  await writeFile(join(repo, 'app.txt'), 'v2, not committed\n')
  await writeFile(join(repo, 'new.txt'), 'new file\n')
  await writeFile(join(repo, 'staged.txt'), 'staged\n')
  git('add', 'staged.txt')
  await writeFile(join(repo, '.env'), 'SECRET=1\n')
  return { root, p, repo, git }
}

test('a checkpoint keeps uncommitted work and touches nothing', async () => {
  const { p, repo, git } = await scratch()
  const before = { status: git('status', '--porcelain'), head: git('rev-parse', 'HEAD'), branch: git('branch', '--show-current') }
  const memory = join(p.claude, 'projects', '-w-crm', 'memory')
  await real.write(join(memory, 'MEMORY.md'), '- likes short answers\n')

  // From a subfolder, as Claude often works.
  const cp = await take(real, p, { repo: join(repo, 'src'), memory, session: 's1', node: 'n1', toolUseId: 'toolu_1', at: '' })
  expect(cp.missing).toBeUndefined()
  expect(cp.repo?.endsWith('crm')).toBe(true)
  expect(git('show', `${cp.commit}:app.txt`)).toBe('v2, not committed')
  expect(git('show', `${cp.commit}:new.txt`)).toBe('new file')
  expect(git('show', `${cp.commit}:staged.txt`)).toBe('staged')
  expect(spawnSync('git', ['-C', repo, 'cat-file', '-e', `${cp.commit}:.env`]).status).not.toBe(0) // ignored files stay out
  expect(git('rev-parse', cp.ref as string)).toBe(cp.commit as string)
  expect({ status: git('status', '--porcelain'), head: git('rev-parse', 'HEAD'), branch: git('branch', '--show-current') }).toEqual(before)
  expect(await readFile(join(cp.memory as string, 'MEMORY.md'), 'utf8')).toBe('- likes short answers\n')
  // The temporary index is gone again.
  expect(await readdir(join(p.state, 'tmp'))).toEqual([])
})

test('signing settings and a repo with no commits do not get in the way', async () => {
  const { p, repo, git } = await scratch()
  git('config', 'commit.gpgsign', 'true')
  expect((await take(real, p, { repo, memory: '', session: 's1', node: 'n1', toolUseId: 't', at: '' })).commit).toBeTruthy()

  const empty = join(p.home, 'empty')
  await mkdir(empty)
  spawnSync('git', ['-C', empty, 'init', '-q'])
  await writeFile(join(empty, 'a.txt'), 'a\n')
  expect((await take(real, p, { repo: empty, memory: '', session: 's1', node: 'n2', toolUseId: 't', at: '' })).commit).toBeTruthy()

  const plain = await take(real, p, { repo: p.home, memory: '', session: 's1', node: 'n3', toolUseId: 't', at: '' })
  expect(plain.missing).toBe('code not saved: the folder is not a git repository')
})

test('a branch gets the code as it was, with old work uncommitted again', async () => {
  const { p, repo, git } = await scratch()
  await writeFile(join(repo, '.worktreeinclude'), '.env\n')
  let t = newTree('s1', '')
  t.folder = repo
  t = record(t, { topic: 'API framework', options: ['FastAPI'], picked: 'FastAPI', reason: 'r', by: 'both', at: '' }).tree
  const cp = await take(real, p, { repo, memory: '', session: 's1', node: 'n1', toolUseId: 'toolu_1', at: '' })
  t.nodes[1]!.checkpoint = { ...cp, cut: 'row-7', prompt: 'Use FastAPI', prompt_number: 1 }
  await save(real, p, t)
  // Later work in the original, after the decision.
  await writeFile(join(repo, 'app.txt'), 'v3, later\n')

  // The chat fork and tmux are faked; git and the files are real.
  const io: IO = {
    ...real,
    run: async (argv, init) => {
      if (argv[0] === 'claude') {
        const id = argv[argv.indexOf('--session-id') + 1]
        await real.write(join(p.claude, 'projects', '-branch', `${id}.jsonl`), '')
        return { exitCode: 0, stdout: 'Branch ready.', stderr: '' }
      }
      if (argv[0] === 'tmux') {
        return { exitCode: 1, stdout: '', stderr: 'no server running' }
      }
      return real.run(argv, init)
    },
  }
  const plan = await prepare(io, p, 's1', 'n1')
  const made = await create(io, p, plan, false)
  expect(made.copied).toEqual(['.env'])
  expect(await readFile(join(plan.worktree, 'app.txt'), 'utf8')).toBe('v2, not committed\n')
  expect(await readFile(join(plan.worktree, 'new.txt'), 'utf8')).toBe('new file\n')
  expect(await readFile(join(plan.worktree, '.env'), 'utf8')).toBe('SECRET=1\n')
  const status = spawnSync('git', ['-C', plan.worktree, 'status', '--porcelain'], { encoding: 'utf8' }).stdout
  expect(status).toContain(' M app.txt')
  expect(status).toContain('?? new.txt')
  // The original is untouched.
  expect(await readFile(join(repo, 'app.txt'), 'utf8')).toBe('v3, later\n')
  expect(made.command).toContain(`'claude' '--resume' '${plan.session}'`)
})
