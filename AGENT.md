# xbot

> Go AI Agent framework with message bus + plugin architecture. Supports Feishu/QQ/CLI/Web channels, tool calling, pluggable memory, skills, subagents, MCP integration.

## Architecture

Single Go service. Entry point `cmd/xbot-cli/main.go` (CLI), `cmd/runner/` (runner).

Core packages:
- `agent/` — Agent loop, LLM orchestration, middleware pipeline, tool execution
- `channel/` — BubbleTea TUI (CLI), Feishu webhook handler, dispatcher
- `llm/` — LLM client abstraction (OpenAI, Anthropic) with retry wrapper
- `memory/` — Pluggable memory providers: `letta/` (full archival + core memory), `flat/` (basic)
- `tools/` — Built-in tools (Shell, Read, FileReplace, Grep, etc.), sandbox, tool hooks
- `session/` — Multi-tenant session management
- `storage/` — SQLite persistence, vector DB for archival memory
- `config/` — JSON config with env var overrides
- `prompt/` — Go embed templates for system prompt construction

### System Prompt Pipeline

`agent/middleware.go` — `MessagePipeline` executes ordered `MessageMiddleware` chain:

| Priority | Middleware | Key | Purpose |
|----------|-----------|-----|---------|
| 0 | SystemPromptMiddleware | `00_base` | Render prompt.md template |
| 5 | ProjectContextMiddleware | `05_project_context` | Load AGENT.md from CWD |
| 100 | SkillsCatalogMiddleware | `10_skills` | Inject skill names+descriptions |
| 110 | AgentsCatalogMiddleware | `15_agents` | Inject subagent catalog |
| 115 | PermissionControlMiddleware | `14_perm_control` | OS user permission control |
| 120 | MemoryMiddleware | `20_memory` | Core memory (persona/human/working_context) |
| 130 | SenderInfoMiddleware | `30_sender` | Sender name |
| 135 | LanguageMiddleware | `32_language` | Language preference |
| 200 | UserMessageMiddleware | — | Timestamp + user message wrapping |

### Tool Execution Flow

```
LLM Response → executeToolCalls() → execOne() → toolExecutor()
  → HookChain.RunPre() → tool.Execute() → HookChain.RunPost()
```

Tool hooks (`tools/hook.go`): LoggingHook, TimingHook, ApprovalHook.

### Agent Hierarchy ("Three Departments")

`agent/engine_wire.go` — SubAgent inherits parent's HookChain, LLMFactory, skill catalog.
SubAgents bypass pipeline; system prompt built manually in `buildSubAgentRunConfig`.

## Knowledge Files

- `docs/agent/conventions.md` — coding style, naming, error handling patterns
- `docs/agent/gotchas.md` — known pitfalls and workarounds

## Build & Test

```bash
go build ./...                  # compile all
go test ./...                   # run all tests
go test ./agent/ -run TestName  # specific test
golangci-lint run ./...         # lint (runs in pre-commit hook)
```

Pre-commit hook: gofmt → golangci-lint → go build → go test (all must pass).

## Key Conventions

- Pure Go SQLite via `modernc.org/sqlite` (no CGO)
- Error wrapping: `fmt.Errorf("context: %w", err)` — no external pkg/errors
- Logging: `log "xbot/logger"` → `log.WithField().Warn/Error/Debug`
- Config: `config.Config` struct, JSON file at `~/.xbot/config.json`, env var overrides
- Tools implement `tools.Tool` interface in `tools/` package
- Skills: `SKILL.md` with YAML frontmatter (name + description), stored in `tools/embed_skills/`
- Tool parameter schemas: array types MUST include `Items` field (OpenAI validates strictly)
- Middleware: register in `agent/context.go:initPipelines()`, key prefix controls sort order
- LLM client: `NewOpenAILLM` loads models async — never blocks startup
- Settings save: synchronous (`doSaveSettings`), all local I/O, no network calls
- Agent context: `MessageContext.Extra` map for cross-middleware data passing
