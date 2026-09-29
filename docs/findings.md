# What Claude Code does with our MCP server

PRD section 13 asked for this research before any real code. It was done on
29 Sep 2026 against Claude Code 2.1.284 on macOS.

How it was tested: a throwaway MCP server (a "probe") logged everything
Claude Code sent it. A real Claude session ran in a private tmux server
(`tmux -L dtprobe -f /dev/null`), so Amir's own tmux was never touched.
The probe ran twice at once: `probe` with normal settings, and `probe2` with
`"alwaysLoad": true`. Some answers also come from reading the Claude Code
program itself (`~/.local/share/claude/versions/2.1.284`).

## The short answer

| # | Question | Answer |
|---|----------|--------|
| 1 | Which session is calling? | Read `~/.claude/sessions/<parent pid>.json` **on every call**. Do not trust the `CLAUDE_CODE_SESSION_ID` variable. It goes stale after `/clear`. |
| 2 | Held-back tools | Server instructions always show, even when the tool is held back. `"alwaysLoad": true` stops the holding back. Both cut off at 2,048 characters. |
| 3 | Resume | Same session id, same chat file. New MCP server programs start, with the right id. |
| 4 | `/clear` | New session id. The **same** MCP server keeps running. |
| 5 | Sub-agents | Their calls reach the same MCP server and look exactly like main-chat calls. |
| 6 | Active pane | `tmux list-clients -F '#{client_activity} #{pane_id}'`. Take the client with the newest activity. |

## 1. Which session is calling?

Claude Code starts one copy of each MCP server per session. It starts the
server directly, with no shell in between. So the server's parent process
is the `claude` process, and `~/.claude/sessions/<parent pid>.json` holds
the session id.

Claude Code also hands the server these variables at start (seen in the log):

```
CLAUDE_CODE_SESSION_ID=7ef62275-19f6-46a0-855a-9db00156c24e
CLAUDE_PROJECT_DIR=/Users/amirpanahi/Documents/projects/decision-tree-cc
CLAUDECODE=1
TMUX_PANE=%0
```

But the variable is set once, when the server starts. It does not change
after `/clear` (see 4). The session file does. So the server reads the
session file at every tool call. It uses the variable only if the file is
missing.

Each tool call also carries the id of that one tool use:

```json
{"_meta": {"claudecode/toolUseId": "toolu_01CXS66xBW27jzNxzXCvbh44", "progressToken": 2},
 "arguments": {"note": "main"}, "name": "probe_ping"}
```

## 2. Held-back tools and server instructions

What the test session said about its own context:

```
probe  (normal)           tool: only the name at first. It had to search to see the description.
probe2 (alwaysLoad: true) tool: full description from the start.
both                      server instructions: shown in full, from the start.
```

This matches the code. Server instructions go to Claude as their own message
("# MCP Server Instructions"). They are separate from the tool list. So they
show even when the tool is held back.

`alwaysLoad` is a field in the server's settings:

```json
{"command": "~/.local/bin/decision-tree", "args": ["mcp"], "alwaysLoad": true}
```

A single tool can ask for the same thing with `_meta: {"anthropic/alwaysLoad": true}`
in its listing.

**Size limit.** Server instructions and each tool description are cut at
2,048 characters. Longer text ends with "… [truncated]". The setting
`CLAUDE_CODE_MAX_MCP_DESCRIPTION_LENGTH` changes the limit. Our plan: keep
the rules under 2,048 characters, and do not depend on that setting.

## 3. Resume

`claude --resume 8533417c-…` kept the same session id and wrote to the same
chat file (`8533417c-….jsonl`). New MCP server programs started (new pids),
and their variable had the right id. So a resumed session finds its old tree.

## 4. `/clear`

```
                 MCP server pid   variable session id   session file id
before /clear    76697            7ef62275…             7ef62275…
after  /clear    76697            7ef62275…  (stale)    8533417c…  (right)
```

`/clear` starts a new session id and a new chat file. It does not restart
the MCP server. This is why finding 1 says: read the session file at every
call.

## 5. Sub-agents

A sub-agent called `probe_ping`. The call went to the same probe program
(pid 76697). Its request looked the same as a main-chat call: a tool-use id,
nothing else. The server cannot tell who called from the request alone.

One way to tell later, if needed: sub-agent chats are saved in their own
files, so the server could look up the tool-use id. Not worth it for
version 1. Sub-agents rarely make project decisions.

## 6. The active tmux pane, from outside tmux

One call gives the pane each tmux client is showing:

```
tmux list-clients -F '#{client_tty} #{client_activity} #{pane_id}'
/dev/ttys002 1790692370 %23
```

With more than one client, take the one with the newest `client_activity`
(the last key press or output). claude-sidebar already reads these fields in
`internal/tmux/tmux.go`. Then match the pane id to the `tmux` field in the
session files (`"probe:@0.%0"`: the part after `.` is the pane id).

## 7. Live test of the real tool (build step 3)

On 29 Sep 2026 the real `decision-tree mcp` ran in a real Claude session
(Opus 5.5, private tmux server, trees saved to a scratch folder). The prompts
never mentioned the tool. Three turns about a habit-tracker app:

1. "SQLite or Postgres? Lay out the options."
2. "Go with SQLite. Next: server-rendered HTML or React? Also, call the main
   table habit_logs."
3. "Change of plan: host on Vercel, so switch to Postgres. Front end:
   server-rendered HTML plus htmx."

**Run 1** found two problems:

- Going back left the open front-end question (d2) under the dropped SQLite
  branch. The tool's reply said "Still open: none", so Claude thought d2 was
  gone and logged the same question again as d4. The tree had a duplicate.
- Claude told the user about the tree in every reply ("I added this choice
  to the decision tree…").

Fixes: the reply now names questions left behind ("Left behind on the
dropped branch, still unanswered: d2 … answer it with its decision_id … Do
not log it again"), and the rules say "Log quietly".

**Run 2**, same three turns, after the fixes:

```
● SQLite vs Postgres for habit tracker
  Habit tracker: which database?
    ✗ SQLite — dropped: Hosting on Vercel wipes the disk; use Neon free plan
    ● Postgres — Hosting on Vercel wipes the disk; use Neon free plan (user)
      Habit tracker: front end style?
        ○ Server-rendered HTML
        ○ React single-page app
        ● Server-rendered HTML + htmx ◀ — Less work for one user; htmx gives instant row updates (user)
```

- Every choice was logged without being asked: options first, then the pick.
- The table name `habit_logs` was not logged, which is right.
- After going back, Claude answered d2 by its id and added the new option.
  No duplicate.
- Replies mentioned the tree 0 times.
- One difference between runs: run 1 also logged "where to host? → Vercel";
  run 2 did not. Both are fair calls. The week of real use will show how
  much this kind of thing varies.

## 8. Live run of Amir's acceptance tests

On 29 Sep 2026, after the rules changed to "log only once a decision is
made" and "change in place" (PRD sections 6, 7.1, 16), each of Amir's five
scenarios ran in its own real Claude session (Opus 5.5, private tmux server,
trees saved to scratch folders). Every prompt began: "Separate from this
repo: I'm planning a small todo web app. Don't write code or touch any
files. Answer in one short sentence."

| # | What was typed | Calls Claude made | Tree |
|---|----------------|-------------------|------|
| 1 | "Use PostgreSQL rather than SQLite." | 1: Database → PostgreSQL, by user | `× SQLite`, `● Database: PostgreSQL` |
| 2 | "Flask or FastAPI…? Give me your recommendation." then "Agreed." | 1, only after "Agreed": API framework → FastAPI, by both | `× Flask`, `● …API framework: FastAPI` |
| 3 | "I'm thinking of adding Redis… Pitch it to me." then "No. Keep the architecture simple and use Postgres." | 1, only after the "No": Session state storage → PostgreSQL, by user | `× Redis`, `× Signed cookie`, `● …: PostgreSQL` |
| 4 | "For its API we'll use REST." then "Actually change this to GraphQL." | 2: API style → REST, by user; then d1 → GraphQL, by user | `↺ REST`, `● …API style: GraphQL` |
| 5 | "What could I use for caching? Just list the options, don't pick one." | 0 | no tree |

What this shows:

- In scenarios 2 and 3, Claude logged nothing at the recommendation. It
  logged once, after the user answered. So Redis shows as rejected (`×`),
  not as changed (`↺`).
- Claude listed other options that came up in its own reply (Flask in 2,
  "signed cookie" in 3) as rejected. That follows the rule "list the
  alternatives that were talked about".
- No reply mentioned the tree. (One line matched a search for "logged", but
  it was about users staying "logged in".)
- Topics came out a little long ("Todo app API framework"), because every
  prompt said "todo app". Worth watching during the week of real use.

## Side effects of the test

- `git init` made this folder its own git project. Claude Code then asked
  again whether to trust the folder. The test answered "yes".
- The tests left short chats in this folder's history (`7ef62275…`,
  `8533417c…`, the two live runs `7a089a1a…`, `648794e1…`, the install
  check `12106faf…`, and the five acceptance runs `0d0797b5…`, `9521ffd3…`,
  `cc200491…`, `a0831e94…`, `ddfe5e81…`), plus their folders. All were
  deleted after the tests, with Amir's OK.
- The probe code lives in the session scratchpad, not in this project.
