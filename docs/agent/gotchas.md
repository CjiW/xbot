# Known Pitfalls (Cross-Cutting)

## Concurrency

- **Never `defer` semaphore release inside a loop.** Slots accumulate, deadlock when iterations exceed capacity. Release immediately after Generate completes (`agent/engine_test.go:1529`).
- Non-blocking channel sends: always use `select` with `ctx.Done()` to prevent blocking on full channels during shutdown (`agent/agent.go:1229`).

## SQLite

- Pure Go via `modernc.org/sqlite` — no CGO required.
- Use `INSERT ... ON CONFLICT DO UPDATE` or `INSERT OR IGNORE` for TOCTOU-safe upserts.
- `INSERT ... WHERE NOT EXISTS` for concurrent-safe conditional inserts.
- **CRITICAL: modernc.org/sqlite serializes `time.Time` differently from RFC3339 storage format.** When you pass a `time.Time` parameter to `Exec`, the driver formats it with a space separator (`2026-04-14 20:34:25+08:00`), but timestamps stored via `time.Now().Format(time.RFC3339)` use a T separator (`2026-04-14T20:34:25+08:00`). SQLite compares DATETIME as strings lexicographically, and space (`0x20`) sorts before `T` (`0x54`), so `WHERE created_at > ?` with a raw `time.Time` parameter will match ALL rows. **Always format timestamps as strings explicitly** before passing to SQL: `cutoff.Format(time.RFC3339)`. See `storage/sqlite/session.go:PurgeNewerThan` for the fix.

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

## Windows

- `syscall.PROCESS_QUERY_LIMITED_INFORMATION` and `STILL_ACTIVE` are NOT in Go's stdlib `syscall` — define as uint32 constants (0x1000, 259)
- `exec.ExitError.ExitCode()` is cross-platform; avoid `syscall.WaitStatus` type assertion (fails on Windows)
- `signal.Notify(sigCh, syscall.SIGTSTP)` doesn't compile on Windows — use build-tagged files
- PowerShell env output is newline-delimited, not null-delimited — different parsing needed in `mcp_common.go`
