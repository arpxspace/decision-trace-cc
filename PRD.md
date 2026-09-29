# decision-tree — PRD

Status: being built. Steps 1, 2, 3, and 5 are done; the week of real use
(step 4) is running. Last updated 29 Sep 2026, after Amir's acceptance tests
(section 16).

## 1. What it is

A second screen, like claude-sidebar, for one Claude Code session. It draws the
big choices you and Claude make while you talk. It draws them as a tree that
looks like a git graph.

- A choice that got picked is a solid dot `●`. The talk goes on from there.
- Options that were talked about and rejected hang off to the side: `×`.
- A pick that was changed later stays on screen, marked `↺`.

```
●  Todo app
│
├─×  SQLite                     rejected
●  Database: PostgreSQL
│
├─↺  REST                       picked, then changed
●  API: GraphQL
│
●  Auth: JWT  ◀                 you are here
```

Claude builds the tree itself. It gets a new tool, `record_decision`, and
calls it once a decision is made or changed. Nothing is logged while options
are only being discussed. The screen lives in its own iTerm2 split, next to
tmux, like claude-sidebar does.

## 2. Goals

1. **Watch live.** At a glance, see what has been decided so far, and where the talk is now.
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
| **decision** | A choice about the project that was made, with the options that were talked about. |
| **topic** | What a decision is about, as a short statement: "Database". The tree shows "Database: PostgreSQL". |
| **option** | One answer to a decision. "PostgreSQL." |
| **picked** `●` | The option that won. The talk goes on from it. |
| **rejected** `×` | An option that was talked about and lost. Shown grey. (In the code: `not_picked`.) |
| **changed** `↺` | An option that was picked, then changed later. (In the code: `dropped`.) |
| **set aside** | Decisions that depended on a changed pick, moved into their own lane (`drop_later`). |
| **still open** `◌` | An option with no pick yet. Claude no longer logs these (section 6). They only show up in trees saved before 29 Sep 2026. |
| **you are here** `◀` | The newest picked option on the live branch. |
| **the tool** | `record_decision`, the tool Claude calls to change the tree. |
| **MCP server** | A small program that gives Claude Code new tools. Ours gives Claude `record_decision` and `show_decision_tree`. Claude Code starts one copy for each session. |
| **tree file** | The file on disk that holds one session's tree. |
| **split** | The iTerm2 pane that shows the tree. |

## 5. How it works

```
  You and Claude talk in tmux
            │
            │  a decision is made, or changed
            ▼
  ┌────────────────────────────┐
  │ Claude calls                │   record_decision(topic, options,
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
every session. They were rewritten on 29 Sep 2026 to pass Amir's acceptance
tests (section 16).

**Log only once a decision is made.** Nothing is logged while options are
only being discussed. "Redis, Postgres, or an in-memory cache are all
possible" is not a decision (scenario 5). Made means one of these:

| What happened | `by` |
|---------------|------|
| The user states it: "Use PostgreSQL rather than SQLite." | `user` |
| Claude recommends, and the user agrees: "Agreed." | `both` |
| The user leaves it to Claude, and Claude chooses | `claude` |

**A suggestion is not a decision.** Claude says "Let's add Redis." The user
says "No. Keep it simple and use Postgres." That is one decision: Redis
rejected, PostgreSQL picked, by the user. Redis never shows as picked
(scenario 3).

**List the alternatives.** `options` holds every option that was talked
about, so the tree shows what was rejected.

**Changing a decision** ("Actually change this to GraphQL") is a call with
that decision's id and the new pick. It changes in place (section 7.1).

**Counts**: choices at the level of the project:
- A tool or technology: "PostgreSQL, not SQLite."
- An approach: "add an index, not a cache."
- Scope: "leave login out of version 1."
- A design direction: "oldest on top, not newest on top."

**Does not count**: small choices made along the way:
- Names: "call it `load_offers`."
- Order of work: "read the config first."
- Which command or tool Claude runs.

**Many moments, one node.** Five turns about databases, plus one answer to a
question with buttons (the AskUserQuestion tool), become one decision. Claude
logs it once, when one option wins.

**The topic is a statement, not a question**: "Database", "API framework",
"State storage".

**When unsure, leave it out.** A tree with a missing node is easier to fix
than a tree full of small stuff.

**Log quietly.** Claude does not mention the tree in its replies unless
asked. (Without this rule, the live test showed Claude saying "I added this
to the decision tree" in every reply.)

The exact wording Claude sees is `Instructions` in `internal/server/server.go`
(1,642 characters; the limit is 2,048).

Before 29 Sep 2026 the rules also had Claude log options while they were
still being weighed (`◌`). Scenario 5 ruled that out.

## 7. The tools

### 7.1 `record_decision`

One tool does it all. Every call is a decision that was made, or a change to
one, so `picked`, `reason`, and `by` are always needed. A call without a pick
is refused, so a discussion can never turn into a node by mistake.

| Field | Needed? | What it is |
|-------|---------|------------|
| `topic` | for a new decision | What was decided, as a short statement: "Database". |
| `options` | for a new decision | Every option talked about, including the winner. |
| `picked` | always | The winning option. |
| `reason` | always | One line: why this one won. |
| `by` | always | Who made the call: `user`, `claude`, or `both`. |
| `decision_id` | to change a decision | Id of a decision already in the tree. |
| `drop_later` | no | When changing a pick: also set aside the decisions made after it, because they depended on the old pick. |
| `after` | no | For a new decision: the node it grows from. Everything past that node is set aside. |

What each kind of call does:

```
  topic + options + picked      → new decision; the pick carries the line on,
                                  the other options are rejected (×)
  decision_id + a new picked    → a change in place: the old pick is marked ↺,
                                  and the decisions made after it stay; they
                                  move over to the new pick
    ... + drop_later            → the old pick and the decisions after it are
                                  set aside in their own lane, folded
  after = an older node         → everything past that node is set aside, and
                                  the new decision grows from that node
```

A change in place, and the same change with `drop_later`:

```
  in place (the default)          drop_later
  ●  Todo app                      ●  Todo app
  │                                │
  ├─↺  REST                        ├─╮
  ●  API: GraphQL                  │ ↺  API: REST ▸ 2 more
  │                                ●  API: GraphQL  ◀
  ├─×  sessions
  ●  Auth: JWT  ◀
```

Use `drop_later` when the later decisions only made sense with the old pick.
Example: after switching from an index to a cache, "which columns to index"
no longer matters.

Rules the code settled (`internal/tree`):

- **Changes happen in place by default.** This came from scenario 4. The
  first version set aside everything decided after a changed pick. Say you
  picked REST, then made five more decisions, then switched to GraphQL: all
  five would have been set aside.
- **A pick can be changed back.** REST → GraphQL → REST works.
- **A set-aside branch stays set aside.** Picking the head of a set-aside
  branch again is refused ("start a new decision"). So is changing a decision
  inside one.
- **Picks come in the order they were made.** A new decision grows from
  "you are here", so the main line reads in the order things were decided.
- On a change (`decision_id`), `topic` is ignored. New options join as
  rejected.
- Option labels match without caring about capital letters or extra spaces.
- A bad call changes nothing. Its error message says what to do instead.

What the tool sends back to Claude, in one short line:

```
Saved as d4. You are here: GraphQL (n3). Still open: none.
```

When `drop_later` leaves an unanswered question in the set-aside branch, the
reply names it, so Claude answers it instead of asking it again (found in
the live test, `docs/findings.md` part 7). Since picks are now required, this
can only happen in trees saved before 29 Sep 2026:

```
... Left behind on the dropped branch, still unanswered: d2 "Front end".
If one still matters, answer it with its decision_id (add new options if
needed) and it moves here. Do not log it again as a new decision.
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
  "topic": "Speed fix",
  "parent": "n0",
  "options": ["n1", "n2", "n3"],
  "at": "2026-09-29T15:01:40Z"
}
```

Trees saved before 29 Sep 2026 call the topic `"question"`. They still load.

The file also holds `here` (the "you are here" node id) and `fixes` (Amir's
last 50 fixes, so `u` can undo them).

- `state` is one of: `weighing` (still open), `picked`, `not_picked`
  (rejected), `dropped` (changed).
- A changed node keeps its `reason` (why it was picked) and gets a
  `drop_reason` (why it was changed).
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

Chosen by Amir on 29 Sep 2026, after seeing the plain indented text of
`print` and finding it hard to read. Built in `internal/graph` (the layout)
and `internal/ui` (the screen). Run it with `decision-tree view`.

- **A git graph.** The main line runs down the left of the graph. Options
  that were not picked hang off it as stubs. The stub forks from the node the
  decision grows from, and the pick carries the line on, the same way git
  draws a branch.
- **Lines are statements, not questions.** A pick reads `Database:
  PostgreSQL`: the decision's topic, then the pick. Stubs show just their
  label: `× SQLite`, `↺ REST`.
- **Symbols match Amir's acceptance tests:** `●` picked, `×` rejected, `↺`
  changed later.
- **Fits the pane.** The graph is centered left to right, oldest at the
  top, flowing down. Nothing wraps. Room for `◀` is kept on every line, so
  the graph does not shift sideways when "you are here" moves.
- **Too wide? Scroll sideways.** When the graph is wider than the pane, it
  starts at the left edge instead of the middle. `←`/`→` scroll it, and a
  cut end shows "…". The details panel always has the whole text, wrapped.
- **A set-aside branch** (`drop_later`) gets its own lane (`├─╮`) and starts
  folded (`▸ 2 more`).
- **Smaller font:** a program cannot change it. In iTerm2, click the view's
  pane and press Cmd −. That changes only that pane.

```
 todo-app · 4 decisions · following

        ●  Todo app
        │
        ├─×  SQLite
        ●  Database: PostgreSQL
        │
        ●  API framework: FastAPI
        │
        ├─↺  REST
        ●  API: GraphQL
        │
        ├─×  Redis
      › ●  State storage: PostgreSQL  ◀
 ──────────────────────────────────────────────
  State storage: PostgreSQL
  Picked by you · 16:21
  Why: keep the architecture simple; no extra
  service to run
 j/k move · space fold · . here · q quit
```

The **details panel** is the bottom 4 lines. It always shows the node under
the cursor. Long text wraps, so the whole reason can be read:

| Node | Line 1 | Line 2 | Lines 3–4 |
|------|--------|--------|-----------|
| picked | the statement | Picked by you / Picked by Claude / Agreed by you and Claude · 16:21 | Why: … |
| rejected `×` | the statement | Rejected. PostgreSQL was picked. | Why PostgreSQL: … |
| changed `↺` | the statement | Changed to GraphQL · 16:40 | Why: … |
| still open `◌` | the statement | Still open. Nothing picked yet. | Other options: … |
| start | the session title | folder · session id | 3 decisions · started 16:02 |

**Live updates, no flicker.** The view never clears the screen. Four times a
second it checks the tree file's time, and it reads the file again only when
it changed. Bubble Tea skips any frame that is the same as the last one.
Measured on 29 Sep 2026: 0 bytes written in 3 idle seconds; one change wrote
one frame, in place.

### 9.2 Which session it shows

It follows your tmux pane. When you move to a tmux pane running Claude, the
split shows that session's tree. It finds the session the same way
claude-sidebar does (`~/.claude/sessions/<pid>.json`, field `tmux`).

If the pane you move to is not running Claude, the split keeps the last tree
and says "this pane is not Claude". The top line always says which folder it
shows, and whether it is following or pinned. `decision-tree view <session>`
pins one session instead; `f` goes back to following.

It asks tmux once a second: `list-clients` gives each client's pane, and the
client used last wins. Then it finds the Claude session whose session file
names that pane (skipping crashed Claudes whose files were left behind).

### 9.3 Keys

Built now:

| Key | Does |
|-----|------|
| `j` `k` / `↓` `↑` | Move the cursor between nodes |
| `g` `G` | Jump to the top or bottom |
| `space` / `enter` | Fold or unfold what grows from the node |
| `←` `→` / `h` `l` | Scroll sideways, when the graph is wider than the pane |
| `0` | Scroll back to the left edge |
| `.` | Jump to "you are here". The cursor then rides along as new decisions come in. |
| `f` | Go back to following the tmux pane |
| `q` / `esc` | Quit |

Still to build (steps 6 and 7):

| Key | Does |
|-----|------|
| `p` | Mark the option under the cursor as picked (fix) |
| `r` | Rename the node (fix) |
| `d` | Delete the node and everything under it (fix) |
| `u` | Undo your last fix |
| `o` | Open the tree of a past session (a list, newest first) |

Every fix locks the node (section 8).

## 10. Setup

- One Go program, `decision-tree`, with these commands:

| Command | Who runs it | Does |
|---------|-------------|------|
| `mcp` | Claude Code, once per session | The MCP server with the two tools |
| `start` | You | Opens the split |
| `toggle` | The tmux key | Opens or closes the split |
| `view [session]` | You | The live view (section 9), in the current terminal. No session = follow the tmux pane in use. |
| `print [session]` | You | Prints a tree as the same git graph, without colors, then a "Why" list. No session = the newest tree. The first few characters of the id are enough, like a git hash. |
| `list` | You | Lists saved trees, newest first: when, session, folder, how many decisions, start label |

- Same screen libraries as claude-sidebar: Bubble Tea and Lip Gloss.
  The MCP server uses the official MCP library for Go
  (`github.com/modelcontextprotocol/go-sdk`, v1.8.0).
- Each tool also carries `_meta: {"anthropic/alwaysLoad": true}`, so it
  stays loaded even if the server's settings forget `alwaysLoad`.
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
- **Acceptance tests:** Amir's five scenarios (section 16), as Go tests
  (`internal/server/server_test.go`, `TestScenario1…5`) and as live runs in
  real Claude sessions.
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
3. ~~MCP server + `print`. No split yet.~~ Done 29 Sep 2026, with `list` and a
   live test in real Claude (`docs/findings.md`, part 7).
4. **One week of real use.** Amir works as normal. At the end, pick 5
   sessions. For each one, Claude reads the chat file and lists the decisions
   it finds there. Compare that list with the tree from `print`:
   - on the list but not in the tree = a miss (Claude forgot)
   - in the tree but not on the list = an extra (too small to log)

   Many misses → add the guards in 12.1. Many extras → tighten section 6.
5. ~~The split: drawing, following the tmux pane, the detail lines.~~ Done
   29 Sep 2026 as `decision-tree view`, pulled ahead of step 4 at Amir's
   request (the week of use keeps running). Not done yet: opening it as an
   iTerm2 split by itself (`start`/`toggle`, step 7).
   On 29 Sep 2026 Amir's acceptance tests (section 16) changed the rules:
   log only once made, change in place, and the symbols `×` and `↺`. Also
   added: sideways scrolling and a wrapping details panel.
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
| 29 Sep 2026 | Git graph, fitted to the pane, details panel at the bottom | Amir found the indented text of `print` hard to read |
| 29 Sep 2026 | Lines are statements (`topic: pick`), not questions | Amir: "Database used: SQLite" reads better |
| 29 Sep 2026 | Log only once a decision is made; a call needs a pick | Acceptance scenario 5 |
| 29 Sep 2026 | Changing a decision happens in place; `drop_later` sets later ones aside | Acceptance scenario 4; the old way lost later decisions |
| 29 Sep 2026 | Symbols `×` rejected, `↺` changed | Amir's acceptance scenarios |
| 29 Sep 2026 | Scroll sideways instead of cutting long text | Amir asked to read the full text |

**The first plan** used a second AI to read the chat after every Claude reply.
It ran through Codex CLI with `gpt-5.6-luna`. A test worked: 10 seconds and
about 13,000 tokens per reply. Amir switched to having Claude log decisions
itself. With that switch, no chat text goes to OpenAI and nothing is used on
the ChatGPT plan. The tree also updates at once. The cost is that Claude can
forget, which section 7.3 guards against.

## 16. Acceptance tests

Given by Amir on 29 Sep 2026. Each one is a Go test, and each passed a live
run in a real Claude session on the same day (`docs/findings.md`, part 8).
The pictures are how `decision-tree print` draws the result.

**Scenario 1: simple decision.** User: "Use PostgreSQL rather than SQLite."

```
●  Todo app
│
├─×  SQLite
●  Database: PostgreSQL  ◀
```

**Scenario 2: Claude's recommendation accepted.** Claude: "I think FastAPI is
the better choice here." User: "Agreed." Logged with `by: both`, which the
details panel shows as "Agreed by you and Claude".

```
●  Todo app
│
●  API framework: FastAPI  ◀
```

**Scenario 3: recommendation rejected.** Claude: "Let's add Redis." User:
"No. Keep the architecture simple and use Postgres." Redis is rejected (`×`),
not changed (`↺`), because a suggestion was never a decision.

```
●  Todo app
│
├─×  Redis
●  State storage: PostgreSQL  ◀
```

**Scenario 4: decision reversal.** Earlier: REST. Later, user: "Actually
change this to GraphQL."

```
●  Todo app
│
├─↺  REST
●  API: GraphQL  ◀
```

**Scenario 5: discussion without a decision.** Claude: "Redis, Postgres, or
an in-memory cache are all possible." No node. The tool refuses a call with
no pick, and in the live run Claude made no call at all.

