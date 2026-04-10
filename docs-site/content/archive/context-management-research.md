---
title: "context-management-research"
weight: 90
---


> 中书省编撰 | 2026-03-19
> 圣旨：调查先进agent上下文管理方案，改进xbot上下文管理功能

---

## 一、核心问题定义

**用户原话**："用户下一条消息发过来时，上下文里居然还有tool call。tool call应该只在处理一个用户message期间才会出现。用户新消息来的时候，上下文应该只有用户prompt和agent的每个回答。"

**设计目标**：Session 持久层只存储「对话视图」（user + assistant），tool call/result 消息仅在 Run() 循环内存中存在，不跨用户消息持久化。

---

## 二、业界先进方案调研

### 2.1 Claude Code 的上下文管理

| 维度 | 实现方式 |
|------|----------|
| **Auto-compact 触发** | 上下文使用达 ~64-75% 时触发（早期版本 90%+，后调整为更保守策略，保留 "completion buffer"） |
| **压缩内容** | 整体对话历史压缩为摘要，保留最近若干轮完整对话 |
| **tool_use/tool_result 处理** | 压缩时将 tool call 序列摘要化，不保留原始 tool_use/tool_result 对 |
| **子任务级压缩** | 社区强烈要求（GitHub Issue #16960），尚未正式实现 |
| **关键洞察** | Claude Code 视 tool call 为"瞬时执行细节"——压缩后摘要化而非原样保留 |

**源**：GitHub anthropics/claude-code issues #16960, #10691; Hyperdev 分析文章

### 2.2 JetBrains Research: Observation Masking vs LLM Summarization

JetBrains Research 2025年12月的论文对比了两种主流上下文压缩策略：

| 策略 | 原理 | 优势 | 劣势 |
|------|------|------|------|
| **Observation Masking** | 截断/隐藏 tool result（observation），只保留最近 N 轮的完整结果 | 无 LLM 调用成本，速度快 | 需要调优 masking window 参数 |
| **LLM Summarization** | 用 LLM 将旧对话（含 tool calls）压缩为摘要 | 信息保留更完整 | 额外 LLM 调用成本，增加延迟 |

**关键结论**：
- 两种策略在成本节省和问题解决能力上匹配
- Observation masking 在调优 window 后性能最佳（window=10 轮）
- LLM summarization 推荐：一次摘要 21 轮，保留最近 10 轮完整
- 混合策略（summary + tail）是当前最优实践

### 2.3 Acon: Agent Context Optimization (arXiv 2510.00615)

学术框架 Acon 提出了**双重压缩**：
1. **Observation Compression**：压缩 tool result（环境反馈）
2. **History Compression**：压缩对话历史（含 reasoning + action + observation）

核心创新：**failure-driven, task-aware compression**——根据任务失败模式动态调整压缩策略。可通过蒸馏将压缩器部署为小模型，保持 >95% 性能的同时降低成本。

### 2.4 Google ADK: Context as Compiled View

Google Agent Development Kit 将上下文视为**结构化系统的编译视图**：
- 分层架构：system prompt → session state → tool definitions → conversation
- 上下文不是"消息列表"而是"状态快照+必要历史"
- 动态裁剪：根据当前任务需求选择性注入上下文片段

### 2.5 Kubiya: Context Engineering 四大策略

| 策略 | 说明 |
|------|------|
| **Write** | 持久化信息到外部存储 |
| **Select** | 从历史中选择相关片段 |
| **Compress** | 压缩旧上下文为摘要 |
| **Isolate** | 隔离不同任务的上下文，避免交叉污染 |

### 2.6 三层压缩架构（社区最佳实践）

```
Layer 1: Offload — 大 tool result 自动落盘到 offload_store/
Layer 2: Evict   — 上下文达 85% 时驱逐旧 tool call payload
Layer 3: Compact — 用户触发或自动压缩剩余上下文
```

关键设计：**disk as long-term memory**——压缩是"有损的"，但从信息论角度看是"无损的"（所有数据都可在 offload store 中找回）。

---

## 三、关键技术结论

### 3.1 Tool Call 消息的生命周期——业界共识

| 阶段 | Tool Call 可见性 | 原因 |
|------|------------------|------|
| **当前轮 Run()** | ✅ 完整可见 | LLM 需要看到 tool call + result 以决定下一步 |
| **跨轮持久化** | ❌ 不应保留原始形式 | tool call 是执行细节，不是对话内容 |
| **压缩摘要** | ✅ 可摘要化 | 将 tool call 序列压缩为 "执行了 X 工具，结果是 Y" |
| **Session 存储** | ❌ 不应出现 | Session 应存储「对话视图」而非「执行轨迹」 |

### 3.2 最佳实践总结

1. **分离关注点**：Session 存对话（user/assistant），内存存执行（tool call/result）
2. **渐进式压缩**：先截断 → 再摘要 → 最后 offload
3. **Tail 保留**：始终保留最近 N 轮完整对话（含 tool calls），确保当前任务连贯
4. **压缩时机**：64-75% 上下文使用率时触发（而非 90%+），留 completion buffer
5. **摘要质量**：压缩 prompt 应要求保留文件名、函数名、变量名等具体细节

---

## 四、xbot 现有实现分析（代码审计摘要）

### 4.1 当前架构

```
processMessage()
  ├── buildPrompt()        ← 从 session.GetHistory() 加载历史
  │     └── pipeline.Run() ← 注入 system prompt / memory / skills
  ├── Run()                ← 主循环，tool call/result 在内存中累积
  │     └── maybeCompress() ← 自动压缩（含 session 持久化）
  └── tenantSession.AddMessage(userMsg + assistantMsg) ← 仅保存对话
```

### 4.2 问题根因

**正常流程**（无压缩时）：
- ✅ processMessage 只保存 userMsg + assistantMsg → 无 tool call 泄漏

**自动压缩触发时**：
- ❌ `maybeCompress()` 调用 `compressContext()`，后者：
  1. 保留 thinnedTail（含 tool 消息，保留最近 3 组）
  2. 压缩旧历史为摘要
  3. 结果持久化到 session → **tool call 消息泄漏到 session**
- ❌ 代码注释明确写了 "保留所有 tool 消息（tool_calls 和 tool result 必须配对，否则 API 报错）"

**根本矛盾**：compressContext 为了 API 兼容性保留 tool 消息，但持久化时没有区分「LLM 需要的临时上下文」和「session 应存储的长期对话」。

### 4.3 问题影响范围

| 文件 | 问题点 |
|------|--------|
| `agent/compress.go:compressContext()` | 压缩结果包含 tool 消息 |
| `agent/engine.go:maybeCompress()` | 将含 tool 的压缩结果持久化到 session |
| `agent/engine.go:Run()` | Input-too-long 强制压缩路径同样问题 |
| `agent/compress.go:handleCompress()` | /compress 命令同样问题 |

---

## 五、改进方向建议

基于调研结果，建议 xbot 采用**双视图架构**：

1. **Session View（对话视图）**：只存 user + assistant 消息，跨用户消息持久化
2. **LLM View（执行视图）**：Session View + 当前轮的 tool call/result，仅在 Run() 内存中存在

压缩时：
- 压缩结果写入 **LLM View**（可含 tool 消息，用于继续当前 Run）
- 持久化到 **Session View** 时，剥离 tool 消息（只保留压缩摘要 + 最近对话轮的 user/assistant）

详细设计方案见《设计文档》。

---

## 六、参考来源

1. Anthropic Engineering Blog: "Effective Context Engineering for AI Agents"
2. JetBrains Research: "Cutting Through the Noise: Smarter Context Management for LLM-Powered Agents" (2025-12)
3. Acon: "Agent Context Optimization" (arXiv 2510.00615v2)
4. Kubiya: "Context Engineering Best Practices for Reliable AI in 2025"
5. Maxim AI: "Context Window Management: Strategies for Long-Context AI Agents"
6. Claude Code GitHub Issues: #16960, #10691
7. Hyperdev: "How Claude Code Got Better by Protecting More Context"
8. Medium/AI Forum: "Automatic Context Compression in LLM Agents"
