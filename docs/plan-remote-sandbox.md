# Remote Sandbox 重构方案

> **版本**: V2 (门下省审核通过)
> **分支**: `refactor/sandbox-tool-provider` (base: master `5661ab7`)
> **审核**: brainstorm 6 轮讨论 + 门下省 2 轮审核

## 1. 目标

彻底重构 Sandbox 接口，消除工具双路径（local/sandbox）的历史债务，新增 remote 模式（用户本地 runner + WebSocket 通信）。

### 核心原则

- **统一接口**：none/docker/remote 三种模式实现同一 Sandbox 接口
- **不惧重写**：历史债务直接干掉，不做渐进兼容
- **零新依赖**：复用已有 `gorilla/websocket v1.5.0`
- **Exec 替代 Wrap**：所有命令执行通过 `Sandbox.Exec()`，不返回 `exec.Cmd`

## 2. 架构设计

### 2.1 核心类型

```go
// tools/exec.go

type ExecSpec struct {
    Command   string        // 程序路径（Shell=true 时为 shell 脚本）
    Args      []string      // 参数（Shell=true 时忽略）
    Shell     bool          // true = 用 login shell 执行 Command
    Dir       string        // 工作目录
    Env       []string      // 额外环境变量（KEY=VALUE）
    Stdin     string        // 标准输入
    Timeout   time.Duration // 超时（0=默认 120s）
    Workspace string        // 宿主机工作区路径
    UserID    string        // 用户标识
}

type ExecResult struct {
    Stdout   string
    Stderr   string
    ExitCode int
    TimedOut bool
}
```

### 2.2 Sandbox 接口

```go
// tools/sandbox.go

type Sandbox interface {
    // === 执行能力 ===
    Exec(ctx context.Context, spec ExecSpec) (*ExecResult, error)
    ReadFile(ctx context.Context, path string, userID string) ([]byte, error)
    WriteFile(ctx context.Context, path string, data []byte, perm os.FileMode, userID string) error

    // === Shell ===
    GetShell(userID string, workspace string) (string, error)

    // === 管理 ===
    Name() string
    Close() error
    CloseForUser(userID string) error
    IsExporting(userID string) bool
    ExportAndImport(userID string) error
}
```

### 2.3 与旧接口的关键区别

| 旧接口 | 新接口 | 变化原因 |
|--------|--------|----------|
| `Wrap() → (cmdName, cmdArgs, err)` | `Exec(spec) → (result, err)` | Wrap 返回 cmdName+args 让调用方构建 exec.Cmd，remote 模式无法在本地构建 exec.Cmd |
| `RunInSandbox*()` 全局函数 | `Sandbox.Exec()` | 消除 4 处重复的进程管理代码 |
| EditTool base64 hack | `Sandbox.ReadFile/WriteFile` | 消除 `echo '<base64>' \| base64 -d > file` 的 hack |
| `SandboxEnabled` bool | `SandboxMode` string | 支持三种模式的无歧义区分 |

### 2.4 MCP Transport 独立工厂

MCP SDK 核心接口不依赖 `exec.Cmd`：
```go
type Transport interface {
    Connect(ctx context.Context) (Connection, error)
}
```

MCP 连接不经过 Sandbox 接口，走独立工厂：
```go
func NewMCPTransport(ctx context.Context, cfg MCPServerConfig, sandboxMode, userID, workspace string) (mcp.Transport, error) {
    switch sandboxMode {
    case "none":   return buildLocalTransport(...)
    case "docker": return buildDockerTransport(...)
    case "remote": return buildRemoteTransport(...)  // WebSocketTransport
    }
}
```

Remote 模式下，runner 侧启动 MCP 进程，通过 WebSocket 双向转发 JSON-RPC。

### 2.5 设计决策记录

| 决策点 | 选择 | 否决方案 |
|--------|------|----------|
| Exec vs Wrap | Exec | Wrap 返回 exec.Cmd，remote 无法实现 |
| Spawn + Process 接口 | 不搞 | MCP SDK 已有 Transport 抽象，Spawn 是重复造轮子 |
| Glob/Grep 双路径 | 保持策略分发 | local 用 Go 实现，sandbox 用 shell，不强制统一为 Exec |
| path_guard remote 模式 | 方案 B 纯字符串校验 | runner 由用户自己运行，信任边界不同于 docker |
| SandboxEnabled bool | 改为 SandboxMode string | 消除 PreferredSandbox 冗余，支持三种模式 |

## 3. Sandbox 实现

### 3.1 NoneSandbox

```go
func (s *NoneSandbox) Exec(ctx context.Context, spec ExecSpec) (*ExecResult, error) {
    // 构建命令
    args := spec.Args
    if spec.Shell {
        shell, _ := s.GetShell(spec.UserID, spec.Workspace)
        args = []string{shell, "-l", "-c", spec.Command}
    }
    if len(args) == 0 {
        return nil, fmt.Errorf("no command specified")
    }

    execCtx, cancel := context.WithTimeout(ctx, effectiveTimeout(spec.Timeout))
    defer cancel()

    cmd := exec.CommandContext(execCtx, args[0], args[1:]...)
    cmd.Dir = spec.Dir
    cmd.Env = spec.Env  // 可选：合并 os.Environ()
    if spec.Stdin != "" {
        cmd.Stdin = strings.NewReader(spec.Stdin)
    }

    // 进程组管理（从 shell_unix.go 迁入）
    setProcessAttrs(cmd)
    cmd.Cancel = func() error { killProcess(cmd); return nil }
    cmd.WaitDelay = 5 * time.Second

    var stdout, stderr bytes.Buffer
    cmd.Stdout = &stdout
    cmd.Stderr = &stderr
    err := cmd.Run()

    return &ExecResult{
        Stdout:   stdout.String(),
        Stderr:   stderr.String(),
        ExitCode: exitCode(cmd),
        TimedOut: execCtx.Err() == context.DeadlineExceeded,
    }, err
}

func (s *NoneSandbox) ReadFile(_ context.Context, path, _ string) ([]byte, error) {
    return os.ReadFile(path)
}

func (s *NoneSandbox) WriteFile(_ context.Context, path string, data []byte, perm os.FileMode, _ string) error {
    os.MkdirAll(filepath.Dir(path), 0o755)
    return os.WriteFile(path, data, perm)
}
```

### 3.2 DockerSandbox

```go
func (s *dockerSandbox) Exec(ctx context.Context, spec ExecSpec) (*ExecResult, error) {
    container, err := s.getOrCreateContainer(spec.UserID, spec.Workspace)
    if err != nil {
        return nil, err
    }

    // 构建 docker exec 参数
    dockerArgs := []string{"exec", "-i"}
    if spec.Dir != "" {
        dockerArgs = append(dockerArgs, "-w", spec.Dir)
    }
    for _, e := range spec.Env {
        dockerArgs = append(dockerArgs, "-e", e)
    }
    dockerArgs = append(dockerArgs, container.name)

    if spec.Shell {
        shell, _ := s.GetShell(spec.UserID, spec.Workspace)
        dockerArgs = append(dockerArgs, shell, "-l", "-c", spec.Command)
    } else {
        dockerArgs = append(dockerArgs, spec.Args...)
    }

    // 通过宿主机 exec.CommandContext 执行 docker 命令
    execCtx, cancel := context.WithTimeout(ctx, effectiveTimeout(spec.Timeout))
    defer cancel()

    cmd := exec.CommandContext(execCtx, "docker", dockerArgs...)
    if spec.Stdin != "" {
        cmd.Stdin = strings.NewReader(spec.Stdin)
    }
    setProcessAttrs(cmd)
    cmd.Cancel = func() error { killProcess(cmd); return nil }
    cmd.WaitDelay = 5 * time.Second

    var stdout, stderr bytes.Buffer
    cmd.Stdout = &stdout
    cmd.Stderr = &stderr
    err = cmd.Run()

    return &ExecResult{...}, err
}

func (s *dockerSandbox) ReadFile(_ context.Context, path, userID string) ([]byte, error) {
    container, _, err := s.getOrCreateContainer(userID, "")
    if err != nil {
        return nil, err
    }
    // docker exec cat 读取文件
    out, err := exec.Command("docker", "exec", container.name, "cat", path).Output()
    return out, err
}

func (s *dockerSandbox) WriteFile(_ context.Context, path string, data []byte, perm os.FileMode, userID string) error {
    container, _, err := s.getOrCreateContainer(userID, "")
    if err != nil {
        return err
    }
    // docker exec -i cat > file（替代 base64 hack）
    cmd := exec.Command("docker", "exec", "-i", container.name,
        "sh", "-c", fmt.Sprintf("cat > '%s' && chmod %o '%s'", shellEscape(path), perm))
    cmd.Stdin = bytes.NewReader(data)
    out, err := cmd.CombinedOutput()
    if err != nil {
        return fmt.Errorf("docker write: %w, output: %s", err, string(out))
    }
    return nil
}
```

**DockerSandbox.WriteFile 关键改进**：当前用 `echo '<base64>' | base64 -d > file`（edit.go:187-196），新方案用 `docker exec -i cat > file` + stdin 管道直接写入 bytes。消除 base64 编解码开销和 shell 转义问题。

### 3.3 RemoteSandbox

```go
type RemoteSandbox struct {
    tokens     []string
    listenAddr string
    mu         sync.Mutex
    runners    map[string]*runnerConn  // userID → active runner
}

type runnerConn struct {
    conn      *websocket.Conn
    mu        sync.Mutex
    pending   map[string]chan *ExecResult  // requestID → response channel
    processes map[string]*spawnedProcess   // requestID → MCP process
}

func (s *RemoteSandbox) Exec(ctx context.Context, spec ExecSpec) (*ExecResult, error) {
    rc, err := s.getRunner(spec.UserID)
    if err != nil {
        return nil, err
    }

    reqID := uuid.New().String()
    msg := protocol.Message{
        ID:     reqID,
        Type:   "exec",
        Params: json.RawMessage(marshalExecParams(spec)),
    }

    ch := make(chan *ExecResult, 1)
    rc.registerPending(reqID, ch)
    defer rc.unregisterPending(reqID)

    if err := rc.conn.WriteJSON(msg); err != nil {
        return nil, fmt.Errorf("websocket write: %w", err)
    }

    select {
    case result := <-ch:
        return result, nil
    case <-ctx.Done():
        return nil, ctx.Err()
    }
}

func (s *RemoteSandbox) ReadFile(ctx context.Context, path, userID string) ([]byte, error) {
    // 通过 WebSocket 发送 read_file 请求
    result, err := s.sendRequest(ctx, userID, "read_file", map[string]string{"path": path})
    if err != nil {
        return nil, err
    }
    return base64.StdEncoding.DecodeString(result.ContentBase64)
}

func (s *RemoteSandbox) WriteFile(ctx context.Context, path string, data []byte, perm os.FileMode, userID string) error {
    _, err := s.sendRequest(ctx, userID, "write_file", map[string]interface{}{
        "path":          path,
        "content_base64": base64.StdEncoding.EncodeToString(data),
        "mode":          int(perm),
    })
    return err
}
```

## 4. WebSocket 协议

### 4.1 认证

```json
→ {"type":"auth","token":"xxx","workspace":"/home/user/project","runner_id":"runner-1"}
← {"type":"auth_ok","user_id":"user-123"}
```

### 4.2 exec

```json
→ {"id":"req-1","type":"exec","params":{"args":["/bin/sh","-l","-c","ls -la"],"dir":"/workspace","env":[],"stdin":"","timeout":120}}
← {"id":"req-1","type":"result","data":{"stdout":"...","stderr":"","exit_code":0,"timed_out":false}}
```

### 4.3 read_file / write_file

```json
→ {"id":"req-2","type":"read_file","params":{"path":"/workspace/file.go"}}
← {"id":"req-2","type":"result","data":{"content_base64":"...","error":null}}

→ {"id":"req-3","type":"write_file","params":{"path":"/workspace/file.go","content_base64":"...","mode":420}}
← {"id":"req-3","type":"result","data":{"error":null}}
```

文件内容 base64 编码（ReadFile 返回 `[]byte`，二进制文件也走这个路径）。

### 4.4 spawn（MCP Transport 内部使用）

```json
→ {"id":"mcp-1","type":"spawn","params":{"command":"npx","args":["-y","gopls-mcp"],"dir":"/workspace","env":[]}}
← {"id":"mcp-1","type":"data","data":"<JSON-RPC text>","stream":"stdout"}
← {"id":"mcp-1","type":"exit","data":0}
→ {"id":"mcp-1","type":"kill"}
```

数据直接传输字符串（MCP 协议是文本 JSON-RPC，不需要 base64）。

### 4.5 心跳

```json
→ {"type":"ping"}
← {"type":"pong"}
```

间隔 30s。超过 3 次未响应则断开重连。

## 5. 工具改造

### 5.1 ShellTool

| 改动点 | 当前 | 改造后 |
|--------|------|--------|
| 命令执行 | `sandbox.Wrap()` + `exec.CommandContext()` + setProcessAttrs/killProcess/WaitDelay | `sandbox.Exec(ExecSpec{Shell:true, Command:cmd, Dir:...})` |
| persistEnvFromCommand | 3× `RunInSandboxWithShell` | 3× `sandbox.Exec(ExecSpec{Shell:true, Command:...})` |
| Cd 注入 | `cd <dir> && <cmd>` 前缀拼接 | `spec.Dir = toolCtx.CurrentDir`（DockerSandbox.Exec 用 `-w` 参数） |

**消除代码**：`exec.CommandContext` 调用（~40行）、setProcessAttrs/killProcess 直接调用。

### 5.2 ReadTool

| 改动点 | 当前 | 改造后 |
|--------|------|--------|
| sandbox 读取 | `RunInSandboxWithShell("cat -n " + path)` | `sandbox.ReadFile(ctx, path, userID)`，服务端添加行号 |

**消除代码**：`executeInSandbox` 方法（~30行）。

### 5.3 EditTool

| 改动点 | 当前 | 改造后 |
|--------|------|--------|
| sandbox 读文件 | `sandboxReadFile` = `RunInSandboxRaw("cat " + path)` | `sandbox.ReadFile()` |
| sandbox 写文件 | `sandboxWriteFile` = base64 编码 + `echo '<base64>' \| base64 -d > file` | `sandbox.WriteFile()` |
| sandbox 新建文件 | `sandboxWriteNewFile` = base64 编码 + mkdir + 写入 | `sandbox.WriteFile()`（WriteFile 内部 MkdirAll） |

**消除代码**：`sandboxReadFile`/`sandboxWriteFile`/`sandboxWriteNewFile`（~32行 base64 hack）。

### 5.4 GlobTool

| 改动点 | 当前 | 改造后 |
|--------|------|--------|
| sandbox 执行 | `RunInSandboxWithShell("find ...")` | `sandbox.Exec(ExecSpec{Args:["find", ...]})` |
| local 执行 | 纯 Go 实现（`filepath.WalkDir` + glob 匹配） | **保留不变** |

### 5.5 GrepTool

| 改动点 | 当前 | 改造后 |
|--------|------|--------|
| sandbox 执行 | `RunInSandboxRaw("grep ...")` | `sandbox.Exec(ExecSpec{Args:["grep", ...]})` |
| local 执行 | 纯 Go 实现（`searchFile` + `convertGoRE2ToERE`） | **保留不变** |

### 5.6 CdTool

| 改动点 | 当前 | 改造后 |
|--------|------|--------|
| sandbox 目录检测 | `executeInSandbox` = `RunInSandboxWithShell("ls ...")` | `sandbox.Exec(ExecSpec{Args:[shell, "-c", "ls ..."]})` |
| buildDirectoryTree | `RunInSandboxWithShell("ls ...")` | `sandbox.Exec(...)` |
| detectProjectContext | `RunInSandboxWithShell(...)` | `sandbox.Exec(...)` |

### 5.7 bang_command.go

| 改动点 | 当前 | 改造后 |
|--------|------|--------|
| 命令执行 | `sandbox.GetShell()` + `sandbox.Wrap()` + `exec.CommandContext()` + setProcessAttrs/killProcess/WaitDelay | `sandbox.Exec(ExecSpec{Shell:true, Command:cmd, Dir:workspaceRoot, Timeout:120s, Workspace:workspaceRoot, UserID:senderID})` |

**消除代码**：整个 `executeBangCommand` 方法从 50 行缩减到 ~10 行。

## 6. 配置变更

```go
// config/config.go

type SandboxConfig struct {
    Mode           string        // "none", "docker", "remote"
    DockerImage    string        // Docker 模式镜像
    HostWorkDir    string        // DinD 宿主机路径
    IdleTimeout    time.Duration // 空闲超时

    // === 新增：Remote 模式 ===
    RemoteTokens   []string // 允许的 runner token
    RemoteListen   string   // WebSocket 监听地址（如 ":8090"）
}
```

环境变量：
```bash
SANDBOX_MODE=remote
SANDBOX_REMOTE_TOKENS=token1,token2
SANDBOX_REMOTE_LISTEN=:8090
```

## 7. 文件变更清单

### 新增

| 文件 | 行数 | 说明 |
|------|------|------|
| `tools/exec.go` | ~60 | ExecSpec, ExecResult |
| `tools/sandbox.go` | ~40 | 新 Sandbox 接口 |
| `tools/remote_sandbox.go` | ~350 | RemoteSandbox + WebSocket server |
| `tools/remote_sandbox_test.go` | ~250 | 单元测试 |
| `tools/mcp_transport.go` | ~150 | MCP Transport 工厂 + WebSocketTransport |
| `runner/main.go` | ~100 | Runner CLI |
| `runner/config.go` | ~50 | Runner 配置 |
| `runner/client.go` | ~200 | WebSocket 客户端 |
| `runner/handler.go` | ~250 | 请求处理器 |
| `runner/protocol.go` | ~100 | 协议消息类型 |

### 重写

| 文件 | 说明 |
|------|------|
| `tools/sandbox_runner.go` | NoneSandbox/DockerSandbox 实现新接口；删除 Wrap 方法 |

### 大改

| 文件 | 说明 |
|------|------|
| `tools/shell.go` | exec.CommandContext → Sandbox.Exec；persistEnvFromCommand；Cd 注入 → spec.Dir |
| `tools/edit.go` | 删除 base64 hack → Sandbox.ReadFile/WriteFile |

### 中改

| 文件 | 说明 |
|------|------|
| `tools/read.go` | sandbox 分支 → Sandbox.ReadFile |
| `tools/glob.go` | sandbox shell → Sandbox.Exec（保留 local Go 实现） |
| `tools/grep.go` | sandbox shell → Sandbox.Exec（保留 local Go 实现） |
| `tools/cd.go` | executeInSandbox → Sandbox.Exec |
| `tools/mcp_common.go` | sandbox.Wrap → MCP Transport 工厂 |
| `tools/path_guard.go` | 新增 remote 路径校验 |
| `tools/interface.go` | ToolContext: SandboxEnabled bool → SandboxMode string |
| `tools/skill.go` | sandboxBaseDir 适配 remote |
| `tools/download.go` | SandboxToHostPath 适配 remote |
| `agent/bang_command.go` | Wrap → Sandbox.Exec |
| `agent/agent.go` | SandboxEnabled → SandboxMode |
| `config/config.go` | SandboxConfig 增加 Remote 字段 |
| `main.go` | SandboxMode 替换；配置适配 |

### 删除

| 文件 | 说明 |
|------|------|
| `tools/sandbox_exec.go` (163行) | RunInSandbox 系列全部被 Sandbox.Exec 替代 |

### 测试修改

| 文件 | 说明 |
|------|------|
| `tools/sandbox_unit_test.go` (429行) | 适配 SandboxMode |
| 其他测试文件 | SandboxEnabled → SandboxMode |

### 不动

| 文件 | 原因 |
|------|------|
| `tools/shell_unix.go` / `shell_windows.go` | setProcessAttrs/killProcess 移入 NoneSandbox.Exec 内部 |
| `tools/shell_env.go` | parseEnvFileLines/parseExportStatements 保留 |

## 8. 实施阶段

### Phase 1：接口定义 + NoneSandbox

1. 新建 `tools/exec.go`（ExecSpec, ExecResult）
2. 新建 `tools/sandbox.go`（Sandbox 接口）
3. `tools/sandbox_runner.go`：NoneSandbox 实现新接口（Exec/ReadFile/WriteFile）
4. 保留旧接口过渡（Sandbox 旧方法共存）

**验收**：`go build` 通过，现有测试通过

### Phase 2：工具改造（全部 6 个）

1. ShellTool → Sandbox.Exec（含 persistEnvFromCommand 3 处）
2. ReadTool → Sandbox.ReadFile
3. EditTool → Sandbox.ReadFile/WriteFile（删除 base64 hack）
4. GlobTool → Sandbox.Exec（sandbox 部分）
5. GrepTool → Sandbox.Exec（sandbox 部分）
6. CdTool → Sandbox.Exec

**验收**：none 模式下所有工具测试通过

### Phase 3：DockerSandbox + 删除旧代码

1. DockerSandbox 实现新接口
2. 删除旧 Wrap 方法和 `tools/sandbox_exec.go`
3. 删除 SandboxEnabled → SandboxMode 过渡

**验收**：docker 模式下所有测试通过

### Phase 4：bang_command + MCP Transport 工厂

1. bang_command.go → Sandbox.Exec
2. mcp_common.go → NewMCPTransport 工厂
3. main.go / agent.go → SandboxMode

**验收**：`go test ./...` 全绿

### Phase 5：RemoteSandbox 服务端

1. `tools/remote_sandbox.go`（WebSocket server）
2. `tools/mcp_transport.go`（WebSocketTransport）
3. `config/config.go` Remote 配置
4. 单元测试

**验收**：`SANDBOX_MODE=remote` 启动成功

### Phase 6：Runner CLI

1. `runner/` 完整实现
2. 端到端测试

**验收**：runner 连接 + agent 执行命令

### Phase 7：收尾

1. path_guard.go remote 适配
2. 文档更新
3. 安全加固（非 TLS 告警、token 审计日志）

## 9. 风险与缓解

| 风险 | 严重度 | 缓解 |
|------|--------|------|
| SandboxMode 替换 176 处引用 | 中 | 分步：先加字段并存，逐步替换，最后删旧字段 |
| DockerSandbox WriteFile stdin 管道 | 低 | V1 够用，大文件后续优化为 docker cp |
| WebSocket 长连接不稳定 | 中 | 心跳 30s + 自动重连 + 指数退避 |
| Runner 断连时命令执行中 | 低 | Exec 返回 runner 不可用错误 |
| path_guard remote 安全性 | 低 | 方案 B 纯字符串校验，信任 runner |

## 10. 设计评审记录

### Brainstorm（6 轮）

1. **核心架构选型**：Exec Provider vs Tool Provider → Exec Provider
2. **Wrap vs Exec**：Wrap 返回 exec.Cmd 不可远程化 → Exec 统一
3. **Spawn + Process**：试图替代 exec.Cmd → MCP SDK 已有 Transport 抽象，否决
4. **Glob/Grep 双路径**：不强制统一，保留策略分发
5. **path_guard remote 模式**：方案 B 纯字符串校验
6. **最终确认**：Exec-only + MCP Transport 工厂

### 门下省审核（2 轮）

**V1 驳回**：遗漏 Wrap 语义、管理方法、bang_command/mcp_common/agent.go/main.go、ShellTool 特殊逻辑

**V2 通过**：所有问题已修正
