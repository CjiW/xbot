---
title: "plan-tool-hooks"
weight: 220
---

# Issue #98：工具执行 Hook 机制（PreToolUse / PostToolUse）

## 调研结果

### 1. 工具定义与执行流程

#### 核心类型定义

| 类型 | 文件 | 说明 |
|------|------|------|
| `Tool` 接口 | `tools/interface.go:82-84` | `Execute(ctx *ToolContext, input string) (*ToolResult, error)` |
| `ToolResult` | `tools/interface.go:66-74` | 工具执行结果，含 Summary/Detail/Tips/IsError |
| `ToolContext` | `tools/interface.go:19-55` | 工具执行上下文，含 Ctx/WorkingDir/Registry 等 |
| `Registry` | `tools/interface.go:86-90` | 工具注册表，支持全局/核心/会话级工具管理 |
| `ToolCall` | `llm/types.go:57-61` | LLM 层的工具调用结构，含 ID/Name/Arguments |

#### 工具执行调用链

工具执行有**两条路径**，最终都调用 `Tool.Execute()`：

**路径 A：主 Agent 工具执行器**（`agent/engine_wire.go:391-455`）

```
Agent.buildToolExecutor()
  → 闭包函数(ctx, tc llm.ToolCall)
    → session MCP 查找 / 全局 Registry 查找
    → 激活检查 (IsToolActive)
    → 刷新 round (TouchTool)
    → buildToolContext(ctx, cfg)
    → tool.Execute(toolCtx, tc.Arguments)  ← 【插入点 A】
```

**路径 B：SubAgent 默认执行器**（`agent/engine.go:686-695`）

```
defaultToolExecutor(cfg)
  → 闭包函数(ctx, tc)
    → cfg.Tools.Get(tc.Name)
    → buildToolContext(ctx, cfg)
    → tool.Execute(toolCtx, tc.Arguments)  ← 【插入点 B】
```

**统一调用入口**：两条路径都通过 `RunConfig.ToolExecutor` 字段注入到 `engine.go` 的 `Run()` 函数：

```go
// agent/engine.go:93
ToolExecutor func(ctx context.Context, tc llm.ToolCall) (*tools.ToolResult, error)
```

在 `Run()` 主循环中，`execOne` 闭包（`engine.go:351-405`）负责单个工具的执行：

```go
// engine.go:391-395
start := time.Now()
result, execErr := toolExecutor(execCtx, tc)  // ← 【统一插入点】
elapsed := time.Since(start)
```

### 2. Middleware 机制分析

`agent/middleware.go` 中已实现 `MessagePipeline`，设计模式清晰：

| 组件 | 说明 |
|------|------|
| `MessageMiddleware` 接口 | `Name() + Priority() + Process(mc)` |
| `MessagePipeline` | 并发安全的中间件链，支持 Use/Remove/Run |
| 执行模式 | 按 Priority 排序，顺序执行，Process 返回 error 时**日志记录但继续** |
| 并发安全 | `sync.RWMutex`，Run 时获取快照后释放锁 |

**关键设计决策**：
- middleware 失败不中断流程（仅日志 Warn）
- 支持 Use/Remove 动态增删
- Run 使用 snapshot 避免 Run 期间锁竞争

### 3. Hook 插入策略分析

**选项 1：在 `tool.Execute()` 内部插入** — 需要修改所有 54 个 `Execute()` 方法，不可行。

**选项 2：在 `ToolExecutor` 层插入（推荐）** — 修改 `buildToolExecutor` 和 `defaultToolExecutor` 的返回闭包，在调用 `tool.Execute()` 前后插入 hook 调用。只需改 2 处，且两条路径都能覆盖。

**选项 3：在 `Run()` 的 `execOne` 中插入** — 虽然统一，但 hook 需要访问 `ToolContext`（如 channel/senderID），而 `execOne` 只能通过闭包获取 `toolExecutor`。

**结论**：选项 2 最优。hook 需要的信息（toolName、params、result、context）都可以在 `ToolExecutor` 闭包中获取。

---

## 方案

### 一、ToolHook 接口定义

新建文件 `tools/hook.go`：

```go
package tools

import (
    "context"
    "time"
)

// ToolHook 工具执行钩子接口
// 在工具调用前后提供扩展点，用于日志、审计、拦截、统计等场景。
type ToolHook interface {
    // Name 返回 hook 名称，用于日志和调试
    Name() string

    // PreToolUse 在工具执行前调用。
    // params: 工具参数（已解析的 JSON，map 形式，便于 hook 访问字段）
    // 返回 error 时跳过工具执行，将 error 作为工具执行结果返回给 LLM。
    PreToolUse(ctx context.Context, toolName string, params map[string]any) error

    // PostToolUse 在工具执行后调用（无论 PreToolUse 是否跳过）。
    // result: 工具执行结果（PreToolUse 返回 error 时为 nil）
    // elapsed: 工具执行耗时
    // err: 工具执行错误（PreToolUse 返回 error 时为该 error）
    PostToolUse(ctx context.Context, toolName string, params map[string]any, result *ToolResult, err error, elapsed time.Duration)
}
```

### 二、HookChain 管理器

新建文件 `tools/hook.go`（与接口同文件）：

```go
// HookChain 工具 hook 链，按注册顺序执行所有 hook。
// 并发安全：支持运行时动态注册/注销。
type HookChain struct {
    mu    sync.RWMutex
    hooks []ToolHook
}

func NewHookChain() *HookChain { ... }
func (c *HookChain) Use(hooks ...ToolHook) { ... }
func (c *HookChain) Remove(name string) int { ... }
func (c *HookChain) RunPre(ctx context.Context, toolName string, params map[string]any) error { ... }
func (c *HookChain) RunPost(ctx context.Context, toolName string, params map[string]any, result *ToolResult, err error, elapsed time.Duration) { ... }
```

**执行规则**：

| 场景 | PreToolUse | PostToolUse |
|------|-----------|-------------|
| hook 返回 error | 立即停止 Pre 链，返回该 error，**不执行工具** | 仍执行所有 Post hook（err = hook 的 error） |
| hook panic | recover，记录日志，继续下一个 hook | 同左 |
| hook 正常 | 继续下一个 | 继续下一个 |

**关键**：PreToolUse 返回 error 时**阻断工具执行**，但 PostToolUse **始终执行**（确保审计日志完整）。

### 三、RunConfig 集成

修改 `agent/engine.go` 的 `RunConfig`：

```go
type RunConfig struct {
    // ... 现有字段 ...
    
    // HookChain 工具执行 hook 链（nil = 无 hook）
    HookChain *tools.HookChain
}
```

### 四、ToolExecutor 闭包插入 Hook

#### 4.1 主 Agent 工具执行器（`agent/engine_wire.go:437`）

在 `buildToolExecutor` 返回的闭包中，`tool.Execute(toolCtx, tc.Arguments)` 调用前后插入：

```go
return func(ctx context.Context, tc llm.ToolCall) (*tools.ToolResult, error) {
    // ... 现有的 1-4 步（查找、激活检查、刷新 round、mkdir） ...
    
    // 5. 解析参数（供 hook 使用）
    var params map[string]any
    json.Unmarshal([]byte(tc.Arguments), &params)
    
    // 6. 执行 PreToolUse hooks
    if cfg.HookChain != nil {
        if err := cfg.HookChain.RunPre(ctx, tc.Name, params); err != nil {
            return nil, err  // 跳过工具执行
        }
    }
    
    // 7. 执行工具
    toolCtx := buildToolContext(ctx, cfg)
    start := time.Now()
    result, execErr := tool.Execute(toolCtx, tc.Arguments)
    elapsed := time.Since(start)
    
    // 8. 执行 PostToolUse hooks
    if cfg.HookChain != nil {
        cfg.HookChain.RunPost(ctx, tc.Name, params, result, execErr, elapsed)
    }
    
    return result, execErr
}
```

**注意**：`buildToolExecutor` 的闭包需要能访问 `cfg.HookChain`。当前闭包内部定义了 `cfg *RunConfig`，只需在闭包内增加 hook 调用即可。

#### 4.2 SubAgent 默认执行器（`agent/engine.go:686`）

同理在 `defaultToolExecutor` 的闭包中插入 hook：

```go
func defaultToolExecutor(cfg *RunConfig) func(ctx context.Context, tc llm.ToolCall) (*tools.ToolResult, error) {
    return func(ctx context.Context, tc llm.ToolCall) (*tools.ToolResult, error) {
        tool, ok := cfg.Tools.Get(tc.Name)
        if !ok {
            return nil, fmt.Errorf("unknown tool: %s", tc.Name)
        }

        // 解析参数
        var params map[string]any
        json.Unmarshal([]byte(tc.Arguments), &params)

        // Pre hooks
        if cfg.HookChain != nil {
            if err := cfg.HookChain.RunPre(ctx, tc.Name, params); err != nil {
                return nil, err
            }
        }

        toolCtx := buildToolContext(ctx, cfg)
        start := time.Now()
        result, execErr := tool.Execute(toolCtx, tc.Arguments)
        elapsed := time.Since(start)

        // Post hooks
        if cfg.HookChain != nil {
            cfg.HookChain.RunPost(ctx, tc.Name, params, result, execErr, elapsed)
        }

        return result, execErr
    }
}
```

#### 4.3 RunConfig 传递 HookChain

在 `buildMainRunConfig`（`agent/engine_wire.go:40-140`）、`buildCronRunConfig`（`agent/engine_wire.go:143-200`）、`buildSubAgentRunConfig`（`agent/engine_wire.go:203-350`）中，将 `Agent.hookChain` 传入 `RunConfig.HookChain`。

### 五、内置 Hook 实现

新建文件 `tools/hook_builtin.go`：

#### 5.1 日志 Hook（LoggingHook）

```go
type LoggingHook struct{}

func (h *LoggingHook) Name() string { return "logging" }

func (h *LoggingHook) PreToolUse(ctx context.Context, toolName string, params map[string]any) error {
    log.Ctx(ctx).WithFields(log.Fields{
        "tool":   toolName,
        "params": params,
    }).Info("Tool execution started")
    return nil
}

func (h *LoggingHook) PostToolUse(ctx context.Context, toolName string, params map[string]any, result *ToolResult, err error, elapsed time.Duration) {
    fields := log.Fields{
        "tool":    toolName,
        "elapsed": elapsed.Round(time.Millisecond),
    }
    if err != nil {
        log.Ctx(ctx).WithFields(fields).WithError(err).Warn("Tool execution failed")
    } else {
        log.Ctx(ctx).WithFields(fields).Info("Tool execution completed")
    }
}
```

#### 5.2 耗时统计 Hook（TimingHook）

```go
type TimingHook struct {
    mu       sync.Mutex
    stats    map[string]*toolTimingStats  // toolName → stats
}

type toolTimingStats struct {
    Count    int
    Total    time.Duration
    Min      time.Duration
    Max      time.Duration
}

func NewTimingHook() *TimingHook { ... }

func (h *TimingHook) Name() string { return "timing" }

func (h *TimingHook) PreToolUse(ctx context.Context, toolName string, params map[string]any) error {
    return nil  // 仅在 Post 中统计
}

func (h *TimingHook) PostToolUse(ctx context.Context, toolName string, params map[string]any, result *ToolResult, err error, elapsed time.Duration) {
    h.mu.Lock()
    defer h.mu.Unlock()
    // 累加统计
}

// Stats 返回各工具的耗时统计快照
func (h *TimingHook) Stats() map[string]toolTimingStats { ... }

// Reset 清零统计
func (h *TimingHook) Reset() { ... }
```

### 六、Agent 集成

修改 `agent/agent.go`：

```go
type Agent struct {
    // ... 现有字段 ...
    hookChain *tools.HookChain  // 工具执行 hook 链
}
```

在 `New()` 函数中初始化默认 hook chain：

```go
// agent/agent.go New() 函数中
hookChain := tools.NewHookChain()
hookChain.Use(tools.NewLoggingHook())
hookChain.Use(tools.NewTimingHook())
agent.hookChain = hookChain
```

暴露方法供外部注册 hook：

```go
// ToolHookChain 返回 Agent 的工具 hook 链，支持运行时动态注册/注销
func (a *Agent) ToolHookChain() *tools.HookChain {
    return a.hookChain
}
```

### 七、配置机制

当前不需要额外的配置文件。hook 失败的处理策略**内置在 HookChain 中**：

| 策略 | 说明 |
|------|------|
| PreToolUse panic | recover + 日志 Error + **继续下一个 hook**（不阻断） |
| PostToolUse panic | recover + 日志 Error + 继续下一个 |
| PreToolUse 返回 error | **阻断工具执行**，返回该 error |

这与 middleware 的策略一致：**hook 自身异常不阻断主流程，但 PreToolUse 返回的语义 error 会阻断**。

如果未来需要更细粒度的配置（如某个 hook 失败是否阻断），可以在 `ToolHook` 接口增加 `OnPanic() error` 方法，或通过 `HookOption` 配置。当前版本保持简洁。

---

## 任务拆解

### 阶段 1：核心接口与管理器（无破坏性改动）

| # | 文件 | 操作 | 说明 |
|---|------|------|------|
| 1 | `tools/hook.go` | **新建** | 定义 `ToolHook` 接口、`HookChain` 结构体及方法 |
| 2 | `tools/hook.go` | 新建 | `HookChain.Use()` / `Remove()` / `RunPre()` / `RunPost()` 实现 |

### 阶段 2：内置 Hook 实现

| # | 文件 | 操作 | 说明 |
|---|------|------|------|
| 3 | `tools/hook_builtin.go` | **新建** | `LoggingHook` 实现 |
| 4 | `tools/hook_builtin.go` | 新建 | `TimingHook` 实现（含 `Stats()` / `Reset()`） |

### 阶段 3：Agent 集成

| # | 文件 | 操作 | 说明 |
|---|------|------|------|
| 5 | `agent/engine.go` | **修改** | `RunConfig` 增加 `HookChain *tools.HookChain` 字段 |
| 6 | `agent/engine_wire.go` | **修改** | `buildToolExecutor()` 闭包中插入 Pre/Post hook 调用 |
| 7 | `agent/engine.go` | **修改** | `defaultToolExecutor()` 闭包中插入 Pre/Post hook 调用 |
| 8 | `agent/engine_wire.go` | **修改** | `buildMainRunConfig()` 传递 `HookChain` |
| 9 | `agent/engine_wire.go` | **修改** | `buildCronRunConfig()` 传递 `HookChain` |
| 10 | `agent/engine_wire.go` | **修改** | `buildSubAgentRunConfig()` 传递 `HookChain` |
| 11 | `agent/agent.go` | **修改** | `Agent` 结构体增加 `hookChain` 字段 |
| 12 | `agent/agent.go` | **修改** | `New()` 函数中初始化默认 hook chain |
| 13 | `agent/agent.go` | **修改** | 新增 `ToolHookChain()` 公开方法 |

### 阶段 4：测试

| # | 文件 | 操作 | 说明 |
|---|------|------|------|
| 14 | `tools/hook_test.go` | **新建** | `HookChain` 单元测试 |
| 15 | `tools/hook_builtin_test.go` | **新建** | `LoggingHook` / `TimingHook` 单元测试 |
| 16 | `agent/engine_test.go` | **修改** | 增加带 hook 的 `Run()` 测试用例 |

### 改动汇总

| 类型 | 数量 |
|------|------|
| 新建文件 | 4（`hook.go`, `hook_builtin.go`, `hook_test.go`, `hook_builtin_test.go`） |
| 修改文件 | 4（`engine.go`, `engine_wire.go`, `agent.go`, `engine_test.go`） |
| 新增代码量 | ~300 行（含测试 ~200 行） |

---

## 验证标准

### 阶段 1 验证
- [ ] `HookChain` 支持注册多个 hook，按注册顺序执行
- [ ] `HookChain.Remove()` 按名称移除，返回移除数量
- [ ] `RunPre()` 中任一 hook 返回 error，立即停止并返回该 error
- [ ] `RunPre()` 中 hook panic 时 recover 并继续下一个
- [ ] `RunPost()` 始终执行所有 hook（即使 Pre 返回了 error）
- [ ] 并发安全：多个 goroutine 同时 Use/Remove/Run 无 data race

### 阶段 2 验证
- [ ] `LoggingHook` 在 Pre/Post 阶段输出正确的日志（通过日志捕获验证）
- [ ] `TimingHook` 正确累计各工具的 count/total/min/max
- [ ] `TimingHook.Stats()` 返回正确的快照
- [ ] `TimingHook.Reset()` 正确清零

### 阶段 3 验证
- [ ] 主 Agent 执行工具时，hook 被正确调用（通过测试 hook 验证调用顺序和参数）
- [ ] SubAgent 执行工具时，hook 被正确调用
- [ ] PreToolUse 返回 error 时，工具不执行，error 返回给 LLM
- [ ] `go test ./...` 全部通过
- [ ] `go vet ./...` 无警告
- [ ] `go build ./...` 编译成功

### 阶段 4 验证
- [ ] 测试覆盖：Pre 正常/Pre 阻断/Post 正常/panic 恢复/并发安全
- [ ] `go test -race ./tools/ ./agent/` 无 race

---

## 风险与注意

### 1. 参数解析开销
每次工具调用都需要 `json.Unmarshal` 参数为 `map[string]any`。对于高频调用的工具（如 Shell），这是额外开销。

**缓解**：解析失败时传空 map（不阻断），且 JSON 解析对小型参数（<1KB）几乎无感知。

### 2. Hook 阻断风险
PreToolUse 返回 error 会阻断工具执行。如果用户注册了有 bug 的 hook，可能导致所有工具无法使用。

**缓解**：
- hook 自身的 panic 不会阻断（recover 后继续）
- 文档明确说明 PreToolUse 返回 error 的语义
- 可通过 `HookChain.Remove()` 快速移除有问题的 hook

### 3. 与 Middleware 的职责边界
Middleware 管消息构建（system prompt、历史、skills 注入），Hook 管工具执行。两者互补，不重叠。

**注意**：不要在 Hook 中修改 LLM 消息或上下文，那是 Middleware 的职责。

### 4. 性能影响
Hook 增加了每次工具调用的固定开销（两次函数调用 + JSON 解析）。

**缓解**：
- `LoggingHook` 和 `TimingHook` 本身非常轻量（~微秒级）
- 可通过 `HookChain.Remove("logging")` 在生产环境关闭日志 hook
- 未来可考虑 hook 执行结果的 metrics 上报

### 5. SubAgent Hook 继承
当前方案中 SubAgent 继承父 Agent 的 `HookChain` 实例。这意味着所有层级共享同一套 hook。

**这是合理的**：统计和日志通常需要全局视角。如果未来需要按 Agent 级别隔离 hook，可通过 `buildSubAgentRunConfig` 中创建新的 `HookChain` 来实现。

### 6. 现有日志的调整
`engine.go:388-405` 中的 `execOne` 闭包已有工具调用的日志（`Tool call:` / `Tool done:` / `Tool failed:`）。引入 `LoggingHook` 后，这些日志会重复。

**建议**：在阶段 3 实现时，评估是否将 `execOne` 中的日志移除或降级为 Debug，由 `LoggingHook` 统一接管。这是可选的优化，不影响功能正确性。
