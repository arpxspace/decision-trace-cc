// The rules for changing a tree: every kind of record_decision call, and the
// user's fixes with undo.
import { describe, expect, test } from 'claude-code/testing'

import type { Tree } from '../types'
import type { Call, Result } from '../hooks/tree'
import { decision, fixDelete, fixPick, fixRename, MAX_UNDO, newTree, node, record, ROOT, TreeError, undo, upTo, VERSION } from '../hooks/tree'

const t0 = Date.UTC(2026, 8, 29, 15, 0, 0)
/** t0 plus some minutes. */
const at = (min: number) => new Date(t0 + min * 60_000).toISOString()

/** A tree that can change, so a test reads like a story. */
class Story {
  t: Tree = newTree('s1', at(0))

  /** Runs a call that must work. */
  rec(c: Omit<Call, 'at'> & { at?: string }): Result {
    const r = record(this.t, { at: at(0), ...c })
    this.t = r.tree
    return r.result
  }

  /** Runs a call that must fail with a message containing want, and checks the tree did not change. */
  recErr(c: Omit<Call, 'at'>, want: string): void {
    const before = JSON.stringify(this.t)
    let msg = ''
    try {
      record(this.t, { at: at(0), ...c })
    } catch (err) {
      expect(err instanceof TreeError).toBe(true)
      msg = (err as Error).message
    }
    expect(msg).toContain(want)
    expect(JSON.stringify(this.t)).toBe(before)
  }

  /**
   * The visible tree as indented text. A decision is a line with its id and
   * topic, above its options:
   *   ● picked   ○ not picked   ◌ being weighed   ✗ changed   ◀ you are here
   */
  shape(): string {
    const sym: Record<string, string> = { picked: '●', not_picked: '○', weighing: '◌', dropped: '✗' }
    const lines: string[] = []
    const draw = (id: string, depth: number) => {
      const n = node(this.t, id)
      if (!n) {
        return
      }
      const label = n.id === ROOT && n.label === '' ? 'start' : n.label
      lines.push(`${'  '.repeat(depth)}${sym[n.state]} ${label}${n.id === this.t.here ? ' ◀' : ''}`)
      for (const d of this.t.decisions) {
        if (d.parent === n.id && !d.hidden) {
          lines.push(`${'  '.repeat(depth + 1)}${d.id} ${d.topic}`)
          for (const o of d.options) {
            if (!node(this.t, o)?.hidden) {
              draw(o, depth + 2)
            }
          }
        }
      }
    }
    draw(ROOT, 0)
    return lines.join('\n')
  }
}

const picture = (s: string) => s.trim()

/** The CRM example: d1 which fix → the index (n3); d2 which index → company + date (n5). */
function crm(): Story {
  const s = new Story()
  s.rec({ topic: 'Which fix for slow search?', options: ['Rewrite in Rust', 'add a cache', 'add a database index'], at: at(1) })
  s.rec({ decision_id: 'd1', picked: 'add a database index', reason: 'fixes the query itself', by: 'both', at: at(2) })
  s.rec({ topic: 'Which index?', options: ['on email', 'on company + date'], picked: 'on company + date', reason: 'matches the search', by: 'claude', at: at(3) })
  return s
}

describe('record', () => {
  test('a new tree is just the start', () => {
    const s = new Story()
    expect(s.t.version).toBe(VERSION)
    expect(s.t.session_id).toBe('s1')
    expect(s.shape()).toBe('● start ◀')
  })

  test('options are weighed, then one is picked', () => {
    const s = new Story()
    let r = s.rec({ topic: 'Which fix for slow search?', options: ['Rewrite in Rust', 'add a cache', 'add a database index'], at: at(1) })
    expect(r).toMatchObject({ decisionId: 'd1', here: ROOT, open: ['d1'] })
    r = s.rec({ decision_id: 'd1', picked: 'Add a Database Index', reason: 'fixes the query itself', by: 'user', at: at(2) })
    expect(r).toMatchObject({ here: 'n3', open: [] })
    expect(s.shape()).toBe(picture(`
● start
  d1 Which fix for slow search?
    ○ Rewrite in Rust
    ○ add a cache
    ● add a database index ◀`))
    expect(node(s.t, 'n3')).toMatchObject({ reason: 'fixes the query itself', by: 'user', at: at(2) })
  })

  test('a decision picked at once', () => {
    expect(crm().shape()).toBe(picture(`
● start
  d1 Which fix for slow search?
    ○ Rewrite in Rust
    ○ add a cache
    ● add a database index
      d2 Which index?
        ○ on email
        ● on company + date ◀`))
  })

  // Acceptance scenario 4: "Actually change this to GraphQL." What was
  // decided after REST still stands, so it moves over to GraphQL.
  test('a change happens in place, and can be changed back', () => {
    const s = new Story()
    s.rec({ topic: 'API', options: ['REST'], picked: 'REST', reason: 'simple', by: 'both', at: at(1) })
    s.rec({ topic: 'Auth', options: ['JWT', 'sessions'], picked: 'JWT', reason: 'stateless', by: 'claude', at: at(2) })
    const r = s.rec({ decision_id: 'd1', options: ['GraphQL'], picked: 'GraphQL', reason: 'the client needs flexible queries', by: 'user', at: at(3) })
    expect(r.here).toBe('n2')
    expect(s.shape()).toBe(picture(`
● start
  d1 API
    ✗ REST
    ● GraphQL
      d2 Auth
        ● JWT ◀
        ○ sessions`))
    expect(node(s.t, 'n1')).toMatchObject({ drop_reason: 'the client needs flexible queries', reason: 'simple' })
    s.rec({ decision_id: 'd1', picked: 'REST', reason: 'GraphQL was too much', by: 'user', at: at(4) })
    expect(s.shape()).toBe(picture(`
● start
  d1 API
    ● REST
      d2 Auth
        ● JWT ◀
        ○ sessions
    ✗ GraphQL`))
  })

  test('drop_later sets the later decisions aside under the old pick', () => {
    const s = crm()
    const r = s.rec({ decision_id: 'd1', picked: 'add a cache', reason: 'the index did not help', by: 'both', drop_later: true, at: at(4) })
    expect(r.here).toBe('n2')
    expect(s.shape()).toBe(picture(`
● start
  d1 Which fix for slow search?
    ○ Rewrite in Rust
    ● add a cache ◀
    ✗ add a database index
      d2 Which index?
        ○ on email
        ● on company + date`))
    expect(node(s.t, 'n3')).toMatchObject({ drop_reason: 'the index did not help', reason: 'fixes the query itself' })
  })

  test('after goes back to a node and sets aside what came after it', () => {
    const s = crm()
    const r = s.rec({ topic: 'Which column order?', options: ['date first', 'company first'], after: 'n3', at: at(4) })
    expect(r.here).toBe('n3')
    expect(s.shape()).toBe(picture(`
● start
  d1 Which fix for slow search?
    ○ Rewrite in Rust
    ○ add a cache
    ● add a database index ◀
      d2 Which index?
        ○ on email
        ✗ on company + date
      d3 Which column order?
        ◌ date first
        ◌ company first`))
    s.recErr({ topic: 'Q', options: ['a'], after: 'n2' }, 'not on the live branch')
    s.recErr({ topic: 'Q', options: ['a'], after: 'n99' }, 'there is no node')
  })

  test('open decisions float down to "you are here"', () => {
    const s = new Story()
    s.rec({ topic: 'When to deploy?', options: ['tonight', 'now'], at: at(1) })
    const r = s.rec({ topic: 'Which database?', options: ['Postgres', 'SQLite'], picked: 'Postgres', reason: 'many writers', by: 'user', at: at(2) })
    expect(r).toMatchObject({ open: ['d1'], left: [] })
    expect(s.shape()).toBe(picture(`
● start
  d2 Which database?
    ● Postgres ◀
      d1 When to deploy?
        ◌ tonight
        ◌ now
    ○ SQLite`))
  })

  test('an open decision left behind on a set-aside branch is named', () => {
    const s = new Story()
    s.rec({ topic: 'Q1', options: ['A', 'B'], picked: 'A', reason: 'r', by: 'user', at: at(1) })
    s.rec({ topic: 'Q2', options: ['x', 'y'], at: at(2) })
    const r = s.rec({ decision_id: 'd1', picked: 'B', reason: 'A failed', by: 'user', drop_later: true, at: at(3) })
    expect(r).toMatchObject({ open: [], left: ['d2'] })
    s.rec({ decision_id: 'd2', picked: 'x', reason: 'r', by: 'user', at: at(4) })
    expect(s.shape()).toBe(picture(`
● start
  d1 Q1
    ✗ A
    ● B
      d2 Q2
        ● x ◀
        ○ y`))
  })

  test('new options join as weighed, or as not picked once a pick was made', () => {
    const s = new Story()
    s.rec({ topic: 'Q', options: ['a', 'b'], at: at(1) })
    s.rec({ decision_id: 'd1', options: [' A ', 'c'], at: at(2) })
    expect(s.shape()).toBe(picture(`
● start ◀
  d1 Q
    ◌ a
    ◌ b
    ◌ c`))
    s.rec({ decision_id: 'd1', picked: 'b', reason: 'r', by: 'user', at: at(3) })
    s.rec({ decision_id: 'd1', options: ['d'], at: at(4) })
    expect(s.shape()).toBe(picture(`
● start
  d1 Q
    ○ a
    ● b ◀
    ○ c
    ○ d`))
  })

  test('the result names a new pick, and not the same pick again', () => {
    const s = new Story()
    expect(s.rec({ topic: 'API', options: ['REST', 'GraphQL'], picked: 'REST', reason: 'r', by: 'user' }).picked).toBe('n1')
    expect(s.rec({ decision_id: 'd1', picked: 'GraphQL', reason: 'r', by: 'user' }).picked).toBe('n2')
    expect(s.rec({ decision_id: 'd1', picked: 'GraphQL', reason: 'better reason', by: 'user' }).picked).toBe('')
    expect(node(s.t, 'n2')?.reason).toBe('better reason')
  })

  test('bad calls say what to do instead, and change nothing', () => {
    const s = crm()
    s.recErr({ options: ['a'] }, 'topic is needed')
    s.recErr({ topic: 'Q', options: [' ', ''] }, 'options is needed')
    s.recErr({ topic: 'Q', options: ['a'], picked: 'b', reason: 'r', by: 'user' }, 'not one of the options')
    s.recErr({ topic: 'Q', options: ['a'], picked: 'a', by: 'user' }, 'reason is needed')
    s.recErr({ topic: 'Q', options: ['a'], picked: 'a', reason: 'r', by: 'me' }, 'by is needed')
    s.recErr({ decision_id: 'd9' }, 'there is no decision')
    s.recErr({ decision_id: 'd1', picked: 'zzz', reason: 'r', by: 'user' }, 'not an option of d1')
  })

  test('a set-aside branch cannot be picked again', () => {
    const s = crm()
    s.rec({ decision_id: 'd1', picked: 'add a cache', reason: 'r', by: 'user', drop_later: true })
    s.recErr({ decision_id: 'd1', picked: 'add a database index', reason: 'r', by: 'user' }, 'set aside')
    s.recErr({ decision_id: 'd2', picked: 'on email', reason: 'r', by: 'user' }, 'set aside')
  })

  test('upTo hides what came later, in a copy', () => {
    const s = new Story()
    s.rec({ topic: 'API framework', options: ['FastAPI', 'Flask'], picked: 'FastAPI', reason: 'r', by: 'both' })
    s.rec({ topic: 'Cache', options: ['Redis', 'none'], picked: 'Redis', reason: 'r', by: 'claude' })
    const up = new Story()
    up.t = upTo(s.t, 'n1')
    expect(up.shape()).toBe(picture(`
● start
  d1 API framework
    ● FastAPI ◀
    ○ Flask`))
    expect(decision(up.t, 'd2')?.hidden).toBe(true)
    expect(s.t.here).toBe('n3')
    expect(decision(s.t, 'd2')?.hidden).toBeUndefined()
  })
})

describe('fixes', () => {
  test('a rename locks the node', () => {
    const s = crm()
    s.t = fixRename(s.t, 'n1', '  Rust   rewrite ')
    expect(node(s.t, 'n1')).toMatchObject({ label: 'Rust rewrite', locked: true })
    s.recErr({ decision_id: 'd1', picked: 'Rust rewrite', reason: 'r', by: 'user' }, 'fixed')
    expect(() => fixRename(s.t, 'n1', ' ')).toThrow()
  })

  test("the user's pick is a change in place, and Claude cannot move it", () => {
    const s = crm()
    s.t = fixPick(s.t, 'n2', at(5))
    expect(s.shape()).toBe(picture(`
● start
  d1 Which fix for slow search?
    ○ Rewrite in Rust
    ● add a cache
      d2 Which index?
        ○ on email
        ● on company + date ◀
    ✗ add a database index`))
    expect(node(s.t, 'n2')).toMatchObject({ locked: true, by: 'user' })
    s.recErr({ decision_id: 'd1', picked: 'Rewrite in Rust', reason: 'r', by: 'claude' }, 'fixed')
    s.rec({ topic: 'Cache for how long?', options: ['5 minutes'], picked: '5 minutes', reason: 'r', by: 'claude' })
    // The user can change their own fix, locks and all.
    s.t = fixPick(s.t, 'n1', at(6))
    expect(node(s.t, 'n1')?.state).toBe('picked')
    expect(node(s.t, 'n2')?.state).toBe('dropped')
  })

  test('a deleted node stays deleted when Claude brings it up again', () => {
    const s = crm()
    s.t = fixDelete(s.t, 'n1')
    const r = s.rec({ decision_id: 'd1', options: ['rewrite in rust', 'rewrite in Go'] })
    expect(r.skipped).toEqual(['rewrite in rust'])
    expect(s.shape()).toBe(picture(`
● start
  d1 Which fix for slow search?
    ○ add a cache
    ● add a database index
      d2 Which index?
        ○ on email
        ● on company + date ◀
    ○ rewrite in Go`))
    s.recErr({ decision_id: 'd1', picked: 'Rewrite in Rust', reason: 'r', by: 'user' }, 'deleted')
  })

  test('deleting under "you are here" moves it up; the start cannot be deleted', () => {
    const s = crm()
    s.t = fixDelete(s.t, 'n3')
    expect(s.shape()).toBe(picture(`
● start ◀
  d1 Which fix for slow search?
    ○ Rewrite in Rust
    ○ add a cache`))
    s.recErr({ decision_id: 'd2', picked: 'on email', reason: 'r', by: 'user' }, 'deleted')
    expect(() => fixDelete(s.t, ROOT)).toThrow()
  })

  test('undo takes fixes back, one at a time', () => {
    const s = crm()
    const start = s.shape()
    s.t = fixRename(s.t, 'n1', 'Rust')
    s.t = fixDelete(s.t, 'n3')
    s.t = undo(undo(s.t))
    expect(s.shape()).toBe(start)
    expect(node(s.t, 'n1')?.locked).toBeUndefined()
    expect(node(s.t, 'n3')?.hidden).toBeUndefined()
    expect(() => undo(s.t)).toThrow('nothing to undo')
  })

  test("undo keeps Claude's later work", () => {
    const s = crm()
    s.t = fixPick(s.t, 'n2', at(5)) // d2 moves over to the cache
    s.rec({ topic: 'Cache time', options: ['5 minutes'], picked: '5 minutes', reason: 'r', by: 'claude' })
    s.t = undo(s.t)
    expect(s.shape()).toBe(picture(`
● start
  d1 Which fix for slow search?
    ○ Rewrite in Rust
    ○ add a cache
    ● add a database index
      d2 Which index?
        ○ on email
        ● on company + date
          d3 Cache time
            ● 5 minutes ◀`))
  })

  test('only the last fixes are kept', () => {
    const s = crm()
    for (let i = 0; i < MAX_UNDO + 10; i++) {
      s.t = fixRename(s.t, 'n1', `name ${i}`)
    }
    expect(s.t.fixes?.length).toBe(MAX_UNDO)
  })
})
