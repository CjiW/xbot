---
title: "phase2-implementation-plan"
weight: 140
---

# Phase 2 上下文管理实施计划

> 中书省拟 | 2026-03-19
> 状态：门下省一审驳回修改（已修订）
> 前置文档：[设计文档](context-management-phase2-design.md)、[Phase 1 设计](context-management-design.md)
> 目标：将 Phase 2 四个方向（分层压缩、智能触发、话题分区、质量保障）逐步落地到代码

---

## 一、现状摘要

### 1.1 代码基线

| 组件 | 文件 | 状态 |
|------|------|------|
| ContextManager 接口 | `agent/context_manager.go` | ✅ 稳定 |
| phase1Manager | `agent/context_manager_phase1.go` | ✅ 稳定，含双视图+thinTail |
| phase2Manager | `agent/context_manager_phase2.go` | ⚠️ 空壳，ShouldCompress fallback Phase1，Compress 返回 error |
| compressMessages | `agent/compress.go` | ✅ LLM 压缩核心函数，Phase1/2 共用 |
| Run() 主循环 | `agent/engine.go` | ✅ maybeCompress 闭包，调用 ContextManager |
| Agent 结构体 | `agent/agent.go` | ✅ 持有 contextManagerConfig + RWMutex 保护 |
| ContextManagerConfig | `agent/context_manager.go` | ✅ MaxContextTokens、CompressionThreshold、DefaultMode |

### 1.2 关键约束

1. **ContextManager 接口不变**：Phase 2 实现必须满足现有接口签名
2. **phase2Manager 扩展**：内部状态需要从 Run() 循环获取（迭代计数、工具调用列表等），需通过新参数传递
3. **maybeCompress 是闭包**：捕获了 `messages`、`cfg`、`ctx` 等变量，Phase 2 的动态阈值需要在此闭包内或通过接口传递
4. **SubAgent 不需要压缩**：`buildSubAgentRunConfig` 中 ContextManager=nil
5. **session 非线程安全**：话题切换检测必须同步执行
6. **compressMessages Phase1/2 共用**：修改压缩 prompt 会同时影响 Phase1 和 Phase2（正面影响，但需验证）

### 1.3 实施策略

设计文档建议的实施顺序：**P2.1 → P2.4 → P2.2 → P2.3 → P2.6 → P2.5**

本次实施计划按此顺序编排，每个步骤独立可合并、可回滚。

---

## 二、P2.1 — 智能压缩触发（动态阈值 + 冷却机制）

> 预估：2天 | 依赖：无 | 新增文件：`agent/trigger.go`

### 2.1 目标

替换静态 0.7 阈值为动态阈值（0.5–0.85），引入压缩冷却防止无限循环。

### 2.2 新增文件：`agent/trigger.go`

包含以下组件：

#### 2.2.1 TriggerInfo 结构体

```go
type TriggerInfo struct {
    MaxTokens       int       // 最大上下文 token 数
    CurrentTokens   int       // 当前消息 token 数
    IterationCount  int       // 当前 Run() 迭代次数
    ToolCallCount   int       // 本次 Run() 的工具调用总次数
    TokenHistory    []int     // 最近 N 次迭代的 token 快照（滑窗）
    GrowthRate      float64   // 近期 token 增长速率
    RecentTools     []string  // 最近 5 次工具调用名称
    ToolPattern     ToolCallPattern
}
```

#### 2.2.2 ToolCallPattern 枚举

```go
type ToolCallPattern int
const (
    PatternConversation ToolCallPattern = iota
    PatternReadHeavy
    PatternWriteHeavy
    PatternMixed
    PatternSubAgent
)
```

#### 2.2.3 核心函数

| 函数 | 签名 | 说明 |
|------|------|------|
| `calculateDynamicThreshold` | `(info TriggerInfo) float64` | 三因子（阶段/增长/模式）动态阈值 |
| `DetectToolPattern` | `(recentTools []string) ToolCallPattern` | 5种工具模式检测 |
| `isScanningPhase` | `(recentTools []string) bool` | 纯读取扫描阶段检测 |

#### 2.2.4 TokenGrowthTracker

```go
type TokenGrowthTracker struct {
    window  []tokenSnapshot  // 滑动窗口（默认 10）
    maxSize int
}
```

- `Record(iteration, tokens)` — 记录快照
- `GrowthRate()` — 加权线性回归斜率
- `IsExponentialGrowth()` — 指数增长检测（加速增长比率）
- `Snapshots()` — 返回窗口快照
- `Reset()` — 清空窗口

#### 2.2.5 CompressCooldown

```go
type CompressCooldown struct {
    lastCompressIteration int
    cooldownIterations    int  // 默认 3
}
```

- `ShouldTrigger(currentIteration)` — 判断是否可触发
- `RecordCompress(iteration)` — 记录触发
- `Reset()` — 重置状态

### 2.3 修改点

#### 2.3.1 `agent/context_manager.go` — ContextManager 接口扩展

**当前 ShouldCompress 签名**：
```go
ShouldCompress(messages []llm.ChatMessage, model string, toolTokens int) bool
```

**问题**：动态阈值需要 TriggerInfo，但接口签名只有基础参数。

**方案**：引入新接口 `SmartCompressor`，phase2Manager 额外实现此接口：

```go
// SmartCompressor 智能压缩接口（Phase 2 扩展）
type SmartCompressor interface {
    // ShouldCompressDynamic 使用完整触发信息判断是否需要压缩
    ShouldCompressDynamic(info TriggerInfo) bool
    // Provider 返回触发信息提供者（用于 engine.go 填充 GrowthTracker 和 Cooldown）
    Provider() *TriggerInfoProvider
}

// TriggerInfoProvider 提供压缩触发信息
type TriggerInfoProvider struct {
    GrowthTracker *TokenGrowthTracker
    Cooldown      *CompressCooldown
}

func NewTriggerInfoProvider() *TriggerInfoProvider {
    return &TriggerInfoProvider{
        GrowthTracker: &TokenGrowthTracker{maxSize: 10},
        Cooldown:      &CompressCooldown{cooldownIterations: 3},
    }
}

// Reset 重置所有状态（用于 /new 命令）
func (p *TriggerInfoProvider) Reset() {
    p.GrowthTracker.Reset()
    p.Cooldown.Reset()
}
```

Run() 中的 maybeCompress 通过类型断言检测是否支持智能触发：
```go
if smart, ok := cm.(SmartCompressor); ok {
    // 每次 Run() 开始时获取 Provider，确保引用最新
    provider := smart.Provider()
    triggerInfo := BuildTriggerInfo(i, messages, toolsUsed, provider, cfg)
    shouldCompress = smart.ShouldCompressDynamic(triggerInfo)
    // 无论是否压缩，都记录 token 快照
    provider.GrowthTracker.Record(i, msgTokens)
} else {
    shouldCompress = cm.ShouldCompress(messages, cfg.Model, toolTokens)
}
```

**影响评估**：此方案零改动现有接口，Phase1 和 noopManager 完全不受影响。

#### 2.3.2 `agent/engine.go` — maybeCompress 增强

在 maybeCompress 闭包中：
1. 检测 ContextManager 是否实现 SmartCompressor
2. 若是，构造 TriggerInfo 并调用 ShouldCompressDynamic
3. 记录 token 快照到 GrowthTracker（无论是否压缩）

#### 2.3.3 `agent/context_manager_phase2.go` — phase2Manager 增强

- 实现 `SmartCompressor` 接口
- 持有 `*TriggerInfoProvider`
- ShouldCompress 仍可用（作为 fallback）
- ShouldCompressDynamic 实现动态阈值逻辑

#### 2.3.4 `agent/agent.go` — Agent 结构体扩展

> [修订 A] 明确 TriggerInfoProvider 的生命周期管理

```go
type Agent struct {
    // ... 现有字段 ...

    // Phase 2: 智能触发状态（按 sessionKey 索引）
    triggerProviders sync.Map // map[string]*TriggerInfoProvider，key = sessionKey
}

// getTriggerProvider 获取或创建指定 session 的 TriggerInfoProvider
func (a *Agent) getTriggerProvider(sessionKey string) *TriggerInfoProvider {
    if v, ok := a.triggerProviders.Load(sessionKey); ok {
        return v.(*TriggerInfoProvider)
    }
    provider := NewTriggerInfoProvider()
    actual, _ := a.triggerProviders.LoadOrStore(sessionKey, provider)
    return actual.(*TriggerInfoProvider)
}
```

**生命周期管理**：

| 事件 | 操作 |
|------|------|
| 每次 Run() 开始 | 从 `triggerProviders` 获取 Provider，注入 phase2Manager |
| `/new` 命令 | `triggerProviders.Delete(sessionKey)` 清理旧状态 |
| `/compress` 手动压缩 | 通过 Provider 记录冷却状态 |
| Agent 销毁 | triggerProviders 自动随 Agent GC |

#### 2.3.5 `/new` 命令清理

在 `handleNew` 中添加：
```go
a.triggerProviders.Delete(sessionKey)
```

### 2.4 新增依赖

无。全部纯 Go 实现，无外部依赖。

### 2.5 验证标准

| # | 验证项 | 方法 |
|---|--------|------|
| 1 | 动态阈值在扫描阶段低于标准 | 构造 PatternReadHeavy 的 TriggerInfo，阈值应 ≤0.65 |
| 2 | 冷却机制阻止连续压缩 | 连续两次 ShouldCompressDynamic，第二次应返回 false |
| 3 | 指数增长检测 | 构造加速增长的 tokenHistory，IsExponentialGrowth 应返回 true |
| 4 | Phase1/None 不受影响 | 切换 mode=phase1/none，原有行为不变 |
| 5 | 单元测试覆盖 | trigger.go 测试覆盖率 > 85% |
| 6 | Provider 跨 Run() 持久化 | 连续两次 Run() 使用同一 Provider，GrowthTracker 数据持续 |
| 7 | /new 重置 Provider | /new 后 Provider 被 Delete，下次 Run() 创建新实例 |

### 2.6 回滚方案

1. 从 phase2Manager 移除 SmartCompressor 实现
2. maybeCompress 恢复原始 ShouldCompress 调用
3. Agent 结构体移除 triggerProviders 字段
4. /new 清理逻辑移除

---

## 三、P2.4 — 压缩质量保障（指纹 + 评分 + 结构化标记）

> 预估：2.5天 | 依赖：无 | 新增文件：`agent/quality.go`

> [修订 G] 时间估算从 2天调整为 2.5天，含 Phase1 兼容性量化验证

### 3.1 目标

让压缩结果可审计、可度量，关键信息不丢失。

### 3.2 新增文件：`agent/quality.go`

#### 3.2.1 KeyInfoFingerprint 结构体

```go
type KeyInfoFingerprint struct {
    FilePaths    []string `json:"file_paths"`
    Identifiers  []string `json:"identifiers"`
    Errors       []string `json:"errors"`
    Decisions    []string `json:"decisions"`
}
```

#### 3.2.2 核心函数

| 函数 | 说明 |
|------|------|
| `ExtractFingerprint(messages)` | 从消息列表提取关键信息指纹 |
| `ValidateCompression(original, compressed, fp)` | 校验压缩后信息保留率，返回 (保留率, 丢失列表) |
| `EvaluateQuality(originalTokens, compressedTokens, fp, compressed)` | 综合质量评分 (0-1) |
| `containsSemanticMatch(compressed, target)` | 语义模糊匹配（归一化子串 + 关键词重叠度） |

#### 3.2.3 辅助函数

| 函数 | 说明 |
|------|------|
| `extractFilePaths(text)` | 正则提取文件路径 |
| `extractCodeIdentifiers(text)` | 提取驼峰/下划线标识符 |
| `isErrorContext(text)` | 检测错误上下文 |
| `extractErrorMessages(text)` | 提取错误描述 |
| `extractDecisions(text)` | 提取决策记录 |
| `splitToWords(text)` | 文本分词（去停用词） |
| `countStructuredMarkers(text)` | 统计 @file: 等标记数量 |

### 3.3 修改点

#### 3.3.1 `agent/compress.go` — 结构化压缩 prompt

替换 `compressMessages` 中的 `compressionPrompt` 常量为结构化版本：

```go
const structuredCompressionPrompt = `You are a context compression expert...

## OUTPUT FORMAT
Use these markers:
@file:{path} — File references
@func:{name} — Function signatures
@type:{name} — Type definitions
@error:{description} — Errors encountered
@decision:{description} — Decisions made
@todo:{description} — Pending tasks
@config:{key=value} — Config changes
...`
```

**兼容性说明**：
- `compressMessages` 是 Phase1/Phase2 **共用**函数
- 替换 prompt 常量**同时影响 Phase1 和 Phase2**
- **正面影响**：结构化 prompt 产出更好格式的摘要，Phase1 用户也受益
- **风险缓解**：结构化 prompt 比原始 prompt 长度增加约 200 tokens，但 input token 消耗增加 < 2%，在可接受范围
- **量化验证**：Phase1 压缩后 token 数不应比原始 prompt 增加 > 5%（见验证项 6）

#### 3.3.2 `agent/context_manager_phase2.go` — Compress 增强版

phase2Manager.Compress 实现：
1. 压缩前调用 ExtractFingerprint
2. 调用 compressMessages（Phase1 逻辑）
3. 调用 EvaluateQuality 评估
4. 若质量分 < 0.6 且关键信息保留率 < 0.8，使用增强 prompt 重新压缩（最多1次）
5. 记录质量日志

#### 3.3.3 `agent/compress.go` — extractDialogueFromTail 增强

识别 `📂 [offload:...]` 标记，不对其二次截断（为 P2.2 预留）：
```go
case msg.Role == "tool":
    if strings.HasPrefix(msg.Content, "📂 [offload:") {
        // 保留 offload 摘要完整，不截断
        pendingToolSummary.WriteString(msg.Content + "\n")
    } else {
        toolContent := truncateRunes(msg.Content, 200)
        ...
    }
```

### 3.4 验证标准

| # | 验证项 | 方法 |
|---|--------|------|
| 1 | 指纹提取准确性 | 构造含文件路径/错误的消息，验证 ExtractFingerprint 结果 |
| 2 | 质量评分合理 | 构造高质量和低质量压缩输出，评分差异 > 0.3 |
| 3 | 结构化 prompt 输出 | 实际 LLM 调用验证输出含 @file: 等标记 |
| 4 | 重新压缩逻辑 | 模拟低质量首次压缩，验证增强版触发 |
| 5 | 不影响 Phase1 功能 | mode=phase1 时，compressMessages 正常工作 |
| 6 | Phase1 token 增量 < 5% | 结构化 prompt 导致的 Phase1 压缩后 token 增加不超过 5% |

### 3.5 回滚方案

1. 恢复原始 `compressionPrompt` 常量
2. phase2Manager.Compress 恢复直接调用 compressMessages（跳过指纹/评分）
3. 移除 `extractDialogueFromTail` 中的 offload 标记识别（恢复为统一截断逻辑）

---

## 四、P2.2 — Layer 1 Offload（大 tool result 落盘）

> 预估：2天 | 依赖：无 | 新增文件：`agent/offload.go`、`tools/offload_recall.go`

### 4.1 目标

单条 tool result 超 2000 tokens 时自动落盘，替换为规则摘要 + 可召回标记。

### 4.2 新增文件：`agent/offload.go`

#### 4.2.1 核心结构

```go
type OffloadConfig struct {
    MaxResultTokens int    // 默认 2000
    MaxResultBytes  int    // 默认 10240
    StoreDir        string // 默认 {DataDir}/offload_store/
    CleanupAgeDays  int    // 默认 7（启动时清理 N 天前的残留数据）
}

type OffloadedResult struct {
    ID        string
    ToolName  string
    Args      string
    FilePath  string
    TokenSize int
    Timestamp time.Time
    Summary   string
}

type OffloadStore struct {
    config    OffloadConfig
    sessionMu sync.Map // map[sessionKey]*sessionIndex
}

type offloadIndex struct {
    mu      sync.RWMutex
    entries []OffloadedResult
}
```

#### 4.2.2 核心函数

| 函数 | 说明 |
|------|------|
| `NewOffloadStore(config)` | 创建 offload store |
| `(s *OffloadStore) MaybeOffload(sessionKey, toolName, args, result string) (OffloadedResult, bool)` | 检测并执行 offload，返回 (结果, 是否offload) |
| `(s *OffloadStore) Recall(sessionKey, id string) (string, error)` | 按 ID 召回完整内容 |
| `(s *OffloadStore) CleanSession(sessionKey)` | 清理指定 session 的 offload 数据 |
| `(s *OffloadStore) CleanStale()` | 启动时清理超过 CleanupAgeDays 天的残留数据 |

#### 4.2.3 规则摘要生成（同步，无 LLM 依赖）

```go
func generateRuleSummary(toolName, args, content string) string {
    switch {
    case toolName == "Read":
        return summarizeFileRead(args, content)    // 文件名+行数+首尾+关键函数
    case toolName == "Grep":
        return summarizeGrepResult(content)         // 匹配数+前3条
    case toolName == "Shell":
        return summarizeShellOutput(content)         // 退出码+最后5行
    default:
        return summarizeGeneric(content)             // 前300字符+N tokens已offload
    }
}
```

#### 4.2.4 残留数据清理

> [修订 J] 增加异常退出后残留清理机制

```go
// CleanStale 在 Agent 启动时调用，清理超过 CleanupAgeDays 天的 offload 数据
func (s *OffloadStore) CleanStale() {
    cutoff := time.Now().AddDate(0, 0, -s.config.CleanupAgeDays)
    // 遍历 StoreDir，删除 mtime < cutoff 的 session 目录
    // 记录清理日志
}
```

### 4.3 新增文件：`tools/offload_recall.go`

> [修订 A] 补充注册机制和 sessionKey 获取路径

```go
type OffloadRecallTool struct {
    store *agent.OffloadStore // 依赖注入
}

func (t *OffloadRecallTool) Name() string { return "offload_recall" }
func (t *OffloadRecallTool) Description() string { return "召回已 offload 的工具结果完整内容" }
func (t *OffloadRecallTool) Parameters() []llm.ToolParam {
    return []llm.ToolParam{
        {Name: "id", Type: "string", Description: "offload ID（从工具结果中的 📂 标记获取）", Required: true},
    }
}

func (t *OffloadRecallTool) Execute(ctx *tools.ToolContext, args string) (*tools.ToolResult, error) {
    // 1. 解析 args 获取 ID
    var params struct{ ID string `json:"id"` }
    if err := json.Unmarshal([]byte(args), &params); err != nil {
        return nil, fmt.Errorf("invalid args: %w", err)
    }

    // 2. 从 ToolContext 获取 sessionKey
    //    ToolContext 包含 session 信息，通过 ctx.SessionID() 或类似方法获取
    sessionKey := ctx.SessionID()
    if sessionKey == "" {
        return nil, fmt.Errorf("offload_recall requires a session context")
    }

    // 3. 调用 store.Recall(sessionKey, id)
    content, err := t.store.Recall(sessionKey, params.ID)
    if err != nil {
        return nil, fmt.Errorf("offload recall failed: %w", err)
    }

    // 4. 返回完整内容（如果超过 8000 字符则截断并提示）
    if len([]rune(content)) > 8000 {
        content = content[:8000] + "\n\n[内容过长，已截断。如需更多请指定范围。]"
    }

    return &tools.ToolResult{LLMContent: content}, nil
}
```

**注册方式**：在 `agent/agent.go` 的 `New()` 中，与现有工具一起注册：
```go
// Phase 2: offload_recall 工具（仅主 Agent 使用）
if a.offloadStore != nil {
    recallTool := &tools.OffloadRecallTool{Store: a.offloadStore}
    a.toolsRegistry.Register(recallTool)
}
```

> [修订 I] **SubAgent 不包含 offload_recall 工具**：
> - offload_recall 仅注册在主 Agent 的 toolsRegistry 中
> - SubAgent 通过 `buildSubAgentRunConfig` 获取独立的工具子集（排除 offload_recall）
> - 原因：SubAgent 不使用 offload_store（无 ContextManager、无 OffloadStore），调用会因找不到 sessionKey 对应的索引而失败

### 4.4 修改点

#### 4.4.1 `agent/engine.go` — execOne 后插入 offload 逻辑

在工具执行完成后、消息追加前：

```go
// 工具执行结果处理循环中
for idx, tc := range response.ToolCalls {
    r := execResults[idx]
    content := r.llmContent

    // === Phase 2: Layer 1 Offload ===
    if offloadStore != nil && !r.err {
        offloaded, wasOffloaded := offloadStore.MaybeOffload(sessionKey, tc.Name, tc.Arguments, content)
        if wasOffloaded {
            content = offloaded.Summary
            log.Ctx(ctx).WithFields(log.Fields{
                "tool":         tc.Name,
                "offload_id":   offloaded.ID,
                "tokens_saved": offloaded.TokenSize,
            }).Info("Tool result offloaded")
        }
    }

    toolMsg := llm.NewToolMessage(tc.Name, tc.ID, tc.Arguments, content)
    ...
}
```

**注意**：offloadStore 需要通过 RunConfig 传入，类似 ContextManager 的模式。

#### 4.4.2 `agent/agent.go` — Agent 初始化 OffloadStore

在 `New()` 中创建 OffloadStore，通过 buildMainRunConfig 注入到 RunConfig：
```go
a.offloadStore = agent.NewOffloadStore(agent.OffloadConfig{
    StoreDir:       filepath.Join(a.dataDir, "offload_store"),
    CleanupAgeDays: 7,
})
a.offloadStore.CleanStale() // 启动时清理残留
```

#### 4.4.3 `agent/engine.go` — RunConfig 扩展

```go
type RunConfig struct {
    // ... 现有字段 ...
    OffloadStore *agent.OffloadStore // Phase 2: offload store（nil = 不启用）
}
```

#### 4.4.4 `/new` 命令清理 offload

在 handleNew 中调用：
```go
if a.offloadStore != nil {
    a.offloadStore.CleanSession(sessionKey)
}
```

### 4.5 存储结构

```
offload_store/
  └── {session_key}/
        ├── offload_abc123.json   ← 完整 tool result
        ├── offload_def456.json
        └── index.json            ← 索引（所有条目元数据）
```

### 4.6 验证标准

| # | 验证项 | 方法 |
|---|--------|------|
| 1 | 大结果自动 offload | 构造 >2000 tokens 的工具结果，验证落盘+摘要替换 |
| 2 | 小结果不 offload | <2000 tokens 的结果应原样保留 |
| 3 | offload_recall 召回 | 通过工具调用 offload_recall 获取完整内容 |
| 4 | offload_recall sessionKey 隔离 | 不同 session 的 offload 数据不互通 |
| 5 | 摘要质量 | Read 类摘要包含文件名+行数+关键函数名 |
| 6 | /new 清理 | 切换会话后 offload_store 对应目录已清理 |
| 7 | 磁盘写入性能 | offload 写入 < 10ms（JSON 序列化+单文件） |
| 8 | SubAgent 无 offload_recall | 验证 SubAgent 工具列表中不包含 offload_recall |
| 9 | 残留清理 | 构造 N+1 天前的 offload 目录，启动后自动清理 |

### 4.7 回滚方案

> [修订 F] 补充数据残留清理步骤

1. `RunConfig.OffloadStore` 置 nil
2. `tools/offload_recall.go` 文件不注册（移除注册代码）
3. **残留数据清理**（可选，回滚后执行一次）：
   ```bash
   rm -rf {DataDir}/offload_store/
   ```
   或通过配置 `OffloadStore.CleanStale()` 在下次启动时自动清理

---

## 五、P2.3 — Layer 2 Evict（信息密度驱逐）

> 预估：2天 | 依赖：无硬依赖，建议在 P2.2 之后实施 | 新增文件：无（修改 `agent/compress.go`）

> [修订 C] 修正依赖声明：P2.3 虽然不硬依赖 P2.2，但设计文档 §3.5 明确说 evictByDensity 需正确处理"可能已包含 Offload 后的摘要消息"（`📂 [offload:...]` 标记），因此建议在 P2.2 之后实施

### 5.1 目标

升级 thinTail 为基于信息密度评分的选择性驱逐。

### 5.2 修改点

#### 5.2.1 `agent/compress.go` — 新增 evictByDensity 函数

```go
// DensityScore 信息密度评分
type DensityScore struct {
    Score float64
    Index int
}

// defaultDensityScorer 默认信息密度评分
func defaultDensityScorer(msg llm.ChatMessage) float64 {
    score := 0.0
    content := msg.Content

    // Offload 标记消息：已是摘要，不再驱逐（保留引用标记供 offload_recall 使用）
    if strings.HasPrefix(content, "📂 [offload:") {
        return 1.0 // 中性分数，不会被优先驱逐
    }

    // 高密度信号
    if containsErrorPattern(content)    { score += 3.0 }
    if containsDecisionPattern(content) { score += 2.5 }
    if containsFilePath(content)        { score += 1.0 }
    if len([]rune(content)) < 500       { score += 1.5 }

    // 低密度信号
    if isLargeCodeDump(content)         { score -= 2.0 }
    if isRepetitiveGrepResult(content)  { score -= 1.5 }
    if msg.Role == "tool" && len([]rune(content)) > 3000 { score -= 2.0 }

    return score
}

// evictByDensity 按信息密度驱逐旧 tool result
func evictByDensity(messages []llm.ChatMessage, keepGroups int, targetTokens int, model string) []llm.ChatMessage {
    // 1. 识别工具组（复用 thinTail 的组识别逻辑）
    // 2. 保留尾部 keepGroups 组完整
    // 3. 对剩余 tool 消息按密度评分排序
    // 4. 从低密度开始驱逐：替换 tool result 为 "[evicted] 工具摘要（N tokens 已驱逐）"
    // 5. 跳过含有 📂 [offload:...] 标记的消息（已是摘要，驱逐无意义）
    // 6. 驱逐直到 targetTokens 以下
}
```

#### 5.2.2 `agent/context_manager_phase2.go` — Compress 流水线

phase2Manager.Compress 实现 Evict → Compact 两阶段流水线：

```go
func (m *phase2Manager) Compress(ctx context.Context, messages []llm.ChatMessage, client llm.LLM, model string) (*CompressResult, error) {
    // Phase 1: Evict（信息密度驱逐）
    targetTokens := int(float64(m.config.MaxContextTokens) * 0.7)
    evicted := evictByDensity(messages, 3, targetTokens, model)
    evictTokens, _ := llm.CountMessagesTokens(evicted, model)

    // Phase 2: Compact（如果 Evict 后仍超阈值）
    if evictTokens >= int(float64(m.config.MaxContextTokens)*m.config.CompressionThreshold) {
        return compressMessages(ctx, evicted, client, model)
    }

    // Evict 后已足够，构建双视图
    return buildCompressResultFromEvicted(evicted), nil
}
```

### 5.3 与 thinTail 的关系

- **不删除** thinTail 函数（Phase1 仍使用）
- evictByDensity 是 thinTail 的增强版，Phase2 使用 evictByDensity
- 两者逻辑独立，互不影响

### 5.4 验证标准

| # | 验证项 | 方法 |
|---|--------|------|
| 1 | 低密度内容优先驱逐 | 大代码块分数低于错误消息 |
| 2 | 高密度内容保留 | 错误信息、决策记录评分 > 2.0 |
| 3 | Evict 后不超阈值 | 验证驱逐后 token 数 < targetTokens |
| 4 | 保留 tool 消息结构 | API 兼容：保留 assistant(tool_calls) + tool call_id |
| 5 | Phase1 不受影响 | mode=phase1 时仍使用 thinTail |
| 6 | Offload 标记消息不被驱逐 | 包含 `📂 [offload:...]` 的 tool result 获得中性分数，不被 evict |

### 5.5 回滚方案

phase2Manager.Compress 恢复为直接调用 compressMessages。

---

## 六、P2.6 — 结构化压缩标记增强

> 预估：1天 | 依赖：P2.4 | 修改文件：`agent/compress.go`、`agent/quality.go`

### 6.1 目标

在 P2.4 引入的结构化 prompt 基础上，强化标记要求并集成到压缩流程。

### 6.2 修改点

#### 6.2.1 `agent/compress.go` — 增强版压缩 prompt

P2.4 已引入结构化 prompt，P2.6 进一步优化：
- 增加 "MUST preserve ALL @error: items" 强调
- 增加话题分隔指导（"如果涉及多个话题，使用 ## header 分隔"）
- 增加 offload 引用保留指导

#### 6.2.2 `agent/quality.go` — 标记检测增强

增加标记完整性检测：
```go
// ValidateMarkers 检查压缩输出是否包含必要的结构化标记
func ValidateMarkers(compressed string, fp KeyInfoFingerprint) []string {
    var missing []string
    for _, path := range fp.FilePaths {
        if !strings.Contains(compressed, "@file:"+path) && !strings.Contains(compressed, path) {
            missing = append(missing, "文件: "+path)
        }
    }
    for _, err := range fp.Errors {
        if !strings.Contains(compressed, "@error:") && !containsSemanticMatch(compressed, err) {
            missing = append(missing, "错误: "+err)
        }
    }
    return missing
}
```

### 6.3 验证标准

| # | 验证项 | 方法 |
|---|--------|------|
| 1 | LLM 输出含 @file: 标记 | 实际调用验证 |
| 2 | @error: 标记强制保留 | 含错误的输入，压缩后必含 @error: |
| 3 | 多话题分隔 | 输入跨话题对话，压缩后含 ## header |
| 4 | Phase1 兼容性 | mode=phase1 时，增强 prompt 不影响现有压缩功能 |
| 5 | 标记降级行为 | LLM 不遵守标记时，质量评分 < 0.6 触发重新压缩；重试仍失败则接受当前结果并记录告警日志 |
| 6 | Prompt 增长影响 | 增强 prompt 导致的 LLM input token 增加不超过 3%（相比 P2.4 版本） |

### 6.4 回滚方案

恢复 P2.4 版本的 compressionPrompt。

---

## 七、P2.5 — 话题分区隔离

> 预估：4天 | 依赖：P2.1 | 新增文件：`agent/topic.go`

> [修订 G] 时间估算从 3天调整为 4天，含 CJK bigram 测试、processMessage 集成、防误判充分测试

### 7.1 目标

自动检测话题切换，选择性压缩历史话题，保留当前话题完整。

### 7.2 新增文件：`agent/topic.go`

#### 7.2.1 话题检测器

```go
type TopicDetector struct {
    CosineThreshold float64  // 默认 0.3
    MinSegmentSize  int      // 默认 3
}

type TopicSegment struct {
    ID           string
    StartIdx     int
    EndIdx       int
    MessageCount int
    Keywords     []string
    Summary      string
    IsCurrent    bool
}
```

核心函数：

| 函数 | 说明 |
|------|------|
| `(d *TopicDetector) Detect(messages)` | 检测话题边界，返回片段列表 |
| `extractKeywords(text)` | 轻量关键词提取（中英文 bigram） |
| `extractCJKChars(text)` | 提取 CJK 字符序列 |
| `cosineSimilarity(a, b []string)` | 余弦相似度（关键词向量） |
| `groupIntoTurns(messages)` | 按对话轮次分组 |

#### 7.2.2 选择性压缩器

```go
type SelectiveCompressor struct {
    detector *TopicDetector
}

func (sc *SelectiveCompressor) Compress(ctx, messages, client, model) (*CompressResult, error)
```

逻辑：
1. 检测话题分区
2. 单话题 → 走标准压缩
3. 多话题 → 压缩历史话题，保留当前话题完整
4. 构建话题标注的双视图

### 7.3 修改点

#### 7.3.1 `agent/agent.go` — 话题切换检测集成

在 `processMessage` 中，buildPrompt 之后、Run 之前：

```go
// Phase 2: 话题切换检测（同步压缩）
if a.topicDetector != nil && a.enableTopicIsolation {
    history, _ := tenantSession.GetMessages()
    if len(history) > 10 {
        segments := a.topicDetector.Detect(history)
        if len(segments) > 1 && segments[len(segments)-1].MessageCount <= 2 {
            // 同步压缩（不能异步，session 非线程安全）
            result, err := a.selectiveCompressor.Compress(ctx, history, client, model)
            if err == nil {
                tenantSession.Clear()
                for _, msg := range result.SessionView {
                    tenantSession.AddMessage(msg)
                }
            }
        }
    }
}
```

> **TopicDetector 线程安全**：TopicDetector 是无状态的纯算法对象（所有方法均为值接收者或无共享状态），天然支持并发。session 数据通过 Agent 的 RWMutex 保护。

#### 7.3.2 `agent/context_manager_phase2.go` — 话题感知压缩

phase2Manager.Compress 在 Compact 阶段使用 SelectiveCompressor 替代直接 compressMessages。

#### 7.3.3 Agent 结构体扩展

```go
type Agent struct {
    // ... 现有字段 ...
    topicDetector        *TopicDetector
    selectiveCompressor  *SelectiveCompressor
    enableTopicIsolation bool
}
```

### 7.4 防误判机制

| 防护层 | 阈值 | 作用 |
|--------|------|------|
| 最小历史 | `len(history) > 10` | 历史太短不检测 |
| 最小片段 | `MinSegmentSize = 3` | 避免碎片化 |
| 相似度阈值 | `CosineThreshold = 0.3` | 保守判定 |
| 新话题短 | `MessageCount <= 2` | 只在刚切换时触发 |
| 压缩失败降级 | 错误时继续完整历史 | 不影响主流程 |

### 7.5 配置项

```go
EnableTopicIsolation        bool    `json:"enable_topic_isolation"`     // 默认 false（实验性）
TopicMinSegmentSize         int     `json:"topic_min_segment_size"`     // 默认 3
TopicSimilarityThreshold    float64 `json:"topic_similarity_threshold"` // 默认 0.3
```

### 7.6 验证标准

| # | 验证项 | 方法 |
|---|--------|------|
| 1 | 单话题不触发 | 连续同一话题对话，不触发分区压缩 |
| 2 | 话题切换检测 | 切换话题后，第二次消息触发压缩 |
| 3 | 当前话题保留 | 压缩后当前话题消息完整保留 |
| 4 | 历史话题有标注 | 压缩摘要含 `## 📁 Topic: xxx` 格式 |
| 5 | 中文话题检测 | 中文对话能检测到话题切换 |
| 6 | 防误判 | 短历史（<10条）不触发；碎片片段不触发 |
| 7 | 大会话性能 | 100 条消息的话题检测耗时 < 50ms |

### 7.7 回滚方案

`EnableTopicIsolation = false` 即完全禁用，无代码变更。

---

## 八、文件变更总览

| 文件 | 变更类型 | 阶段 | 说明 |
|------|---------|------|------|
| `agent/trigger.go` | **新增** | P2.1 | 动态阈值、增长追踪、冷却机制、工具模式检测 |
| `agent/quality.go` | **新增** | P2.4 | 指纹提取、质量评分、语义匹配、结构化标记 |
| `agent/offload.go` | **新增** | P2.2 | Offload store、规则摘要、召回、残留清理 |
| `agent/topic.go` | **新增** | P2.5 | 话题检测、中英文分词、选择性压缩 |
| `tools/offload_recall.go` | **新增** | P2.2 | offload_recall 工具（仅主 Agent 注册） |
| `agent/context_manager.go` | 修改 | P2.1 | 新增 SmartCompressor 接口 |
| `agent/context_manager_phase2.go` | 修改 | 全部 | 实现 Phase2 Compress、SmartCompressor |
| `agent/compress.go` | 修改 | P2.3/P2.4/P2.6 | evictByDensity、结构化 prompt、offload 标记识别 |
| `agent/engine.go` | 修改 | P2.1/P2.2 | maybeCompress 增强、RunConfig 扩展、offload 插入 |
| `agent/agent.go` | 修改 | P2.1/P2.2/P2.5 | TriggerProviders、OffloadStore、话题检测器、配置项 |

---

## 九、依赖关系图

```
P2.1 智能触发 ──────────────────────────┐
                                         ↓
P2.4 质量保障 ─────→ P2.6 结构化标记     │
                                         │
P2.2 Offload ──────→ P2.3 Evict* ────────┤
        (*建议顺序，非硬依赖)             │
                                         ↓
                              P2.5 话题分区 ──→ Phase2 完成
```

**关键路径**：P2.1 → P2.5（话题分区依赖智能触发）

**并行可做**：P2.1 + P2.2 + P2.4 之间无依赖，可并行开发

> [修订 C] P2.3 依赖声明已修正为"建议在 P2.2 之后"，但仍可与其他阶段并行

---

## 十、集成测试计划

每个阶段合并后执行以下集成测试：

| # | 测试场景 | 验证目标 |
|---|---------|---------|
| 1 | 10轮 Read 工具调用（大文件） | P2.2 Offload 触发 + P2.1 动态阈值降低 |
| 2 | 30轮混合工具调用 | P2.3 Evict 触发 + P2.4 质量评分 |
| 3 | 话题切换（先讨论 A，再讨论 B） | P2.5 分区压缩 |
| 4 | 长任务稳定运行（50轮迭代） | P2.1 冷却机制 + 动态阈值 |
| 5 | mode 切换（phase1 ↔ phase2） | 运行时切换无异常 |
| 6 | `/new` 命令 | offload store 清理 + TriggerProvider 重置 + 状态清零 |
| 7 | `/compress` 手动命令 | ManualCompress 走 Phase2 流水线 |
| 8 | SubAgent 运行 | SubAgent 不触发 Phase2 逻辑，无 offload_recall 工具 |
| 9 | 异常重启后残留清理 | 构造过期的 offload 数据，重启后自动清理 |

---

## 十一、风险评估与缓解

| 风险 | 概率 | 影响 | 缓解 |
|------|------|------|------|
| 动态阈值过于激进导致频繁压缩 | 中 | 用户体验差 | 冷却机制 + 最小阈值 0.5 + 可配置 |
| Offload 磁盘占用增长 | 低 | 磁盘空间 | /new 清理 + CleanStale 启动清理 + 可配置 |
| 异常退出导致 offload 残留 | 中 | 磁盘空间 | CleanStale 启动时清理 N 天前数据 |
| 话题检测中文精度不足 | 中 | 误判话题切换 | bigram 对边界检测够用 + 5层防误判 + 默认关闭 |
| 结构化 prompt LLM 不遵守 | 中 | 质量评分低 | 质量评分检测 + 重新压缩 + 降级为无标记 + 告警日志 |
| evictByDensity 性能 | 低 | 每轮增加计算 | O(n) 遍历 + 仅在压缩时触发 |
| SmartCompressor 接口设计不当 | 低 | 后续重构成本 | 接口极简（2个方法），改动小 |
| TriggerInfoProvider 跨 Run() 状态泄漏 | 低 | 阈值计算偏差 | /new 时 Delete，Provider.Reset() 可用 |
| 结构化 prompt 影响 Phase1 token 消耗 | 中 | 成本增加 | 量化验证 < 5% + prompt 增量控制在 200 tokens 内 |
| 话题检测在大会话中性能退化 | 低 | 响应变慢 | CJK bigram 为 O(n)，100条 < 50ms，超出阈值可优化 |

---

## 十二、里程碑与时间线

> [修订 G] 总工期调整为 16 个工作日（增加 P2.4 和 P2.5 的缓冲）

| 阶段 | 开始 | 完成 | 产出 |
|------|------|------|------|
| **P2.1 智能触发** | D1 | D2 | trigger.go + engine.go 修改 + Agent 生命周期管理 + 测试 |
| **P2.4 质量保障** | D3 | D5 | quality.go + compress.go 修改 + Phase1 兼容性验证 + 测试 |
| **P2.2 Offload** | D6 | D7 | offload.go + offload_recall.go + 注册机制 + SubAgent 隔离 + 测试 |
| **P2.3 Evict** | D8 | D9 | compress.go evict + Offload 标记兼容 + phase2 流水线 + 测试 |
| **P2.6 结构化标记** | D10 | D10 | prompt 优化 + 标记验证 + 降级行为 + 测试 |
| **P2.5 话题分区** | D11 | D14 | topic.go + CJK 测试 + processMessage 集成 + 防误判测试 + 性能测试 |
| **全量集成测试** | D15 | D15 | 9个集成场景 + 文档更新 |
| **代码审查 + 合并** | D16 | D16 | PR 审查 + 修复 + 合并 |

**总计：16 个工作日**

---

## 十三、完成后预期效果

| 指标 | Phase 1（当前） | Phase 2（完成后） |
|------|----------------|------------------|
| 压缩阈值 | 静态 70% | 动态 50-85% |
| 大 tool result | LLM 摘要化 | Offload 到磁盘 + 可召回 |
| 话题隔离 | ❌ | ✅ 自动检测 + 选择性压缩 |
| 压缩质量 | 不可知 | 指纹校验 + 评分（0-1） |
| 压缩冷却 | ❌ | ✅ 3轮冷却 |
| 指数增长应对 | ❌ | ✅ 提前触发 |
| 信息密度感知 | ❌ | ✅ 按密度选择性驱逐 |
| Offload 数据残留 | N/A | ✅ 启动清理 + /new 即时清理 |
| SubAgent 隔离 | ✅ | ✅ offload_recall 不注入 |

---

## 附录 A：门下省审核意见处理记录

| 编号 | 意见 | 处理 | 位置 |
|------|------|------|------|
| A | offload_recall 注册机制和 sessionKey 获取未说明 | ✅ 补充注册代码、ToolContext.SessionID() 路径 | §4.3 |
| B | TriggerInfoProvider 生命周期未明确 | ✅ 补充 sync.Map 按 sessionKey 存储、/new 清理、Reset() | §2.3.4 |
| C | P2.3 隐式依赖 P2.2，声明有误 | ✅ 修正为"建议在 P2.2 之后实施"，依赖关系图更新 | §5 标题 + §9 |
| D | P2.3 缺少 Offload 标记兼容性验证 | ✅ 新增验证项 6，defaultDensityScorer 增加 Offload 标记处理 | §5.2.1 + §5.4 |
| E | P2.6 验证项不足 | ✅ 补充 3 项（Phase1 兼容、降级行为、prompt 增量影响） | §6.3 |
| F | P2.2 回滚缺少数据残留清理 | ✅ 补充 rm -rf 步骤和 CleanStale 自动清理 | §4.7 |
| G | P2.4/P2.5 时间偏紧 | ✅ P2.4 2→2.5天，P2.5 3→4天，总工期 14→16天 | §3 标题 + §7 标题 + §12 |
| H | Offload 异步 LLM 摘要增强未包含 | ⏭️ 标记为可选，不在本阶段实施（可在 Phase 2.1 补充） | §4.2.2 备注 |
| I | offload_recall 在 SubAgent 中行为 | ✅ 说明主 Agent 独占注册，SubAgent 不含此工具 | §4.3 |
| J | 异常退出残留清理 | ✅ 增加 CleanStale() 启动清理机制 + CleanupAgeDays 配置 | §4.2.4 + §4.4.2 |
