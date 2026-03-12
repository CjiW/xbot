---
name: skill-creator
description: Create, update, or delete skills. Use when designing, structuring, or packaging skills with scripts, references, and assets. Activate when user asks to create a new skill, modify an existing skill, or discusses skill design.
---

# Skill Creator

Guide for creating and managing skills in xbot.

## What is a Skill

A skill is a directory containing:
- `SKILL.md` - Required: YAML frontmatter + markdown body
- `scripts/` - Optional: executable scripts (bash/python)
- `references/` - Optional: docs loaded into context on demand
- `assets/` - Optional: templates, images, fonts for output

## How Skills Work

1. **Discovery**: On every message, all skill `name` + `description` are listed in system prompt
2. **Loading**: LLM uses `Read` tool to load SKILL.md when task matches a skill's description
3. **Execution**: LLM runs scripts via `Shell` tool

**Key**: `description` in frontmatter is the ONLY trigger. Write it clearly and comprehensively.

## Where to Create Skills

Skills must be created relative to the current working directory:

```
./skills/{skill-name}/
├── SKILL.md
├── scripts/
├── references/
└── assets/
```

## Creating a Skill

Use `Edit` tool to create the skill directory and files.

### Step 1: Create SKILL.md

```markdown
---
name: my-skill
description: What this skill does and WHEN to use it. Be specific about triggers.
---

# Skill Title

Instructions for using this skill...
```

### Step 2: Add scripts/references/assets if needed

Create executable scripts with proper shebangs:
```bash
#!/usr/bin/env bash
# scripts/run.sh

echo "Hello from skill"
```

### Step 3: Reference files in SKILL.md

Use relative paths from the skill directory:
```markdown
Run the script:
bash scripts/run.sh <args>

Read the reference:
See references/docs.md for details.
```

## Updating a Skill

Use `Read` tool to view current SKILL.md, then `Edit` tool to modify.

## Deleting a Skill

Use `Shell` tool to remove the skill directory.

## Writing Guidelines

### Frontmatter
- `name`: lowercase, hyphens (e.g. `pdf-editor`)
- `description`: include WHAT it does + WHEN to use it

### Body
- Keep under 500 lines
- Use imperative form
- Only include info the LLM doesn't already know

### Scripts
- Use when code would be rewritten repeatedly
- Test scripts before finalizing
- Use `#!/usr/bin/env bash` or `#!/usr/bin/env python3`

### References
- For large docs (>100 lines), include a table of contents
