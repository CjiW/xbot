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

## Subscription & Model Resolution

- **CLI stores subscriptions in config.json, server stores in DB (`user_llm_subscriptions` table).** These are separate data sources. `GetLLMForModel` must check both via `configSubsFn` closure (CLI) and `subscriptionSvc` (DB).
- **`configSubToLLMSubscription` creates lightweight `sqlite.LLMSubscription` from config — has `Model` field but no `CachedModels`.** Config subs only match on their `Model` field, not on cached API model lists.
- **`UpdateCachedModels(subID)` crashes (nil deref on `sub.Model`) if subID doesn't exist in DB.** Always nil-check `sub` after `Get()`. Config subs have IDs that don't exist in DB.
- **`OnModelsLoaded` callback runs in `NewOpenAILLM`'s async goroutine** — must be safe for concurrent use. Config-only subs will trigger DB write that fails gracefully (nil check in `UpdateCachedModels`).
- **Tier fallback chain**: unconfigured tier falls through vanguard→balance→swift, returns first configured or empty with `usedTier=false`. Empty tier must NOT return default client with wrong model.
- **`createClientFromSub` uses subscription's credentials with a *different* model** — always verify the target model is actually served by that subscription's endpoint.

## Per-Package Pitfalls

- `docs/agent/agent.md` — SubAgent deadlocks, context management
- `docs/agent/llm.md` — streaming bugs, retry context traps
- `docs/agent/tools.md` — tool schema Items requirement, hook chain behavior

## Windows

- `syscall.PROCESS_QUERY_LIMITED_INFORMATION` and `STILL_ACTIVE` are NOT in Go's stdlib `syscall` — define as uint32 constants (0x1000, 259)
- `exec.ExitError.ExitCode()` is cross-platform; avoid `syscall.WaitStatus` type assertion (fails on Windows)
- `signal.Notify(sigCh, syscall.SIGTSTP)` doesn't compile on Windows — use build-tagged files
- PowerShell env output is newline-delimited, not null-delimited — different parsing needed in `mcp_common.go`
