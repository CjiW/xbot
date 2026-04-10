---
title: "context-management-design"
weight: 70
---


> ⚠️ **演进说明（2026-03-27）**：本文档描述的 Phase 1 设计核心原则「执行视图隔离 — Tool call/result 仅在 Run() 内存中存在」已在后续重构（commit `45d6078`）中被调整。当前实现改为直接持久化 engine 产生的 assistant + tool 消息到 session，以确保下一轮对话拥有完整上下文。本文档作为历史设计参考保留。

> 中书省拟 | 2026-03-19
> 状态：待陛下审核
> 前置文档：《调研报告》context-management-research.md

---

## 一、设计目标

### 1.1 核心原则

> **Tool call 消息只在处理一个用户 message 期间存在。用户新消息来的时候，上下文只有 user prompt 和 assistant 回复。**

| 原则 | 说明 |
|------|------|
| **对话视图纯净** | Session 持久层只存 user + assistant 消息 |
| **执行视图隔离** | Tool call/result 仅在 Run() 内存中存在 |
| **压缩不泄漏** | 自动压缩持久化时剥离 tool 消息 |
| **信息不丢失** | Tool 执行的关键结果融入 assistant 回复或压缩摘要 |

### 1.2 与现有系统协同

- ✅ 与现有 Letta 三层记忆系统（Core/Archival/Recall）无冲突
- ✅ 与记忆整理（consolidation）机制兼容
- ✅ 与中间件管道（pipeline）架构兼容

---

## 二、现状分析

### 2.1 当前消息流（问题路径）

```
用户消息 → buildPrompt()
           ├── session.GetHistory()  ← 可能含 tool 消息（已泄漏）
           └── pipeline.Run()        ← 注入 system/memory/skills
         → Run()
           ├── LLM 调用 → tool call → tool result → LLM 调用 → ...
           ├── maybeCompress()
           │     ├── compressContext()  → 压缩结果含 tool 消息
           │     └── session.Clear() + session.AddMessage()  ← tool 消息泄漏到 session ❌
           └── 返回 finalContent
         → session.AddMessage(userMsg)
         → session.AddMessage(assistantMsg)  ← 正常路径无问题 ✅
```

### 2.2 问题代码定位

**`compress.go:compressContext()`** — 核心问题函数：

```go
// 当前注释（有问题）：
// 核心原则：
// 1. 保留所有 tool 消息（tool_calls 和 tool result 必须配对，否则 API 报错）  ← 仅为 API 兼容
// 2. 把压缩后的摘要作为 user prompt 直接调用 LLM
// 3. 保留 system 消息和最近的对话轮次
```

返回的 compressed 消息列表中包含 `thinnedTail`（最近 3 组 tool call/result），这些消息被持久化到 session 后，下次 `GetHistory()` 就会加载出来。

**`engine.go:maybeCompress()`** — 持久化逻辑：

```go
// 持久化压缩结果到 session
if cfg.Session != nil {
    cfg.Session.Clear()
    for _, msg := range compressed {  // ← compressed 含 tool 消息
        if msg.Role == "system" { continue }
        cfg.Session.AddMessage(msg)  // ← tool 消息写入 session ❌
    }
}
```

---

## 三、设计方案：双视图架构

### 3.1 架构概览

```
┌─────────────────────────────────────────────────────────┐
│                    LLM View (内存)                       │
│  ┌───────────┐  ┌──────────────────┐  ┌──────────────┐  │
│  │ System    │  │ Session History  │  │ Current Turn │  │
│  │ Prompt    │  │ (user+assistant) │  │ Tool Calls   │  │
│  │ + Memory  │  │ + Compressed     │  │ + Results    │  │
│  │ + Skills  │  │   Summary        │  │              │  │
│  └───────────┘  └──────────────────┘  └──────────────┘  │
│       ↓               ↓                    ↓           │
│  ─ ─ ─ ─ ─ ─ ─ → 发送给 LLM ← ─ ─ ─ ─ ─ ─ ─ ─ ─ ─   │
└─────────────────────────────────────────────────────────┘
                          ↕ Run() 期间同步
┌─────────────────────────────────────────────────────────┐
│               Session View (持久化)                      │
│  ┌──────────────────────────────────────────────────┐   │
│  │ user_msg_1 → assistant_msg_1                     │   │
│  │ user_msg_2 → assistant_msg_2                     │   │
│  │ [compressed_summary] (user role)                 │   │
│  │ user_msg_N → assistant_msg_N                     │   │
│  │ ⚠️ 不含任何 tool/assistant(tool_calls) 消息       │   │
│  └──────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────┘
```

### 3.2 关键设计：compressContext 改造

**改造前**：
```
compressContext(messages) → compressed messages（含 tool）
  → 持久化到 session（tool 泄漏）
  → 继续用于 LLM 调用
```

**改造后**：
```
compressContext(messages) → compressed messages（含 tool，用于 LLM 继续调用）
                           + sessionMessages（纯 user/assistant，用于持久化）
```

具体实现：`compressContext` 返回两个切片：

```go
// CompressResult 压缩结果，区分 LLM 视图和 Session 视图
type CompressResult struct {
    // LLMView 用于继续当前 Run() 循环的 LLM 调用
    // 可能包含 tool 消息（API 兼容性需要）
    LLMView []llm.ChatMessage
    
    // SessionView 用于持久化到 session
    // 只包含 user + assistant（无 tool）消息
    // tool 执行的关键信息已融入压缩摘要
    SessionView []llm.ChatMessage
}
```

### 3.3 compressContext 详细设计

```go
func (a *Agent) compressContext(ctx context.Context, messages []llm.ChatMessage, 
    client llm.LLM, model string) (*CompressResult, error) {
    
    // 第一步：找到尾部安全切割点（不变）
    tailStart := findSafeTailStart(messages)
    
    // 第二步：分离 thinnedTail（用于 LLM View）
    thinnedTail := thinTail(messages[tailStart:], 3)
    
    // 第三步：从 thinnedTail 提取对话摘要（用于 Session View）
    tailSummary := extractDialogueFromTail(thinnedTail)
    
    // 第四步：压缩旧历史（不变）
    compressed := compressOldHistory(messages[:tailStart], client, model)
    
    // 构建两个视图
    llmView := []llm.ChatMessage{systemMsg}
    llmView = append(llmView, userSummaryMsg(compressed))
    llmView = append(llmView, thinnedTail...)  // 含 tool 消息
    
    sessionView := []llm.ChatMessage{}
    sessionView = append(sessionView, userSummaryMsg(compressed))  // 压缩摘要
    sessionView = append(sessionView, tailSummary...)  // 尾部对话摘要
    
    return &CompressResult{
        LLMView:    llmView,
        SessionView: sessionView,
    }, nil
}
```

### 3.4 extractDialogueFromTail 设计

从 thinnedTail（含 tool 消息）中提取纯对话视图：

```go
// extractDialogueFromTail 从含 tool 消息的尾部提取 user/assistant 对话
// 每个 tool group 的效果被摘要为 assistant 消息的一部分
func extractDialogueFromTail(tail []llm.ChatMessage) []llm.ChatMessage {
    var result []llm.ChatMessage
    var pendingToolSummary strings.Builder  // 累积当前轮的 tool 执行摘要
    
    for _, msg := range tail {
        switch {
        case msg.Role == "user":
            flushPending(&result, &pendingToolSummary)
            result = append(result, llm.NewUserMessage(msg.Content))
            
        case msg.Role == "assistant" && len(msg.ToolCalls) > 0:
            // assistant 发起了 tool call，记录工具名称
            flushPending(&result, &pendingToolSummary)
            if msg.Content != "" {
                // assistant 有文本内容，先记录
                pendingToolSummary.WriteString(msg.Content + "\n")
            }
            for _, tc := range msg.ToolCalls {
                pendingToolSummary.WriteString(fmt.Sprintf("🔧 %s(%s)\n", tc.Name, truncateArgs(tc.Arguments, 100)))
            }
            
        case msg.Role == "assistant":
            flushPending(&result, &pendingToolSummary)
            result = append(result, llm.NewAssistantMessage(msg.Content))
            
        case msg.Role == "tool":
            // tool result，累积摘要（截断长内容）
            toolContent := truncateRunes(msg.Content, 200)
            pendingToolSummary.WriteString(fmt.Sprintf("  → %s\n", toolContent))
        }
    }
    flushPending(&result, &pendingToolSummary)
    return result
}

// flushPending 将累积的 tool 执行摘要作为 assistant 消息添加到结果
func flushPending(result *[]llm.ChatMessage, builder *strings.Builder) {
    if builder.Len() == 0 {
        return
    }
    *result = append(*result, llm.NewAssistantMessage(builder.String()))
    builder.Reset()
}
```

**输出示例**：

```
输入（thinnedTail）：
  [user] "帮我查看 main.go"
  [assistant, tool_calls: [Read("main.go")]]
  [tool] "package main\nimport (..."
  [assistant] "这是 main.go 的内容，主要功能是..."

输出（SessionView）：
  [user] "帮我查看 main.go"
  [assistant] "🔧 Read(main.go)\n  → package main\nimport (...\n这是 main.go 的内容，主要功能是..."
```

### 3.5 maybeCompress 持久化改造

```go
maybeCompress := func() {
    // ... 触发判断逻辑不变 ...
    
    result, compressErr := cc.CompressFunc(ctx, messages, cfg.LLMClient, cfg.Model)
    // ...
    
    // LLM View：继续当前 Run
    messages = result.LLMView
    
    // Session View：持久化到 session（不含 tool 消息）
    if cfg.Session != nil {
        cfg.Session.Clear()
        for _, msg := range result.SessionView {
            cfg.Session.AddMessage(msg)  // 纯 user/assistant
        }
    }
}
```

### 3.6 处理 Input-too-long 强制压缩

同 maybeCompress 逻辑，使用 `result.SessionView` 持久化，`result.LLMView` 继续调用。

### 3.7 handleCompress (/compress 命令) 改造

同 maybeCompress 逻辑。用户手动 /compress 时，Session View 写入 session，LLM View 仅在内存中。

---

## 四、压缩策略优化

### 4.1 压缩阈值调整

```go
// 当前
CompressionThreshold: 0.8  // 80% 触发

// 建议（参考 Claude Code 64-75% 策略）
CompressionThreshold: 0.7  // 70% 触发，留 30% completion buffer
```

### 4.2 压缩 Prompt 优化

当前的压缩 prompt 较为通用，建议增加：

```go
compressionPrompt := `You are a context compression expert. ...

## Compression Rules
1. Retain ALL key facts, decisions, and important details
2. Keep track of what the user has asked for and what has been done
3. Preserve any file paths, code snippets, or technical details
4. Maintain the logical flow and context of the conversation
5. Note any errors or issues that were encountered
6. **For tool execution sequences**: summarize what was done and the result,
   but don't include raw tool call/request format
7. Preserve any open questions or unfinished tasks
8. Include the current working directory and any important state
`
```

### 4.3 Tail 保留策略

```go
// 当前
thinTail(tail, 3)  // 保留最近 3 组 tool group

// 建议不变，但增加日志
// 最近 3 组 tool call 对应的对话轮次，确保当前任务有足够上下文继续
```

---

## 五、清理已有脏数据

### 5.1 Session 数据迁移

需要清理已有 session 中泄漏的 tool 消息：

```go
// cleanupToolMessages 清理 session 中的 tool 消息
func cleanupToolMessages(tenantSession *session.TenantSession) error {
    messages, err := tenantSession.GetAllMessages()
    if err != nil {
        return err
    }
    
    var clean []llm.ChatMessage
    for _, msg := range messages {
        // 跳过 tool 消息和含 tool_calls 的 assistant 消息
        if msg.Role == "tool" {
            continue
        }
        if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
            // 如果 assistant 消息只有 tool_calls 无 content，跳过
            if strings.TrimSpace(msg.Content) == "" {
                continue
            }
            // 如果有 content，保留但移除 ToolCalls
            msg.ToolCalls = nil
        }
        clean = append(clean, msg)
    }
    
    tenantSession.Clear()
    for _, msg := range clean {
        tenantSession.AddMessage(msg)
    }
    return nil
}
```

建议在 Agent 启动时或在 /new 命令中自动执行清理。

---

## 六、实施计划

### Phase 1: 核心修复（优先级最高）

| 任务 | 文件 | 预估 |
|------|------|------|
| 新增 `CompressResult` 类型 | `agent/compress.go` | 小 |
| 改造 `compressContext` 返回双视图 | `agent/compress.go` | 中 |
| 新增 `extractDialogueFromTail` | `agent/compress.go` | 中 |
| 改造 `maybeCompress` 持久化逻辑 | `agent/engine.go` | 小 |
| 改造 input-too-long 压缩逻辑 | `agent/engine.go` | 小 |
| 改造 `handleCompress` | `agent/compress.go` | 小 |
| 更新 `CompressFunc` 类型签名 | `agent/engine.go` | 小 |

### Phase 2: 策略优化

| 任务 | 说明 |
|------|------|
| 压缩阈值调整为 0.7 | `New()` 中的默认值 |
| 优化压缩 prompt | `compressContext` 中的 prompt 模板 |
| 启动时清理脏数据 | `session` 包增加迁移函数 |

### Phase 3: 测试

| 测试 | 说明 |
|------|------|
| 单元测试：extractDialogueFromTail | 验证各种 tail 组合的输出 |
| 单元测试：compressContext 双视图 | 验证 LLMView 含 tool、SessionView 不含 |
| 集成测试：正常流程无 tool 泄漏 | 模拟多轮对话，检查 session |
| 集成测试：自动压缩无 tool 泄漏 | 触发压缩后检查 session |
| 回归测试：/compress 命令 | 手动压缩后检查 session |

---

## 七、风险评估

| 风险 | 影响 | 缓解措施 |
|------|------|----------|
| extractDialogueFromTail 信息丢失 | assistant 回复可能丢失 tool 执行细节 | 关键 tool 结果保留 200 字符摘要 |
| compressContext 双视图不一致 | LLM View 和 Session View 信息不同步 | 共享压缩摘要文本，确保信息一致性 |
| 压缩时机过早 | 任务未完成就触发压缩 | 保留 completion buffer（70% 阈值） |
| 脏数据清理影响现有会话 | 清理后已有对话上下文丢失 | 只清理 tool 消息，保留所有 user/assistant |

---

## 八、预期效果

### 8.1 上下文纯净度

```
压缩前 session 状态（❌ 当前）：
  [user] "帮我修改代码"
  [assistant, tool_calls: [Read(...), Edit(...)]]  ← 不应出现
  [tool] "package main..."                          ← 不应出现
  [assistant] "已修改完成"
  [user] "再帮我检查一下"
  [assistant, tool_calls: [Grep(...)]]             ← 不应出现
  ...

压缩后 session 状态（✅ 目标）：
  [user] "帮我修改代码"
  [assistant] "🔧 Read(main.go)\n  → package main...\n🔧 Edit(main.go)\n  → 已修改\n已修改完成"
  [user] "再帮我检查一下"
  [assistant] "🔧 Grep(pattern=\"error\")\n  → 找到 3 处匹配\n检查结果如下..."
```

### 8.2 上下文效率提升

- Session 存储大小减少 ~40-60%（移除 tool result 的大段内容）
- GetHistory 加载更快（更少消息条数）
- 上下文窗口利用率提升（更多空间给有用内容）

### 8.3 用户体验改善

- ✅ 新消息的上下文干净，只有对话内容
- ✅ LLM 不会因历史 tool call 而产生困惑
- ✅ 压缩后对话连贯性保持
