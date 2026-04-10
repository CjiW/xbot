---
title: "plan-remote-sandbox"
weight: 190
---


> 最后更新：2026-03-26
> 状态：中书省 brainstorm 6 轮 + 门下省 2 轮审核
> 分支：`refactor/sandbox-tool-provider`
> 前置方案：V3（已废弃）

---

## 1. 背景与动机

### 1.1 V3 方案的致命缺陷

V3 方案设计了 Sandbox 接口（Exec/ReadFile/WriteFile），但在以下方面存在根本性问题：

1. **Offload 退化**：remote 模式下 offload hash 计算"自动退化"（`os.ReadFile` 失败 → 跳过 stale 检测），这是偷懒不是设计
2. **配置路径错误**：个人 skill/agent 路径用了 `.xbot/skills` 和 `.xbot/agents`，实际应为 `/workspace/skills` 和 `/workspace/agents`
3. **Remote 语义不清**：没有明确 "remote = 所有东西跑在用户本机，server 只是 WebSocket 协调器"

### 1.2 V4 硬约束（不可违反）

| # | 约束 | 说明 |
|---|------|------|
| H1 | Offload 不可退化 | hash 计算、内容读写必须全部通过 Sandbox.ReadFile/WriteFile 完成 |
| H2 | 配置路径 `/workspace/skills` + `/workspace/agents` | 不是 `.xbot/skills` / `.xbot/agents` |
| H3 | Remote = 用户本机 | ShellTool、ReadFile/WriteFile、skill/agent、offload、MCP stdio 全在用户本机 |
| H4 | 彻底重构（方案 A） | 不渐进迁移，直接重写，不怕重写历史文件 |

---

## 2. 架构总览

### 2.1 核心原则

```
Remote Sandbox 的本质：
  Server = WebSocket 协调器（不接触用户文件）
  Runner = 用户本机代理（执行所有文件 I/O 和命令）
  Sandbox 接口 = 统一抽象（所有模式共享同一套 API）
```

### 2.2 三种模式对比

| 维度 | none | docker | remote (V4) |
|------|------|--------|-------------|
| Shell 执行 | `os/exec` | `docker exec` | WebSocket → Runner `os/exec` |
| 文件读写 | `os.ReadFile` | `docker exec cat/tee` | WebSocket/HTTP → Runner `os.ReadFile` |
| Skill/Agent 发现 | 本地 `/workspace/skills` | 容器内 `/workspace/skills` | 用户本机 `/workspace/skills` |
| Offload hash | `os.ReadFile` | `docker exec cat` | Sandbox.ReadFile（穿越到用户本机） |
| MCP stdio | 本地启动 | 容器内启动 | 用户本机启动 |
| Server 是否接触用户文件 | ✅ 直接读写 | ✅ 通过 docker API | ❌ 不接触，只做 WebSocket 协调 |
| ReadOnlyRoots | ✅ 支持 | ✅ 支持 | ❌ 不支持 |

### 2.3 关键设计决策

| # | 决策 | 理由 |
|---|------|------|
| D1 | 单一 Sandbox 接口（不拆 FileIO） | FileIO 永远由 Sandbox 提供，拆分增加复杂度但收益不大 |
| D2 | Sandbox.ReadFile/WriteFile 要求绝对路径 | 相对路径解析依赖 session 级 Cwd，Sandbox 是多 session 共享 |
| D3 | Offload JSON 存 server 端（os.*），hash 走 Sandbox | offload 是 agent 内部状态，消费者在 server 端 |
| D4 | OffloadStore.MaybeOffload 接收 Sandbox 参数（方案 X2） | 显式依赖，OffloadStore 保持纯数据存储 |
| D5 | Skill 发现用 Server 缓存 + TTL 5min + 主动失效 | 简单可靠，避免 runner 侧 file watch 的跨平台复杂度 |
| D6 | skill_sync.go 在 remote 模式下跳过 | 全局 skill 由 SkillStore 在 server 端注入 system prompt |
| D7 | 废除 `__FEISHU_FILE__::` 协议 | 工具层直接 Sandbox.ReadFile + Feishu API 上传 |
| D8 | 删除 SandboxToHostPath / HostToSandboxPath | 路径语义统一为 sandbox 路径，转换是 Sandbox 实现内部细节 |
| D9 | Glob/Grep 统一走 Sandbox.Exec（删除 executeLocal） | 消除双路径分歧，Sandbox.Exec 在 none 模式下就是 os/exec |
| D10 | Sandbox.WriteFile 不自动 MkdirAll | 遵循最小意外原则，显式调用 Sandbox.MkdirAll |
| D11 | copyDir 跨 Sandbox 时展开 symlink | Sandbox 无 Symlink 方法，skill/agent 不应依赖 symlink 语义 |
| D12 | filepath.WalkDir 用递归 ReadDir 替代 | Sandbox 接口没有 Walk 方法 |

---

## 3. Sandbox 接口定义

### 3.1 核心接口

```go
package tools

// MaxSandboxFileSize is the maximum file size for ReadFile/WriteFile (500MB).
const MaxSandboxFileSize = 500 * 1024 * 1024

// ExecSpec defines the parameters for a sandbox command execution.
type ExecSpec struct {
    Command   string        // executable or shell command
    Args      []string      // arguments (ignored when Shell=true)
    Shell     bool          // use shell for execution (sh -c)
    Dir       string        // working directory (absolute path in sandbox)
    Env       []string      // environment variables
    Stdin     string        // stdin input
    Timeout   time.Duration // execution timeout
    Workspace string        // workspace root (for sandbox setup)
    UserID    string        // user identity (for sandbox routing)
}

// ExecResult holds the result of a sandbox command execution.
type ExecResult struct {
    Stdout   string // standard output
    Stderr   string // standard error
    ExitCode int    // exit code (-1 if timed out)
    TimedOut bool   // whether execution timed out
}

// SandboxFileInfo is the sandbox equivalent of os.FileInfo.
// Does NOT include Sys() — cross-process metadata is meaningless.
type SandboxFileInfo struct {
    Name    string      // base name
    Size    int64       // length in bytes
    Mode    os.FileMode // file mode bits
    ModTime time.Time   // modification time
    IsDir   bool        // is directory
}

// DirEntry represents a directory entry from ReadDir.
type DirEntry struct {
    Name  string
    IsDir bool
    Size  int64
}

// Sandbox defines the unified interface for all sandbox modes.
// All file path parameters must be absolute paths in sandbox format.
// Path conversion (sandbox↔host) is an internal concern of each implementation.
type Sandbox interface {
    // === Command Execution ===
    Exec(ctx context.Context, spec ExecSpec) (*ExecResult, error)

    // === File I/O ===
    // ReadFile reads the entire file at path. Path must be absolute.
    // Returns os.ErrNotExist if file does not exist.
    ReadFile(ctx context.Context, path string, userID string) ([]byte, error)

    // WriteFile writes data to path. Path must be absolute.
    // Does NOT auto-create parent directories — call MkdirAll first.
    WriteFile(ctx context.Context, path string, data []byte, perm os.FileMode, userID string) error

    // Stat returns file info. Path must be absolute.
    // Returns os.ErrNotExist if file does not exist.
    Stat(ctx context.Context, path string, userID string) (*SandboxFileInfo, error)

    // ReadDir lists directory entries. Path must be absolute.
    ReadDir(ctx context.Context, path string, userID string) ([]DirEntry, error)

    // MkdirAll creates directory tree. Path must be absolute.
    MkdirAll(ctx context.Context, path string, perm os.FileMode, userID string) error

    // Remove removes a file. Path must be absolute.
    Remove(ctx context.Context, path string, userID string) error

    // RemoveAll removes a directory tree. Path must be absolute.
    RemoveAll(ctx context.Context, path string, userID string) error

    // === Shell Configuration ===
    // GetShell returns the preferred shell command for the user/workspace.
    GetShell(userID string, workspace string) (string, error)

    // === Lifecycle ===
    Name() string
    Close() error
    CloseForUser(userID string) error

    // === Export/Import (docker-specific) ===
    IsExporting(userID string) bool
    ExportAndImport(userID string) error
}
```

### 3.2 三种实现

```go
// NoneSandbox — all operations are direct os.* calls
type NoneSandbox struct { /* no state */ }

// DockerSandbox — all operations go through docker exec / docker cp
type DockerSandbox struct {
    client    *client.Client
    image     string
    containers sync.Map // userID → containerID
}

// RemoteSandbox — all operations go through WebSocket or HTTP to Runner
type RemoteSandbox struct {
    connections sync.Map // userID → *runnerConnection
    wsServer    *ws.Server
    httpServer  *http.Server
}
```

### 3.3 路径语义

```
Sandbox 路径 = 绝对路径，以 SandboxWorkDir 为前缀

示例（SandboxWorkDir = /workspace）：
  /workspace/src/main.go      ← 绝对路径，所有模式通用
  /workspace/skills/my-skill/ ← skill 目录
  /workspace/agents/my-agent.md ← agent 文件
```

| 模式 | Sandbox 路径 → 实际文件系统 |
|------|--------------------------|
| none | `/workspace/src/main.go` → 宿主机 `/workspace/src/main.go`（或 SandboxWorkDir 对应目录） |
| docker | `/workspace/src/main.go` → 容器内 `/workspace/src/main.go`（DockerSandbox 内部转换为宿主机路径） |
| remote | `/workspace/src/main.go` → 用户本机 `/workspace/src/main.go`（通过 WebSocket/HTTP 传输给 Runner） |

---

## 4. RemoteSandbox 详细设计

### 4.1 架构

```
┌─────────────────────────────────────────────────────┐
│                    xbot Server                       │
│                                                     │
│  ┌──────────┐    WebSocket     ┌──────────────────┐ │
│  │  Agent   │◄───────────────►│ RemoteSandbox    │ │
│  │  Engine  │    (text/binary) │ (Sandbox impl)   │ │
│  └──────────┘                  └──────┬───────────┘ │
│                                       │             │
│  ┌──────────┐    HTTP API      ┌──────┴───────────┐ │
│  │ Feishu   │◄───────────────►│ Runner HTTP      │ │
│  │ Upload   │    (multipart)   │ Server           │ │
│  └──────────┘                  └──────────────────┘ │
└─────────────────────────────────────────────────────┘
                                        │
                              WebSocket / HTTP
                                        │
┌─────────────────────────────────────────────────────┐
│                User's Machine (Runner)              │
│                                                     │
│  ┌─────────────────────────────────────────────┐    │
│  │  Runner CLI (main.go)                       │    │
│  │  ├── WebSocket client → Server              │    │
│  │  ├── HTTP server (file upload/download)     │    │
│  │  ├── Exec handler → os/exec                 │    │
│  │  ├── ReadFile handler → os.ReadFile         │    │
│  │  ├── WriteFile handler → os.WriteFile       │    │
│  │  ├── Stat handler → os.Stat                 │    │
│  │  ├── ReadDir handler → os.ReadDir           │    │
│  │  ├── MkdirAll handler → os.MkdirAll         │    │
│  │  ├── Remove/RemoveAll → os.Remove/os.RemoveAll│   │
│  │  └── Path guard → validate path safety       │    │
│  └─────────────────────────────────────────────┘    │
│                                                     │
│  /workspace/          ← user's workspace            │
│  /workspace/skills/   ← user skills                 │
│  /workspace/agents/   ← user agents                 │
└─────────────────────────────────────────────────────┘
```

### 4.2 双通道文件传输

RemoteSandbox 内部对大文件使用 HTTP 通道，对调用方完全透明。

```
ReadFile  ≤4MB  → WebSocket: {type:"read_file", path:"...", user_id:"..."}
ReadFile  >4MB  → HTTP GET   http://runner:PORT/api/v1/files?path=...&user_id=...

WriteFile ≤4MB  → WebSocket: {type:"write_file", path:"...", data:"<base64>", perm:0644, user_id:"..."}
WriteFile >4MB  → HTTP POST  http://runner:PORT/api/v1/files (multipart)
```

| 参数 | 默认值 | 说明 |
|------|--------|------|
| HTTP 阈值 | 4MB | 超过此大小走 HTTP 通道 |
| 最大文件大小 | 500MB | 超过直接报错，防止 OOM |

**实现细节**：
- RemoteSandbox.ReadFile/WriteFile 内部判断 `len(data) > 4*1024*1024`
- WebSocket 消息用 base64 编码（二进制 WebSocket 帧在某些代理下不安全）
- HTTP 通道用 multipart/form-data（支持流式传输）
- Runner HTTP Server 监听 `127.0.0.1:{随机端口}`（启动时上报给 Server）

### 4.3 WebSocket 协议

```json
// Server → Runner (request)
{"id":"req_001","type":"exec","command":"ls","args":["-la"],"shell":true,"dir":"/workspace","env":[],"stdin":"","timeout":30,"user_id":"ou_xxx"}
{"id":"req_002","type":"read_file","path":"/workspace/src/main.go","user_id":"ou_xxx"}
{"id":"req_003","type":"write_file","path":"/workspace/src/main.go","data":"<base64>","perm":384,"user_id":"ou_xxx"}
{"id":"req_004","type":"stat","path":"/workspace/src/main.go","user_id":"ou_xxx"}
{"id":"req_005","type":"read_dir","path":"/workspace/skills","user_id":"ou_xxx"}
{"id":"req_006","type":"mkdir_all","path":"/workspace/skills/new","perm":493,"user_id":"ou_xxx"}
{"id":"req_007","type":"remove","path":"/workspace/skills/old","user_id":"ou_xxx"}
{"id":"req_008","type":"remove_all","path":"/workspace/skills/old-dir","user_id":"ou_xxx"}

// Runner → Server (response)
{"id":"req_001","type":"exec_result","stdout":"...","stderr":"...","exit_code":0,"timed_out":false}
{"id":"req_002","type":"file_content","data":"<base64>"}
{"id":"req_002","type":"error","message":"file not found","code":"ENOENT"}
{"id":"req_004","type":"file_info","name":"main.go","size":1234,"mode":420,"mod_time":"2026-03-26T00:00:00Z","is_dir":false}
{"id":"req_005","type":"dir_entries","entries":[{"name":"my-skill","is_dir":true,"size":4096},{"name":"readme.md","is_dir":false,"size":256}]}
```

### 4.4 Runner 设计

```
runner/
├── main.go              # CLI entry point
├── client.go            # WebSocket client (connects to server)
├── handler.go           # Request handlers (exec, read_file, etc.)
├── server.go            # HTTP server (large file transfer)
├── pathguard.go         # Path safety validation
├── auth.go              # Authentication (token-based)
└── go.mod
```

**Runner CLI 用法**：
```bash
xbot-runner --server ws://server:8080/ws --token <auth-token> --workspace /workspace
```

**Path Guard**：
- Runner 侧执行路径安全检查
- 拒绝路径逃逸：`/workspace/../../etc/passwd` → 拒绝
- 只允许访问 workspace 目录及其子目录
- 使用 `filepath.Clean` + `strings.HasPrefix` 验证

---

## 5. os.* 调用全景分类

### 5.1 必须改走 Sandbox（涉及用户文件）

#### 5.1.1 Offload hash 计算（2 处）

| 文件 | 行号 | 操作 | 改为 |
|------|------|------|------|
| `agent/offload.go` | 215 | `os.ReadFile(hostPath)` — hash 计算 | `Sandbox.ReadFile(ctx, readPath, userID)` |
| `agent/offload.go` | 350 | `os.ReadFile(resolvedPath)` — InvalidateStaleReads | `Sandbox.ReadFile(ctx, readPath, userID)` |

**注意**：offload JSON 自身的存储（行 188,237,242,311）保持 os.* 不变——offload JSON 是 server 内部状态。

#### 5.1.2 文件工具（Read/Edit/Glob/Grep）

| 文件 | 行号 | 操作 | 改为 |
|------|------|------|------|
| `tools/read.go` | 185 | `os.ReadFile` | `Sandbox.ReadFile` |
| `tools/edit.go` | 337 | `os.ReadFile` | `Sandbox.ReadFile` |
| `tools/edit.go` | 376 | `os.MkdirAll(dir, 0755)` — 创建新文件前 | `Sandbox.MkdirAll` |
| `tools/edit.go` | 382 | `os.WriteFile` — doCreate | `Sandbox.WriteFile` |
| `tools/edit.go` | 364 | `os.WriteFile` — doReplace | `Sandbox.WriteFile` |
| `tools/glob.go` | 198,217,259 | `os.Stat`/`filepath.Glob`/`filepath.WalkDir` | **删除 executeLocal，统一走 Sandbox.Exec** |
| `tools/grep.go` | 388,509,536 | `os.Open`/`os.Stat`/`filepath.WalkDir` | **删除 executeLocal，统一走 Sandbox.Exec** |
| `tools/cd.go` | 263 | `os.ReadDir(dir)` — buildDirectoryTree | `Sandbox.ReadDir` |

#### 5.1.3 Skill/Agent 发现与加载

| 文件 | 行号 | 操作 | 数据域 | 改为 |
|------|------|------|--------|------|
| `agent/skills.go` | 53 | `os.ReadDir(dir)` — 扫描 skill 目录 | 用户 skill `/workspace/skills` | `Sandbox.ReadDir`（通过缓存层） |
| `agent/skills.go` | 67 | `os.Stat(skillFile)` — 检查 SKILL.md | 用户 skill | `Sandbox.Stat`（通过缓存层） |
| `agent/skills.go` | 120 | `os.ReadFile(target)` — 读 SKILL.md | 用户 skill | `Sandbox.ReadFile`（通过缓存层） |
| `agent/skills.go` | 153 | `os.ReadFile(path)` — 全局 skill | 全局 skill（server 目录） | **不改**，os.* |
| `agent/agents.go` | 41 | `os.Stat(dir)` — 检查 agents 目录 | 用户 agent `/workspace/agents` | `Sandbox.Stat`（通过缓存层） |
| `tools/skill.go` | 94 | `os.Stat(dir)` — 检查 skill 目录 | 用户 skill | `Sandbox.Stat` |
| `tools/skill.go` | 101 | `os.ReadDir(c.hostRoot)` — resolveSkill 扫描 | 用户 skill | `Sandbox.ReadDir` |
| `tools/skill.go` | 120 | `os.ReadFile(target)` — 加载 SKILL.md | 用户 skill | `Sandbox.ReadFile` |
| `tools/skill.go` | 136 | `filepath.Walk(hostDir, ...)` — doListFiles | 用户 skill | **递归 Sandbox.ReadDir**（见 5.1.8） |
| `tools/subagent_loader.go` | 14 | `os.ReadDir(dir)` — LoadAgentRoles 扫描 | 用户 agent 目录时 | `Sandbox.ReadDir`（用户目录分支） |
| `tools/subagent_loader.go` | 37 | `os.ReadFile` — 加载 agent .md | 用户 agent | `Sandbox.ReadFile` |
| `tools/subagent_roles.go` | 58 | `os.Stat(dir)` — 检查 agent 目录 | 用户 agent | `Sandbox.Stat` |

**注意**：`tools/skill.go` 的 `resolveSkill` 同时处理全局和用户目录，需区分：全局目录用 os.ReadDir，用户目录用 Sandbox.ReadDir。`tools/subagent_loader.go` 的 `LoadAgentRoles` 同理。

#### 5.1.4 Registry install/uninstall/publish（用户 workspace 端）

**install（cache → 用户 workspace）**：

| 文件 | 行号 | 操作 | 改为 |
|------|------|------|------|
| `agent/registry.go` | 159 | `os.Stat(destDir)` — 检查已安装 | `Sandbox.Stat` |
| `agent/registry.go` | 164 | `copyDir(src, dst)` — 递归复制含 symlink | **拆分为 server 读 + Sandbox 写**（见 6.3） |
| `agent/registry.go` | 180 | `os.MkdirAll(agentsDir, 0o755)` — installAgent | `Sandbox.MkdirAll` |
| `agent/registry.go` | 196 | `os.Stat(destFile)` — 检查已安装 | `Sandbox.Stat` |
| `agent/registry.go` | 200 | `os.ReadFile(srcPath)` — 读 cache | **不改**（server 端读 cache） |
| `agent/registry.go` | 204 | `os.WriteFile(destFile, data)` — 写到用户目录 | `Sandbox.WriteFile` |
| `agent/registry.go` | 230-234 | `os.Stat` + `os.RemoveAll` — uninstallSkill | `Sandbox.Stat` + `Sandbox.RemoveAll` |
| `agent/registry.go` | 243-248 | `os.Stat` + `os.Remove` — uninstallAgent | `Sandbox.Stat` + `Sandbox.Remove` |

**publish（用户 workspace → server cache）**：

| 文件 | 行号 | 操作 | 改为 |
|------|------|------|------|
| `agent/registry.go` | 70 | `snapshotDirToCache(skillDir, cacheDir)` — skillDir 在用户本机 | **源端走 Sandbox 递归 ReadFile，目标端 os.WriteFile**（见 6.3） |
| `agent/registry.go` | 55-72 | `publishSkill` — findSkillDirForUser 找用户 skill | 用户目录分支 os.Stat → Sandbox.Stat（见下） |
| `agent/registry.go` | 88-95 | `publishAgent` — findAgentFile 找用户 agent | 用户目录分支 os.Stat → Sandbox.Stat（见下） |

**search/List（扫描用户 workspace 下的 skill/agent）**：

| 文件 | 行号 | 操作 | 改为 |
|------|------|------|------|
| `agent/registry.go` | 304-316 | `scanSkillDir(dir)` — `os.ReadDir` | `Sandbox.ReadDir`（当 dir 是用户目录时） |
| `agent/registry.go` | 325 | `scanAgentDir(dir)` — `os.ReadDir` | `Sandbox.ReadDir`（当 dir 是用户目录时） |
| `agent/registry.go` | 389-396 | `findSkillDirForUser` — `os.Stat` | `Sandbox.Stat`（用户目录分支） |
| `agent/registry.go` | 406-414 | `findAgentFile` — `os.Stat` | `Sandbox.Stat`（用户目录分支） |

#### 5.1.5 Feishu 文件操作

| 文件 | 行号 | 操作 | 改为 |
|------|------|------|------|
| `tools/download.go` | 151 | `os.MkdirAll(filepath.Dir(outputPath), 0755)` | `Sandbox.MkdirAll` |
| `tools/download.go` | 152+ | `os.Create` + `io.Copy` | `Sandbox.WriteFile` |
| `tools/feishu_mcp/download.go` | 79,82 | `os.MkdirAll` + `os.Create` + `io.Copy` | `Sandbox.MkdirAll` + `Sandbox.WriteFile` |
| `tools/feishu_mcp/file.go` | 77 | `os.Open` | `Sandbox.ReadFile` |
| `tools/feishu_mcp/file.go` | 386 | `os.Stat`（检查文件大小） | `Sandbox.Stat` |
| `tools/feishu_mcp/file.go` | 391+ | `__FEISHU_FILE__::hostPath` 协议 | **废除**，改为 Sandbox.ReadFile + Feishu API 直接上传 |

#### 5.1.6 Agent 层写操作

| 文件 | 行号 | 操作 | 改为 |
|------|------|------|------|
| `agent/bang_command.go` | 49 | `os.MkdirAll(workspaceRoot, 0o755)` — 创建用户工作目录 | `Sandbox.MkdirAll` |
| `agent/bang_command.go` | 169 | `os.WriteFile` — 写 bang 输出 | `Sandbox.WriteFile` |
| `agent/prompt_handler.go` | 63 | `os.WriteFile` — 写 prompt 文件 | `Sandbox.WriteFile` |

#### 5.1.7 用户工作目录创建

以下 5 处 `os.MkdirAll(workspaceRoot)` 在消息处理入口处创建用户工作目录。remote 模式下 workspaceRoot 指向用户本机，必须走 Sandbox：

| 文件 | 行号 | 场景 | 改为 |
|------|------|------|------|
| `agent/agent.go` | 1395 | cron 回调创建用户工作目录 | `Sandbox.MkdirAll` |
| `agent/agent.go` | 1441 | 正常消息处理创建用户工作目录 | `Sandbox.MkdirAll` |
| `agent/bang_command.go` | 49 | bang 命令创建工作目录 | `Sandbox.MkdirAll`（同 5.1.6） |
| `agent/interactive.go` | 346 | SubAgent 构建父 ToolContext | `Sandbox.MkdirAll` |
| `agent/engine_wire.go` | 534 | wire 层确保用户工作目录 | `Sandbox.MkdirAll` |

**实现方式**：Agent 持有 Sandbox 引用，在 workspaceRoot 创建处统一调用 `Sandbox.MkdirAll(ctx, workspaceRoot, 0o755, senderID)`。

#### 5.1.8 filepath.WalkDir 替代方案

`tools/skill.go:136` 的 `doListFiles` 和 `agent/registry.go:427` 的 `copyDir`/`snapshotDirToCache` 使用 `filepath.WalkDir` 递归遍历目录。Sandbox 接口没有 Walk 方法，需要替代方案：

**递归 ReadDir 工具函数**：

```go
// WalkSandboxDir 递归遍历 Sandbox 目录，等价于 filepath.WalkDir。
// fn 回调接收相对路径和 DirEntry。只回调文件（跳过目录本身）。
func WalkSandboxDir(ctx context.Context, sb Sandbox, root, userID string, fn func(relPath string, entry DirEntry) error) error {
    return walkSandboxDir(ctx, sb, root, "", userID, fn)
}

func walkSandboxDir(ctx context.Context, sb Sandbox, dir, relBase, userID string, fn func(string, DirEntry) error) error {
    entries, err := sb.ReadDir(ctx, dir, userID)
    if err != nil {
        return err // 目录不存在 → 返回错误
    }
    for _, e := range entries {
        relPath := filepath.Join(relBase, e.Name)
        if e.IsDir {
            if err := walkSandboxDir(ctx, sb, filepath.Join(dir, e.Name), relPath, userID, fn); err != nil {
                return err
            }
        } else {
            if err := fn(relPath, e); err != nil {
                return err
            }
        }
    }
    return nil
}
```

#### 5.1.9 Symlink 处理策略

当前 `copyDir`（registry.go:427）使用 `os.Lstat` + `os.Readlink` + `os.Symlink` 处理符号链接。Sandbox 接口没有 Symlink 方法。

**策略：copyDir 跨 Sandbox 复制时展开 symlink（follow symlinks）**

- **publish**（用户本机 → server cache）：使用 `WalkSandboxDir` + `Sandbox.ReadFile` 读取目标内容（自动 follow symlink）
- **install**（server cache → 用户本机）：使用 `os.WalkDir` + `os.ReadFile` 读 cache，`Sandbox.WriteFile` 写目标（递归创建目录，不创建 symlink）
- **结果**：所有 symlink 被展平为普通文件。这是可接受的——skill/agent 内部不应依赖 symlink 语义

### 5.2 保持 server 本地（不改）

| 文件 | 原因 |
|------|------|
| `agent/offload.go:188,237,242,311` | offload JSON 自身存储（server 内部状态） |
| `agent/context.go:66,71,94` | server prompt 模板（`.xbot/prompts/`） |
| `agent/skills.go:153` | 全局 skill 定义（server 目录） |
| `agent/registry.go:144,185,356,370,374` | server 缓存（`.xbot/registry/`） |
| `agent/registry.go:200` | 读 server cache（publish/install 的源端读 cache） |
| `config/*` | server 配置 |
| `storage/*` | 数据库迁移和操作 |
| `logger/*` | 日志系统 |
| `channel/*` | channel handler 本地文件 |
| `tools/manage_tools.go:265` | MCP 配置读取（server 本地） |
| `tools/session_mcp.go:358,377` | MCP 配置读取（server 本地） |
| `tools/mcp_common.go:248,499` | MCP bin 目录检查（server 本地） |
| `tools/logs.go:126` | 日志目录扫描（`.xbot/logs/`，server 内部） |

### 5.3 Remote 模式下跳过

| 文件 | 原因 |
|------|------|
| `tools/skill_sync.go` | 全局 skill 同步到 workspace → remote 不需要（server 端直接注入 system prompt） |

### 5.4 删除

| 文件 | 原因 |
|------|------|
| `tools/workspace_scope.go` 中的 `SandboxToHostPath`/`HostToSandboxPath` | 路径语义统一为 sandbox 路径，转换是 Sandbox 实现内部细节 |
| `tools/glob.go` 中的 `executeLocal` | 统一走 Sandbox.Exec |
| `tools/grep.go` 中的 `executeLocal` | 统一走 Sandbox.Exec |
| `tools/feishu_mcp/file.go` 中的 `__FEISHU_FILE__::` 协议 | 工具层直接读+上传 |

---

## 6. 关键子系统改造

### 6.1 Offload 穿越 Sandbox

**问题**：OffloadStore 当前用 `os.ReadFile` 计算 hash 和检测 stale，remote 模式下文件在用户本机。

**方案**：

```go
// OffloadStore 不持有 Sandbox — 通过参数传入（方案 X2）
type OffloadStore struct {
    config   OffloadConfig
    sessions sync.Map
}

// MaybeOffload: 新签名
func (s *OffloadStore) MaybeOffload(
    ctx context.Context,
    sessionKey, toolName, args, result string,
    resolvedReadPath string,  // 由 engine.go 预解析的绝对路径
    fs tools.Sandbox,         // Sandbox 实例
) (OffloadedResult, bool)

// InvalidateStaleReads: 新签名
func (s *OffloadStore) InvalidateStaleReads(
    ctx context.Context,
    sessionKey string,
    fs tools.Sandbox,
) []string
```

**engine.go 调用改造**：

```go
// engine.go (改造后)
resolvedReadPath := ""
if tc.Name == "Read" {
    if p := extractJSONStringField(tc.Arguments, "path"); p != "" {
        cwd := cfg.Session.GetCurrentDir()
        if cwd == "" {
            cwd = cfg.SandboxWorkDir
        }
        if filepath.IsAbs(p) {
            resolvedReadPath = p
        } else {
            resolvedReadPath = filepath.Join(cwd, p)
        }
    }
}
offloaded, wasOffloaded := cfg.OffloadStore.MaybeOffload(
    ctx, offloadSessionKey, tc.Name, tc.Arguments, offloadContent,
    resolvedReadPath, cfg.Sandbox,
)
```

**offload.go 内部改造**：

```go
// 新代码：hash 计算
if resolvedReadPath != "" && fs != nil {
    if rawData, err := fs.ReadFile(ctx, resolvedReadPath, userID); err == nil {
        entry.ContentHash = fmt.Sprintf("%x", sha256.Sum256(rawData))
    }
    // hash 失败 → 不设置 ContentHash → stale 检测不生效
    // 这是正确行为：文件不可读时无法判断是否 stale
}
```

### 6.2 Skill/Agent 发现

**问题**：remote 模式下用户 skill/agent 在用户本机 `/workspace/skills` 和 `/workspace/agents`。

**方案**：SkillStore 缓存 + TTL

```go
type SkillStore struct {
    workDir    string
    globalDirs []string
    sandbox    tools.Sandbox
    catalog    *skillCatalog
    catalogTime time.Time
    mu         sync.RWMutex
}

func (s *SkillStore) GetCatalog(ctx context.Context, senderID string) string {
    s.mu.RLock()
    if s.catalog != nil && time.Since(s.catalogTime) < 5*time.Minute {
        cat := s.catalog
        s.mu.RUnlock()
        return cat.render(senderID)
    }
    s.mu.RUnlock()
    return s.refreshCatalog(ctx, senderID)
}

func (s *SkillStore) refreshCatalog(ctx context.Context, senderID string) string {
    var entries []skillEntry

    // 1. 全局 skill → server 本地 os.ReadDir + os.ReadFile
    for _, dir := range s.globalDirs {
        entries = append(entries, s.scanGlobalSkills(dir)...)
    }

    // 2. 用户 skill → Sandbox.ReadDir + Sandbox.ReadFile
    if s.sandbox != nil {
        userSkillsDir := "/workspace/skills"
        if dirEntries, err := s.sandbox.ReadDir(ctx, userSkillsDir, senderID); err == nil {
            for _, de := range dirEntries {
                if de.IsDir {
                    entry := s.readSkillDefViaSandbox(ctx, filepath.Join(userSkillsDir, de.Name), senderID)
                    entries = append(entries, entry)
                }
            }
        }
    }

    s.mu.Lock()
    s.catalog = buildCatalog(entries)
    s.catalogTime = time.Now()
    s.mu.Unlock()

    return s.catalog.render(senderID)
}

func (s *SkillStore) InvalidateCache() {
    s.mu.Lock()
    s.catalog = nil
    s.mu.Unlock()
}
```

### 6.3 Registry install/uninstall/publish

**问题**：install 从 server cache 复制到用户 workspace，publish 从用户 workspace 复制到 server cache，uninstall 从用户 workspace 删除。涉及跨 Sandbox 文件复制。

**方案**：RegistryManager 接收 Sandbox 引用。

```go
type RegistryManager struct {
    workDir     string
    sharedStore *sqlite.SharedStore
    sandbox     tools.Sandbox  // 新增
}
```

**installSkill — server cache (os.*) → user workspace (Sandbox.*)**：

```go
func (rm *RegistryManager) installSkill(ctx context.Context, entry *sqlite.SharedEntry, senderID string) error {
    destDir := filepath.Join("/workspace/skills", entry.Name)

    // 检查目标：Sandbox
    if _, err := rm.sandbox.Stat(ctx, destDir, senderID); err == nil {
        return fmt.Errorf("skill %q already installed", entry.Name)
    }

    // 读源：server cache (os.WalkDir 递归读)
    var files []struct{ RelPath string; Data []byte }
    err := filepath.WalkDir(entry.SourcePath, func(path string, d fs.DirEntry, err error) error {
        if err != nil { return err }
        if d.IsDir() { return nil }
        rel, _ := filepath.Rel(entry.SourcePath, path)
        data, err := os.ReadFile(path) // follow symlink automatically
        if err != nil { return err }
        files = append(files, struct{ RelPath string; Data []byte }{rel, data})
        return nil
    })
    if err != nil { return fmt.Errorf("read cache: %w", err) }

    // 写目标：Sandbox（递归创建目录 + 写文件）
    for _, f := range files {
        dstPath := filepath.Join(destDir, f.RelPath)
        if err := rm.sandbox.MkdirAll(ctx, filepath.Dir(dstPath), 0o755, senderID); err != nil {
            return fmt.Errorf("mkdir: %w", err)
        }
        if err := rm.sandbox.WriteFile(ctx, dstPath, f.Data, 0o644, senderID); err != nil {
            return fmt.Errorf("write: %w", err)
        }
    }
    return nil
}
```

**publishSkill — user workspace (Sandbox.*) → server cache (os.*)**：

```go
func (rm *RegistryManager) publishSkill(ctx context.Context, name, author string) error {
    skillDir := rm.findSkillDirForUser(ctx, name, author)
    if skillDir == "" {
        return fmt.Errorf("skill %q not found", name)
    }

    cacheDir := rm.registryCacheDir("skill", name)

    // 读源：Sandbox 递归读（WalkSandboxDir）
    var files []struct{ RelPath string; Data []byte }
    err := WalkSandboxDir(ctx, rm.sandbox, skillDir, author, func(relPath string, entry DirEntry) error {
        fullPath := filepath.Join(skillDir, relPath)
        data, err := rm.sandbox.ReadFile(ctx, fullPath, author)
        if err != nil { return err }
        files = append(files, struct{ RelPath string; Data []byte }{relPath, data})
        return nil
    })
    if err != nil { return fmt.Errorf("read skill: %w", err) }

    // 写目标：server cache (os.*)
    if err := os.RemoveAll(cacheDir); err != nil && !os.IsNotExist(err) {
        return fmt.Errorf("clean cache: %w", err)
    }
    for _, f := range files {
        dstPath := filepath.Join(cacheDir, f.RelPath)
        if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
            return fmt.Errorf("mkdir cache: %w", err)
        }
        if err := os.WriteFile(dstPath, f.Data, 0o644); err != nil {
            return fmt.Errorf("write cache: %w", err)
        }
    }
    // ... publish to shared store
}
```

**findSkillDirForUser — 用户目录分支走 Sandbox**：

```go
func (rm *RegistryManager) findSkillDirForUser(ctx context.Context, name, senderID string) string {
    // 先查全局（server 本地）
    if dir := rm.findSkillDir(name); dir != "" {
        return dir
    }
    // 再查用户目录（Sandbox）
    if senderID != "" && rm.sandbox != nil {
        path := filepath.Join("/workspace/skills", name)
        if _, err := rm.sandbox.Stat(ctx, filepath.Join(path, "SKILL.md"), senderID); err == nil {
            return path
        }
    }
    return ""
}
```

### 6.4 Feishu 文件操作

**方案**：废除 `__FEISHU_FILE__::` 协议，工具层直接读+上传。

```go
// DownloadFile (Feishu → 用户本机)
func downloadAndSave(ctx context.Context, sandbox tools.Sandbox, userID, filePath string, feishuData []byte) error {
    if err := sandbox.MkdirAll(ctx, filepath.Dir(filePath), 0o755, userID); err != nil {
        return err
    }
    return sandbox.WriteFile(ctx, filePath, feishuData, 0o644, userID)
}

// UploadFile/SendFile (用户本机 → Feishu)
func uploadFile(ctx context.Context, sandbox tools.Sandbox, userID, filePath string) (string, error) {
    data, err := sandbox.ReadFile(ctx, filePath, userID)
    if err != nil { return "", err }
    return feishuClient.UploadFile(data, filepath.Base(filePath))
}
```

**双跳不可避免**：server ↔ Feishu 是第一跳（需要 token），server ↔ Runner 是第二跳（Sandbox API）。这是 remote 模式的固有代价。

### 6.5 RunConfig/ToolContext 改造

```go
type RunConfig struct {
    // ... 现有字段 ...
    Sandbox    tools.Sandbox  // Sandbox 实例引用
    SandboxMode string        // "none", "docker", "remote"（配置时决策）
}

type ToolContext struct {
    // ... 现有字段 ...
    Sandbox tools.Sandbox  // V4 新增
    // SandboxMode 不加 — 用 Sandbox.Name() 代替
}
```

### 6.6 SubAgent CWD 处理

```go
// 旧代码：所有模式都做 workspaceRoot → sandboxWorkDir 转换
if cwd != "" && cfg.SandboxEnabled && cfg.WorkspaceRoot != "" && cfg.SandboxWorkDir != "" {
    if strings.HasPrefix(cwd, cfg.WorkspaceRoot) {
        cwd = cfg.SandboxWorkDir + cwd[len(cfg.WorkspaceRoot):]
    }
}

// 新代码：remote 模式下跳过转换
// 原因：remote 模式下 CWD 由 Cd 工具设置，已经是 sandbox 格式的绝对路径（/workspace/...）。
// Cd 工具在所有模式下统一使用 Sandbox.Stat 验证路径，返回的路径已经是 sandbox 路径。
// docker 模式仍需转换：docker 模式下 Cd 工具返回的是 host 路径（因为 path_guard 基于 WorkspaceRoot）。
if cwd != "" && cfg.SandboxMode != "remote" && cfg.WorkspaceRoot != "" && cfg.SandboxWorkDir != "" {
    if strings.HasPrefix(cwd, cfg.WorkspaceRoot) {
        cwd = cfg.SandboxWorkDir + cwd[len(cfg.WorkspaceRoot):]
    }
}
```

### 6.7 Glob/Grep 统一

**删除 `executeLocal`，所有模式走 `Sandbox.Exec`**：

```go
// 旧代码
func (t *GlobTool) Execute(ctx *ToolContext, args ...) {
    if ctx.SandboxEnabled { return t.executeInSandbox(ctx, args) }
    return t.executeLocal(ctx, args)
}

// 新代码
func (t *GlobTool) Execute(ctx *ToolContext, args ...) {
    return t.executeViaSandbox(ctx, args)
}
```

### 6.8 EditTool 统一

```go
// 旧代码：base64 hack
func sandboxWriteFile(ctx *ToolContext, path, content string) {
    encoded := base64.StdEncoding.EncodeToString([]byte(content))
    executeInSandboxRaw(ctx, fmt.Sprintf("mkdir -p %s", dir))
    executeInSandboxRaw(ctx, fmt.Sprintf("echo '%s' | base64 -d > %s", encoded, path))
}

// 新代码
func sandboxWriteFile(ctx context.Context, sandbox tools.Sandbox, userID, path string, data []byte, perm os.FileMode) error {
    dir := filepath.Dir(path)
    if err := sandbox.MkdirAll(ctx, dir, 0o755, userID); err != nil {
        return err
    }
    return sandbox.WriteFile(ctx, path, data, perm, userID)
}
```

---

## 7. 实施计划

### Phase 1：Sandbox 接口 + None 实现 + workspaceRoot 创建改造

1. 定义 `Sandbox` 接口和所有类型
2. 实现 `NoneSandbox`（所有方法直接调 os.*）
3. `RunConfig`/`ToolContext` 加 `Sandbox` + `SandboxMode`
4. `main.go` 初始化 Sandbox 并注入 Agent
5. **改造 5 处 workspaceRoot 创建**（5.1.7）→ `Sandbox.MkdirAll`

**目标**：不破坏现有功能，建立新抽象层。workspaceRoot 创建统一走 Sandbox。

### Phase 2：EditTool/ReadTool 统一走 Sandbox

1. `tools/edit.go`：删除 base64 hack，改用 Sandbox.ReadFile/WriteFile
2. `tools/read.go`：os.ReadFile → Sandbox.ReadFile
3. ToolContext 传递 Sandbox 引用

### Phase 3：Glob/Grep 统一

1. 删除 `executeLocal` 函数
2. 所有模式走 `Sandbox.Exec`（`find`/`grep` 命令）
3. `tools/cd.go:263` buildDirectoryTree → Sandbox.ReadDir

### Phase 4：Offload 穿越 Sandbox

1. 修改 MaybeOffload/InvalidateStaleReads 签名
2. engine.go 解析 Read 路径并传入绝对路径
3. offload hash 计算走 Sandbox.ReadFile

### Phase 5：Skill/Agent 发现改造

1. SkillStore 加缓存 + TTL
2. AgentStore 同理
3. registry install/uninstall/publish 穿越 Sandbox（含 findSkillDirForUser/findAgentFile）
4. scanSkillDir/scanAgentDir 穿越 Sandbox
5. skill_sync.go 加 remote 模式跳过守卫
6. tools/skill.go resolveSkill/doListFiles 穿越 Sandbox
7. subagent_loader.go LoadAgentRoles 穿越 Sandbox

### Phase 6：Feishu/Download/Agent 写操作改造

1. 废除 `__FEISHU_FILE__::` 协议
2. download.go/feishu_mcp/download.go → Sandbox.MkdirAll + Sandbox.WriteFile
3. feishu_mcp/file.go → Sandbox.ReadFile + Feishu API
4. bang_command.go/prompt_handler.go → Sandbox.WriteFile
5. 删除 SandboxToHostPath/HostToSandboxPath

### Phase 7：DockerSandbox 实现

1. 基于 docker exec 实现所有 Sandbox 方法
2. 文件操作：`docker exec cat`/`docker exec -i tee`
3. 目录操作：`docker exec mkdir`/`docker exec rm`

### Phase 8：RemoteSandbox + Runner 实现

1. WebSocket server + client
2. Runner CLI（main.go + handler.go）
3. 双通道文件传输
4. Path guard
5. Runner HTTP Server

### Phase 9：清理

1. 删除旧 Wrap 方法
2. 删除 executeLocal 函数
3. 删除 `__FEISHU_FILE__::` 协议
4. `.xbot` 路径引用加注释标记
5. 测试覆盖

**Phase 依赖关系**：
```
Phase 1 → Phase 2/3 (可并行) → Phase 4 (依赖 2) → Phase 5/6 (可并行) → Phase 7/8 (可并行) → Phase 9
```

---

## 8. Brainstorm 共识清单（19 条）

| # | 条目 | 决策 |
|---|------|------|
| 1 | Sandbox 接口方法 | Exec + ReadFile + WriteFile + Stat + ReadDir + MkdirAll + Remove + RemoveAll |
| 2 | ReadFile/WriteFile 路径要求 | 绝对路径，内部处理 sandbox→host 转换 |
| 3 | Offload JSON 存储 | 存 server（os.*），hash 走 Sandbox.ReadFile |
| 4 | OffloadStore 依赖注入 | 接收 Sandbox 参数（方案 X2） |
| 5 | readPath 解析 | engine.go 用 cfg.Session.GetCurrentDir() 解析，传绝对路径 |
| 6 | Skill 发现机制 | Server 缓存 + TTL 5min + 主动失效 |
| 7 | skill_sync.go | remote 模式跳过 |
| 8 | MCP 配置 | 不动，server 本地 |
| 9 | 路径转换函数 | 删除 SandboxToHostPath/HostToSandboxPath |
| 10 | EditTool | 统一走 Sandbox.ReadFile/WriteFile |
| 11 | bang_command/prompt_handler | 传 Sandbox 引用，走 Sandbox.WriteFile |
| 12 | Feishu 文件操作 | 废除 `__FEISHU_FILE__::` 协议 |
| 13 | ReadOnlyRoots | remote 模式不支持 |
| 14 | 大文件传输 | 一次性 []byte，≤4MB WebSocket，>4MB HTTP |
| 15 | SandboxFileInfo | 替代 os.FileInfo，不含 Sys() |
| 16 | RunConfig | 加 Sandbox + SandboxMode |
| 17 | ToolContext | 加 Sandbox（不加 SandboxMode，用 Sandbox.Name()） |
| 18 | Glob/Grep | 删除 executeLocal，统一走 Sandbox.Exec |
| 19 | MkdirAll | Sandbox 接口增加 MkdirAll 方法 |
