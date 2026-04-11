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
| `memory_tools.go` | Core memory tools (append/replace/rethink/search/recall) |
| `context_edit.go` | ContextEdit tool (conversation history surgery) |
| `cron.go` | Cron tool (scheduled tasks) |
| `task_manager.go` | Background task management |

## Tool Schema Rule

**Array types MUST include `Items` field.** OpenAI API rejects schemas without it.
```go
Items: &llm.ToolParamItems{Type: "string"}
```

## Hook Chain

Three built-in hooks: LoggingHook, TimingHook, ApprovalHook.
Chain is shared across main Agent and all SubAgents (same instance pointer).
Max 20 hooks (`MaxHookChainLen` in `hook.go`).

PreToolUse: does NOT short-circuit on error (records first error, continues all).
PostToolUse: guarantees all hooks run even if one panics (recover).

## Sandbox Types

- `none`: direct execution (default). Uses `/bin/bash -l -c` on Unix, `powershell.exe -Command` on Windows
- `docker`: Docker container per OS user (always Linux)
- `remote`: remote runner process via runner protocol (always Linux)

## Windows Support

- **None sandbox only** — docker/remote sandboxes are always Linux
- Shell: `powershell.exe -Command` replaces `/bin/bash -l -c`
- Process management: `taskkill /T /F` replaces `kill(-pgid, SIGKILL)`; `CREATE_NEW_PROCESS_GROUP` replaces `Setpgid`
- `run_as` (sudo) not supported on Windows — returns error
- Platform helpers in `shell_unix.go` / `shell_windows.go`: `setProcessAttrs`, `killProcessTree`, `isProcessAlive`, `defaultShell`, `loginShellArgs`
- `cmdbuilder` uses `defaultShell`/`defaultShellFlag` constants from `shell_default.go` / `shell_windows.go`
