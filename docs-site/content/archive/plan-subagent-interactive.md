---
title: "plan-subagent-interactive"
weight: 200
---

# SubAgent Interactive Mode

> 状态：待审批
> 日期：2026-03-17

## 1. 背景与目标

### 现状
当前 SubAgent 是**一次性执行**模式：`spawnSubAgent()` 创建 SubAgent → `Run()` 执行 → 记忆整理 → 返回结果 → 上下文销毁。每次调用都是全新对话，只能通过持久化记忆系统（core memory + archival memory）继承之前学到的信息。

### 需求
增加 **Interactive Mode**，使 SubAgent 可以：
1. **多轮对话**：parent agent 可以多次向同一个 SubAgent 发送消息，SubAgent 保持上下文
2. **手动卸载**：parent agent 使用 `UnloadSubAgent` 工具主动结束 SubAgent 会话
3. **自动清理**：parent agent 退出时（`Run()` 结束），所有活跃的 SubAgent 自动卸载并整理记忆

### 不做的事情
- **不持久化 session 到 SQLite**：SubAgent 的会话历史只存在于内存中，退出时通过 memorize 将学习写入记忆系统（与当前设计一致）
- **不做超时回收**：SubAgent 的生命周期完全由 parent agent 控制（显式 unload 或 parent 退出）

## 2. 核心设计

### 2.1 会话管理模型

```
Agent 结构体新增:
  activeSubAgents sync.Map  // key: conversationID → *activeSubAgent

activeSubAgent 结构体:
  conversationID string              // "parentAgentID/roleName"
  role          string               // 角色名
  parentAgentID string               // 父 Agent ID
  cfg           RunConfig            // 预构建的配置（记忆、工具等）
  messages      []llm.ChatMessage    // 对话历史（内存中）
  mu            sync.Mutex           // 保护 messages
  cancel        context.CancelFunc   // 用于强制终止
```

### 2.2 会话标识

```
conversationID = parentAgentID + "/" + roleName
```

**唯一性保证**：
- 同一 parent agent 同时只能有一个某 role 的 interactive SubAgent
- 如果已有活跃会话，后续 `SubAgent` 工具调用自动路由到现有会话（多轮对话）
- 不同 parent agent 的相同 role 有各自独立的会话

### 2.3 生命周期

```
创建（SubAgent 工具，interactive=true）:
  1. 检查 activeSubAgents 是否已有该 conversationID
  2. 如有 → 获取会话，追加新 user 消息，继续 Run()
  3. 如无 → 创建新会话，构建 RunConfig，Run()

多轮对话（SubAgent 工具，interactive=true，已有会话）:
  1. 获取 activeSubAgent
  2. 追加 user 消息到 messages
  3. 用现有 messages 继续调用 Run()
  4. SubAgent 返回后，messages 更新（追加 assistant 回复）
  5. SubAgent 退出 Run() 时，RunOutput.WaitingForInput = true
  6. Run() 返回给 parent agent，parent agent 可以继续发送

手动卸载（UnloadSubAgent 工具）:
  1. 获取 activeSubAgent
  2. 执行 consolidateSubAgentMemory()
  3. 从 activeSubAgents 删除
  4. 调用 cancel() 终止 SubAgent

自动清理（parent Run() 结束）:
  1. 遍历 activeSubAgents，找到属于当前 parentAgentID 的所有会话
  2. 对每个会话执行 consolidateSubAgentMemory()
  3. 从 activeSubAgents 删除
  4. 调用 cancel()
```

### 2.4 退出时机的影响

| 退出方式 | 记忆整理 | 资源回收 |
|---------|---------|---------|
| UnloadSubAgent（手动） | ✅ memorize | ✅ 删除会话 |
| parent Run() 结束（自动） | ✅ memorize | ✅ 删除会话 |
| SubAgent 自行结束（无更多工具调用） | ❌ 不整理（等待 parent 卸载） | ❌ 保持活跃 |

**关键设计**：SubAgent 在 `Run()` 中正常退出（无更多工具调用）时，不自动整理记忆、不删除会话。它只是暂停，等待 parent agent 下次发送消息。只有 parent agent 显式卸载或 parent 退出时，才触发记忆整理和资源回收。

## 3. Run() 的改造

### 3.1 新增 WaitingForInput 字段

当前 `Run()` 在没有更多工具调用时返回 final content 并结束。Interactive SubAgent 需要一个"暂停等待"状态：

```go
type RunOutput struct {
    *bus.OutboundMessage
    Messages         []llm.ChatMessage
    WaitingForInput  bool  // Interactive mode: SubAgent 暂停，等待 parent 继续对话
}
```

### 3.2 Run() 的新参数

在 `RunConfig` 中新增：

```go
type RunConfig struct {
    // ... 现有字段 ...

    // Interactive 交互模式
    // true 时，Run() 在 SubAgent 无工具调用时返回 WaitingForInput=true
    // 而不是返回 final content 并结束
    Interactive bool
}
```

### 3.3 Run() 行为变化

当 `cfg.Interactive == true` 且 LLM 返回无工具调用的内容时：

**当前行为**：
```
return buildOutput(&bus.OutboundMessage{Content: cleanContent, ...})
```

**Interactive 行为**：
```
// 将 assistant 回复追加到 messages（供下次继续）
messages = append(messages, llm.NewAssistantMessage(cleanContent))

// 返回，标记为等待输入
return buildOutput(&bus.OutboundMessage{
    Content:        cleanContent,  // parent agent 看到的回复
    WaitingForInput: true,          // 告诉 spawn 逻辑：不要整理记忆
})
```

## 4. 工具变更

### 4.1 SubAgent 工具扩展

在现有 `SubAgentTool` 参数中增加 `interactive` 字段：

```go
func (t *SubAgentTool) Parameters() []llm.ToolParam {
    return []llm.ToolParam{
        {Name: "task", Type: "string", Description: "The task description", Required: true},
        {Name: "role", Type: "string", Description: "Predefined role name", Required: true},
        {Name: "interactive", Type: "boolean", Description: "If true, keep the sub-agent alive for multi-turn conversation. Use UnloadSubAgent to end it."},
    }
}
```

交互模式下 `SubAgentTool.Execute()` 的流程变化：
1. 解析 `interactive` 参数
2. 如果 `interactive=true`，调用 `ctx.Manager.InteractiveSubAgent()` 而非 `RunSubAgent()`
3. 返回 SubAgent 的回复

### 4.2 新增 UnloadSubAgent 工具

```go
type UnloadSubAgentTool struct{}

func (t *UnloadSubAgentTool) Name() string { return "UnloadSubAgent" }

func (t *UnloadSubAgentTool) Description() string {
    return `End an interactive sub-agent session and consolidate its memory.
The sub-agent's conversation learnings will be persisted to its memory system.

Parameters (JSON):
  - role: string (required), the role name of the sub-agent to unload

Example: {"role": "code-reviewer"}`
}

func (t *UnloadSubAgentTool) Parameters() []llm.ToolParam {
    return []llm.ToolParam{
        {Name: "role", Type: "string", Description: "The role name of the interactive sub-agent to end", Required: true},
    }
}

func (t *UnloadSubAgentTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
    // 解析 role 参数
    // 调用 ctx.Manager.UnloadSubAgent(ctx, role)
    // 返回确认信息
}
```

### 4.3 SubAgentManager 接口扩展

```go
type SubAgentManager interface {
    RunSubAgent(parentCtx *ToolContext, task string, systemPrompt string, allowedTools []string, caps SubAgentCapabilities, roleName string) (string, error)
    
    // 新增：交互式 SubAgent
    InteractiveSubAgent(parentCtx *ToolContext, task string, systemPrompt string, allowedTools []string, caps SubAgentCapabilities, roleName string) (string, error)
    
    // 新增：卸载交互式 SubAgent
    UnloadSubAgent(parentCtx *ToolContext, roleName string) (string, error)
}
```

## 5. 实现计划

### Phase 1: activeSubAgent 会话管理

#### TODO 1.1: 定义 activeSubAgent 结构
- **文件**: `agent/subagent_interactive.go`（新增）
- **内容**:
  ```go
  type activeSubAgent struct {
      conversationID string
      role           string
      parentAgentID  string
      cfg            RunConfig
      messages       []llm.ChatMessage  // 对话历史
      mu             sync.Mutex
      cancel         context.CancelFunc
  }
  ```
- **存储**: `Agent.activeSubAgents sync.Map`（key: conversationID）

#### TODO 1.2: InteractiveSubAgent 方法
- **文件**: `agent/subagent_interactive.go`
- **流程**:
  1. 计算 `conversationID = parentAgentID + "/" + roleName`
  2. 查找 `activeSubAgents` 中是否已有会话
  3. **已有会话**（多轮）：
     - 锁定 mu
     - 追加 `llm.NewUserMessage(task)` 到 messages
     - 构建 cfg（复用已有 cfg 的 Memory、ToolContextExtras 等，替换 messages）
     - 设置 `cfg.Interactive = true`
     - `cfg.Messages = subAgent.messages`
     - `Run(ctx, cfg)` → 获取回复
     - 将 assistant 回复追加到 messages
     - 返回回复给 parent agent
  4. **新会话**：
     - 调用 `buildSubAgentRunConfig()` 构建完整 cfg
     - 设置 `cfg.Interactive = true`
     - `Run(ctx, cfg)` → 获取回复
     - 将 assistant 回复追加到 messages
     - 创建 `activeSubAgent` 存入 `activeSubAgents`
     - 返回回复给 parent agent

#### TODO 1.3: UnloadSubAgent 方法
- **文件**: `agent/subagent_interactive.go`
- **流程**:
  1. 计算 conversationID
  2. 从 `activeSubAgents` 获取并删除
  3. 执行 `consolidateSubAgentMemory()`
  4. 调用 `cancel()`（如果 SubAgent 正在运行）
  5. 返回确认信息

#### TODO 1.4: cleanupSubAgents 方法
- **文件**: `agent/subagent_interactive.go`
- **流程**:
  1. 遍历 `activeSubAgents`，找到属于指定 parentAgentID 的所有会话
  2. 对每个执行 `consolidateSubAgentMemory()` + `cancel()`
  3. 从 map 中删除
- **调用时机**: `spawnSubAgent()` 返回前（或 `buildMainRunConfig` 中注册 cleanup callback）

### Phase 2: Run() 交互模式支持

#### TODO 2.1: RunConfig 增加 Interactive 字段
- **文件**: `agent/engine.go`
- **改动**: `RunConfig` 新增 `Interactive bool`

#### TODO 2.2: Run() 交互模式退出逻辑
- **文件**: `agent/engine.go`
- **改动**: 在 `Run()` 中，当 LLM 无工具调用时：
  ```go
  if !response.HasToolCalls() {
      if cfg.Interactive {
          // 交互模式：追加 assistant 回复到 messages，标记等待输入
          messages = append(messages, llm.NewAssistantMessage(cleanContent))
          out := buildOutput(&bus.OutboundMessage{
              Channel:         cfg.Channel,
              ChatID:          cfg.ChatID,
              Content:         cleanContent,
              ToolsUsed:       toolsUsed,
              WaitingForInput: true,
          })
          return out
      }
      // 非交互模式：原有逻辑
      return buildOutput(...)
  }
  ```

### Phase 3: 工具层适配

#### TODO 3.1: SubAgentTool 增加 interactive 参数
- **文件**: `tools/subagent.go`
- **改动**: Parameters() 增加 `interactive` 字段，Execute() 根据 interactive 参数选择调用路径

#### TODO 3.2: 新增 UnloadSubAgentTool
- **文件**: `tools/subagent.go`（或新建 `tools/unload_subagent.go`）
- **内容**: UnloadSubAgent 工具实现

#### TODO 3.3: SubAgentManager 接口扩展
- **文件**: `tools/interface.go`
- **改动**: 接口新增 `InteractiveSubAgent()` 和 `UnloadSubAgent()` 方法

#### TODO 3.4: spawnAgentAdapter 适配
- **文件**: `agent/engine.go`
- **改动**: `spawnAgentAdapter` 实现新增的两个接口方法

### Phase 4: parent 退出时自动清理

#### TODO 4.1: 在 spawnSubAgent 退出时清理
- **文件**: `agent/engine_wire.go`
- **改动**: 在 `spawnSubAgent()` 中，当 `Run()` 返回后，检查是否有当前 parent agent 的活跃 SubAgent 会话，执行清理
- **更好的方案**: 使用 `defer` 在 `Run()` 的调用点（`processMessage` 中 `Run(ctx, cfg)` 之后）触发清理

#### TODO 4.2: 注册 UnloadSubAgent 工具
- **文件**: `tools/interface.go`（DefaultRegistry）
- **改动**: `RegisterCore(&UnloadSubAgentTool{})`

### Phase 5: 测试

#### TODO 5.1: 单元测试
- `activeSubAgent` 的创建、查找、删除
- conversationID 的唯一性

#### TODO 5.2: 集成测试
- Interactive 模式：创建 → 多轮对话 → 卸载
- 自动清理：parent 退出时 SubAgent 被清理
- 非 interactive 模式不受影响（回归）

## 6. 关键设计决策

### Q1: 为什么用内存 map 而不是持久化 session？
SubAgent 的交互是短暂的（parent agent 一次 `Run()` 调用内），不需要跨进程持久化。使用 `sync.Map` 简单高效，退出时通过 memorize 持久化学习。

### Q2: Interactive SubAgent 退出 Run() 后，它的 messages 在哪？
在 `activeSubAgent.messages` 中（内存）。下次 parent 发送消息时，这些 messages 被传入新的 `Run()` 调用继续对话。

### Q3: 如果 Interactive SubAgent 的上下文太长怎么办？
复用现有的 AutoCompress 机制。在 `buildSubAgentRunConfig` 中已经为 SubAgent 配置了 AutoCompress，Interactive 模式下同样生效。

### Q4: 多个 parent agent 同时创建同一 role 的 Interactive SubAgent？
每个 parent agent 有独立的 conversationID（`parentAgentID/roleName`），互不影响。同一 parent agent 同时只能有一个某 role 的 Interactive SubAgent（第二次调用路由到已有会话）。

### Q5: Interactive SubAgent 使用的资源什么时候释放？
- Run() 退出后，LLM 连接、工具等由 GC 回收
- 只有 `activeSubAgent` 结构（含 messages 切片）保留在内存中
- UnloadSubAgent 或 parent 退出时，`activeSubAgent` 被删除，messages 可被 GC

## 7. 改动文件清单

| 文件 | 改动类型 | 说明 |
|------|---------|------|
| `agent/subagent_interactive.go` | 新增 | activeSubAgent 结构、InteractiveSubAgent、UnloadSubAgent、cleanupSubAgents |
| `agent/engine.go` | 修改 | RunConfig.Interactive、Run() 交互模式退出逻辑、RunOutput.WaitingForInput |
| `agent/agent.go` | 修改 | Agent 新增 activeSubAgents sync.Map 字段 |
| `agent/engine_wire.go` | 修改 | spawnSubAgent 退出清理、buildSubAgentRunConfig 可能微调 |
| `tools/subagent.go` | 修改 | SubAgentTool 增加 interactive 参数 |
| `tools/interface.go` | 修改 | SubAgentManager 接口扩展、DefaultRegistry 注册 UnloadSubAgentTool |
| `tools/unload_subagent.go` | 新增（可选） | UnloadSubAgentTool 实现 |

## 8. 风险与缓解

| 风险 | 缓解措施 |
|------|---------|
| Interactive SubAgent messages 膨胀 | AutoCompress 自动压缩 |
| parent 异常退出未清理 | Agent.Close() 中遍历清理所有 activeSubAgents |
| 并发访问 activeSubAgent | sync.Mutex 保护 messages，sync.Map 原子操作 |
| UnloadSubAgent 时 SubAgent 正在 Run() | cancel context 中断 Run()，consolidate 超时保护 |
