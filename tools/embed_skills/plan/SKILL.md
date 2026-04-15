---
name: plan
description: "Build implementation plans for complex tasks. Use when the user asks to plan, design an approach, or think through a task before coding. Also activate for large refactorings, multi-file changes, or when the user says 'plan first' or '/plan'."
---

# Plan Mode

Break complex tasks into actionable, verifiable implementation plans before writing any code.

## Rules

1. **READ-ONLY.** Do NOT modify files, run non-readonly commands, or make any changes to the system. You may only read files, search code, and explore the codebase.
2. **One plan, one file.** Write the plan to a single markdown file. Update it in place as the plan evolves — do not create multiple drafts.
3. **End with a decision point.** Every plan must conclude with clear next steps and a go/no-go decision for the user.

## Workflow

### Phase 1: Understand

Goal: Fully understand the request and the relevant codebase before proposing anything.

- Parse the user's request: what is the desired outcome, what are the constraints, what is the scope?
- Read AGENT.md and relevant knowledge files first (architecture, conventions, gotchas).
- Use `Grep`/`Glob`/`Read` to trace code paths, find dependencies, and understand existing patterns.
- Identify all files that will be affected.
- If anything is unclear, ask the user a focused question before proceeding.

Output: a list of affected files, key dependencies, and any open questions.

### Phase 2: Design

Goal: Propose an implementation approach with clear trade-offs.

- Describe the approach in 2-3 sentences.
- List the changes per file (what changes, why, not how).
- Identify risks: breaking changes, test gaps, performance implications, migration needs.
- If multiple approaches exist, briefly compare them and recommend one.
- Reference existing conventions from knowledge files — do not invent new patterns.

Output: a structured plan with file-level change descriptions and risk assessment.

### Phase 3: Review

Goal: Validate the plan against reality before finalizing.

- Re-read critical files to verify assumptions.
- Check for edge cases the plan might miss.
- Verify the plan does not conflict with gotchas or known pitfalls.
- Ensure the Definition of Done is clear and testable.

### Phase 4: Finalize

Goal: Write the final plan to a file the user can review and edit.

Write the plan to `docs/plans/<task-name>.md` with this structure:

```markdown
# Plan: <task name>

## Summary
One paragraph describing what we're doing and why.

## Changes

### `<path/to/file.go>`
- What: <concise description of the change>
- Why: <rationale>

### `<path/to/file.go>`
...

## Risks
- <risk>: <mitigation>

## Definition of Done
- [ ] <verifiable criterion 1>
- [ ] <verifiable criterion 2>

## Open Questions
- <question> (if any remain)
```

Keep the plan concise enough to scan in 30 seconds, detailed enough to execute without ambiguity.

## After Planning

When the user approves the plan:

1. Read the plan file to refresh context.
2. Create a TODO list from the Definition of Done items.
3. Execute the plan step by step, checking off items as you go.
4. After completion, run the project's standard verification (build/test/lint).

## Tips

- For tasks touching 3+ files or involving architectural changes, always plan first.
- Use SubAgent (explore role) to investigate subsystems in parallel during Phase 1.
- The plan file is a living document — update it if you discover new information during any phase.
- If the task turns out to be trivial (1-2 files, clear approach), say so and skip the formal plan.
