---
name: agent-creator
description: Create, update, list, or delete SubAgent roles. Use when the user asks to create a new agent role, modify an existing one, list available roles, or discusses agent/role design and configuration.
---

# Agent Creator

Create and manage SubAgent roles stored as `.md` files in the agents directory.

## Required Tools

After loading this skill, immediately call `load_tools` for these tools:
- (none — this skill uses built-in tools: Shell, Read, Edit, Glob)

## Agent File Format

Agent role files live in:
- **Global**: `{WorkDir}/.xbot/agents/{role-name}.md`
- **User-private**: `{WorkDir}/.xbot/users/{sender}/workspace/agents/{role-name}.md`

Each file is a Markdown document with YAML frontmatter:

```markdown
---
name: my-agent
description: "What this agent does and when to delegate to it"
tools:
  - Shell
  - Read
  - Edit
  - Grep
  - Glob
  - Fetch
  - WebSearch
capabilities:
  memory: true
  send_message: false
  spawn_agent: false
---

You are a specialized agent that...

## Instructions
- ...
- ...
```

### Frontmatter Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Agent role name (used in `SubAgent(role=...)`) |
| `description` | string | Yes | One-line description for the system prompt catalog |
| `tools` | list | No | Allowed tools (default: all). If empty, agent gets all tools |
| `capabilities.memory` | bool | No | Access to core/archival/recall memory (default: false) |
| `capabilities.send_message` | bool | No | Can send messages to IM channels (default: false) |
| `capabilities.spawn_agent` | bool | No | Can create child SubAgents (default: false) |

### Body (System Prompt)

Everything after the closing `---` becomes the agent's system prompt. This is prepended to the universal SubAgent prompt template. Keep it focused — the agent inherits the parent's tool usage patterns.

## Workflow

### 1. Gather Requirements

Ask the user (or infer from context):
- **Role name** — lowercase-with-hyphens, descriptive (e.g. `code-reviewer`, `data-analyst`)
- **Purpose** — what tasks will this agent handle?
- **Tools needed** — which tools from the available set? (Shell, Read, Edit, Grep, Glob, Fetch, WebSearch, SubAgent, Cron, etc.)
- **Capabilities** — does it need memory? IM access? Ability to spawn sub-agents?

### 2. Create Agent File

1. Determine the target directory:
   - Check if `ctx.SenderID` is available → user-private dir
   - Otherwise → global dir
   - Use `Glob` to check existing agents: `pattern: ".xbot/agents/*.md"`
2. Write the file using `Edit` (mode: create)
3. Verify the file parses correctly by running the loader test pattern

### 3. Validate

After creating/updating, verify:
```bash
cd /workspace/xbot && go test -run TestLoadAgentRole ./tools/ -v
```

### 4. List Existing Agents

Scan the agents directories:
```bash
# Global agents
ls -la /workspace/xbot/.xbot/agents/
# Check format
head -20 /workspace/xbot/.xbot/agents/{name}.md
```

## Naming Conventions

- Use **lowercase-with-hyphens**: `code-reviewer`, not `CodeReviewer`
- Name should be **verb-focused** or **role-focused**: `code-reviewer`, `doc-writer`, `researcher`
- Keep under 30 characters

## Best Practices for System Prompt

1. **Be specific** about the agent's expertise and limitations
2. **Define output format** if the agent produces structured results
3. **Set boundaries** — what the agent should NOT do
4. **Keep it under 500 words** — the universal template adds more context
5. **Include examples** if the task format is non-obvious

## Common Tool Combinations

| Agent Type | Typical Tools |
|------------|--------------|
| Code reviewer | Shell, Read, Grep, Glob, Edit |
| Researcher | WebSearch, Fetch, Read |
| Writer | Read, Edit, Glob |
| DevOps | Shell, Read, Grep, Glob |
| Data analyst | Shell, Read, Grep, Glob, Fetch |
