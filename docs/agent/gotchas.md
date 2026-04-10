# Known Pitfalls

## Tool Schemas

- **Array types MUST include `Items` field.** OpenAI API rejects schemas with array properties missing `items`. Example: `Items: &llm.ToolParamItems{Type: "string"}`. See `memory/letta/letta.go` for reference.

## LLM / Streaming

- DeepSeek providers duplicate `reasoning_content` in Content. TrimSpace comparison deduplicates (`llm/openai.go:584`).
- Empty stream deltas (all fields nil) cause panic if not skipped (`llm/openai.go:763`).
- `finish_reason` in intermediate chunks causes premature termination — must only check after stream loop ends (`llm/openai.go:788`).
- Must send Usage before Done event, or consumer misses usage data (`llm/openai.go:836`).
- Retry wrapper creates fresh context per attempt (`context.Background()`), not inheriting parent deadline. Parent cancel is bridged via separate goroutine (`llm/retry.go:230-257`).
- `GenerateStream` does NOT use perAttemptCtx — defer cancel() would kill the async stream goroutine too early (`llm/retry.go:278`).

## Interactive SubAgent

- **Never hold `ia.mu` while calling Run().** Nested SubAgent → SpawnInteractiveSession → cleanupExpiredSessions → acquires `ia.mu` on other sessions → deadlock (`agent/interactive.go:440`).
- SubAgent errors are invisible to parent as Go error. Must embed error annotation in Content for parent LLM to detect (`agent/interactive.go:338`).
- Progress tree corruption: spawn-time closures point to stale progress info. Must rebuild ProgressNotifier from current ctx during send phase (`agent/interactive.go:446`).

## Concurrency

- **Never `defer` semaphore release inside a loop.** Slots accumulate, deadlock when iterations exceed capacity. Release immediately after Generate completes (`agent/engine_test.go:1529`).
- Non-blocking channel sends: always use `select` with `ctx.Done()` to prevent blocking on full channels during shutdown (`agent/agent.go:1229`).

## Context Management

- `Pipeline.Assemble()` used to panic on duplicate system messages. Now safely deduplicates, keeping first only (`agent/middleware.go:170`).
- Cd tool state: `buildToolContext` must update both `tc.CurrentDir` and `cfg.InitialCWD`, otherwise second buildToolContext reads stale path (`agent/engine_test.go:1514`).

## SQLite

- Pure Go via `modernc.org/sqlite` — no CGO required.
- Use `INSERT ... ON CONFLICT DO UPDATE` or `INSERT OR IGNORE` for TOCTOU-safe upserts.
- `INSERT ... WHERE NOT EXISTS` for concurrent-safe conditional inserts.

## Hugo Docs Site

- `hugo-geekdoc` theme auto-generates `<h1>` from frontmatter `title`. Custom override at `docs-site/layouts/_default/single.html` removes it to avoid duplicate h1 with markdown `#` headings.
- Theme loaded via Hugo modules (not git submodule). Empty `themes/hugo-geekdoc/` directory locally.

## Startup

- `NewOpenAILLM` loads model list asynchronously (goroutine). `ListModels()` returns fallback model immediately.
- Settings save is synchronous (`doSaveSettings` in `channel/cli_helpers.go`). All operations are local I/O — no network calls.
