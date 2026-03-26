# Remote Sandbox Workspace Unification — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make remote sandbox mode use runner's workspace path instead of host paths, and sync global skills/agents to the runner on registration.

**Architecture:** Add `Workspace(userID string) string` to the `Sandbox` interface. Remove `SandboxWorkDir` from `RunConfig`/`ToolContext` — all consumers use `sandbox.Workspace(userID)` instead. Change `SandboxEnabled` from `sandboxMode == "docker"` to `sandbox.Name() != "none"`. Sync skills/agents to runner on registration via `RemoteSandbox`.

**Tech Stack:** Go 1.21+, gorilla/websocket, existing Sandbox interface

---

### Task 1: Add `Workspace(userID)` to Sandbox interface

**Files:**
- Modify: `tools/sandbox.go:60-101` (Sandbox interface)
- Modify: `tools/docker_sandbox.go` (add Workspace method)
- Modify: `tools/none_sandbox.go` (add Workspace method)
- Modify: `tools/remote_sandbox.go` (update Workspace method — already exists from prior work)

- [ ] **Step 1: Add `Workspace(userID string) string` to the Sandbox interface**

In `tools/sandbox.go`, add the method to the `Sandbox` interface after the `Name()` method (around line 94):

```go
	// === Lifecycle ===
	Name() string
	Workspace(userID string) string  // Returns the workspace root path for the given user
	Close() error
```

- [ ] **Step 2: Implement `Workspace()` on `NoneSandbox`**

In `tools/none_sandbox.go`, add after the `Name()` method (after line 14):

```go
func (s *NoneSandbox) Workspace(userID string) string { return "" }
```

- [ ] **Step 3: Implement `Workspace()` on `DockerSandbox`**

In `tools/docker_sandbox.go`, add after the `Name()` method (after line 40):

```go
func (s *DockerSandbox) Workspace(userID string) string { return "/workspace" }
```

- [ ] **Step 4: Verify `RemoteSandbox.Workspace()` already exists**

Confirm `tools/remote_sandbox.go` already has the `Workspace(userID string) string` method (it was added in prior work). It should return `rc.workspace` from the runner connection.

- [ ] **Step 5: Build to verify interface satisfaction**

Run: `go build ./tools/...`
Expected: PASS (all three implementations satisfy the new interface method)

- [ ] **Step 6: Commit**

```bash
git add tools/sandbox.go tools/none_sandbox.go tools/docker_sandbox.go
git commit -m "feat(sandbox): add Workspace(userID) method to Sandbox interface"
```

---

### Task 2: Remove `SandboxWorkDir` from `RunConfig` and `ToolContext`

**Files:**
- Modify: `agent/engine.go:60` (RunConfig struct)
- Modify: `tools/interface.go:23` (ToolContext struct)
- Modify: `agent/engine.go:1343-1439` (buildToolContext)
- Modify: `agent/engine.go:1326-1341` (sandboxReadOnlyRoots)

- [ ] **Step 1: Remove `SandboxWorkDir` from `RunConfig`**

In `agent/engine.go`, line 60, remove this field:

```go
	SandboxWorkDir   string   // 沙箱内工作目录（如 /workspace）
```

- [ ] **Step 2: Remove `SandboxWorkDir` from `ToolContext`**

In `tools/interface.go`, line 23, remove this field:

```go
	SandboxWorkDir          string          // 沙箱内工作目录（如 Docker 为 /workspace，非沙箱时与 WorkspaceRoot 相同）
```

- [ ] **Step 3: Update `buildToolContext` to remove `SandboxWorkDir` references**

In `agent/engine.go`, `buildToolContext` function (starting at line 1345), remove line 1360:

```go
		SandboxWorkDir:       cfg.SandboxWorkDir,
```

And update line 1362 to pass empty string (or a helper):
```go
		SandboxReadOnlyRoots: sandboxReadOnlyRoots(cfg.ReadOnlyRoots, "", cfg.WorkspaceRoot),
```

- [ ] **Step 4: Update `sandboxReadOnlyRoots` — empty sandboxWorkDir returns passthrough**

The existing `sandboxReadOnlyRoots` function at line 1328 already handles this correctly — when `sandboxWorkDir == ""`, it returns `hostRoots` unchanged. No change needed to the function logic.

- [ ] **Step 5: Build to find all compile errors from removing SandboxWorkDir**

Run: `go build ./...`
Expected: FAIL — multiple files reference `SandboxWorkDir`. This is expected; we'll fix them in subsequent tasks.

- [ ] **Step 6: Do NOT commit yet** — wait until all consumers are updated (Tasks 3-8).

---

### Task 3: Update path guard functions to use Sandbox interface

**Files:**
- Modify: `tools/path_guard.go:10-22` (defaultWorkspaceRoot)
- Modify: `tools/path_guard.go:170-175` (sandboxBaseDir)
- Modify: `tools/path_guard.go:179-181` (shouldUseSandbox)
- Modify: `tools/path_guard_test.go:75-95` (TestSandboxBaseDir)

- [ ] **Step 1: Rewrite `defaultWorkspaceRoot`**

In `tools/path_guard.go`, replace the `defaultWorkspaceRoot` function (lines 10-22):

```go
func defaultWorkspaceRoot(ctx *ToolContext) string {
	if ctx == nil {
		return ""
	}
	if ctx.Sandbox != nil && ctx.Sandbox.Name() != "none" {
		return ctx.Sandbox.Workspace(ctx.OriginUserID)
	}
	if ctx.WorkspaceRoot != "" {
		return ctx.WorkspaceRoot
	}
	return ctx.WorkingDir
}
```

- [ ] **Step 2: Rewrite `sandboxBaseDir`**

In `tools/path_guard.go`, replace `sandboxBaseDir` (lines 170-175):

```go
func sandboxBaseDir(ctx *ToolContext) string {
	if ctx != nil && ctx.Sandbox != nil && ctx.Sandbox.Name() != "none" {
		return ctx.Sandbox.Workspace(ctx.OriginUserID)
	}
	return ""
}
```

- [ ] **Step 3: Rewrite `shouldUseSandbox`**

In `tools/path_guard.go`, replace `shouldUseSandbox` (lines 179-181):

```go
func shouldUseSandbox(ctx *ToolContext) bool {
	return ctx != nil && ctx.Sandbox != nil && ctx.Sandbox.Name() != "none"
}
```

- [ ] **Step 4: Update `TestSandboxBaseDir` to use Sandbox interface**

In `tools/path_guard_test.go`, update the test to use mock sandbox instead of `SandboxWorkDir`:

```go
type mockSandbox struct {
	name      string
	workspace string
}

func (m *mockSandbox) Name() string                                               { return m.name }
func (m *mockSandbox) Workspace(userID string) string                             { return m.workspace }
func (m *mockSandbox) Exec(ctx context.Context, spec ExecSpec) (*ExecResult, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *mockSandbox) ReadFile(ctx context.Context, path string, userID string) ([]byte, error) {
	return nil, os.ErrNotExist
}
func (m *mockSandbox) WriteFile(ctx context.Context, path string, data []byte, perm os.FileMode, userID string) error {
	return nil
}
func (m *mockSandbox) Stat(ctx context.Context, path string, userID string) (*SandboxFileInfo, error) {
	return nil, os.ErrNotExist
}
func (m *mockSandbox) ReadDir(ctx context.Context, path string, userID string) ([]DirEntry, error) {
	return nil, os.ErrNotExist
}
func (m *mockSandbox) MkdirAll(ctx context.Context, path string, perm os.FileMode, userID string) error {
	return nil
}
func (m *mockSandbox) Remove(ctx context.Context, path string, userID string) error {
	return os.ErrNotExist
}
func (m *mockSandbox) RemoveAll(ctx context.Context, path string, userID string) error {
	return nil
}
func (m *mockSandbox) GetShell(userID string, workspace string) (string, error) {
	return "/bin/bash", nil
}
func (m *mockSandbox) Close() error                     { return nil }
func (m *mockSandbox) CloseForUser(userID string) error { return nil }
func (m *mockSandbox) IsExporting(userID string) bool    { return false }
func (m *mockSandbox) ExportAndImport(userID string) error { return nil }

func TestSandboxBaseDir(t *testing.T) {
	tests := []struct {
		name string
		ctx  *ToolContext
		want string
	}{
		{"nil ctx", nil, ""},
		{"none sandbox", &ToolContext{Sandbox: &mockSandbox{name: "none"}}, ""},
		{"remote sandbox", &ToolContext{
			Sandbox:     &mockSandbox{name: "remote", workspace: "/home/user/ws"},
			OriginUserID: "u1",
		}, "/home/user/ws"},
		{"docker sandbox", &ToolContext{
			Sandbox:     &mockSandbox{name: "docker", workspace: "/workspace"},
			OriginUserID: "u1",
		}, "/workspace"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sandboxBaseDir(tt.ctx)
			if got != tt.want {
				t.Errorf("sandboxBaseDir() = %q, want %q", got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 5: Run tests**

Run: `go test ./tools/ -run TestSandboxBaseDir -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add tools/path_guard.go tools/path_guard_test.go
git commit -m "refactor(path_guard): use Sandbox interface instead of SandboxWorkDir"
```

---

### Task 4: Update SandboxEnabled from docker-only to all non-none modes

**Files:**
- Modify: `agent/engine_wire.go:76` (buildBaseRunConfig)
- Modify: `agent/engine_wire.go:341` (buildSubAgentRunConfig)
- Modify: `agent/engine_wire.go:478` (buildToolExecutor)
- Modify: `agent/interactive.go:359` (buildParentToolContext)

- [ ] **Step 1: Update `buildBaseRunConfig`**

In `agent/engine_wire.go`, line 76, change:

```go
		SandboxEnabled:   a.sandboxMode == "docker",
```
to:
```go
		SandboxEnabled:   a.sandboxMode != "none",
```

Also remove line 69 (`SandboxWorkDir: a.sandboxWorkDir()`).

- [ ] **Step 2: Update `buildSubAgentRunConfig`**

In `agent/engine_wire.go`, line 341, change:

```go
		SandboxEnabled:   parentCtx.SandboxEnabled,
```
to:
```go
		SandboxEnabled:   shouldUseSandbox(parentCtx),
```

Also remove line 334 (`SandboxWorkDir: parentCtx.SandboxWorkDir`).

- [ ] **Step 3: Update `buildToolExecutor`**

In `agent/engine_wire.go`, line 478, change:

```go
		SandboxEnabled:   a.sandboxMode == "docker",
```
to:
```go
		SandboxEnabled:   a.sandboxMode != "none",
```

Also remove line 471 (`SandboxWorkDir: a.sandboxWorkDir()`).

- [ ] **Step 4: Update `buildParentToolContext`**

In `agent/interactive.go`, line 359, change:

```go
		SandboxEnabled:      a.sandboxMode == "docker",
```
to:
```go
		SandboxEnabled:      a.sandboxMode != "none",
```

Also remove line 352 (`SandboxWorkDir: a.sandboxWorkDir()`).

- [ ] **Step 5: Build to check for remaining `sandboxWorkDir` references**

Run: `go build ./agent/...`
Expected: Some compile errors remain (SandboxWorkDir references in SubAgent prompt building, offload, etc.)

- [ ] **Step 6: Commit**

```bash
git add agent/engine_wire.go agent/interactive.go
git commit -m "fix(sandbox): SandboxEnabled covers remote mode, remove SandboxWorkDir from builders"
```

---

### Task 5: Update SubAgent prompt building and CWD translation

**Files:**
- Modify: `agent/engine_wire.go:252-254` (SubAgent workDir for prompt)
- Modify: `agent/engine.go:1426-1429` (CWD translation in buildToolContext)
- Modify: `agent/engine.go:1081,1127` (offload SandboxWorkDir references)

- [ ] **Step 1: Update SubAgent `workDir` for prompt**

In `agent/engine_wire.go`, lines 252-255, replace:

```go
	workDir := parentCtx.SandboxWorkDir
	if workDir == "" {
		workDir = parentCtx.WorkspaceRoot
	}
```
with:
```go
	workDir := parentCtx.WorkspaceRoot
	if parentCtx.Sandbox != nil && parentCtx.Sandbox.Name() != "none" {
		workDir = parentCtx.Sandbox.Workspace(parentCtx.OriginUserID)
	}
```

- [ ] **Step 2: Update CWD translation in `buildToolContext`**

In `agent/engine.go`, lines 1426-1429, replace:

```go
		if cwd != "" && cfg.SandboxEnabled && cfg.WorkspaceRoot != "" && cfg.SandboxWorkDir != "" {
			if strings.HasPrefix(cwd, cfg.WorkspaceRoot) {
				cwd = cfg.SandboxWorkDir + cwd[len(cfg.WorkspaceRoot):]
			}
		}
```
with:
```go
		if cwd != "" && cfg.Sandbox != nil && cfg.Sandbox.Name() != "none" && cfg.WorkspaceRoot != "" {
			sandboxWS := cfg.Sandbox.Workspace(cfg.OriginUserID)
			if sandboxWS != "" && strings.HasPrefix(cwd, cfg.WorkspaceRoot) {
				cwd = sandboxWS + cwd[len(cfg.WorkspaceRoot):]
			}
		}
```

- [ ] **Step 3: Update offload `MaybeOffload` call**

In `agent/engine.go`, line 1081, replace `cfg.SandboxWorkDir` with an empty string for now (offload sandbox path resolution will be handled in a separate concern — the offload system reads from host, not sandbox):

```go
			offloaded, wasOffloaded := cfg.OffloadStore.MaybeOffload(ctx, offloadSessionKey, tc.Name, tc.Arguments, offloadContent, cfg.WorkspaceRoot, "", cfg.OriginUserID)
```

Line 1127, similarly:
```go
			staleIDs := cfg.OffloadStore.InvalidateStaleReads(ctx, offloadSessionKey, cfg.WorkspaceRoot, "", cfg.OriginUserID)
```

Note: Offload system reads/stores on the server side. For remote mode, offload is not currently used (the runner doesn't have offload data). Passing empty `sandboxWorkDir` means no path translation happens, which is correct — the server sees server paths.

- [ ] **Step 4: Build**

Run: `go build ./agent/...`
Expected: PASS (after Tasks 2-4 are applied)

- [ ] **Step 5: Commit**

```bash
git add agent/engine.go agent/engine_wire.go
git commit -m "fix(subagent): use Sandbox.Workspace for prompt and CWD, remove SandboxWorkDir from offload"
```

---

### Task 6: Update tool guards that reference `SandboxEnabled` and `SandboxWorkDir`

**Files:**
- Modify: `tools/shell.go:144,192` (ShellTool dir selection)
- Modify: `tools/read.go:56` (ReadTool guard)
- Modify: `tools/edit.go:127` (EditTool guard)
- Modify: `tools/grep.go:92` (GrepTool guard)
- Modify: `tools/glob.go:107` (GlobTool guard)
- Modify: `tools/cd.go:52,558,623,641` (CdTool guards)
- Modify: `tools/feishu_mcp/file.go:81,391` (FeishuMCP file guards)
- Modify: `tools/feishu_mcp/download.go:115` (FeishuMCP download guard)
- Modify: `tools/skill.go:92-93` (resolveSkill bases)
- Modify: `tools/sandbox_exec.go:148` (setSandboxDir docker case)
- Modify: `tools/sandbox_unit_test.go` (test ToolContext updates)

- [ ] **Step 1: Update `tools/shell.go` dir selection**

In `tools/shell.go`, replace lines 144-145:

```go
		} else if toolCtx != nil && toolCtx.SandboxWorkDir != "" {
			dir = toolCtx.SandboxWorkDir // e.g. /workspace
```
with:
```go
		} else if toolCtx != nil && toolCtx.Sandbox != nil && toolCtx.Sandbox.Name() != "none" {
			dir = toolCtx.Sandbox.Workspace(toolCtx.OriginUserID)
```

- [ ] **Step 2: Update `tools/read.go` guard**

In `tools/read.go`, line 56, replace:

```go
	if ctx != nil && ctx.SandboxEnabled && ctx.WorkspaceRoot != "" {
```
with:
```go
	if shouldUseSandbox(ctx) {
```

- [ ] **Step 3: Update `tools/edit.go` guard**

In `tools/edit.go`, line 127, replace:

```go
	if ctx != nil && ctx.SandboxEnabled && ctx.WorkspaceRoot != "" {
```
with:
```go
	if shouldUseSandbox(ctx) {
```

- [ ] **Step 4: Update `tools/grep.go` guard**

In `tools/grep.go`, line 92, replace:

```go
	if ctx != nil && ctx.SandboxEnabled && ctx.WorkspaceRoot != "" {
```
with:
```go
	if shouldUseSandbox(ctx) {
```

- [ ] **Step 5: Update `tools/glob.go` guard**

In `tools/glob.go`, line 107, replace:

```go
	if ctx != nil && ctx.SandboxEnabled && ctx.WorkspaceRoot != "" {
```
with:
```go
	if shouldUseSandbox(ctx) {
```

- [ ] **Step 6: Update `tools/cd.go` guards**

In `tools/cd.go`, there are multiple references:

Line 52, replace:
```go
	if ctx != nil && ctx.SandboxEnabled && ctx.WorkspaceRoot != "" {
```
with:
```go
	if shouldUseSandbox(ctx) {
```

Lines 558 and 623, replace each occurrence of `ctx.SandboxWorkDir` with:
```go
	sandboxBaseDir(ctx)
```

Line 641, replace:
```go
	sandboxBase := ctx.SandboxWorkDir
```
with:
```go
	sandboxBase := sandboxBaseDir(ctx)
```

- [ ] **Step 7: Update `tools/feishu_mcp/file.go` guards**

Lines 81 and 391, replace each occurrence of:
```go
	if ctx.Sandbox != nil && ctx.SandboxWorkDir != "" {
```
with:
```go
	if shouldUseSandbox(ctx) {
```

- [ ] **Step 8: Update `tools/feishu_mcp/download.go` guard**

Line 115, replace:
```go
	useSandbox := ctx != nil && ctx.Sandbox != nil && ctx.SandboxWorkDir != ""
```
with:
```go
	useSandbox := shouldUseSandbox(ctx)
```

- [ ] **Step 9: Update `tools/skill.go` resolveSkill bases**

In `tools/skill.go`, lines 92-93, replace:
```go
			filepath.Join(ctx.SandboxWorkDir, "skills"),
			filepath.Join(ctx.SandboxWorkDir, ".skills"),
```
with:
```go
			filepath.Join(sandboxBaseDir(ctx), "skills"),
			filepath.Join(sandboxBaseDir(ctx), ".skills"),
```

- [ ] **Step 10: Update `tools/sandbox_exec.go` setSandboxDir docker case**

In `tools/sandbox_exec.go`, line 148, replace:
```go
		spec.Dir = ctx.SandboxWorkDir // 容器内路径，如 /workspace
```
with:
```go
		spec.Dir = ctx.Sandbox.Workspace(ctx.OriginUserID) // 容器内路径，如 /workspace
```

- [ ] **Step 11: Update test ToolContexts in `tools/sandbox_unit_test.go`**

Replace all occurrences of `SandboxWorkDir: "/workspace"` with the mock sandbox approach:

```go
// Before:
ctx := &ToolContext{
    SandboxWorkDir: "/workspace",
    ...
}

// After:
ctx := &ToolContext{
    Sandbox:     &mockSandbox{name: "docker", workspace: "/workspace"},
    OriginUserID: "test-user",
    ...
}
```

Add the `mockSandbox` type from Task 3 to this test file if not already defined (use a shared test helper or duplicate it).

- [ ] **Step 12: Update `tools/path_guard_test.go` remaining tests**

In `tools/path_guard_test.go`, update the `ResolveReadPath_SandboxPathConversion` test (line 128) to use the mock sandbox instead of `SandboxWorkDir`:

```go
	ctx := &ToolContext{
		WorkspaceRoot:  filepath.Join(root, "host-workspace"),
		Sandbox:       &mockSandbox{name: "docker", workspace: sandboxDir},
		SandboxEnabled: true,
		OriginUserID:   "test-user",
	}
```

- [ ] **Step 13: Build and test**

Run: `go build ./tools/... && go test ./tools/ -v -count=1`
Expected: PASS

- [ ] **Step 14: Commit**

```bash
git add tools/shell.go tools/read.go tools/edit.go tools/grep.go tools/glob.go tools/cd.go tools/feishu_mcp/file.go tools/feishu_mcp/download.go tools/skill.go tools/sandbox_exec.go tools/sandbox_unit_test.go tools/path_guard_test.go
git commit -m "refactor(tools): replace SandboxWorkDir with Sandbox interface in all tool guards"
```

---

### Task 7: Remove `sandboxWorkDir()` method and update initStores

**Files:**
- Modify: `agent/agent.go:1729-1737` (sandboxWorkDir method — delete)
- Modify: `agent/agent.go:424-429` (initStores sandboxWorkDir computation)
- Modify: `agent/agent.go:605-608` (RegistryManager sandboxWorkDir)
- Modify: `agent/skills.go` (SkillStore sandboxWorkDir)
- Modify: `agent/agents.go` (AgentStore sandboxWorkDir)
- Modify: `agent/registry.go` (RegistryManager sandboxWorkDir)

- [ ] **Step 1: Delete `sandboxWorkDir()` method**

In `agent/agent.go`, delete lines 1729-1737:

```go
// sandboxWorkDir returns the sandbox working directory path.
// In docker mode, this is "/workspace" (the container-internal mount point).
// In none mode, this is empty (no sandbox path mapping needed).
func (a *Agent) sandboxWorkDir() string {
	if a.sandboxMode == "docker" {
		return "/workspace"
	}
	return ""
}
```

- [ ] **Step 2: Update `initStores` to remove `sandboxWorkDir` parameter**

In `agent/agent.go`, lines 424-429, replace:

```go
	sandboxWorkDir := ""
	if cfg.SandboxMode == "docker" || cfg.SandboxMode == "remote" {
		sandboxWorkDir = "/workspace"
	}

	skillStore := NewSkillStore(cfg.WorkDir, globalSkillDirs, cfg.Sandbox, sandboxWorkDir)
```
with:
```go
	skillStore := NewSkillStore(cfg.WorkDir, globalSkillDirs, cfg.Sandbox)
```

And line 436:
```go
	agentStore := NewAgentStore(cfg.WorkDir, agentsDir, cfg.Sandbox, sandboxWorkDir)
```
with:
```go
	agentStore := NewAgentStore(cfg.WorkDir, agentsDir, cfg.Sandbox)
```

- [ ] **Step 3: Update `SkillStore` to remove `sandboxWorkDir` field**

In `agent/skills.go`:

Remove the `sandboxWorkDir` field from the struct (line 24):
```go
	sandboxWorkDir string        // 沙箱内工作目录（"/workspace" for docker/remote, "" for none）
```

Update constructor (line 33):
```go
func NewSkillStore(workDir string, globalDirs []string, sandbox tools.Sandbox) *SkillStore {
	return &SkillStore{
		workDir:    workDir,
		globalDirs: globalDirs,
		sandbox:    sandbox,
	}
}
```

Update `userSkillsDir` (line 57-62):
```go
func (s *SkillStore) userSkillsDir(senderID string) string {
	if s.sandbox != nil && s.sandbox.Name() != "none" {
		return filepath.Join(s.sandbox.Workspace(senderID), "skills")
	}
	return tools.UserSkillsRoot(s.workDir, senderID)
}
```

Update `isUserSkillsSandboxed` (line 65-67):
```go
func (s *SkillStore) isUserSkillsSandboxed() bool {
	return s.sandbox != nil && s.sandbox.Name() != "none"
}
```

- [ ] **Step 4: Update `AgentStore` to remove `sandboxWorkDir` field**

In `agent/agents.go`:

Remove the `sandboxWorkDir` field from the struct (line 20):
```go
	sandboxWorkDir string
```

Update constructor (line 24):
```go
func NewAgentStore(workDir string, globalDir string, sandbox tools.Sandbox) *AgentStore {
	return &AgentStore{workDir: workDir, globalDir: globalDir, sandbox: sandbox}
}
```

Update `userAgentsDir` (line 29-34):
```go
func (s *AgentStore) userAgentsDir(senderID string) string {
	if s.sandbox != nil && s.sandbox.Name() != "none" {
		return filepath.Join(s.sandbox.Workspace(senderID), "agents")
	}
	return tools.UserAgentsRoot(s.workDir, senderID)
}
```

Update `GetAgentsCatalog` sandbox checks (lines 54, 67) — replace `s.sandboxWorkDir == ""` with `s.sandbox == nil || s.sandbox.Name() == "none"`:
```go
	if i == 0 || s.sandbox == nil || s.sandbox.Name() == "none" {
```

- [ ] **Step 5: Update `RegistryManager` to remove `sandboxWorkDir` field**

In `agent/registry.go`:

Remove the `sandboxWorkDir` field from the struct (line 25):
```go
	sandboxWorkDir string // "/workspace" for docker/remote, "" for none
```

Update constructor (line 29):
```go
func NewRegistryManager(store *SkillStore, agentStore *AgentStore, sharedStore *sqlite.SharedSkillRegistry, workDir string, sandbox tools.Sandbox) *RegistryManager {
	return &RegistryManager{
		store:       store,
		agentStore:  agentStore,
		sharedStore: sharedStore,
		workDir:     workDir,
		sandbox:     sandbox,
	}
}
```

Update `useSandbox` (line 42):
```go
func (rm *RegistryManager) useSandbox() bool {
	return rm.sandbox != nil && rm.sandbox.Name() != "none"
}
```

Update `userSkillsDir` (line 51-56):
```go
func (rm *RegistryManager) userSkillsDir(senderID string) string {
	if rm.useSandbox() {
		return filepath.Join(rm.sandbox.Workspace(senderID), "skills")
	}
	return tools.UserSkillsRoot(rm.workDir, senderID)
}
```

Update `userAgentsDir` (line 58-63):
```go
func (rm *RegistryManager) userAgentsDir(senderID string) string {
	if rm.useSandbox() {
		return filepath.Join(rm.sandbox.Workspace(senderID), "agents")
	}
	return tools.UserAgentsRoot(rm.workDir, senderID)
}
```

- [ ] **Step 6: Update RegistryManager call site in `agent.go`**

In `agent/agent.go`, lines 605-609, replace:

```go
	sandboxWorkDir := ""
	if cfg.SandboxMode == "docker" || cfg.SandboxMode == "remote" {
		sandboxWorkDir = "/workspace"
	}
	a.registryManager = NewRegistryManager(a.skills, a.agents, sharedRegistry, cfg.WorkDir, cfg.Sandbox, sandboxWorkDir)
```
with:
```go
	a.registryManager = NewRegistryManager(a.skills, a.agents, sharedRegistry, cfg.WorkDir, cfg.Sandbox)
```

- [ ] **Step 7: Build**

Run: `go build ./agent/...`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add agent/agent.go agent/skills.go agent/agents.go agent/registry.go
git commit -m "refactor(agent): remove sandboxWorkDir, use Sandbox.Workspace in stores"
```

---

### Task 8: Update `initPipelines` and `buildPrompt` for remote mode

**Files:**
- Modify: `agent/context.go:146-151` (initPipelines promptWorkDir)
- Modify: `agent/context.go:198-204` (NewCronMessageContext — indirect, no change needed)

- [ ] **Step 1: Update `initPipelines` promptWorkDir**

In `agent/context.go`, lines 146-151, replace:

```go
	promptWorkDir := a.workDir
	if a.sandboxMode == "docker" {
		promptWorkDir = "/workspace"
	}
```
with:
```go
	promptWorkDir := a.workDir
	if a.sandboxMode == "docker" {
		promptWorkDir = "/workspace"
	} else if a.sandboxMode == "remote" {
		// Remote mode: promptWorkDir is per-user, set dynamically in middleware
		// Here we use a placeholder; actual per-request workDir is set by
		// SystemPromptMiddleware which reads from sandbox
		promptWorkDir = "" // will be overridden per-request
	}
```

Note: The `SystemPromptMiddleware` reads `PromptData.WorkDir` which is set per-request in `NewMessageContext`. The actual per-user remote workspace is resolved dynamically. The middleware will need to resolve it at render time.

- [ ] **Step 2: Check `SystemPromptMiddleware` and ensure it resolves remote workspace**

Read `agent/middleware.go` or equivalent to find `SystemPromptMiddleware`. It should already set `workDir` from context. Verify the remote case is handled — if the middleware creates a `PromptData` with `WorkDir`, it needs to resolve the remote workspace for the user.

If the middleware sets `WorkDir` from `a.workDir` without sandbox awareness, add sandbox resolution:

```go
workDir := a.workDir
if a.sandbox != nil && a.sandbox.Name() == "remote" && senderID != "" {
	if ws := a.sandbox.Workspace(senderID); ws != "" {
		workDir = ws
	}
}
```

- [ ] **Step 3: Build**

Run: `go build ./agent/...`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add agent/context.go agent/middleware.go
git commit -m "fix(prompt): resolve remote sandbox workspace for system prompt"
```

---

### Task 9: Sync skills/agents to runner on registration

**Files:**
- Modify: `tools/remote_sandbox.go` (add globalSkillDirs/agentsDir fields, sync on registration)
- Modify: `tools/skill_sync.go:27-36` (remove remote skip in EnsureSynced)

- [ ] **Step 1: Add `globalSkillDirs` and `agentsDir` to `RemoteSandbox`**

In `tools/remote_sandbox.go`, add fields to the `RemoteSandbox` struct (after line 48):

```go
// Skill/agent sync config
	globalSkillDirs []string // server-side global skill directories
	agentsDir       string   // server-side global agents directory
```

- [ ] **Step 2: Update `NewRemoteSandbox` to accept sync dirs**

Add a `RemoteSandboxSyncConfig` struct and update `NewRemoteSandbox`:

```go
// RemoteSandboxSyncConfig holds directories to sync to runners on registration.
type RemoteSandboxSyncConfig struct {
	GlobalSkillDirs []string // server-side global skill directories
	AgentsDir       string   // server-side global agents directory
}
```

Update `NewRemoteSandbox` signature to accept sync config:

```go
func NewRemoteSandbox(cfg RemoteSandboxConfig, syncCfg RemoteSandboxSyncConfig) (*RemoteSandbox, error) {
```

Store the sync dirs:
```go
	rs := &RemoteSandbox{
		// ... existing fields ...
		globalSkillDirs: syncCfg.GlobalSkillDirs,
		agentsDir:       syncCfg.AgentsDir,
	}
```

- [ ] **Step 3: Add sync on registration**

In `tools/remote_sandbox.go`, after storing the runner connection (after line 163), add:

```go
	// Sync global skills and agents to runner on registration
	go rs.syncToRunner(reg.UserID, reg.Workspace)
```

- [ ] **Step 4: Implement `syncToRunner` method**

Add a new method to `RemoteSandbox`:

```go
// syncToRunner copies global skills and agents to the runner's workspace.
// Runs in a goroutine after registration; errors are logged, not fatal.
func (rs *RemoteSandbox) syncToRunner(userID, workspace string) {
	if workspace == "" || (len(rs.globalSkillDirs) == 0 && rs.agentsDir == "") {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	log.WithFields(log.Fields{
		"user_id":  userID,
		"workspace": workspace,
	}).Info("Syncing global skills/agents to runner")

	// Sync skills
	for _, srcDir := range rs.globalSkillDirs {
		rs.syncDirToRunner(ctx, userID, workspace, srcDir, ".skills")
	}

	// Sync agents
	if rs.agentsDir != "" {
		rs.syncAgentsToRunner(ctx, userID, workspace, rs.agentsDir, ".agents")
	}

	log.WithField("user_id", userID).Info("Skill/agent sync to runner complete")
}

// syncDirToRunner copies skill subdirectories from a server directory to the runner.
func (rs *RemoteSandbox) syncDirToRunner(ctx context.Context, userID, workspace, srcDir, dstSubdir string) {
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		log.WithError(err).Warnf("syncToRunner: cannot read skill dir %s", srcDir)
		return
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		srcSkill := filepath.Join(srcDir, e.Name())
		dstSkill := filepath.Join(workspace, dstSubdir, e.Name())
		rs.syncTreeToRunner(ctx, userID, srcSkill, dstSkill)
	}
}

// syncAgentsToRunner copies .md agent files from a server directory to the runner.
func (rs *RemoteSandbox) syncAgentsToRunner(ctx context.Context, userID, workspace, srcDir, dstSubdir string) {
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		log.WithError(err).Warnf("syncToRunner: cannot read agents dir %s", srcDir)
		return
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		srcPath := filepath.Join(srcDir, e.Name())
		dstPath := filepath.Join(workspace, dstSubdir, e.Name())
		rs.syncFileToRunner(ctx, userID, srcPath, dstPath)
	}
}

// syncTreeToRunner recursively copies a directory tree to the runner.
func (rs *RemoteSandbox) syncTreeToRunner(ctx context.Context, userID, srcDir, dstDir string) {
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		srcPath := filepath.Join(srcDir, e.Name())
		dstPath := filepath.Join(dstDir, e.Name())
		if e.IsDir() {
			rs.syncTreeToRunner(ctx, userID, srcPath, dstPath)
		} else {
			rs.syncFileToRunner(ctx, userID, srcPath, dstPath)
		}
	}
}

// syncFileToRunner copies a single file to the runner.
func (rs *RemoteSandbox) syncFileToRunner(ctx context.Context, userID, srcPath, dstPath string) {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		log.WithError(err).Warnf("syncToRunner: cannot read %s", srcPath)
		return
	}

	if err := rs.MkdirAll(ctx, filepath.Dir(dstPath), 0o755, userID); err != nil {
		log.WithError(err).Warnf("syncToRunner: mkdir %s", filepath.Dir(dstPath))
		return
	}

	if err := rs.WriteFile(ctx, dstPath, data, 0o644, userID); err != nil {
		log.WithError(err).Warnf("syncToRunner: write %s", dstPath)
		return
	}
}
```

Note: `tools/remote_sandbox.go` needs to add `"os"` and `"path/filepath"` to its imports.

- [ ] **Step 5: Update `EnsureSynced` to not skip remote mode**

In `tools/skill_sync.go`, remove the remote skip (lines 33-36):

```go
	// V4: remote 模式下跳过全局 skill 同步（skill 由 remote sandbox 管理）
	if ctx.Sandbox != nil && ctx.Sandbox.Name() == "remote" {
		return
	}
```

After this change, `EnsureSynced` will try to sync to `ctx.WorkspaceRoot` (host path) even in remote mode. This is harmless — the host-side `.skills` directory may exist for other purposes, and the runner gets its copy from registration sync. However, we should guard it properly:

Replace the removed block with:
```go
	// Remote mode: skills are synced on runner registration, skip host-side sync
	if ctx.Sandbox != nil && ctx.Sandbox.Name() == "remote" {
		return
	}
```

Actually, keep the skip — it's correct. The runner gets synced on registration, not via EnsureSynced.

- [ ] **Step 6: Update callers of `NewRemoteSandbox` to pass sync config**

Search for all callers of `NewRemoteSandbox` and update them to pass the sync config. This will likely be in `main.go` or an initialization function. Add the `globalSkillDirs` and `agentsDir` parameters from the Agent config.

- [ ] **Step 7: Build**

Run: `go build ./...`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add tools/remote_sandbox.go tools/skill_sync.go
git commit -m "feat(remote-sandbox): sync global skills/agents to runner on registration"
```

---

### Task 10: Update engine tests and integration tests

**Files:**
- Modify: `agent/engine_test.go` (update SandboxWorkDir references)
- Modify: `agent/integration_test.go` (update SandboxWorkDir reference)

- [ ] **Step 1: Update `agent/engine_test.go`**

All references to `SandboxWorkDir: "/workspace"` in test RunConfig and ToolContext need to be replaced with the mock sandbox approach.

Search for `SandboxWorkDir` and replace with:
```go
Sandbox:     &mockSandbox{name: "docker", workspace: "/workspace"},
OriginUserID: "test-user",
```

The assertions checking `capturedCtx.SandboxWorkDir` need to be updated to check `capturedCtx.Sandbox.Workspace("test-user")`.

- [ ] **Step 2: Update `agent/integration_test.go`**

Line 80, replace:
```go
		SandboxWorkDir:       env.tmpDir,
```
with the mock sandbox pattern using the same `tmpDir` as workspace.

- [ ] **Step 3: Run all agent tests**

Run: `go test ./agent/ -v -count=1`
Expected: PASS

- [ ] **Step 4: Run all tests**

Run: `make test`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add agent/engine_test.go agent/integration_test.go
git commit -m "test: update engine tests to use Sandbox.Workspace instead of SandboxWorkDir"
```

---

### Task 11: Final verification

- [ ] **Step 1: Run full build**

Run: `make ci`
Expected: lint PASS, build PASS, test PASS

- [ ] **Step 2: Verify no remaining SandboxWorkDir references**

Run: `grep -r "SandboxWorkDir" --include="*.go" .`
Expected: No results (except maybe in offload code which passes empty string)

- [ ] **Step 3: Verify no remaining `sandboxWorkDir()` calls**

Run: `grep -r "sandboxWorkDir()" --include="*.go" .`
Expected: No results

- [ ] **Step 4: Verify SandboxEnabled is not docker-gated**

Run: `grep -n 'sandboxMode == "docker"' --include="*.go" -r .`
Expected: Only in places that need docker vs remote distinction (not for SandboxEnabled)

- [ ] **Step 5: Commit any final fixes**

```bash
git add -A
git commit -m "chore: final cleanup for remote sandbox workspace unification"
```
