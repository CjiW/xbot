# Architecture

## Package Map

```
cmd/xbot-cli/     CLI entry point, app wiring, subscription management
cmd/runner/       Remote runner process (sandbox execution)
agent/            Agent loop, LLM orchestration, middleware pipeline, tool execution
channel/          Channel adapters: CLI (BubbleTea), Feishu, QQ, Web
llm/              LLM client abstraction (OpenAI, Anthropic), retry, streaming
memory/           Pluggable memory: letta/ (archival+core), flat/ (in-memory)
tools/            Built-in tools, sandbox, hooks, MCP integration
session/          Multi-tenant session management
storage/          SQLite persistence, vector DB for archival memory
config/           JSON config (~/.xbot/config.json), env var overrides
prompt/           Go embed templates for system prompt construction
event/            Event routing and trigger system
cron/             Cron scheduler for timed tasks
oauth/            OAuth 2.0 server and providers
```

## Message Flow

```
User Message → Bus.Inbound → Dispatcher → Channel.HandleMessage
  → Agent.HandleMessage → chatProcessLoop → runState.Run()
    → Pipeline.Assemble(system prompt) → LLM.Generate()
    → executeToolCalls() → toolExecutor() → HookChain → Tool.Execute()
    → results → LLM.Generate() → ... (loop up to maxIterations)
    → ExtractFinalReply() → Bus.Outbound → Dispatcher → Channel.Send()
```

## System Prompt Pipeline

`agent/middleware.go` — `MessagePipeline` executes ordered `MessageMiddleware` chain.
Registered in `agent/context.go:initPipelines()`.

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

## Tool Execution

```
LLM Response → executeToolCalls() → execOne() → toolExecutor()
  → HookChain.RunPre() → tool.Execute() → HookChain.RunPost()
```

Two modes (`agent/engine_run.go`):
- **Normal**: all serial
- **ReadWrite split**: read tools parallel (max 8), write tools serial, SubAgent concurrent

## Key Interfaces

| Interface | File | Purpose |
|-----------|------|---------|
| `LLM` | `llm/interface.go` | Generate, ListModels, Stream |
| `Tool` | `tools/interface.go` | Execute, Definition, Name |
| `ToolHook` | `tools/hook.go` | PreToolUse, PostToolUse |
| `Sandbox` | `tools/sandbox.go` | Run, Sync, Resolve |
| `Channel` | `channel/channel.go` | Start, Stop, Send |
| `MessageMiddleware` | `agent/middleware.go` | Process(mc) |
| `MemoryProvider` | `memory/memory.go` | Core + Archival memory |
| `AgentBackend` | `agent/backend.go` | Abstract local/remote agent execution |

## Concurrency Model

- Agent main loop: one goroutine per chat (`chatProcessLoop`)
- Commands: serialized via message queue (non-concurrent commands)
- Tool calls: controlled by `maxConcurrency` (global semaphore) + read/write split
- LLM calls: per-tenant semaphore (`llm/semaphore.go`)
- Background tasks: goroutine + WaitGroup, drained on shutdown

## AgentBackend

The `AgentBackend` interface (`agent/backend.go`) abstracts where the agent loop runs:

- **LocalBackend** (`agent/backend_local.go`): In-process `agent.Agent`. CLI creates the agent,
  runs `agent.Run()`, and executes tools locally. This is the default mode (no `--server` flag / `serve` subcommand).
- **RemoteBackend** (`agent/backend_remote.go`): Connects to a remote xbot server via WebSocket.
  Agent loop and tool execution run server-side; CLI is a display/input layer.

Server entry can now be launched either from the root binary (`main.go`) or from the CLI binary via
`xbot-cli serve [--config path]`. Both paths call the same reusable server startup logic in `serverapp/`.

The `serverapp/` package is structured as:
- `server.go` — `Run()` startup, channel registration, graceful shutdown
- `rpc.go` — generic RPC dispatch helpers (`rpc0`, `rpc1`, `rpc1void`, etc.)
- `rpc_table.go` — RPC method registry + auth helpers (`requireAdmin`, `ownOrAdmin`)
- `rpc_*.go` — handler groups by domain (settings, llm, subscription, session, tasks)
- `callbacks.go` — shared Runner/Registry/LLM callback builders (used by Web + Feishu)
- `setting_handlers.go` — runtime setting registry for server-side effects

Adding a new CLI RPC: define a typed handler method on `rpcContext` in the appropriate `rpc_*.go`,
then register it with one line in `buildRPCTable()` in `rpc_table.go`. No switch-case to update.

Both implement the same `AgentBackend` interface, so CLI code works identically regardless of mode.
Management methods (LLMFactory, SettingsService, etc.) return nil for RemoteBackend until the
WS protocol is extended with RPC support.

### RemoteBackend Connection

CLI connects to server's web channel WebSocket endpoint with query params:
- `?client_type=cli&token=<runner_token>` — token-based auth for CLI clients
- Server validates token against `runner_tokens` table, returns associated `userID`
- Messages use the same WS protocol as web browser clients (`wsMessage`/`wsClientMessage`)

## Per-Package Details

- `docs/agent/agent.md` — agent loop, middleware, SubAgent, context management
- `docs/agent/llm.md` — LLM clients, streaming pitfalls, retry behavior
- `docs/agent/tools.md` — built-in tools, hooks, sandbox types
- `docs/agent/channel.md` — CLI, Feishu, Web, QQ adapters
- `docs/agent/memory.md` — letta vs flat providers
- `docs/agent/conventions.md` — error handling, logging, testing, naming
- `docs/agent/gotchas.md` — cross-cutting pitfalls
