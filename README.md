# decision-trace

Every decision you and Claude Code settle on, drawn as a tree beside the
chat. Branch from any one to try another path.

```
 the chat                                  │ Decision                        ✕
                                           │
 > Use PostgreSQL rather than SQLite.      │    ●  Start
                                           │    │
 ● Got it: PostgreSQL, not SQLite.         │    ├─×  SQLite
                                           │    ●  Database: PostgreSQL
 > Let's add Redis for caching.            │    │
 ● Redis or no cache? I'd skip it for now. │    ├─×  Redis
                                           │  › ●  Cache: none  ◀
 > Agreed.                                 │ ──────────────────────────────
                                           │  Cache: none
                                           │  Agreed by you and Claude · 16:21
                                           │  Why: keep the stack small
```

decision-trace is a Claude Code plugin. While you talk, Claude logs each
project-level decision once it is made: the option that won, the options
that lost, who made the call, and why. A pane beside the chat draws them as
a git-style tree.

- `●` a pick. The talk went on from here.
- `×` an option that was talked about and lost.
- `↺` a pick that was changed later.
- `◀` you are here.

Every pick is also a checkpoint. Pick an old decision, press `b`, and a new
Claude session starts from that moment, in its own folder, with the chat,
the code, and Claude's memory as they were then. The original session is
never changed.

## Install

You need Claude Code 2.1.287 or newer. Plugins with code of their own, like
this one, are early access in Claude Code.

In Claude Code, run:

```
/plugin marketplace add arpxspace/decision-trace-cc
/plugin install decision-trace@decision-trace
```

Then start a new session. The pane opens by itself once the terminal is at
least 144 columns wide. Run `/decision-trace` to open it at any width.

Depending on your permission mode, Claude Code may ask before Claude uses the
two tools. To allow them always, add them to `permissions.allow` in
`~/.claude/settings.json`:

```json
"mcp__decision-trace__record_decision",
"mcp__decision-trace__show_decision_tree"
```

Optional:

- **git** in the folder you work in. Without it, a checkpoint has no code,
  and a branch shares the original folder.
- **tmux**. When Claude runs in tmux, a branch opens in a new tmux window.
  Without tmux, it opens in a new tab of your terminal (see
  [Terminals and systems](#terminals-and-systems)).

## Use it

Talk as you normally do. You don't type anything to log a decision.

| Command | Does |
|---------|------|
| `/decision-trace` | Opens the pane on this session's tree |
| `/decision-trace <session>` | Opens another session's tree. The start of its id is enough. |

**Click the tree once** to give it the keys. `esc` gives them back to the
prompt.

| Key | Does |
|-----|------|
| `j` `k` or `↓` `↑` | Move between nodes |
| click | Put the cursor on a row |
| `g` `G` | Jump to the top or bottom |
| `.` | Jump to "you are here" |
| `space` | Fold or unfold what grows from a node |
| `enter` | On a pick: where it came from, and what a branch from it would start with. On a branch (`⎇`): open its tree |
| `b` | Branch from the pick under the cursor. It shows the plan and asks first: `y` makes it, `n` cancels |
| `B` | The same, then jump to the new tmux window (a new tab comes to the front by itself) |
| `p` | In a branch: open the tree it came from |
| `f` | Back to this session's tree |
| `q` | Close the screen on top, or the pane |

Long text wraps. The details panel at the bottom shows the whole reason for
the node under the cursor.

To stop the pane from opening at the start of every session, turn off
decision-trace's `openAtStart` in `/config`.

## How the mod works

Claude Code can load plugins that carry code: a TypeScript module of hooks
that runs inside Claude Code itself. Claude Code calls these hooks at fixed
moments, such as when a session starts, when Claude calls a tool, or when a
row is saved to the chat. A hook can answer the moment, or change what
happens. decision-trace is one such module, with no program of its own to
install.

```
              Claude Code
 ┌───────────────────────────────────────────────────────────┐
 │                                                           │
 │  Claude ── record_decision ──▶ hooks/register.tsx         │
 │                                  │                        │
 │  the chat ── a row is saved ───▶ │  the hooks module      │
 │                                  │  (the one file that    │
 │  the pane ◀── draws ──────────── │   talks to Claude Code)│
 │     │                            │                        │
 │     └── keys ──────────────────▶ │                        │
 └──────────────────────────────────┼────────────────────────┘
                                    │ files and programs, through
                                    │ Claude Code's own tools
                                    ▼
      the tree file      git          claude -p        tmux
      (one per session)  (snapshots,  (forks the chat  (opens a
                          worktrees)   for a branch)    branch window)
```

### The hooks

| Moment | What the hook does |
|--------|--------------------|
| The session starts | Registers the two tools and `/decision-trace`. Starts a timer that keeps the pane fresh. Opens the pane. |
| The system prompt is built | Adds the rules for when to log a decision (below) |
| The tool list is built | Keeps both tools in Claude's list, instead of behind tool search |
| Claude calls `record_decision` | Checks the call, changes the tree, and saves it. A new pick also gets a checkpoint. It answers Claude in one line: `Saved as d1. You are here: PostgreSQL (n1).` |
| Claude calls `show_decision_tree` | Answers with the tree as text, with ids, for when Claude loses track of them |
| A chat row is saved | When the row answers a `record_decision` call, saves the row's id with that pick's checkpoint. A branch cuts the chat there. |
| The pane is drawn | Draws the tree with `hooks/view.ts` |
| A key is pressed in the pane | `hooks/view.ts` moves the cursor itself. For anything that reads or changes files, it asks the hooks module. |

### What counts as a decision

These rules go in Claude's system prompt in every session:

- Log a decision only once it is made. Options that are only being discussed
  are not a decision.
- Made means one of three things. The user states it (`by: user`). Claude
  recommends and the user agrees (`by: both`). Or the user leaves it to
  Claude, and Claude chooses (`by: claude`).
- A suggestion is not a decision. If Claude suggests Redis and you say "No,
  use Postgres", Redis shows as rejected (`×`), not as changed (`↺`).
- Only project-level choices count: a tool, an approach, scope, a design
  direction. Names, the order of work, and which command to run do not.
- Log quietly. Claude does not mention the tree unless you ask.

Changing a decision ("Actually, use GraphQL") happens in place. The old pick
is marked `↺`, and what was decided after it stays. When those later
decisions depended on the old pick, Claude sets them aside instead, in a
lane of their own.

### What is saved, and where

| What | Where |
|------|-------|
| One tree per session | macOS and Linux: `~/.local/state/decision-trace/<session>.json` (or `$XDG_STATE_HOME/decision-trace`). Windows: `%LOCALAPPDATA%\decision-trace` |
| The code at each pick | A git commit of the working folder, uncommitted and new files included. It is kept under a hidden ref, `refs/decision-trace/checkpoints/<session>/<node>`. It is built in a temporary index, so your branch, staging area, stash, and log never change. Files git ignores are left out. |
| Claude's memory at each pick | A copy of the project's memory folder, in the state folder under `checkpoints/` |
| The tree at each pick | A copy, so a branch starts with the tree as it was then |
| Where the pick came from | The message you typed before it, what Claude said just before, and the id of the chat row that answers the call |

### Privacy

- **What leaves your computer.** decision-trace makes no network calls of
  its own. The one exception is making a branch: the fork step resumes the
  chat with `claude -p`, which sends the chat, up to the decision, to
  Anthropic, as any resumed session does. It is one short Haiku reply.
- **Who can read the files.** On macOS and Linux, the state folder is made
  readable by you alone (`chmod 700`) before anything is written to it. On
  Windows, `%LOCALAPPDATA%` is already your own.
- **Your prompts are kept.** A tree saves the message you typed before each
  decision, and what Claude said just before it. They stay in the tree file
  after you delete the chat from Claude Code.
- **Snapshots keep files git does not ignore, untracked ones included.** If
  you never added a secret file (like `.env`) to `.gitignore`, a snapshot
  saves a copy of it in `.git`, and the copy stays after you delete the
  file. Normal `git push` does not send the hidden refs, but
  `git push --mirror` does. Keep secrets in `.gitignore`.
- **Branches copy what `.worktreeinclude` names**, like `.env`, into the
  branch's folder in the state folder.

To remove everything decision-trace saved for a repo, and then the trees,
checkpoints, and branch folders (macOS and Linux):

```sh
git for-each-ref --format='%(refname)' refs/decision-trace | xargs -n 1 git update-ref -d
git worktree list                        # remove any branch worktrees first: git worktree remove <path>
rm -rf ~/.local/state/decision-trace
```

### Making a branch

```
 b on a pick ─▶ the plan, and "Create a branch?" ─▶ y
                                                   │
   1. the code     git worktree from the snapshot, in the state folder's
                   worktrees/<project>/<name>. Uncommitted work comes back
                   as uncommitted. Files your .worktreeinclude names
                   (like .env) are copied in.
   2. the chat     claude -p --resume <session> --fork-session
                   --resume-session-at <row>: a new session, cut right after
                   the decision. One short reply from Haiku, with no tools
                   and no hooks.
   3. the memory   the saved copy, in the new folder's own memory folder
   4. the tree     the tree as it was then, marked "branch of <session>"
   5. the window   runs claude --resume <new session>: in a new window of
                   the same tmux session when Claude runs in tmux, else in a
                   new tab of your terminal, else the pane shows the command
```

A branch shows up in the original's tree under the pick it came from, as
`⎇ <name> · <n> decisions`.

### The files

| File | What it is |
|------|------------|
| `hooks/register.tsx` | The hooks module: the tools, the pane, `/decision-trace`, and the actions behind the keys |
| `hooks/tree.ts` | The rules for changing a tree: picks, changes in place, set-aside lanes, and fixes with undo |
| `hooks/view.ts` | The pane's screen: layout, cursor, folding, wrapping, and the details panel |
| `hooks/graph.ts` | Lays a tree out as a git-style graph |
| `hooks/files.ts` | Trees on disk, where things are on each system, and copying any kind of file |
| `hooks/checkpoint.ts` | The git snapshot and the memory copy |
| `hooks/branch.ts` | Planning and making a branch |
| `hooks/chat.ts` | Claude Code's chat files: the cut point, and your message before a decision |
| `hooks/rules.ts`, `hooks/show.ts` | What Claude reads: the rules, the tools, and their answers |

## Terminals and systems

The pane is drawn by Claude Code, so it works in any terminal that Claude
Code runs in. A few things depend on your setup.

- **The keys need the fullscreen layout.** There, the pane sits beside the
  chat, and a click gives it the keys. Without the fullscreen layout, the
  pane sits above the prompt, and Claude Code does not pass clicks to it: you
  can read the tree, but not use its keys. To turn the fullscreen layout on,
  set `"tui": "fullscreen"` in `~/.claude/settings.json`.
- **Inside tmux, turn the mouse on** (`set -g mouse on`). Without it, tmux
  keeps clicks for itself, and the pane never gets the keys.
- **The pane opens by itself only from 144 columns.** Below that, run
  `/decision-trace`.
- **Fonts.** The tree uses box-drawing characters (`│ ├─ ╰─`) and symbols
  (`● × ↺ ◀ ⎇`). Most programming fonts draw them one cell wide. A font that
  draws them wider breaks the lines of the graph.
- **macOS and Linux** are tested. Windows under WSL works as Linux does.
- **Windows without WSL** should work, but has not been tested yet. Files are
  copied with `robocopy`, which ships with Windows.

### Where a branch opens

When Claude runs in tmux, a branch opens in a new window of the same tmux
session. Otherwise it opens in a new tab of the terminal Claude Code runs in:

| Terminal | Opens |
|----------|-------|
| iTerm2 | a new tab in the current window |
| Terminal (macOS) | a new window, since Terminal cannot open a tab from a script |
| Windows Terminal | a new tab, with `wt`. It starts `claude.exe` itself, so it needs the native install of Claude Code. |
| WezTerm | a new tab, with `wezterm cli spawn` |
| kitty | a new tab, with `kitty @ launch`. Turn on `allow_remote_control` in `kitty.conf` first. |
| GNOME Terminal | a new tab |
| Konsole | a new tab |

In any other terminal (Ghostty, the VS Code terminal, and others), the pane
shows the command that starts the branch, ready to paste.

On macOS, the first tab may make macOS ask whether Claude Code may control
iTerm2 or Terminal. Say yes, or the tab cannot open.

## Limits

- Claude can forget to log a decision, or log one that is too small. The
  rules above keep this rare, but not gone.
- Claude Code's API for plugins with code is early access, and may change
  between releases.
- Branching uses Claude Code files and one flag that are not documented: the
  session files in `~/.claude/sessions`, the chat files in
  `~/.claude/projects`, and `--resume-session-at`. A Claude Code update could
  change them.
- Views of every git ref (`git log --all`, `gitk --all`, lazygit's full
  graph) show the checkpoints, as `decision-trace checkpoint: …`. Normal
  `git log`, `git status`, `git branch`, and `git push` do not.
- Checkpoints and branch folders stay until you remove them. There is no
  cleanup command yet; [Privacy](#privacy) shows how to remove them by hand.
- Keys to fix the tree by hand (pick, rename, delete, undo) are planned. The
  rules for them are in `hooks/tree.ts`, but no key calls them yet.

## Develop

```sh
git clone https://github.com/arpxspace/decision-trace-cc
cd decision-trace-cc
claude --plugin-dir .            # a session with the plugin loaded from here
```

In that session, saving a file reloads the plugin.

```sh
claude plugin validate .          # checks the manifest and the hooks module
claude plugin test .              # the tests in tests/
bun test dev/                     # checkpoints and branches against real git
```

Claude Code writes the API's types to `.claude-plugin/types/` when it loads
the plugin. After that, `tsc -p .` type-checks the code (TypeScript 5.4 or
newer).

## License

MIT. See [LICENSE](LICENSE).
