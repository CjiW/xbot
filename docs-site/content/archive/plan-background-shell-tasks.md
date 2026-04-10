---
title: "plan-background-shell-tasks"
weight: 150
---


> Generated: 2026-04-02
> Status: Pending Review

## Background & Goals

### Current Problem
When a shell command runs indefinitely (e.g. `tail -f`, dev server, long test), two issues occur:

1. **Ctrl+C only cancels the agent loop, not the actual process** — The agent loop returns, but `exec.CommandContext` may not reliably kill all child processes (especially in Docker sandbox where only the CLI process is killed, container processes continue). The process may leak.

2. **Entire agent loop blocks** — `execOne()` in `engine.go:967` synchronously calls `toolExecutor(execCtx, tc)`. In default serial mode (`engine.go:1108-1116`), one stuck shell = entire iteration stuck. No other tools can run, no progress updates reach the user.

### Goal
Implement a background task system similar to Claude Code's approach:

1. **Shell tool gets `background: true` parameter** — when set, the command runs in a goroutine and returns a task ID immediately
2. **Task lifecycle management** — `task_kill`, `task_status`, `task_read` tools for the LLM to monitor/control
3. **Notification on completion** — when a background task finishes, its result is injected into the conversation as a user message, triggering a new iteration
4. **User can kill via CLI** — `/kill <task_id>` command or Ctrl+C during background execution

### Non-Goals (for now)
- Scheduled/loop tasks (`/loop` like Claude) — can be added later
- Background tasks for non-Shell tools — Shell is the only tool that commonly runs indefinitely
- Persistent tasks across restarts — tasks are in-memory only

## Current Architecture Analysis

### Key Files
| File | Role | Change Type |
|------|------|-------------|
| `tools/shell.go` | Shell tool implementation | Modify (add background param) |
| `tools/task_manager.go` | NEW — background task lifecycle manager | New |
| `tools/task_tools.go` | NEW — task_kill/task_status/task_read tools | New |
| `agent/engine.go` | Agent loop, tool execution | Modify (inject task manager, handle bg results) |
| `agent/engine_wire.go` | Wire task manager into RunConfig/ToolContext | Modify |
| `tools/interface.go` | ToolContext, ToolResult | Modify (add TaskManager field) |
| `channel/cli.go` | CLI rendering, /kill command | Modify (render bg tasks, /kill) |
| `agent/progress.go` | Progress data structures | Modify (add bg task info) |

### Data Flow (Proposed)

```
LLM calls Shell(background=true, command="npm run dev")
    │
    ▼
engine.execOne()
    ├── Foreground path (background=false): synchronous as before
    └── Background path (background=true):
        ├── Create BackgroundTask{ID, Command, Status: Running, StartedAt}
        ├── Launch goroutine: sandbox.Exec(detachedCtx, spec)
        ├── Store task in TaskManager
        └── Return ToolResult{Summary: "Background task started [id: abc123]"}
    │
    ▼
Agent continues next iteration with task ID info
    │
    ▼
Goroutine completes:
    ├── Update task {Status: Done/Error, Result, FinishedAt}
    └── Notify engine via callback → inject result as user message
    │
    ▼
New iteration: LLM sees "[bg:abc123] Task completed: ..." and can act on it
```

### Task Manager Design

```go
// tools/task_manager.go
type BackgroundTask struct {
    ID         string        `json:"id"`         // unique ID (8-char hex)
    Command    string        `json:"command"`     // original command
    Status     string        `json:"status"`      // "running" | "done" | "error" | "killed"
    StartedAt  time.Time     `json:"started_at"`
    FinishedAt *time.Time    `json:"finished_at,omitempty"`
    Output     string        `json:"output"`      // stdout+stderr (truncated to 50KB)
    ExitCode   int           `json:"exit_code"`
    Error      string        `json:"error,omitempty"`
    cancel     context.CancelFunc  // internal, not serialized
}

type TaskManager interface {
    // Start launches a background task and returns immediately
    Start(ctx context.Context, sandbox Sandbox, spec ExecSpec, userID string) (*BackgroundTask, error)
    // Kill terminates a running task
    Kill(taskID string) error
    // Status returns current task state
    Status(taskID string) (*BackgroundTask, error)
    // List returns all tasks for a session
    List(sessionKey string) []*BackgroundTask
    // OnComplete registers a callback for task completion
    OnComplete(sessionKey string, callback func(task *BackgroundTask))
}
```

### Kill Mechanism

The key improvement over current behavior: **process group kill**.

For `NoneSandbox`:
```go
// Instead of plain exec.CommandContext:
cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
// Kill: syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)  // kill entire process group
```

For `DockerSandbox`:
```go
// After docker exec, use: docker exec --sig-kill <container> kill -9 <pid>
// Or: docker stop <exec_id> (docker 1.41+)
```

### Notification Flow

When a background task completes, the engine needs to inject a "virtual user message" to trigger a new LLM iteration:

```
Background goroutine completes
    → TaskManager calls OnComplete callback
    → Engine injects into messages: {role: "user", content: "[bg:{id}] completed (exit {code}):\n{output}"}
    → Triggers next iteration of the agentic loop
```

**Implementation**: The engine's `Run()` loop needs a secondary channel to receive background task notifications:

```go
// In engine.go Run():
taskNotifyCh := make(chan *BackgroundTask, 16) // buffered

for i := 0; i < maxIter; i++ {
    select {
    case task := <-taskNotifyCh:
        // Inject bg task result as user message
        messages = append(messages, llm.Message{
            Role: "user",
            Content: fmt.Sprintf("[bg:%s] Task %s (exit %d):\n%s",
                task.ID, task.Status, task.ExitCode, truncate(task.Output, 50000)),
        })
        i-- // don't count this as an iteration
        continue
    default:
    }
    // ... normal iteration logic
}
```

### LLM Tools

**task_status** — Check status of a background task:
```json
{
    "name": "task_status",
    "parameters": {"task_id": "abc123"}
}
```
Returns: current status, output so far (for running tasks, returns last N bytes of output).

**task_kill** — Kill a running background task:
```json
{
    "name": "task_kill",
    "parameters": {"task_id": "abc123"}
}
```
Returns: confirmation that task was killed.

**task_read** (optional, can defer) — Read more output from a completed task:
```json
{
    "name": "task_read",
    "parameters": {"task_id": "abc123", "offset": 0, "limit": 8000}
}
```

### CLI Integration

1. **Progress display**: Background tasks show in progress area as:
   ```
   ● bg:abc123 npm run dev (running 45s)
   ```

2. **`/kill` command**: User types `/kill abc123` to kill a specific task, or `/kill` to list running tasks.

3. **Completion notification**: When a bg task completes during idle (no active agent loop), show a status bar notification like:
   ```
   [bg:abc123] npm run dev exited with code 0 (tap to view output)
   ```

## Detailed Plan

### Phase 1: Task Manager Core
- [ ] 1.1 Create `tools/task_manager.go` — `BackgroundTask` struct, `TaskManager` interface, in-memory implementation with sync.RWMutex
- [ ] 1.2 Implement `Start()` — creates task, launches goroutine with sandbox.Exec, calls OnComplete callback when done
- [ ] 1.3 Implement `Kill()` — cancels context, kills process group (syscall.Kill(-pgid, SIGKILL))
- [ ] 1.4 Implement `Status()` and `List()` — thread-safe reads
- [ ] 1.5 Per-session task tracking — tasks scoped to sessionKey, cleaned up on session close

### Phase 2: Shell Tool Background Mode
- [ ] 2.1 Add `background` parameter to Shell tool — `Parameters()` add `{Name: "background", Type: "boolean", ...}`
- [ ] 2.2 In `ShellTool.Execute()`, check `background=true` → call `TaskManager.Start()` instead of blocking
- [ ] 2.3 Return immediate result with task ID: `"Background task started [id: abc123]. Use task_status to monitor, task_kill to terminate."`
- [ ] 2.4 For background tasks, use process group kill (`Setpgid: true`) in NoneSandbox

### Phase 3: Task Control Tools
- [ ] 3.1 Create `tools/task_tools.go` — `TaskStatusTool`, `TaskKillTool`, `TaskReadTool`
- [ ] 3.2 Register tools in `tools/interface.go` — add to core tool registration
- [ ] 3.3 `TaskStatusTool`: returns status + last 2000 chars of output for running, full output for completed
- [ ] 3.4 `TaskKillTool`: calls TaskManager.Kill(), returns confirmation
- [ ] 3.5 `TaskReadTool`: paginated output reading for completed tasks (optional, can defer)

### Phase 4: Engine Integration
- [ ] 4.1 Add `TaskManager` to `ToolContext` — `interface.go`
- [ ] 4.2 Add `TaskManager` to `RunConfig` — `engine.go`
- [ ] 4.3 Wire TaskManager in `buildToolContext()` — pass from RunConfig
- [ ] 4.4 In `Run()` loop, add `select` on task completion channel — inject result as user message, decrement iteration counter
- [ ] 4.5 Register `OnComplete` callback in engine setup — pipes bg task results into the notification channel
- [ ] 4.6 Update system prompt / tool descriptions to document background mode

### Phase 5: CLI Integration
- [ ] 5.1 Add background task info to `StructuredProgress` — `progress.go`
- [ ] 5.2 Render running bg tasks in progress block — dimmed line with `● bg:id command (running Xs)`
- [ ] 5.3 Add `/kill <task_id>` command parsing in CLI Update — call TaskManager.Kill() via closure
- [ ] 5.4 Completion notification during idle — tempStatus or status bar hint

### Phase 6: Testing
- [ ] 6.1 Unit test: TaskManager Start/Kill/Status lifecycle
- [ ] 6.2 Unit test: background Shell tool returns task ID immediately
- [ ] 6.3 Unit test: background task completion injects notification
- [ ] 6.4 Integration test: agent loop receives bg task result in next iteration

## Verification

- `go build ./...` compiles cleanly
- `go test ./...` all pass
- Manual test: run `sleep 1000` with background=true → verify immediate return
- Manual test: kill bg task with `/kill` → verify process dies
- Manual test: bg task completes → verify agent gets notified and acts on result

## Rollback Strategy

All changes are additive (new tools + new parameter). If issues arise:
1. Remove `background` parameter from Shell tool → reverts to sync-only
2. Remove task_* tools from registration → no bg task control
3. Engine `select` on taskNotifyCh falls through to `default` → no behavioral change

## Notes

- **Output buffering**: Background tasks buffer stdout/stderr. Need to handle large output — cap at 50KB per task, rotate if needed
- **Process group kill is critical**: Without `Setpgid`, child processes leak. This applies to both foreground and background modes — should fix for foreground too
  - **NoneSandbox**: `none_sandbox.go` needs `cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}` + `syscall.Kill(-pid, SIGKILL)` for kill. Must be platform-conditional (Unix only, Windows uses Job Objects)
  - **DockerSandbox**: `docker_sandbox.go` — need to track exec PID inside container and kill with `docker exec kill -9 <pid>`. Complex — Phase 1 may only implement NoneSandbox process group kill, Docker can follow
- **Remote sandbox**: Background tasks need the runner to support async exec. May need runner protocol extension. **Phase 1-3 should skip remote sandbox** — background only works with NoneSandbox initially, Docker in Phase 2+
- **Concurrency**: Multiple bg tasks should run in parallel. TaskManager goroutines should be independent
- **Memory**: Each task holds up to 50KB output. With 10 concurrent tasks, that's 500KB — acceptable
- **Timeout**: Background tasks have NO timeout by default (they run indefinitely until killed or completed). This is the whole point. But we should add a max lifetime (e.g. 24h) as safety net
- **Existing tool timeout**: The engine's `execCtx, cancel = context.WithTimeout(ctx, toolTimeout)` in `engine.go:958` must NOT apply to background tasks. Background tasks use their own detached context

✅ Self-review passed
