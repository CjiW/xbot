# 并发 SubAgent 设计方案

> 状态：v2 ✅ 门下省审核通过
> v1 审核意见：驳回，4 个 P0 问题（EnableReadWriteSplit 默认值错误、并发分支设计不完整、信号量容量更新、LLM 类型判断）
> 作者：中书省
> 日期：2026-03-23
> 分支：`feat/concurrent-subagent`

## 目录

1. [需求概述](#1-需求概述)
2. [现状分析](#2-现状分析)
3. [技术方案](#3-技术方案)
4. [文件变更清单](#4-文件变更清单)
5. [任务拆分](#5-任务拆分)
6. [验证标准](#6-验证标准)
7. [风险与注意](#7-风险与注意)

---

## 1. 需求概述

| # | 需求 | 优先级 |
|---|------|--------|
| 1 | 并发 SubAgent：同一轮 tool calls 中的多个 SubAgent 调用改为并发执行 | P0 |
| 2 | 账号维度 LLM 并发限制：无论是公共 LLM 还是个人 LLM，都尊重 tenant 维度的并发上限 | P0 |
| 3 | 用户可配置并发数：允许用户在 Settings 中配置自己的 LLM 最大并发数 | P1 |
| 4 | 持久化 + Settings UI：并发数配置持久化到数据库，并在飞书 Settings 卡片中展示 | P1 |

---

## 2. 现状分析

### 2.1 SubAgent 执行机制

**调用链路**：LLM 返回 tool_calls → engine.go 遍历 tool_calls → 串行执行每个 SubAgentTool.Execute() → SubAgentTool 调用 ctx.Manager.RunSubAgent() → spawnAgentAdapter.RunSubAgent() → Agent.RunSubAgent() → engine.Run()

**关键发现**：

1. **`tools/subagent.go:62-160`** — `SubAgentTool.Execute()` 是同步阻塞调用。它调用 `ctx.Manager.RunSubAgent()`，后者最终调用 `engine.Run()`，执行完整的 Agent 循环（多轮 LLM 调用 + 工具执行）。

2. **`agent/engine.go:1057-1077`** — `spawnAgentAdapter.RunSubAgent()` 是一个普通的同步函数：
   ```go
   func (a *spawnAgentAdapter) RunSubAgent(...) (string, error) {
       msg := a.buildMsg(parentCtx, task, roleName, systemPrompt, allowedTools, caps, false)
       out, err := a.spawnFn(parentCtx.Ctx, msg)
       ...
       return out.Content, nil
   }
   ```
   其中 `a.spawnFn` 在 `engine_wire.go:150` 中注入为 `a.spawnSubAgent`，最终调用 `agent.RunSubAgent()`（`agent/agent.go:1717-1723`），后者直接调用 `Run(ctx, cfg)` 阻塞等待整个 SubAgent 完成。

3. **结论**：SubAgent 的执行完全是在 engine.Run() 的 tool call 循环中同步进行的，没有任何并发机制。

### 2.2 Engine 现有并发模型

**`agent/engine.go:695-860`** — tool calls 的执行逻辑：

```go
// engine.go:808-860
if cfg.EnableReadWriteSplit {
    // Phase 1: 只读操作并行执行（maxParallel = 8）
    // Phase 2: 写操作串行执行
} else {
    // 全部串行执行（默认行为）
    for idx, tc := range response.ToolCalls {
        execOne(toolCallEntry{iteration: i, index: idx, tc: tc})
    }
}
```

**关键发现**：

- **`EnableReadWriteSplit`**（`engine.go:113-114`）：已有读写分离并行能力。**主 Agent 已默认开启**（`engine_wire.go:90`：`EnableReadWriteSplit: true`），SubAgent 的 `buildSubAgentRunConfig()` 中未设置此字段（默认 false）。
- **`readOnlyTools`**（`engine.go:171-175`）：只包含 `Read`、`Grep`、`Glob`、`WebSearch`、`ChatHistory`。**SubAgent 不在只读列表中**，因此在主 Agent 中 SubAgent 被归为写操作而串行执行（Phase 2）。
- **SubAgent 无超时**（`engine.go:735-738`）：`SubAgent` 工具被特殊处理，不加 ToolTimeout（因为 SubAgent 本身有自己的 LLMTimeout）。
- **`execOne` 使用共享的 `execResults` slice**（`engine.go:707`）：按 index 写入结果，已有并发安全的基础。

### 2.3 LLM 调用链路与并发控制

**`llm/retry.go:33-62`** — `RetryLLM` 已有信号量机制：

```go
type RetryConfig struct {
    MaxConcurrent int // 最大并发数（0 表示不限制）
    ...
}

func NewRetryLLM(inner LLM, cfg RetryConfig) *RetryLLM {
    r := &RetryLLM{inner: inner, config: cfg}
    if cfg.MaxConcurrent > 0 {
        r.sem = make(chan struct{}, cfg.MaxConcurrent)
    }
    return r
}

func (r *RetryLLM) acquire(ctx context.Context) func() {
    if r.sem == nil { return func() {} }
    select {
    case r.sem <- struct{}{}: return func() { <-r.sem }
    case <-ctx.Done(): return func() {}
    }
}
```

**`main.go:39-43`** — 创建全局 LLM 客户端时 **未设置 MaxConcurrent**：

```go
llmClient, err := createLLM(cfg.LLM, llm.RetryConfig{
    Attempts: uint(cfg.Agent.LLMRetryAttempts),
    Delay:    cfg.Agent.LLMRetryDelay,
    MaxDelay: cfg.Agent.LLMRetryMaxDelay,
    // MaxConcurrent: 0（不限制）
})
```

**`agent/llm_factory.go:113-130`** — 用户自定义 LLM 客户端也 **未设置 MaxConcurrent**：

```go
func (f *LLMFactory) createClient(cfg *sqlite.UserLLMConfig) (llm.LLM, string) {
    // 只传了 Provider, BaseURL, APIKey, Model
    // 没有传 MaxConcurrent
}
```

**结论**：RetryLLM 的并发控制能力已就绪，但从未被启用。全局 LLM 和用户 LLM 都没有设置 MaxConcurrent。

### 2.4 请求级并发控制（Agent 层面）

**`agent/agent.go:869`** — 全局信号量控制消息处理并发：

```go
sem := make(chan struct{}, a.maxConcurrency) // 默认 3
```

**`agent/agent.go:962-981`** — 按用户动态选择信号量：

```go
func (a *Agent) getSemaphoreForMessage(msg bus.InboundMessage, globalSem chan struct{}) chan struct{} {
    if a.isGroupChat(msg) { return globalSem }
    if a.llmFactory.HasCustomLLM(senderID) { return a.getUserSemaphore(senderID) }
    return globalSem
}
```

- 有自定义 LLM 的私聊用户 → 独立信号量（容量 1，即串行）
- 其他 → 全局信号量（容量 3）

**关键发现**：这里的信号量控制的是 **消息处理级别** 的并发（即同时处理几条用户消息），而非 **LLM 调用级别** 的并发。当多个 SubAgent 并发运行时，它们共享同一个消息处理槽位，不会受到此信号量的限制（因为它们在同一个消息处理流程内执行）。

### 2.5 Tenant / Session 结构

**`session/tenant.go`** — TenantSession 结构简单，**没有并发控制字段**：

```go
type TenantSession struct {
    tenantID   int64
    channel    string
    chatID     string
    ...
}
```

**`storage/sqlite/user_settings.go`** — 已有通用的 KV 存储：

```sql
CREATE TABLE user_settings (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    channel    TEXT NOT NULL,
    sender_id  TEXT NOT NULL,
    key        TEXT NOT NULL,
    value      TEXT NOT NULL DEFAULT '',
    updated_at INTEGER NOT NULL,
    UNIQUE(channel, sender_id, key)
);
```

可通过 `key = "llm_max_concurrent"` 存储用户的并发配置，无需建新表。

### 2.6 Settings UI 机制

**`channel/feishu_settings.go`** — 飞书 Settings 卡片：

- **`BuildSettingsCard()`**（第 21 行）：构建交互式飞书卡片，按 tab 分组（general / model / market / metrics）
- **`HandleSettingsAction()`**（第 65 行）：处理卡片回调
- **`buildGeneralTabContent()`**（第 276 行）：通用设置 tab（当前包含上下文管理模式、沙箱管理）
- **`buildModelTabContent()`**：模型设置 tab（LLM 配置、模型选择、max_context、thinking_mode）
- **`SettingsCallbacks`**（`channel/feishu.go:39-68`）：注入 Agent 层回调

**`channel/capability.go`** — 通用 Settings 接口：
- `SettingDefinition`：key, label, description, type, options, category
- `SettingsCapability`：Schema + Submit
- 支持 `number` 类型的 Setting

### 2.7 总结：现状问题清单

| # | 问题 | 位置 | 影响 |
|---|------|------|------|
| 1 | SubAgent 在 engine.Run() 的 tool call 循环中串行执行 | `engine.go:848-854` | 多 SubAgent 调用性能差 |
| 2 | RetryLLM 的 MaxConcurrent 从未被设置 | `main.go:39-43`, `llm_factory.go:113` | LLM 调用无并发限制 |
| 3 | 用户 LLM 客户端不支持并发配置 | `UserLLMConfig` 无 MaxConcurrent 字段 | 无法为个人 LLM 设置并发上限 |
| 4 | Settings 中无并发配置 UI | `feishu_settings.go` | 用户无法配置并发数 |
| 5 | 并发 SubAgent 无 tenant 维度的 LLM 限流 | 无 | 公共 LLM 可能被单个用户的多个 SubAgent 耗尽 |

---

## 3. 技术方案

### 3.1 架构设计

并发控制分为 **两层**：

```
┌──────────────────────────────────────────────────┐
│ 层 1：Tool Call 级并发（engine.Run 内）           │
│   多个 SubAgent tool call 并行执行                │
│   通过 SubAgent 信号量限制并发数                   │
│   ↓ 每个 SubAgent 内部多轮 LLM 调用              │
├──────────────────────────────────────────────────┤
│ 层 2：LLM 调用级并发（RetryLLM 信号量）           │
│   所有 LLM 请求通过 RetryLLM.acquire() 获取令牌   │
│   按 OriginUserID（tenant）分配独立信号量          │
│   防止单用户耗尽公共/个人 LLM 并发配额             │
└──────────────────────────────────────────────────┘
```

### 3.2 方案 A：并发 SubAgent（Tool Call 层）

#### 3.2.1 核心思路

在 engine.Run() 的 tool call 执行阶段，当同一轮有多个 SubAgent tool calls 时，并行执行它们。

#### 3.2.2 并发模型

**现状**：主 Agent 的 ReadWriteSplit 已开启（`engine_wire.go:90`），tool call 执行分为 2 phase：
- Phase 1：只读工具（Read/Grep/Glob 等）并行（`maxParallel=8`）
- Phase 2：写工具（含 SubAgent）串行

**改造后的 3-phase 模型**（仅当 `EnableConcurrentSubAgents=true` 时生效）：

```go
// engine.go tool call 执行逻辑（改造后伪代码）

// 第一步：将 tool calls 分为三类
var readOps, writeOps, subAgentOps []toolCallEntry
for idx, tc := range response.ToolCalls {
    entry := toolCallEntry{iteration: i, index: idx, tc: tc}
    if tc.Name == "SubAgent" {
        subAgentOps = append(subAgentOps, entry)
    } else if readOnlyTools[tc.Name] {
        readOps = append(readOps, entry)
    } else {
        writeOps = append(writeOps, entry)
    }
}

// Phase 1: 只读操作并行执行（复用现有 ReadWriteSplit 逻辑，maxParallel=8）
// 【与现有逻辑完全一致，零修改】

// Phase 2: 非 SubAgent 写操作串行执行
// 【与现有逻辑完全一致，零修改】

// Phase 3: SubAgent 并发执行（新增，仅在 EnableConcurrentSubAgents=true 时）
if cfg.EnableConcurrentSubAgents && len(subAgentOps) > 1 {
    var wg sync.WaitGroup
    for _, entry := range subAgentOps {
        wg.Add(1)
        go func(e toolCallEntry) {
            defer wg.Done()
            if cfg.SubAgentSem != nil {
                select {
                case cfg.SubAgentSem <- struct{}{}:
                    defer func() { <-cfg.SubAgentSem }()
                case <-ctx.Done():
                    return
                }
            }
            execOne(e)
        }(entry)
    }
    wg.Wait()
    // Phase 3 完成后通知进度更新（与 Phase 1 的 notifyProgress("") 对齐）
    if autoNotify {
        notifyProgress("")
    }
} else if len(subAgentOps) > 0 {
    // 仅 1 个 SubAgent 或未开启并发 → 串行（原有行为）
    for _, entry := range subAgentOps {
        execOne(entry)
    }
}
```

**与现有 ReadWriteSplit 的共存策略**：
- 只读工具的并行逻辑完全不变（Phase 1）
- 写工具的串行逻辑完全不变（Phase 2）
- SubAgent 从原来的"归类为写工具串行执行"变为"独立 Phase 3 并发执行"
- 非 SubAgent 写工具仍然串行

**混合场景**（同一轮 tool calls 既有 SubAgent 又有 Read/Edit）：
- 执行顺序：Phase 1（Read 并行）→ Phase 2（Edit 串行）→ Phase 3（SubAgent 并发）
- 非 SubAgent 工具先执行完毕，SubAgent 并发启动
- 未来可优化：非 SubAgent 写工具与 SubAgent 并行执行（需评估依赖关系）

#### 3.2.3 为什么不在现有的 ReadWriteSplit 中把 SubAgent 归为只读？

**不行**，原因：
1. ReadWriteSplit 的只读列表（`readOnlyTools`）是硬编码的全局集合，与读写分离的语义绑定
2. SubAgent 并发需要独立的信号量控制，不能和普通只读工具共用 `maxParallel = 8` 的信号量
3. SubAgent 并发是一个**可选功能**（需要配置开启），不应与 ReadWriteSplit 耦合

#### 3.2.4 SubAgent 信号量

新增 `RunConfig` 字段：

```go
// RunConfig 新增
SubAgentSem chan struct{} // SubAgent 并发信号量（nil = 不限制，容量 = 最大并发 SubAgent 数）
```

信号量在 `engine_wire.go` 中创建时，从用户配置读取并发数：

```go
subAgentConc := getUserSubAgentConcurrency(originUserID) // 从 user_settings 或默认值读取
cfg.SubAgentSem = make(chan struct{}, subAgentConc)
```

### 3.3 方案 B：LLM 调用级并发限制（tenant 维度）

#### 3.3.1 核心思路

为每个 tenant（OriginUserID）的 LLM 调用建立独立的并发信号量。公共 LLM 按 tenant 隔离，个人 LLM 按 tenant 隔离。

#### 3.3.2 实现：Per-Tenant LLM Semaphore

新增 `LLMSemaphoreManager` 组件：

```go
// llm/semaphore.go (新文件)
package llm

type LLMSemaphoreManager struct {
    mu        sync.RWMutex
    semaphores map[string]chan struct{} // key: "senderID:llmKey" → semaphore
    // llmKey: "global" 公共 LLM, "personal:senderID" 个人 LLM
}

// Acquire 获取 tenant 的 LLM 信号量
// llmKey: "global" 或 "personal"
// 返回释放函数
func (m *LLMSemaphoreManager) Acquire(ctx context.Context, senderID, llmKey string, maxConc int) func() {
    key := senderID + ":" + llmKey
    m.mu.RLock()
    sem, ok := m.semaphores[key]
    m.mu.RUnlock()
    
    if !ok {
        m.mu.Lock()
        sem, _ = m.semaphores[key]
        if sem == nil {
            sem = make(chan struct{}, maxConc)
            m.semaphores[key] = sem
        }
        m.mu.Unlock()
    }
    
    select {
    case sem <- struct{}{}: return func() { <-sem }
    case <-ctx.Done(): return func() {}
    }
}
```

#### 3.3.3 注入方式

**不修改 RetryLLM**，而是在 engine.Run() 的 LLM 调用点注入信号量获取：

```go
// engine.go Run() 函数内，每次 LLM 调用前
release := cfg.LLMSemAcquire(ctx) // 从 ToolContext 或 RunConfig 获取
defer release()
response, err := cfg.LLMClient.Generate(ctx, ...)
```

**LLM 类型判断**：当前 LLM 客户端是公共 LLM 还是个人 LLM，通过 `LLMFactory.HasCustomLLM(senderID)` 判断（`agent/llm_factory.go:81`）。该函数检查 `UserLLMConfig` 表中是否有该 senderID 的记录。

```go
// engine_wire.go: buildMainRunConfig 中的注入逻辑
senderID := parentMsg.OriginUserID
llmKey := "global"
if a.llmFactory.HasCustomLLM(senderID) {
    llmKey = "personal"
}

cfg.LLMSemAcquire = func(ctx context.Context) func() {
    globalConc, personalConc := a.llmFactory.GetLLMConcurrency(senderID)
    maxConc := globalConc
    if llmKey == "personal" {
        maxConc = personalConc
    }
    return a.llmSemManager.Acquire(ctx, senderID, llmKey, maxConc)
}
```

这样做的好处：
1. 不改动 `llm/` 包的接口（保持纯粹的 LLM 客户端职责）
2. 信号量可以精确地按 tenant + LLM 类型隔离
3. 方便后续扩展（如按模型类型分配不同并发配额）

#### 3.3.4 信号量容量来源

| 场景 | 信号量 key | 容量来源 |
|------|-----------|---------|
| 公共 LLM + 用户 A | `userA:global` | 用户 A 的 `llm_max_concurrent` 设置，默认 5 |
| 个人 LLM + 用户 A | `userA:personal` | 用户 A 的 `llm_max_concurrent_personal` 设置，默认 3 |
| 公共 LLM + 用户 B | `userB:global` | 用户 B 的配置，默认 5 |

### 3.4 方案 C：用户可配置并发数

#### 3.4.1 存储方案

使用现有 `user_settings` 表，新增两个 key：

| key | 类型 | 默认值 | 说明 |
|-----|------|--------|------|
| `llm_max_concurrent` | int | 5 | 公共 LLM 最大并发数（含 SubAgent + 主 Agent） |
| `llm_max_concurrent_personal` | int | 3 | 个人 LLM 最大并发数 |

无需建新表或 migration。

#### 3.4.2 信号量容量动态更新

**问题**：用户在 Settings 中修改并发数后，已创建的信号量容量不会自动更新（`make(chan struct{}, N)` 创建后容量不可变）。

**解决方案**：`LLMSemaphoreManager` 的唯一公开方法 `Acquire()` 直接封装容量检查与信号量获取。每次调用时通过 `getCapacity` 回调动态读取最新并发数：

```go
// llm/semaphore.go
func (m *LLMSemaphoreManager) Acquire(ctx context.Context, senderID, llmKey string, getCapacity func() int) func() {
    key := senderID + ":" + llmKey
    desired := getCapacity()

    // Double-check locking: 容量不匹配则重建信号量
    m.mu.RLock()
    sem := m.semaphores[key]
    m.mu.RUnlock()
    if sem == nil || cap(sem) != desired {
        m.mu.Lock()
        sem = m.semaphores[key]
        if sem == nil || cap(sem) != desired {
            newSem := make(chan struct{}, desired)
            m.semaphores[key] = newSem
            sem = newSem
        }
        m.mu.Unlock()
    }

    select {
    case sem <- struct{}{}: return func() { <-sem }
    case <-ctx.Done(): return func() {}
    }
}
```

**重建时的等待处理**：旧信号量中已有的 goroutine 持有令牌不受影响（它们会继续执行，完成后向旧信号量释放令牌——旧信号量虽然不再被新请求使用，但不会被 GC 因为 goroutine 仍持有引用）。新请求会使用新信号量。短暂过渡期内实际并发可能超过新限制值（等于旧容量 - 已释放数），但窗口极小（通常 <1s），可接受。

#### 3.4.3 读取逻辑

```go
// agent/llm_factory.go 新增方法
func (f *LLMFactory) GetLLMConcurrency(senderID string) (global, personal int) {
    settings, err := f.settingsSvc.Get("feishu", senderID)
    if err != nil || settings == nil {
        return 5, 3 // 默认值
    }
    global = parseOrDefault(settings["llm_max_concurrent"], 5)
    personal = parseOrDefault(settings["llm_max_concurrent_personal"], 3)
    return
}
```

#### 3.4.3 Settings UI

在飞书 Settings 卡片的 **模型** tab 中新增"并发限制"区块（与现有 max_context、thinking_mode 放在一起）：

```
🤖 模型
─────────────
LLM 配置: [配置] [删除]
模型: [选择模型 ▼]
最大上下文: [选择 ▼]
思考模式: [选择 ▼]
─────────────
并发限制
公共 LLM 并发: [5 ▼]    ← 新增
个人 LLM 并发: [3 ▼]    ← 新增
```

选项值：1, 2, 3, 5, 8, 10, 0(不限)

### 3.5 完整调用时序

以一个调用 3 个 SubAgent 的场景为例：

```
主 Agent (Run loop)
  │
  ├── LLM 调用 #1 (acquire global sem) → 返回 3 个 SubAgent tool calls
  │
  ├── Phase 1: 检测到 EnableConcurrentSubAgents + 3 SubAgent calls
  │   ├── go SubAgent-1 (acquire global sem) → engine.Run() → 多轮 LLM + 工具
  │   ├── go SubAgent-2 (acquire global sem) → engine.Run() → 多轮 LLM + 工具
  │   └── go SubAgent-3 (等待信号量) → (SubAgent-1 或 2 释放后 acquire) → engine.Run()
  │   [SubAgent 信号量容量 = 用户配置的 llm_max_concurrent - 1（留 1 给主 Agent）]
  │
  ├── LLM 调用 #2 (合并 3 个 SubAgent 结果) → 可能返回新 tool calls
  │   ...
  └── 最终回复
```

### 3.6 关于 SubAgent 信号量容量的计算

SubAgent 的最大并发数不能简单等于用户的 `llm_max_concurrent`，因为主 Agent 自身也需要占一个 LLM 并发槽位。

**公式**：`subAgentMaxConc = max(1, userMaxConcurrent - 1)`

但需要注意：主 Agent 在 SubAgent 运行期间通常不调用 LLM（只在合并结果时调用），所以可以更积极地利用配额。

**建议策略**：SubAgent 信号量容量 = `userMaxConcurrent`（即不为主 Agent 预留）。原因：
1. 主 Agent 在 SubAgent 执行期间不调用 LLM，无资源竞争
2. 如果用户的 llm_max_concurrent=5，说明他愿意同时有 5 个 LLM 请求在飞
3. 实际 LLM 级并发限制由更底层的 LLMSemaphoreManager 保证

---

## 4. 文件变更清单

### 4.1 新增文件

| 文件 | 说明 |
|------|------|
| `llm/semaphore.go` | `LLMSemaphoreManager`：per-tenant LLM 并发信号量管理器 |

### 4.2 修改文件

| 文件 | 变更内容 | 理由 |
|------|---------|------|
| **`agent/engine.go`** | 1. RunConfig 新增 `SubAgentSem chan struct{}` 和 `LLMSemAcquire func(context.Context) func()` 字段<br>2. 新增 `EnableConcurrentSubAgents bool` 字段<br>3. tool call 执行逻辑增加 SubAgent 并发分支：当检测到多个 SubAgent calls 且 EnableConcurrentSubAgents=true 时，使用 goroutine + SubAgentSem 并发执行<br>4. LLM 调用前调用 `cfg.LLMSemAcquire()` | 核心变更：实现并发 SubAgent 执行和 LLM 调用级并发限制 |
| **`agent/engine_wire.go`** | 1. `buildMainRunConfig()` 注入 `SubAgentSem`、`LLMSemAcquire`、`EnableConcurrentSubAgents=true`<br>2. `buildSubAgentRunConfig()` 注入 `SubAgentSem`（继承父级信号量，子 Agent 嵌套 SubAgent 也受限制）<br>3. `LLMSemAcquire` 回调从 `LLMSemaphoreManager` 获取 | 配置注入：将并发控制能力注入到 RunConfig |
| **`agent/llm_factory.go`** | 1. 新增 `LLMSemaphoreManager` 字段<br>2. 新增 `GetLLMSemaphoreManager()` 方法<br>3. 新增 `GetLLMConcurrency(senderID)` 方法，从 user_settings 读取并发配置 | 提供用户级并发配置的读取能力 |
| **`agent/agent.go`** | 1. Agent struct 新增 `llmSemManager *llm.LLMSemaphoreManager`<br>2. 初始化时创建 LLMSemaphoreManager 并传入 LLMFactory<br>3. buildMainRunConfig 时传入 | 管理 LLMSemaphoreManager 生命周期 |
| **`channel/feishu.go`** | SettingsCallbacks 新增 `LLMGetConcurrency func(senderID string) (int, int)` 和 `LLMSetConcurrency func(senderID string, global, personal int) error` | 支持飞书 Settings 卡片回调和数据查询 |
| **`channel/feishu_settings.go`** | 1. `buildModelTabContent()` 新增"并发限制"区块（两个 select_static 控件）<br>2. `HandleSettingsAction()` 新增 `settings_set_concurrency` action 处理 | Settings UI 展示并发配置 |
| **`main.go`** | SetSettingsCallbacks 时注入 LLMGetConcurrency / LLMSetConcurrency 回调 | 连接 Settings 卡片和 Agent 层 |

### 4.3 不需要修改的文件

| 文件 | 理由 |
|------|------|
| `tools/subagent.go` | SubAgentTool.Execute() 不需要改动，并发控制在上层 engine.go 实现 |
| `llm/retry.go` | RetryLLM 的 MaxConcurrent 机制保持原样，新的并发控制在更上层 |
| `storage/sqlite/user_settings.go` | 已有的通用 KV 存储，直接用 `Get/Set` 即可 |
| `session/tenant.go` | TenantSession 结构无需改动，tenant 维度通过 OriginUserID 标识 |

---

## 5. 任务拆分

### Phase 1：LLM 调用级并发限制（基础设施）

> **依赖**：无
> **目标**：为所有 LLM 调用建立 per-tenant 并发信号量

| # | 任务 | 产出 | 预估 |
|---|------|------|------|
| 1.1 | 创建 `llm/semaphore.go`：实现 `LLMSemaphoreManager` | 新文件 | 0.5h |
| 1.2 | 修改 `agent/llm_factory.go`：新增 `GetLLMConcurrency()` 方法，从 user_settings 读取配置 | 修改 | 0.5h |
| 1.3 | 修改 `agent/agent.go`：初始化 `LLMSemaphoreManager`，传入 LLMFactory | 修改 | 0.3h |
| 1.4 | 修改 `agent/engine.go`：RunConfig 新增 `LLMSemAcquire`，LLM 调用前调用 | 修改 | 0.5h |
| 1.5 | 修改 `agent/engine_wire.go`：注入 `LLMSemAcquire` 回调到 buildMainRunConfig 和 buildSubAgentRunConfig | 修改 | 0.5h |
| 1.6 | 编写单元测试 `llm/semaphore_test.go` | 新文件 | 0.5h |

### Phase 2：并发 SubAgent 执行

> **依赖**：Phase 1
> **目标**：同一轮的多个 SubAgent tool calls 并发执行

| # | 任务 | 产出 | 预估 |
|---|------|------|------|
| 2.1 | 修改 `agent/engine.go`：RunConfig 新增 `SubAgentSem` 和 `EnableConcurrentSubAgents` | 修改 | 0.3h |
| 2.2 | 修改 `agent/engine.go`：tool call 执行逻辑增加 SubAgent 并发分支 | 修改 | 1.5h |
| 2.3 | 修改 `agent/engine_wire.go`：注入 SubAgentSem 到 buildMainRunConfig / buildSubAgentRunConfig | 修改 | 0.3h |
| 2.4 | 编写集成测试：验证多个 SubAgent 并发执行、信号量限制生效 | 测试 | 1h |

### Phase 3：用户配置 + Settings UI

> **依赖**：Phase 1
> **目标**：用户可通过飞书 Settings 卡片配置并发数

| # | 任务 | 产出 | 预估 |
|---|------|------|------|
| 3.1 | 修改 `channel/feishu.go`：SettingsCallbacks 新增 LLMGetConcurrency / LLMSetConcurrency | 修改 | 0.3h |
| 3.2 | 修改 `channel/feishu_settings.go`：buildModelTabContent() 新增并发限制区块 | 修改 | 0.5h |
| 3.3 | 修改 `channel/feishu_settings.go`：HandleSettingsAction() 新增 settings_set_concurrency 处理 | 修改 | 0.3h |
| 3.4 | 修改 `main.go`：注入 SettingsCallbacks | 修改 | 0.2h |
| 3.5 | 修改 `agent/agent.go`：实现 SetLLMConcurrency 方法（写 user_settings） | 修改 | 0.3h |
| 3.6 | 端到端测试：Settings 卡片设置并发数 → 验证生效 | 测试 | 0.5h |

### 依赖关系图

```
Phase 1 (LLM Semaphore)
  ├── Phase 2 (Concurrent SubAgent) ← 依赖 Phase 1
  └── Phase 3 (Settings UI)         ← 依赖 Phase 1
```

---

## 6. 验证标准

### 6.1 功能验证

| # | 验证项 | 方法 |
|---|--------|------|
| V1 | 多 SubAgent 并发执行 | 构造一个需要调用 3 个 SubAgent 的任务，观察日志中 tool call 执行时间应接近最长那个 SubAgent 而非三者之和 |
| V2 | SubAgent 并发数受限 | 设置 `llm_max_concurrent=2`，调用 4 个 SubAgent，日志应显示同时最多 2 个在执行 |
| V3 | LLM 调用级并发限制 | 设置 `llm_max_concurrent=1`，调用 1 个 SubAgent（多轮 LLM），应串行执行所有 LLM 调用 |
| V4 | 公共/个人 LLM 并发隔离 | 公共 LLM 设置并发=2，个人 LLM 设置并发=3，验证两者互不影响 |
| V5 | Settings UI 可配置 | 飞书 Settings 卡片中修改并发数 → 重启后配置保持 → 发送消息验证生效 |
| V6 | 默认值正确 | 新用户未配置时，使用默认值（公共=5，个人=3） |

### 6.2 性能验证

| # | 验证项 | 预期 |
|---|--------|------|
| P1 | 3 个轻量 SubAgent 串行 vs 并发 | 并发耗时 ≈ 串行耗时的 1/3 |
| P2 | 信号量引入的延迟 | acquire/release 开销 < 1ms |

### 6.3 回归验证

| # | 验证项 | 方法 |
|---|--------|------|
| R1 | 单 SubAgent 行为不变 | 1 个 SubAgent 时不应启动 goroutine，走原有逻辑 |
| R2 | 读写分离不受影响 | EnableReadWriteSplit 模式下只读工具仍然并行 |
| R3 | 非 SubAgent 工具不受影响 | Shell、Edit 等工具仍然正常串行执行 |
| R4 | Interactive SubAgent 不受影响 | interactive 模式仍为同步 |

---

## 7. 风险与注意

### 7.1 高风险

| 风险 | 影响 | 缓解措施 |
|------|------|---------|
| **execResults 并发写入** | 多个 goroutine 按 index 写入 `execResults` slice，可能导致数据竞争 | `execResults` 按 index 分配，无交叉写入，已有并发安全基础。但需确认 `execOne` 中是否有共享可变状态。**需 code review** |
| **LLM Provider 限流** | 并发增加可能导致 LLM API 返回 429 | RetryLLM 已有重试+退避机制。可考虑在 429 时动态降低并发（未来优化） |
| **进度通知顺序** | 并发 SubAgent 的进度通知可能乱序 | 接受乱序。进度通知本身是即时性的，不要求严格顺序 |

### 7.2 中风险

| 风险 | 影响 | 缓解措施 |
|------|------|---------|
| **Context 取消传播** | 并发 SubAgent 中某个失败/超时，其他应继续还是取消？ | 默认：独立执行，互不影响。通过 ctx.Done() 传播父级取消 |
| **内存使用** | 并发 SubAgent 各自持有独立的 messages slice、context 等 | 每个并发 SubAgent 的内存占用与串行时相同，总量随并发数线性增长。限制默认最大并发为 5 |
| **execOne 中的 structuredProgress 并发访问** | `structuredProgress.ActiveTools` 被多个 goroutine 读写 | 需确认 SubAgent 内部 engine.Run() 是否使用独立的 structuredProgress。经分析，SubAgent 有独立的 RunConfig 和执行上下文，不共享父级的 structuredProgress。但 `execOne` 闭包中写 `progressLines[progressStartIdx+entry.index]` 需确认不同 entry 的 index 不重叠（已确认：每个 entry 有唯一的 iteration+index 组合，不会重叠） |

### 7.3 低风险

| 风险 | 影响 | 缓解措施 |
|------|------|---------|
| **向后兼容** | 不开启时行为完全不变 | `EnableConcurrentSubAgents` 默认 false，渐进式启用 |
| **用户配置为 0** | 可能表示"不限制"，需明确语义 | 0 表示使用默认值，提供"不限"选项时存储为 -1 |

### 7.4 未来优化方向

1. **动态并发调整**：根据 429 错误频率自动降低 per-tenant 并发
2. **优先级队列**：主 Agent 的 LLM 调用优先于 SubAgent
3. **并发指标监控**：在 Settings 的 metrics tab 中展示当前 LLM 并发使用情况
4. **SubAgent 结果缓存**：相同 task + role 的 SubAgent 结果可缓存复用

---

## 附录 A：关键代码段索引

| 文件 | 行号 | 内容 |
|------|------|------|
| `agent/engine.go` | 695-705 | tool call 遍历 & execResults 初始化 |
| `agent/engine.go` | 722-755 | `execOne` 函数定义（含 SubAgent 无超时处理） |
| `agent/engine.go` | 808-860 | ReadWriteSplit 并行执行逻辑 |
| `agent/engine.go` | 1057-1077 | `spawnAgentAdapter.RunSubAgent()` |
| `agent/engine.go` | 1129-1160 | `spawnAgentAdapter.buildMsg()` |
| `agent/engine_wire.go` | 196-310 | `buildSubAgentRunConfig()` |
| `agent/engine_wire.go` | 150-152 | 主 Agent SpawnAgent 注入 |
| `agent/llm_factory.go` | 113-130 | `createClient()` — 未设置 MaxConcurrent |
| `agent/agent.go` | 869 | `globalSem` 创建 |
| `agent/agent.go` | 962-981 | `getSemaphoreForMessage()` |
| `llm/retry.go` | 38-62 | RetryConfig + MaxConcurrent 信号量 |
| `llm/retry.go` | 250-265 | `Generate()` 中 acquire/release |
| `tools/subagent.go` | 62-160 | `SubAgentTool.Execute()` |
| `storage/sqlite/db.go` | 221-230 | user_settings 表结构 |
| `channel/feishu_settings.go` | 21-50 | `BuildSettingsCard()` tab 路由 |
| `channel/feishu_settings.go` | 276-340 | `buildGeneralTabContent()` 参考 |
| `channel/feishu.go` | 39-68 | `SettingsCallbacks` 结构 |
