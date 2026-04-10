---
title: "context-management-phase2-design"
weight: 80
---


> ⚠️ **演进说明（2026-03-27）**：本文档基于 Phase 1 的「执行视图隔离」原则设计，该原则已在后续重构（commit `45d6078`）中被调整——当前实现直接持久化 engine 产生的 assistant + tool 消息。本 Phase 2 方案中依赖该原则的部分可能需要修订。本文档状态仍为「待审核」。

> 中书省拟 | 2026-03-19
> 状态：待陛下审核
> 前置文档：[调研报告](context-management-research.md)、[Phase 1 设计文档](context-management-design.md)
> 目标：超越 Claude Code 的上下文管理能力

---

## 一、Phase 1 回顾与 Phase 2 定位

### 1.1 Phase 1 已完成基础

| 能力 | 状态 | 涉及文件 |
|------|------|----------|
| CompressResult 双视图（LLMView + SessionView） | ✅ | `agent/compress.go:14-17` |
| maybeCompress 使用 SessionView 持久化 | ✅ | `agent/engine.go` |
| extractDialogueFromTail 尾部对话提取 | ✅ | `agent/compress.go` |
| 压缩阈值 0.7 | ✅ | `agent/agent.go` Agent 结构体 `compressionThreshold` |
| thinTail 尾部旧工具组精简 | ✅ | `agent/compress.go` |
| flushPending / truncateArgs 辅助函数 | ✅ | `agent/compress.go` |
| CompressFunc 签名 `func(ctx, msgs, client, model) (*CompressResult, error)` | ✅ | `agent/engine.go` |

### 1.2 Phase 2 设计目标：超越 Claude Code

Claude Code 的上下文管理策略（截至 2025 年）：

| Claude Code 能力 | xbot Phase 1 | xbot Phase 2 目标 |
|------------------|-------------|-------------------|
| 静态 64-75% 阈值触发压缩 | ✅ 0.7 静态阈值 | **动态阈值**：根据任务阶段、增长速率、工具模式自适应 |
| 整体 LLM 摘要压缩 | ✅ | ✅ + **分层渐进压缩**（Offload → Evict → Compact） |
| 保留最近 N 轮完整 | ✅ thinTail(3) | ✅ + **话题感知保留** |
| 工具结果摘要化 | ✅ extractDialogueFromTail | ✅ + **自动 Offload 大结果到磁盘 + 可召回** |
| 社区呼声的子任务级压缩（未实现） | — | ✅ **话题分区隔离 + 选择性压缩** |
| 压缩质量可观测 | ❌ | ✅ **关键信息完整性校验 + 质量评分** |
| 增长速率预测 | ❌ | ✅ **加权线性回归 + 指数增长检测** |

**Phase 2 的核心差异化**：从"被动压缩"进化为"主动上下文编排"——不是等上下文满了才压缩，而是持续感知、智能调度、分层管理。

---

## 二、整体架构

```
┌─────────────────────────────────────────────────────────────────┐
│                    xbot Phase 2 上下文管理引擎                    │
│                                                                 │
│  ┌─────────────┐   ┌──────────────────┐   ┌──────────────────┐ │
│  │ 智能触发引擎  │   │ 分层压缩引擎      │   │ 质量保障引擎      │ │
│  │             │   │                  │   │                  │ │
│  │ · 任务阶段   │──▶│ · Layer 1        │   │ · 关键信息校验    │ │
│  │   感知      │   │   Offload        │   │ · 结构化标记      │ │
│  │ · 增长速率   │   │   (execOne后)    │   │ · 质量评分        │ │
│  │   预测      │   │ · Layer 2 Evict  │   │                  │ │
│  │ · 工具模式   │   │ · Layer 3        │   └────────┬─────────┘ │
│  │   感知      │   │   Compact        │            │            │
│  │ · 压缩冷却   │   │                  │◀───────────┘            │
│  └──────┬──────┘   └──────┬───────────┘                         │
│         │                 │                                     │
│         ▼                 ▼                                     │
│  ┌─────────────────────────────────────────────────────────────┐│
│  │                  话题分区管理器                               ││
│  │                                                             ││
│  │  Topic 1: 上下文缓存 │ 压缩摘要 │ Offload 索引                ││
│  │  Topic 2: 上下文缓存 │ 压缩摘要 │ Offload 索引                ││
│  │  Current: 完整上下文（最近对话 + 活跃工具结果）                 ││
│  └─────────────────────────────────────────────────────────────┘│
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

---

## 三、方向一：三层渐进压缩

### 3.1 设计思路

借鉴社区最佳实践（社区三层架构：Offload → Evict → Compact），但做了关键增强：

1. **Offload 不只是落盘**——落盘后生成"可召回摘要"，支持后续按需检索
2. **Evict 不是简单截断**——根据信息密度分级驱逐，保留高密度信息
3. **三层联动**——各层之间有数据流转通道，Offload 的数据可被后续检索回填

**与 Claude Code 的区别**：Claude Code 只有一个 Compact 层（LLM 摘要），xbot Phase 2 提供三层渐进保护，避免一次性 LLM 压缩导致的信息损失。

### 3.2 Layer 1: Offload（大 tool result 自动落盘）

**触发条件**：单条 tool result 内容超过阈值（如 2000 tokens 或 10KB）

**执行策略**：

```go
// OffloadConfig Layer 1 配置
type OffloadConfig struct {
    MaxResultTokens int    // 单条 tool result 触发 offload 的 token 阈值（默认 2000）
    MaxResultBytes  int    // 单条 tool result 触发 offload 的字节阈值（默认 10240）
    StoreDir        string // offload 存储目录（默认 {DataDir}/offload_store/）
}

// OffloadedResult 已 offload 的 tool result 的元数据
type OffloadedResult struct {
    ID        string    // 唯一 ID（uuid）
    ToolName  string    // 工具名称
    Args      string    // 工具参数（截断）
    FilePath  string    // 落盘文件路径
    TokenSize int       // 原始 token 数
    Timestamp time.Time // offload 时间
    Summary   string    // 摘要（规则提取同步生成 + 可选 LLM 增强）
}
```

**摘要生成策略（同步 + 可选异步增强）**：

```
工具执行返回大 result
  → 检测 tokens > MaxResultTokens
  → 【同步】写入 offload_store/{session_key}/{uuid}.json（原始内容完整保存）
  → 【同步】规则提取摘要（毫秒级，无 LLM 依赖）：
      - 文件类：提取文件名 + 行数 + 前3行 + 后3行 + 关键函数名
      - Grep 类：提取匹配数量 + 前3条匹配（截断行）
      - Shell 类：提取退出码 + 最后5行输出
      - 通用：截取前300字符 + "...(N tokens 已 offload)"
  → 【同步】替换上下文中的 tool result 为摘要 + 引用标记
  → 【可选异步】用 LLM 生成更精确的摘要，回填 OffloadedResult.Summary
     （异步回填不阻塞主流程，摘要仅用于 offload_recall 的快速预览）
```

> **设计决策**：摘要分为两层——同步规则摘要是**必需的**（保证主流程零延迟、信息不缺失），LLM 摘要是**可选增强**（提升可读性，但不影响功能）。

**上下文替换示例**：

```
原始 tool result（5000 tokens）：
  "package main\nimport (\n  \"fmt\"\n  ...200行代码...\n)\nfunc main() { ... }"

替换后（~200 tokens，同步规则摘要）：
  "📂 [offload:offload_abc123] Read("main.go")
   文件: xbot/agent/main.go (245行, 5234 tokens)
   预览: package main — 定义了 Agent 结构体和 New() 构造函数
         关键函数: New(cfg Config), Run(), processMessage(), buildPrompt()
   原始内容已保存至 offload_store，可通过 offload_recall 工具按 ID 检索。"
```

**offload_recall 工具设计**：

```go
// offload_recall 工具定义
// 注册方式：在 tools/ 目录新增 offload_recall.go，通过 Tools.Registry 注册
type OffloadRecallArgs struct {
    ID string `json:"id" jsonschema:"description=offload 条目 ID（如 offload_abc123）"`
}

// 执行逻辑：
// 1. 从 offload_store/{session_key}/index.json 查找 ID 对应的文件路径
// 2. 读取完整 tool result 内容
// 3. 如果内容仍然过大（> MaxResultTokens），返回截断版本 + 原始文件路径
// 4. 返回格式：
//    "📂 [offload recall: {id}] {tool_name}({args})\n{完整内容或截断版本}"

// 与 HookChain 集成：
// - 无需特殊 Hook，作为普通工具注册到 Agent 的 Tools 列表中
// - LLM 在上下文中看到 [offload:xxx] 标记时，自然会使用 offload_recall 工具获取详情
// - 依赖 Issue #98 的 Tool Hook 机制（PreToolUse/PostToolUse）来拦截大结果
```

**与 Phase 1 的衔接**：

- Offload 发生在 `Run()` 循环的工具执行阶段（`execOne` 返回后），早于任何压缩触发
- Offload 后的消息直接进入 `messages` 切片，后续的 thinTail / compressContext 操作无需感知 Offload 细节
- `extractDialogueFromTail` 识别 offload 标记（`📂 [offload:...]`），不对其二次截断

### 3.3 Layer 2: Evict（上下文驱逐旧 tool payload）

**触发条件**：上下文使用率达到动态阈值的 **80%**（低于 Compact 触发的 100%），给予缓冲空间

**执行策略**：

```go
// EvictPolicy 驱逐策略
type EvictPolicy struct {
    // 保留最近 N 组完整 tool call/result
    KeepRecentGroups int // 默认 3（与 thinTail 对齐）

    // 信息密度评分：决定驱逐优先级
    // 低密度（大块代码转储、重复 grep 结果）优先驱逐
    // 高密度（错误信息、决策记录、小但关键的代码片段）最后驱逐
    DensityScorer func(msg llm.ChatMessage) float64
}

// defaultDensityScorer 默认信息密度评分函数
func defaultDensityScorer(msg llm.ChatMessage) float64 {
    score := 0.0
    content := msg.Content

    // 高密度信号（加分）
    if containsErrorPattern(content)    { score += 3.0 } // 错误信息
    if containsDecisionPattern(content) { score += 2.5 } // 决策记录
    if containsFilePath(content)        { score += 1.0 } // 文件路径引用
    if len([]rune(content)) < 500      { score += 1.5 } // 短内容天然密度高

    // 低密度信号（减分）
    if isLargeCodeDump(content)         { score -= 2.0 } // 大段代码转储
    if isRepetitiveGrepResult(content)  { score -= 1.5 } // 重复 grep 结果
    if msg.Role == "tool" && len([]rune(content)) > 3000 { score -= 2.0 }

    return score
}
```

**驱逐过程**：

```
输入：messages（可能已包含 Offload 后的摘要消息）
  1. 从尾部保留 KeepRecentGroups 组完整 tool call/result
  2. 对剩余的 tool 消息按密度评分排序
  3. 从低密度开始驱逐：
     - 替换 tool result 为 "→ [evicted] 工具摘要（N tokens 已驱逐）"
     - 保留 assistant(tool_calls) 消息的结构（API 兼容性）
  4. 驱逐直到上下文降到阈值的 70% 以下
```

**与 Phase 1 的衔接**：

- Layer 2 是现有 `thinTail` 的**增强版**：thinTail 只按位置截断，Layer 2 按信息密度选择性截断
- 建议替代：将 thinTail 逻辑升级为 Evict 逻辑，保留 API 接口不变

### 3.4 Layer 3: Compact（现有 LLM 摘要压缩）

**触发条件**：Layer 2 Evict 后仍超过动态阈值

**增强点**：

- 使用**结构化压缩 prompt**（见方向四），要求输出带标记的摘要
- **话题感知压缩**：压缩时尊重话题分区边界（见方向三）
- **双视图输出**：Phase 1 已实现，保持不变

### 3.5 分层执行流水线

```go
// EvictCompactPipeline 分层压缩流水线
// 注：Layer 1 (Offload) 在 execOne 后立即触发，不在此 Pipeline 中编排
// Pipeline 仅编排 Layer 2 (Evict) 和 Layer 3 (Compact)
type EvictCompactPipeline struct {
    evictor  *Evictor           // Layer 2
    compFunc CompressFunc       // Layer 3（Phase 1 的 compressContext）
    quality  *QualityEvaluator  // 质量评估（方向四）
}

// Execute 执行压缩流水线，按需触发，不一定每层都执行
func (p *EvictCompactPipeline) Execute(ctx context.Context, 
    messages []llm.ChatMessage, 
    trigger TriggerInfo,
    client llm.LLM, model string) (*CompressResult, error) {
    
    currentTokens, _ := llm.CountMessagesTokens(messages, model)
    threshold := calculateDynamicThreshold(trigger)
    
    // 未达到 Evict 阈值（动态阈值的 80%），不需要压缩
    if float64(currentTokens) / float64(trigger.MaxTokens) < threshold * 0.8 {
        return nil, nil
    }
    
    // Layer 2: Evict
    evicted := p.evictor.Evict(messages, threshold * 0.7)
    evictTokens, _ := llm.CountMessagesTokens(evicted, model)
    
    // Layer 3: Compact（如果 Evict 后仍超阈值）
    var result *CompressResult
    if float64(evictTokens) / float64(trigger.MaxTokens) >= threshold {
        result, _ = p.compFunc(ctx, evicted, client, model)
    } else {
        // Evict 后已足够，直接构建双视图
        result = buildCompressResultFromMessages(evicted)
    }
    
    return result, nil
}
```

> **与现有 CompressFunc 的兼容性**：`CompressFunc` 签名不变，Pipeline 内部在调用时传入标准的参数。`TriggerInfo` 仅用于 Pipeline 内部的阈值计算，不修改 `CompressFunc` 的接口签名。现有使用 `CompressFunc` 的代码（如 `agent/engine.go` 中的 `maybeCompress`）无需修改签名，仅在需要动态阈值时通过 Pipeline 调用。

### 3.6 数据流总览

```
工具执行 → [Layer 1 Offload] → messages（大结果替换为规则摘要+标记）
              ↓ (单次工具执行后立即触发，同步)
         Run() 循环继续
              ↓ (maybeCompress 检测到上下文过大)
         [Layer 2 Evict] → messages（低密度 tool result 驱逐）
              ↓ (Evict 后仍超过动态阈值)
         [Layer 3 Compact] → CompressResult{LLMView, SessionView}
              ↓
         持久化 SessionView 到 session
         继续用 LLMView 调用 LLM
```

### 3.7 Offload Store 存储设计

```
offload_store/
  ├── {session_key}/
  │     ├── offload_abc123.json    ← 完整 tool result
  │     ├── offload_def456.json
  │     └── index.json             ← 索引文件（所有 offload 条目的元数据）
  └── cleanup_policy: 会话结束时 /new 时清理
```

**索引文件结构**：

```json
{
  "session_key": "feishu:oc_abc123",
  "entries": [
    {
      "id": "offload_abc123",
      "tool_name": "Read",
      "args": "{\"path\":\"main.go\"}",
      "file": "offload_abc123.json",
      "token_size": 5234,
      "created_at": "2026-03-19T01:00:00Z",
      "summary": "main.go 入口文件，定义 Agent 结构体..."
    }
  ]
}
```

---

## 四、方向二：智能压缩触发

### 4.1 设计思路

Claude Code 使用静态 64-75% 阈值，这是一个"一刀切"策略。但在实际使用中：

- **信息积累期**（刚接手任务）：需要更多上下文空间来收集信息，不应过早压缩
- **信息爆发期**（大量文件读取）：上下文快速增长，需要提前触发
- **决策执行期**（写代码）：上下文稳定增长，但当前信息很关键，不应压缩
- **任务收尾期**（验证结果）：需要历史决策信息，应保留更多历史

xbot Phase 2 引入**动态阈值算法**，综合考虑任务阶段、增长速率、工具调用模式，外加**压缩冷却机制**防止无限循环。

### 4.2 触发信息采集

```go
// TriggerInfo 压缩触发所需的上下文信息
type TriggerInfo struct {
    // 基础信息
    MaxTokens    int       // 最大上下文 token 数
    CurrentTokens int      // 当前上下文 token 数
    
    // 任务阶段信息
    IterationCount int     // 当前 Run() 循环迭代次数
    ToolCallCount  int     // 本次 Run() 的工具调用总次数
    IsFirstUserMsg bool    // 是否是本次用户消息的第一轮迭代
    
    // 增长速率信息
    TokenHistory   []int   // 最近 N 次迭代的 token 数快照（滑窗）
    GrowthRate     float64 // 近期 token 增长速率（tokens/iteration）
    
    // 工具模式信息
    RecentTools    []string // 最近 5 次工具调用名称
    ToolPattern    ToolCallPattern // 识别出的工具调用模式
}

// ToolCallPattern 工具调用模式
type ToolCallPattern int

const (
    PatternConversation ToolCallPattern = iota // 纯对话（无工具调用）
    PatternReadHeavy                           // 读密集（大量 Read/Grep/Glob）
    PatternWriteHeavy                          // 写密集（大量 Edit/Shell 写操作）
    PatternMixed                               // 混合模式
    PatternSubAgent                            // SubAgent 调用
)
```

### 4.3 动态阈值算法

```go
// calculateDynamicThreshold 计算动态压缩阈值
// 返回值范围 [0.5, 0.85]，Claude Code 固定为 0.64-0.75
func calculateDynamicThreshold(info TriggerInfo) float64 {
    // 基础阈值
    baseThreshold := 0.70
    
    // === 因子 1: 任务阶段调节 ===
    stageFactor := 1.0
    switch {
    case info.IterationCount <= 3:
        // 信息积累期：宽容阈值，允许更多空间
        stageFactor = 0.85
    case info.IterationCount <= 10:
        // 活跃工作期：标准阈值
        stageFactor = 0.70
    case info.ToolCallCount > 20:
        // 长任务执行期：开始收窄
        stageFactor = 0.65
    default:
        stageFactor = 0.70
    }
    
    // === 因子 2: 增长速率预测 ===
    growthFactor := 1.0
    if info.GrowthRate > 0 {
        // 预测达到 90% 还需要多少轮
        remainingTokens := float64(info.MaxTokens*9/10 - info.CurrentTokens)
        iterationsToFull := remainingTokens / info.GrowthRate
        switch {
        case iterationsToFull < 2:
            // 即将爆满，立即触发（降低阈值）
            growthFactor = 0.50
        case iterationsToFull < 5:
            // 快速增长，提前触发
            growthFactor = 0.60
        case iterationsToFull > 15:
            // 增长缓慢，放宽阈值
            growthFactor = 0.80
        }
    }
    
    // === 因子 3: 工具模式调节 ===
    patternFactor := 1.0
    switch info.ToolPattern {
    case PatternReadHeavy:
        // 读密集：大量文件内容涌入，需要更积极的压缩
        if isScanningPhase(info.RecentTools) {
            patternFactor = 0.60 // 扫描阶段，积极压缩
        } else {
            patternFactor = 0.65
        }
    case PatternWriteHeavy:
        // 写密集：编辑操作通常结果较小，不需要太积极
        patternFactor = 0.75
    case PatternSubAgent:
        // SubAgent 返回结果可能较大，适当提前
        patternFactor = 0.65
    case PatternConversation:
        // 纯对话：增长缓慢，放宽
        patternFactor = 0.80
    case PatternMixed:
        patternFactor = 0.70
    }
    
    // === 综合计算 ===
    // 取各因子的最小值（最保守的策略获胜）
    dynamicThreshold := min(baseThreshold, stageFactor, growthFactor, patternFactor)
    
    // 限制范围
    dynamicThreshold = clamp(dynamicThreshold, 0.50, 0.85)
    
    return dynamicThreshold
}

func clamp(v, min, max float64) float64 {
    if v < min { return min }
    if v > max { return max }
    return v
}
```

### 4.4 增长速率预测

```go
// TokenGrowthTracker token 增长速率追踪器
// 生命周期：存储在 Agent 结构体中，按 session key 索引
//   Agent.growthTrackers map[string]*TokenGrowthTracker
// 每次 Run() 开始时通过 sessionKey 获取或创建，跨 Run() 持久化
// /new 命令时清理对应 session 的 tracker
type TokenGrowthTracker struct {
    window   []tokenSnapshot // 滑动窗口
    maxSize  int             // 窗口大小（默认 10）
}

type tokenSnapshot struct {
    iteration int
    tokens    int
    timestamp time.Time
}

// Record 记录一次 token 快照
func (t *TokenGrowthTracker) Record(iteration int, tokens int) {
    t.window = append(t.window, tokenSnapshot{
        iteration: iteration,
        tokens:    tokens,
        timestamp: time.Now(),
    })
    if len(t.window) > t.maxSize {
        t.window = t.window[1:]
    }
}

// GrowthRate 计算 token 增长速率（tokens/iteration）
// 使用加权线性回归，近期数据权重更高
func (t *TokenGrowthTracker) GrowthRate() float64 {
    if len(t.window) < 2 {
        return 0
    }
    
    // 加权线性回归
    var sumX, sumY, sumXY, sumX2, sumW float64
    n := len(t.window)
    
    for i, snap := range t.window {
        w := float64(i+1) / float64(n) // 近期权重更高
        x := float64(snap.iteration)
        y := float64(snap.tokens)
        sumW += w
        sumX += w * x
        sumY += w * y
        sumXY += w * x * y
        sumX2 += w * x * x
    }
    
    // 斜率 = 增长速率
    slope := (sumW*sumXY - sumX*sumY) / (sumW*sumX2 - sumX*sumX)
    return slope
}

// IsExponentialGrowth 检测是否指数增长
// 指数增长意味着即将爆满，需要立即触发压缩
func (t *TokenGrowthTracker) IsExponentialGrowth() bool {
    if len(t.window) < 4 {
        return false
    }
    
    // 计算相邻增长的比率
    var ratios []float64
    for i := 1; i < len(t.window); i++ {
        delta := t.window[i].tokens - t.window[i-1].tokens
        if delta > 0 && t.window[i-1].tokens > 0 {
            ratios = append(ratios, float64(delta)/float64(t.window[i-1].tokens))
        }
    }
    
    if len(ratios) < 2 {
        return false
    }
    
    // 如果增长率在持续增大（加速增长），判定为指数增长
    accelerating := true
    for i := 1; i < len(ratios); i++ {
        if ratios[i] <= ratios[i-1] {
            accelerating = false
            break
        }
    }
    
    return accelerating
}

// Snapshots 返回当前窗口快照（供 TriggerInfo.TokenHistory 使用）
func (t *TokenGrowthTracker) Snapshots() []int {
    result := make([]int, len(t.window))
    for i, snap := range t.window {
        result[i] = snap.tokens
    }
    return result
}
```

### 4.5 压缩冷却机制

> 防止无限压缩循环：当动态阈值降到 0.5 且 Evict/Compact 无法降到阈值以下时，每次迭代都会触发压缩。

```go
// CompressCooldown 压缩冷却管理器
type CompressCooldown struct {
    lastCompressIteration int  // 上次触发压缩时的迭代计数
    cooldownIterations    int  // 冷却轮数（默认 3）
}

// ShouldTrigger 判断是否应该触发压缩
// 返回 false 表示处于冷却期，应跳过本次压缩
func (c *CompressCooldown) ShouldTrigger(currentIteration int) bool {
    if c.lastCompressIteration == 0 {
        return true // 从未压缩过
    }
    return (currentIteration - c.lastCompressIteration) >= c.cooldownIterations
}

// RecordCompress 记录一次压缩触发
func (c *CompressCooldown) RecordCompress(iteration int) {
    c.lastCompressIteration = iteration
}
```

### 4.6 工具模式检测

```go
// DetectToolPattern 检测当前工具调用模式
func DetectToolPattern(recentTools []string) ToolCallPattern {
    if len(recentTools) == 0 {
        return PatternConversation
    }
    
    readTools := map[string]bool{"Read": true, "Grep": true, "Glob": true, "WebSearch": true}
    writeTools := map[string]bool{"Edit": true, "Shell": true, "Write": true, "DownloadFile": true}
    
    readCount, writeCount, subAgentCount := 0, 0, 0
    for _, t := range recentTools {
        if readTools[t] { readCount++ }
        if writeTools[t] { writeCount++ }
        if t == "SubAgent" { subAgentCount++ }
    }
    
    total := len(recentTools)
    
    switch {
    case subAgentCount > total/2:
        return PatternSubAgent
    case readCount > total*3/4 && writeCount == 0:
        return PatternReadHeavy
    case writeCount > total/2:
        return PatternWriteHeavy
    case readCount > 0 && writeCount > 0:
        return PatternMixed
    default:
        return PatternConversation
    }
}

// isScanningPhase 判断是否处于代码扫描阶段
// 扫描阶段的特征：最近多次工具调用都是读取类，没有任何写入操作
func isScanningPhase(recentTools []string) bool {
    if len(recentTools) < 3 {
        return false
    }
    
    writeTools := map[string]bool{"Edit": true, "Shell": true, "Write": true}
    
    for _, t := range recentTools {
        if writeTools[t] {
            return false // 发现写入操作，不是扫描阶段
        }
    }
    
    return true // 所有最近工具调用都是读取类
}
```

### 4.7 集成到 maybeCompress

```go
// Phase 2 增强版 maybeCompress（伪代码，展示修改点）
// recentToolCalls 从 toolsUsed []string 中取最近 N 条
//   func recentToolCalls(toolsUsed []string, n int) []string {
//       if len(toolsUsed) < n { n = len(toolsUsed) }
//       return toolsUsed[len(toolsUsed)-n:]
//   }

maybeCompress := func() {
    cc := cfg.AutoCompress
    if cc == nil || len(messages) <= 3 {
        return
    }
    
    // === Phase 2 新增：压缩冷却检查 ===
    cooldown := a.getCooldown(sessionKey) // 从 Agent.cooldowns map 中获取
    if !cooldown.ShouldTrigger(i) {
        growthTracker.Record(i, totalTokens) // 仍然记录快照
        return
    }
    
    // === Phase 2 新增：采集触发信息 ===
    currentTokens, _ := llm.CountMessagesTokens(messages, cfg.Model)
    toolDefs := cfg.Tools.AsDefinitionsForSession(sessionKey)
    toolTokens, _ := llm.CountToolsTokens(toolDefs, cfg.Model)
    totalTokens := currentTokens + toolTokens
    
    // growthTracker 来自 Agent.growthTrackers[sessionKey]
    // 生命周期：存储在 Agent 结构体中（map[string]*TokenGrowthTracker）
    // /new 时通过 delete(Agent.growthTrackers, sessionKey) 清理
    triggerInfo := TriggerInfo{
        MaxTokens:      cc.MaxContextTokens,
        CurrentTokens:  totalTokens,
        IterationCount: i,
        ToolCallCount:  len(toolsUsed),
        IsFirstUserMsg: i == 0,
        TokenHistory:   growthTracker.Snapshots(),
        GrowthRate:     growthTracker.GrowthRate(),
        RecentTools:    recentToolCalls(toolsUsed, 5), // 取最近 5 条工具调用
        ToolPattern:    DetectToolPattern(recentToolCalls(toolsUsed, 5)),
    }
    
    // === Phase 2 新增：动态阈值 ===
    dynamicThreshold := calculateDynamicThreshold(triggerInfo)
    threshold := int(float64(cc.MaxContextTokens) * dynamicThreshold)
    
    if totalTokens < threshold {
        // 记录 token 快照（用于增长速率追踪）
        growthTracker.Record(i, totalTokens)
        return
    }
    
    // === Phase 2 新增：指数增长紧急触发 ===
    if growthTracker.IsExponentialGrowth() {
        log.Ctx(ctx).Warn("Exponential token growth detected, forcing early compression")
        threshold = int(float64(cc.MaxContextTokens) * 0.50)
    }
    
    // 记录冷却
    cooldown.RecordCompress(i)
    
    // 执行压缩（复用 Phase 1 逻辑）
    // ... 后续不变 ...
}
```

### 4.8 与 Claude Code 的对比

| 维度 | Claude Code | xbot Phase 2 |
|------|------------|-------------|
| 阈值类型 | 静态 64-75% | 动态 50-85% |
| 增长感知 | ❌ | ✅ 加权线性回归 + 指数检测 |
| 模式感知 | ❌ | ✅ 5 种工具模式识别 |
| 阶段感知 | ❌ | ✅ 4 个任务阶段 |
| 紧急压缩 | ❌ | ✅ 指数增长立即触发 |
| 压缩冷却 | ❌ | ✅ 防止无限循环 |

---

## 五、方向三：对话分区隔离

### 5.1 设计思路

参考 Kubiya 的 Isolate 策略，但更进一步：不仅隔离任务，还要**自动检测话题切换**，实现无需用户显式操作的智能分区。

**核心洞察**：长会话中，用户经常在同一会话里讨论多个不相关的话题。传统压缩将所有历史一锅端，导致当前话题的关键信息可能被压缩丢失。

**与 Claude Code 的区别**：Claude Code（截至 2025 年）没有话题分区能力（社区 Issue #16960 强烈要求但未实现）。xbot Phase 2 实现自动话题检测 + 分区压缩，这是**超越 Claude Code 的关键差异化功能**。

### 5.2 话题检测算法

```go
// TopicDetector 话题检测器
type TopicDetector struct {
    // 话题切换判定参数
    CosineThreshold float64 // 余弦相似度阈值（默认 0.3，低于此值认为话题切换）
    MinSegmentSize  int     // 最小片段长度（默认 3 条消息，避免碎片化）
}

// TopicSegment 话题片段
type TopicSegment struct {
    ID          string             // 片段 ID
    StartIdx    int                // 起始消息索引（在 session 中的位置）
    EndIdx      int                // 结束消息索引
    MessageCount int               // 消息数
    Keywords    []string           // 提取的关键词（用于标记）
    Summary     string             // 压缩后的摘要（仅已压缩的片段有值）
    IsCurrent   bool               // 是否是当前活跃话题
}

// Detect 检测话题边界
// 输入：session 中的 user/assistant 消息列表
// 输出：话题片段列表
func (d *TopicDetector) Detect(messages []llm.ChatMessage) []TopicSegment {
    if len(messages) < d.MinSegmentSize*2 {
        return []TopicSegment{{ID: "topic_0", StartIdx: 0, EndIdx: len(messages) - 1, IsCurrent: true}}
    }
    
    // 步骤 1: 将消息按"对话轮次"分组（一轮 = user + assistant）
    turns := groupIntoTurns(messages)
    
    // 步骤 2: 为每个轮次提取特征向量
    // 使用轻量级特征（关键词 TF-IDF 或简单词袋），支持中英文混合
    turnFeatures := make([][]string, len(turns))
    for i, turn := range turns {
        turnFeatures[i] = extractKeywords(turn.Text())
    }
    
    // 步骤 3: 计算相邻轮次的相似度
    boundaries := []int{0}
    for i := 1; i < len(turnFeatures); i++ {
        similarity := cosineSimilarity(turnFeatures[i-1], turnFeatures[i])
        if similarity < d.CosineThreshold {
            lastBoundary := boundaries[len(boundaries)-1]
            if i - lastBoundary >= d.MinSegmentSize {
                boundaries = append(boundaries, i)
            }
        }
    }
    boundaries = append(boundaries, len(turns))
    
    // 步骤 4: 构建话题片段
    var segments []TopicSegment
    for i := 0; i < len(boundaries)-1; i++ {
        seg := TopicSegment{
            ID:       fmt.Sprintf("topic_%d", i),
            StartIdx: turns[boundaries[i]].StartMsgIdx,
            EndIdx:   turns[boundaries[i+1]-1].EndMsgIdx,
            Keywords: mergeKeywords(turnFeatures[boundaries[i]:boundaries[i+1]]),
            IsCurrent: i == len(boundaries)-2,
        }
        seg.MessageCount = seg.EndIdx - seg.StartIdx + 1
        segments = append(segments, seg)
    }
    
    return segments
}
```

### 5.3 轻量级特征提取（无 embedding 依赖，支持中英文）

为确保话题检测不增加 LLM/embedding 成本，使用**轻量级关键词提取**，并支持中文分词：

```go
// extractKeywords 从文本中提取关键词（规则方法，无需 LLM）
// 支持中英文混合：英文用空格/正则分词，CJK 字符按 bigram 拆分
func extractKeywords(text string) []string {
    keywords := make(map[string]bool)
    
    // 1. 提取文件路径模式
    for _, fp := range filepathPattern.FindAllString(text, -1) {
        keywords[strings.ToLower(fp)] = true
    }
    
    // 2. 提取代码标识符（驼峰/下划线命名）
    for _, id := range identifierPattern.FindAllString(text, -1) {
        keywords[strings.ToLower(id)] = true
    }
    
    // 3. 提取英文单词（去除常见停用词）
    words := tokenizeEnglish(text)
    for _, w := range removeStopWords(words) {
        keywords[strings.ToLower(w)] = true
    }
    
    // 4. 提取 CJK 字符序列（中文/日文/韩文）
    // 策略：使用 bigram（相邻两个字符为一组），而非依赖外部分词库
    // 原因：避免引入 jieba 等重量级依赖，bigram 对话题切换检测足够
    // 示例："上下文管理" → ["上下", "下文", "文管", "管理"]
    cjkChars := extractCJKChars(text) // 提取连续 CJK 字符序列
    for _, segment := range cjkChars {
        runes := []rune(segment)
        for j := 0; j < len(runes)-1; j++ {
            keywords[string(runes[j:j+2])] = true // bigram
        }
    }
    
    // 5. 转换为列表
    result := make([]string, 0, len(keywords))
    for k := range keywords {
        result = append(result, k)
    }
    return result
}

// extractCJKChars 从文本中提取连续的 CJK 字符序列
// 使用 Unicode 范围：\p{Han}（中文）、\p{Hiragana}+\p{Katakana}（日文）、\p{Hangul}（韩文）
var cjkPattern = regexp.MustCompile(`[\p{Han}\p{Hiragana}\p{Katakana}\p{Hangul}]+`)

func extractCJKChars(text string) []string {
    return cjkPattern.FindAllString(text, -1)
}

// tokenizeEnglish 提取英文单词
var englishWordPattern = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9_]*`)

func tokenizeEnglish(text string) []string {
    return englishWordPattern.FindAllString(text, -1)
}
```

**中文分词设计决策**：

| 方案 | 优点 | 缺点 | 选择 |
|------|------|------|------|
| jieba 分词 | 精度高 | 依赖 C 库，跨平台编译困难 | ❌ 不引入 |
| 按空格分词 | 简单 | 中文无空格分隔，完全无效 | ❌ |
| bigram | 无依赖，适合话题切换检测 | 精度不如完整分词 | ✅ 采用 |
| unigram | 最简单 | 太细粒度，相似度计算噪声大 | ❌ |

> bigram 对话题切换检测是**够用的**——不同话题的关键词 bigram 重叠度自然低，即使没有精确分词也能有效检测边界。

### 5.4 选择性压缩策略

```go
// SelectiveCompressor 选择性压缩器
type SelectiveCompressor struct {
    detector *TopicDetector
}

// Compress 选择性压缩：保留当前话题完整，压缩历史话题
func (sc *SelectiveCompressor) Compress(
    ctx context.Context,
    messages []llm.ChatMessage,
    client llm.LLM,
    model string,
) (*CompressResult, error) {
    
    // 1. 检测话题分区
    segments := sc.detector.Detect(messages)
    
    if len(segments) <= 1 {
        // 只有一个话题，走标准压缩（Phase 1 逻辑）
        return standardCompress(ctx, messages, client, model)
    }
    
    // 2. 分离当前话题和历史话题
    var currentMsgs, historyMsgs []llm.ChatMessage
    for _, seg := range segments {
        if seg.IsCurrent {
            currentMsgs = messages[seg.StartIdx : seg.EndIdx+1]
        } else {
            historyMsgs = append(historyMsgs, messages[seg.StartIdx:seg.EndIdx+1]...)
        }
    }
    
    // 3. 压缩历史话题（可按话题分别压缩，保留话题标记）
    compressedHistory := sc.compressHistoryByTopic(ctx, historyMsgs, segments, client, model)
    
    // 4. 构建双视图
    historySummary := buildTopicAnnotatedSummary(compressedHistory)
    summaryMsg := llm.NewUserMessage("[Previous conversation context]\n\n" + historySummary)
    
    // LLM View: 保留当前话题的 tool 消息（API 兼容）
    llmView := []llm.ChatMessage{summaryMsg}
    llmView = append(llmView, currentMsgs...)
    
    // Session View: 当前话题提取对话视图
    currentSessionView := extractDialogueFromTail(currentMsgs)
    sessionView := []llm.ChatMessage{summaryMsg}
    sessionView = append(sessionView, currentSessionView...)
    
    return &CompressResult{
        LLMView:     llmView,
        SessionView: sessionView,
    }, nil
}
```

### 5.5 话题切换感知（同步压缩，避免并发写入）

> ⚠️ **设计约束**：话题压缩必须在 `processMessage` 的主流程中**同步执行**，不能用 `go` 异步。
> 原因：`TenantSession` 不是线程安全的，异步 goroutine 与主流程并发写入 `session.AddMessage()` 会导致数据损坏。
> 如果后续需要异步，必须先给 `TenantSession` 增加 `sync.RWMutex`（但当前版本保持同步，最小改动）。

```go
// 在 processMessage 中集成话题切换检测
func (a *Agent) processMessage(ctx context.Context, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
    // ... 现有逻辑 ...
    
    tenantSession, err := a.multiSession.GetOrCreateSession(msg.Channel, msg.ChatID)
    
    // === Phase 2 新增：话题切换检测（同步压缩） ===
    if a.topicDetector != nil && a.enableAutoCompress && a.enableTopicIsolation {
        history, _ := tenantSession.GetMessages()
        if len(history) > 10 { // 至少 10 条消息才检测（避免误判）
            segments := a.topicDetector.Detect(history)
            
            if len(segments) > 1 {
                latestSeg := segments[len(segments)-1]
                // 最新话题只有 1-2 条消息（刚切换）
                if latestSeg.MessageCount <= 2 {
                    log.Ctx(ctx).WithFields(log.Fields{
                        "old_topic": segments[len(segments)-2].Keywords,
                        "new_topic": latestSeg.Keywords,
                    }).Info("Topic switch detected, triggering synchronous selective compression")
                    
                    // ⚠️ 同步压缩（不能 go 异步，避免并发写入 session）
                    // 压缩耗时（LLM 调用）约 1-3 秒，在用户消息处理前期可接受
                    result, err := a.selectiveCompressor.Compress(ctx, history, a.client, a.cfg.Model)
                    if err == nil {
                        // 压缩成功，替换 session 消息
                        tenantSession.Clear()
                        for _, m := range result.SessionView {
                            tenantSession.AddMessage(m)
                        }
                    } else {
                        log.Ctx(ctx).WithError(err).Warn("Selective compression failed, continuing with full history")
                    }
                }
            }
        }
    }
    
    // ... 后续逻辑不变 ...
}
```

**话题切换检测的误判缓解**：

| 防护层 | 措施 | 作用 |
|--------|------|------|
| 最小历史 | `len(history) > 10` | 历史太短不检测 |
| 最小片段 | `MinSegmentSize = 3` | 避免碎片化话题 |
| 相似度阈值 | `CosineThreshold = 0.3` | 保守判定，减少假阳性 |
| 新话题短 | `MessageCount <= 2` | 只在刚切换时触发 |
| 压缩失败降级 | 错误时继续使用完整历史 | 不影响主流程 |

### 5.6 话题标注的压缩摘要格式

```
[Previous conversation context]

## 📁 Topic: 认证模块开发 (topic_0)
关键词: auth, login, jwt, middleware, token
摘要: 实现了 JWT 认证中间件。创建了 auth/middleware.go，包含 TokenValidator 和
RefreshToken 逻辑。修复了 token 过期后未正确刷新的 bug（#342）。使用了 RS256 算法。
关键文件: auth/middleware.go, auth/token.go, config/auth.yaml

## 📁 Topic: 数据库迁移 (topic_1)
关键词: migration, schema, postgres, user_table
摘要: 编写了数据库迁移脚本，新增 users 表（含 email, password_hash, created_at 字段）。
使用 golang-migrate 库。迁移 ID: 20260319_001_create_users。
关键文件: migrations/20260319_001_create_users.up.sql

---
↑ 以上为历史话题摘要，可按需召回详情（offload_store 中保留完整数据）
```

---

## 六、方向四：压缩质量保障

### 6.1 设计思路

压缩是有损操作，但信息损失应该可控、可度量。Phase 2 引入三层质量保障：

1. **压缩前校验**：提取关键信息指纹，压缩后验证是否保留
2. **结构化标记**：要求 LLM 压缩输出使用结构化标记，便于检索和校验
3. **质量评分**：量化压缩质量，低于阈值时触发重新压缩

**与 Claude Code 的区别**：Claude Code 没有压缩质量保障机制，压缩结果"黑盒化"。xbot Phase 2 让压缩透明、可审计。

### 6.2 关键信息指纹

```go
// KeyInfoFingerprint 关键信息指纹
type KeyInfoFingerprint struct {
    // 文件路径（压缩后应保留所有被引用的文件路径）
    FilePaths []string `json:"file_paths"`
    
    // 函数/类型/变量名（压缩后应保留关键标识符）
    Identifiers []string `json:"identifiers"`
    
    // 错误信息（压缩后必须保留所有遇到的错误）
    Errors []string `json:"errors"`
    
    // 决策记录（压缩后应保留关键决策）
    Decisions []string `json:"decisions"`
    
    // 待办/未完成任务
    PendingTasks []string `json:"pending_tasks"`
}

// ExtractFingerprint 从消息列表中提取关键信息指纹
// 子函数实现说明：
// - extractPathsFromJSON: 从 JSON 字符串中提取 "path"/"file" 字段值
// - extractFilePaths: 从文本中用正则提取 ./xxx、/xxx、xxx.go 等路径模式
// - extractCodeIdentifiers: 提取驼峰/下划线命名的代码标识符
// - isErrorContext: 检测文本是否包含错误上下文（error, panic, failed, 40x, 50x）
// - extractErrorMessages: 从错误上下文中提取具体错误描述（error行/JSON错误消息）
// - extractDecisions: 提取决策标记（"决定"、"decided"、"choose" 等模式后的内容）
func ExtractFingerprint(messages []llm.ChatMessage) KeyInfoFingerprint {
    fp := KeyInfoFingerprint{}
    seen := make(map[string]bool)
    
    addUnique := func(items []string, target *[]string) {
        for _, item := range items {
            if !seen[item] {
                seen[item] = true
                *target = append(*target, item)
            }
        }
    }
    
    for _, msg := range messages {
        text := msg.Content
        if msg.Role == "tool" {
            // 从工具参数中提取文件路径
            if paths := extractPathsFromJSON(msg.ToolArguments); len(paths) > 0 {
                addUnique(paths, &fp.FilePaths)
            }
        }
        
        addUnique(extractFilePaths(text), &fp.FilePaths)
        addUnique(extractCodeIdentifiers(text), &fp.Identifiers)
        
        if isErrorContext(text) {
            addUnique(extractErrorMessages(text), &fp.Errors)
        }
        
        addUnique(extractDecisions(text), &fp.Decisions)
    }
    
    return fp
}
```

### 6.3 压缩质量校验

```go
// ValidateCompression 校验压缩质量
// 返回保留率 (0.0-1.0) 和丢失的关键信息列表
func ValidateCompression(original, compressed string, fp KeyInfoFingerprint) (float64, []string) {
    total := len(fp.FilePaths) + len(fp.Errors) + len(fp.Decisions)
    if total == 0 {
        return 1.0, nil // 无关键信息需保留
    }
    
    retained := 0
    var lost []string
    
    for _, path := range fp.FilePaths {
        if strings.Contains(compressed, path) {
            retained++
        } else {
            lost = append(lost, "文件路径: "+path)
        }
    }
    
    for _, err := range fp.Errors {
        if strings.Contains(compressed, err) || containsSemanticMatch(compressed, err) {
            retained++
        } else {
            lost = append(lost, "错误信息: "+err)
        }
    }
    
    for _, d := range fp.Decisions {
        if strings.Contains(compressed, d) || containsSemanticMatch(compressed, d) {
            retained++
        } else {
            lost = append(lost, "决策: "+d)
        }
    }
    
    return float64(retained) / float64(total), lost
}

// containsSemanticMatch 语义模糊匹配
// 用于检测压缩摘要中是否"语义上"保留了某条信息（即使措辞不同）
// 实现策略（轻量级，无 LLM 依赖）：
//   1. 精确子串匹配（已在外层做）
//   2. 归一化后子串匹配：统一大小写、去除标点、去除空格
//   3. 关键词重叠度：将原始文本拆分为关键词（空格/标点分割），
//      检查压缩文本中包含的关键词比例是否 >= 0.6
//   4. 编辑距离（可选）：对于短文本（<50字符），检查编辑距离/长度比 < 0.3
func containsSemanticMatch(compressed, target string) bool {
    // 策略 1: 归一化后子串匹配
    normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(compressed, " ", ""), ".", ""))
    normalizedTarget := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(target, " ", ""), ".", ""))
    if strings.Contains(normalized, normalizedTarget) {
        return true
    }
    
    // 策略 2: 关键词重叠度
    targetWords := splitToWords(target)
    if len(targetWords) == 0 {
        return false
    }
    matched := 0
    for _, w := range targetWords {
        if strings.Contains(normalized, strings.ToLower(w)) {
            matched++
        }
    }
    overlapRatio := float64(matched) / float64(len(targetWords))
    if overlapRatio >= 0.6 {
        return true
    }
    
    // 策略 3: 短文本编辑距离
    if len([]rune(target)) < 50 {
        // 简化版 Levenshtein 距离检查（仅比较归一化后的前缀/后缀）
        // 完整 Levenshtein 对长文本太慢，此处用启发式
        if strings.HasPrefix(normalized, normalizedTarget[:len(normalizedTarget)/2]) {
            return true
        }
    }
    
    return false
}

// splitToWords 将文本拆分为关键词（去除常见停用词和短词）
func splitToWords(text string) []string {
    // 按空格和标点分割
    parts := regexp.MustCompile(`[\s,.\-:;!?/\\|(){}[\]<>]+`).Split(text, -1)
    stopWords := map[string]bool{"the": true, "a": true, "an": true, "is": true, "are": true,
        "was": true, "were": true, "in": true, "on": true, "at": true, "to": true, "for": true,
        "of": true, "and": true, "or": true, "的": true, "了": true, "是": true, "在": true}
    var result []string
    for _, p := range parts {
        p = strings.ToLower(strings.TrimSpace(p))
        if len(p) > 1 && !stopWords[p] {
            result = append(result, p)
        }
    }
    return result
}
```

### 6.4 结构化压缩标记

要求 LLM 压缩输出使用以下结构化标记：

```go
// 增强版压缩 prompt
const structuredCompressionPrompt = `You are a context compression expert. Compress the conversation history
into a structured summary.

## OUTPUT FORMAT (IMPORTANT: use these structured markers)

Use the following markers to categorize information:

@file:{path} — File references (e.g., @file:agent/compress.go)
@func:{name} — Function/method signatures (e.g., @func:compressContext())
@type:{name} — Type/struct definitions (e.g., @type:CompressResult)
@error:{description} — Errors encountered (e.g., @error:API returned 429 rate limit)
@decision:{description} — Decisions made (e.g., @decision:use dual-view architecture)
@todo:{description} — Pending tasks (e.g., @todo:add unit tests for extractDialogueFromTail)
@config:{key=value} — Configuration changes (e.g., @config:threshold=0.7)

## Compression Rules
1. Retain ALL key facts, decisions, and important details
2. Mark all file paths with @file:
3. Mark all function/type names with @func: or @type:
4. Mark all errors with @error: — these MUST be preserved
5. Mark decisions with @decision:
6. Mark unfinished tasks with @todo:
7. Maintain chronological order within each topic
8. If multiple topics were discussed, separate them with ## headers

## Conversation History (to compress)
` + "{{ .History }}"
```

**压缩输出示例**：

```
## 认证模块开发

@decision:使用 JWT RS256 算法实现认证
@file:auth/middleware.go — 创建了 TokenValidator 中间件
  @func:ValidateToken() — 验证 JWT token，支持刷新
  @func:RefreshToken() — 使用 refresh_token 获取新的 access_token
@file:auth/token.go — Token 生成和验证工具函数
  @func:GenerateTokenPair() — 生成 access_token + refresh_token 对

@error:token 过期后返回 401 但未触发刷新流程
  修复：在 @file:auth/middleware.go 添加 RefreshMiddleware，检测 401 后自动刷新

@config:JWT_EXPIRY=24h, REFRESH_EXPIRY=7d

@todo:添加 token 黑名单机制（用于登出功能）
@todo:编写 middleware 单元测试
```

### 6.5 压缩质量评分

```go
// CompressionQuality 压缩质量评估结果
type CompressionQuality struct {
    Score          float64  // 综合质量分 (0.0-1.0)
    KeyInfoRate    float64  // 关键信息保留率
    SizeReduction  float64  // 上下文大小缩减率
    MarkerCount    int      // 结构化标记数量
    LostItems      []string // 丢失的关键信息
    Recommendations []string // 改进建议
}

// EvaluateQuality 评估压缩质量
func EvaluateQuality(originalTokens, compressedTokens int, fp KeyInfoFingerprint, compressed string) CompressionQuality {
    quality := CompressionQuality{}
    
    // 1. 关键信息保留率（权重 50%）
    quality.KeyInfoRate, quality.LostItems = ValidateCompression("", compressed, fp)
    
    // 2. 大小缩减率（权重 30%）
    if originalTokens > 0 {
        quality.SizeReduction = 1.0 - float64(compressedTokens)/float64(originalTokens)
    }
    
    // 3. 结构化标记丰富度（权重 20%）
    markers := countStructuredMarkers(compressed)
    quality.MarkerCount = markers
    markerScore := math.Min(float64(markers)/20.0, 1.0) // 20 个标记为满分
    
    // 综合评分
    quality.Score = quality.KeyInfoRate*0.50 + quality.SizeReduction*0.30 + markerScore*0.20
    
    // 生成改进建议
    if quality.KeyInfoRate < 0.8 {
        quality.Recommendations = append(quality.Recommendations,
            fmt.Sprintf("关键信息保留率 %.0f%% 低于阈值，丢失: %v", quality.KeyInfoRate*100, quality.LostItems))
    }
    if quality.SizeReduction < 0.3 {
        quality.Recommendations = append(quality.Recommendations,
            "压缩率低于 30%，建议更激进地压缩")
    }
    if markerScore < 0.3 {
        quality.Recommendations = append(quality.Recommendations,
            "结构化标记不足，建议强化标记要求")
    }
    
    return quality
}

// countStructuredMarkers 统计压缩输出中的结构化标记数量
func countStructuredMarkers(text string) int {
    markers := []string{"@file:", "@func:", "@type:", "@error:", "@decision:", "@todo:", "@config:"}
    count := 0
    for _, m := range markers {
        count += strings.Count(text, m)
    }
    return count
}
```

### 6.6 质量保障集成到压缩流程

```go
// 增强版 compressContext（集成质量保障）
func (a *Agent) compressContextWithQuality(ctx context.Context, 
    messages []llm.ChatMessage, client llm.LLM, model string) (*CompressResult, *CompressionQuality, error) {
    
    // Phase 1: 提取关键信息指纹（压缩前）
    fingerprint := ExtractFingerprint(messages)
    originalTokens, _ := llm.CountMessagesTokens(messages, model)
    
    // Phase 2: 执行压缩（使用结构化 prompt）
    result, err := a.compressContext(ctx, messages, client, model)
    if err != nil {
        return nil, nil, err
    }
    
    // Phase 3: 评估压缩质量
    compressedTokens, _ := llm.CountMessagesTokens(result.SessionView, model)
    compressedText := extractTextFromMessages(result.SessionView)
    
    quality := EvaluateQuality(originalTokens, compressedTokens, fingerprint, compressedText)
    
    // Phase 4: 质量不达标时重新压缩（最多重试 1 次）
    if quality.Score < 0.6 && quality.KeyInfoRate < 0.8 {
        log.Ctx(ctx).WithFields(log.Fields{
            "score":        quality.Score,
            "key_info_rate": quality.KeyInfoRate,
            "lost_items":   quality.LostItems,
        }).Warn("Compression quality below threshold, re-compressing with enhanced prompt")
        
        enhancedResult, err := a.compressContextEnhanced(ctx, messages, fingerprint, client, model)
        if err == nil {
            enhancedTokens, _ := llm.CountMessagesTokens(enhancedResult.SessionView, model)
            enhancedText := extractTextFromMessages(enhancedResult.SessionView)
            newQuality := EvaluateQuality(originalTokens, enhancedTokens, fingerprint, enhancedText)
            
            if newQuality.Score > quality.Score {
                return enhancedResult, &newQuality, nil
            }
        }
        // 增强版也失败，使用原始结果（有总比没有好）
    }
    
    return result, &quality, nil
}
```

---

## 七、实施方案

### 7.1 分阶段实施路线

| 阶段 | 内容 | 预估工作量 | 依赖 |
|------|------|-----------|------|
| **P2.1** | 智能压缩触发（动态阈值 + 冷却机制） | 2天 | 无 |
| **P2.2** | Layer 1 Offload（大 tool result 落盘 + offload_recall） | 2天 | 无 |
| **P2.3** | Layer 2 Evict（信息密度驱逐） | 2天 | 无 |
| **P2.4** | 压缩质量保障（指纹+评分+语义匹配） | 2天 | 无 |
| **P2.5** | 话题分区隔离（含中英文支持） | 3天 | P2.1 |
| **P2.6** | 结构化压缩标记 | 1天 | P2.4 |

**建议实施顺序**：P2.1 → P2.4 → P2.2 → P2.3 → P2.6 → P2.5

理由：
1. P2.1（动态阈值+冷却）改动最小、收益最直接——改善压缩时机和稳定性
2. P2.4（质量保障）为后续所有压缩操作提供质量基线
3. P2.2+P2.3（分层压缩）需要修改压缩流水线
4. P2.6（结构化标记）改善压缩输出格式
5. P2.5（话题分区）最复杂，放最后

### 7.2 文件变更清单

| 文件 | 变更类型 | 说明 |
|------|---------|------|
| `agent/compress.go` | 修改 | 增加 Evict、质量保障逻辑 |
| `agent/engine.go` | 修改 | maybeCompress 集成动态阈值、增长追踪、冷却机制 |
| `agent/topic.go` | **新增** | 话题检测（含中英文分词）、分区管理、选择性压缩 |
| `agent/offload.go` | **新增** | Offload store 管理、offload_recall 工具 |
| `agent/quality.go` | **新增** | 压缩质量评估（指纹提取、语义匹配、评分） |
| `agent/trigger.go` | **新增** | 动态阈值算法、增长速率追踪、冷却机制、工具模式检测 |
| `llm/types.go` | 修改 | ChatMessage 增加 OffloadRef 字段（可选） |
| `agent/agent.go` | 修改 | 增加 Phase 2 配置项、growthTrackers map、cooldowns map |
| `tools/offload_recall.go` | **新增** | offload_recall 工具实现（供 Agent 工具调用） |

### 7.3 配置项扩展

```go
// Phase 2 新增配置项（全部有默认值，默认关闭实验性功能）
type Config struct {
    // ... 现有配置 ...
    
    // === Phase 2: 分层压缩 ===
    EnableOffload     bool    `json:"enable_offload"`      // 启用 Layer 1 Offload（默认 true）
    OffloadMaxTokens  int     `json:"offload_max_tokens"`   // 单条 tool result 触发 offload 的阈值（默认 2000）
    
    // === Phase 2: 智能触发 ===
    EnableDynamicThreshold bool `json:"enable_dynamic_threshold"` // 启用动态阈值（默认 true）
    CompressCooldownRounds  int  `json:"compress_cooldown_rounds"` // 压缩冷却轮数（默认 3）
    
    // === Phase 2: 话题分区 ===
    EnableTopicIsolation   bool    `json:"enable_topic_isolation"`    // 启用话题分区隔离（默认 false，实验性）
    TopicMinSegmentSize    int     `json:"topic_min_segment_size"`    // 最小话题片段长度（默认 3）
    TopicSimilarityThreshold float64 `json:"topic_similarity_threshold"` // 话题切换相似度阈值（默认 0.3）
    
    // === Phase 2: 质量保障 ===
    EnableQualityCheck     bool    `json:"enable_quality_check"`     // 启用压缩质量校验（默认 true）
    MinCompressionQuality  float64 `json:"min_compression_quality"`  // 最低压缩质量分（默认 0.6）
}
```

---

## 八、风险评估

| 风险 | 影响 | 概率 | 缓解措施 |
|------|------|------|----------|
| Offload 磁盘 IO 影响性能 | 工具执行后写入延迟增加 | 低 | offload 文件写入为同步但轻量（JSON 序列化+单文件写入，毫秒级）；/new 时清理 |
| 话题检测误判 | 正常对话被误认为话题切换 | 中 | MinSegmentSize=3 + 阈值 0.3 + 仅 >10 条时检测 + MessageCount<=2 才触发 |
| 动态阈值计算开销 | 每次迭代增加计算 | 低 | 增长追踪用滑窗 O(10)；工具模式检测 O(5)；均纳秒级 |
| 结构化标记 LLM 不遵守 | 压缩输出不含 @file: 等标记 | 中 | prompt 强调 + 质量评分检测 + 不遵守时降级为无标记模式 |
| 多话题压缩 LLM 调用增加 | 按话题分别压缩增加成本 | 低 | 最多 3 个历史话题分别压缩；超出合并压缩 |
| 质量重新压缩的 LLM 成本 | 不达标时需要额外调用 | 低 | 最多重试 1 次；重新压缩 prompt 更紧凑 |
| 话题压缩同步执行延迟 | 压缩耗时 1-3 秒阻塞主流程 | 中 | 仅在话题切换时触发（低频）；压缩失败时降级继续 |
| 中文 bigram 话题检测精度 | 不如完整分词 | 低 | bigram 对话题边界检测够用；可后续升级为 jieba |

---

## 九、预期效果

### 9.1 量化指标

| 指标 | Claude Code | xbot Phase 1 | xbot Phase 2 |
|------|------------|-------------|-------------|
| 上下文利用率 | ~75%（静态阈值） | ~70%（静态阈值） | **50-85%**（动态阈值） |
| 大 tool result 处理 | 摘要化 | 摘要化 | **Offload 到磁盘 + 可召回** |
| 话题隔离 | ❌ | ❌ | **✅ 自动检测 + 选择性压缩** |
| 压缩质量可观测 | ❌ | ❌ | **✅ 指纹校验 + 评分** |
| 压缩信息损失 | 不可知 | 不可知 | **可度量（关键信息保留率）** |
| 提前压缩能力 | ❌ | ❌ | **✅ 指数增长预测 + 提前触发** |
| 压缩冷却保护 | ❌ | ❌ | **✅ 3 轮冷却防止无限循环** |
| 多语言话题检测 | ❌ | ❌ | **✅ 中英文 bigram 混合支持** |

### 9.2 用户体验提升

- **长任务稳定性**：动态阈值 + 提前压缩 + 冷却保护，避免"上下文爆满→强制压缩→丢失关键信息"的恶性循环
- **多话题流畅度**：话题分区隔离确保切换话题时不会丢失之前话题的上下文
- **大文件处理**：Offload 让 Agent 可以读取任意大小的文件而不会撑爆上下文
- **压缩透明度**：质量评分让用户（和开发者）了解压缩效果

---

## 十、附录：与 Claude Code 的全面对比

| 维度 | Claude Code (2025) | xbot Phase 2 |
|------|-------------------|-------------|
| **压缩触发** | 静态 64-75% | 动态 50-85%，感知任务阶段/增长速率/工具模式 |
| **压缩层级** | 单层（LLM 摘要） | 三层渐进（Offload → Evict → Compact） |
| **大结果处理** | LLM 摘要化 | Offload 到磁盘 + 可按 ID 召回 |
| **信息密度** | 无概念 | 按密度评分选择性驱逐 |
| **话题感知** | ❌（社区强烈要求） | ✅ 自动检测 + 选择性压缩 |
| **子任务级压缩** | ❌（社区强烈要求） | ✅ 话题分区实现类似效果 |
| **压缩质量** | 黑盒 | 指纹校验 + 质量评分 + 结构化标记 |
| **增长预测** | ❌ | 加权线性回归 + 指数增长检测 |
| **压缩冷却** | ❌ | 3 轮冷却防止无限循环 |
| **可观测性** | 仅 token 数 | 压缩质量分、关键信息保留率、丢失列表 |
| **压缩可恢复性** | ❌（不可逆） | ✅（offload_store 保留原始数据） |
| **多语言支持** | 英文 | 中英文混合话题检测 |
