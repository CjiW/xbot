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
- **User-scoped semaphores must not be hardcoded to capacity 1 when one sender can own multiple independent chats/sessions (for example remote CLI windows authenticated as `admin`).** Size them from configured concurrency or key them by session, otherwise different windows will block each other and look like a leaked semaphore.

### Subscription & Model Resolution
- **`user_llm_subscriptions` DB is the single source of truth for ALL LLM config** (provider, model, base_url, api_key, max_output_tokens, thinking_mode). These keys are subscription-scoped — they must NOT appear in `settingHandlerRegistry`, `CLIRuntimeSettingKeys`, or `user_settings` table. Adding them back would cause startup `applyRuntimeSettings` to overwrite DB with stale values (e.g. name→provider, max_output_tokens→8192).
- **CLI subscriptions are in config.json, server subscriptions are in DB (`user_llm_subscriptions`).** `GetLLMForModel` must check both — `configSubsFn` (CLI) and `subscriptionSvc` (DB).
- **`UpdateCachedModels(subID)` nil-derefs if subID not in DB.** Always nil-check `sub` after `Get()`. Config subs have IDs not in DB.
- **`OnModelsLoaded` callback runs in `NewOpenAILLM`'s async goroutine** — must be concurrency-safe.
- **Tier fallback**: unconfigured tier → vanguard→balance→swift chain. Empty tier must NOT return default client with wrong model.
- **`createClientFromSub` uses sub's credentials with a *different* model** — verify target model is served by that endpoint.

### Context Management & Token Estimation
- **`maybeCompress` must NEVER use pure local token estimation.** Token counts must come from API responses (`lastPromptTokens`/`lastCompletionTokens`). The `no_data` fallback (no API data) skips all compress/masking checks with `totalTokens=0`. Tests must set `cfg.LastPromptTokens` to simulate a previous Run.
- **`buildToolContextExtras` uses `TenantSession.MemoryService()` for `MemorySvc`/`TenantID`, NOT `LettaMemory` type assertion.** These are tenant-level fields that work for all memory providers. Only LettaMemory-specific fields (CoreMemory, ArchivalMemory, ToolIndexer) stay inside the type assertion.
- **`ObservationMaskStore` and `OffloadStore` both persist to disk.** Mask uses `~/.xbot/mask/{tenantID}/{id}.json`, Offload uses `~/.xbot/offload_store/{session}/{id}.json`. `Recall` falls back to disk on memory miss. Both cleaned on compress and `/clear`.
- **`lastPersistedCount` MUST be updated after every compression path.** `runCompression`, `handleInputTooLong`, and `context_window_exceeded` handler all replace `s.messages` with a compressed `LLMView` (much shorter). If `lastPersistedCount` is not reset to `len(s.messages)`, `postToolProcessing`'s incremental persistence check (`len(s.messages) > s.lastPersistedCount`) will never be true, and all messages after compression are silently lost on restart. Set it only after successful `Session.Clear() + AddMessage()` writes.

### Startup
- `NewOpenAILLM` loads model list asynchronously. `ListModels()` returns fallback immediately.
- Settings save is synchronous — all local I/O, no network calls.
- **`SaveToFile` uses deep JSON merge to preserve unknown fields.** `json.Unmarshal` silently drops fields not in the Go struct. `SaveToFile` reads the existing disk file first and recursively merges struct JSON into it, so user-added custom fields (or future struct fields added in newer versions) survive load→save cycles. Never bypass `SaveToFile` with raw `json.Marshal` writes to config.json.

### CLI / BubbleTea
- **`parseKeyInput` with modifiers must NOT set `Text` field.** `Key.String()` returns `Text` if non-empty (ultraviolet `key.go:392`), bypassing `Keystroke()`. `{Code:'c', Text:"c", Mod:ModCtrl}.String()` → `"c"` not `"ctrl+c"`, breaking cancel.
- **Iteration snapshot deduplication**: PhaseDone + handleAgentMessage can both snapshot the same iteration. Always dedup by iteration number, preferring PhaseDone (has reasoning from server).
- **`ElapsedWall` must be set in ALL snapshot paths** — missing it causes total time to fall back to summing only last iteration's tool.Elapsed.
- **SubAgent remote mode: `convertWsProgressToCLI` must copy StreamContent/ReasoningStreamContent** from WsProgressPayload to CLIProgressPayload — otherwise stream content never reaches CLI.
- **SubAgent remote mode: tick chain breakage** — `tickCmd()` injection must be unconditional (not gated on `!m.fastTickActive`) in splashDoneMsg, PhaseDone return, and history reload paths. Conditional injection causes chain to break during session switches.
- **SubAgent session view: viewport freeze on return** — when main session's turn ended while viewing agent session, PhaseDone is detected on return but assistant reply is missing. Tick handler with `busy=false` must check `!m.renderCacheValid` as fallback.
- **SubAgent CWD inheritance**: `parent_cwd` metadata must fallback to `parentCtx.WorkingDir` when `parentCtx.CurrentDir` is empty (parent never Cd'd). `buildParentToolContext` must also fallback to `workspaceRoot`. Otherwise SubAgent starts in `a.workDir` (config value) instead of the parent's actual working directory.
- **Group chat members must be pre-spawned**: `CreateChat(type="group")` must auto-spawn each member agent and register AgentChannel in Dispatcher. Otherwise `@mentions` in SendMessage fail with "unknown channel: agent:role/instance".

### Windows
- `syscall.PROCESS_QUERY_LIMITED_INFORMATION` and `STILL_ACTIVE` not in Go stdlib — define as uint32 constants.
- `exec.ExitError.ExitCode()` is cross-platform; avoid `syscall.WaitStatus` type assertion.
- `signal.Notify(sigCh, syscall.SIGTSTP)` doesn't compile on Windows — use build-tagged files.
- PowerShell env output is newline-delimited, not null-delimited.

## Project Context

`ProjectContextMiddleware` auto-loads this file into system prompt. After code changes, update relevant Knowledge Files to keep documentation in sync.
