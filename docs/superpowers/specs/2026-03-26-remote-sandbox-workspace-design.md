# Remote Sandbox Workspace Unification

## Problem

Remote sandbox mode has two issues:

1. **Workspace path leakage**: The LLM sees host paths (e.g. `/app/.xbot/users/ou_xxx/workspace`) instead of the runner's workspace. This is because `SandboxWorkDir` is `""` for remote mode, causing all path fallbacks to use host paths.

2. **Global skills/agents not available**: Skills and agents on the server are not copied to the runner, so the LLM cannot discover or use them.

## Root Cause

- `sandboxWorkDir()` returns `""` for non-docker modes
- `SandboxEnabled` is `a.sandboxMode == "docker"` — false for remote
- `buildPrompt()` only handles docker mode for workDir
- `EnsureSynced()` skips remote mode entirely
- Path guard functions (`defaultWorkspaceRoot`, `shouldUseSandbox`) gate on `SandboxEnabled || SandboxWorkDir != ""`

## Design

### Sandbox Interface Extension

Add one method to `tools.Sandbox` interface:

```go
Workspace(userID string) string
```

Implementations:

| Sandbox | Return |
|---------|--------|
| DockerSandbox | `"/workspace"` |
| RemoteSandbox | Runner's reported workspace (from registration) |
| NoneSandbox | `""` (empty = use host paths) |

### RunConfig Changes

| Field | Change |
|-------|--------|
| `SandboxWorkDir` | Remove from `RunConfig` and `ToolContext`. All consumers use `sandbox.Workspace(userID)`. |
| `SandboxEnabled` | Change from `sandboxMode == "docker"` to `sandbox.Name() != "none"`. |
| `SandboxReadOnlyRoots` | Keep in `ToolContext` but leave empty for remote mode. |
| `SandboxMode` | Keep for places that need docker vs remote distinction. |
| `ReadOnlyRoots` | Keep — stores host paths used as sync source. |
| `WorkingDir` | Keep — host workDir for server-side paths (MCP config, DataDir). |
| `WorkspaceRoot` | Keep — host user workspace for server-side operations (sync source, bang output). |

### Path Guard Changes

All functions in `tools/path_guard.go` that currently read `ctx.SandboxWorkDir` or `ctx.SandboxEnabled` switch to use the Sandbox interface:

```go
// Before
func defaultWorkspaceRoot(ctx *ToolContext) string {
    if ctx.SandboxEnabled && ctx.SandboxWorkDir != "" { return ctx.SandboxWorkDir }
    ...
}

// After
func defaultWorkspaceRoot(ctx *ToolContext) string {
    if ctx.Sandbox != nil && ctx.Sandbox.Name() != "none" {
        return ctx.Sandbox.Workspace(ctx.OriginUserID)
    }
    ...
}
```

Affected functions:
- `defaultWorkspaceRoot` — use `sandbox.Workspace()`
- `sandboxBaseDir` — use `sandbox.Workspace()`
- `shouldUseSandbox` — use `sandbox.Name() != "none"`
- `sandboxReadOnlyRoots` — keep for docker, return as-is for remote (empty `SandboxWorkDir` triggers passthrough)

### Tool Changes

Each tool that dispatches on `SandboxEnabled` updates its guard:

```go
// Before
if ctx.SandboxEnabled && ctx.WorkspaceRoot != "" { ... }

// After (for tools using Sandbox API — Read, Write, Glob, Grep, Cd)
if shouldUseSandbox(ctx) { ... }

// After (for Shell tool — already uses sandbox.Name() switch)
// Already correct after previous fix
```

### Agent Init Changes

**`buildBaseRunConfig` / `buildToolExecutor`**:
- Remove `SandboxWorkDir: a.sandboxWorkDir()`
- Change `SandboxEnabled: a.sandboxMode == "docker"` → `SandboxEnabled: a.sandboxMode != "none"`

**`buildPrompt` / `initPipelines`**:
- `promptWorkDir` switches on `sandboxMode`:
  ```go
  switch sandboxMode {
  case "docker": promptWorkDir = "/workspace"
  case "remote": promptWorkDir = sandbox.Workspace(senderID)
  default: promptWorkDir = a.workDir
  }
  ```

**`sandboxWorkDir()` method**:
- Remove. All callers use `sandbox.Workspace(userID)` directly.

**`ensureWorkspace`**:
- Already sandbox-aware (`sandbox.MkdirAll` when sandbox != nil).
- `os.MkdirAll(wsRoot)` calls in `engine_wire.go:545` and `interactive.go:346` need to use sandbox API or skip for remote mode.

### Skill/Agent Sync on Runner Registration

When a runner connects and registers (`RemoteSandbox.handleRegister`):

1. `RemoteSandbox` stores `globalSkillDirs` and `agentsDir` (passed at init time)
2. On registration, triggers a one-time sync:
   - Read server-side `globalSkillDirs` contents via `os.ReadDir`
   - Write each skill subdirectory to runner via `Sandbox.MkdirAll` + `Sandbox.WriteFile`
   - Read server-side `agentsDir` contents via `os.ReadDir`
   - Write each agent `.md` file to runner via `Sandbox.WriteFile`
3. Skills/agents land at `{runner_workspace}/.skills/` and `{runner_workspace}/.agents/`
4. LLM discovers them via normal workspace scanning (SkillTool, SubAgentTool)

Sync happens once per runner connection. Reconnection re-triggers sync.

### SubAgent RunConfig

SubAgent inherits `Sandbox` from parent `ToolContext`. `buildSubAgentRunConfig`:
- Uses `sandbox.Workspace(userID)` for prompt `workDir`
- Uses `sandbox.Name() != "none"` for `SandboxEnabled`

## Files Changed

| File | Change |
|------|--------|
| `tools/sandbox.go` | Add `Workspace(userID) string` to Sandbox interface |
| `tools/remote_sandbox.go` | Implement `Workspace()`, add sync on registration |
| `tools/docker_sandbox.go` | Implement `Workspace()` → `"/workspace"` |
| `tools/none_sandbox.go` | Implement `Workspace()` → `""` |
| `agent/engine.go` | Remove `SandboxWorkDir` from RunConfig, update `buildToolContext` |
| `agent/engine_wire.go` | Update `SandboxEnabled`, remove `SandboxWorkDir`, fix `os.MkdirAll` |
| `tools/path_guard.go` | Update all guard functions to use Sandbox interface |
| `tools/shell.go` | Already uses `sandbox.Name()` switch — no change needed |
| `tools/cd.go` | Update `shouldUseSandbox` usage |
| `tools/edit.go` | Update `SandboxEnabled` guard |
| `tools/read.go` | Update `SandboxEnabled` guard |
| `tools/grep.go` | Update `SandboxEnabled` guard |
| `tools/glob.go` | Update `SandboxEnabled` guard |
| `tools/sandbox_exec.go` | Update `setSandboxDir` to use `sandbox.Workspace()` |
| `tools/interface.go` | Remove `SandboxWorkDir` from ToolContext |
| `tools/skill.go` | Update `resolveSkill` to use `sandbox.Workspace()` |
| `agent/agent.go` | Remove `sandboxWorkDir()`, update `buildPrompt` |
| `agent/context.go` | Update `initPipelines` promptWorkDir |
| `agent/interactive.go` | Fix `os.MkdirAll`, update `SandboxEnabled` |

## Out of Scope

- MCP stdio in remote mode (architecturally incompatible)
- CdTool path resolution in remote mode (CurrentDir semantics need separate design)
- SubAgent CWD inheritance in remote mode
