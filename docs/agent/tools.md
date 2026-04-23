# tools/ — Built-in Tools

## Key Files

| File | Purpose |
|------|---------|
| `interface.go` | Tool interface, SubAgentManager, SessionMCPManagerProvider |
| `hook.go` | ToolHook interface, HookChain (Pre/Post hooks) |
| `hook_builtin.go` | LoggingHook, TimingHook |
| `approval.go` | ApprovalHook (permission control) |
| `sandbox.go` | Sandbox interface (Run, Sync, Resolve) |
| `sandbox_router.go` | Selects sandbox type (none/docker/remote) |
| `docker_sandbox.go` | Docker sandbox implementation |
| `remote_sandbox.go` | Remote runner sandbox (~1300 lines) |
| `cd.go` | Cd tool (directory switching, persists across turns) |
| `edit.go` | FileReplace + FileCreate tools |
| `read.go` | Read tool (line-numbered output) |
| `grep.go` | Grep tool (Go RE2 regex) |
| `glob.go` | Glob tool (pattern matching) |
| `fetch.go` | Fetch tool (HTTP → markdown) |
| `shell.go` | Shell tool (command execution) |
| `shell_unix.go` | Unix process helpers: setProcessAttrs (Setpgid), killProcessTree (-pgid SIGKILL), isProcessAlive (Signal(0)), defaultShell, loginShellArgs |
| `shell_windows.go` | Windows process helpers: setProcessAttrs (CREATE_NEW_PROCESS_GROUP), killProcessTree (taskkill /T /F), isProcessAlive (OpenProcess), defaultShell (powershell.exe), loginShellArgs |
| `none_sandbox.go` | None sandbox (local execution). Uses platform helpers from shell_unix/shell_windows.go |
| `mcp_common.go` | MCP protocol definitions |
| `mcp_remote_transport.go` | MCP HTTP transport |
| `memory_tools.go` | Core memory tools (append/replace/rethink/search/recall) — letta only |
| `knowledge_tools.go` | Project knowledge tools (write/list) — provider-agnostic |
| `flat_memory_tools.go` | Flat memory tools (read/write/list) — flat provider only |
| `context_edit.go` | ContextEdit tool (conversation history surgery) |
| `cron.go` | Cron tool (scheduled tasks) |
| `task_manager.go` | Background task management |
| `checkpoint.go` | CheckpointHook + CheckpointStore (Ctrl+K rewind file rollback) |
| `create_chat.go` | CreateChat tool — create agent private chat or moderated group chat |
| `send_message.go` | SendMessage tool — unified routing to agent/group/IM targets |
| `group_state.go` | GroupState struct + sync.Map store for meeting-mode group chats |

## Tool Schema Rule

**Array types MUST include `Items` field.** OpenAI API rejects schemas without it.
```go
Items: &llm.ToolParamItems{Type: "string"}
```

## Hook Chain

Three built-in hooks: LoggingHook, TimingHook, ApprovalHook.
Fourth hook: CheckpointHook (file snapshot for Ctrl+K rewind, CLI-only).
Chain is shared across main Agent and all SubAgents (same instance pointer).
Max 20 hooks (`MaxHookChainLen` in `hook.go`).

PreToolUse: does NOT short-circuit on error (records first error, continues all).
PostToolUse: guarantees all hooks run even if one panics (recover).

## Sandbox Types

- `none`: direct execution (default). Uses `/bin/bash -l -c` on Unix, `powershell.exe -Command` on Windows
- `docker`: Docker container per OS user (always Linux)
- `remote`: remote runner process via runner protocol (always Linux)

## Agent Communication (CreateChat + SendMessage)

Two tools for inter-agent messaging via the Dispatcher's AgentChannel mechanism.

### CreateChat
- **type=agent**: Spawns an interactive SubAgent (`InteractiveSubAgentManager.SpawnInteractive`), registers an `AgentChannel` in the Dispatcher. Returns `agent:<role>/<instance>` address.
- **type=group**: Creates a `GroupState` in the global `sync.Map`. Members are address strings (not pre-spawned). Returns `group:<id>` address.

### SendMessage
Routes by address prefix:
- `agent:*` → `Dispatcher.SendMessage()` → `AgentChannel.Send()` (RPC, blocks for reply)
- `group:*` → `GroupState` meeting mode: parses `@agent:xxx` mentions, builds history prompt, sends to each mentioned agent sequentially
- `feishu:/web:/qq:/cli:` → `Dispatcher.SendMessage()` → IM channel (fire-and-forget)

### Meeting Mode (Group)
- Moderator (caller) controls who speaks via `@agent:role/instance` mentions
- Messages without @mentions are recorded in history but don't trigger agents
- @mentioned agents receive full discussion history + current question
- Round counter increments per moderator message WITH mentions; group auto-closes at `max_rounds` (default 10)
- `group_state.go`: `GroupState` struct with `sync.Mutex`, global `groupStore sync.Map`
- `channel/agent_channel.go`: `AgentChannel` wraps SubAgent as Dispatcher Channel with per-request RPC reply channels

## Windows Support

- **None sandbox only** — docker/remote sandboxes are always Linux
- Shell: `powershell.exe -Command` replaces `/bin/bash -l -c`
- Process management: `taskkill /T /F` replaces `kill(-pgid, SIGKILL)`; `CREATE_NEW_PROCESS_GROUP` replaces `Setpgid`
- `run_as` (sudo) not supported on Windows — returns error
- Platform helpers in `shell_unix.go` / `shell_windows.go`: `setProcessAttrs`, `killProcessTree`, `isProcessAlive`, `defaultShell`, `loginShellArgs`
- `cmdbuilder` uses `defaultShell`/`defaultShellFlag` constants from `shell_default.go` / `shell_windows.go`
