---
name: agent-creator
description: "Manage SubAgent roles: create, view, modify, or delete agent definitions. MUST activate when user asks to create/edit/view/inspect any agent, or when you need to look at agent files under ~/.xbot/agents/."
---

# Agent Creator

Create new SubAgent roles for specialized tasks.

## Instructions

### Step 1: Understand the Agent's Purpose

Ask the user:
1. What task should this agent handle?
2. What tools does it need?
3. Any specific output format or workflow?

### Step 2: Create Agent File

**IMPORTANT**: Create agent files in the correct agents directory, NOT in the current working directory. Use the system prompt's **"Agents 存储目录"** path (each agent's `<dir>` field also shows its definition location; `embed` means built-in). For example, if Agents 存储目录 is `/opt/xbot/.xbot/agents`, create files as `/opt/xbot/.xbot/agents/{agent-name}.md`.

Agent definition uses YAML frontmatter + Markdown body:

```markdown
---
name: {agent-name}
description: "{What this agent does. Use WHEN to use it — this is the trigger.}"
model: balance
tools:
  - ToolName1
  - ToolName2
capabilities:
  memory: true
  send_message: false
  spawn_agent: true
---

You are a {agent-name} agent. Your job is to {one-sentence purpose}.

## Process

1. **Step 1** — Description
2. **Step 2** — Description
3. **Step 3** — Description

## Output Format

### Summary
One paragraph: what was done, overall result.

### Details
Structured output based on task type.

## Rules

- **Rule 1** — What to do
- **Rule 2** — What to avoid
- **Rule 3** — Specific constraints
```

### Step 3: Choose Tools

Common tools for agents:
- **Code**: Read, Grep, Glob, Shell, Edit
- **Research**: WebSearch, Fetch, Grep, Glob
- **Testing**: Shell, Read, Glob
- **Communication**: feishu_send_message, feishu_docx_*

If `tools` is omitted, the agent gets the full dynamic tool set (search_tools + load_tools).
If `tools` is specified, only those tools are directly available — no search/load needed.

### Step 3.5: Choose Model Tier

The `model` field in frontmatter controls which LLM model the agent uses. Three tiers are available:

| Tier | When to use | Examples |
|------|-------------|---------|
| `vanguard` | Complex reasoning, architecture decisions, multi-step analysis | Code review of critical PRs, complex refactoring plans |
| `balance` (default) | General tasks, most agents | Code exploration, test writing, docs, debugging |
| `swift` | Simple/fast tasks, bulk operations | Log parsing, file searching, formatting, trivial edits |

**Rules:**
- If `model` is omitted, defaults to `balance` (NOT the parent agent's model).
- The caller can override with the `model_tier` parameter in SubAgent() at invocation time.
- Think about cost vs quality: don't use vanguard for grep/search tasks; don't use swift for design tasks.
- Examples: exploration agents → `swift`; code reviewer → `vanguard`; most others → omit (gets `balance`).

### Step 4: Configure Capabilities

Capabilities control what extra powers the agent has:

| Capability | Default | Description |
|------------|---------|-------------|
| `memory` | false | Access to Letta memory system (core/archival/recall) |
| `send_message` | false | Can send messages directly to IM channels |
| `spawn_agent` | true | Can create sub-agents (watch recursion depth) |

### Step 5: Write Quality Content

Follow `code-reviewer.md` quality standard:
- ✅ Specific process steps (not vague)
- ✅ Clear output format with examples
- ✅ Explicit rules and constraints
- ✅ Edge case handling
- ❌ Avoid generic descriptions like "analyze code" — specify how

### Step 6: Verify

List available agents to confirm:
```bash
ls -la agents/
```

## Agent Naming Convention

- Use lowercase with hyphens: `code-reviewer`, `explorer`, `tester`
- Name should reflect its role/function
- Description must include "Use when..." trigger phrase

## Example

```markdown
---
name: data-analyst
description: "Data analysis agent. Use when user needs to analyze data, generate insights, or create visualizations."
tools:
  - Read
  - Grep
  - Shell
capabilities:
  memory: true
---

You are a data analyst agent. Your job is to analyze data and generate actionable insights.

## Process

1. **Understand data** — Read data files, identify structure and fields.
2. **Explore patterns** — Use shell commands (awk, sed, sort, uniq) to find patterns.
3. **Generate insights** — Summarize findings with specific numbers.

## Output Format

### Summary
Key findings in one paragraph.

### Statistics
| Metric | Value |
|--------|-------|
| Total records | X |
| Unique values | Y |

### Insights
- Finding 1
- Finding 2

## Rules
- Always provide specific numbers, not vague statements
- Use tables for structured data
- Cite file:line references when analyzing code
```
