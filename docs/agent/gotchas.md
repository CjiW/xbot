# Known Pitfalls (Cross-Cutting)

## Concurrency

- **Never `defer` semaphore release inside a loop.** Slots accumulate, deadlock when iterations exceed capacity. Release immediately after Generate completes (`agent/engine_test.go:1529`).
- Non-blocking channel sends: always use `select` with `ctx.Done()` to prevent blocking on full channels during shutdown (`agent/agent.go:1229`).

## SQLite

- Pure Go via `modernc.org/sqlite` — no CGO required.
- Use `INSERT ... ON CONFLICT DO UPDATE` or `INSERT OR IGNORE` for TOCTOU-safe upserts.
- `INSERT ... WHERE NOT EXISTS` for concurrent-safe conditional inserts.

## Hugo Docs Site

- `hugo-geekdoc` theme auto-generates `<h1>` from frontmatter `title`. Custom override at `docs-site/layouts/_default/single.html` removes it.
- Theme loaded via Hugo modules (not git submodule).

## Startup

- `NewOpenAILLM` loads model list asynchronously. `ListModels()` returns fallback immediately.
- Settings save is synchronous (`doSaveSettings`) — all local I/O, no network calls.

## Per-Package Pitfalls

- `docs/agent/agent.md` — SubAgent deadlocks, context management
- `docs/agent/llm.md` — streaming bugs, retry context traps
- `docs/agent/tools.md` — tool schema Items requirement, hook chain behavior
