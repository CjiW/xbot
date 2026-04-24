# xbot Hooks 系统设计

> 对标 Claude Code Hooks，重新设计 xbot 的完整生命周期钩子系统。
> **不保留旧接口**，用统一的新系统替换现有 `ToolHook`/`HookChain`。

## 1. 现状与差距

### 1.1 现有系统

| 组件 | 位置 | 问题 |
|------|------|------|
| `ToolHook` / `HookChain` | `tools/hook.go` | 仅 Pre/Post Tool，纯 Go 插件，用户无法配置 |
| `LoggingHook` / `TimingHook` | `tools/hook_builtin.go` | 硬编码行为 |
| `ApprovalHook` | `tools/approval.go` | 与新系统权限决策冲突 |
| `EventTrigger` | `event/` + `tools/event_trigger.go` | 入站 webhook，与新系统互补 |

### 1.2 与 Claude Code Hooks 差距

| 维度 | Claude Code | xbot 现状 |
|------|-------------|-----------|
| 事件覆盖 | 30+ 生命周期事件 | 仅 PreToolUse / PostToolUse |
| Hook 类型 | command / http / mcp_tool / prompt / agent | 仅 Go 编译时插件 |
| 配置方式 | JSON 文件（多层级） | Go 代码硬编码 |
| 匹配器 | 正则 + if 条件 | 无 |
| 决策控制 | allow / deny / ask / defer + 修改输入 | 仅 block |
| 作用域 | user / project / plugin / policy | 全局单例 |

---

## 2. 架构

```
                    ┌─────────────────────┐
                    │    Hook Manager      │
                    │ (配置加载·事件分发    │
                    │  匹配·决策聚合)      │
                    └─────────┬───────────┘
                              │
              ┌───────────────┼───────────────┐
              │               │               │
     ┌────────▼──────┐ ┌─────▼──────┐ ┌──────▼───────┐
     │ Matcher Engine │ │  Executors │ │ Config Layers│
     │                │ │            │ │              │
     │ tool name      │ │ command    │ │ user         │
     │ event glob     │ │ http       │ │ project      │
     │ regex          │ │ callback   │ │ local        │
     │ if-condition   │ │ mcp_tool   │ │              │
     └────────────────┘ └────────────┘ └──────────────┘
```

## 3. 生命周期事件

### 3.1 事件总表

| 事件 | 触发时机 | 可阻塞 | matcher 过滤 |
|------|----------|--------|-------------|
| **SessionStart** | 会话启动/恢复 | ❌ | `startup`/`resume`/`clear`/`compact` |
| **SessionEnd** | 会话结束 | ❌ | `clear`/`logout`/`other` |
| **UserPromptSubmit** | 用户消息提交后、LLM 处理前 | ✅ | 不支持，总是触发 |
| **PreToolUse** | 工具执行前 | ✅ | 工具名（`Shell`/`FileCreate`/正则） |
| **PostToolUse** | 工具执行成功后 | ❌ | 工具名 |
| **PostToolUseFailure** | 工具执行失败后 | ❌ | 工具名 |
| **PostToolBatch** | 一轮工具调用全部完成后 | ✅ | 不支持 |
| **PermissionRequest** | 权限审批时 | ✅ | 工具名 |
| **PermissionDenied** | 权限被拒时 | ❌ | 工具名 |
| **SubAgentStart** | SubAgent 创建时 | ❌ | agent 类型名 |
| **SubAgentStop** | SubAgent 完成时 | ✅ | agent 类型名 |
| **AgentStop** | Agent 完成响应 | ✅ | 不支持 |
| **AgentError** | API 调用失败 | ❌ | `rate_limit`/`server_error`/... |
| **PreCompact** | 上下文压缩前 | ✅ | `manual`/`auto` |
| **PostCompact** | 压缩完成后 | ❌ | `manual`/`auto` |
| **CronFired** | 定时任务触发 | ❌ | cron job ID |
| **WebhookReceived** | 外部 webhook 到达 | ❌ | trigger ID |

### 3.2 事件载荷

所有事件共享基础字段：

```json
{
  "session_id": "sess_abc123",
  "channel": "cli",
  "sender_id": "cli_user",
  "chat_id": "cli_user",
  "cwd": "/home/user/project",
  "hook_event_name": "PreToolUse",
  "timestamp": "2026-04-24T18:00:00Z"
}
```

**PreToolUse：**
```json
{
  "...基础字段...": "",
  "tool_name": "Shell",
  "tool_input": { "command": "rm -rf /tmp/build" },
  "tool_use_id": "tool_001"
}
```

**PostToolUse：**
```json
{
  "...基础字段...": "",
  "tool_name": "FileReplace",
  "tool_input": { "path": "main.go", "old_string": "...", "new_string": "..." },
  "tool_use_id": "tool_002",
  "tool_elapsed_ms": 120,
  "tool_error": ""
}
```

**UserPromptSubmit：**
```json
{
  "...基础字段...": "",
  "prompt": "帮我重构这个模块"
}
```

**SessionStart：**
```json
{
  "...基础字段...": "",
  "source": "startup",
  "model": "glm-5-turbo",
  "memory_provider": "letta"
}
```

**PreCompact / PostCompact：**
```json
{
  "...基础字段...": "",
  "trigger": "auto",
  "message_count": 150,
  "estimated_tokens_before": 142000,
  "estimated_tokens_after": 8000
}
```

## 4. 匹配器

### 4.1 语法

| 模式 | 规则 | 示例 |
|------|------|------|
| `"*"` / `""` / 省略 | 匹配所有 | — |
| 仅 `字母\|数字\|_\|\\|` | 精确匹配或 `\\|` 多选 | `"Shell"` / `"Shell\\|FileCreate"` |
| 其他字符 | Go 正则 | `"^mcp__"` / `"File.*"` |

### 4.2 `if` 条件（仅工具事件）

```
Shell(rm *)           — 匹配 Shell 工具 + command 参数含 rm
FileReplace(*.go)     — 匹配 FileReplace 工具 + path 参数匹配 *.go
Shell(git push *)     — 匹配 Shell 工具 + command 子命令
```

优先评估 `matcher`（粗筛），再评估 `if`（细筛）。

## 5. Handler 类型

### 5.1 command — Shell 命令

```json
{
  "type": "command",
  "command": ".xbot/hooks/lint.sh",
  "timeout": 30,
  "async": false
}
```

- 事件载荷通过 stdin（JSON）传入
- 退出码 0：成功，解析 stdout JSON 输出
- 退出码 2：阻塞，stderr 反馈给 agent
- 其他退出码：非阻塞错误，继续执行

### 5.2 http — HTTP POST

```json
{
  "type": "http",
  "url": "http://localhost:8080/hooks/pre-tool",
  "headers": { "Authorization": "Bearer $MY_TOKEN" },
  "allowed_env_vars": ["MY_TOKEN"],
  "timeout": 10
}
```

- 事件载荷作为 POST body（`Content-Type: application/json`）
- 2xx：成功，解析 body
- 非 2xx / 连接失败：非阻塞错误
- 想阻塞必须返回 2xx + JSON body 含 `decision: "block"`

### 5.3 callback — Go 内置

```go
// 内置 hook 注册为 callback 类型，优先级最高
type CallbackHook struct {
    Name string
    Fn   func(ctx context.Context, event Event) (*Result, error)
}
```

现有 `LoggingHook`、`TimingHook`、`ApprovalHook` 迁移为 callback handler。

### 5.4 mcp_tool — MCP 工具调用

```json
{
  "type": "mcp_tool",
  "server": "security",
  "tool": "scan_file",
  "input": { "path": "${tool_input.path}" }
}
```

- `input` 中 `${path}` 从事件载荷插值
- 工具返回 `isError: true` = 非阻塞错误

## 6. 决策控制

### 6.1 Handler 返回格式

退出码 0 + stdout JSON：

```json
{
  "decision": "allow",
  "reason": "安全检查通过"
}
```

或更细粒度的 `hookSpecificOutput`：

```json
{
  "hookSpecificOutput": {
    "hookEventName": "PreToolUse",
    "permissionDecision": "deny",
    "permissionDecisionReason": "危险命令被策略阻止",
    "updatedInput": { "command": "rm -ri /tmp/build" }
  }
}
```

### 6.2 决策优先级

多 handler 返回不同决策时：`deny` > `defer` > `ask` > `allow`

### 6.3 各事件决策能力

| 事件 | 决策方式 | 说明 |
|------|----------|------|
| PreToolUse | `permissionDecision` | allow/deny/ask/defer + updatedInput |
| PermissionRequest | `decision.behavior` | allow/deny |
| UserPromptSubmit | 顶级 `decision` | `"block"` + reason |
| PostToolBatch | 顶级 `decision` | `"block"` 阻止下一轮模型调用 |
| AgentStop | 顶级 `decision` | `"block"` 阻止 agent 停止 |
| SubAgentStop | 顶级 `decision` | `"block"` 阻止 subagent 停止 |
| PreCompact | 顶级 `decision` | `"block"` 阻止压缩 |
| SessionStart/End、PostToolUse、PostToolUseFailure 等 | — | 无决策控制，仅副作用 |

### 6.4 全局控制

```json
{
  "decision": "block",
  "reason": "..."
}

// 或完全终止 agent：
{
  "continue": false,
  "stopReason": "构建失败，修复后再继续"
}
```

## 7. 配置

### 7.1 配置层级

| 位置 | 范围 | 可共享 |
|------|------|--------|
| `~/.xbot/hooks.json` | 用户级（所有项目） | ❌ |
| `<project>/.xbot/hooks.json` | 项目级 | ✅ 可提交 git |
| `<project>/.xbot/hooks.local.json` | 本地覆盖 | ❌ gitignored |

合并顺序：`user → project → local`（后者覆盖前者）

### 7.2 配置格式

```json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Shell",
        "hooks": [
          {
            "type": "command",
            "if": "Shell(rm -rf *)",
            "command": ".xbot/hooks/block-rm.sh",
            "timeout": 10
          }
        ]
      },
      {
        "matcher": "FileCreate|FileReplace",
        "hooks": [
          {
            "type": "command",
            "command": ".xbot/hooks/lint-check.sh",
            "async": true
          }
        ]
      }
    ],
    "PostToolUse": [
      {
        "matcher": "FileCreate|FileReplace",
        "hooks": [
          {
            "type": "http",
            "url": "http://localhost:3000/api/build-trigger",
            "timeout": 5
          }
        ]
      }
    ],
    "UserPromptSubmit": [
      {
        "hooks": [
          {
            "type": "command",
            "command": ".xbot/hooks/context-loader.sh"
          }
        ]
      }
    ],
    "SessionStart": [
      {
        "matcher": "startup",
        "hooks": [
          {
            "type": "command",
            "command": ".xbot/hooks/session-init.sh"
          }
        ]
      }
    ],
    "PreCompact": [
      {
        "hooks": [
          {
            "type": "command",
            "command": ".xbot/hooks/save-state.sh"
          }
        ]
      }
    ]
  }
}
```

### 7.3 脚本引用路径

环境变量帮助定位脚本：

| 变量 | 值 |
|------|------|
| `$XBOT_HOME` | `~/.xbot` |
| `$XBOT_PROJECT_DIR` | 当前项目根目录 |
| `$XBOT_SESSION_ID` | 当前 session ID |

## 8. 核心 Go 接口

```go
// agent/hooks/manager.go

// Manager 是 hook 系统的核心。
type Manager struct {
    layers    []*ConfigLayer
    executors map[string]Executor
    builtins  []*CallbackHook    // Go 内置 hook（Logging、Timing、Approval）
    mu        sync.RWMutex
}

// Emit 分发事件，返回聚合决策。
func (m *Manager) Emit(ctx context.Context, event Event) (*Decision, error)

// Event 统一的事件接口。
type Event interface {
    EventName() string                    // "PreToolUse"
    Payload() map[string]any              // 完整载荷
    ToolName() string                     // 工具事件有值，其他为 ""
    ToolInput() map[string]any            // 工具事件有值
}

// Decision 聚合决策。
type Decision struct {
    Action       Action          // Allow / Deny / Ask / Defer
    Reason       string
    UpdatedInput map[string]any  // PreToolUse 专用
    Context      string          // 注入 agent 上下文
}

type Action int
const ( Allow Action = iota; Deny; Ask; Defer )

// Executor handler 执行器接口。
type Executor interface {
    Type() string
    Execute(ctx context.Context, def *HookDef, event Event) (*Result, error)
}

// Result handler 执行结果。
type Result struct {
    ExitCode     int
    Stdout       string
    Stderr       string
    Decision     string         // "allow"/"deny"/"ask"/"defer"
    Reason       string
    UpdatedInput map[string]any
    Context      string
}
```

## 9. 迁移计划

### 废弃清单

| 旧代码 | 处置 |
|--------|------|
| `tools/hook.go` — `ToolHook` / `HookChain` | **删除**，由 `agent/hooks/manager.go` 替代 |
| `tools/hook_builtin.go` — `LoggingHook` / `TimingHook` | **迁移**为 `Manager` 的 builtin callback |
| `tools/approval.go` — `ApprovalHook` | **迁移**为 `PreToolUse` + `PermissionRequest` 的 builtin callback |
| `tools/hook_test.go` | **删除**，用新测试替代 |
| `agent/engine.go` — `executeWithHooks()` | **重写**，直接调用 `Manager.Emit()` |
| `agent/engine.go` — `HookChain` 字段 | **替换**为 `HookManager` |
| `agent/agent.go` — `hookChain` 字段 | **替换**为 `hooks.Manager` |
| `agent/backend*.go` — `ToolHookChain()` | **替换**为 `HookManager()` |

### 新增清单

| 新代码 | 说明 |
|--------|------|
| `agent/hooks/manager.go` | 核心管理器 |
| `agent/hooks/event.go` | Event 类型定义 |
| `agent/hooks/matcher.go` | 匹配器引擎 |
| `agent/hooks/executor_command.go` | Shell 命令执行器 |
| `agent/hooks/executor_http.go` | HTTP POST 执行器 |
| `agent/hooks/executor_mcp.go` | MCP 工具执行器 |
| `agent/hooks/config.go` | 配置加载与合并 |
| `agent/hooks/builtin.go` | 内置 hook（Logging、Timing、Approval） |

### 不动

| 代码 | 原因 |
|------|------|
| `event/` 包 | Event Trigger（入站 webhook）保持独立，与新 hook 系统互补 |
| `tools/event_trigger.go` | EventTrigger 工具保持不变 |

## 10. 集成点

```
agent/agent.go
  Run() 启动 → Manager.Emit(SessionStart)
  Run() 结束 → Manager.Emit(SessionEnd)

agent/engine_run.go
  Run() 入口 → Manager.Emit(UserPromptSubmit)     可阻塞
  Run() 结束 → Manager.Emit(AgentStop)             可阻塞

agent/engine.go
  executeTool() 前 → Manager.Emit(PreToolUse)      可阻塞 + 修改输入
  executeTool() 后 → Manager.Emit(PostToolUse)     记录
  executeTool() 错 → Manager.Emit(PostToolUseFailure)  错误通知
  批量工具完成      → Manager.Emit(PostToolBatch)   可阻塞

agent/context_manager.go
  maybeCompress() 前 → Manager.Emit(PreCompact)    可阻塞
  maybeCompress() 后 → Manager.Emit(PostCompact)   验证

tools/approval.go
  权限对话框 → Manager.Emit(PermissionRequest)     自动审批/拒绝
  权限被拒   → Manager.Emit(PermissionDenied)      重试建议

agent/subagent.go
  创建 → Manager.Emit(SubAgentStart)
  完成 → Manager.Emit(SubAgentStop)                可阻塞
```

## 11. 安全

| 风险 | 缓解 |
|------|------|
| 恶意 shell 命令 | command 类型默认禁用，需 `config.json` 中 `enable_command_hooks: true` |
| 死循环 | 单事件最多 10 handler，总超时 60s |
| 信息泄露 | 事件载荷中脱敏 api_key / secret 字段 |
| SSRF | HTTP handler 禁止内网地址（可配置白名单） |
| 覆盖安全 | 低层级 deny 不可被高层级 allow 覆盖 |

## 12. 对标总结

| 特性 | Claude Code | xbot |
|------|-------------|------|
| 事件数 | 30+ | 17（覆盖核心场景） |
| Handler 类型 | command / http / mcp_tool / prompt / agent | command / http / callback / mcp_tool |
| 匹配器 | 正则 + if 条件 | 一致 |
| 决策控制 | allow/deny/ask/defer + updatedInput | 一致 |
| 配置层级 | user/project/local/policy/plugin | user/project/local |
| 入站 hook | 无 | WebhookReceived + CronFired（独有） |
| 异步 hook | async + asyncRewake | async |
| 脚本环境变量 | `$CLAUDE_PROJECT_DIR` 等 | `$XBOT_PROJECT_DIR` 等 |
