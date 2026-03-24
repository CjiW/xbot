# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Development Commands

```bash
make dev          # Run in development mode
make build        # Build the xbot binary
make run          # Build and run
make test         # Run all tests with race detection
make fmt          # Format Go code
make lint         # Run golangci-lint
make ci           # Run lint → build → test
make clean        # Remove binary and coverage output
make clean-memory # Clear .xbot data
```

## Architecture

```
Channel → MessageBus → Agent → LLM → Tools
                          ↓
                    Memory (flat/letta)
```

**Core components:**
- **bus/** — Inbound/Outbound message channels
- **channel/** — IM channels (feishu, qq), dispatcher routes messages
- **agent/** — Agent loop: LLM → tool calls → response
- **llm/** — LLM clients (OpenAI-compatible, Anthropic)
- **tools/** — Tool registry; implement `Tool` interface and register in `DefaultRegistry()`
- **memory/** — Memory providers: `flat` (default) or `letta` (three-tier MemGPT)
- **config/** — Configuration loading from environment variables / `.env`
- **cron/** — Scheduled task scheduler (cron expressions + one-shot `at`)
- **crypto/** — AES-256-GCM encryption utilities for API keys and OAuth tokens
- **logger/** — Logrus-based structured logging with file rotation
- **oauth/** — OAuth 2.0 server and flow management for user-level authorization
- **pprof/** — Optional pprof debug endpoint
- **session/** — Multi-tenant session management (channel + chatID isolation)
- **storage/** — SQLite persistence (sessions, memory, tenants, migrations)
- **version/** — Build version info injected via `-ldflags`

**Agent Loop** (`agent/agent.go`):
1. Call LLM with messages + tool definitions
2. Execute tools, append results
3. Repeat until max iterations (default 100)

**Memory System:**
- `flat` (default): Long-term memory blob + event history (Grep-searchable)
- `letta`: Core Memory (SQLite blocks) + Archival Memory (chromem-go vectors) + Recall Memory (FTS5)

## Code Conventions

- Logging: `log "xbot/logger"` (logrus wrapper)
- Tool results: Return `*ToolResult` with `Summary` (LLM context) and optional `Detail` (frontend)
- Error handling: Tools return errors as string in `ToolResult`
- File operations relative to `WORK_DIR`

## Configuration

Environment variables (or `.env`):
- `LLM_PROVIDER` — `openai` or `anthropic`
- `LLM_BASE_URL`, `LLM_API_KEY`, `LLM_MODEL`
- `LLM_EMBEDDING_PROVIDER`, `LLM_EMBEDDING_BASE_URL`, `LLM_EMBEDDING_API_KEY`, `LLM_EMBEDDING_MODEL`, `LLM_EMBEDDING_MAX_TOKENS`
- `MEMORY_PROVIDER` — `flat` (default) or `letta`
- `FEISHU_ENABLED`, `FEISHU_APP_ID`, `FEISHU_APP_SECRET`, `FEISHU_ENCRYPT_KEY`, `FEISHU_VERIFICATION_TOKEN`, `FEISHU_DOMAIN`
- `QQ_ENABLED`, `QQ_APP_ID`, `QQ_CLIENT_SECRET`
- `WORK_DIR` — Working directory
- `PROMPT_FILE` — Custom prompt template (default `prompt.md`)
- `SINGLE_USER` — Single-user mode (default `false`)
- `AGENT_MAX_ITERATIONS` — Max tool-call iterations (default `100`)
- `AGENT_MAX_CONCURRENCY` — Max concurrent LLM calls (default `3`)
- `AGENT_MEMORY_WINDOW` — Memory consolidation trigger (default `50`)
- `AGENT_ENABLE_AUTO_COMPRESS` — Auto context compression (default `true`)
- `AGENT_MAX_CONTEXT_TOKENS` — Max context tokens (default `100000`)
- `AGENT_CONTEXT_MODE` — Context ordering mode
- `MAX_SUBAGENT_DEPTH` — Max nested subagent depth (default `6`)
- `XBOT_ENCRYPTION_KEY` — AES-256-GCM key (base64-encoded 32 bytes) for encrypting API keys and OAuth tokens
- `OAUTH_ENABLE`, `OAUTH_HOST`, `OAUTH_PORT`, `OAUTH_BASE_URL`
- `SANDBOX_MODE` — Sandbox mode: `docker` (default) or `none`
- `SANDBOX_DOCKER_IMAGE`, `HOST_WORK_DIR`, `SANDBOX_IDLE_TIMEOUT_MINUTES`
- `PPROF_ENABLE`, `PPROF_HOST`, `PPROF_PORT`
- `SERVER_HOST`, `SERVER_PORT`
- `LOG_LEVEL`, `LOG_FORMAT`
- `LLM_RETRY_ATTEMPTS` — LLM retry attempts (default `5`)
- `STARTUP_NOTIFY_CHANNEL`, `STARTUP_NOTIFY_CHAT_ID`
- `TAVILY_API_KEY` — API key for Tavily web search tool
