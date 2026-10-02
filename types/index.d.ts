// decision-trace's contract: a session's tree as it is saved on disk, and
// the values the pane draws from.

/** Where an option stands. */
export type State = 'weighing' | 'picked' | 'not_picked' | 'dropped'

/** Who made a call. */
export type By = 'user' | 'claude' | 'both'

/** What was saved when an option was picked, so a branch can start there. */
export type Checkpoint = {
  /** The record_decision call's id in the chat. */
  tool_use_id?: string
  /** The chat row to resume a branch at: the tool's answer to that call. */
  cut?: string
  /** The message the user typed before the decision, and which one it was (1 = the first). */
  prompt?: string
  prompt_number?: number
  /** What Claude said last before logging the decision. */
  before?: string
  /** The git repo's top folder, the snapshot of the working folder, and the hidden ref that keeps it. */
  repo?: string
  commit?: string
  ref?: string
  /** A copy of Claude's memory for the project, and of the whole tree, at that moment. */
  memory?: string
  tree?: string
  at?: string
  /** What could not be saved, in plain words. */
  missing?: string
}

/** The start node, or one option of a decision. */
export type TreeNode = {
  id: string
  /** The decision this option belongs to; absent for the start node. */
  decision?: string
  label: string
  state: State
  /** Why it was picked, and who picked it. */
  reason?: string
  by?: string
  /** When it was added, or last changed state. */
  at: string
  /** Why a pick was changed. */
  drop_reason?: string
  /** The user fixed it by hand, so Claude's calls must not change it. */
  locked?: boolean
  /** The user deleted it. It stays in the file, so Claude cannot add it back. */
  hidden?: boolean
  checkpoint?: Checkpoint
}

/** What is being decided, and its options (node ids), growing from one node. */
export type Decision = {
  id: string
  topic: string
  parent: string
  options: string[]
  at: string
  hidden?: boolean
}

/** One of the user's fixes, kept so it can be undone: what it changed, as it was before. */
export type Change = {
  what: string
  here: string
  here_after: string
  nodes?: TreeNode[]
  decisions?: Decision[]
}

/** Where a branched session came from. */
export type BranchOrigin = {
  name: string
  from_session: string
  from_node: string
  from_decision: string
  /** Decisions it started with, copied from the original. */
  inherited: number
  statement: string
  cut_message: string
  commit?: string
  repo?: string
  /** The branch's own folder; absent when it shares the original folder. */
  worktree?: string
  at: string
  missing?: string
}

/** One session's tree. Nothing is ever removed, only hidden, so ids keep their meaning. */
export type Tree = {
  version: number
  session_id: string
  folder?: string
  /** "You are here": the newest pick on the live branch. */
  here: string
  nodes: TreeNode[]
  decisions: Decision[]
  fixes?: Change[]
  branch?: BranchOrigin
}

/** A session that branched from a node. */
export type Stub = { session: string; name: string; decisions: number }

/** What the pane loads for one session: the tree is null until the first decision. */
export type Data = {
  session: string
  path: string
  tree: Tree | null
  branches: Record<string, Stub[]>
}

/** A screen shown over the graph. */
export type Overlay = {
  kind: 'source' | 'confirm' | 'working' | 'notice'
  title: string
  body: string[]
  hint: string
  /** For a source screen: the pick it is about. */
  node?: string
  /** For a confirm: the branch to make once the person says yes. */
  plan?: { session: string; node: string; focus: boolean }
}

/** Another session's tree the pane shows instead of this one's. */
export type Shown = { session: string; goTo?: { node: string; n: number } } | null

/** What the pane's view draws. */
export type ViewProps = {
  data: Data | null
  /** True while the pane shows this session's own tree. */
  isOwn: boolean
  error: string
  overlay: Overlay | null
  /** Put the cursor on this node; n changes with each new ask. */
  goTo: { node: string; n: number } | null
  /** The pane's body, for the first drawing, before the view is laid out. */
  columns: number
  rows: number
}

declare module 'claude-code' {
  interface PluginState {
    'decision-trace': {
      shown: Shown
      data: Data | null
      error: string
      overlay: Overlay | null
    }
  }
}
