---
name: post-dev
description: "Post-development cleanup: update AGENT.md and knowledge files to reflect code changes. MUST activate before git commit (or when user asks to commit/push). Also activate after any code modification that adds/removes files, changes architecture, or modifies core behavior."
---

# Knowledge Management

Maintain a living knowledge base so future sessions (with zero memory) can work effectively.

## Iron Rules

1. **Every file referenced in AGENT.md MUST exist on disk.** Before adding a reference, create the file. Before removing a file, remove its reference. Broken references are worse than no references.
2. **Knowledge files are the primary deliverable, not AGENT.md.** AGENT.md is just an index. When you learn something non-obvious, write it into the appropriate knowledge file. Only update AGENT.md's index entry if the file list changed.
3. **Read before write.** Before updating any knowledge file, read it first. Before creating AGENT.md references, verify the target file exists.
4. **Do NOT copy the structure from this skill into AGENT.md.** Every project is different. Observe the actual project structure and document what exists, not what a template says should exist.

## Two-Layer Architecture

```
AGENT.md (auto-injected into prompt — the single source of truth)
  → project summary, build commands, architecture overview
  → GOTCHAS: critical pitfalls written directly here (no separate file)
  → Knowledge Files index: tells you WHERE to look for details
  → should make you want to Read specific files, not answer questions directly

Knowledge files (the actual knowledge, on disk)
  → agent reads them with Read tool when needed
  → each file is self-contained on one topic
  → AGENT.md references them; agent uses them
```

## AGENT.md

Auto-loaded into system prompt (up to 10000 chars). Keep it concise but information-dense.

Purpose: tell your future self **where to look** AND **what will kill you**.

What belongs:
- One-line project summary
- Architecture overview (2-3 sentences, link to detail file for more)
- Build/test/lint commands
- **GOTCHAS section**: critical pitfalls that MUST be visible without opening extra files. These are non-negotiable — if a pitfall can waste hours or crash the process, it goes here. Keep it to bullet points, no prose.
- **Knowledge Files section**: list of existing files with one-line descriptions
- Key conventions that don't fit elsewhere (max 5 bullets)

What does NOT belong:
- Anything that belongs in a knowledge file (except critical gotchas)
- Specific line numbers, function signatures, or code snippets
- Information already in README
- Minor/niche gotchas that are package-specific → put those in the relevant knowledge file

## Knowledge Files

These are where the real knowledge lives. **Create them freely — one file per topic, no need to consolidate.** More small files is better than fewer large ones.

### Directory Structure

Mirror the repository's directory structure under the knowledge root (e.g. `docs/agent/`). This makes it trivial to find the right file:

```
docs/agent/                    ← knowledge root
  architecture.md              ← cross-cutting: message flow, pipeline, conventions
  conventions.md               ← cross-cutting: coding style, error handling
  agent.md                     ← agent/ package: loop, engine, middleware
  channel.md                   ← channel/ package: CLI, Feishu, Web, QQ
  llm.md                       ← llm/ package: OpenAI, Anthropic, retry, streaming
  tools.md                     ← tools/ package: built-in tools, sandbox, hooks
  memory.md                    ← memory/ package: letta, flat, providers
  session.md                   ← session/ package: multi-tenant sessions
  storage.md                   ← storage/ package: SQLite, vector DB
  config.md                    ← config/ package: JSON config, env overrides
  prompt.md                    ← prompt/ package: templates, embed, rendering
```

When you explore a new subsystem or package, create its knowledge file. Don't worry about having too many — future sessions will use AGENT.md's index to find exactly the right file.

### When to create a new knowledge file

- You explored a subsystem deeply enough to document it
- A knowledge file would save future sessions from re-exploring the same code
- A topic is growing too large in an existing file — split it

### When to update an existing knowledge file

- You discovered something that changes or contradicts what's documented
- The project structure changed (files moved, APIs renamed)
- You found a bug, workaround, or gotcha worth recording

## Decision Flow

After completing a task, update knowledge:

1. **Did I encounter or fix a gotcha/pitfall? → Write it directly into AGENT.md's GOTCHAS section.** Critical gotchas (crashes, silent data loss, hours-wasting traps) go into AGENT.md so they're always visible without opening extra files. Package-specific/niche gotchas can go into the relevant knowledge file instead.
2. Did I learn something about the codebase (APIs, dependencies, conventions, architecture)? → Write it into the relevant knowledge file
3. Did the file/knowledge list change? → Update AGENT.md's Knowledge Files index
4. Did any existing documentation become stale due to my changes? → Update it in place
5. Did I add gotchas to AGENT.md? → Remove `docs/agent/gotchas.md` if it exists and all its content has been migrated to AGENT.md. AGENT.md should be the single source of truth for critical pitfalls.

**Default to updating.** Every session should leave the knowledge base more accurate than it found it. The only exception is truly trivial changes (typo fixes, comment-only edits).

## Accuracy Maintenance

- Before writing: Read the existing file first
- After writing: Verify AGENT.md references match actual files on disk
- When deleting/renaming files: Update all references in AGENT.md and other knowledge files
- Do NOT just append — revise outdated content
- Keep each file focused on one topic; split when a file grows beyond ~200 lines
- After significant updates: spot-check that AGENT.md index entries match actual files on disk
