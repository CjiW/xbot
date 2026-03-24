# SubAgent 独立记忆系统

> 状态：已完成（Phase 1-4 全部实现）
> 日期：2026-03-17

## 1. 背景与目标

### 现状问题
当前 SubAgent 的记忆机制存在以下问题：

1. **共享父 Agent 的记忆**：`buildSubAgentRunConfig()` 中 `caps.Memory` 分支直接继承父 Agent 的 `ToolContextExtras`（tenantID、CoreMemory、ArchivalMemory 等全部复用），SubAgent 和父 Agent 读写的是同一份记忆数据
2. **无独立人格**：SubAgent 没有自己的 persona block，core_memory_tools 写 persona 会覆盖父 Agent 的全局 persona（`tenantID=0, userID=""`）
3. **human block 语义错误**：SubAgent 的 human 应该记录的是"调用者"的特征，但当前 `ctx.SenderID` 是原始用户，不是调用者 Agent
4. **无记忆整理能力**：SubAgent 退出时没有触发 `Memorize()`，对话过程中的学习全部丢失
5. **working_context 污染**：SubAgent 使用父 Agent 的 tenantID，working_context block 与父 Agent 共享

### 设计目标

**核心隐喻**：SubAgent 和调用者之间的对话，就像 xbot 与用户之间的私聊。

| 概念 | 主 Agent 视角 | SubAgent 视角 |
|------|-------------|-------------|
| "用户" | 飞书用户 (senderID) | 调用者 Agent (parentAgentID) |
| "自己" | xbot (persona 全局共享) | SubAgent 自己 (persona 按 agentID 隔离) |
| 会话 | channel:chatID (飞书会话) | parentAgentID:subAgentID (内部会话) |
| human block | 飞书用户特征 | 调用者 Agent 特征 |
| persona block | xbot 身份 (全局) | SubAgent 独立身份 |
| archival memory | 按 tenantID 隔离 | 按 agentID 隔离 |
| working_context | 按 tenantID 隔离 | 按 parentAgentID+agentID 隔离 |

## 2. 技术方案

### 2.1 SubAgent Tenant 隔离

**原则**：每个 SubAgent 有自己的 tenantID，用于隔离所有记忆数据。

```
tenantID 生成规则: SHA256(parentTenantID + ":" + parentAgentID + ":" + roleName)[:8] → int64
```

这样做的好处：
- CoreMemory、ArchivalMemory、RecallMemory、WorkingContext 全部自动隔离
- 同一 SubAgent 被不同调用者调用时，有各自独立的记忆
- 同一调用者多次调用同一 SubAgent，记忆连续

**不需要的方案（被否决）**：为 SubAgent 复用 `GetOrCreateSession()` — 因为 SubAgent 的 session 是临时的（内存中的消息列表），不需要持久化到 SQLite session 表。

### 2.2 Core Memory 语义重映射

SubAgent 的三个 core block 的隔离策略：

| Block | 存储键 | 说明 |
|-------|--------|------|
| `persona` | `(subTenantID, "persona", "")` | SubAgent 自己的人格，完全独立 |
| `human` | `(subTenantID, "human", parentAgentID)` | 按调用者隔离，记录调用者的特征 |
| `working_context` | `(subTenantID, "working_context", "")` | 按 subTenantID 隔离 |

实现方式：修改 `buildToolContextExtras()` 为 SubAgent 创建独立的 `ToolContextExtras`，使用 subTenantID 和 parentAgentID 作为 userID。

### 2.3 SubAgent 独立记忆生命周期

#### 2.3.1 记忆初始化

在 `spawnSubAgent()` 中，当 `caps.Memory == true` 时：

```go
// 1. 生成 subTenantID
subTenantID := deriveSubAgentTenantID(parentTenantID, parentAgentID, roleName)

// 2. 创建独立的 LettaMemory 实例
subMemory := letta.New(subTenantID, coreSvc, archivalSvc, memorySvc, toolIndexSvc)

// 3. 注入到 RunConfig
cfg.Memory = subMemory                    // MemoryProvider（Recall 注入 system prompt）
cfg.ToolContextExtras = &ToolContextExtras{
    TenantID:       subTenantID,
    CoreMemory:     coreSvc,               // 共享服务实例，数据已按 tenantID 隔离
    ArchivalMemory: archivalSvc,           // 同上
    MemorySvc:      memorySvc,
    RecallTimeRange: recallFn,             // 按 tenantID 过滤
    ToolIndexer:    subMemory,
}

// 4. 设置 context 中的 userID = parentAgentID
//    让 Recall/Memorize 中的 human block 使用 parentAgentID
ctx = letta.WithUserID(ctx, parentAgentID)
```

#### 2.3.2 记忆注入（Recall）

SubAgent 的 `Run()` 开始时，通过 `MemoryMiddleware` 调用 `subMemory.Recall(ctx, task)`，注入三个 block 到 system prompt：

```
## Core Memory
### Persona
我是 code-reviewer，专注于代码审查...
### Human
（调用者 main agent 的特征，由之前的对话积累）
### Working Context
当前审查任务：PR #148...
```

#### 2.3.3 退出前记忆整理（Memorize）

**关键设计**：SubAgent 退出前**同步**执行 `Memorize()`。

```go
func (a *Agent) spawnSubAgent(ctx context.Context, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
    // ... 现有逻辑 ...

    out := Run(subCtx, cfg)

    // --- 退出前记忆整理（同步） ---
    if caps.Memory && out.Error == nil {
        a.consolidateSubAgentMemory(subCtx, subTenantID, parentAgentID, out, llmClient, model)
    }

    return out, nil
}
```

`consolidateSubAgentMemory` 实现：
```go
func (a *Agent) consolidateSubAgentMemory(
    ctx context.Context,
    subTenantID int64,
    parentAgentID string,
    out *bus.OutboundMessage,
    llmClient llm.LLM,
    model string,
) {
    // 1. 收集本次对话消息（从 Run 的 messages 中提取，需要新字段暴露）
    messages := out.Messages  // 需要修改 OutboundMessage 或 Run 返回值

    // 2. 设置 userID = parentAgentID
    ctx = letta.WithUserID(ctx, parentAgentID)

    // 3. 创建独立 LettaMemory 并执行 Memorize
    mem := letta.New(subTenantID, coreSvc, archivalSvc, memorySvc, toolIndexSvc)
    result, _ := mem.Memorize(ctx, memory.MemorizeInput{
        Messages:         messages,
        LastConsolidated: 0,           // 全量整理（SubAgent 无持久化 session）
        LLMClient:        llmClient,
        Model:            model,
        ArchiveAll:       true,         // 归档所有消息
        MemoryWindow:     0,            // 不保留，全部归档
    })

    if result.OK {
        log.Info("SubAgent memory consolidation completed")
    }
}
```

### 2.4 Compact 支持

SubAgent 也需要上下文压缩能力：

```go
// 在 buildSubAgentRunConfig 中
if caps.Memory {
    cfg.AutoCompress = &CompressConfig{
        MaxContextTokens:     maxContextTokens,
        CompressionThreshold: compressionThreshold,
        CompressFunc:         a.compressContext,
    }
}
```

与主 Agent 的区别：
- **同步压缩**：SubAgent 不需要进度通知，直接压缩
- **不持久化**：SubAgent 无 `Session`，压缩结果只在内存中生效
- **退出前一定会 compact+memorize**：即使没有超过阈值，退出前也做一次记忆整理

### 2.5 Run 输出扩展

当前 `Run()` 返回 `*bus.OutboundMessage`，需要扩展以暴露内部消息列表（用于退出后的 memorize）：

```go
type RunOutput struct {
    *bus.OutboundMessage
    Messages []llm.ChatMessage  // 完整对话消息（用于 SubAgent memorize）
}
```

修改 `Run()` 返回 `RunOutput`，或修改 `OutboundMessage` 增加可选字段。

## 3. 实现计划

### Phase 1: SubAgent Tenant 隔离基础（核心）

#### TODO 1.1: 生成 SubAgent Tenant ID ✅
- **文件**: `agent/subagent_tenant.go`（新文件）
- **改动**: 新增 `deriveSubAgentTenantID(parentTenantID int64, parentAgentID, roleName string) int64`
- **策略**: 使用 SHA256 哈希确保确定性和唯一性，结果为负数避免与正常 tenantID 冲突
- **测试**: `agent/subagent_tenant_test.go` — 确定性、唯一性、负数范围

#### TODO 1.2: 创建独立的 ToolContextExtras ✅
- **文件**: `agent/engine_wire.go`
- **改动**: 修改 `buildSubAgentRunConfig()` 中 `caps.Memory` 分支
  - 通过 `buildSubAgentMemory()` 创建独立的 `LettaMemory` 实例
  - `subCtx = letta.WithUserID(subCtx, parentAgentID)` — human block 使用调用者 ID
  - 移除对 `a.buildToolContextExtras(parentCtx.Channel, parentCtx.ChatID)` 的直接调用
  - 新增 `buildSubAgentMemory(subTenantID, parentAgentID)` 方法
  - 新增 `subAgentHumanBlockSenderID(parentTenantID, parentAgentID, roleName)` 函数
- **注意**: CoreMemory/ArchivalMemory 服务实例是共享的（SQLite/chromem-go 线程安全），数据按 tenantID 自动隔离
- **新增**: `session/multitenant.go` — `CoreMemoryService()` / `ArchivalService()` / `MemoryService()` 访问器方法

#### TODO 1.3: 修改 Run() 接受 Memory Provider ✅
- **文件**: `agent/engine.go`, `agent/engine_wire.go`
- **改动**: 
  - `Run()` 返回 `*RunOutput`（嵌入 `*bus.OutboundMessage` + `Messages []llm.ChatMessage`）
  - `buildSubAgentRunConfig()` 设置 `cfg.Memory = subMemory`
  - SubAgent 的 system prompt 中注入独立的记忆（在 `buildSubAgentRunConfig` 中调用 `mem.Recall()`）

### Phase 2: 退出前记忆整理

#### TODO 2.1: 扩展 Run 输出 ✅
- **文件**: `agent/engine.go`, `agent/agent.go`, `agent/engine_wire.go`
- **改动**: `Run()` 返回 `*RunOutput`（方案 A），包含嵌入的 `*bus.OutboundMessage` + `Messages []llm.ChatMessage`
  - 所有调用点已适配（嵌入字段自动透传 `.Content`、`.Error` 等属性）
  - `spawnSubAgent` 通过 `.OutboundMessage` 提取原始消息返回
  - 新增 `buildOutput()` 辅助函数在所有返回点统一填充 `Messages`

#### TODO 2.2: 实现退出前 consolidateSubAgentMemory ✅
- **文件**: `agent/engine_wire.go`
- **改动**: 
  ```go
  func (a *Agent) consolidateSubAgentMemory(ctx, mem, messages, task, roleName, parentAgentID, llmClient, model)
  ```
- 调用 `mem.Memorize()` 同步执行，`ArchiveAll=true`
- 超时保护：context 带超时（30s），避免 memorize 卡住

#### TODO 2.3: 在 spawnSubAgent 中触发 ✅
- **文件**: `agent/engine_wire.go`
- **改动**: `spawnSubAgent()` 末尾，当 `cfg.Memory != nil && out.Error == nil` 时调用 consolidateSubAgentMemory

### Phase 3: Compact 支持

#### TODO 3.1: SubAgent 启用 AutoCompress ✅
- **文件**: `agent/engine_wire.go`
- **改动**: `buildSubAgentRunConfig()` 中 `caps.Memory` 分支增加 `cfg.AutoCompress`
- SubAgent 没有进度通知（`ProgressNotifier == nil`），压缩静默执行
- 使用与主 Agent 相同的 `maxContextTokens` 和 `compressionThreshold`

### Phase 4: ~~首次初始化 Persona~~（已否决）

**否决原因**：SubAgent 的 system prompt 已作为 system message 固定存在于对话中，再重复写入 persona block 会导致同一内容被注入两次，浪费 token 且可能产生语义冲突。persona block 的正确用途是由 SubAgent 通过 `memorize()` 自行积累（经验、偏好、对调用者的观察等），首次使用时为空是正常状态。

## 4. 关键设计决策

### Q1: 为什么不给 SubAgent 创建 TenantSession？
TenantSession 会写入 SQLite session 表，SubAgent 的对话是临时的，不需要持久化。SubAgent 的 session 只存在于 `Run()` 的 `messages` 变量中（内存），退出时通过 memorize 将学习写入记忆系统。

### Q2: 为什么 memorize 是同步的？
主 Agent 的 memorize 是异步的（因为用户可能在等回复），但 SubAgent 的 memorize 在返回结果给调用者之前完成。这样调用者拿到的 SubAgent 结果已经包含了最新的记忆更新。如果 memorize 失败或超时，只记日志不影响 SubAgent 返回结果。

### Q3: 同一 role 被不同调用者调用，记忆是否共享？
不共享。`deriveSubAgentTenantID(parentTenantID, parentAgentID, roleName)` 中包含了 `parentAgentID`，所以同一 role 被不同的父 Agent 调用时会有不同的 subTenantID。这符合设计：SubAgent 和每个调用者的关系是独立的"私聊"。

### Q4: archival memory 的 tenantID 如何隔离？
chromem-go 的 ArchivalService 的 `Insert/Search/Count` 方法都接受 `tenantID` 参数。SubAgent 使用自己的 subTenantID，数据自动隔离。所有 tenant 的数据存储在同一个 chromem-go 实例中（共享 embedding 模型连接），通过 tenantID 命名空间隔离。

### Q5: 不开启 Memory 的 SubAgent 怎么办？
保持现状：不注入记忆工具、不创建 LettaMemory、不做 memorize。行为与现在完全一致。

## 5. 改动文件清单

| 文件 | 改动类型 | 说明 |
|------|---------|------|
| `agent/engine.go` | 修改 | Run() 返回 *RunOutput；新增 buildOutput() 辅助函数 |
| `agent/engine_wire.go` | 修改 | 核心：buildSubAgentMemory 记忆隔离、spawnSubAgent 退出整理、consolidateSubAgentMemory |
| `agent/subagent_tenant.go` | 新增 | deriveSubAgentTenantID — SubAgent 独立租户 ID 生成 |
| `agent/subagent_tenant_test.go` | 新增 | deriveSubAgentTenantID 单元测试 |
| `session/multitenant.go` | 修改 | 新增 CoreMemoryService()/ArchivalService()/MemoryService() 访问器 |
| `agent/middleware_builtin.go` | 无改动 | MemoryMiddleware 已通用，自动生效 |
| `memory/letta/letta.go` | 无改动 | Recall/Memorize 已通过 context 传递 userID |
| `storage/sqlite/core_memory.go` | 无改动 | GetBlock/SetBlock 已按 (tenantID, block, userID) 隔离 |
| `tools/subagent_roles.go` | 无改动 | Capabilities.Memory 字段已存在 |

## 6. 风险与缓解

| 风险 | 缓解措施 |
|------|---------|
| deriveSubAgentTenantID 哈希冲突 | 使用 64 位哈希 + 验证唯一性 |
| memorize 同步阻塞 SubAgent 返回 | 设置 30s 超时，失败只记日志 |
| 首次 SubAgent 无 persona | 正常状态，persona 由 memorize 自行积累 |
| archival 存储膨胀 | SubAgent 的 archival 条目数量有限（每次调用最多几条），长期可通过 TTL 清理 |

## 7. 测试计划

1. **单元测试**: `deriveSubAgentTenantID` 确定性和唯一性
2. **集成测试**: SubAgent 调用后检查 core_memory_blocks 表中有独立记录
3. **隔离测试**: 两个 SubAgent 各自写 human block，验证数据不互相污染
4. **连续性测试**: 同一 SubAgent 被同一调用者多次调用，验证记忆持续更新
5. **退出测试**: SubAgent 完成后检查 archival_memory 有归档记录
