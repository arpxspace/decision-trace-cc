// The decision-tree mod: Claude Code's side of the tools (PRD 18). It gives
// Claude record_decision and show_decision_tree and the rules for them, and
// hands every call to the Go program, which keeps the tree and draws it.
import type { EngineInterface, ProcessRunResult, Register, ToolCallResult } from 'claude-code'

// The names Claude sees are mcp__<plugin>__<tool>, the same as the MCP
// server's, so the allow rules in settings.json still match.
const RECORD = 'mcp__decision-tree__record_decision'
const SHOW = 'mcp__decision-tree__show_decision_tree'

// What `decision-tree describe` prints.
type Spec = {
  instructions: string
  tools: { name: string; description: string; inputSchema: Record<string, unknown> }[]
}

// The Go program is told the session; the MCP server had to look it up.
async function sessionFlags($: EngineInterface): Promise<string[]> {
  return ['--session', await $.session.id(), '--cwd', await $.session.cwd()]
}

function answer(ran: ProcessRunResult): ToolCallResult {
  if (ran.exitCode === 0) {
    return { result: ran.stdout.trim() }
  }
  return failed(ran.stderr.trim().replace(/^decision-tree: /, ''))
}

function failed(message: string): ToolCallResult {
  return { isError: true, result: message, text: message }
}

export const register: Register = (on, options) => {
  const binary =
    typeof options.binary === 'string' && options.binary !== '' ? options.binary : 'decision-tree'
  // Empty until the tools are registered, so Claude never reads rules for
  // tools it does not have.
  let rules = ''

  on('session.start', async ($, e, next) => {
    try {
      const ran = await $.process.run([binary, 'describe'])
      if (ran.exitCode !== 0) {
        throw new Error(ran.stderr.trim())
      }
      const spec = JSON.parse(ran.stdout) as Spec
      for (const tool of spec.tools) {
        await $.tool.register(tool)
      }
      rules = spec.instructions
    } catch (err) {
      $.ui.toast(`decision-tree is off: ${err instanceof Error ? err.message : String(err)}`)
    }

    return next(e)
  })

  // The rules go in the system prompt, where the MCP server's instructions were.
  on('prompt.compose', async ($, e, next) => {
    const composed = await next(e)
    if (rules === '') {
      return composed
    }

    return {
      sections: [...composed.sections, { id: 'decision-tree:rules', text: rules, scope: 'session' }],
    }
  })

  // Keep both tools in Claude's list, not behind ToolSearch, as the MCP
  // server's alwaysLoad did.
  on('tool.describe', { tool: RECORD }, async ($, e, next) => ({ ...(await next(e)), isDeferred: false }))
  on('tool.describe', { tool: SHOW }, async ($, e, next) => ({ ...(await next(e)), isDeferred: false }))

  on('tool.call', { tool: RECORD }, async ($, e) => {
    // The rest of e is the call itself, as Claude sent it.
    const { tool, tool_use_id, consent, agentId, ...call } = e
    // The tool use id marks where a branch would cut the chat (PRD 17.1).
    const marked = tool_use_id === undefined ? [] : ['--tool-use-id', tool_use_id]
    try {
      const argv = [binary, 'record', ...(await sessionFlags($)), ...marked]
      return answer(await $.process.run(argv, { stdin: JSON.stringify(call) }))
    } catch (err) {
      return failed(`decision-tree could not run: ${err instanceof Error ? err.message : String(err)}`)
    }
  })

  on('tool.call', { tool: SHOW }, async $ => {
    try {
      return answer(await $.process.run([binary, 'show', ...(await sessionFlags($))]))
    } catch (err) {
      return failed(`decision-tree could not run: ${err instanceof Error ? err.message : String(err)}`)
    }
  })
}
