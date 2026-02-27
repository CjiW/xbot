# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Development Commands

```bash
make dev          # Run in development mode (go run .)
make build        # Build the xbot binary
make test         # Run all tests
make fmt          # Format Go code
make clean-db     # Clear .xbot data, MEMORY.md, HISTORY.md, cron.json
```

## Architecture Overview

xbot is an AI Agent with a message bus + plugin architecture:

```
Channel (feishu) → MessageBus → Agent (LLM + Tools) → MessageBus → Channel
                                ↓
                            Memory (MEMORY.md / HISTORY.md)
```

### Core Components

- **bus/** — Inbound/Outbound message channels for decoupling channels from agent
- **channel/** — Channel interface (feishu.go). Dispatcher routes messages to channels
- **agent/** — Agent loop: process inbound → LLM → tool calls → response
- **llm/** — LLM interface supporting OpenAI-compatible and CodeBuddy providers
- **tools/** — Tool registry. Tools implement `Execute(ctx *ToolContext, input string) (*ToolResult, error)`

### Agent Loop (agent/agent.go:254)

The `runLoop` function implements the core iteration:
1. Call LLM with messages + tool definitions
2. If no tool calls → return content
3. Execute each tool via `executeTool`
4. Append tool results as `tool` messages
5. Repeat until max iterations (default 20)

### Memory System (agent/memory.go)

Dual-layer memory:
- **MEMORY.md** — Long-term facts, injected into system prompt
- **HISTORY.md** — Append-only event log, searchable via Grep tool

Auto-consolidation triggers when session exceeds `AGENT_MEMORY_WINDOW` (default 50). LLM summarizes old messages into MEMORY.md and HISTORY.md, then context is trimmed.

### Context Construction (agent/context.go)

Message order optimized for KV-cache命中率:
```
System prompt (stable) → Skills (semi-stable) → Memory (changes) → Time (always changes)
```

The `PromptLoader` supports hot-reloading: changes to `PROMPT_FILE` are picked up without restart.

## Code Conventions

- **Logging**: Use `log "xbot/logger"` (logrus wrapper). Log tools calls with elapsed time
- **Tool results**: Return `*ToolResult` with `Summary` (for LLM context) and optional `Detail` (for frontend only)
- **Sorting**: Registry lists tools by name to ensure stable order for KV-cache
- **Error handling**: Tools return errors as string content in `ToolResult`; let Agent handle logging
- **Working directory**: All file operations relative to `WORK_DIR` (default current directory)

## Adding New Tools

1. Implement `Tool` interface in `tools/`:
```go
type MyTool struct{}
func (t *MyTool) Name() string { return "my_tool" }
func (t *MyTool) Description() string { return "..." }
func (t *MyTool) Parameters() []llm.ToolParam { return [...] }
func (t *MyTool) Execute(ctx *ToolContext, input string) (*ToolResult, error)
```

2. Register in `tools.DefaultRegistry()` (tools/interface.go:106)

## Configuration

All config via environment variables or `.env` file:
- `LLM_PROVIDER` — `openai` or `codebuddy`
- `LLM_BASE_URL`, `LLM_API_KEY`, `LLM_MODEL`
- `FEISHU_ENABLED`, `FEISHU_APP_ID`, `FEISHU_APP_SECRET`
- `WORK_DIR` — Working directory for file operations
- `PROMPT_FILE` — Optional custom system prompt template (Go template syntax)
- `AGENT_MAX_ITERATIONS`, `AGENT_MEMORY_WINDOW`
