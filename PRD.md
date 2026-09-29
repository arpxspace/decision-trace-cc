# decision-tree — PRD

Status: draft 3, 29 Sep 2026. Waiting for Amir to confirm. No code until then.

## 1. What it is

A second screen, like claude-sidebar, for one Claude Code session. It draws the
big choices you and Claude make while you talk. It draws them as a tree that
looks like a git graph.

- A choice that got picked is a solid dot. The talk goes on from there.
- Choices you talked about but did not pick stay on screen in grey.
- Choices still being weighed show as hollow dots.

```
●  start: CRM search is slow
├─○  rewrite in Rust              not picked
├─○  add a cache                  not picked
│
●  add a database index
├─○  index on email               not picked
│
●  index on company + date   ◀ you are here
├─◌  run it tonight               being weighed
└─◌  run it now                   being weighed
```

Claude builds the tree itself. It gets a new tool, `record_decision`. It calls
the tool when options come up and again when one gets picked. The screen
lives in its own iTerm2 split, next to tmux, like claude-sidebar does.

## 2. Goals

1. **Watch live.** At a glance, see where the talk is right now and what is still open.
2. **Look back later.** Open an old session's tree and see why each choice was picked.
3. **No extra work for you.** You talk as normal. Claude logs the choices.
4. **Instant.** The tree changes the moment Claude calls the tool.
5. **Fix mistakes.** When a node is wrong, you can fix it in the split.

## 3. Not goals (version 1)

- One tree across many sessions. Each session gets its own tree.
- Small choices, like a function name or which file to read first.
- A second AI reading the chat. (That was the first plan. See section 15.)
- Trees for sessions from before the tool was installed.
- A web page or a picture file. The split is the only screen.

## 4. Words used in this document

| Word | What it means here |
|------|--------------------|
| **decision** | A choice about the project, with 2 or more options. "Postgres or SQLite?" |
| **option** | One answer to a decision. "Postgres." |
| **picked** `●` | The option that won. The talk goes on from it. |
| **not picked** `○` | An option that was talked about and lost. Shown grey. |
| **being weighed** `◌` | An option on the table. Nobody has picked yet. |
| **dropped** `✗` | A picked option that was given up later. Its branch ends there. |
| **you are here** `◀` | The newest picked option on the live branch. |
| **the tool** | `record_decision`, the tool Claude calls to change the tree. |
| **MCP server** | A small program that gives Claude Code new tools. Ours gives Claude `record_decision` and `show_decision_tree`. Claude Code starts one copy for each session. |
| **tree file** | The file on disk that holds one session's tree. |
| **split** | The iTerm2 pane that shows the tree. |

## 5. How it works

```
  You and Claude talk in tmux
            │
            │  options come up, or one gets picked
            ▼
  ┌────────────────────────────┐
  │ Claude calls                │   record_decision(question, options,
  │ record_decision             │                   picked, reason, by)
  └─────────────┬──────────────┘
                ▼
  ┌────────────────────────────┐
  │ MCP server                  │   checks the call, writes the tree file,
  │ `decision-tree mcp`         │   tells Claude the node id and "you are here"
  └─────────────┬──────────────┘
                ▼
  ┌────────────────────────────┐
  │ tree file for this session  │   ~/.local/state/decision-tree/<session-id>.json
  └─────────────┬──────────────┘
                ▼
  ┌────────────────────────────┐
  │ the split (TUI)             │   sees the file change, redraws
  └────────────────────────────┘
```

## 6. What counts as a decision

These rules go in the MCP server's instructions, so Claude reads them in
every session.

**Counts** — choices at the level of the project:
- A tool or technology: "Postgres, not SQLite."
- An approach: "add an index, not a cache."
- Scope: "leave login out of version 1."
- A design direction: "oldest on top, not newest on top."
- Going back: "the index did not help, let's try the cache."

**Does not count** — small choices made along the way:
- Names: "call it `load_offers`."
- Order of work: "read the config first."
- Which command or tool Claude runs.

**Many moments, one node.** Five turns about databases, plus one answer to a
question with buttons (the AskUserQuestion tool), become one decision:
"which database?" Claude logs it once when the options are laid out. It logs
it again when one gets picked. It does not log each turn.

**When unsure, leave it out.** A tree with a missing node is easier to fix
than a tree full of small stuff.

## 7. The tools

### 7.1 `record_decision`

One tool does it all. What it does depends on what Claude fills in.

| Field | Needed? | What it is |
|-------|---------|------------|
| `question` | yes | "Which fix for slow CRM search?" |
| `options` | yes | All options talked about, including the winner. |
| `picked` | no | The winning option. Leave it empty while still weighing. |
| `reason` | only with `picked` | One line: why this one won. |
| `by` | only with `picked` | Who made the call: `user`, `claude`, or `both`. |
| `decision_id` | no | Id of a decision already in the tree, to update it. |
| `after` | no | Id of the node this decision grows from. Default: "you are here". |

What each kind of call does:

```
  No decision_id, no picked      → new decision, all options "being weighed"
  No decision_id, with picked    → new decision, picked at once
  decision_id, with picked       → that decision gets its pick;
                                   the other options turn grey
  decision_id of an OLD decision,
    picked = a grey option       → going back: the old branch ends in ✗,
                                   a new branch starts from this option
  after = an older node          → the branch past that node ends in ✗,
                                   the new decision grows from the older node
```

Rules the code settled (step 2, `internal/tree`):

- **Choices on the table stay at the bottom.** A decision with no pick yet
  moves down to "you are here" each time the talk moves on. When it gets
  picked, it grows from "you are here". So the tree reads in the order things
  were decided, not the order they came up.
- **A given-up branch stays given up.** An open decision under a dropped
  branch is no longer listed as open. Picking a `✗` option again is refused:
  "start a new decision". Going back to a decision that sits inside a dropped
  branch is refused the same way.
- On an update (`decision_id`), `question` is ignored. New options join as
  "being weighed", or as "not picked" if the decision was already made.
- Option labels match without caring about capital letters or extra spaces.
- A bad call changes nothing. Its error message says what to do instead.

What the tool sends back to Claude, in one short line:

```
Saved as d4. You are here: add a database index. Still open: none.
```

**Locked nodes.** If Amir fixed a node by hand (section 9.3), a call that would
change it fails with this message: `The user fixed "add a cache" (n2) by hand.
Leave it as it is.` Claude sees the message and moves on.

### 7.2 `show_decision_tree`

It takes no fields. It returns the tree as short text with ids. Claude uses it
when it has lost track of the ids. That can happen after the chat gets
shortened to save space (Claude Code calls this "compacting").

### 7.3 Stopping Claude from forgetting

Claude can forget to call the tool. Version 1 has one guard, and it needs
nothing from you:

**Server instructions.** The rules from section 6 ride along with the MCP
server. Claude Code shows them in every session, even when the tool itself
is not loaded yet. (Tested: `docs/findings.md`, part 2.)

Two details make the guard stronger:
- The server is added with `"alwaysLoad": true`. Then Claude sees the full
  tool description from the start, not just the tool's name.
- Claude Code cuts server instructions and each tool description at 2,048
  characters. So the rules from section 6 must fit in 2,048 characters. The
  details of how to call the tool (7.1) go in the tool's own description,
  which has its own 2,048.

Amir does not want to type anything to catch misses. So there is no `/decide`
command and no reminder line. The week of real use (section 14, step 4) shows
whether this one guard is enough. If it is not, section 12.1 lists what to add.

## 8. The tree file

One file per session: `~/.local/state/decision-tree/<session-id>.json`.

A node (an option, or the start node):

```json
{
  "id": "n3",
  "decision": "d1",
  "label": "add a database index",
  "state": "dropped",
  "reason": "fixes the slow query itself; a cache only helps repeat searches",
  "by": "both",
  "at": "2026-09-29T15:40:02Z",
  "drop_reason": "the index did not fix the slow query",
  "locked": false,
  "hidden": false
}
```

A decision, which holds its options and the node it grows from:

```json
{
  "id": "d1",
  "question": "Which fix for slow CRM search?",
  "parent": "n0",
  "options": ["n1", "n2", "n3"],
  "at": "2026-09-29T15:01:40Z"
}
```

The file also holds `here` (the "you are here" node id) and `fixes` (Amir's
last 50 fixes, so `u` can undo them).

- `state` is one of: `weighing`, `picked`, `not_picked`, `dropped`.
- A dropped node keeps its `reason` (why it was picked) and gets a
  `drop_reason` (why it was given up).
- The top node, "start", gets its label from the session title in the chat
  file. If there is no title yet, it uses your first message, cut short.
- Two programs write this file: the MCP server (Claude's calls) and the split
  (your fixes). Each one locks the file, reads it fresh, changes it, and saves
  it. So neither one wipes out the other's change. A file that cannot be read
  is left alone, never overwritten.
- A node you delete stays in the file, hidden and locked. So Claude cannot add
  it back.

**Which tree a call goes to.** At every tool call, the MCP server reads
`~/.claude/sessions/<its parent pid>.json` to get the session id. It does
not use the `CLAUDE_CODE_SESSION_ID` variable, because that goes stale after
`/clear` (`docs/findings.md`, parts 1 and 4). So after `/clear`, new calls
go to a new, empty tree.

## 9. The split

### 9.1 Layout

Oldest at the top. It reads like a story. The split scrolls down on its own
so "you are here" stays in view.

When you go back to a grey option, it becomes a new branch, like a git branch
from an old commit:

```
●  start: CRM search is slow
├─○  rewrite in Rust                 not picked
├─╮
│ ●  add a database index            picked first
│ ●  index on company + date
│ ✗  did not help, dropped
│
●  add a cache                       was grey, then picked
●  cache results for 5 minutes   ◀ you are here
```

The bottom three lines show the node under the cursor:

```
──────────────────────────────────────────────
which fix for slow CRM search? → add a cache · by both · 15:40
the index did not fix the slow query; repeat searches are most of the load
```

### 9.2 Which session it shows

It follows your tmux pane. When you move to a tmux pane running Claude, the
split shows that session's tree. It finds the session the same way
claude-sidebar does (`~/.claude/sessions/<pid>.json`, field `tmux`).

If the pane you move to is not running Claude, the split keeps the last tree
and dims the title. The top line always says which session and folder it shows.

### 9.3 Keys

| Key | Does |
|-----|------|
| `↑` `↓` / `j` `k` | Move the cursor between nodes |
| `p` | Mark the option under the cursor as picked (fix) |
| `r` | Rename the node (fix) |
| `d` | Delete the node and everything under it (fix) |
| `u` | Undo your last fix |
| `o` | Open the tree of a past session (a list, newest first) |
| `f` | Go back to following the tmux pane |
| `q` | Quit |

Every fix locks the node (section 8).

## 10. Setup

- One Go program, `decision-tree`, with these commands:

| Command | Who runs it | Does |
|---------|-------------|------|
| `mcp` | Claude Code, once per session | The MCP server with the two tools |
| `start` | You | Opens the split |
| `toggle` | The tmux key | Opens or closes the split |
| `print <session>` | You | Prints a tree as plain text (for testing) |

- Same screen libraries as claude-sidebar: Bubble Tea and Lip Gloss.
  The MCP server uses the official MCP library for Go.
- Reuse claude-sidebar's code for tmux, the session files, and the iTerm2 split.
  Copy it for now, and share it later if both tools grow.
- Install to `~/.local/bin/decision-tree`.
- Hook up to Claude Code, for all projects:
  - Add the MCP server, with its tools always loaded:
    `claude mcp add-json --scope user decision-tree '{"command":"/Users/amirpanahi/.local/bin/decision-tree","args":["mcp"],"alwaysLoad":true}'`
  - In `~/.claude/settings.json`: allow both tools, so Claude does not ask
    you first each time.
- A tmux key (next to claude-sidebar's `prefix + a`) runs `decision-tree toggle`.
  The exact key is picked at build time.
- Config file `~/.config/decision-tree/config.toml`: a list of folders where
  the tools are turned off.

## 11. Tests

- **Tree logic:** every kind of call in 7.1, locks, deleted nodes, going back,
  two writers at once. Plain Go tests. `make test`.
- **Tool checks:** bad calls get clear errors. Examples: `picked` is not one
  of the `options`, or a `decision_id` does not exist.
- **Drawing:** saved trees drawn to text and compared with saved pictures.
- **End to end:** a fake MCP call, a fake session file, a private tmux server
  (the same method as claude-sidebar's `make e2e`). Never touches real tmux.
- **Real use:** one week of normal work (section 14, step 4). This is the test
  that matters most.

## 12. Risks

### 12.1 Claude forgets to log

This is the main risk of this plan. Version 1 has only the server instructions
(7.3). If the week of real use shows too many misses, add these, in order.
None of them needs Amir to type anything:

1. **A one-line reminder with each message.** A UserPromptSubmit hook (it runs
   each time Amir sends a message) adds about 30 words for Claude: "log
   project decisions; you are here: …". Over 100 messages that is about
   4,000 tokens of Claude's context.
2. **A second AI that reads the chat.** The first plan (section 15). It runs
   after each Claude reply and catches what Claude missed.

### 12.2 Claude logs too much

The opposite problem: small choices get logged. Fix it by tightening the
rules in section 6. The week of real use shows which way it leans.

### 12.3 The tool may not be loaded

Solved. Claude Code sometimes holds back MCP tools until they are needed.
The server instructions show anyway, and `"alwaysLoad": true` stops the
holding back (`docs/findings.md`, part 2).

### 12.4 Formats nobody promised to keep

The session files and the chat file layout are not documented as stable.
A Claude Code update could break them. The split then shows the error on its
top line instead of a wrong tree.

## 13. Check before building

Done on 29 Sep 2026. The full write-up is in `docs/findings.md`.

| # | Question | Answer | What it means for the build |
|---|----------|--------|-----------------------------|
| 1 | Which session is calling? | The session file of the server's parent process | Read it at every call (section 8) |
| 2 | Do the rules reach Claude if the tool is held back? | Yes. Plus `alwaysLoad` stops the holding back. 2,048-character limit. | The one guard works (7.3) |
| 3 | Resume | Same session id, same chat file | The old tree just continues |
| 4 | `/clear` | New session id, same MCP server | New, empty tree after `/clear` |
| 5 | Sub-agents | Same server; their calls look like main-chat calls | Allowed in version 1; the rules say main chat only |
| 6 | Active pane | `tmux list-clients` gives each client's pane | Reuse claude-sidebar's tmux code |

## 14. Build order

1. ~~Research (section 13). Write up the findings.~~ Done 29 Sep 2026.
2. ~~Tree file + tree logic, with tests.~~ Done 29 Sep 2026: `internal/tree`, `internal/store`.
3. MCP server + `print`. No split yet.
4. **One week of real use.** Amir works as normal. At the end, pick 5
   sessions. For each one, Claude reads the chat file and lists the decisions
   it finds there. Compare that list with the tree from `print`:
   - on the list but not in the tree = a miss (Claude forgot)
   - in the tree but not on the list = an extra (too small to log)

   Many misses → add the guards in 12.1. Many extras → tighten section 6.
5. The split: drawing, following the tmux pane, the detail lines.
6. Fixes and locks.
7. Past trees (`o`), install, the tmux key.

Step 4 is the real test. If Claude does not log good decisions, the screen
does not matter.

## 15. Choices already made

| Date | Choice | Why |
|------|--------|-----|
| 29 Sep 2026 | Tree is for watching live and looking back | Amir wants both |
| 29 Sep 2026 | Only project-level decisions | Small choices are noise |
| 29 Sep 2026 | Git-graph look, oldest on top | Reads like a story |
| 29 Sep 2026 | Own iTerm2 split, follows the tmux pane | Same setup as claude-sidebar |
| 29 Sep 2026 | One tree per session | Keep it simple |
| 29 Sep 2026 | Fixes in the split, fixed nodes locked | Mistakes will happen |
| 29 Sep 2026 | **Claude logs decisions with a tool** | Replaces the first plan (see below) |
| 29 Sep 2026 | Only one guard: server instructions | Amir does not want to type `/decide`. The reminder line waits until the week of use shows it is needed |
| 29 Sep 2026 | `/clear` starts a new, empty tree | `/clear` usually means a new topic (default; Amir can change it) |
| 29 Sep 2026 | Sub-agent calls are allowed | The server cannot tell them apart, and they are rare (default; Amir can change it) |

**The first plan** used a second AI to read the chat after every Claude reply.
It ran through Codex CLI with `gpt-5.6-luna`. A test worked: 10 seconds and
about 13,000 tokens per reply. Amir switched to having Claude log decisions
itself. With that switch, no chat text goes to OpenAI and nothing is used on
the ChatGPT plan. The tree also updates at once. The cost is that Claude can
forget, which section 7.3 guards against.
