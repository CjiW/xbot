---
name: skill-creator
description: Create, update, or delete skills. Use when designing, structuring, or packaging skills with scripts, references, and assets. Activate when user asks to create a new skill, modify an existing skill, or discusses skill design.
---

# Skill Creator

Guide for creating and managing skills in xbot.

## Skill Directory

All skills live under `.xbot/skills/`. Each skill is a directory:

```
.xbot/skills/{skill-name}/
├── SKILL.md          # Required: YAML frontmatter + markdown body
├── scripts/          # Optional: executable scripts (bash/python)
├── references/       # Optional: docs loaded into context on demand
└── assets/           # Optional: templates, images, fonts for output
```

## How Skills Work (Progressive Disclosure)

1. **Discovery**: On every message, all skill `name` + `description` are listed in system prompt as `<available_skills>` XML
2. **Loading**: LLM uses `Read` tool to load SKILL.md when task matches a skill's description
3. **Execution**: LLM runs scripts via `Shell` tool, reads references via `Read` tool

**Key implication**: `description` in frontmatter is the ONLY trigger mechanism. Write it clearly and comprehensively.

## Creating a Skill

Use `Edit` tool to create all files. No special Skill tool needed.

### Step 1: Create SKILL.md

```bash
# Use Edit tool with mode "create"
path: .xbot/skills/{skill-name}/SKILL.md
```

SKILL.md format:

```markdown
---
name: my-skill
description: What this skill does and WHEN to use it. Be specific about triggers.
---

# Skill Title

Instructions for using this skill...
```

### Step 2: Add scripts/references/assets (if needed)

```bash
# Scripts
path: .xbot/skills/{skill-name}/scripts/run.sh

# References
path: .xbot/skills/{skill-name}/references/api-docs.md

# Assets
path: .xbot/skills/{skill-name}/assets/template.html
```

### Step 3: Referencing paths in SKILL.md

Use the skill's directory path relative to the working directory:

```markdown
Run the script:
bash .xbot/skills/my-skill/scripts/run.sh <args>

Read the reference:
See .xbot/skills/my-skill/references/api-docs.md for details.
```

## Updating a Skill

Use `Read` tool to view current SKILL.md, then `Edit` tool (replace/line mode) to modify.

## Deleting a Skill

```bash
# Use Shell tool
rm -rf .xbot/skills/{skill-name}
```

## Writing Guidelines

### Frontmatter

- `name`: lowercase, hyphens, short (e.g. `pdf-editor`)
- `description`: include WHAT it does + WHEN to use it. This is the only thing visible before loading.

### Body

- Keep under 500 lines. Split large content into `references/` files.
- Use imperative form ("Run the script", not "You should run the script")
- Only include info the LLM doesn't already know
- Prefer concise examples over verbose explanations

### Scripts

- Include when the same code would be rewritten repeatedly
- Test scripts before finalizing
- Use `#!/usr/bin/env bash` or `#!/usr/bin/env python3` shebang

### References

- For large docs (>100 lines), include a table of contents
- Reference from SKILL.md with clear "when to read" guidance

### What NOT to include

- README.md, CHANGELOG.md, INSTALLATION_GUIDE.md
- User-facing documentation
- Setup/testing procedures
