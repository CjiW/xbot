# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Development Commands

```bash
make dev          # Run in development mode
make build        # Build the xbot binary
make test         # Run all tests with race detection
make fmt          # Format Go code
make lint         # Run golangci-lint
make ci           # Run lint → build → test
make clean-db     # Clear .xbot data
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
- **llm/** — LLM clients (OpenAI-compatible, CodeBuddy, Anthropic)
- **tools/** — Tool registry; implement `Tool` interface and register in `DefaultRegistry()`
- **memory/** — Memory providers: `flat` (default) or `letta` (three-tier MemGPT)

**Agent Loop** (`agent/agent.go`):
1. Call LLM with messages + tool definitions
2. Execute tools, append results
3. Repeat until max iterations (default 20)

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
- `LLM_PROVIDER` — `openai`, `codebuddy`, or `anthropic`
- `LLM_BASE_URL`, `LLM_API_KEY`, `LLM_MODEL`
- `MEMORY_PROVIDER` — `flat` (default) or `letta`
- `FEISHU_ENABLED`, `FEISHU_APP_ID`, `FEISHU_APP_SECRET`
- `WORK_DIR` — Working directory
- `SINGLE_USER` — Single-user mode (default false): disables per-user isolation for workspace, memory, MCP config, and LLM config
