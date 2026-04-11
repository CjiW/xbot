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
- **config/** — Configuration loading from `config.json`
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

Configuration (via `config.json`):
- `llm.provider` — `openai` or `anthropic`
- `llm.base_url`, `llm.api_key`, `llm.model`
- `llm.max_output_tokens` — Max tokens in LLM response (0 = model default)
- `embedding.provider`, `embedding.base_url`, `embedding.api_key`, `embedding.model`, `embedding.max_tokens`
- `agent.memory_provider` — `flat` (default) or `letta`
- `feishu.enabled`, `feishu.app_id`, `feishu.app_secret`, `feishu.encrypt_key`, `feishu.verification_token`, `feishu.domain`
- `qq.enabled`, `qq.app_id`, `qq.client_secret`
- `agent.work_dir` — Working directory
- `agent.prompt_file` — Custom prompt template (default `prompt.md`)
- `agent.max_iterations` — Max tool-call iterations (default `2000`)
- `agent.max_concurrency` — Max concurrent LLM calls (default `3`)
- `agent.enable_auto_compress` — Auto context compression (default `true`)
- `agent.max_context_tokens` — Max context tokens (default `200000`)
- `agent.context_mode` — Context ordering mode
- `agent.max_sub_agent_depth` — Max nested subagent depth (default `6`)
- `XBOT_ENCRYPTION_KEY` env var — AES-256-GCM key (base64-encoded 32 bytes) for encrypting API keys and OAuth tokens
- `oauth.enable`, `oauth.host`, `oauth.port`, `oauth.base_url`
- `sandbox.mode` — Sandbox mode: `none` (default), `docker`, or `remote`
- `sandbox.docker_image`, `sandbox.host_work_dir`, `sandbox.idle_timeout`
- `pprof.enable`, `pprof.host`, `pprof.port`
- `server.host`, `server.port`
- `log.level`, `log.format`
- `agent.llm_retry_attempts` — LLM retry attempts (default `5`)
- `startup_notify.channel`, `startup_notify.chat_id`
- `tavily_api_key` — API key for Tavily web search tool
