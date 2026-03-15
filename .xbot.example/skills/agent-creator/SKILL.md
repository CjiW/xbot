---
name: agent-creator
description: "Create a new SubAgent role. Use when user asks to create a new agent/role, or needs a specialized assistant for a specific task."
---

# Agent Creator

Create new SubAgent roles for specialized tasks.

## Required Tools

After loading this skill, immediately call `load_tools` for these tools:
- Edit
- Shell
- Glob

## Instructions

### Step 1: Understand the Agent's Purpose

Ask the user:
1. What task should this agent handle?
2. What tools does it need?
3. Any specific output format or workflow?

### Step 2: Create Agent File

Create `.xbot/agents/{agent-name}.md` with this template:

```markdown
---
name: {agent-name}
description: "{What this agent does. Use WHEN to use it — this is the trigger.}"
tools:
  - ToolName1
  - ToolName2
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

### Step 4: Write Quality Content

Follow `code-reviewer.md` quality standard:
- ✅ Specific process steps (not vague)
- ✅ Clear output format with examples
- ✅ Explicit rules and constraints
- ✅ Edge case handling
- ❌ Avoid generic descriptions like "analyze code" — specify how

### Step 5: Verify

List available agents to confirm:
```bash
ls -la .xbot/agents/
```

## Agent Naming Convention

- Use lowercase with hyphens: `code-reviewer`, `explorer`, `tester`
- Name should reflect its role/function
- Description must include "Use when..." trigger phrase

## Example

User wants: "a data analyst agent"

```markdown
---
name: data-analyst
description: "Data analysis agent. Use when user needs to analyze data, generate insights, or create visualizations."
tools:
  - Read
  - Grep
  - Shell
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
