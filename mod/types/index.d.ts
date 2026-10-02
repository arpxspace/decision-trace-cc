// The decision-tree mod's contract: a tree as `decision-tree data` prints
// it, and the values the pane draws from (PRD 19).

/** Where an option stands. */
export type State = 'weighing' | 'picked' | 'not_picked' | 'dropped'

/** What was saved when an option was picked, so a branch can start there. */
export type Checkpoint = {
  tool_use_id?: string
  commit?: string
  memory?: string
  missing?: string
}

/** The start node, or one option of a decision. */
export type TreeNode = {
  id: string
  decision?: string
  label: string
  state: State
  reason?: string
  by?: string
  at: string
  drop_reason?: string
  hidden?: boolean
  checkpoint?: Checkpoint
}

/** What is being decided, and its options (node ids). */
export type Decision = {
  id: string
  topic: string
  parent: string
  options: string[]
  at: string
  hidden?: boolean
}

/** Where a branched session came from. */
export type BranchOrigin = {
  name: string
  from_session: string
  from_node: string
  statement: string
  at: string
}

/** One session's tree. */
export type Tree = {
  session_id: string
  folder?: string
  here: string
  nodes: TreeNode[]
  decisions: Decision[]
  branch?: BranchOrigin
}

/** A session that branched from a node. */
export type Stub = { session: string; name: string; decisions: number }

/** What `decision-tree data` prints: the tree is null until the first decision. */
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
    'decision-tree': {
      shown: Shown
      data: Data | null
      error: string
      overlay: Overlay | null
    }
  }
}
