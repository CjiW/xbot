---
name: knowledge-management
description: "Manage project knowledge files (AGENT.md and knowledge tree) across sessions. Activate after completing significant tasks, when exploring a new codebase, or when asked to review/update project documentation."
---

# Knowledge Management

Manage persistent project knowledge that survives across sessions.

## Core Principle

Write as if the next reader (your future self) has **zero memory** of this project.
Every entry must be self-contained and immediately actionable.

## AGENT.md

`AGENT.md` at the project root is the entry point. It is automatically loaded into
your system prompt (first 2000 chars). Use it as an **index**, not a dump.

### Structure

```markdown
# Project Name

> One-line summary of what this project does.

## Architecture
Brief overview. Link to detail files for complex subsystems.

## Knowledge Files
- `docs/agent/architecture.md` — module relationships, data flow
- `docs/agent/conventions.md` — coding style, naming rules, error handling
- `docs/agent/gotchas.md` — known pitfalls and workarounds

## Build & Test
- `make build` — compile
- `make test` — run tests
- `go test ./path/...` — test specific package

## Key Conventions
- Rule 1
- Rule 2
```

### What belongs in AGENT.md

- Architecture overview (where is the entry point, what are the main packages)
- Build/test/lint commands
- Critical conventions that differ from language defaults
- **References** to deeper knowledge files (tree structure)

### What does NOT belong

- Information already in README
- Obvious facts ("Go uses .go files")
- Specific line numbers or function names that change frequently
- Verbose explanations — keep it terse, link to detail files

## Knowledge Tree

For non-trivial projects, maintain a tree of knowledge files under `docs/agent/`
(or a similar convention). Each file covers one topic deeply.

### Rules

- **One topic per file**: architecture, conventions, gotchas, APIs, deployment, etc.
- **Cross-reference**: files link to each other with relative paths
- **Shallow tree**: prefer flat list over deep nesting; max 2 levels
- **AGENT.md is the root**: all knowledge files are referenced from AGENT.md

### Example tree

```
AGENT.md                          ← index (auto-injected in prompt)
docs/agent/
  architecture.md                 ← module map, data flow diagrams
  conventions.md                  ← coding style, naming, error handling
  gotchas.md                      ← known pitfalls, workarounds
  testing.md                      ← test conventions, fixtures, mocks
```

## When to Create or Update

### Create AGENT.md
- After first meaningful exploration of a new project
- When the user asks "document this project for yourself"

### Update AGENT.md or knowledge files
- After completing a task that revealed non-obvious architecture or conventions
- When you discover a gotcha, workaround, or hidden dependency
- After making structural changes (new packages, renamed APIs, moved files)

### When NOT to update
- Trivial changes (typo fix, comment addition)
- Changes to ephemeral state (test output, temporary branches)
- When the user hasn't asked and nothing surprising was discovered

**Rule of thumb**: Only update when future sessions would benefit. If in doubt, skip.

## Self-Improvement

After completing a task, briefly consider:

1. **Should I create/update AGENT.md?** Only if the project has none, or you
   discovered important non-obvious information.
2. **Should I create a knowledge file?** Only if the topic is complex enough
   that a short AGENT.md entry can't cover it AND you expect to revisit it.
3. **Should I create a Skill?** Only if you've developed a repeatable workflow
   that would benefit from structured instructions (e.g., a project-specific
   deployment checklist, a debugging methodology for this codebase).

**Anti-bloat guard**: Never add knowledge just because you can. Every piece of
documentation must earn its place by saving future effort. If existing docs
already cover something, don't duplicate — link instead.

## Updating Existing Content

When project structure changes (files moved, packages renamed, conventions
shifted), update the affected knowledge files. Do NOT just append new info
on top — **revise** the existing content to stay accurate.

Checklist for updates:
- [ ] Is the old content still accurate? If not, fix it.
- [ ] Are cross-references still valid? Fix broken links.
- [ ] Is the file getting too long? Consider splitting.
- [ ] Can anything be removed because it's now obvious or obsolete?

## Format Guidelines

- Use relative paths for all file references
- Keep paragraphs short (2-3 sentences)
- Use bullet lists for enumerations
- Use headers for structure, not decoration
- Write in English for code-level docs; project-level docs may follow the
  project's existing language convention
