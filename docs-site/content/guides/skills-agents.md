---
title: "Skills & Agents"
weight: 30
---

# Skills & Agents

xbot supports extensible skills and role-based sub-agents, both defined as Markdown files.

## Skills

Skills are Markdown-based capability packages that provide specialized instructions for specific tasks. They are loaded from the workspace on demand.

### Skill Structure

Each skill is a directory under `~/.xbot/skills/` (or embedded in the binary):

```
~/.xbot/skills/
└── my-skill/
    └── SKILL.md       # Main skill definition
```

### Built-in Skills

| Skill | Description |
|-------|-------------|
| `agent-creator` | Create new SubAgent role definitions |
| `debug` | Investigate and fix bugs |
| `skill-creator` | Create, update, or delete skills |

### Using Skills

The agent discovers and loads skills via the `Skill` tool:

```
Skill(name="debug", action="load")
```

## SubAgents

SubAgents are role-based sub-programs that can be delegated tasks. They run independently with their own tool set and context.

### Agent Structure

Each agent is a Markdown file under `~/.xbot/agents/` (or embedded):

```
~/.xbot/agents/
├── explore.md         # Code exploration agent
├── chancellery.md     # Review agent
├── secretariat.md     # Planning agent
└── ...
```

### Built-in Agents (Three Provinces System)

| Agent | Role | Tools |
|-------|------|-------|
| `explore` | Code exploration and logic analysis | Grep, Glob, Read, Shell |
| `chancellery` | Review and quality assurance | Read, Grep, Glob, Shell |
| `secretariat` | Planning and architecture design | Read, Grep, Glob, Shell |
| `department-state` | Task execution coordination | Read, Grep, Glob, Shell |
| `ministry-works` | Code implementation | (specialized) |
| `ministry-justice` | Bug hunting and correctness | Read, Grep, Glob |
| `ministry-personnel` | Code quality review | Read, Grep, Glob |
| `ministry-revenue` | Performance analysis | Read, Grep, Glob, Shell |
| `ministry-rites` | Documentation review | Read, Grep, Glob |
| `ministry-defense` | Security review | Read, Grep, Glob, Shell |

### Usage Modes

**One-shot** (default):

```
SubAgent(task="review this code", role="chancellery")
```

**Interactive** (multi-turn session):

```
SubAgent(task="analyze this module", role="explore", interactive=true, instance="review-1")
SubAgent(task="now check the tests", role="explore", action="send", instance="review-1")
SubAgent(task="", role="explore", action="unload", instance="review-1")
```

### Limits

- Max nesting depth: 6 (`MAX_SUBAGENT_DEPTH`)
- Parallel instances: supported via unique `instance` IDs

## Marketplace

Users can publish, browse, install, and uninstall skills and agents through the marketplace (Web channel and slash commands).

| Command | Description |
|---------|-------------|
| `/browse` | Browse marketplace entries |
| `/install <name>` | Install a skill/agent |
| `/uninstall <name>` | Uninstall |
| `/publish` | Publish own skill/agent |
| `/unpublish` | Remove from marketplace |
