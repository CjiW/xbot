# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Development Commands

```bash
make dev          # Run server in development mode (go run)
make build        # Build xbot binary
make run          # Build and run server
make test         # Run all tests with race detection
go test -v -race ./tools -run TestFoo   # Run a single test
make fmt          # Format Go code
make lint         # Run golangci-lint
make ci           # Run lint → build → test → web-lint → web-build
make clean        # Remove binary and coverage output
make clean-memory # Clear .xbot/ data
make install-cli  # Build xbot-cli and install to /usr/local/bin
make web-build    # Build React frontend (cd web && yarn build)
make web-lint     # Lint frontend (cd web && yarn lint)
make web-dev      # Start Vite dev server for frontend
```

## Entry Points

- `main.go` — Server entry point (`xbot` binary)
- `cmd/xbot-cli/main.go` — CLI entry point (TUI via BubbleTea v2, supports local and remote backend)
- `cmd/runner/main.go` — Remote sandbox runner process

## Architecture

```
Channel → MessageBus → Dispatcher → Agent → LLM → Tools
                                        ↓
                                  Memory (flat/letta)
```

**Core components:**
- **bus/** — Inbound/Outbound message channels
- **channel/** — IM adapters (CLI BubbleTea, Feishu, QQ, NapCat/OneBot 11, Web), dispatcher routes messages
- **agent/** — Agent loop: LLM → tool calls → response. Also contains middleware pipeline and `AgentBackend` abstraction
- **llm/** — LLM clients (OpenAI-compatible, Anthropic), retry wrapper, streaming
- **tools/** — Tool registry; implement `Tool` interface and register in `DefaultRegistry()`
- **memory/** — Memory providers: `flat` (default) or `letta` (three-tier MemGPT)
- **config/** — Configuration loading from environment variables / `.env` (server) or `~/.xbot/config.json` (CLI)
- **cron/** — Scheduled task scheduler (cron expressions + one-shot `at`)
- **crypto/** — AES-256-GCM encryption utilities for API keys and OAuth tokens
- **logger/** — Logrus-based structured logging with file rotation
- **oauth/** — OAuth 2.0 server and flow management for user-level authorization
- **pprof/** — Optional pprof debug endpoint
- **session/** — Multi-tenant session management (channel + chatID isolation)
- **storage/** — SQLite persistence (sessions, memory, tenants, migrations)
- **version/** — Build version info injected via `-ldflags`
- **prompt/** — Go-embedded system prompt templates with channel-specific overrides

**Agent Loop** (`agent/agent.go`):
1. Call LLM with messages + tool definitions
2. Execute tools, append results
3. Repeat until max iterations (default `2000`)

**AgentBackend** (`agent/backend.go`):
- `LocalBackend` — In-process agent loop (default for server and `xbot-cli --local`)
- `RemoteBackend` — CLI connects to remote server via WebSocket; agent loop runs server-side
- Both implement the same interface so CLI code works identically regardless of mode

**System Prompt Pipeline** (`agent/middleware.go`):
Ordered `MessageMiddleware` chain registered in `agent/context.go:initPipelines()`:
- `00_base` — Render prompt.md template
- `05_project_context` — Load `AGENT.md` from CWD into system prompt
- `10_skills` — Inject skill catalog
- `15_agents` — Inject subagent catalog
- `14_perm_control` — OS user permission control
- `20_memory` — Core memory (persona/human/working_context)
- `30_sender` — Sender name
- `32_language` — Language preference

**Tool Execution Modes** (`agent/engine_run.go`):
- **Normal**: all tools serial
- **Read/Write split**: read tools parallel (max 8), write tools serial, SubAgent concurrent

**Memory System:**
- `flat` (default): Long-term memory blob + event history (Grep-searchable)
- `letta`: Core Memory (SQLite blocks) + Archival Memory (chromem-go vectors) + Recall Memory (FTS5)

## Critical Gotchas

These are documented in `AGENT.md`; read it before any code change.

### Concurrency
- **Never `defer` semaphore release inside a loop.** Release immediately after `Generate` completes, or iterations exceeding capacity deadlock.
- Non-blocking channel sends: always use `select` with `ctx.Done()` to prevent blocking on full channels during shutdown.
- **User-scoped semaphores must not be hardcoded to capacity 1** when one sender can own multiple independent chats/sessions (e.g. remote CLI windows authenticated as `admin`). Size them from configured concurrency or key by session.

### Subscription & LLM Config
- **`user_llm_subscriptions` DB is the single source of truth for ALL LLM config** (provider, model, base_url, api_key, max_output_tokens, thinking_mode). These keys must NOT appear in `settingHandlerRegistry`, `CLIRuntimeSettingKeys`, or `user_settings` table. Adding them back causes startup `applyRuntimeSettings` to overwrite DB with stale values.
- **CLI subscriptions are in `config.json`, server subscriptions are in DB.** `GetLLMForModel` must check both — `configSubsFn` (CLI) and `subscriptionSvc` (DB).
- **`UpdateCachedModels(subID)` nil-derefs if subID not in DB.** Always nil-check `sub` after `Get()`.

### Context Management & Compression
- **`maybeCompress` must NEVER use pure local token estimation.** Token counts must come from API responses (`lastPromptTokens`/`lastCompletionTokens`). The `no_data` fallback skips all compress/masking checks with `totalTokens=0`.
- **`lastPersistedCount` MUST be updated after every compression path.** `runCompression`, `handleInputTooLong`, and `context_window_exceeded` handler all replace `s.messages` with a compressed `LLMView`. If `lastPersistedCount` is not reset to `len(s.messages)`, `postToolProcessing`'s incremental persistence check will never be true, and all messages after compression are silently lost on restart.
- **`ObservationMaskStore` and `OffloadStore` both persist to disk.** Mask uses `~/.xbot/mask/{tenantID}/{id}.json`, Offload uses `~/.xbot/offload_store/{session}/{id}.json`. Both cleaned on compress and `/clear`.

## Code Conventions

- Logging: `log "xbot/logger"` (logrus wrapper)
- Tool results: Return `*ToolResult` with `Summary` (LLM context) and optional `Detail` (frontend)
- Error handling: Tools return errors as string in `ToolResult`
- File operations relative to `WORK_DIR`
- Pre-commit workflow: gofmt → golangci-lint → go build → go test

## Configuration

**Server** reads from environment variables or `.env` file. **CLI** (`xbot-cli`) uses `~/.xbot/config.json` with env var overrides.

Key environment variables:
- `LLM_PROVIDER` — `openai` or `anthropic`
- `LLM_BASE_URL`, `LLM_API_KEY`, `LLM_MODEL`
- `LLM_EMBEDDING_PROVIDER`, `LLM_EMBEDDING_BASE_URL`, `LLM_EMBEDDING_API_KEY`, `LLM_EMBEDDING_MODEL`, `LLM_EMBEDDING_MAX_TOKENS`
- `MEMORY_PROVIDER` — `flat` (default) or `letta`
- `FEISHU_ENABLED`, `FEISHU_APP_ID`, `FEISHU_APP_SECRET`, `FEISHU_ENCRYPT_KEY`, `FEISHU_VERIFICATION_TOKEN`, `FEISHU_DOMAIN`
- `QQ_ENABLED`, `QQ_APP_ID`, `QQ_CLIENT_SECRET`
- `NAPCAT_ENABLED`, `NAPCAT_WS_URL`, `NAPCAT_TOKEN` — OneBot 11 adapter
- `WEB_ENABLED`, `WEB_HOST`, `WEB_PORT`, `WEB_STATIC_DIR`, `WEB_UPLOAD_DIR` — Browser chat channel
- `WORK_DIR` — Working directory
- `PROMPT_FILE` — Custom prompt template (default `prompt.md`)
- `AGENT_MAX_ITERATIONS` — Max tool-call iterations (default `2000`)
- `AGENT_MAX_CONCURRENCY` — Max concurrent LLM calls (default `3`)
- `AGENT_ENABLE_AUTO_COMPRESS` — Auto context compression (default `true`)
- `AGENT_MAX_CONTEXT_TOKENS` — Max context tokens (default `200000`)
- `AGENT_CONTEXT_MODE` — Context ordering mode
- `MAX_SUBAGENT_DEPTH` — Max nested subagent depth (default `6`)
- `XBOT_ENCRYPTION_KEY` — AES-256-GCM key (base64-encoded 32 bytes) for encrypting API keys and OAuth tokens
- `OAUTH_ENABLE`, `OAUTH_HOST`, `OAUTH_PORT`, `OAUTH_BASE_URL`
- `SANDBOX_MODE` — Sandbox mode: `none` (default), `docker`, or `remote`
- `SANDBOX_DOCKER_IMAGE`, `HOST_WORK_DIR`, `SANDBOX_IDLE_TIMEOUT_MINUTES`
- `PPROF_ENABLE`, `PPROF_HOST`, `PPROF_PORT`
- `SERVER_HOST`, `SERVER_PORT`
- `LOG_LEVEL`, `LOG_FORMAT`
- `LLM_RETRY_ATTEMPTS` — LLM retry attempts (default `5`)
- `STARTUP_NOTIFY_CHANNEL`, `STARTUP_NOTIFY_CHAT_ID`
- `TAVILY_API_KEY` — API key for Tavily web search tool
