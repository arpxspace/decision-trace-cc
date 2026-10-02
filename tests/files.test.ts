// Trees on disk, and where things are, on each system.
import { describe, expect, test } from 'claude-code/testing'

import { branchesOf, copy, find, join, keepPrivate, list, load, places, remove, terminalOf, treePath, update } from '../hooks/files'
import { newTree, record } from '../hooks/tree'
import { fail, HOME, World } from './fake'

const at = '2026-09-29T15:00:00.000Z'

describe('places', () => {
  test('macOS and Linux: ~/.local/state, or $XDG_STATE_HOME', () => {
    expect(places({ HOME: '/home/u' })).toEqual(HOME)
    expect(places({ HOME: '/home/u', XDG_STATE_HOME: '/x', CLAUDE_CONFIG_DIR: '/c' })).toMatchObject({ state: '/x/decision-trace', claude: '/c' })
  })

  test('Windows: %LOCALAPPDATA%, with Windows paths', () => {
    const p = places({ OS: 'Windows_NT', USERPROFILE: 'C:\\Users\\ana', LOCALAPPDATA: 'C:\\Users\\ana\\AppData\\Local' })
    expect(p).toEqual({
      windows: true,
      home: 'C:\\Users\\ana',
      state: 'C:\\Users\\ana\\AppData\\Local\\decision-trace',
      claude: 'C:\\Users\\ana\\.claude',
      terminal: '',
    })
    expect(treePath(p, 's1')).toBe('C:\\Users\\ana\\AppData\\Local\\decision-trace\\s1.json')
    expect(join('/a/', '/b', 'c')).toBe('/a/b/c')
  })
})

test('finds the terminal from the variables it sets', () => {
  expect(terminalOf({ TERM_PROGRAM: 'iTerm.app' })).toBe('iterm')
  expect(terminalOf({ TERM_PROGRAM: 'Apple_Terminal' })).toBe('apple-terminal')
  expect(terminalOf({ WT_SESSION: 'x' })).toBe('windows-terminal')
  expect(terminalOf({ TERM_PROGRAM: 'WezTerm' })).toBe('wezterm')
  expect(terminalOf({ KITTY_WINDOW_ID: '1' })).toBe('kitty')
  expect(terminalOf({ GNOME_TERMINAL_SCREEN: 'x' })).toBe('gnome-terminal')
  expect(terminalOf({ KONSOLE_VERSION: '230800' })).toBe('konsole')
  expect(terminalOf({ TERM_PROGRAM: 'ghostty' })).toBe('')
})

describe('trees on disk', () => {
  test('update reads fresh, saves, and keeps two changes apart', async () => {
    const w = new World()
    const add = (topic: string) => update(w.io, HOME, 's1', t => record(t, { topic, options: ['a'], picked: 'a', reason: 'r', by: 'user', at }).tree)
    await Promise.all([add('Q1'), add('Q2'), add('Q3')])
    const t = await load(w.io, HOME, 's1')
    expect(t?.decisions.map(d => d.topic)).toEqual(['Q1', 'Q2', 'Q3'])
    expect(w.text(treePath(HOME, 's1'))?.endsWith('\n')).toBe(true)
  })

  test('a failed change saves nothing, and a broken file is left alone', async () => {
    const w = new World()
    await expect(update(w.io, HOME, 's1', () => { throw new Error('no') })).rejects.toThrow('no')
    expect(w.text(treePath(HOME, 's1'))).toBeUndefined()
    w.put(treePath(HOME, 's2'), '{not json')
    await expect(update(w.io, HOME, 's2', t => t)).rejects.toThrow('s2.json')
    expect(w.text(treePath(HOME, 's2'))).toBe('{not json')
  })

  test('a bad session id never becomes a path', () => {
    expect(() => treePath(HOME, '../etc/passwd')).toThrow('bad session id')
  })

  test('list is newest first; find takes the start of an id', async () => {
    const w = new World()
    for (const id of ['aaaa1111', 'aaaa2222', 'bbbb3333']) {
      w.put(treePath(HOME, id), JSON.stringify(newTree(id, at)))
    }
    w.put(join(HOME.state, 'notes.txt'), '')
    expect((await list(w.io, HOME)).map(t => t.session)).toEqual(['bbbb3333', 'aaaa2222', 'aaaa1111'])
    expect(await find(w.io, HOME, 'bbbb')).toBe('bbbb3333')
    await expect(find(w.io, HOME, 'aaaa')).rejects.toThrow('matches 2 sessions')
    await expect(find(w.io, HOME, 'cccc')).rejects.toThrow('no tree for session')
  })

  test('branchesOf finds the sessions that branched from one, by node', async () => {
    const w = new World()
    w.put(treePath(HOME, 'parent'), JSON.stringify(newTree('parent', at)))
    const branch = (id: string, node: string, made: string) => {
      const t = record(newTree(id, at), { topic: 'Q', options: ['a'], picked: 'a', reason: 'r', by: 'user', at }).tree
      t.branch = { name: `try-${id}`, from_session: 'parent', from_node: node, from_decision: 'd1', inherited: 0, statement: 's', cut_message: 'u', at: made }
      w.put(treePath(HOME, id), JSON.stringify(t))
    }
    branch('b2', 'n1', '2026-09-29T16:00:00Z')
    branch('b1', 'n1', '2026-09-29T15:00:00Z')
    expect(await branchesOf(w.io, HOME, 'parent')).toEqual({
      n1: [
        { session: 'b1', name: 'try-b1', decisions: 1 },
        { session: 'b2', name: 'try-b2', decisions: 1 },
      ],
    })
  })
})

describe('copying any kind of file', () => {
  test('macOS and Linux use mkdir and cp', async () => {
    const w = new World({ '/r/.env': 'SECRET=1' })
    await copy(w.io, HOME, '/r/.env', '/w/.env')
    expect(w.runs.map(r => r.argv)).toEqual([['mkdir', '-p', '/w'], ['cp', '-R', '/r/.env', '/w/.env']])
    await remove(w.io, HOME, '/r/.env')
    expect(w.runs.at(-1)?.argv).toEqual(['rm', '-rf', '/r/.env'])
  })

  test('Windows uses robocopy, which says it worked with any code under 8', async () => {
    const win = places({ OS: 'Windows_NT', USERPROFILE: 'C:\\Users\\ana', LOCALAPPDATA: 'C:\\L' })
    const w = new World({ 'C:\\r\\.env': 'SECRET=1', 'C:\\r\\node_modules/x': '' })
    w.script = () => ({ exitCode: 1 }) // 1: files were copied
    await copy(w.io, win, 'C:\\r\\.env', 'C:\\w\\.env')
    expect(w.runs.at(-1)?.argv.slice(0, 4)).toEqual(['robocopy', 'C:\\r', 'C:\\w', '.env'])
    await copy(w.io, win, 'C:\\r\\node_modules', 'C:\\w\\node_modules')
    expect(w.runs.at(-1)?.argv.slice(0, 4)).toEqual(['robocopy', 'C:\\r\\node_modules', 'C:\\w\\node_modules', '/E'])
    w.script = () => fail('ERROR 5: Access is denied.', 16)
    await expect(copy(w.io, win, 'C:\\r\\.env', 'C:\\w\\.env')).rejects.toThrow('robocopy could not copy')
  })

  test('Windows removes with PowerShell, which takes the path as plain text', async () => {
    const win = places({ OS: 'Windows_NT', USERPROFILE: 'C:\\Users\\R&D', LOCALAPPDATA: 'C:\\Users\\R&D\\AppData\\Local' })
    const path = "C:\\Users\\R&D\\AppData\\Local\\decision-trace\\tmp\\it's"
    const w = new World({ [path + '/index']: '' })
    await remove(w.io, win, path)
    expect(w.runs.at(-1)?.argv).toEqual([
      'powershell', '-NoProfile', '-NonInteractive', '-Command',
      "Remove-Item -LiteralPath 'C:\\Users\\R&D\\AppData\\Local\\decision-trace\\tmp\\it''s' -Recurse -Force",
    ])
    expect(w.ran('cmd')).toHaveLength(0)
  })
})

describe('privacy', () => {
  test('the state folder is made readable by this user alone, once', async () => {
    const w = new World()
    await keepPrivate(w.io, HOME)
    await keepPrivate(w.io, HOME)
    expect(w.runs.map(r => r.argv)).toEqual([['mkdir', '-p', HOME.state], ['chmod', '700', HOME.state]])
    // On Windows, %LOCALAPPDATA% is already the user's own.
    const win = new World()
    await keepPrivate(win.io, places({ OS: 'Windows_NT', USERPROFILE: 'C:\\Users\\ana', LOCALAPPDATA: 'C:\\L' }))
    expect(win.runs).toHaveLength(0)
  })
})
