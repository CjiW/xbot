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
AGENT.md (index, auto-injected into prompt)
  → tells you WHERE to look for details
  → should make you want to Read specific files, not answer questions directly

Knowledge files (the actual knowledge, on disk)
  → agent reads them with Read tool when needed
  → each file is self-contained on one topic
  → AGENT.md references them; agent uses them
```

## AGENT.md

Auto-loaded into system prompt (up to 10000 chars). Keep it concise — an index, not an encyclopedia.

Purpose: tell your future self **where to look**, not **everything you know**.

What belongs:
- One-line project summary
- Architecture overview (2-3 sentences, link to detail file for more)
- Build/test/lint commands
- **Knowledge Files section**: list of existing files with one-line descriptions
- Key conventions that don't fit elsewhere (max 5 bullets)

What does NOT belong:
- Anything that belongs in a knowledge file
- Specific line numbers, function signatures, or code snippets
- Information already in README

## Knowledge Files

These are where the real knowledge lives. **Create them freely — one file per topic, no need to consolidate.** More small files is better than fewer large ones.

### Directory Structure

Mirror the repository's directory structure under the knowledge root (e.g. `docs/agent/`). This makes it trivial to find the right file:

```
docs/agent/                    ← knowledge root
  architecture.md              ← cross-cutting: message flow, pipeline, conventions
  conventions.md               ← cross-cutting: coding style, error handling
  gotchas.md                   ← cross-cutting: known pitfalls
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

When to NOT create/update:
- Trivial changes (typo fixes, comment edits)
- Information that's already correctly documented
- When nothing surprising was learned

## Decision Flow

After completing a task, ask yourself:

1. Did I learn something non-obvious? → Write it into the relevant knowledge file (or create one)
2. Did the file/knowledge list change? → Update AGENT.md's index
3. Did nothing worth remembering happen? → Skip entirely

**Most of the time, the answer should be (3).** Do not inflate documentation.

## Accuracy Maintenance

- Before writing: Read the existing file first
- After writing: Verify AGENT.md references match actual files on disk
- When deleting/renaming files: Update all references in AGENT.md and other knowledge files
- Do NOT just append — revise outdated content
- Check existing knowledge files for staleness: are the described files, APIs, and conventions still current? If not, update or remove

## Validation

After creating or significantly updating the knowledge base, verify it works:

1. Launch a SubAgent (explore role) restricted to only reading AGENT.md and files referenced from it
2. Ask it architecture questions that require understanding the project structure
3. If it cannot answer correctly, the knowledge files are incomplete — fix them
4. The test proves the knowledge base is self-sufficient for a zero-memory agent
