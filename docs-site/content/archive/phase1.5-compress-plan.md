---
title: "phase1.5-compress-plan"
weight: 130
---


> **状态**：✅ 门下省审核通过，待工部实施
> **创建日期**：2026-03-19
> **依赖**：无外部依赖，纯内部重构

---

## 1. 概述

### 1.1 背景与问题

Phase 2 上下文压缩架构（`context_manager_phase2.go`）存在根本性缺陷——**eviction 层会导致死循环**：

```
evictByDensity() 驱逐了 LLM 正在使用的代码内容
  → LLM 丢失上下文，重新读取文件
    → 上下文再次膨胀
      → 再次触发 eviction
        → ∞ 死循环
```

eviction 这个概念在 coding agent 场景下不可救药：agent 的工作模式就是反复读取和修改文件，而 eviction 的核心假设是"旧内容可以安全丢弃"——这两者根本矛盾。

### 1.2 Phase 1.5 定位

**Phase 1.5 = Phase 1 的简单架构 + Phase 2 的好部件**

| 来源 | 保留/删除 | 部件 |
|------|----------|------|
| Phase 1 架构 | ✅ 保留 | 简单的 Offload → Compact 流程 |
| Phase 2 fingerprint | ✅ 保留 | `ExtractFingerprint()` + `compressMessagesWithFingerprint()` |
| Phase 2 质量评分 | ✅ 保留 | `EvaluateQuality()` + `ValidateCompression()` |
| Phase 2 冷却期 | ✅ 保留 | `CompressCooldown`（含死循环检测） |
| Phase 2 智能触发 | ✅ 保留 | `ShouldCompressDynamic()` + `BuildTriggerInfo()` |
| Phase 2 eviction | ❌ 删除 | `evictByDensity()` 及其所有辅助代码 |
| Phase 2 流水线 | ⚠️ 简化 | 从 "Offload → Evict → Compact" 变为 "Offload → Compact" |

### 1.3 核心变化

1. **去掉 eviction 层**：不再按信息密度驱逐 tool result，避免死循环
2. **增强 fingerprint**：新增"活跃文件"追踪，保护 LLM 正在使用的文件不被截断
3. **改进压缩 prompt**：从"压缩对话"改为"提取任务状态"，输出结构化任务状态文档
4. **智能 tail 保护**：涉及活跃文件的 tool 组全量保留，仅对非活跃 tail 截断

---

## 2. 删除项

以下代码从 `xbot/agent/compress.go` 中彻底删除：

### 2.1 删除的函数

| 函数名 | 所在文件 | 行号 | 说明 |
|--------|---------|------|------|
| `defaultDensityScorer()` | `compress.go` | 第221行 | 默认信息密度评分 |
| `containsErrorPattern()` | `compress.go` | 第262行 | 错误关键词检测 |
| `containsDecisionPattern()` | `compress.go` | 第274行 | 决策关键词检测 |
| `isLargeCodeDump()` | `compress.go` | 第286行 | 大代码块检测 |
| `isRepetitiveGrepResult()` | `compress.go` | 第300行 | 重复 grep 结果检测 |
| `evictByDensity()` | `compress.go` | 第338行 | 核心 eviction 函数 |
| `buildCompressResultFromEvicted()` | `compress.go` | 第421行 | 从 evicted 消息构建结果 |
| `countEvictedMessages()` | `compress.go` | 第639行 | 统计被驱逐消息数 |

### 2.2 删除的结构体/类型

| 名称 | 所在文件 | 行号 | 说明 |
|------|---------|------|------|
| `evictToolGroup` | `compress.go` | 第331行 | 工具组边界 [start, end] |
| `DensityScore` | `compress.go` | 第214行 | 信息密度评分结果 |

### 2.3 删除的调用（从 phase2Manager 中移除）

**`context_manager_phase2.go`** 中的 `Compress()` 方法（第65行起）和 `ManualCompress()` 方法（第167行起）：

- `Compress()` 中删除 `evictByDensity()` 调用
- `Compress()` 中删除 `buildCompressResultFromEvicted()` 调用
- `Compress()` 中删除 `countEvictedMessages()` 调用
- `ManualCompress()` 中删除 `evictByDensity()` 调用
- `ManualCompress()` 中删除 `buildCompressResultFromEvicted()` 调用

### 2.4 删除的测试文件

| 文件 | 说明 |
|------|------|
| `evict_test.go` | **整个文件删除**。包含 24 个测试函数（`TestContainsErrorPattern_*`、`TestContainsDecisionPattern_*`、`TestIsLargeCodeDump_*`、`TestIsRepetitiveGrepResult_*`、`TestDefaultDensityScorer_*`、`TestEvictByDensity_*` 等） |

### 2.5 删除 compress_test.go 中的测试

| 测试函数 | 行号 | 说明 |
|---------|------|------|
| `TestBuildCompressResultFromEvicted_NoDuplicateSystemMessage` | 第204行 | 测试被删函数 |
| `TestBuildCompressResultFromEvicted_SystemPreservedAtFront` | 第240行 | 测试被删函数 |

---

## 3. 保留但整合项

以下部件从 Phase 2 保留，并整合到简化后的架构中：

### 3.1 fingerprint 引导

**保留**：`ExtractFingerprint()` + `compressMessagesWithFingerprint()`

- **位置**：`quality.go:24`（`ExtractFingerprint`）、`compress.go:666`（`compressMessagesWithFingerprint`）
- **行为**：不变。压缩前提取关键信息指纹，注入 LLM prompt 引导保留
- **增强**：见第 4 节

### 3.2 质量评分

**保留**：`EvaluateQuality()` + `ValidateCompression()` + `ValidateMarkers()`

- **位置**：`quality.go:57`（`ValidateCompression`）、`quality.go:117`（`EvaluateQuality`）、`quality.go:417`（`ValidateMarkers`）
- **行为**：不变。Phase 2 的 `Compress()` 仍使用质量评分决定是否重试

### 3.3 冷却期与死循环检测

**保留**：`CompressCooldown`（`RecordIneffective()` / `RecordEffective()`）

- **位置**：`trigger.go:123`（`CompressCooldown`）、`trigger.go:156`（`RecordIneffective`）、`trigger.go:164`（`RecordEffective`）
- **行为**：不变。连续低效压缩自动加大冷却期

### 3.4 智能触发

**保留**：`ShouldCompressDynamic()` + `BuildTriggerInfo()` + `calculateDynamicThreshold()` + `DetectToolPattern()`

- **位置**：`context_manager_phase2.go:35`（`ShouldCompressDynamic`）、`trigger.go:183`（`calculateDynamicThreshold`）、`trigger.go:234`（`DetectToolPattern`）、`trigger.go:284`（`BuildTriggerInfo`）
- **行为**：不变。三因子动态阈值（阶段因子 + 增长因子 + 模式因子）

### 3.5 thinTail / aggressiveThinTail

**保留但改为可选**：`thinTail()` + `aggressiveThinTail()`

- **位置**：`compress.go:512`（`thinTail`）、`compress.go:573`（`aggressiveThinTail`）
- **行为变更**：不再无条件截断所有旧 tool 组，改为仅当 token 超限时对**非活跃 tail** 截断
- **活跃文件保护**：见第 4.3 节

---

## 4. 增强项

### 4.1 增强 fingerprint：活跃文件追踪

**目标**：识别 LLM 最近 N 轮正在操作的文件，在压缩时全量保护。

#### 4.1.1 新增 `ActiveFile` 结构体

在 `quality.go` 中新增：

```go
// ActiveFile 最近 N 轮活跃文件记录
type ActiveFile struct {
    Path         string   // 文件路径
    LastSeenIter int      // 最后出现的轮次
    Functions    []string // 涉及的函数签名（从 tool result 中提取）
}

// ExtractActiveFiles 从最近 N 轮 tool call 中提取活跃文件。
// 扫描 messages 尾部的 tool call，提取 Read/Edit/Shell 等涉及的文件路径。
func ExtractActiveFiles(messages []llm.ChatMessage, lastN int) []ActiveFile
```

#### 4.1.2 修改 `KeyInfoFingerprint` 结构体

在 `quality.go:12` 中，为 `KeyInfoFingerprint` 新增字段：

```go
type KeyInfoFingerprint struct {
    FilePaths    []string      // 原有
    Identifiers  []string      // 原有
    Errors       []string      // 原有
    Decisions    []string      // 原有
    ActiveFiles  []ActiveFile  // 新增：最近 N 轮的活跃文件
}
```

#### 4.1.3 修改 `ExtractFingerprint()`

在 `quality.go:24` 的 `ExtractFingerprint()` 中调用 `ExtractActiveFiles()`，将活跃文件填充到 `fp.ActiveFiles`：

```go
func ExtractFingerprint(messages []llm.ChatMessage) KeyInfoFingerprint {
    // ... 原有逻辑 ...
    
    // 新增：提取活跃文件
    fp.ActiveFiles = ExtractActiveFiles(messages, 3)
    return fp
}
```

#### 4.1.4 `ExtractActiveFiles` 实现要点

从 messages 尾部向前扫描，找到最近 3 轮 tool call（一轮 = 一组 assistant(tool_calls) + 对应 tool results），按以下规则提取文件路径：

| 工具名 | 参数来源 | 路径提取字段 |
|--------|---------|-------------|
| Read | `Arguments` JSON | `path` |
| Edit / Write | `Arguments` JSON | `path` 或 `file_path` |
| Glob | `Arguments` JSON | `pattern` |
| Grep | `Arguments` JSON | `path` |
| Shell | 无固定参数 | 从 `Content` 中正则提取 `/\S+\.go` 等 |
| SubAgent | 不适用 | 不提取 |

- 从 `Content` 中提取函数签名（正则匹配 `func \w+` 模式）
- 去重后按 `LastSeenIter` 降序排列

### 4.2 改进压缩 prompt

**目标**：从"压缩对话摘要"改为"提取结构化任务状态文档"。

#### 4.2.1 替换 `structuredCompressionPrompt`

在 `compress.go:22` 中，将现有的 `structuredCompressionPrompt` 替换为新版本 `taskStatePrompt`：

```go
const taskStatePrompt = `You are a task state extraction expert. Extract the current task state from the conversation into a structured document.

## Goal
Transform verbose conversation history into a concise, structured task state document.
Focus on WHAT has been done, WHAT is in progress, and WHAT remains.

## Output Format
Use these structured sections and markers:

### 📋 Task Summary
Brief overview of what the user asked for and current progress.

### 📁 Active Files
@file:{path} — Files currently being worked on (MUST include ALL active file references)
@func:{signature} — Key function signatures from active files

### ✅ Completed Steps
- What has been done so far (with file paths and specific details)

### 🔄 Current Step
What is being worked on right now. Include:
- Current file being edited/read
- Pending modifications
- Context needed to continue

### ❌ Errors (MUST preserve ALL)
@error:{description} — Every error encountered (essential for debugging)

### 📌 Decisions
@decision:{description} — All decisions made during this session

### 📝 Pending Tasks
@todo:{description} — Tasks not yet started

## Compression Rules
1. PRESERVE ALL file paths that appear in active file operations
2. PRESERVE ALL error messages verbatim
3. PRESERVE all function signatures from active files
4. Include specific details (variable names, line numbers, code snippets)
5. If 📂 [offload:...] markers exist, preserve them verbatim
6. Prioritize RECENT information over old history
7. This is NOT a summary — it's a task state document for continuing work`
```

#### 4.2.2 修改 prompt 注入逻辑

在 `compress.go:666` 的 `compressMessagesWithFingerprint()` 中：

1. 将第778行的 `structuredCompressionPrompt` 替换为 `taskStatePrompt`
2. 在 `fpSection` 中新增活跃文件区块：

```go
if len(fp.ActiveFiles) > 0 {
    fpSection.WriteString("\n## ACTIVE FILES (must be fully preserved in output):\n")
    for _, af := range fp.ActiveFiles {
        fmt.Fprintf(&fpSection, "  @file:%s\n", af.Path)
        for _, fn := range af.Functions {
            fmt.Fprintf(&fpSection, "  @func:%s\n", fn)
        }
    }
}
```

### 4.3 不截断活跃 tail

**目标**：tail 部分如果涉及活跃文件，全量保留。

#### 4.3.1 修改 `thinTail()` 签名

```go
// thinTail 精简尾部旧工具组，保留最近 keepGroups 组完整内容。
// activeFiles: 活跃文件集合，涉及这些文件的 tool 组不会被截断。
func thinTail(tail []llm.ChatMessage, keepGroups int, activeFiles []ActiveFile) []llm.ChatMessage
```

#### 4.3.2 实现要点

- 在识别工具组后，额外检查每个组的 tool call 参数中是否包含活跃文件路径
- 如果 tool group 涉及活跃文件，将其标记为"活跃组"，即使不在 `keepGroups` 范围内也不截断
- 仅对非活跃组执行原有的截断逻辑

#### 4.3.3 同步修改 `aggressiveThinTail()`

签名同样增加 `activeFiles` 参数，逻辑与 `thinTail` 一致。

#### 4.3.4 修改调用链

在 `compressMessagesWithFingerprint()` 中，第690行的 `thinTail` 调用和第815行的 `aggressiveThinTail` 调用均需传递 `activeFiles`：

```go
// 第二步：精简尾部旧工具组
activeFiles := ExtractActiveFiles(messages, 3)
var thinnedTail []llm.ChatMessage
if tailStart < len(messages) {
    thinnedTail = thinTail(messages[tailStart:], 1, activeFiles)
}
```

---

## 5. 架构变更

### 5.1 Phase 2 新流程（简化后）

```
Compress():
  1. ExtractFingerprint() — 提取指纹 + 活跃文件
  2. thinTail(activeFiles) — 仅截断非活跃 tail
  3. compressMessagesWithFingerprint() — LLM 压缩（使用 taskStatePrompt）
  4. EvaluateQuality() + ValidateCompression() — 质量校验
  5. 低质量时重试（compressMessagesWithFingerprintAndLostItems）
  6. 仍不足时 aggressiveThinTail(activeFiles)
```

对比旧流程：

```
旧：Offload → Evict（evictByDensity）→ Compact（LLM）
新：Offload → Compact（LLM + 增强fingerprint + 活跃文件保护）
```

### 5.2 phase2Manager 简化

`phase2Manager` 从"三层流水线调度器"简化为"外层壳"：

| 职责 | 保留 | 说明 |
|------|------|------|
| 冷却期管理 | ✅ | `CompressCooldown` |
| 智能触发 | ✅ | `ShouldCompressDynamic()` |
| 质量评分 | ✅ | `EvaluateQuality()` + `ValidateCompression()` |
| 低质量重试 | ✅ | `compressMessagesWithFingerprintAndLostItems()` |
| eviction 调度 | ❌ 删除 | 不再调用 `evictByDensity()` |
| evicted 结果构建 | ❌ 删除 | 不再调用 `buildCompressResultFromEvicted()` |

### 5.3 context_manager.go 中的注释更新

`NewContextManager()` 第216-217行需更新：

```go
// 旧（第216-217行）：
// Phase 2: 三层渐进压缩（Offload → Evict → Compact）
log.WithField("mode", mode).Info("Using Phase 2 smart compression (Offload → Evict → Compact)")
// 新：
// Phase 2: 智能压缩（Offload → Compact，无 Evict 层）
log.WithField("mode", mode).Info("Using Phase 1.5 smart compression (Offload → Compact)")
```

同时更新 `ContextModePhase2` 常量注释（第17行）：

```go
// 旧：
// ContextModePhase2 Phase 2 三层渐进压缩
// 新：
// ContextModePhase2 Phase 2 智能压缩（Offload → Compact，含 fingerprint 引导与活跃文件保护）
```

### 5.4 context_manager_phase2.go 完整重写

`phase2Manager.Compress()` 方法（第65行起）重写为：

```go
func (m *phase2Manager) Compress(ctx context.Context, messages []llm.ChatMessage, client llm.LLM, model string) (*CompressResult, error) {
    originalTokens, _ := llm.CountMessagesTokens(messages, model)

    log.Ctx(ctx).WithFields(map[string]interface{}{
        "original_tokens": originalTokens,
        "max_tokens":      m.config.MaxContextTokens,
    }).Info("Phase 1.5 compress: starting")

    // 步骤1：提取指纹（含活跃文件）
    fp := ExtractFingerprint(messages)

    // 步骤2：带 fingerprint 引导的 LLM 压缩
    result, err := compressMessagesWithFingerprint(ctx, messages, fp, client, model)
    if err != nil {
        return nil, err
    }

    // 步骤3：质量校验
    compressedText := joinMessages(result.SessionView)
    compressedTokens, _ := llm.CountMessagesTokens(result.LLMView, model)
    quality := EvaluateQuality(originalTokens, compressedTokens, fp, compressedText)

    _, lostItems := ValidateCompression(messages, result.SessionView, fp)
    retentionRate := 1.0
    if totalItems := len(fp.FilePaths) + len(fp.Identifiers) + len(fp.Errors) + len(fp.Decisions); totalItems > 0 {
        retentionRate = float64(totalItems-len(lostItems)) / float64(totalItems)
    }

    // 步骤4：低质量重试
    if quality < 0.6 && retentionRate < 0.8 && client != nil && len(lostItems) > 0 {
        retryResult, retryErr := compressMessagesWithFingerprintAndLostItems(ctx, messages, fp, lostItems, client, model)
        if retryErr == nil {
            retryText := joinMessages(retryResult.SessionView)
            retryTokens, _ := llm.CountMessagesTokens(retryResult.LLMView, model)
            retryQuality := EvaluateQuality(originalTokens, retryTokens, fp, retryText)
            if retryQuality > quality {
                quality = retryQuality
                result = retryResult
            }
        }
    }

    // 步骤5：标记完整性检测
    missingMarkers := ValidateMarkers(compressedText, fp)
    if len(missingMarkers) > 0 {
        log.Ctx(ctx).WithFields(map[string]interface{}{
            "missing_markers": len(missingMarkers),
            "quality_score":   quality,
        }).Warn("Phase 1.5 compress missing structured markers")
    }

    log.Ctx(ctx).WithFields(map[string]interface{}{
        "quality_score":  quality,
        "retention_rate": retentionRate,
        "markers":        countStructuredMarkers(compressedText),
        "new_tokens":     compressedTokens,
    }).Info("Phase 1.5 compress quality report")

    return result, nil
}
```

`phase2Manager.ManualCompress()` 方法（第167行起）重写为：

```go
func (m *phase2Manager) ManualCompress(ctx context.Context, messages []llm.ChatMessage, client llm.LLM, model string) (*CompressResult, error) {
    fp := ExtractFingerprint(messages)
    result, err := compressMessagesWithFingerprint(ctx, messages, fp, client, model)
    if err != nil {
        // 降级到不带指纹的压缩
        return compressMessages(ctx, messages, client, model)
    }
    return result, nil
}
```

### 5.5 不再需要 `compressMessagesWithFingerprint` 中的 evicted 参数

旧流程中 `Compress()` 先调用 `evictByDensity()` 得到 `evicted` 消息，再传入 `compressMessagesWithFingerprint()`。
新流程直接传入原始 `messages`，由 `compressMessagesWithFingerprint()` 内部处理 tail 截断。

这实际上简化了调用链——`compressMessagesWithFingerprint()` 已经内置了 tail 分离和 thinTail 逻辑（第690行、第815行），无需外部预处理。

---

## 6. 受影响文件清单

### 6.1 `agent/compress.go`

| 操作 | 对象 | 行号 | 说明 |
|------|------|------|------|
| **删除** | `DensityScore` 结构体 | 第214行 | 信息密度评分结果 |
| **删除** | `defaultDensityScorer()` | 第221行 | 默认密度评分函数 |
| **删除** | `containsErrorPattern()` | 第262行 | 错误关键词检测 |
| **删除** | `containsDecisionPattern()` | 第274行 | 决策关键词检测 |
| **删除** | `isLargeCodeDump()` | 第286行 | 大代码块检测 |
| **删除** | `isRepetitiveGrepResult()` | 第300行 | 重复 grep 检测 |
| **删除** | `evictToolGroup` 结构体 | 第331行 | 工具组边界 |
| **删除** | `evictByDensity()` | 第338行 | 核心 eviction 函数 |
| **删除** | `buildCompressResultFromEvicted()` | 第421行 | evicted 结果构建 |
| **删除** | `countEvictedMessages()` | 第639行 | 统计被驱逐消息数 |
| **替换** | `structuredCompressionPrompt` | 第22行 | 改为 `taskStatePrompt` |
| **修改** | `thinTail()` | 第512行 | 新增 `activeFiles` 参数 |
| **修改** | `aggressiveThinTail()` | 第573行 | 新增 `activeFiles` 参数 |
| **修改** | `compressMessagesWithFingerprint()` | 第666行 | 使用新 prompt + 注入活跃文件 + 传递 activeFiles 给 thinTail |

### 6.2 `agent/quality.go`

| 操作 | 对象 | 行号 | 说明 |
|------|------|------|------|
| **新增** | `ActiveFile` 结构体 | — | 活跃文件记录 |
| **新增** | `ExtractActiveFiles()` | — | 提取最近 N 轮活跃文件 |
| **修改** | `KeyInfoFingerprint` | 第12行 | 新增 `ActiveFiles` 字段 |
| **修改** | `ExtractFingerprint()` | 第24行 | 调用 `ExtractActiveFiles()` 填充新字段 |

### 6.3 `agent/context_manager_phase2.go`

| 操作 | 对象 | 行号 | 说明 |
|------|------|------|------|
| **重写** | `Compress()` | 第65行 | 去掉 eviction，直接 fingerprint + LLM 压缩 |
| **重写** | `ManualCompress()` | 第167行 | 去掉 eviction，直接 fingerprint + LLM 压缩 |
| **修改** | 注释 | 第12行附近 | 流程描述从 "Offload → Evict → Compact" 更新 |

### 6.4 `agent/context_manager.go`

| 操作 | 对象 | 行号 | 说明 |
|------|------|------|------|
| **修改** | `ContextModePhase2` 注释 | 第17行 | 从"三层渐进压缩"更新 |
| **修改** | `NewContextManager()` 日志文案 | 第216-217行 | 从 "Offload → Evict → Compact" 更新 |

### 6.5 `agent/evict_test.go`

| 操作 | 说明 |
|------|------|
| **删除整个文件** | 24 个测试函数全部删除 |

### 6.6 `agent/compress_test.go`

| 操作 | 对象 | 行号 | 说明 |
|------|------|------|------|
| **删除** | `TestBuildCompressResultFromEvicted_NoDuplicateSystemMessage` | 第204行 | 被删函数的测试 |
| **删除** | `TestBuildCompressResultFromEvicted_SystemPreservedAtFront` | 第240行 | 被删函数的测试 |
| **修改** | `TestCompressMessagesWithFingerprint_InjectsFingerprint` | 第263行 | 验证新 prompt 格式（active files 注入） |

### 6.7 `agent/quality_test.go`

| 操作 | 对象 | 说明 |
|------|------|------|
| **新增** | `TestExtractActiveFiles_*` | 活跃文件提取测试 |
| **新增** | `TestExtractFingerprint_WithActiveFiles` | 验证 fingerprint 包含活跃文件 |
| **修改** | `TestExtractFingerprint_*` | 考虑新增 ActiveFiles 字段 |

### 6.8 `agent/context_manager_test.go`

| 操作 | 对象 | 说明 |
|------|------|------|
| **修改** | `TestPhase2Manager_Compress` | 更新预期行为（不再有 eviction 阶段） |

### 6.9 未受影响的文件

以下文件**不受影响**，无需修改：

- `agent/trigger.go` — 智能触发逻辑完全保留
- `agent/trigger_test.go` — 对应测试保留
- `agent/context_manager_phase1.go` — Phase 1 管理器不变
- `agent/offload.go` / `agent/offload_test.go` — offload 独立于 eviction

---

## 7. 测试策略

### 7.1 需要删除的测试

| 测试文件 | 测试函数数 | 原因 |
|---------|----------|------|
| `evict_test.go` | 全部 24 个测试 | eviction 功能整体删除 |
| `compress_test.go` | 2 个（`TestBuildCompressResultFromEvicted_*`） | 被删函数测试 |

**共计删除 26 个测试。**

### 7.2 需要修改的测试

| 测试文件 | 测试函数 | 修改内容 |
|---------|---------|---------|
| `compress_test.go` | `TestCompressMessagesWithFingerprint_InjectsFingerprint`（第263行） | 验证新 prompt 包含 `taskStatePrompt` 关键词（如"task state"、"Active Files"）而非旧的 "context compression" |
| `compress_test.go` | `TestCompressMessagesWithFingerprint_EmptyFingerprintNoInjection`（第317行） | 确保 ActiveFiles 为空时不注入活跃文件区块 |
| `context_manager_test.go` | `TestPhase2Manager_Compress` | Phase 2 Compress 不再有 eviction 阶段，直接走 LLM 压缩路径 |
| `quality_test.go` | `TestExtractFingerprint_*` | 验证 `fp.ActiveFiles` 字段被正确填充 |

### 7.3 需要新增的测试

#### `quality_test.go` 新增

| 测试函数 | 说明 |
|---------|------|
| `TestExtractActiveFiles_WithReadToolCalls` | 从 Read tool call 的 Arguments JSON 提取 `path` 字段 |
| `TestExtractActiveFiles_WithEditToolCalls` | 从 Edit tool call 的 Arguments JSON 提取 `path` 字段 |
| `TestExtractActiveFiles_WithShellGrep` | 从 Shell(grep) 结果中正则提取文件路径 |
| `TestExtractActiveFiles_Deduplication` | 相同文件出现多次只记录一次 |
| `TestExtractActiveFiles_FunctionSignatures` | 从 tool result 中提取函数签名 |
| `TestExtractActiveFiles_EmptyMessages` | 空消息返回空结果 |
| `TestExtractFingerprint_WithActiveFiles` | 验证 ExtractFingerprint 正确填充 ActiveFiles |

#### `compress_test.go` 新增

| 测试函数 | 说明 |
|---------|------|
| `TestThinTail_PreservesActiveFileGroups` | 涉及活跃文件的 tool 组不被截断 |
| `TestThinTail_StillThinsInactiveGroups` | 不涉及活跃文件的 tool 组正常截断 |
| `TestAggressiveThinTail_PreservesActiveFileGroups` | 激进模式下也保护活跃文件 |
| `TestCompressMessagesWithFingerprint_ActiveFilesInPrompt` | 活跃文件被注入到压缩 prompt 中 |

#### `context_manager_test.go` 新增

| 测试函数 | 说明 |
|---------|------|
| `TestPhase2Manager_Compress_NoEviction` | 验证 Phase 2 Compress 不再调用 eviction |

**共计新增约 11 个测试。**

---

## 8. 向后兼容

### 8.1 API 兼容性

| 接口 | 变化 | 影响 |
|------|------|------|
| `ContextManager` 接口（`context_manager.go:40`） | **不变** | 所有实现者签名不变 |
| `SmartCompressor` 接口（`context_manager.go:185`） | **不变** | Phase 2 仍实现此接口 |
| `phase2Manager.Mode()` | **不变** | 返回 `ContextModePhase2` |
| `ContextModePhase2` 常量 | **不变** | 值仍为 `"phase2"` |
| `/context mode phase2` 命令 | **不变** | 仍可切换，用户无感知 |

### 8.2 行为变化

| 场景 | 旧行为 | 新行为 |
|------|--------|--------|
| Phase 2 自动压缩 | Evict → Compact | 直接 Compact |
| Phase 2 手动压缩 | Evict → Compact | 直接 Compact |
| 压缩 prompt | "压缩对话历史" | "提取任务状态文档" |
| tail 截断 | 无条件截断旧组 | 保护活跃文件组 |

用户感知：
- ✅ `/context mode phase2` 仍然有效
- ✅ `/compress` 手动命令仍然有效
- ✅ 已有 session 数据不受影响（压缩是对运行时上下文的操作，不改变 session 存储格式）
- ⚠️ 压缩后的文本格式会变化（从对话摘要变为结构化任务状态文档），但这是内部格式，不影响功能

### 8.3 回滚方案

如果 Phase 1.5 出现问题，回滚步骤：

1. `git revert` 到重构前的 commit
2. eviction 相关代码已在版本控制中完整保留
3. 无数据库 schema 变更，无 session 数据格式变更
4. 回滚后 `ContextModePhase2` 恢复为 eviction 模式

---

## 9. 实施步骤

按依赖关系排序，每步独立可验证：

| 步骤 | 内容 | 验证方式 |
|------|------|---------|
| **Step 1** | 删除 eviction 代码：从 `compress.go` 删除 `evictByDensity` 等 8 个函数/结构体 | `go build ./agent/...` 通过；被删函数引用均不存在 |
| **Step 2** | 删除 eviction 测试：删除 `evict_test.go` 整个文件 + `compress_test.go` 中 2 个被删函数测试 | `go test ./agent/...` 通过（不含新测试） |
| **Step 3** | 重写 `phase2Manager`：简化 `Compress()`（第65行）和 `ManualCompress()`（第167行） | `go test ./agent/... -run TestPhase2Manager` 通过 |
| **Step 4** | 更新注释和日志：`context_manager.go` 第17行、第216-217行 | 代码审查 |
| **Step 5** | 新增 `ActiveFile` + `ExtractActiveFiles()` 到 `quality.go` | `go test ./agent/... -run TestExtractActiveFiles` 通过 |
| **Step 6** | 修改 `KeyInfoFingerprint`（第12行）+ `ExtractFingerprint()`（第24行） | `go test ./agent/... -run TestExtractFingerprint` 通过 |
| **Step 7** | 替换压缩 prompt（第22行）+ 修改 `compressMessagesWithFingerprint()`（第666行） | `go test ./agent/... -run TestCompressMessagesWithFingerprint` 通过 |
| **Step 8** | 修改 `thinTail()`（第512行）/ `aggressiveThinTail()`（第573行）增加活跃文件保护 | `go test ./agent/... -run "TestThinTail|TestAggressiveThinTail"` 通过 |
| **Step 9** | 全量测试 | `go test ./agent/...` 全部通过 |
| **Step 10** | 集成测试 | 启动 xbot，使用 phase2 模式执行长对话，观察压缩行为 |

---

## 10. 风险与注意

| 风险 | 等级 | 缓解措施 |
|------|------|---------|
| 去掉 eviction 后 LLM 压缩成本增加 | 中 | eviction 本身有死循环风险，LLM 压缩是正确路径；通过 thinTail 控制输入大小 |
| 新 prompt 效果不如旧 prompt | 低 | taskStatePrompt 是更结构化的输出格式，理论上效果更好；通过质量评分验证 |
| 活跃文件提取不准确 | 低 | `ExtractActiveFiles` 只从 tool call Arguments JSON 中提取路径（`path` 字段），来源可靠 |
| Phase 2 行为变化对已有用户的影响 | 极低 | 压缩是内部操作，用户不直接感知压缩 prompt 格式变化 |
| 删除代码过多导致回归 | 中 | 被删代码有完善的测试覆盖（26 个测试），删除后已有测试保证其他功能正常 |

---

## 附录 A：代码量估算

| 类别 | 文件 | 删除行数 | 新增行数 | 修改行数 |
|------|------|---------|---------|---------|
| eviction 核心代码 | `compress.go` | ~250 | 0 | 0 |
| 压缩 prompt | `compress.go` | ~30 | ~35 | 0 |
| thinTail 签名 | `compress.go` | 0 | ~20 | ~10 |
| compressMessagesWithFingerprint | `compress.go` | 0 | ~15 | ~10 |
| ActiveFile 相关 | `quality.go` | 0 | ~60 | ~5 |
| phase2Manager 重写 | `context_manager_phase2.go` | ~130 | ~50 | 0 |
| context_manager 注释 | `context_manager.go` | 2 | 2 | 0 |
| 测试删除 | `evict_test.go` + `compress_test.go` | ~500 | 0 | 0 |
| 测试新增 | `quality_test.go` + `compress_test.go` + `context_manager_test.go` | 0 | ~250 | ~30 |
| **合计** | | **~912** | **~432** | **~55** |

**净减少约 425 行代码**，同时增加了活跃文件保护和更结构化的压缩输出。

---

## 附录 B：关键代码引用

| 概念 | 文件 | 行号 |
|------|------|------|
| `ContextManager` 接口 | `context_manager.go` | 第40行 |
| `SmartCompressor` 接口 | `context_manager.go` | 第185行 |
| `ContextModePhase2` 常量及注释 | `context_manager.go` | 第17行 |
| `NewContextManager()` Phase 2 分支 | `context_manager.go` | 第216行 |
| `phase2Manager` 结构体 | `context_manager_phase2.go` | 第12行 |
| `phase2Manager.Compress()` | `context_manager_phase2.go` | 第65行 |
| `phase2Manager.ManualCompress()` | `context_manager_phase2.go` | 第167行 |
| `phase2Manager.ShouldCompressDynamic()` | `context_manager_phase2.go` | 第35行 |
| `DensityScore` 结构体 | `compress.go` | 第214行 |
| `defaultDensityScorer()` | `compress.go` | 第221行 |
| `containsErrorPattern()` | `compress.go` | 第262行 |
| `containsDecisionPattern()` | `compress.go` | 第274行 |
| `isLargeCodeDump()` | `compress.go` | 第286行 |
| `isRepetitiveGrepResult()` | `compress.go` | 第300行 |
| `evictToolGroup` 结构体 | `compress.go` | 第331行 |
| `evictByDensity()` | `compress.go` | 第338行 |
| `buildCompressResultFromEvicted()` | `compress.go` | 第421行 |
| `structuredCompressionPrompt` | `compress.go` | 第22行 |
| `thinTail()` | `compress.go` | 第512行 |
| `aggressiveThinTail()` | `compress.go` | 第573行 |
| `compressMessagesWithFingerprint()` | `compress.go` | 第666行 |
| `countEvictedMessages()` | `compress.go` | 第639行 |
| `KeyInfoFingerprint` 结构体 | `quality.go` | 第12行 |
| `ExtractFingerprint()` | `quality.go` | 第24行 |
| `ValidateCompression()` | `quality.go` | 第57行 |
| `EvaluateQuality()` | `quality.go` | 第117行 |
| `ValidateMarkers()` | `quality.go` | 第417行 |
| `CompressCooldown` | `trigger.go` | 第123行 |
| `calculateDynamicThreshold()` | `trigger.go` | 第183行 |
| `DetectToolPattern()` | `trigger.go` | 第234行 |
| `BuildTriggerInfo()` | `trigger.go` | 第284行 |
