# 统一 Agent 架构设计文档

> 合并 Issue #127（Agent 架构统一）与 Issue #119（SubAgent 记忆隔离）的完整设计。
> 核心理念：**InboundMessage / OutboundMessage 作为统一通信协议**，覆盖 IM↔Agent 和 Agent↔Agent 两种通信场景。

## 1. 现状分析

### 1.1 核心问题

| 维度 | 主 Agent | SubAgent | 差异影响 |
|------|----------|----------|----------|
| **消息循环** | `runLoop()` 366 行 | `RunSubAgent()` 135 行 | ~60-70% 重复代码 |
| **通信协议** | `bus.InboundMessage` → `bus.OutboundMessage` | 函数调用 `(task string) → (string, error)` | 两套完全不同的通信方式 |
| **工具集** | `Registry` 全量（含 session MCP、激活/过期） | `Clone()` 后去掉 SubAgent，无 session MCP | SubAgent 能力被阉割 |
| **ToolContext** | 30+ 字段（SendFunc、Memory、Registry 等） | ~10 字段（仅文件系统） | SubAgent 不能发消息、不能访问记忆 |
| **System Prompt** | `pipeline.Run()` 构建（含 memory、skills、agents） | 角色文件 SystemPrompt + 工作目录 | SubAgent 无记忆注入 |
| **会话管理** | `TenantSession` 持久化 | 纯内存，无持久化 | SubAgent 无法跨任务积累经验 |
| **LLM 选择** | `llmFactory.GetLLM(senderID)` 支持用户自定义 | 固定 `a.llmClient` / `a.model` | SubAgent 不尊重用户 LLM 配置 |
| **并发控制** | 信号量 + per-request cancel | 父 context 传递 | SubAgent 无独立取消 |
| **进度通知** | 有（sendMessage patch 更新） | 无 | SubAgent 长时间运行时用户无感知 |
| **自动压缩** | 有 | 无 | SubAgent 长任务可能 OOM |

### 1.2 通信协议现状

当前 `InboundMessage` / `OutboundMessage` 仅用于 **IM 渠道 ↔ Agent** 通信：

```
IM 渠道 ──InboundMessage──→ MessageBus ──→ Agent.processMessage()
Agent ──OutboundMessage──→ MessageBus ──→ IM 渠道.Send()
```

而 **Agent ↔ SubAgent** 通信是完全不同的函数调用：

```go
// 当前：SubAgent 通过函数调用，返回纯 string
result, err := ctx.Manager.RunSubAgent(parentCtx, task, systemPrompt, allowedTools)
// result 是 string，丢失了所有元信息（channel、sender、media 等）
```

**问题**：
1. SubAgent 的输入只有 `task string`，没有 sender 信息、渠道信息、媒体附件
2. SubAgent 的输出只有 `string`，不能携带媒体、不能指定回复目标
3. 两套通信方式导致 `processMessage()` 和 `RunSubAgent()` 无法复用

### 1.3 Tenant 隔离现状

当前 tenant 按 `(channel, chatID)` 隔离：
- 私聊 chatID = `ou_xxx`（用户 open_id）
- 群聊 chatID = `oc_xxx`（群 chat_id）

已修复的隔离方案：
- **persona**: `tenantID=0`（全局唯一）
- **human**: `tenantID=0, userID=ou_xxx`（跨 tenant 按用户隔离）
- **working_context**: 按 `tenantID` 隔离（每个会话独立）

SubAgent 目前**完全没有记忆**，这是 Issue #119 要解决的核心问题。

### 1.4 代码量化

```
agent/agent.go:     2459 行, 40 个方法
  - runLoop():       366 行（主循环）
  - RunSubAgent():   135 行（SubAgent 循环，~70% 与 runLoop 重复）
  - executeTool():    95 行（工具执行，主 Agent 专属）
  - processMessage(): 150 行（消息处理入口）
  - buildPrompt():    60 行（prompt 构建）
```

## 2. 目标架构

### 2.1 核心思想

**两个统一**：
1. **统一通信协议** — `InboundMessage` / `OutboundMessage` 既是 IM↔Agent 的消息载体，也是 Agent↔Agent 的通信协议
2. **统一运行时** — Agent 就是 Agent，没有主从之分，差异通过配置注入

```
                    ┌──────────────────────────────────────────┐
                    │           Unified Message Protocol       │
                    │   InboundMessage ←→ OutboundMessage      │
                    └──────────────┬───────────────────────────┘
                                   │
              ┌────────────────────┼────────────────────┐
              │                    │                     │
     IM Channel → Agent      Agent → SubAgent      SubAgent → Agent
     (feishu/qq/cli)         (spawn)               (reply)
              │                    │                     │
              ▼                    ▼                     ▼
         InboundMessage       InboundMessage        OutboundMessage
         (channel=feishu)     (channel=agent)       (channel=agent)

                    ┌──────────────┐
                    │  AgentEngine │  ← 统一的 Agent 运行时
                    │  (engine.go) │
                    └──────┬───────┘
                           │ RunConfig 配置注入
              ┌────────────┼────────────┐
              ▼            ▼            ▼
         Agent "main"  Agent "cr"   Agent "deploy"
         (飞书绑定)    (code-review) (部署专用)
              │
              │ 每个 Agent 实例都有（按需配置）：
              ├── LLM client + model
              ├── Tools (Registry 或子集)
              ├── Session (可选，持久化历史)
              ├── Memory (可选，core/archival)
              ├── Pipeline (可选，system prompt 构建)
              ├── SendFunc (可选，能直接发消息)
              ├── ProgressNotifier (可选，进度通知)
              └── 可以 spawn 其他 Agent
```

### 2.2 统一消息协议设计

#### 2.2.1 InboundMessage 扩展

```go
// bus/bus.go

// InboundMessage 统一的入站消息。
// 来源可以是 IM 渠道（feishu/qq/cli）或其他 Agent（agent 内部调用）。
type InboundMessage struct {
    // === 路由 ===
    Channel    string            // 消息来源: "feishu", "qq", "cli", "agent"
    SenderID   string            // 发送者标识（IM 用户 ID 或父 Agent ID）
    SenderName string            // 发送者姓名
    ChatID     string            // 会话标识（IM 群组 ID 或 Agent 会话 ID）
    ChatType   string            // "p2p" / "group" / "agent"

    // === 内容 ===
    Content    string            // 消息文本
    Media      []string          // 媒体文件路径/URL

    // === 元数据 ===
    Metadata   map[string]string // 渠道/调用方特定元数据
    Time       time.Time
    RequestID  string            // 请求追踪 ID

    // === 调度标记 ===
    IsCron     bool              // 是否由 cron 定时任务触发

    // === Agent 间通信扩展 ===
    ParentAgentID string         // 父 Agent ID（仅 channel="agent" 时有值）
    SystemPrompt  string         // 覆盖 system prompt（仅 channel="agent" 时有值）
    AllowedTools  []string       // 工具白名单（仅 channel="agent" 时有值，空=全部）
    RoleName      string         // SubAgent 角色名（仅 channel="agent" 时有值）
}

// IsFromAgent 判断消息是否来自其他 Agent（而非 IM 渠道）
func (m *InboundMessage) IsFromAgent() bool {
    return m.Channel == "agent"
}

// OriginChannel 获取原始 IM 渠道（Agent 间调用时从 Metadata 继承）
func (m *InboundMessage) OriginChannel() string {
    if m.Channel == "agent" {
        if ch, ok := m.Metadata["origin_channel"]; ok {
            return ch
        }
    }
    return m.Channel
}

// OriginChatID 获取原始 IM 会话 ID
func (m *InboundMessage) OriginChatID() string {
    if m.Channel == "agent" {
        if id, ok := m.Metadata["origin_chat_id"]; ok {
            return id
        }
    }
    return m.ChatID
}

// OriginSenderID 获取原始 IM 发送者 ID
func (m *InboundMessage) OriginSenderID() string {
    if m.Channel == "agent" {
        if id, ok := m.Metadata["origin_sender"]; ok {
            return id
        }
    }
    return m.SenderID
}
```

#### 2.2.2 OutboundMessage 扩展

```go
// OutboundMessage 统一的出站消息。
// 目标可以是 IM 渠道或调用方 Agent。
type OutboundMessage struct {
    // === 路由 ===
    Channel  string            // 目标渠道: "feishu", "qq", "agent"
    ChatID   string            // 目标会话

    // === 内容 ===
    Content  string            // 消息文本
    Media    []string          // 附件文件路径

    // === 元数据 ===
    Metadata map[string]string // 附加元数据

    // === Agent 间通信扩展 ===
    ToolsUsed   []string       // 使用过的工具列表（SubAgent 返回时携带）
    WaitingUser bool           // 是否等待用户响应
    Error       error          // 执行错误（SubAgent 返回时携带）
}
```

#### 2.2.3 通信流程对比

**改造前**：

```
IM → InboundMessage → processMessage() → runLoop(ctx, messages, channel, chatID, ...) → string
                                              ↓
                                         RunSubAgent(parentCtx, task, prompt, tools) → string
```

**改造后**：

```
IM → InboundMessage → Engine.Run(cfg) → OutboundMessage
                          ↓
                     SubAgent 调用也构造 InboundMessage:
                     InboundMessage{
                         Channel:       "agent",
                         Content:       task,
                         SenderID:      parentAgentID,
                         ParentAgentID: parentAgentID,
                         SystemPrompt:  rolePrompt,
                         AllowedTools:  role.Tools,
                         RoleName:      "code-reviewer",
                         Metadata: map[string]string{
                             "origin_channel": parentMsg.Channel,
                             "origin_chat_id": parentMsg.ChatID,
                             "origin_sender":  parentMsg.SenderID,
                         },
                     }
                          ↓
                     Engine.Run(cfg) → OutboundMessage{Content: result, ToolsUsed: [...]}
```

### 2.3 SubAgent 记忆模型

```
用户请求 → 主 Agent (tenant: feishu:oc_xxx)
              │
              ├─ Persona:  tenantID=0, userID=""     (全局唯一)
              ├─ Human:    tenantID=0, userID=ou_xxx  (跨 tenant 按用户)
              └─ Working:  tenantID=N                 (按会话)
              │
              ▼ SubAgent("code-reviewer", task)

SubAgent "code-reviewer":
  ├─ Persona:  tenantID=0, userID="", subagent="code-reviewer"  (全局唯一)
  ├─ Human:    继承调用者的 human block（只读引用，不独立存储）
  ├─ Working:  不持久化（每次任务清空）
  └─ Archival: collection="subagent_code-reviewer" (全局共享，独立 collection)
```

**关键决策**：
1. SubAgent 的 **persona** 独立于主 Agent（每个角色有自己的身份认知）
2. SubAgent 的 **human** 继承调用者（code-reviewer 需要知道它在为谁工作）
3. SubAgent 的 **working_context** 不持久化（任务完成即清空，避免跨任务污染）
4. SubAgent 的 **archival memory** 全局共享（code-reviewer 的经验所有用户共享）

## 3. 详细设计

### 3.1 RunConfig — 统一的运行配置

`RunConfig` 从 `InboundMessage` 中提取路由信息，不再散落为 8 个独立参数：

```go
// agent/engine.go

// RunConfig 统一的 Agent 运行配置。
// 主 Agent 和 SubAgent 使用同一个 Run() 方法，差异通过配置注入。
type RunConfig struct {
    // === 必需 ===
    LLMClient llm.LLM
    Model     string
    Tools     *tools.Registry
    Messages  []llm.ChatMessage

    // === 身份（从 InboundMessage 提取） ===
    AgentID    string // "main", "main/code-reviewer"
    Channel    string // 原始 IM 渠道（用于 ToolContext）
    ChatID     string // 原始 IM 会话
    SenderID   string // 原始发送者
    SenderName string

    // === 循环控制 ===
    MaxIterations int // 0 = 使用默认值 100

    // === 可选能力（nil = 不启用） ===

    // Session 持久化（nil = 纯内存，不持久化）
    Session *session.TenantSession

    // SessionKey 工具激活的 session key（为空时从 Channel+ChatID 生成）
    SessionKey string

    // ProgressNotifier 进度通知回调（nil = 不通知）
    ProgressNotifier func(lines []string)

    // AutoCompress 自动上下文压缩配置（nil = 不压缩）
    AutoCompress *CompressConfig

    // SendFunc 向 IM 渠道发送消息（nil = 不能发消息）
    SendFunc func(channel, chatID, content string) error

    // Memory 记忆提供者（nil = 无记忆）
    Memory memory.MemoryProvider

    // ToolContextExtras 额外的 ToolContext 字段注入
    ToolContextExtras *ToolContextExtras

    // SpawnAgent SubAgent 创建能力（nil = 不能创建子 Agent）
    // 输入输出都是统一消息：InboundMessage → OutboundMessage
    SpawnAgent func(ctx context.Context, msg bus.InboundMessage) (*bus.OutboundMessage, error)

    // OAuthHandler OAuth 自动触发处理器（nil = 不处理 OAuth）
    OAuthHandler func(ctx context.Context, tc llm.ToolCall, execErr error) (content string, handled bool)
}

// CompressConfig 自动压缩配置
type CompressConfig struct {
    MaxContextTokens     int
    CompressionThreshold float64
    CompressFunc         func(ctx context.Context, messages []llm.ChatMessage, model string) ([]llm.ChatMessage, error)
}

// ToolContextExtras Letta 记忆相关的 ToolContext 扩展字段
type ToolContextExtras struct {
    TenantID              int64
    CoreMemory            *sqlite.CoreMemoryService
    ArchivalMemory        *vectordb.ArchivalService
    MemorySvc             *sqlite.MemoryService
    RecallTimeRange       vectordb.RecallTimeRangeFunc
    ToolIndexer           memory.ToolIndexer
    InjectInbound         func(channel, chatID, senderID, content string)
    Registry              *tools.Registry
    InvalidateAllSessionMCP func()
}
```

### 3.2 AgentEngine — 统一的运行时

```go
// agent/engine.go（新文件，从 agent.go 提取）

// Run 统一的 Agent 循环。
// 输入：RunConfig（从 InboundMessage 构建）
// 输出：OutboundMessage（可直接发送到 IM 或返回给父 Agent）
func Run(ctx context.Context, cfg RunConfig) *bus.OutboundMessage {
    maxIter := cfg.MaxIterations
    if maxIter == 0 {
        maxIter = 100
    }
    sessionKey := cfg.SessionKey
    if sessionKey == "" && cfg.Channel != "" {
        sessionKey = cfg.Channel + ":" + cfg.ChatID
    }
    messages := cfg.Messages

    var toolsUsed []string
    var waitingUser bool
    var progressLines []string

    // 进度通知
    notifyProgress := func(extra string) {
        if cfg.ProgressNotifier == nil {
            return
        }
        lines := progressLines
        if extra != "" {
            lines = append(append([]string{}, progressLines...), extra)
        }
        cfg.ProgressNotifier(lines)
    }

    // 自动压缩
    maybeCompress := func() {
        if cfg.AutoCompress == nil || len(messages) <= 3 {
            return
        }
        // ... 与当前 runLoop 中的 maybeCompress 逻辑相同
    }

    // 工具激活 tick
    if sessionKey != "" {
        cfg.Tools.TickSession(sessionKey)
    }

    // 主循环
    for i := 0; i < maxIter; i++ {
        maybeCompress()

        if cfg.ProgressNotifier != nil && i > 0 {
            notifyProgress("> 💭 思考中...")
        }

        toolDefs := cfg.Tools.AsDefinitionsForSession(sessionKey)
        response, err := cfg.LLMClient.Generate(ctx, cfg.Model, messages, toolDefs)
        if err != nil {
            if ctx.Err() != nil {
                return &bus.OutboundMessage{
                    Channel: cfg.Channel, ChatID: cfg.ChatID,
                    Content: "Agent was cancelled.", Error: ctx.Err(),
                    ToolsUsed: toolsUsed,
                }
            }
            return &bus.OutboundMessage{
                Channel: cfg.Channel, ChatID: cfg.ChatID,
                Error: fmt.Errorf("%w: %w", ErrLLMGenerate, err),
                ToolsUsed: toolsUsed,
            }
        }

        if !response.HasToolCalls() {
            return &bus.OutboundMessage{
                Channel: cfg.Channel, ChatID: cfg.ChatID,
                Content:     llm.StripThinkBlocks(response.Content),
                ToolsUsed:   toolsUsed,
                WaitingUser: waitingUser,
            }
        }

        // ... tool call 处理（读写分离并行、OAuth、进度通知）
        // 与当前 runLoop 逻辑相同，但通过 cfg 字段控制差异
    }

    return &bus.OutboundMessage{
        Channel: cfg.Channel, ChatID: cfg.ChatID,
        Content:   "已达到最大迭代次数，请重新描述你的需求。",
        ToolsUsed: toolsUsed,
    }
}

// buildToolContext 统一构建 ToolContext
func buildToolContext(ctx context.Context, cfg *RunConfig) *tools.ToolContext {
    tc := &tools.ToolContext{
        Ctx:        ctx,
        AgentID:    cfg.AgentID,
        Channel:    cfg.Channel,
        ChatID:     cfg.ChatID,
        SenderID:   cfg.SenderID,
        SenderName: cfg.SenderName,
        SendFunc:   cfg.SendFunc,
    }

    // 注入 SpawnAgent（包装为 SubAgentManager 接口）
    if cfg.SpawnAgent != nil {
        tc.Manager = &spawnAgentAdapter{
            spawnFn:  cfg.SpawnAgent,
            parentID: cfg.AgentID,
            channel:  cfg.Channel,
            chatID:   cfg.ChatID,
            senderID: cfg.SenderID,
        }
    }

    // 注入 Letta 记忆字段
    if ext := cfg.ToolContextExtras; ext != nil {
        tc.TenantID = ext.TenantID
        tc.CoreMemory = ext.CoreMemory
        tc.ArchivalMemory = ext.ArchivalMemory
        tc.MemorySvc = ext.MemorySvc
        tc.RecallTimeRange = ext.RecallTimeRange
        tc.ToolIndexer = ext.ToolIndexer
        tc.InjectInbound = ext.InjectInbound
        tc.Registry = ext.Registry
        tc.InvalidateAllSessionMCP = ext.InvalidateAllSessionMCP
    }

    return tc
}

// spawnAgentAdapter 将 SpawnAgent 函数适配为 SubAgentManager 接口。
// 核心职责：将 (task, prompt, tools) 函数签名转换为统一的 InboundMessage。
type spawnAgentAdapter struct {
    spawnFn  func(ctx context.Context, msg bus.InboundMessage) (*bus.OutboundMessage, error)
    parentID string
    channel  string
    chatID   string
    senderID string
}

func (a *spawnAgentAdapter) RunSubAgent(parentCtx *tools.ToolContext, task string, systemPrompt string, allowedTools []string) (string, error) {
    // 构造统一的 InboundMessage
    msg := bus.InboundMessage{
        Channel:       "agent",
        Content:       task,
        SenderID:      parentCtx.SenderID,
        SenderName:    parentCtx.SenderName,
        ChatID:        parentCtx.ChatID,
        ChatType:      "agent",
        ParentAgentID: a.parentID,
        SystemPrompt:  systemPrompt,
        AllowedTools:  allowedTools,
        Time:          time.Now(),
        Metadata: map[string]string{
            "origin_channel": a.channel,
            "origin_chat_id": a.chatID,
            "origin_sender":  a.senderID,
        },
    }

    out, err := a.spawnFn(parentCtx.Ctx, msg)
    if err != nil {
        return "", err
    }
    if out.Error != nil {
        return out.Content, out.Error
    }
    return out.Content, nil
}
```

### 3.3 主 Agent 适配（processMessage → 统一消息流）

```go
// agent/agent.go

func (a *Agent) processMessage(ctx context.Context, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
    // ... 现有的前置逻辑（requestID、session、command 匹配等）不变

    // 构建 LLM 消息
    messages, err := a.buildPrompt(ctx, msg, tenantSession)
    if err != nil {
        return nil, err
    }

    // 构建 RunConfig（从 InboundMessage 提取路由信息）
    llmClient, model := a.llmFactory.GetLLM(msg.SenderID)
    sessionKey := msg.Channel + ":" + msg.ChatID

    cfg := RunConfig{
        LLMClient:     llmClient,
        Model:         model,
        Tools:         a.tools,
        Messages:      messages,
        AgentID:       "main",
        Channel:       msg.Channel,
        ChatID:        msg.ChatID,
        SenderID:      msg.SenderID,
        SenderName:    msg.SenderName,
        MaxIterations: a.maxIterations,
        Session:       tenantSession,
        SessionKey:    sessionKey,
        SendFunc:      a.sendMessage,
        ToolContextExtras: a.buildToolContextExtras(ctx, msg, tenantSession),
    }

    // SpawnAgent：接收 InboundMessage，返回 OutboundMessage
    cfg.SpawnAgent = func(ctx context.Context, subMsg bus.InboundMessage) (*bus.OutboundMessage, error) {
        return a.handleSubAgentMessage(ctx, subMsg)
    }

    // 进度通知
    if preReplyNotify {
        cfg.ProgressNotifier = func(lines []string) {
            _ = a.sendMessage(msg.Channel, msg.ChatID, formatProgressLines(lines))
        }
    }

    // 自动压缩
    if a.enableAutoCompress {
        cfg.AutoCompress = &CompressConfig{
            MaxContextTokens:     a.maxContextTokens,
            CompressionThreshold: a.compressionThreshold,
            CompressFunc:         a.compressContext,
        }
    }

    // 运行统一引擎
    out := Run(ctx, cfg)

    // 后处理（保存会话、发送回复、添加 reaction）
    // ... 从 out.Content / out.ToolsUsed / out.WaitingUser / out.Error 提取结果
    return out, out.Error
}
```

### 3.4 SubAgent 适配（统一消息入口）

```go
// agent/agent.go

// handleSubAgentMessage 处理来自其他 Agent 的消息（统一入口）
func (a *Agent) handleSubAgentMessage(ctx context.Context, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
    // 1. 从 InboundMessage 提取 SubAgent 配置
    roleName := msg.RoleName
    parentAgentID := msg.ParentAgentID
    task := msg.Content
    systemPrompt := msg.SystemPrompt
    allowedTools := msg.AllowedTools

    // 2. 工具集准备
    subTools := a.tools.Clone()
    subTools.Unregister("SubAgent")
    if len(allowedTools) > 0 {
        allowed := make(map[string]bool, len(allowedTools))
        for _, name := range allowedTools {
            allowed[name] = true
        }
        for _, tool := range subTools.List() {
            if !allowed[tool.Name()] {
                subTools.Unregister(tool.Name())
            }
        }
    }

    // 3. 构建消息
    if wd := msg.Metadata["workspace_root"]; wd != "" {
        systemPrompt += fmt.Sprintf("\n\nWorking directory: %s\n", wd)
    }
    messages := []llm.ChatMessage{
        llm.NewSystemMessage(systemPrompt),
        llm.NewUserMessage(task),
    }

    // 4. 记忆注入（Phase 2 新增）
    var memoryProvider memory.MemoryProvider
    var toolExtras *ToolContextExtras
    // if role.Memory { memoryProvider, toolExtras = a.createSubAgentMemory(roleName, msg) }

    // 5. 从 InboundMessage 继承原始 IM 信息
    originChannel := msg.OriginChannel()
    originChatID := msg.OriginChatID()
    originSender := msg.OriginSenderID()

    // 6. 构建 RunConfig
    cfg := RunConfig{
        LLMClient:     a.llmClient,
        Model:         a.model,
        Tools:         subTools,
        Messages:      messages,
        AgentID:       parentAgentID + "/" + roleName,
        Channel:       originChannel,
        ChatID:        originChatID,
        SenderID:      originSender,
        SenderName:    msg.SenderName,
        MaxIterations: 100,
        Memory:        memoryProvider,
        ToolContextExtras: toolExtras,
    }

    log.WithFields(log.Fields{
        "parent": parentAgentID,
        "role":   roleName,
        "task":   tools.Truncate(task, 80),
    }).Info("SubAgent started via unified message")

    // 7. 运行统一引擎
    out := Run(ctx, cfg)

    log.WithFields(log.Fields{
        "parent": parentAgentID,
        "role":   roleName,
        "tools":  out.ToolsUsed,
    }).Info("SubAgent completed")

    return out, nil
}
```

### 3.5 SubAgentTool 适配

```go
// tools/subagent.go — 零改动

func (t *SubAgentTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
    // ... 解析参数、查找角色定义（不变）

    // SubAgentManager.RunSubAgent 签名不变
    // spawnAgentAdapter 内部完成 (task, prompt, tools) → InboundMessage → OutboundMessage → string
    result, err := ctx.Manager.RunSubAgent(ctx, params.Task, role.SystemPrompt, role.AllowedTools)
    if err != nil {
        return NewResult(fmt.Sprintf("Sub-agent error: %v", err)), nil
    }
    return NewResult(result), nil
}
```

**关键**：`SubAgentManager` 接口签名不变，`spawnAgentAdapter` 内部完成转换。`SubAgentTool` 零改动。

### 3.6 SubAgent 记忆隔离实现（Phase 2）

#### 3.6.1 数据库 Schema 扩展

`core_memory_blocks` 表新增 `subagent_id` 列：

```sql
-- 现有 schema
CREATE TABLE core_memory_blocks (
    tenant_id  INTEGER NOT NULL,
    block_name TEXT NOT NULL,
    user_id    TEXT NOT NULL DEFAULT '',
    content    TEXT NOT NULL DEFAULT '',
    char_limit INTEGER NOT NULL DEFAULT 2000,
    PRIMARY KEY (tenant_id, block_name, user_id)
);

-- 迁移：重建表以更新 PRIMARY KEY（SQLite 不支持 ALTER PRIMARY KEY）
CREATE TABLE core_memory_blocks_new (
    tenant_id   INTEGER NOT NULL,
    block_name  TEXT NOT NULL,
    user_id     TEXT NOT NULL DEFAULT '',
    subagent_id TEXT NOT NULL DEFAULT '',
    content     TEXT NOT NULL DEFAULT '',
    char_limit  INTEGER NOT NULL DEFAULT 2000,
    PRIMARY KEY (tenant_id, block_name, user_id, subagent_id)
);
INSERT INTO core_memory_blocks_new (tenant_id, block_name, user_id, content, char_limit)
    SELECT tenant_id, block_name, user_id, content, char_limit FROM core_memory_blocks;
DROP TABLE core_memory_blocks;
ALTER TABLE core_memory_blocks_new RENAME TO core_memory_blocks;
```

SubAgent 记忆存储规则：

| Block | tenant_id | user_id | subagent_id | 说明 |
|-------|-----------|---------|-------------|------|
| persona (主) | 0 | "" | "" | 全局唯一 |
| persona (SubAgent) | 0 | "" | "code-reviewer" | 每角色唯一 |
| human | 0 | "ou_xxx" | "" | 跨 tenant 按用户（SubAgent 继承，不独立存储） |
| working_context (主) | N | "" | "" | 按会话 |
| working_context (SubAgent) | — | — | — | **不持久化** |

#### 3.6.2 Archival Memory 隔离

SubAgent 使用独立的向量 collection：

```go
// 主 Agent archival collection: "tenant_{tenantID}"（现有）
// SubAgent archival collection: "subagent_{role}"（新增，全局共享）

func archivalCollectionName(agentID string, tenantID int64) string {
    if strings.Contains(agentID, "/") {
        parts := strings.Split(agentID, "/")
        role := parts[len(parts)-1]
        if role == "sub" && len(parts) >= 2 {
            role = parts[len(parts)-2]
        }
        return "subagent_" + role
    }
    return fmt.Sprintf("tenant_%d", tenantID)
}
```

#### 3.6.3 SubAgent 记忆创建流程

```go
func (a *Agent) createSubAgentMemory(role string, msg bus.InboundMessage) (memory.MemoryProvider, *ToolContextExtras) {
    // 1. 获取或创建 SubAgent persona
    persona, _, _ := a.coreSvc.GetBlockWithSubAgent(0, "persona", "", role)
    if persona == "" {
        roleDef, _ := tools.GetSubAgentRole(role)
        if roleDef != nil {
            persona = fmt.Sprintf("I am %s. %s", role, roleDef.Description)
            a.coreSvc.SetBlockWithSubAgent(0, "persona", persona, "", role)
        }
    }

    // 2. 继承调用者的 human block（只读快照）
    originSender := msg.OriginSenderID()
    human, _, _ := a.coreSvc.GetBlock(0, "human", originSender, "")

    // 3. 创建 SubAgent 专用的 ArchivalService
    collectionName := "subagent_" + role
    archival := a.archivalSvc.ForCollection(collectionName)

    // 4. 构建 SubAgentMemory
    mem := letta.NewSubAgentMemory(persona, human, archival)

    // 5. 构建 ToolContextExtras
    extras := &ToolContextExtras{
        TenantID:       0,
        CoreMemory:     a.coreSvc,
        ArchivalMemory: archival,
    }

    return mem, extras
}
```

### 3.7 调用链追踪与防递归

```go
// agent/call_chain.go

type callChainKey struct{}

// CallChain 调用链上下文
type CallChain struct {
    Chain []string // ["main", "main/code-reviewer"]
}

const MaxSubAgentDepth = 3

func CallChainFromContext(ctx context.Context) *CallChain {
    if cc, ok := ctx.Value(callChainKey{}).(*CallChain); ok {
        return cc
    }
    return &CallChain{Chain: []string{"main"}}
}

func WithCallChain(ctx context.Context, cc *CallChain) context.Context {
    return context.WithValue(ctx, callChainKey{}, cc)
}

func (cc *CallChain) CanSpawn(targetRole string) error {
    if len(cc.Chain) >= MaxSubAgentDepth {
        return fmt.Errorf("max SubAgent depth %d reached (chain: %v)", MaxSubAgentDepth, cc.Chain)
    }
    currentID := cc.Chain[len(cc.Chain)-1]
    targetID := currentID + "/" + targetRole
    for _, id := range cc.Chain {
        if id == targetID {
            return fmt.Errorf("circular SubAgent call: %s already in chain %v", targetID, cc.Chain)
        }
    }
    return nil
}

func (cc *CallChain) Spawn(targetRole string) *CallChain {
    currentID := cc.Chain[len(cc.Chain)-1]
    newChain := make([]string, len(cc.Chain)+1)
    copy(newChain, cc.Chain)
    newChain[len(cc.Chain)] = currentID + "/" + targetRole
    return &CallChain{Chain: newChain}
}
```

### 3.8 角色定义增强

`.xbot/agents/code-reviewer.md` frontmatter 扩展：

```yaml
---
name: code-reviewer
description: "Code review specialist"
tools:
  - Shell
  - Read
  - Grep
  - Glob
  - Fetch
  - WebSearch
# === 新增字段（Phase 2+） ===
memory: true              # 启用记忆（persona + archival）
send_message: false       # 不能直接发消息（默认 false）
max_iterations: 50        # 自定义迭代上限（默认 100）
---
```

`SubAgentRole` 结构体扩展：

```go
type SubAgentRole struct {
    Name          string
    Description   string
    SystemPrompt  string
    AllowedTools  []string
    // Phase 2 新增
    Memory        bool // 启用记忆
    SendMessage   bool // 能直接发消息
    MaxIterations int  // 自定义迭代上限（0 = 默认 100）
}
```

## 4. 迁移路径（渐进式）

### Phase 0: 补充 runLoop 集成测试（前置条件）

**目标**：为 Phase 1 重构建立安全网。

**新增文件**：`agent/engine_test.go`

**测试用例**：
- `TestRun_BasicConversation` — 无 tool call，直接返回 OutboundMessage
- `TestRun_SingleToolCall` — 一次 tool call + 最终回复
- `TestRun_MultiToolCall` — 多次 tool call 循环
- `TestRun_MaxIterations` — 达到最大迭代次数
- `TestRun_ProgressNotification` — 进度通知回调被正确调用
- `TestRun_AutoCompress` — token 超阈值时触发压缩
- `TestRun_ReadWriteSplit` — 只读工具并行、写工具串行
- `TestRun_ContextCancellation` — context 取消时优雅退出
- `TestRun_LLMError_GracefulDegradation` — LLM 错误时返回 OutboundMessage.Error
- `TestRun_WaitingUser` — 工具标记 WaitingUser 时停止循环
- `TestRun_SubAgentViaMessage` — SubAgent 通过 InboundMessage 调用

**预估**：1-2 天

### Phase 1: 统一消息协议 + 提取 AgentEngine（核心重构）

**目标**：
1. 扩展 `InboundMessage` / `OutboundMessage` 为统一通信协议
2. 提取 `Run()` 函数，消除 `runLoop` 和 `RunSubAgent` 的代码重复
3. SubAgent 调用改为消息传递

**文件变更**：

| 文件 | 操作 | 说明 |
|------|------|------|
| `bus/bus.go` | 修改 | `InboundMessage` 新增 Agent 间通信字段；`OutboundMessage` 新增 ToolsUsed/WaitingUser/Error |
| `agent/engine.go` | 新增 | `RunConfig` + `Run()` + `buildToolContext()` + `spawnAgentAdapter` |
| `agent/agent.go` | 修改 | `runLoop()` → 构建 RunConfig + 调用 `Run()` |
| `agent/agent.go` | 修改 | `RunSubAgent()` → `handleSubAgentMessage()` 接收 InboundMessage |
| `agent/agent.go` | 删除 | `executeTool()` → 移入 engine.go |

**向后兼容**：
- `SubAgentManager` 接口签名不变（`RunSubAgent(ctx, task, prompt, tools) → (string, error)`）
- `spawnAgentAdapter` 内部完成 `string → InboundMessage → OutboundMessage → string` 转换
- `SubAgentTool` 零改动

**验证**：所有现有测试通过 + Phase 0 新增测试通过，行为不变。

**预估**：3-4 天

### Phase 2: SubAgent 记忆隔离

**目标**：SubAgent 获得独立的 persona + archival memory。

**文件变更**：

| 文件 | 操作 | 说明 |
|------|------|------|
| `storage/sqlite/core_memory.go` | 修改 | 新增 `subagent_id` 列 + 迁移 + `GetBlockWithSubAgent` / `SetBlockWithSubAgent` |
| `storage/vectordb/archival.go` | 修改 | `ForCollection(name)` 方法，支持自定义 collection |
| `memory/letta/subagent_memory.go` | 新增 | `SubAgentMemory` 实现 `MemoryProvider` |
| `agent/agent.go` | 修改 | `handleSubAgentMessage()` 中创建 SubAgent 记忆并注入 RunConfig |
| `tools/subagent_loader.go` | 修改 | frontmatter 解析 `memory` 字段 |
| `tools/subagent_roles.go` | 修改 | `SubAgentRole` 新增 `Memory` 字段 |

**验证**：
- SubAgent persona 独立于主 Agent
- SubAgent archival memory 跨用户共享
- SubAgent human block 继承调用者（只读）
- `memory: false` 的角色行为不变

**预估**：3-4 天

### Phase 3: 统一 ToolContext + 角色能力声明

**目标**：SubAgent 的 ToolContext 与主 Agent 一致（按需配置）。

**文件变更**：

| 文件 | 操作 | 说明 |
|------|------|------|
| `agent/engine.go` | 修改 | `buildToolContext()` 统一构建 |
| `tools/subagent_loader.go` | 修改 | 解析 `send_message`, `max_iterations` |
| 角色定义文件 | 修改 | 声明能力 |

**预估**：1-2 天

### Phase 4: 调用链追踪 + 防递归

**目标**：支持 SubAgent 嵌套调用，防止无限递归。

**文件变更**：

| 文件 | 操作 | 说明 |
|------|------|------|
| `agent/call_chain.go` | 新增 | `CallChain` + 深度/循环检测 |
| `agent/engine.go` | 修改 | `Run()` 注入调用链 context |
| `tools/subagent.go` | 修改 | `Execute()` 检查调用链 |

**预估**：1 天

## 5. 风险评估与缓解

| 风险 | 级别 | 缓解措施 |
|------|------|----------|
| `runLoop` 回归 | 🔴 高 | Phase 0 补充集成测试（mock LLM + mock tools）作为安全网 |
| InboundMessage 字段膨胀 | 🟡 中 | Agent 间通信字段仅在 `channel="agent"` 时有值，IM 渠道不受影响；后续可拆为 embedded struct |
| SubAgent 记忆泄漏 | 🟡 中 | working_context 不持久化；archival 有 collection 隔离 |
| 数据库迁移失败 | 🟡 中 | `subagent_id` 列有默认值 ''，重建表在事务中执行 |
| 性能影响 | 🟢 低 | SubAgent 记忆是可选的（`memory: false` 时零开销） |
| 向后兼容 | 🟢 低 | `SubAgentManager` 接口不变，`SubAgentTool` 零改动 |

## 6. 关键决策记录

| # | 决策 | 选项 | 选择 | 理由 |
|---|------|------|------|------|
| 1 | 通信协议 | A: 保持两套（函数调用 + 消息） B: 统一为 InboundMessage/OutboundMessage | **B** | 消除两套通信方式的割裂，SubAgent 获得完整的上下文信息（sender、channel、media） |
| 2 | SubAgent human block | A: 独立存储 B: 继承调用者 | **B** | SubAgent 为调用者服务，需要知道调用者是谁；独立存储会导致数据冗余 |
| 3 | SubAgent working_context | A: 持久化 B: 不持久化 | **B** | 每次任务独立，持久化会导致跨任务污染 |
| 4 | SubAgent archival | A: 按用户隔离 B: 全局共享 | **B** | code-reviewer 的经验应该所有用户共享 |
| 5 | 统一方式 | A: 提取 AgentEngine 类 B: RunConfig + 函数 | **B** | 函数式更简单，避免引入新的类层次；Go 惯用法 |
| 6 | 记忆启用方式 | A: 代码硬编码 B: 角色定义声明 | **B** | 灵活，用户可自定义角色能力 |
| 7 | 防递归深度 | 3 / 5 / 无限制 | **3** | 实际场景很少超过 2 层，3 层足够且安全 |
| 8 | SubAgentManager 接口 | A: 改为消息签名 B: 保持不变，adapter 转换 | **B** | 最小改动，SubAgentTool 零改动，adapter 模式隔离变化 |

## 7. 不在本设计范围内

以下功能明确排除，留待后续 issue：

1. **Interactive 模式**（Issue #119 提到的多轮 SubAgent 对话）— 需要更复杂的控制流，单独设计
2. **SubAgent 间直接通信** — 当前 SubAgent 只能通过父 Agent 间接通信；统一消息协议为未来直接通信打下基础
3. **SubAgent 持久化 session** — 当前 SubAgent 不保留对话历史，每次任务从零开始
4. **记忆清理 API** — SubAgent archival memory 的手动清理/容量限制
5. **SubAgent Memorize** — SubAgent 的 `Memorize()` 当前为 no-op，后续可支持任务结束后自动归档经验
6. **MessageBus 路由 Agent 消息** — 当前 Agent 间消息是同步函数调用，不经过 MessageBus；未来可改为异步消息路由


## 8. 统一寻址与消息路由

### 8.1 问题：当前寻址方式碎片化

系统中存在 **5 种不同的 ID/寻址方式**，散落在不同层，没有统一的寻址体系：

| 场景 | 当前寻址方式 | 拼接规则 | 问题 |
|------|-------------|----------|------|
| 消息路由（chat 分组） | `channel + ":" + chatID` | `"feishu:oc_xxx"` | 两字段手动拼接，散落在 10+ 处 |
| 取消请求 | `channel + ":" + chatID + ":" + senderID` | `"feishu:oc_xxx:ou_xxx"` | 三字段拼接 |
| 会话隔离（session） | `channel + ":" + chatID` | `"feishu:oc_xxx"` | 同上 |
| 工具激活（session key） | `channel + ":" + chatID` | `"feishu:oc_xxx"` | 同上 |
| 存储隔离（tenant） | `tenantID`（int64 自增） | `1, 2, 3...` | 与 channel:chatID 的映射隐藏在 DB |
| Agent 标识 | `"main"`, `"main/code-reviewer"` | 字符串 | 与 IM 寻址完全不同 |
| Sandbox 容器 | `"xbot-" + userID` | `"xbot-ou_xxx"` | 又一套命名 |
| 记忆隔离（persona） | `tenantID=0` | 全局 | 特殊值 |
| 记忆隔离（human） | `tenantID=0, userID=ou_xxx` | 按用户 | 又一种组合 |

**核心问题**：
1. **没有统一的"地址"概念** — 每个子系统自己拼接 key，规则不一致
2. **IM 用户和 Agent 是两套寻址空间** — 无法用同一种方式定位"消息的发送方/接收方"
3. **消息路由硬编码在 Agent.Run()** — 不经过消息总线，无法扩展

### 8.2 统一寻址设计

#### 8.2.1 Address 类型

引入 `Address` 作为系统中所有实体的统一标识：

```go
// bus/address.go

// Address 统一寻址标识。
// 格式: scheme://id[/sub]
//
// 实体类型与 scheme 对应：
//   - im://feishu/ou_xxx          → 飞书用户（私聊）
//   - im://feishu/oc_xxx          → 飞书群聊
//   - im://qq/xxx                 → QQ 用户/群
//   - agent://main                → 主 Agent
//   - agent://main/code-reviewer  → SubAgent
//   - system://cron               → 定时任务
//   - system://cli                → CLI 调试
type Address struct {
    Scheme string // "im", "agent", "system"
    Domain string // "feishu", "qq", "main", "cron"
    ID     string // "ou_xxx", "oc_xxx", "code-reviewer"
}

// String 返回 URI 格式: scheme://domain/id
func (a Address) String() string {
    if a.ID == "" {
        return a.Scheme + "://" + a.Domain
    }
    return a.Scheme + "://" + a.Domain + "/" + a.ID
}

// ParseAddress 从 URI 字符串解析 Address
func ParseAddress(s string) (Address, error) {
    // "im://feishu/ou_xxx" → {Scheme:"im", Domain:"feishu", ID:"ou_xxx"}
    // "agent://main"       → {Scheme:"agent", Domain:"main", ID:""}
    // "agent://main/code-reviewer" → {Scheme:"agent", Domain:"main", ID:"code-reviewer"}
    ...
}

// 便捷构造函数
func IMAddress(channel, id string) Address {
    return Address{Scheme: "im", Domain: channel, ID: id}
}

func AgentAddress(parts ...string) Address {
    if len(parts) == 1 {
        return Address{Scheme: "agent", Domain: parts[0]}
    }
    return Address{Scheme: "agent", Domain: parts[0], ID: strings.Join(parts[1:], "/")}
}

func SystemAddress(name string) Address {
    return Address{Scheme: "system", Domain: name}
}

// 判断方法
func (a Address) IsIM() bool     { return a.Scheme == "im" }
func (a Address) IsAgent() bool  { return a.Scheme == "agent" }
func (a Address) IsSystem() bool { return a.Scheme == "system" }

// Channel 返回 IM 渠道名（仅 im:// 有意义）
func (a Address) Channel() string {
    if a.Scheme == "im" {
        return a.Domain
    }
    return ""
}
```

#### 8.2.2 地址映射表

| 实体 | 当前标识 | 统一地址 |
|------|---------|---------|
| 飞书用户（私聊） | `channel="feishu", chatID="ou_xxx"` | `im://feishu/ou_xxx` |
| 飞书群聊 | `channel="feishu", chatID="oc_xxx"` | `im://feishu/oc_xxx` |
| QQ 用户 | `channel="qq", chatID="xxx"` | `im://qq/xxx` |
| 主 Agent | `"main"` | `agent://main` |
| SubAgent | `"main/code-reviewer"` | `agent://main/code-reviewer` |
| 定时任务 | `IsCron=true` | `system://cron` |
| CLI 调试 | `channel="cli"` | `im://cli/local` |

#### 8.2.3 InboundMessage / OutboundMessage 改造

```go
// bus/bus.go

type InboundMessage struct {
    // === 统一寻址 ===
    From Address // 消息发送方（IM 用户 / 父 Agent / cron）
    To   Address // 消息接收方（Agent）

    // === 内容 ===
    Content  string
    Media    []string
    Metadata map[string]string
    Time     time.Time

    // === 调度 ===
    RequestID string

    // === Agent 间通信（仅 From.IsAgent() 时有值）===
    SystemPrompt string
    AllowedTools []string
    RoleName     string

    // === 兼容字段（过渡期，逐步废弃）===
    // Deprecated: 使用 From.Channel() 代替
    Channel string
    // Deprecated: 使用 From.ID 代替
    SenderID string
    // ...
}

// 便捷方法（过渡期兼容 + 语义清晰）
func (m *InboundMessage) SenderAddress() Address { return m.From }
func (m *InboundMessage) TargetAddress() Address { return m.To }

// OriginIM 获取原始 IM 地址（Agent 间调用时从 Metadata 追溯）
func (m *InboundMessage) OriginIM() Address {
    if m.From.IsIM() {
        return m.From
    }
    if origin, ok := m.Metadata["origin_address"]; ok {
        addr, _ := ParseAddress(origin)
        return addr
    }
    return Address{}
}

type OutboundMessage struct {
    // === 统一寻址 ===
    From Address // 发送方 Agent
    To   Address // 接收方（IM 用户/群 / 父 Agent）

    // === 内容 ===
    Content  string
    Media    []string
    Metadata map[string]string

    // === Agent 返回扩展 ===
    ToolsUsed   []string
    WaitingUser bool
    Error       error

    // === 兼容字段（过渡期）===
    Channel string
    ChatID  string
}
```

#### 8.2.4 消息总线路由

当前 `MessageBus` 只是两个 channel（Inbound/Outbound），路由逻辑硬编码在 `Agent.Run()` 和 `Dispatcher` 中。

统一寻址后，MessageBus 可以根据 `To` 地址自动路由：

```go
// bus/router.go

// Router 消息路由器，根据 Address 分发消息
type Router struct {
    handlers map[string]Handler // scheme -> handler
    mu       sync.RWMutex
}

// Handler 处理特定 scheme 的消息
type Handler interface {
    // HandleOutbound 处理出站消息（发送到目标）
    HandleOutbound(msg OutboundMessage) (string, error)
}

// RegisterHandler 注册 scheme 处理器
func (r *Router) RegisterHandler(scheme string, h Handler) {
    r.mu.Lock()
    defer r.mu.Unlock()
    r.handlers[scheme] = h
}

// Route 根据 To 地址路由出站消息
func (r *Router) Route(msg OutboundMessage) (string, error) {
    r.mu.RLock()
    h, ok := r.handlers[msg.To.Scheme]
    r.mu.RUnlock()
    if !ok {
        return "", fmt.Errorf("no handler for scheme %q", msg.To.Scheme)
    }
    return h.HandleOutbound(msg)
}
```

路由注册：

```go
// main.go
router := bus.NewRouter()

// IM 渠道路由：根据 To.Domain 分发到对应 Channel
router.RegisterHandler("im", &IMRouter{channels: map[string]channel.Channel{
    "feishu": feishuChannel,
    "qq":     qqChannel,
}})

// Agent 路由：同步调用（当前阶段）
router.RegisterHandler("agent", &AgentRouter{agent: mainAgent})

// System 路由：日志/忽略
router.RegisterHandler("system", &SystemRouter{})
```

### 8.3 与 Sandbox 架构的兼容

Sandbox 当前按 `userID` 隔离（每个用户一个 Docker 容器 `xbot-{userID}`）。

统一寻址后，Sandbox 的 key 从 `userID` 变为 `Address.String()`：

```go
// 当前
func (s *dockerSandbox) getOrCreateContainer(userID string) (*dockerContainer, error) {
    containerName := "xbot-" + sanitize(userID)
    ...
}

// 统一寻址后
func (s *dockerSandbox) getOrCreateContainer(owner Address) (*dockerContainer, error) {
    // im://feishu/ou_xxx → xbot-feishu-ou_xxx
    // agent://main/code-reviewer → xbot-agent-main-code-reviewer
    containerName := "xbot-" + sanitize(owner.String())
    ...
}
```

**兼容策略**：
1. **Phase 1**：Sandbox 接口不变，内部将 `Address.ID` 传给现有 `userID` 参数（因为当前只有飞书一个渠道，`Address.ID == userID`）
2. **Phase 2**：Sandbox 接口改为接收 `Address`，支持多渠道用户隔离
3. **SubAgent Sandbox**：SubAgent 继承父 Agent 的 Sandbox（共享工作目录），不创建独立容器

### 8.4 Tenant 与 Address 的关系

当前 `tenantID` 是数据库自增 ID，通过 `(channel, chatID)` 查找。统一寻址后：

```
Address → tenantID 的映射不变（仍然是 DB 查找）
但 key 从 (channel, chatID) 变为 Address.String()
```

```go
// storage/sqlite/tenant.go

// 当前
func (s *TenantService) GetOrCreateTenantID(channel, chatID string) (int64, error)

// 统一寻址后
func (s *TenantService) GetOrCreateTenantID(addr bus.Address) (int64, error) {
    // 内部仍然用 (channel, chatID) 存储，但入参统一为 Address
    channel := addr.Domain  // "feishu"
    chatID := addr.ID       // "oc_xxx"
    ...
}
```

**记忆隔离规则不变**：
- persona: `tenantID=0`（全局）
- human: `tenantID=0, userID=Address.ID`（按用户）
- working_context: 按 `tenantID`（按会话）

### 8.5 Session Key 统一

当前散落在 10+ 处的 `channel + ":" + chatID` 拼接，统一为 `Address.String()`：

```go
// 当前（散落在各处）
sessionKey := msg.Channel + ":" + msg.ChatID
cancelKey := msg.Channel + ":" + msg.ChatID + ":" + msg.SenderID

// 统一后
sessionKey := msg.To.String()   // "im://feishu/oc_xxx"（会话地址）
cancelKey := msg.From.String()  // "im://feishu/ou_xxx"（发送者地址）
```

注意：取消请求的 key 从 `channel:chatID:senderID` 变为 `From.String()`，因为取消的语义是"取消某个发送者的请求"，用发送者地址即可。但群聊中需要区分不同用户的请求，所以 cancelKey 应该是 `To.String() + ":" + From.String()`（会话 + 发送者）。

### 8.6 迁移策略

#### 原则：渐进式，不破坏现有功能

**Step 1: 引入 Address 类型（纯新增，零破坏）**

```
bus/address.go          — 新增 Address 类型 + ParseAddress + 便捷构造函数
bus/address_test.go     — 单元测试
```

**Step 2: InboundMessage / OutboundMessage 新增 From/To 字段（双写）**

```go
type InboundMessage struct {
    From Address // 新增
    To   Address // 新增

    // 保留所有旧字段，渠道层同时填充新旧字段
    Channel    string
    SenderID   string
    ChatID     string
    ...
}
```

渠道层（feishu.go, qq.go）在构造 InboundMessage 时同时填充 `From/To` 和旧字段：

```go
msg := bus.InboundMessage{
    // 新字段
    From: bus.IMAddress("feishu", senderOpenID),
    To:   bus.IMAddress("feishu", chatID),
    // 旧字段（兼容）
    Channel:  "feishu",
    SenderID: senderOpenID,
    ChatID:   chatID,
    ...
}
```

**Step 3: 逐步迁移消费方（每次一个子系统）**

按优先级迁移：
1. `Agent.Run()` 中的 chatQueue key → `msg.To.String()`
2. cancelKey → `msg.To.String() + ":" + msg.From.String()`
3. sessionKey → `msg.To.String()`
4. Dispatcher 路由 → `msg.To`
5. TenantService → `GetOrCreateTenantID(addr Address)`
6. Sandbox → `getOrCreateContainer(owner Address)`

每步都可以独立 PR，独立测试。

**Step 4: 废弃旧字段**

所有消费方迁移完成后，标记旧字段为 `Deprecated`，最终删除。

### 8.7 与 Phase 1（统一消息协议）的关系

统一寻址是 Phase 1 的**增强**，不是前置条件。建议：

- **Phase 1** 先完成 `RunConfig + Run()` 提取，此时仍用 `channel/chatID/senderID` 字段
- **Phase 1.5** 引入 `Address` 类型，InboundMessage/OutboundMessage 新增 `From/To`（双写）
- **Phase 2+** 逐步迁移消费方到 `Address`

这样 Phase 1 的核心重构（消除 runLoop/RunSubAgent 重复）不被寻址改造阻塞。

### 8.8 完整地址示例

```
场景: 用户在飞书群聊中发消息，Agent 调用 code-reviewer SubAgent

1. IM → Agent:
   From: im://feishu/ou_f1dddbe7xxx    (飞书用户)
   To:   im://feishu/oc_670cd0d6xxx    (飞书群聊 → 路由到 Agent)

2. Agent → SubAgent:
   From: agent://main                   (主 Agent)
   To:   agent://main/code-reviewer     (SubAgent)
   Metadata["origin_address"] = "im://feishu/ou_f1dddbe7xxx"

3. SubAgent → Agent (返回):
   From: agent://main/code-reviewer
   To:   agent://main

4. Agent → IM (回复):
   From: agent://main
   To:   im://feishu/oc_670cd0d6xxx    (回复到群聊)
```

### 8.9 开放问题

| # | 问题 | 当前倾向 | 待讨论 |
|---|------|---------|--------|
| 1 | Address 是否需要包含 tenant 信息？ | **否** — tenant 是存储层概念，Address 是通信层概念，通过查找映射 | 如果频繁查找成为瓶颈，可以缓存 |
| 2 | 群聊中 `To` 是群地址还是 Agent 地址？ | **群地址** `im://feishu/oc_xxx` — Agent 监听该地址 | Agent 地址 `agent://main` 更语义化，但需要额外映射 |
| 3 | 多 Agent 实例（未来）如何寻址？ | 每个 Agent 有唯一 `agent://` 地址，Router 分发 | 当前单 Agent 不需要 |
| 4 | Address 是否需要序列化到 DB？ | **是** — tenant 表的 `(channel, chat_id)` 可以改为 `address TEXT` | 需要迁移 |
| 5 | cancelKey 用 `From` 还是 `From+To`？ | **From+To** — 群聊中同一用户在不同群的请求应独立取消 | 当前 `channel:chatID:senderID` 已经是 To+From |

