# xbot

> Go AI Agent framework with message bus + plugin architecture. Supports Feishu/QQ/CLI/Web channels, tool calling, pluggable memory, skills, subagents, MCP integration.

## Quick Reference

- Entry points: `cmd/xbot-cli/` (CLI), `cmd/runner/` (remote sandbox)
- Build: `go build ./...` | Test: `go test ./...` | Lint: `golangci-lint run ./...`
- Config: `~/.xbot/config.json`, env var overrides
- Pre-commit: gofmt → golangci-lint → go build → go test

## Knowledge Files

- `docs/agent/architecture.md` — package map, message flow, pipeline, key interfaces, concurrency
- `docs/agent/agent.md` — agent loop, middleware, SubAgent, context management, masking
- `docs/agent/llm.md` — LLM clients, streaming pitfalls, retry behavior
- `docs/agent/tools.md` — built-in tools, hooks, sandbox types
- `docs/agent/channel.md` — CLI, Feishu, Web, QQ adapters
- `docs/agent/memory.md` — letta vs flat providers
- `docs/agent/conventions.md` — error handling, logging, testing, naming, build

## Gotchas — MUST READ Before Any Code Change

### Concurrency
- **Never `defer` semaphore release inside a loop.** Deadlock when iterations exceed capacity. Release immediately after Generate completes.
- Non-blocking channel sends: always use `select` with `ctx.Done()` to prevent blocking on full channels during shutdown.

### Subscription & Model Resolution
- **CLI subscriptions are in config.json, server subscriptions are in DB (`user_llm_subscriptions`).** `GetLLMForModel` must check both — `configSubsFn` (CLI) and `subscriptionSvc` (DB).
- **`UpdateCachedModels(subID)` nil-derefs if subID not in DB.** Always nil-check `sub` after `Get()`. Config subs have IDs not in DB.
- **`OnModelsLoaded` callback runs in `NewOpenAILLM`'s async goroutine** — must be concurrency-safe.
- **Tier fallback**: unconfigured tier → vanguard→balance→swift chain. Empty tier must NOT return default client with wrong model.
- **`createClientFromSub` uses sub's credentials with a *different* model** — verify target model is served by that endpoint.

### Startup
- `NewOpenAILLM` loads model list asynchronously. `ListModels()` returns fallback immediately.
- Settings save is synchronous — all local I/O, no network calls.

### CLI / BubbleTea
- **`parseKeyInput` with modifiers must NOT set `Text` field.** `Key.String()` returns `Text` if non-empty (ultraviolet `key.go:392`), bypassing `Keystroke()`. `{Code:'c', Text:"c", Mod:ModCtrl}.String()` → `"c"` not `"ctrl+c"`, breaking cancel.
- **Iteration snapshot deduplication**: PhaseDone + handleAgentMessage can both snapshot the same iteration. Always dedup by iteration number, preferring PhaseDone (has reasoning from server).
- **`ElapsedWall` must be set in ALL snapshot paths** — missing it causes total time to fall back to summing only last iteration's tool.Elapsed.

### Windows
- `syscall.PROCESS_QUERY_LIMITED_INFORMATION` and `STILL_ACTIVE` not in Go stdlib — define as uint32 constants.
- `exec.ExitError.ExitCode()` is cross-platform; avoid `syscall.WaitStatus` type assertion.
- `signal.Notify(sigCh, syscall.SIGTSTP)` doesn't compile on Windows — use build-tagged files.
- PowerShell env output is newline-delimited, not null-delimited.

## Project Context

`ProjectContextMiddleware` auto-loads this file into system prompt. After code changes, update relevant Knowledge Files to keep documentation in sync.
