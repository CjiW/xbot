# OS User-Based Permission Control Design

> 2026-04-08

## 1. Problem Statement

Agent permission control is a hard problem. Command-level matching (Claude Code style) is fragile — regex patterns don't cover all edge cases, and the permission boundary lives at the wrong abstraction level.

**Our approach**: leverage Linux's mature OS user identity model as the permission boundary. Built-in tools (Shell, FileReplace, FileCreate, etc.) can optionally execute as a different OS user. The user configures two profiles:

- **Default user** — no approval needed, used for routine operations (optional)
- **Privileged user** — requires pre-hook approval before each execution (optional)

Both are optional. If neither is configured, the feature is **disabled** — tools execute as the xbot process user (current behavior, zero change).

This is simpler, more robust, and more Unix-idiomatic than command matching. The OS kernel enforces file permissions, PATH restrictions, and capability boundaries — no need to reinvent any of that.

## 2. Scope

- **Opt-in feature**: disabled by default, must be explicitly enabled
- **Priority**: none sandbox mode first (CLI single-user scenario)
- **Tools**: Shell, FileReplace, FileCreate (the core side-effect tools)
- **Approval**: channel-agnostic interface; CLI implementation via AskUser first
- **Future**: docker sandbox, remote sandbox, Web channel

### 2.1 Default Behavior (Feature Disabled)

When `default_user` and `privileged_user` are both unset (the default):
- All tools execute as the xbot process user — **zero behavioral change from current code**
- The `run_as` parameter is still present in tool schemas but is ignored (treated as empty string)
- No ApprovalHook is registered in HookChain
- No system prompt section about user control
- No sudo wrapping, no approval dialogs, no performance overhead

### 2.2 Activation

User explicitly sets `default_user` and/or `privileged_user` in settings. On first enable, the system generates a sudoers setup script and reminds the user to restart.

## 3. Core Concepts

### 3.1 Two User Profiles

| Profile | Approval | Use Case |
|---------|----------|----------|
| **default_user** | None | Daily development: read files, edit code, run builds |
| **privileged_user** | Pre-hook approval required | System changes: install packages, modify system files, restart services |

Both are optional. If neither is configured, tools execute as the xbot process user (current behavior).

The LLM chooses which user to execute as by passing a `run_as` parameter in tool calls:

```json
{"name": "Shell", "arguments": {"command": "apt install nginx", "run_as": "privileged"}}
```

If `run_as` is omitted, the tool executes as the xbot process user (no change from current behavior). If `run_as` matches `default_user`, it executes directly. If `run_as` matches `privileged_user`, the approval hook fires.

### 3.2 Why OS Users?

1. **Kernel-enforced isolation** — file permissions, capabilities, cgroups, seccomp all work out of the box
2. **PATH/bin restrictions** — each user can only execute binaries accessible to them
3. **Audit trail** — `auditd`, `syslog`, and `last` log who ran what
4. **No reinvention** — decades of Unix security engineering vs. homegrown regex matching
5. **Simple mental model** — "run as alice" vs. "run as root" is something every developer understands

### 3.3 Pre-Hook Approval

When a tool call targets the privileged user:
1. `ApprovalHook` (a `ToolHook`) intercepts in `PreToolUse`
2. Sends `ApprovalRequest` to the channel via a channel-agnostic interface
3. Channel renders approval UI (CLI: AskUser dialog; Web: future)
4. User approves → execution continues; denies → returns "user denied" error

## 4. Current Architecture (Baseline)

### 4.1 None Sandbox Execution Paths

**Shell tool** (`tools/shell.go:56`):
```
ShellTool.Execute() → buildSpec() → sandbox.Exec(spec)
  → SandboxRouter → NoneSandbox.Exec() → buildCmdFromSpec()
  → exec.Command("/bin/sh", "-c", command)
```

**FileReplace / FileCreate** (`tools/edit.go:152,47`):
```
FileReplaceTool.Execute() → shouldUseSandbox(ctx)
  → false (none sandbox) → executeLocal()
  → os.ReadFile() / os.WriteFile()  ← direct OS calls, no exec.Command
```

### 4.2 Key Integration Points

| What | Location | Current Behavior |
|------|----------|-----------------|
| `ExecSpec` | `tools/sandbox.go:20-35` | No `RunAsUser` field |
| `buildCmdFromSpec()` | `tools/none_sandbox.go:431-456` | Creates `exec.Cmd` with no user switch |
| `NoneSandbox` struct | `tools/none_sandbox.go:21` | Empty struct, stateless |
| `FileReplace.executeLocal()` | `tools/edit.go:203-224` | Direct `os.ReadFile/WriteFile` |
| `FileCreate.executeLocal()` | `tools/edit.go:71-96` | Direct `os.MkdirAll/WriteFile` |
| `ToolContext` | `tools/interface.go:19-70` | No `RunAsUser` field |
| `user_settings` table | `storage/sqlite/schema.go:151-160` | Generic KV `(channel, sender_id, key, value)` |
| `ToolHook` interface | `tools/hook.go:17-27` | `PreToolUse` can block execution |
| `HookChain` | `tools/hook.go:29-110` | Runs all hooks in order |
| `checkDangerousCommand` | `tools/shell.go:600-629` | Blocks bare `sudo` — must coexist with injected `sudo -u` |

### 4.3 Existing Settings Pattern

`user_settings` is a generic KV store per `(channel, sender_id)`. Already used for `active_runner`:
```sql
SELECT value FROM user_settings WHERE channel = 'web' AND sender_id = ? AND key = 'active_runner'
```
We'll add keys `default_user` and `privileged_user` using the same pattern.

## 5. Design

### 5.1 User Configuration

Stored in `user_settings` table:

| Key | Example Value | Meaning |
|-----|---------------|---------|
| `default_user` | `"alice"` | Execute as this user without approval |
| `privileged_user` | `"root"` | Execute as this user, requires approval |

Channel-agnostic storage: the settings work for CLI, Web, Feishu, etc. The `channel` column can be `"*"` for cross-channel settings, or channel-specific.

### 5.2 Tool Parameter Extension

Core side-effect tools gain an optional `run_as` parameter. **This is a tool schema change** — it modifies the `Parameters()` return value, which flows through to the LLM's function calling schema via `toOpenAITools()` (`llm/openai.go:352`).

**Schema change audit** (every modification point in the tool→LLM pipeline):

| # | File | Method | Change |
|---|------|--------|--------|
| S1 | `tools/shell.go:48` | `ShellTool.Parameters()` | Add `{Name: "run_as", Type: "string", Required: false}` |
| S2 | `tools/edit.go:35` | `FileCreateTool.Parameters()` | Add `{Name: "run_as", Type: "string", Required: false}` |
| S3 | `tools/edit.go:130` | `FileReplaceTool.Parameters()` | Add `{Name: "run_as", Type: "string", Required: false}` |
| S4 | `llm/openai.go:352` | `toOpenAITools()` | **No change needed** — reads `Parameters()` dynamically, new param flows through automatically |
| S5 | `tools/shell.go:57` | `ShellTool.Execute()` params struct | Add `RunAs string \`json:"run_as"\`` |
| S6 | `tools/edit.go:42` | `FileCreateParams` struct | Add `RunAs string \`json:"run_as"\`` |
| S7 | `tools/edit.go:142` | `FileReplaceParams` struct | Add `RunAs string \`json:"run_as"\`` |

**Backward compatibility**: `run_as` is `Required: false`. When omitted or empty, behavior is identical to current code — no sudo wrapping, no approval check. The LLM sees the parameter in the schema but won't use it unless the system prompt tells it to (which only happens when the feature is enabled).

**Risk mitigation**:
- The param struct field uses `json:"run_as"` with a string zero value (`""`) — no nil pointer risk
- The `Execute()` method checks `params.RunAs == ""` early and short-circuits to existing path
- No existing test asserts on the full `Parameters()` return — tests use functionally, not schema inspection
- If an older LLM ignores the new param, everything still works (defaults to current user)

```go
// tools/shell.go — ShellParams (struct used in Execute)
type ShellParams struct {
    Command    string  `json:"command"`
    Timeout    float64 `json:"timeout"`
    Background bool    `json:"background"`
    RunAs      string  `json:"run_as,omitempty"`  // NEW: optional, empty = current user
}

// tools/shell.go — Parameters() (schema for LLM)
func (t *ShellTool) Parameters() []llm.ToolParam {
    return []llm.ToolParam{
        {Name: "command", Type: "string", Description: "The command to execute", Required: true},
        {Name: "timeout", Type: "number", Description: "Timeout in seconds (default: 120, max: 600)", Required: false},
        {Name: "background", Type: "boolean", Description: "Run command in background...", Required: false},
        {Name: "run_as", Type: "string", Description: "OS username to execute as (requires permission control to be enabled)", Required: false},  // NEW
    }
}
```

The `run_as` value must match either `default_user` or `privileged_user` from user settings. Any other value is rejected. When the feature is disabled (both settings empty), any non-empty `run_as` value is rejected with a clear error.

### 5.3 Execution Path — Shell

In none sandbox mode, `buildCmdFromSpec()` (`tools/none_sandbox.go:431`) is the single chokepoint for all `exec.Cmd` creation. User switching happens here:

```
buildCmdFromSpec(ctx, spec, managedCtx):
  if spec.RunAsUser != "":
      // sudo -n -u <user> -- /bin/sh -c "<command>"
      // Requires: xbot user has NOPASSWD sudoers entry for target user
      cmd = exec.CommandContext(ctx, "sudo", "-n", "-H", "-u", spec.RunAsUser,
          "--", "/bin/sh", "-l", "-c", spec.Command)
  else:
      cmd = exec.CommandContext(ctx, "/bin/sh", "-c", spec.Command)  // existing
```

**Why `sudo -n -u` instead of `SysProcAttr.Credential`?**
- `SysProcAttr.Credential` requires xbot to run as root — unrealistic for most deployments
- `sudo -n` (non-interactive) won't prompt for password — fails fast if not configured
- `sudoers` can be configured per-user: `xbot_user ALL=(target_user) NOPASSWD: ALL`
- Process group management (`Setpgid`, SIGKILL) still works through sudo

**Why `--` separator?**
- Prevents command injection via malicious tool arguments being interpreted as sudo flags
- The target command is passed as a single argument to `/bin/sh -c`

**Why `-H` flag?**
- Sets `$HOME` to the target user's home directory instead of the calling user's
- Ensures the login shell (`-l`) sources the correct profile

**Existing `checkDangerousCommand` interaction**:
- The LLM-generated command does NOT contain `sudo` — it's injected by the sandbox layer below
- The safety check at `shell.go:600-629` sees the original command (before sudo wrapping), so no conflict
- If the LLM itself tries to use `sudo`, the existing check blocks it — correct behavior

### 5.4 Execution Path — FileReplace / FileCreate

These tools use direct `os.ReadFile/WriteFile` in none mode, bypassing `exec.Command`. We add a helper that writes via subprocess:

```go
// tools/edit.go — new helper
func writeFileAsUser(runAsUser, path, content string, perm os.FileMode) error {
    // Use sudo to write as the target user
    cmd := exec.Command("sudo", "-n", "-H", "-u", runAsUser,
        "--", "/bin/sh", "-c", fmt.Sprintf("cat > '%s' && chmod %o '%s'",
            shellescape(path), perm, shellescape(path)))
    cmd.Stdin = strings.NewReader(content)
    return cmd.Run()
}
```

In `executeLocal()`, the path branches:
```go
if params.RunAs != "" && isPrivilegedUser(params.RunAs) {
    return writeFileAsUser(params.RunAs, filePath, newContent, 0644)
}
// existing: os.WriteFile(filePath, []byte(newContent), 0644)
```

Files created this way are owned by the target OS user — correct behavior.

### 5.5 Approval System

#### 5.5.1 Channel-Agnostic Interface

```go
// tools/approval.go (new file)

// ApprovalRequest represents a pending user approval for a tool execution.
type ApprovalRequest struct {
    ToolName string // e.g., "Shell"
    ToolArgs string // JSON arguments (for display)
    RunAs    string // Target OS user
    Reason   string // Human-readable description
    // Extracted details for display
    Command  string // Parsed command (for Shell)
    FilePath string // Target file (for FileReplace/FileCreate)
}

// ApprovalResult is the user's decision.
type ApprovalResult int

const (
    ApprovalDenied   ApprovalResult = 0
    ApprovalApproved ApprovalResult = 1
)

// ApprovalHandler is the channel-agnostic interface for user approval.
type ApprovalHandler interface {
    RequestApproval(ctx context.Context, req ApprovalRequest) (ApprovalResult, error)
}
```

#### 5.5.2 ApprovalHook

```go
// tools/approval.go

type ApprovalHook struct {
    handler        ApprovalHandler
    defaultUser    string // from user settings
    privilegedUser string // from user settings
}

func (h *ApprovalHook) PreToolUse(ctx context.Context, toolName string, args string) error {
    runAs := extractRunAs(args) // parse "run_as" from JSON args

    if runAs == "" || runAs == h.defaultUser {
        return nil // no approval needed
    }
    if runAs != h.privilegedUser {
        return fmt.Errorf("unknown run_as user %q: must be %q or %q",
            runAs, h.defaultUser, h.privilegedUser)
    }

    // Privileged user — request approval
    req := ApprovalRequest{
        ToolName: toolName,
        ToolArgs: args,
        RunAs:    runAs,
        // ... extract Command/FilePath for display ...
    }
    result, err := h.handler.RequestApproval(ctx, req)
    if err != nil {
        return fmt.Errorf("approval request failed: %w", err)
    }
    if result != ApprovalApproved {
        return fmt.Errorf("user denied execution as %q", runAs)
    }
    return nil
}
```

The `ApprovalHook` is registered in `HookChain` and only activates for tool calls that target the privileged user. Normal operations (no `run_as` or `default_user`) pass through with zero overhead.

#### 5.5.3 CLI Implementation

```go
// channel/cli_approval.go (new file)

type CLIApprovalHandler struct {
    program *tea.Program
}

func (h *CLIApprovalHandler) RequestApproval(ctx context.Context, req ApprovalRequest) (ApprovalResult, error) {
    // Use program.Send() to push approval dialog to TUI
    // Block until user responds via a channel
    // Return ApprovalApproved or ApprovalDenied
}
```

CLI rendering:
```
┌─ ⚠ Approval Required ─────────────────────────────┐
│ Tool:     Shell                                     │
│ Run as:   root (privileged)                         │
│ Command:  apt install nginx                         │
│                                                     │
│ Allow this operation? [Y/n]                         │
└─────────────────────────────────────────────────────┘
```

### 5.6 System Prompt Integration

**This section is only included when the feature is enabled** (at least one of `default_user`/`privileged_user` is set). When disabled, the LLM sees the `run_as` parameter in tool schemas but has no context about it — it simply won't use it.

Dynamic system prompt section tells the LLM about available users:

```markdown
## Execution User Control

You can execute tools as a different OS user by passing the `run_as` parameter.
Available users are configured by the system administrator.

### Available Users
| User | Approval | Description |
|------|----------|-------------|
| (default) | None | Current process user |
| alice | None | Default execution user |
| root | **Required** | Privileged user — user must approve each use |

### Rules
- Omit `run_as` to execute as the current process user
- Use `run_as: "alice"` for routine operations
- Use `run_as: "root"` ONLY when the task genuinely requires elevated privileges
- Always explain WHY you need the privileged user when requesting it
```

### 5.7 ExecSpec Extension

```go
// tools/sandbox.go — ExecSpec
type ExecSpec struct {
    // ... existing fields ...
    RunAsUser string // NEW: execute as this OS user (none sandbox only)
}
```

Threaded from tool params → ExecSpec → buildCmdFromSpec:
```
ShellParams.RunAs → ShellTool.Execute() → buildSpec() → spec.RunAsUser
  → NoneSandbox.Exec() → buildCmdFromSpec() → sudo -n -H -u <user> -- ...
```

### 5.8 Runner Support (Shared Command Builder)

The runner binary (`cmd/runner/`) and TUI-as-runner mode both use `internal/runnerclient/` which has **completely independent** execution code from `tools/none_sandbox.go`. Currently zero code is shared:

| Aspect | Server (`tools/`) | Runner (`runnerclient/`) |
|--------|-------------------|--------------------------|
| ExecSpec | `tools.ExecSpec` — has `Workspace`, `UserID`, `KeepAlive` | `runnerclient.ExecSpec` — simpler, no user fields |
| Command builder | `buildCmdFromSpec()` shared helper | Inline in `NativeExecutor.Exec()` at `native.go:35` |
| File writes | `os.WriteFile()` directly | `os.WriteFile()` directly at `native.go:100` |

To avoid duplicating the `sudo -u` logic, we extract a shared command builder into a new internal package:

#### 5.8.1 Shared Command Builder

```go
// internal/cmdbuilder/cmdbuilder.go (new package)

package cmdbuilder

// Build creates an *exec.Cmd with optional OS user switching.
// This is the single source of truth for command construction,
// used by both NoneSandbox (server) and NativeExecutor (runner).
func Build(ctx context.Context, shell bool, command string, args []string,
    dir string, env []string, stdin string, runAsUser string) *exec.Cmd {
    // ... unified command building with sudo -n -H -u wrapping ...
}
```

Both `tools/none_sandbox.go` and `internal/runnerclient/native.go` call `cmdbuilder.Build()` instead of building commands inline.

#### 5.8.2 Runner ExecSpec Extension

```go
// internal/runnerclient/executor.go — ExecSpec
type ExecSpec struct {
    // ... existing fields ...
    RunAsUser string `json:"run_as_user,omitempty"` // NEW: execute as this OS user
}
```

#### 5.8.3 Runner Protocol Extension

```go
// internal/runnerproto/runner_proto.go — ExecRequest
type ExecRequest struct {
    // ... existing fields ...
    RunAsUser string `json:"run_as_user,omitempty"` // NEW: target OS user
}
```

#### 5.8.4 Runner Handler

```go
// internal/runnerclient/handler.go — handleExec()
spec := ExecSpec{
    // ... existing fields ...
    RunAsUser: req.RunAsUser, // NEW: pass through from protocol
}
```

#### 5.8.5 Runner CLI Flags

```bash
xbot-runner --default-user alice --privileged-user root ...
```

Runner stores these locally (no DB needed — runner is single-user by nature). When the server sends `run_as_user` in an exec request, the runner validates it against its local configuration before executing.

#### 5.8.6 Runner WriteFileAsUser

Same `writeFileAsUser` logic, extracted to the shared `cmdbuilder` package:

```go
// internal/cmdbuilder/file.go
func WriteFileAsUser(runAsUser, path string, data []byte, perm os.FileMode) error {
    cmd := exec.Command("sudo", "-n", "-H", "-u", runAsUser,
        "--", "/bin/sh", "-c", fmt.Sprintf("cat > '%s' && chmod %o '%s'",
            shellescape(path), perm, shellescape(path)))
    cmd.Stdin = strings.NewReader(data)
    return cmd.Run()
}
```

Used by both `tools/edit.go` (server-side none sandbox) and `internal/runnerclient/native.go` (runner-side).

## 6. Data Flow

### 6.1 Normal Execution (No run_as)

```
LLM: {name: "Shell", arguments: {command: "go test ./..."}}
  → ShellTool.Execute()
  → buildSpec(): spec.RunAsUser = "" (no run_as param)
  → NoneSandbox.Exec(spec)
  → buildCmdFromSpec(): exec.Command("/bin/sh", "-c", "go test ./...")
  → [unchanged from current behavior]
```

### 6.2 Default User Execution (No Approval)

```
LLM: {name: "Shell", arguments: {command: "go build ./...", run_as: "alice"}}
  → ShellTool.Execute(): params.RunAs = "alice"
  → ApprovalHook.PreToolUse(): runAs == defaultUser → return nil
  → buildSpec(): spec.RunAsUser = "alice"
  → NoneSandbox.Exec(spec)
  → buildCmdFromSpec(): exec.Command("sudo", "-n", "-H", "-u", "alice", "--", "/bin/sh", "-l", "-c", "go build ./...")
```

### 6.3 Privileged User Execution (With Approval)

```
LLM: {name: "Shell", arguments: {command: "apt install nginx", run_as: "root"}}
  → ShellTool.Execute(): params.RunAs = "root"
  → ApprovalHook.PreToolUse(): runAs == privilegedUser
    → CLIApprovalHandler.RequestApproval()
    → CLI shows approval dialog
    → User presses Y → ApprovalApproved
  → buildSpec(): spec.RunAsUser = "root"
  → NoneSandbox.Exec(spec)
  → buildCmdFromSpec(): exec.Command("sudo", "-n", "-H", "-u", "root", "--", "/bin/sh", "-l", "-c", "apt install nginx")
```

### 6.4 Privileged User Denied

```
LLM: {name: "Shell", arguments: {command: "apt install nginx", run_as: "root"}}
  → ApprovalHook.PreToolUse(): runAs == privilegedUser
    → CLI shows approval dialog
    → User presses N → ApprovalDenied
  → return error: "user denied execution as root"
  → ToolResult{Summary: "Execution denied by user", IsError: true}
  → LLM learns to respect user's decision
```

### 6.5 Invalid User

```
LLM: {name: "Shell", arguments: {command: "ls", run_as: "hacker"}}
  → ApprovalHook.PreToolUse(): runAs not in {defaultUser, privilegedUser}
  → return error: `unknown run_as user "hacker": must be "alice" or "root"`
```

### 6.6 Runner Execution (Remote Sandbox Path)

When the agent uses a remote runner, the flow extends through the WebSocket protocol:

```
LLM: {name: "Shell", arguments: {command: "npm install", run_as: "alice"}}
  → ShellTool.Execute(): params.RunAs = "alice"
  → ApprovalHook.PreToolUse(): runAs == defaultUser → return nil
  → shouldUseSandbox(ctx) = true (remote runner connected)
  → buildSpec(): spec.RunAsUser = "alice"
  → RemoteSandbox.Exec(spec)
    → WebSocket: {"type": "exec", "body": {"command": "npm install", "shell": true, "run_as_user": "alice"}}
  → Runner handler.handleExec()
    → spec.RunAsUser = req.RunAsUser = "alice"
    → NativeExecutor.Exec(ctx, spec)
      → cmdbuilder.Build(ctx, ..., runAsUser: "alice")
      → exec.Command("sudo", "-n", "-H", "-u", "alice", "--", "sh", "-c", "npm install")
  → Result flows back through WebSocket
```

The approval happens on the **agent side** (server), before the request reaches the runner. The runner itself just executes — it trusts the server's approval decision. The runner validates `run_as_user` against its local `--default-user`/`--privileged-user` config as a safety net.

## 7. Configuration

### 7.1 Feature Activation Flow

```
1. User opens CLI settings panel → "Permission Control" section
2. User sets default_user and/or privileged_user
3. System detects first-time enable (sudoers not yet configured)
4. System generates sudoers setup script and shows it to user
5. User runs the script (requires sudo password once)
6. System reminds: "Restart xbot-cli for changes to take effect"
7. On restart: ApprovalHook registered, system prompt updated
```

### 7.2 sudoers Setup Script Generation

When the user first enables permission control, the system generates a setup script:

```bash
#!/bin/bash
# xbot permission control setup script
# Run this script with sudo: sudo bash /path/to/setup-xbot-sudoers.sh

CURRENT_USER="$(whoami)"
XBOT_USER="${CURRENT_USER}"  # xbot process runs as this user

sudoers_file="/etc/sudoers.d/xbot"

echo "Setting up sudoers for xbot permission control..."
echo ""
echo "This will allow user '${XBOT_USER}' to run commands as:"
echo "  - alice (default, no approval needed)"
echo "  - root (privileged, requires approval)"

cat > /tmp/xbot-sudoers.$$ << 'EOF'
# xbot permission control — auto-generated
# Allows xbot process user to execute as configured users without password
xbot_user ALL=(alice) NOPASSWD: ALL
xbot_user ALL=(root) NOPASSWD: ALL
EOF

# Replace placeholder with actual username
sed -i "s/xbot_user/${XBOT_USER}/g" /tmp/xbot-sudoers.$$

# Validate and install (visudo checks syntax)
visudo -c -f /tmp/xbot-sudoers.$$ && \
    install -m 0440 /tmp/xbot-sudoers.$$ "${sudoers_file}" && \
    rm /tmp/xbot-sudoers.$$ && \
    echo "✓ sudoers configured at ${sudoers_file}" && \
    echo "⚠ Please restart xbot-cli for changes to take effect" || {
    rm -f /tmp/xbot-sudoers.$$
    echo "✗ Failed to configure sudoers"
    exit 1
}
```

The script:
- Uses `visudo -c` to validate syntax before installing (prevents locking yourself out)
- Installs with `0440` permissions (required by sudoers)
- Replaces the placeholder username with the actual xbot process user
- Reminds user to restart xbot-cli

### 7.3 Detection: Is sudoers Configured?

Before enabling the feature, check if sudoers is already set up:

```go
func isSudoersConfigured() bool {
    currentUser := os.Getenv("USER")
    // Test: sudo -n -u <default_user> true
    cmd := exec.Command("sudo", "-n", "-u", currentUser, "--", "true")
    return cmd.Run() == nil
}
```

If not configured, show the setup script. If already configured, skip the reminder.

### 7.4 User Settings (Runtime)

Via CLI settings panel or API:
```
default_user = "alice"
privileged_user = "root"
```

If `default_user` is not set, the `run_as` parameter is the only way to switch users.
If `privileged_user` is not set, all `run_as` values that aren't `default_user` are rejected.
If **both** are unset, the feature is disabled — all `run_as` values are rejected.

## 8. Implementation Plan

### Phase 1: Shared Command Builder + Shell Support

Extract the shared command builder first — both server and runner depend on it.

| # | Task | File | Description |
|---|------|------|-------------|
| 1.1 | Shared `cmdbuilder` package | `internal/cmdbuilder/cmdbuilder.go` (new) | `Build()` with `sudo -n -H -u` wrapping, `WriteFileAsUser()` helper |
| 1.2 | Server `ExecSpec.RunAsUser` | `tools/sandbox.go` | Add field to ExecSpec struct |
| 1.3 | Server `buildCmdFromSpec` refactor | `tools/none_sandbox.go:431` | Delegate to `cmdbuilder.Build()` instead of inline cmd construction |
| 1.4 | Server async path | `tools/none_sandbox.go:368` | `noneSandboxExecAsync` inherits via refactored buildCmdFromSpec |
| 1.5 | **Shell schema change** | `tools/shell.go:48` | Add `run_as` to `Parameters()` — **protocol change, audit S1** |
| 1.6 | **Shell params struct** | `tools/shell.go:57` | Add `RunAs string` to Execute params — **audit S5** |
| 1.7 | Settings read helper | `tools/` | Read `default_user`/`privileged_user` from user_settings |
| 1.8 | **Opt-in guard** | `tools/shell.go` | When both settings empty: reject non-empty `run_as` with clear error |
| 1.9 | Validation | `tools/shell.go` | When enabled: validate `run_as` against configured users |
| 1.10 | **Runner `ExecSpec.RunAsUser`** | `internal/runnerclient/executor.go` | Add field to runner ExecSpec |
| 1.11 | **Runner `NativeExecutor` refactor** | `internal/runnerclient/native.go:35` | Delegate to `cmdbuilder.Build()` instead of inline cmd construction |
| 1.12 | **Runner protocol extension** | `internal/runnerproto/runner_proto.go` | Add `RunAsUser` to `ExecRequest` |
| 1.13 | **Runner handler threading** | `internal/runnerclient/handler.go:209` | Pass `req.RunAsUser` to ExecSpec |
| 1.14 | **Runner CLI flags** | `cmd/runner/main.go` | `--default-user`, `--privileged-user` flags |
| 1.15 | **Runner local validation** | `internal/runnerclient/handler.go` | Validate `run_as_user` against local config |

### Phase 2: File Tools + Approval System

| # | Task | File | Description |
|---|------|------|-------------|
| 2.1 | **FileCreate schema change** | `tools/edit.go:35` | Add `run_as` to `Parameters()` — **protocol change, audit S2** |
| 2.2 | **FileCreate params struct** | `tools/edit.go:42` | Add `RunAs string` to FileCreateParams — **audit S6** |
| 2.3 | **FileReplace schema change** | `tools/edit.go:130` | Add `run_as` to `Parameters()` — **protocol change, audit S3** |
| 2.4 | **FileReplace params struct** | `tools/edit.go:142` | Add `RunAs string` to FileReplaceParams — **audit S7** |
| 2.5 | Server `writeFileAsUser` | `tools/edit.go` | Use `cmdbuilder.WriteFileAsUser()` in executeLocal |
| 2.6 | **Runner `write_file` with user** | `internal/runnerclient/handler.go` | Thread `RunAsUser` to WriteFile path |
| 2.7 | Approval interface | `tools/approval.go` (new) | `ApprovalRequest/Result/Handler` |
| 2.8 | ApprovalHook | `tools/approval.go` | ToolHook implementation — only registered when feature is enabled |
| 2.9 | CLI handler | `channel/cli_approval.go` (new) | AskUser-based approval dialog |
| 2.10 | **Conditional hook registration** | `agent/engine_wire.go` | Register ApprovalHook only when privileged_user is set |

### Phase 3: Integration & Polish

| # | Task | File | Description |
|---|------|------|-------------|
| 3.1 | **Conditional system prompt** | `agent/` | User info section only included when feature is enabled |
| 3.2 | Settings UI | `channel/cli_panel.go` | Add default_user/privileged_user to settings panel |
| 3.3 | **sudoers setup script** | `tools/` or new file | Generate script, detect if sudoers already configured |
| 3.4 | **Restart reminder** | `channel/cli_panel.go` | Show "restart xbot-cli" message after sudoers setup |
| 3.5 | Error messages | Various | Clear errors for misconfiguration, sudo failures, feature disabled |
| 3.6 | Tests | `internal/cmdbuilder/cmdbuilder_test.go` | Unit tests for sudo wrapping + writeFileAsUser |
| 3.7 | Tests | `tools/approval_test.go` | Unit tests for hook allow/deny logic |
| 3.8 | Tests | Schema backward compat | Verify `run_as` omitted → identical behavior |
| 3.9 | Documentation | `docs/ARCHITECTURE.md` | Update with permission control section |

## 9. Edge Cases & Considerations

### 9.1 sudo Not Configured
If `sudo -n -u <user>` fails (sudoers not set up), `buildCmdFromSpec` returns a clear error:
```
failed to execute as user "alice": sudo requires a password (configure NOPASSWD in /etc/sudoers.d/)
```

### 9.2 Process Group Kill with sudo
`killProcessGroup` sends SIGKILL to `-pid` (negative PID = process group). With `sudo`, the process tree is:
```
xbot → sudo -u alice → /bin/sh -l -c "command" → command subprocesses
```
`Setpgid=true` on the sudo process means the entire tree is in the same process group. SIGKILL to `-pid` kills the whole tree. ✓

### 9.3 Background Tasks with sudo
`noneSandboxExecAsync` also uses `buildCmdFromSpec`, so `RunAsUser` is automatically supported. The KeepAlive process management works the same way.

### 9.4 File Ownership
Files created via `writeFileAsUser` are owned by the target user (since the write process runs as that user). This is correct behavior — files created by "alice" belong to alice.

### 9.5 Approval Timeout
If the user doesn't respond within a configurable timeout (default 60s), auto-deny. Prevents indefinite blocking of the agent loop.

### 9.6 SubAgent Inheritance
SubAgents inherit the parent's user settings. The ApprovalHook reads from `ToolContext.OriginUserID`, so it automatically picks up the right configuration. SubAgents cannot escalate beyond what the parent's settings allow.

### 9.7 None Sandbox + NativeExecutor (v1)
The `RunAsUser` field is processed by `NoneSandbox` (server-side) and `NativeExecutor` (runner-side). DockerSandbox and DockerExecutor ignore it in v1. Future: docker could use `--user` flag; the shared `cmdbuilder` makes this straightforward to add.

## 10. Open Questions

1. **Should read-only tools (Read, Grep, Glob) support `run_as`?** Probably not needed in v1 — the OS user mainly affects write operations. Can add later if needed (e.g., reading files owned by another user).

2. **Per-tool user override in config?** E.g., "FileReplace always uses alice, Shell can use either." Useful but adds complexity. Defer to v2.

3. **Approval caching / "always allow for this session"?** Common UX pattern. Defer to v2 — start with per-request approval.

4. **Audit logging?** Log all privileged user executions (approved or denied) for compliance. Should be added in Phase 2.
