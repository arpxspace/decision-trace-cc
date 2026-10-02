// A fake outside world for tests: files in memory, and programs that answer
// from a script and note each run.
import type { On } from 'claude-code'
import { mock } from 'claude-code/testing'

import type { Entry, IO, Places, RunInit, RunResult } from '../hooks/files'

export type Run = { argv: string[]; init?: RunInit }

/** Answers one run, or undefined for "it worked, and printed nothing". */
export type Script = (argv: string[], init?: RunInit) => RunResult | Partial<RunResult> | undefined

export const ok = (stdout = ''): RunResult => ({ exitCode: 0, stdout, stderr: '' })
export const fail = (stderr: string, exitCode = 1): RunResult => ({ exitCode, stdout: '', stderr })

/** Where things are, on a pretend Linux machine. */
export const HOME: Places = {
  windows: false,
  home: '/home/u',
  state: '/home/u/.local/state/decision-trace',
  claude: '/home/u/.claude',
  terminal: '',
}

export class World {
  files = new Map<string, { text: string; mtimeMs: number }>()
  runs: Run[] = []
  clock = 1
  script: Script = () => undefined

  constructor(files: Record<string, string> = {}) {
    for (const [path, text] of Object.entries(files)) {
      this.put(path, text)
    }
  }

  put(path: string, text: string): void {
    this.files.set(path, { text, mtimeMs: this.clock++ })
  }

  text(path: string): string | undefined {
    return this.files.get(path)?.text
  }

  json<T = any>(path: string): T {
    return JSON.parse(this.text(path) ?? 'null') as T
  }

  /** The runs whose first words are these. */
  ran(...start: string[]): Run[] {
    return this.runs.filter(r => start.every((w, i) => r.argv[i] === w))
  }

  io: IO = {
    read: async path => {
      const f = this.files.get(path)
      if (!f) {
        throw new Error(`ENOENT: ${path}`)
      }
      return f.text
    },
    write: async (path, text) => this.put(path, text),
    list: async dir => {
      const seen = new Map<string, Entry>()
      for (const [path, f] of this.files) {
        if (!path.startsWith(dir + '/')) {
          continue
        }
        const [name, ...rest] = path.slice(dir.length + 1).split('/')
        if (name && !seen.has(name)) {
          seen.set(name, { name, kind: rest.length > 0 ? 'dir' : 'file', mtimeMs: rest.length > 0 ? 0 : f.mtimeMs })
        }
      }
      if (seen.size === 0) {
        throw new Error(`ENOENT: ${dir}`)
      }
      return [...seen.values()]
    },
    stat: async path => {
      const f = this.files.get(path)
      if (f) {
        return { kind: 'file', mtimeMs: f.mtimeMs }
      }
      return [...this.files.keys()].some(p => p.startsWith(path + '/')) ? { kind: 'dir', mtimeMs: 0 } : null
    },
    run: async (argv, init) => {
      this.runs.push({ argv: [...argv], init })
      const answer = this.script([...argv], init)
      if (answer === undefined) {
        this.shell(argv)
      }
      return { exitCode: 0, stdout: '', stderr: '', ...answer }
    },
  }

  /** cp -R and rm -rf act on the files in memory, as they would on disk. */
  private shell(argv: string[]): void {
    const [cmd, flag, a, b] = argv
    const under = (root: string) => [...this.files.keys()].filter(p => p === root || p.startsWith(root + '/'))
    if (cmd === 'cp' && flag === '-R' && a && b) {
      for (const p of under(a)) {
        this.put(b + p.slice(a.length), this.text(p) ?? '')
      }
    } else if (cmd === 'rm' && flag === '-rf' && a) {
      for (const p of under(a)) {
        this.files.delete(p)
      }
    }
  }
}

/** Answers the engine's file, program, and environment calls from a World, for tests that go through the plugin's hooks. */
export function wire(on: On, w: World, env: Record<string, string> = { HOME: '/home/u' }): void {
  const deny = (err: unknown) => ({ deny: err instanceof Error ? err.message : String(err) })
  on('fs.read', async (_$, e) => w.io.read(e.path).then(value => ({ value }), deny))
  on('fs.write', async (_$, e) => {
    await w.io.write(e.path, e.text)
    return { value: undefined }
  })
  on('fs.list', async (_$, e) =>
    w.io.list(e.path).then(es => ({ value: es.map(x => ({ ...x, size: 0, isLink: false })) }), deny),
  )
  on('fs.stat', async (_$, e) => {
    const s = await w.io.stat(e.path)
    return s ? { value: { ...s, size: 0, isLink: false } } : { deny: `ENOENT: ${e.path}` }
  })
  on('process.run', async (_$, e) => ({
    value: { ...(await w.io.run([...e.argv], e.init)), isStdoutTruncated: false, isStderrTruncated: false },
  }))
  mock.env(on, env)
}
