// What Claude reads about the two tools: the rules for when to log a
// decision, which go in the system prompt, and each tool's description and
// input. Keep each text under 2,048 characters, Claude Code's limit for a
// tool description (a test checks).

/** The rules: when to log a decision, and how. */
export const INSTRUCTIONS = `decision-trace draws the choices made in this session as a tree. The user watches it in a side pane, so keep it right with record_decision.

Log a decision only once it is made. Never log options that are only being discussed ("Redis, Postgres, or an in-memory cache are all possible" is not a decision). Made means:
- the user states it ("Use PostgreSQL rather than SQLite"): by user
- you recommend and the user agrees ("Agreed"): by both
- the user leaves it to you and you choose: by claude
A suggestion is not a decision. If you suggest Redis and the user says "No, use Postgres", log one decision: options Redis and PostgreSQL, picked PostgreSQL, by user.

In options, list the alternatives that were talked about, so the tree shows what was rejected.

Changing an earlier decision ("Actually change this to GraphQL"): call with that decision_id and the new picked, and add the new option to options if it is new. Set drop_later only if the decisions made after it depended on the old choice.

The topic is a short statement of what was decided, not a question: "Database", "API framework", "State storage". The tree shows "Database: PostgreSQL".

Project-level only: a tool or technology, an approach, scope, a design direction. Not names, the order of work, or which command to run. Many turns about one topic, and answers to AskUserQuestion, are one decision. When unsure, leave it out.

Log quietly. Do not mention the tree or this tool in your replies unless the user asks about it.

Only the main conversation logs decisions, not sub-agents. Keep labels short (under 8 words). If you lose track of the ids, call show_decision_tree.`

const RECORD_DESCRIPTION = `Log a decision once it is made, or change one. The decision-trace rules say what counts.

- New decision: topic + options (every option talked about, the winner included) + picked + reason + by.
- Change an earlier decision: decision_id + picked + reason + by, plus options if the new pick was not an option before. The old pick is marked as changed. Decisions made after it stay, unless drop_later is set: then they are set aside with the old pick, for when they depended on it.
- New decision from an earlier point, setting aside everything after that point: topic + options + picked + reason + by + after (the node id to grow from).

The reply gives the decision id and where "you are here" is.`

const SHOW_DESCRIPTION = 'Show this session\'s decision tree as text, with decision ids (d1) and node ids (n1).'

export const RECORD = 'record_decision'
export const SHOW = 'show_decision_tree'

/** The two tools, as the mod registers them. */
export const TOOLS = [
  {
    name: RECORD,
    description: RECORD_DESCRIPTION,
    inputSchema: {
      type: 'object',
      properties: {
        topic: {
          type: 'string',
          description: 'What was decided, as a short statement, not a question: Database. Needed for a new decision.',
        },
        options: {
          type: 'array',
          items: { type: 'string' },
          description: 'Every option talked about, the winner included. Short labels.',
        },
        picked: { type: 'string', description: 'The option that won. Only log a decision once one has won.' },
        reason: { type: 'string', description: 'One line on why picked won.' },
        by: {
          type: 'string',
          description: 'Who made the call: the user stated it, claude chose it, or both agreed.',
          enum: ['user', 'claude', 'both'],
        },
        decision_id: {
          type: 'string',
          description: 'Id of a decision already in the tree (like d3), to change its pick.',
        },
        drop_later: {
          type: 'boolean',
          description:
            'When changing a pick: also set aside the decisions made after the old pick, because they depended on it.',
        },
        after: {
          type: 'string',
          description:
            'Only for a new decision: the node id (like n4) to grow it from. Everything after that node is set aside.',
        },
      },
      required: ['picked', 'reason', 'by'],
      additionalProperties: false,
    },
  },
  { name: SHOW, description: SHOW_DESCRIPTION, inputSchema: { type: 'object' } },
]
