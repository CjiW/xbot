# xbot 上下文管理架构抽象与开关机制设计

> 中书省拟 | 2026-03-19
> 状态：待门下省审核（v2，修正版）
> 前置文档：[Phase 1 设计](context-management-design.md)、[Phase 2 设计](context-management-phase2-design.md)
> v1 审核意见：门下省驳回修改，6 条修改意见（见附录 A）

---

## 一、调研结果

### 1.1 现有压缩架构

| 文件 | 职责 | 关键结构 |
|------|------|---------|
| `agent/compress.go` | 压缩逻辑实现 | `CompressResult`（L18-21）、`compressContext()`（L240）、`extractDialogueFromTail()`、`thinTail()` |
| `agent/engine.go` | 运行引擎，调用压缩 | `CompressConfig`（L177-181）、`RunConfig.AutoCompress`（L82）、`maybeCompress()`（L172） |
| `agent/engine_wire.go` | 构建运行配置 | `buildMainRunConfig()`、`buildSubAgentRunConfig()`、`buildCronRunConfig()` |
| `agent/context_handler.go` | `/context` 命令 | `handleContext()`（L13）— 仅展示统计信息 |
| `agent/command.go` | 命令接口 | `Command` 接口（L12）、`CommandRegistry`（L49） |
| `agent/command_builtin.go` | 内置命令注册 | `contextCmd`（L168-175）、`registerBuiltinCommands()`（L178-190） |
| `agent/agent.go` | Agent 核心配置 | `Agent` 结构体含 `enableAutoCompress`/`maxContextTokens`/`compressionThreshold`（L136-138） |
| `config/config.go` | 全局配置加载 | `Load()` 一次性读取所有环境变量，`EnableAutoCompress` 默认 `true`（L107） |

### 1.2 当前压缩调用链

```
engine.go Run()
  └── maybeCompress()（L172）
        ├── cfg.AutoCompress == nil → 跳过
        ├── CountMessagesTokens() → 判断是否超阈值
        └── cc.CompressFunc()（L191-192）→ 实际压缩（函数指针）
              └── agent.compressContext()  // Phase 1 实现

engine.go Run()（L264-299）
  └── 输入超限强制压缩路径
        └── cc.CompressFunc() → 同上
```

关键发现：
- **压缩逻辑已通过函数指针注入**：`CompressConfig.CompressFunc` 签名为 `func(ctx context.Context, messages []llm.ChatMessage, client llm.LLM, model string) (*CompressResult, error)`（`engine.go:180`），已有策略模式雏形
- **开关粒度粗**：`enableAutoCompress` 是全局 bool，只能开/关整个自动压缩，无法区分 Phase 1/Phase 2
- **`/context` 命令只读**：现有 `contextCmd`（`command_builtin.go:168-175`）只匹配精确的 `/context`，仅展示统计信息
- **配置体系**：`config.Config.Load()` → `agent.Config` → `Agent`，环境变量统一在 `Load()` 中一次性读取

### 1.3 Phase 2 设计要点（尚未实现）

Phase 2 设计了三层渐进压缩、智能触发、话题分区、质量保障等高级功能。
核心区别：Phase 1 是"一刀切压缩"，Phase 2 是"渐进式多级压缩"。
两者的接口签名一致（均返回 `*CompressResult`），为策略模式切换提供天然基础。

---

## 二、方案设计

### 2.1 架构总览

```
                    ┌─────────────────────────┐
                    │   ContextManager 接口    │  ← 统一抽象
                    └───────────┬─────────────┘
                                │
                    ┌───────────┴─────────────┐
                    │ ContextManagerConfig     │  ← 开关 + 策略选择 + 并发保护
                    │  (sync.RWMutex)          │
                    └───────────┬─────────────┘
                                │
              ┌─────────────────┼─────────────────┐
              │                 │                  │
    ┌─────────▼──────┐ ┌───────▼──────────┐ ┌────▼──────────┐
    │ Phase1Manager  │ │  Phase2Manager   │ │  NoopManager  │
    │ (现有实现)      │ │  (未来实现)       │ │  (不压缩)     │
    └────────────────┘ └──────────────────┘ └───────────────┘
```

### 2.2 ContextManager 接口定义

**新文件**：`agent/context_manager.go`

```go
package agent

import (
    "context"
    "xbot/llm"
    "xbot/session"
)

// ContextMode 上下文管理模式
type ContextMode string

const (
    // ContextModePhase1 Phase 1 双视图架构（当前默认）
    ContextModePhase1 ContextMode = "phase1"
    // ContextModePhase2 Phase 2 三层渐进压缩（未来实现）
    ContextModePhase2 ContextMode = "phase2"
    // ContextModeNone 禁用自动上下文压缩
    ContextModeNone ContextMode = "none"
)

// ValidContextModes 所有可能的上下文模式
var ValidContextModes = []ContextMode{ContextModePhase1, ContextModePhase2, ContextModeNone}

// IsValidContextMode 检查是否为有效的上下文模式
func IsValidContextMode(mode ContextMode) bool {
    for _, m := range ValidContextModes {
        if m == mode {
            return true
        }
    }
    return false
}

// ContextManager 上下文管理器统一接口。
// 所有压缩策略实现此接口，通过策略模式实现新旧架构可切换。
type ContextManager interface {
    // Mode 返回当前管理模式标识。
    Mode() ContextMode

    // ShouldCompress 判断是否需要触发自动压缩。
    // 参数：
    //   - messages: 当前上下文消息
    //   - model: LLM 模型名（用于 token 计数）
    //   - toolTokens: 工具定义占用的 token 数
    ShouldCompress(messages []llm.ChatMessage, model string, toolTokens int) bool

    // Compress 执行上下文压缩。
    Compress(ctx context.Context, messages []llm.ChatMessage, client llm.LLM, model string) (*CompressResult, error)

    // ManualCompress 手动压缩（/compress 命令使用）。
    // 关键契约：无论当前模式如何，ManualCompress 都应尽力执行压缩。
    // 即使 auto=false 的 noopManager，ManualCompress 也降级到 Phase1 执行，
    // 保留 /compress 手动命令始终可用的现有语义。
    ManualCompress(ctx context.Context, messages []llm.ChatMessage, client llm.LLM, model string) (*CompressResult, error)

    // ContextInfo 返回上下文统计信息（/context info 命令使用）。
    ContextInfo(messages []llm.ChatMessage, model string, toolTokens int) *ContextStats

    // SessionHook 返回压缩后的 session 持久化钩子（可选，返回 nil 表示无特殊处理）。
    // Phase 2 可能需要在此钩子中做额外操作（如更新话题分区索引）。
    SessionHook() SessionCompressHook
}

// ContextStats 上下文统计信息
type ContextStats struct {
    SystemTokens    int
    UserTokens      int
    AssistantTokens int
    ToolMsgTokens   int
    ToolDefTokens   int
    TotalTokens     int
    MaxTokens       int
    Threshold       int
    Mode            ContextMode
    IsRuntimeOverride bool // 是否为运行时覆盖
    DefaultMode     ContextMode
}

// SessionCompressHook 压缩后的 session 处理钩子
type SessionCompressHook interface {
    // AfterPersist 在 session 持久化压缩结果后调用
    AfterPersist(ctx context.Context, tenantSession *session.TenantSession, result *CompressResult)
}
```

### 2.3 ContextManagerConfig 配置结构（含并发安全）

**新文件**：`agent/context_manager.go`

```go
import "sync"

// ContextManagerConfig 上下文管理器配置。
// 包含全局配置（环境变量/Agent.Config）和运行时开关（命令行切换）。
// 所有读写操作通过 sync.RWMutex 保护，确保并发安全。
type ContextManagerConfig struct {
    mu sync.RWMutex

    // MaxContextTokens 最大上下文 token 数（默认 100000）
    MaxContextTokens int
    // CompressionThreshold 触发压缩的 token 比例阈值（默认 0.7）
    CompressionThreshold float64

    // DefaultMode 默认压缩模式（启动时决定，来自环境变量或 Agent.Config）
    DefaultMode ContextMode

    // runtimeMode 运行时模式覆盖（通过 /context mode 命令切换）
    // 空值表示使用 DefaultMode，非空值覆盖 DefaultMode
    runtimeMode ContextMode
}

// EffectiveMode 返回当前生效的模式（RuntimeMode 优先）。
// 读锁保护。
func (c *ContextManagerConfig) EffectiveMode() ContextMode {
    c.mu.RLock()
    defer c.mu.RUnlock()
    if c.runtimeMode != "" {
        return c.runtimeMode
    }
    return c.DefaultMode
}

// RuntimeMode 返回当前运行时覆盖模式（无覆盖时返回空字符串）。
// 读锁保护。
func (c *ContextManagerConfig) RuntimeMode() ContextMode {
    c.mu.RLock()
    defer c.mu.RUnlock()
    return c.runtimeMode
}

// SetRuntimeMode 设置运行时模式覆盖。
// 写锁保护。
func (c *ContextManagerConfig) SetRuntimeMode(mode ContextMode) {
    c.mu.Lock()
    defer c.mu.Unlock()
    c.runtimeMode = mode
}

// ResetRuntimeMode 清除运行时覆盖，恢复默认模式。
// 写锁保护。
func (c *ContextManagerConfig) ResetRuntimeMode() {
    c.mu.Lock()
    defer c.mu.Unlock()
    c.runtimeMode = ""
}
```

### 2.4 Agent 结构体变更

在 `agent/agent.go` 中修改 `Agent` 结构体：

```go
type Agent struct {
    // ... 现有字段 ...

    // 上下文压缩配置（新增，替换旧的三个字段）
    contextManagerConfig *ContextManagerConfig
    contextManagerMu     sync.RWMutex            // 保护 contextManager 的并发读写
    contextManager       ContextManager           // 当前激活的管理器实例

    // 以下旧字段将在步骤 7（清理）中删除，步骤 1-6 期间保留用于向后兼容：
    // maxContextTokens     int
    // compressionThreshold float64
    // enableAutoCompress   bool
}
```

新增 Agent 方法（并发安全读写 ContextManager）：

```go
// GetContextManager 获取当前上下文管理器（读锁保护）。
// 用于 buildMainRunConfig / buildSubAgentRunConfig / handleCompress 等场景。
func (a *Agent) GetContextManager() ContextManager {
    a.contextManagerMu.RLock()
    defer a.contextManagerMu.RUnlock()
    return a.contextManager
}

// SetContextManager 替换当前上下文管理器（写锁保护）。
// 用于 /context mode 命令运行时切换。
func (a *Agent) SetContextManager(cm ContextManager) {
    a.contextManagerMu.Lock()
    defer a.contextManagerMu.Unlock()
    a.contextManager = cm
}
```

在 `Agent.Config` 中新增：

```go
type Config struct {
    // ... 现有字段 ...

    // 上下文管理配置（新增）
    // 优先级：ContextMode > EnableAutoCompress 旧字段
    // 默认 "phase1"（保持现有行为）
    ContextMode ContextMode

    // 旧字段保留向后兼容（步骤 1-6 期间保留）：
    // MaxContextTokens     int
    // CompressionThreshold float64
    // EnableAutoCompress   bool
}
```

### 2.5 三种实现

#### 2.5.1 Phase1Manager（现有逻辑重构，消除 Agent 引用）

**新文件**：`agent/context_manager_phase1.go`

核心改动：将 `compressContext` 从 Agent 方法提取为独立函数，消除循环引用。

```go
package agent

import (
    "context"
    "fmt"
    "strings"

    "xbot/llm"
    log "xbot/logger"
)

// phase1Manager Phase 1 双视图压缩管理器。
// 封装现有 compress.go 中的逻辑，行为与现有完全一致。
// 不持有 *Agent 引用，仅依赖配置和独立函数。
type phase1Manager struct {
    config *ContextManagerConfig
}

func newPhase1Manager(cfg *ContextManagerConfig) *phase1Manager {
    return &phase1Manager{config: cfg}
}

func (m *phase1Manager) Mode() ContextMode { return ContextModePhase1 }

func (m *phase1Manager) ShouldCompress(messages []llm.ChatMessage, model string, toolTokens int) bool {
    if len(messages) <= 3 {
        return false
    }
    msgTokens, err := llm.CountMessagesTokens(messages, model)
    if err != nil {
        return false
    }
    tokenCount := msgTokens + toolTokens
    threshold := int(float64(m.config.MaxContextTokens) * m.config.CompressionThreshold)
    return tokenCount >= threshold
}

func (m *phase1Manager) Compress(ctx context.Context, messages []llm.ChatMessage, client llm.LLM, model string) (*CompressResult, error) {
    return compressMessages(ctx, messages, client, model)
}

func (m *phase1Manager) ManualCompress(ctx context.Context, messages []llm.ChatMessage, client llm.LLM, model string) (*CompressResult, error) {
    return compressMessages(ctx, messages, client, model)
}

func (m *phase1Manager) ContextInfo(messages []llm.ChatMessage, model string, toolTokens int) *ContextStats {
    cfg := m.config
    var systemTokens, userTokens, assistantTokens, toolMsgTokens int

    for _, msg := range messages {
        tokens, err := llm.CountMessagesTokens([]llm.ChatMessage{msg}, model)
        if err != nil {
            continue
        }
        switch msg.Role {
        case "system":
            systemTokens += tokens
        case "user":
            userTokens += tokens
        case "assistant":
            assistantTokens += tokens
        case "tool":
            toolMsgTokens += tokens
        }
    }

    total := systemTokens + userTokens + assistantTokens + toolMsgTokens + toolTokens
    threshold := int(float64(cfg.MaxContextTokens) * cfg.CompressionThreshold)

    return &ContextStats{
        SystemTokens:    systemTokens,
        UserTokens:      userTokens,
        AssistantTokens: assistantTokens,
        ToolMsgTokens:   toolMsgTokens,
        ToolDefTokens:   toolTokens,
        TotalTokens:     total,
        MaxTokens:       cfg.MaxContextTokens,
        Threshold:       threshold,
        Mode:            cfg.EffectiveMode(),
        IsRuntimeOverride: cfg.RuntimeMode() != "",
        DefaultMode:     cfg.DefaultMode,
    }
}

func (m *phase1Manager) SessionHook() SessionCompressHook { return nil }
```

**关键重构**：将 `compressContext()` 提取为独立函数 `compressMessages()`（放在 `agent/compress.go` 中）：

```go
// compressMessages 使用 LLM 压缩对话历史（独立函数，不依赖 Agent receiver）。
// 逻辑与现有 compressContext() 完全一致，仅为消除 phase1Manager 对 *Agent 的引用。
// 现有 compressContext() 改为调用此函数：
//
//   func (a *Agent) compressContext(...) { return compressMessages(...) }
func compressMessages(ctx context.Context, messages []llm.ChatMessage, client llm.LLM, model string) (*CompressResult, error) {
    // 第一步：找到尾部安全切割点
    tailStart := len(messages)
    for i := len(messages) - 1; i >= 1; i-- {
        msg := messages[i]
        if msg.Role == "user" {
            tailStart = i
            break
        }
        if msg.Role == "assistant" && len(msg.ToolCalls) == 0 {
            tailStart = i
            break
        }
        if i == 1 {
            tailStart = 1
        }
    }
    // ... 后续逻辑完全复用现有 compressContext 的代码 ...
    // （extractDialogueFromTail、thinTail、LLM 压缩调用等，不动）
}
```

#### 2.5.2 Phase2Manager（预留接口）

**新文件**：`agent/context_manager_phase2.go`

```go
// phase2Manager Phase 2 三层渐进压缩管理器。
// 目前为空壳实现，Phase 2 实现时填充。
// NewContextManager() 中会自动降级到 Phase 1。
type phase2Manager struct {
    config *ContextManagerConfig
    // Phase 2 专用字段（未来实现）：
    // topicPartitioner  *TopicPartitioner
    // qualityChecker    *QualityChecker
    // progressiveLevels [3]CompressionLevel
}

func newPhase2Manager(cfg *ContextManagerConfig) *phase2Manager {
    return &phase2Manager{config: cfg}
}

func (m *phase2Manager) Mode() ContextMode { return ContextModePhase2 }

func (m *phase2Manager) ShouldCompress(messages []llm.ChatMessage, model string, toolTokens int) bool {
    // Phase 2 智能触发逻辑（未来实现）
    // 临时 fallback：与 Phase 1 相同的阈值判断
    if len(messages) <= 3 {
        return false
    }
    msgTokens, err := llm.CountMessagesTokens(messages, model)
    if err != nil {
        return false
    }
    tokenCount := msgTokens + toolTokens
    threshold := int(float64(m.config.MaxContextTokens) * m.config.CompressionThreshold)
    return tokenCount >= threshold
}

func (m *phase2Manager) Compress(ctx context.Context, messages []llm.ChatMessage, client llm.LLM, model string) (*CompressResult, error) {
    // TODO: Phase 2 三层渐进压缩实现
    return nil, fmt.Errorf("phase 2 compression not yet implemented")
}

func (m *phase2Manager) ManualCompress(ctx context.Context, messages []llm.ChatMessage, client llm.LLM, model string) (*CompressResult, error) {
    // ManualCompress 契约：无论模式如何，都尽力执行。
    // Phase 2 未实现时，降级到 compressMessages（Phase 1 逻辑）。
    return compressMessages(ctx, messages, client, model)
}

func (m *phase2Manager) ContextInfo(messages []llm.ChatMessage, model string, toolTokens int) *ContextStats {
    // 复用 Phase 1 的统计逻辑（统计方式相同）
    return newPhase1Manager(m.config).ContextInfo(messages, model, toolTokens)
}

func (m *phase2Manager) SessionHook() SessionCompressHook { return nil }
```

#### 2.5.3 noopManager（禁用自动压缩，但 /compress 仍可用）

**新文件**：`agent/context_manager.go`

关键设计决策：`noopManager` 只禁用**自动**压缩（`ShouldCompress` 返回 false），`ManualCompress` **降级到 Phase 1 执行**，确保 `/compress` 命令始终可用（保持现有语义）。

```go
// noopManager 禁用自动压缩的管理器。
// ShouldCompress 始终返回 false，但 ManualCompress 仍可执行（降级到 Phase 1）。
type noopManager struct {
    config  *ContextManagerConfig
    phase1  *phase1Manager // 内嵌 Phase1 用于 ManualCompress 和 ContextInfo
}

func newNoopManager(cfg *ContextManagerConfig) *noopManager {
    return &noopManager{
        config: cfg,
        phase1: newPhase1Manager(cfg),
    }
}

func (m *noopManager) Mode() ContextMode { return ContextModeNone }

func (m *noopManager) ShouldCompress([]llm.ChatMessage, string, int) bool {
    return false // 自动压缩始终禁用
}

func (m *noopManager) Compress(context.Context, []llm.ChatMessage, llm.LLM, string) (*CompressResult, error) {
    // 自动路径不应到达这里（ShouldCompress 返回 false）
    return nil, fmt.Errorf("auto compression is disabled (mode=none)")
}

func (m *noopManager) ManualCompress(ctx context.Context, messages []llm.ChatMessage, client llm.LLM, model string) (*CompressResult, error) {
    // /compress 手动命令：降级到 Phase 1 执行，保持命令始终可用
    return m.phase1.ManualCompress(ctx, messages, client, model)
}

func (m *noopManager) ContextInfo(messages []llm.ChatMessage, model string, toolTokens int) *ContextStats {
    // 仍返回完整统计信息（复用 Phase1 的实现）
    stats := m.phase1.ContextInfo(messages, model, toolTokens)
    stats.Mode = ContextModeNone
    return stats
}

func (m *noopManager) SessionHook() SessionCompressHook { return nil }
```

### 2.6 ContextManager 工厂

**新文件**：`agent/context_manager.go`

```go
// NewContextManager 根据配置创建对应的 ContextManager 实例。
func NewContextManager(cfg *ContextManagerConfig) ContextManager {
    mode := cfg.EffectiveMode()
    switch mode {
    case ContextModePhase2:
        // Phase 2: 即使未实现也创建 phase2Manager，
        // Compress 会返回错误并自动降级，ManualCompress 降级到 Phase 1
        log.WithField("mode", mode).Warn("Phase 2 not yet implemented, will fallback to Phase 1 on actual compression")
        return newPhase2Manager(cfg)
    case ContextModeNone:
        return newNoopManager(cfg)
    case ContextModePhase1, "":
        return newPhase1Manager(cfg)
    default:
        log.WithField("mode", mode).Warnf("Unknown context mode %q, falling back to Phase 1", mode)
        return newPhase1Manager(cfg)
    }
}
```

### 2.7 开关机制设计

#### 2.7.1 配置层（启动时）

**环境变量**：`AGENT_CONTEXT_MODE`（遵循现有 `AGENT_` 前缀命名规范）

| 值 | 行为 |
|----|------|
| `phase1` | Phase 1 双视图压缩（默认） |
| `phase2` | Phase 2 渐进压缩（未实现时 Compress 降级到 Phase 1） |
| `none` | 禁用自动压缩（`/compress` 仍可用） |
| （空） | 由 `EnableAutoCompress` 旧字段决定：true → phase1，false → none |

**在 `config/config.go` 的 `Load()` 中新增**（与现有 `AGENT_ENABLE_AUTO_COMPRESS` 同级）：

```go
// config/config.go Load() 函数中 Agent 配置段新增：
func Load() Config {
    // ... 现有代码 ...
    agent := AgentConfig{
        // ... 现有字段 ...
        ContextMode:         getEnvString("AGENT_CONTEXT_MODE", ""),      // 新增
        EnableAutoCompress:  getEnvBoolOrDefault("AGENT_ENABLE_AUTO_COMPRESS", true),
        MaxContextTokens:    getEnvInt("AGENT_MAX_CONTEXT_TOKENS", 100000),
        CompressionThreshold: getEnvFloat("AGENT_COMPRESSION_THRESHOLD", 0.7),
    }
    // ...
}
```

**向后兼容逻辑**（在 `Agent.New()` 中）：

```go
// resolveContextMode 根据新旧配置确定上下文管理模式。
// 优先级：ContextMode > EnableAutoCompress > 默认 phase1
func resolveContextMode(cfg Config) ContextMode {
    // 1. 优先使用新配置
    if cfg.ContextMode != "" {
        if IsValidContextMode(cfg.ContextMode) {
            return cfg.ContextMode
        }
        log.WithField("mode", cfg.ContextMode).Warn("Invalid AGENT_CONTEXT_MODE, ignoring")
    }
    // 2. 向后兼容：旧字段
    if !cfg.EnableAutoCompress {
        return ContextModeNone
    }
    // 3. 默认 phase1
    return ContextModePhase1
}
```

#### 2.7.2 命令层（运行时）

**修改现有 `contextCmd`**（`command_builtin.go:168-175`），不新建命令。

现有代码：
```go
// command_builtin.go:168-175
type contextCmd struct{}
func (c *contextCmd) Name() string      { return "/context" }
func (c *contextCmd) Aliases() []string { return nil }
func (c *contextCmd) Match(s string) bool {
    trimmed := strings.TrimSpace(s)
    return strings.ToLower(trimmed) == "/context"
}
func (c *contextCmd) Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
    return a.handleContext(ctx, msg, ...)
}
```

修改后：
```go
// command_builtin.go — 修改现有 contextCmd
type contextCmd struct{}

func (c *contextCmd) Name() string      { return "/context" }
func (c *contextCmd) Aliases() []string { return nil }

// Match 支持子命令匹配：/context, /context info, /context mode ...
func (c *contextCmd) Match(s string) bool {
    trimmed := strings.TrimSpace(strings.ToLower(s))
    return trimmed == "/context" || strings.HasPrefix(trimmed, "/context ")
}

func (c *contextCmd) Concurrent() bool  { return true }

func (c *contextCmd) Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
    content := strings.TrimSpace(msg.Content)
    subCmd := strings.TrimSpace(strings.TrimPrefix(strings.ToLower(content), "/context"))

    switch {
    case subCmd == "" || subCmd == "info":
        return a.handleContextInfo(ctx, msg)
    case strings.HasPrefix(subCmd, "mode"):
        mode := strings.TrimSpace(strings.TrimPrefix(subCmd, "mode"))
        return a.handleContextMode(ctx, msg, mode)
    default:
        return &bus.OutboundMessage{
            Channel: msg.Channel,
            ChatID:  msg.ChatID,
            Content: "未知子命令。用法: /context [info|mode [phase1|phase2|none|default]]",
        }, nil
    }
}
```

模式切换处理：

```go
// handleContextMode 处理 /context mode 子命令
func (a *Agent) handleContextMode(ctx context.Context, msg bus.InboundMessage, modeStr string) (*bus.OutboundMessage, error) {
    cfg := a.contextManagerConfig

    if modeStr == "" {
        // 仅查询当前模式
        stats := a.GetContextManager().ContextInfo(nil, "", 0)
        overrideInfo := ""
        if stats.IsRuntimeOverride {
            overrideInfo = fmt.Sprintf("（运行时覆盖，默认为 %s）", stats.DefaultMode)
        }
        return &bus.OutboundMessage{
            Channel: msg.Channel,
            ChatID:  msg.ChatID,
            Content: fmt.Sprintf("当前上下文模式: %s %s", cfg.EffectiveMode(), overrideInfo),
        }, nil
    }

    target := ContextMode(modeStr)
    if target == "default" {
        cfg.ResetRuntimeMode()
        a.SetContextManager(NewContextManager(cfg))
        return &bus.OutboundMessage{
            Channel: msg.Channel,
            ChatID:  msg.ChatID,
            Content: fmt.Sprintf("已恢复默认上下文模式: %s", cfg.DefaultMode),
        }, nil
    }

    if !IsValidContextMode(target) {
        return &bus.OutboundMessage{
            Channel: msg.Channel,
            ChatID:  msg.ChatID,
            Content: "无效模式。可选: phase1, phase2, none, default",
        }, nil
    }

    // Phase 2 未实现时的额外提示
    extraMsg := ""
    if target == ContextModePhase2 {
        extraMsg = "（Phase 2 尚未实现，压缩时将自动降级到 Phase 1）"
    }

    // 原子操作：先设置配置，再替换 manager
    cfg.SetRuntimeMode(target)
    a.SetContextManager(NewContextManager(cfg))

    return &bus.OutboundMessage{
        Channel: msg.Channel,
        ChatID:  msg.ChatID,
        Content: fmt.Sprintf("已切换上下文模式: %s %s", target, extraMsg),
    }, nil
}
```

#### 2.7.3 engine.go 集成点修改

在 `RunConfig` 中新增字段：

```go
type RunConfig struct {
    // ... 现有字段 ...

    // AutoCompress 自动压缩配置（旧字段，向后兼容，步骤 7 删除）
    AutoCompress *CompressConfig

    // ContextManager 上下文管理器（新字段，优先级高于 AutoCompress）
    // 如果设置了此字段，AutoCompress 被忽略
    ContextManager ContextManager
}
```

**修改 `maybeCompress`**（`engine.go` Run() 内部）：

```go
// engine.go Run() 中修改 maybeCompress 闭包：
maybeCompress := func() {
    // 优先使用 ContextManager（新路径）
    if cm := cfg.ContextManager; cm != nil {
        toolDefs := cfg.Tools.AsDefinitionsForSession(sessionKey)
        toolTokens, _ := llm.CountToolsTokens(toolDefs, cfg.Model)

        if !cm.ShouldCompress(messages, cfg.Model, toolTokens) {
            return
        }

        // ... 通知逻辑不变 ...

        result, compressErr := cm.Compress(ctx, messages, cfg.LLMClient, cfg.Model)
        // ... 后续持久化逻辑不变 ...

        // 调用 SessionHook（如果有的话）
        if hook := cm.SessionHook(); hook != nil && cfg.Session != nil {
            hook.AfterPersist(ctx, cfg.Session, result)
        }
        return
    }

    // 回退路径：旧的 CompressFunc（向后兼容）
    cc := cfg.AutoCompress
    if cc == nil || len(messages) <= 3 {
        return
    }
    // ... 现有 maybeCompress 逻辑完全不变 ...
}
```

**修改输入超限强制压缩路径**（`engine.go` L264-299）：

```go
// 在输入超限强制压缩处，同样优先使用 ContextManager：
if cc := cfg.AutoCompress; cc != nil {
    // 旧路径（保持不变）
    result, compressErr := cc.CompressFunc(ctx, messages, cfg.LLMClient, cfg.Model)
    // ...
} else if cm := cfg.ContextManager; cm != nil {
    // 新路径：手动压缩（不检查阈值）
    result, compressErr := cm.ManualCompress(ctx, messages, cfg.LLMClient, cfg.Model)
    // ... 后续重试逻辑不变 ...
}
```

#### 2.7.4 handleCompress 适配

修改 `agent/compress.go` 中的 `handleCompress()`，保留外壳逻辑，只替换核心压缩调用：

```go
// handleCompress 处理 /compress 命令：手动触发上下文压缩
func (a *Agent) handleCompress(ctx context.Context, msg bus.InboundMessage, tenantSession *session.TenantSession) (*bus.OutboundMessage, error) {
    // 手动 /compress 始终可用，不受模式开关限制

    llmClient, model, _, _ := a.llmFactory.GetLLM(msg.SenderID)

    // buildPrompt、token 计数等外壳逻辑完全保留 ...
    messages, err := a.buildPrompt(ctx, msg, tenantSession)
    // ... 阈值检查、进度消息等保留不变 ...

    // 核心改动：通过 ContextManager.ManualCompress 执行
    cm := a.GetContextManager()
    result, err := cm.ManualCompress(ctx, messages, llmClient, model)
    // ... 后续 session 持久化逻辑完全保留不变 ...
}
```

#### 2.7.5 buildMainRunConfig / buildSubAgentRunConfig 适配

**`buildMainRunConfig()`**（`engine_wire.go`）：

```go
func (a *Agent) buildMainRunConfig(...) RunConfig {
    // ... 现有代码 ...
    cfg := RunConfig{ /* ... */ }

    // 新路径：注入 ContextManager
    cfg.ContextManager = a.GetContextManager()

    // 旧路径兼容（步骤 7 删除）：
    // if cfg.ContextManager == nil && a.enableAutoCompress { ... }

    return cfg
}
```

**`buildSubAgentRunConfig()`**（`engine_wire.go`）：

SubAgent **共用主 Agent 的 ContextManager 实例**（通过 `a.GetContextManager()` 获取）。
- 运行时 `/context mode` 切换对**后续新建的** SubAgent 生效
- 正在运行的 SubAgent 不受影响（它已持有 `RunConfig.ContextManager` 的引用）
- 这与现有行为一致：SubAgent 的 `AutoCompress` 也是构建时注入的，运行时不可变

```go
func (a *Agent) buildSubAgentRunConfig(...) RunConfig {
    // ... 现有代码 ...
    cfg := RunConfig{ /* ... */ }

    // SubAgent 共享主 Agent 的 ContextManager（caps.Memory=true 时）
    if caps.Memory && a.enableAutoCompress {
        cfg.ContextManager = a.GetContextManager()
    }
    // SubAgent 不需要独立的 ContextManager——它与主 Agent 共用同一套压缩配置
    // 未来如果需要 SubAgent 独立模式，可通过 caps.ContextMode 字段扩展

    return cfg
}
```

**`buildCronRunConfig()`**（`engine_wire.go`）：

无需修改。Cron 消息不使用自动压缩（现有行为），也不需要 ContextManager。

#### 2.7.6 /context info 输出增强

修改现有 `/context` 统计输出，增加模式信息：

```
📊 上下文 Token 统计

| 角色 | Token | 占比 |
|------|-------|------|
| System | 5000 | 5.0% |
| User | 40000 | 40.0% |
| Assistant | 15000 | 15.0% |
| Tool (消息) | 30000 | 30.0% |
| Tool (定义) | 10000 | 10.0% |
| **总计** | **100000** | 100% |

⚙️ 配置:
- 最大上下文: 100000 tokens
- 压缩阈值: 70000 tokens (70%)
- 当前模式: phase1（运行时覆盖，默认为 phase1）
```

---

## 三、实现步骤拆解

### 步骤 1：定义接口和配置（1 天）

| 任务 | 文件 | 说明 |
|------|------|------|
| 创建 `ContextManager` 接口、`ContextMode`、`ContextStats` 等 | `agent/context_manager.go` | §2.2 完整定义 |
| 创建 `ContextManagerConfig`（含 `sync.RWMutex`） | `agent/context_manager.go` | §2.3 并发安全配置 |
| 添加 `AGENT_CONTEXT_MODE` 到 `config.Load()` | `config/config.go` | §2.7.1，与 `AGENT_ENABLE_AUTO_COMPRESS` 同级 |
| 新增 `resolveContextMode()` | `agent/agent.go` | §2.7.1 向后兼容 |
| 新增 `GetContextManager()`/`SetContextManager()` | `agent/agent.go` | §2.4 并发安全读写 |

**验证**：`go build ./...` 编译通过

### 步骤 2：提取 compressMessages + 实现 Phase1Manager（1 天）

| 任务 | 文件 | 说明 |
|------|------|------|
| 将 `compressContext` 核心逻辑提取为 `compressMessages` 独立函数 | `agent/compress.go` | §2.5.1，消除 Agent 引用 |
| 保留 `compressContext()` 作为 `compressMessages()` 的 wrapper | `agent/compress.go` | 保持向后兼容 |
| 创建 `phase1Manager`，调用 `compressMessages` | `agent/context_manager_phase1.go` | §2.5.1 |
| 实现 `phase1Manager.ContextInfo()` | `agent/context_manager_phase1.go` | 从 `handleContext` 提取统计逻辑 |

**验证**：
- Phase1Manager 单元测试：`ShouldCompress` 边界值（messages<=3、恰好阈值、超阈值）
- `compressMessages` 行为与原 `compressContext` 完全一致（可用现有测试对比）

### 步骤 3：实现 noopManager、phase2Manager 和工厂（0.5 天）

| 任务 | 文件 | 说明 |
|------|------|------|
| 创建 `noopManager`（ManualCompress 降级到 Phase 1） | `agent/context_manager.go` | §2.5.3 |
| 创建 `phase2Manager` 空壳 | `agent/context_manager_phase2.go` | §2.5.2 |
| 实现 `NewContextManager` 工厂 | `agent/context_manager.go` | §2.6 |

**验证**：
- 工厂函数参数化测试：phase1/phase2/none/""/invalid 全覆盖
- `noopManager.ManualCompress()` 返回有效结果（不返回 error）
- `phase2Manager.Compress()` 返回 error，`ManualCompress()` 返回有效结果

### 步骤 4：Agent 集成（1 天）

| 任务 | 文件 | 说明 |
|------|------|------|
| 修改 `Agent` 结构体新增字段 | `agent/agent.go` | §2.4 |
| 修改 `Agent.New()` 初始化 ContextManager | `agent/agent.go` | 调用 `resolveContextMode()` + `NewContextManager()` |
| 修改 `buildMainRunConfig()` 注入 ContextManager | `agent/engine_wire.go` | §2.7.5 |
| 修改 `buildSubAgentRunConfig()` 注入 ContextManager | `agent/engine_wire.go` | §2.7.5 SubAgent 共用策略 |
| 确认 `buildCronRunConfig()` 无需修改 | `agent/engine_wire.go` | Cron 不使用压缩 |

**验证**：
- 现有行为不变（默认 phase1），所有现有测试通过
- `Agent.New()` 中 `resolveContextMode` 单元测试

### 步骤 5：改造 /context 命令（0.5 天）

| 任务 | 文件 | 说明 |
|------|------|------|
| 修改 `contextCmd.Match()` 支持子命令 | `agent/command_builtin.go` | §2.7.2 |
| 新增 `contextCmd.Concurrent() bool { return true }` | `agent/command_builtin.go` | §2.7.2 |
| 将 `handleContext()` 重命名为 `handleContextInfo()` | `agent/context_handler.go` | 输出增加模式信息 |
| 新增 `handleContextMode()` | `agent/context_handler.go` | §2.7.2 |
| 删除旧的 `/context` 注册中的方法引用 | `agent/command_builtin.go` | 确保只有一个注册入口 |

**验证**：手动测试全部子命令

### 步骤 6：engine.go maybeCompress 适配（1 天）

| 任务 | 文件 | 说明 |
|------|------|------|
| 修改 `RunConfig` 新增 `ContextManager` 字段 | `agent/engine.go` | §2.7.3 |
| 修改 `maybeCompress` 优先使用 ContextManager | `agent/engine.go` | §2.7.3 |
| 修改输入超限强制压缩路径 | `agent/engine.go` | §2.7.3 |
| 修改 `handleCompress` 通过 `ManualCompress` 执行 | `agent/compress.go` | §2.7.4 |

**验证**：
- 自动压缩仍正常触发
- `/compress` 手动命令在各模式下均正常工作
- `mode=none` 时不触发自动压缩，但 `/compress` 仍可用
- 输入超限时强制压缩仍正常工作

### 步骤 7：清理旧字段（0.5 天，可延后）

| 任务 | 文件 | 说明 |
|------|------|------|
| 删除 `enableAutoCompress`/`maxContextTokens`/`compressionThreshold` Agent 字段 | `agent/agent.go` | 由 `contextManagerConfig` 完全替代 |
| 删除 `Agent.Config` 中的旧字段 | `agent/agent.go` | 由 `ContextMode` 替代 |
| 删除 `CompressConfig` 和 `RunConfig.AutoCompress` | `agent/engine.go` | 由 `ContextManager` 替代 |
| 清理 engine_wire.go 中的旧兼容代码 | `agent/engine_wire.go` | 移除所有 `AutoCompress` 回退路径 |
| 删除 `compressContext()` Agent 方法 | `agent/compress.go` | 仅保留 `compressMessages()` 独立函数 |

**验证**：`go build ./...` 编译通过，全部测试通过

**合计工时：5.5 天**

---

## 四、验证标准

### 4.1 功能验证

| # | 场景 | 预期结果 | 验证方式 |
|---|------|---------|---------|
| 1 | 默认启动（无 AGENT_CONTEXT_MODE） | 使用 Phase 1 压缩，行为与现有完全一致 | 现有测试全部通过 |
| 2 | `AGENT_CONTEXT_MODE=none` 启动 | 禁用自动压缩，`/compress` 仍可用 | 手动测试 |
| 3 | `AGENT_CONTEXT_MODE=phase2` 启动 | 自动降级到 Phase 1，日志记录警告 | 检查日志 |
| 4 | `/context` | 显示统计信息 + 当前模式 | 手动测试 |
| 5 | `/context mode phase2` | 切换到 Phase 2，提示降级 | 手动测试 |
| 6 | `/context mode none` | 切换后不再自动压缩，`/compress` 仍可用 | 手动测试 |
| 7 | `/context mode default` | 恢复启动时配置的模式 | 手动测试 |
| 8 | `/compress` 手动压缩（mode=none 时） | 正常执行（降级到 Phase 1） | 手动测试 |
| 9 | SubAgent 自动压缩（mode=none 时） | SubAgent 不触发自动压缩 | 日志验证 |
| 10 | 运行时模式切换 + 新 SubAgent | 新 SubAgent 使用切换后的模式 | 日志验证 |

### 4.2 兼容性验证

| # | 场景 | 预期结果 |
|---|------|---------|
| 1 | `AGENT_ENABLE_AUTO_COMPRESS=false`，不设 `AGENT_CONTEXT_MODE` | 等价于 `CONTEXT_MODE=none` |
| 2 | 不设置任何新字段 | 默认 Phase 1，行为不变 |
| 3 | `/context` 无参数 | 输出格式与现有基本一致，增加模式行 |

### 4.3 单元测试计划

| 测试 | 文件 | 覆盖内容 |
|------|------|---------|
| `TestNewContextManager` | `context_manager_test.go` | phase1/phase2/none/""/invalid 五种输入 |
| `TestContextManagerConfig_EffectiveMode` | `context_manager_test.go` | RuntimeMode 优先级、ResetRuntimeMode |
| `TestPhase1Manager_ShouldCompress` | `context_manager_phase1_test.go` | 边界值：messages<=3、恰好阈值、超阈值 |
| `TestNoopManager` | `context_manager_test.go` | ShouldCompress=false、ManualCompress 返回有效结果 |
| `TestPhase2Manager_ManualCompress` | `context_manager_phase2_test.go` | Compress 返回 error、ManualCompress 降级成功 |
| `TestResolveContextMode` | `agent_test.go` | 新字段优先级、旧字段兼容、默认值 |
| `TestContextManagerConfig_Concurrent` | `context_manager_test.go` | 并发 SetRuntimeMode + EffectiveMode（race detector） |
| `TestGetSetContextManager_Concurrent` | `agent_test.go` | 并发 Get/Set（race detector） |

### 4.4 回滚方案

- 所有旧字段在步骤 1-6 中保留（双写），步骤 7 才删除
- 如果新架构有问题：在 `NewContextManager()` 中强制 `return newPhase1Manager(cfg)` 即可
- `AGENT_CONTEXT_MODE` 环境变量是纯新增，不影响现有部署
- `config.Load()` 中的新字段有默认值（空字符串），空值走旧兼容路径

---

## 五、风险与注意

| 风险 | 影响 | 缓解措施 |
|------|------|---------|
| 运行时模式切换并发安全 | `/context mode` (Concurrent=true) 和 `maybeCompress` 并发读写 | `ContextManagerConfig` 用 `sync.RWMutex`；`Agent.contextManager` 用独立的 `sync.RWMutex` + `Get/Set` 方法；运行 `go test -race` 验证 |
| Phase 2 降级时的用户体验 | 用户切到 Phase 2 期望新功能，实际仍是 Phase 1 | `/context mode phase2` 时明确提示降级；`phase2Manager.Compress` 返回 error 时 `maybeCompress` 降级到 `ManualCompress` |
| /context 命令改造向后兼容 | 现有 `/context` 输出可能变化 | `/context` 无参数输出格式保持不变，仅末尾增加一行模式信息 |
| SubAgent 模式继承 | SubAgent 是否应继承父 Agent 模式 | SubAgent 共用主 Agent 的 `ContextManager` 实例（构建时注入），运行时切换影响后续新建的 SubAgent，不影响正在运行的 SubAgent |

---

## 附录 A：v1 审核意见与修改对照

| # | 门下省意见 | v2 修改 |
|---|-----------|---------|
| 1 | SubAgent ContextManager 策略未明确 | §2.7.5 明确：共用主 Agent 实例，构建时注入，运行时切换不影响正在运行的 SubAgent |
| 2 | `/compress` 在 `mode=none` 时行为与现有语义冲突 | §2.5.3 `noopManager.ManualCompress` 降级到 Phase 1，`/compress` 始终可用 |
| 3 | 并发安全仅提及风险无具体实现 | §2.3 `ContextManagerConfig` 用 `sync.RWMutex`；§2.4 `Agent` 新增 `contextManagerMu` + `Get/Set` 方法；§4.3 新增 race detector 测试 |
| 4 | `config.go` 集成方式与现有 `Load()` 模式不一致 | §2.7.1 改为在 `config.Load()` 中新增 `AGENT_CONTEXT_MODE`，与 `AGENT_ENABLE_AUTO_COMPRESS` 同级 |
| 5 | 建议消除 `phase1Manager` 对 `*Agent` 的引用 | §2.5.1 提取 `compressMessages` 独立函数，`phase1Manager` 不再持有 `*Agent` |
| 6 | 缺少单元测试计划 | §4.3 新增 8 项单元测试覆盖工厂、配置、边界值、并发安全 |
| 额外 | 工时估算偏低 | §三 工时从 3 天调整为 5.5 天 |
| 额外 | `buildCronRunConfig` 未提及 | §2.7.5 明确：Cron 不使用压缩，无需修改 |
| 额外 | `handleCompress` 适配细节不足 | §2.7.4 详细说明：保留外壳逻辑，仅替换核心压缩调用 |
