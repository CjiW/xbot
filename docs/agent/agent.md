# agent/ — Agent Loop & Orchestration

## Core Files

| File | Purpose |
|------|---------|
| `agent.go` | Agent struct, lifecycle, HandleMessage, Run loop (~2366 lines) |
| `engine.go` | Engine interface, SubAgent progress, nested context |
| `engine_run.go` | Tool call execution: execOne, concurrency, read/write split (~1461 lines) |
| `engine_wire.go` | Dependency injection: buildSubAgentRunConfig, HookChain/LLMFactory inheritance (~1282 lines) |
| `context.go` | MessageContext, PromptData, initPipelines() |
| `middleware.go` | MessagePipeline, MessageMiddleware interface |
| `middleware_builtin.go` | Built-in middleware implementations |
| `interactive.go` | Interactive SubAgent: multi-turn sessions, inspect/tail (~870 lines) |
| `compress.go` | Context compression via LLM (~600 lines) |
| `reminder.go` | System reminder injection (<system-reminder>) |
| `skills.go` | SkillStore: directory scan, TTL cache, catalog generation |
| `agents.go` | AgentStore: subagent role discovery, catalog generation |
| `llm_factory.go` | LLMFactory: user custom LLM creation/caching |
| `registry.go` | RegistryManager: skill/agent publishing, installation |
| `settings.go` | SettingsService: channel/user level settings |

## Pipeline Registration

Middleware registered in `agent/context.go:initPipelines()`.
Execution order defined by Priority field (see `architecture.md` for full table).

## SubAgent Architecture

SubAgents bypass the pipeline. System prompt built in `buildSubAgentRunConfig` (`engine_wire.go`).
Inherits parent's: HookChain (same pointer), LLMFactory, skill catalog, tool context extras.

Max nesting depth: 6. Three levels: main → SubAgent → SubSubAgent.

## Interactive SubAgent Architecture

Interactive SubAgents maintain persistent multi-turn sessions via `InteractiveAgent` structs stored in `Agent.interactiveSubAgents` (sync.Map). Key flows:

- **Spawn**: `SpawnInteractiveSession` → creates session, eager-saves user message + final assistant reply to `ia.messages` + DB
- **Send**: `SendToInteractiveSession` → eager-saves user message, runs agent loop, eager-saves final assistant reply
- **Inspect/Tail**: read-only access to `ia.messages`
- **Unload**: `destroyInteractiveSession` → saves memory, cleans DB, removes from map

### Interactive Session Key Format

`interactiveKey = channel:chatID/roleName:instance`

### Remote Mode Persistence

In remote mode, SubAgent messages must be eagerly persisted (not deferred):
- User messages saved immediately in `SpawnInteractiveSession`/`SendToInteractiveSession` before `Run()`
- Final assistant reply appended to `ia.messages` and persisted after `Run()` completes
- `GetOrCreateSession` may return stale tenant after server restart — call `Clear()` to reset
- Placeholder sessions must include user message (not just system prompt)

### OutboundMessage Routing

SubAgent outbound messages go to **parent's channel/chatID** (never the agent session view). The CLI detects these and routes accordingly.

## Interactive SubAgent Pitfalls

- **Never hold `ia.mu` while calling Run()** — deadlock via nested SpawnInteractiveSession → cleanupExpiredSessions
- SubAgent errors invisible as Go error — must embed in Content
- Progress tree corruption from stale closures — rebuild ProgressNotifier from current ctx
- **`handleFinalResponse` must set `ThinkingContent`** on the prompt data — otherwise PhaseDone assistant synthesis has empty content
- **Stream content updates must snapshot** — `StreamContentFunc`/`ReasoningStreamContentFunc` must update `lastProgressSnapshot` for CLI to render

## Context Management

- `Pipeline.Assemble()` safely deduplicates system messages (used to panic) (`middleware.go:170`)
- Cd tool: must update both `tc.CurrentDir` and `cfg.InitialCWD` (`engine_test.go:1514`)
- Dynamic context injection detects CWD changes via `dynamic_context.go`

## Observation Masking

Long tool results auto-masked with `masked:mk_xxxx` placeholders.
Use `recall_masked` tool to retrieve. Configurable thresholds in `observation_masking.go`.
