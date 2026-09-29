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

## Side effects of the test

- `git init` made this folder its own git project. Claude Code then asked
  again whether to trust the folder. The test answered "yes".
- The test left two short chats in this folder's history (`7ef62275…` and
  `8533417c…`), plus their folders. All were deleted after the test.
- The probe code lives in the session scratchpad, not in this project.
