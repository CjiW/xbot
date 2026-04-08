---
name: debug
description: Investigate and fix bugs. Use when the user reports a bug, failing behavior, regression, flaky case, crash, panic, incorrect output, or asks to debug an issue.
---

# Debug

## Goal

Turn vague bug reports into reproducible, verified fixes.

## Default workflow

1. **Define the failure clearly**
   - Restate the observed behavior, expected behavior, affected scope, and current evidence.
   - Before changing code, define a **verifiable success criterion** for the fix.

2. **Reproduce with a test first**
   - Prefer adding or updating an automated test that fails for the reported bug.
   - Reuse the project's existing test style and location.
   - If the bug is hard to express as a full test, create the smallest reproducible check possible (unit test, integration test, script, fixture, or command).

3. **Fix only after reproduction exists**
   - Change code only after you can reproduce the issue with a failing test/check, unless reproduction is impossible in the current environment.
   - Keep the fix scoped to the verified cause.

4. **Verify the fix**
   - Re-run the reproduction test/check and confirm it now passes.
   - Run the nearest related validation commands to catch regressions.

5. **If user cooperation is required**
   - Prepare everything the user needs before asking them to test:
     - exact command(s) to run
     - required environment variables / setup
     - expected output
     - log file path or log collection method
     - what result means success vs failure
   - Ask concise questions only after the test plan is ready.
   - When the user returns with logs/results, analyze both the logs and the user report before proposing the next step.

## Rules

- Prefer **test-first debugging** whenever the environment allows it.
- Do not claim a bug is fixed without a passing reproduction check or an explicit explanation of why local verification is impossible.
- If you cannot reproduce locally, say so clearly and switch to preparing the best user-runnable reproduction and logging plan.
- When asking the user to help test, minimize their work; prepare commands, paths, and logging details for them.
- Always report:
  - how the bug was reproduced
  - what changed
  - how the fix was verified
  - what remains uncertain
