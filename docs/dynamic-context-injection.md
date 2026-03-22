# Prompt 动态注入问题分析与优化方案

> 状态：✅ 门下省审核通过（v2）  
> 分支：feat/prompt-and-mask-refactor  
> 日期：2026-03-22

## 一、调研结果

### 1.1 Prompt 构建全流程

当前 prompt 构建分为两个阶段：**构建阶段**（buildPrompt，用户消息到来时执行一次）和 **运行阶段**（Run 循环，tool call 后只注入 sys_reminder）。

```
用户消息到来
  ↓
processMessage() [agent/agent.go:1171]
  ↓
buildPrompt() [agent/agent.go:1392]
  ├─ 获取 session 历史消息
  ├─ 配置 MCP tools
  ├─ 计算 promptWorkDir
  ├─ 创建 MessageContext（含 CWD、skills catalog、agents catalog、memory）
  └─ pipeline.Run(mc) — 执行中间件链
       ├─ Priority 0:   SystemPromptMiddleware → 渲染 prompt.md 模板（注入 Channel, WorkDir, CWD）
       ├─ Priority 1:   ProjectHintMiddleware → 从 archival memory 注入 [PROJECT_CARD]
       ├─ Priority 5:   ChannelPromptMiddleware → 注入渠道特化 prompt（如飞书）
       ├─ Priority 100: SkillsCatalogMiddleware → 注入 <available_skills>
       ├─ Priority 110: AgentsCatalogMiddleware → 注入 <available_agents>
       ├─ Priority 120: MemoryMiddleware → 注入长期记忆（core + working_context）
       ├─ Priority 130: SenderInfoMiddleware → 注入发送者名称
       └─ Priority 200: UserMessageMiddleware → 构建最终 user message（时间戳 + 系统引导）
  ↓
Assemble() → [system message] + [history] + [user message]
  ↓
Run() [agent/engine.go:189, 包级函数 func Run(ctx, cfg)]
  ├─ 发给 LLM
  ├─ LLM 返回 tool calls → 执行工具
  ├─ sys_reminder 注入（仅追加到 tool message 末尾）
  └─ 循环直到 LLM 返回文本（无 tool calls）
```

**关键发现：system message 在 `buildPrompt` 时构建一次，整个 Run() 循环中不再重建。**

### 1.2 System Prompt 的完整组成

| Key | 来源 | 静态/动态 | 注入时机 |
|-----|------|-----------|----------|
| `00_base` | `prompt.md` 模板渲染 | **半动态** | buildPrompt（Channel/WorkDir 不变，CWD 可能变） |
| `05_project_hint` | archival memory 搜索 | 动态（60s 缓存） | buildPrompt |
| `05_channel_*` | ChannelPromptProvider | 静态/渠道配置 | buildPrompt |
| `10_skills` | SkillStore.GetSkillsCatalog | 动态（扫描文件系统） | buildPrompt |
| `15_agents` | AgentStore.GetAgentsCatalog | 动态（扫描文件系统） | buildPrompt |
| `20_memory` | Memory.Recall() | 动态（语义搜索） | buildPrompt |
| `30_sender` | senderName | 静态（同一对话） | buildPrompt |

### 1.3 `sys_reminder` 机制分析

**文件**: `agent/reminder.go`  
**注入位置**: `engine.go:930-933`，追加到最后一个 tool message 的 Content 末尾  
**触发条件**: LLM 返回 tool calls 时（`engine.go:924` 条件分支内）  
**注入内容**: 用户任务目标、工具调用总数、本轮工具名称、TODO 摘要、行为提醒  
**格式**: `<system-reminder>\n...\n</system-reminder>`  
**关键特性**: sys_reminder 是目前唯一在 Run() 循环中每轮动态注入的机制，但它只追加到 tool message content 中，不影响 system message。

### 1.4 补充说明：Shell 等工具的执行目录是正确的

**重要**：虽然 system prompt 中的 CWD 文本不更新，但 **Shell、Glob、Grep、Read 等工具的实际执行目录是正确的**。

原因：每次工具调用时，`buildToolContext()` 都会从 `cfg.Session.GetCurrentDir()` 重新读取最新的 CWD（`engine.go:1162`），并设置到 `tc.CurrentDir` 中。Cd 工具调用 `session.SetCurrentDir()` 后，下一次工具调用的 `buildToolContext()` 自然会读到新值。

因此，问题 **仅存在于 system prompt 中的文本展示**——LLM 看到的目录文字是旧的，但实际工具执行在新目录。这可能导致 LLM 在思考/推理中引用错误路径。

---

## 二、动态信息注入问题清单

| # | 信息项 | 注入位置 | 注入时机 | 是否有问题 | 问题详情 | 修复优先级 |
|---|--------|----------|----------|------------|----------|-----------|
| 1 | **当前目录 (CWD)** | `00_base` system prompt（prompt.md 模板 `{{.CWD}}`） | buildPrompt（仅首轮） | ⚠️ **是** | Cd 工具在 Run() 循环中执行后通过 `SetCurrentDir` 更新 session，但 system message 中的 `{{.CWD}}` 不会刷新。**注意：工具执行目录本身是正确的**，问题仅在 system prompt 文本展示 | P0 |
| 2 | **available_skills** | `10_skills` system prompt | buildPrompt（仅首轮） | ⚠️ 低风险 | 运行期间 skills 通常不变 | P2 |
| 3 | **available_agents** | `15_agents` system prompt | buildPrompt（仅首轮） | ⚠️ 低风险 | 运行期间 agents 通常不变 | P2 |
| 4 | **长期记忆 (Memory)** | `20_memory` system prompt | buildPrompt（仅首轮） | ⚠️ 是 | LLM 在 Run() 中通过 `core_memory_replace`/`archival_memory_insert` 修改记忆后，system prompt 中的记忆快照不更新 | P1 |
| 5 | **时间戳** | user message 尾部（`现在时间`） | buildPrompt（仅首轮） | ⚠️ 低风险 | 长时间运行的任务可能显示过时时间 | P2 |
| 6 | **项目知识卡片** | `05_project_hint` system prompt | buildPrompt（60s 缓存） | ⚠️ 低风险 | 有 60s 缓存，跨 buildPrompt 调用可能过期 | P2 |

---

## 三、竞品分析（Claude Code）

| 维度 | Claude Code 做法 | xbot 当前做法 |
|------|------------------|---------------|
| **工作目录** | CLI 工具，CWD = 终端 CWD，不存在切换问题 | 通过 Cd 工具切换，CWD 可变，但 prompt 不更新 |
| **动态上下文注入** | system prompt 主要是静态 CLAUDE.md；动态信息通过 tool result 传递 | 试图在 system prompt 中放入动态信息，但只在首轮构建 |
| **sys_reminder** | 类似机制（Anthropic SDK 内置的 tool_result system prompt） | 自定义 sys_reminder，已在每轮 tool call 后注入 |

**关键差异**: xbot 的 Cd 工具使 CWD 在运行时变化，这是 xbot 特有的问题。

---

## 四、优化方案

### 4.1 方案选型

| 方案 | 优点 | 缺点 | 推荐 |
|------|------|------|------|
| **A. 扩展 sys_reminder** | 复用现有机制，改动最小 | sys_reminder 追加到 tool message，语义上不是 system prompt | ❌ |
| **B. Run() 循环中重建 system message** | 最彻底，所有动态信息都更新 | 重建成本高（重新执行 pipeline），可能影响 LLM cache | ❌ |
| **C. 差量动态注入（推荐）** | 新增轻量机制，只注入变化的部分，不影响 LLM cache | 需要新增代码 | ✅ |

### 4.2 推荐方案：差量动态注入（Dynamic Context Injection）

**核心思想**: 不重建整个 system message，而是在 Run() 循环的每个 iteration 中，检测哪些动态信息发生了变化，将变化的部分以 `<dynamic-context>` 标签注入到最新的 tool message 中（与 sys_reminder 类似的注入方式，但注入顺序在 sys_reminder 之前）。

#### 4.2.1 架构设计

```
Run() 循环（每个 iteration）
  ├─ 执行工具
  ├─ 构建 DynamicSnapshot（当前 CWD）
  ├─ 对比上一个 Snapshot，检测变化
  ├─ 如有变化 → 注入 <dynamic-context> 到最新 tool message（在 sys_reminder 之前）
  └─ 注入 sys_reminder（现有机制）
```

#### 4.2.2 具体实现步骤

**Step 1: 定义 DynamicContextInjector**

文件: `agent/dynamic_context.go`（新文件，保持 engine.go 精简）

```go
package agent

import (
	"fmt"
	"strings"

	"xbot/llm"
)

// DynamicSnapshot 动态上下文快照，用于检测运行时变化
type DynamicSnapshot struct {
	CWD string // 当前工作目录
}

// DynamicContextInjector 在 Run() 循环中检测动态信息变化并注入
type DynamicContextInjector struct {
	lastSnapshot *DynamicSnapshot
	getCWD       func() string // 获取当前 CWD 的函数（兼容主 Agent 和 SubAgent）
}

// NewDynamicContextInjector 创建动态上下文注入器
func NewDynamicContextInjector(getCWD func() string) *DynamicContextInjector {
	return &DynamicContextInjector{
		getCWD: getCWD,
	}
}

// InjectIfNeeded 检测动态信息变化，如有变化则注入到最新 tool message
// 注入顺序在 sys_reminder 之前（dynamic-context 描述事实，sys_reminder 描述行为引导）
func (d *DynamicContextInjector) InjectIfNeeded(messages []llm.ChatMessage) {
	current := DynamicSnapshot{CWD: d.getCWD()}
	if d.lastSnapshot == nil {
		d.lastSnapshot = &current
		return // 首轮不注入（system prompt 中的值已是最新的）
	}

	var changes []string
	if current.CWD != d.lastSnapshot.CWD {
		changes = append(changes, fmt.Sprintf("- 当前目录已切换为：%s，切换后所有 Shell 命令在新目录执行", current.CWD))
	}

	if len(changes) == 0 {
		d.lastSnapshot = &current
		return
	}

	// 注入到最新 tool message 末尾
	if len(messages) > 0 {
		lastIdx := len(messages) - 1
		injection := "<dynamic-context>\n环境变化:\n" + strings.Join(changes, "\n") + "\n</dynamic-context>"
		messages[lastIdx].Content += "\n\n" + injection
	}

	d.lastSnapshot = &current
}
```

**Step 2: 在 Run() 函数开头创建实例**

`DynamicContextInjector` 作为 `Run()` 函数的**局部变量**创建，不放入 `RunConfig`：

```go
func Run(ctx context.Context, cfg RunConfig) *RunOutput {
	// ... 现有初始化代码 ...

	// 动态上下文注入器（兼容主 Agent 和 SubAgent）
	dynamicInjector := NewDynamicContextInjector(func() string {
		if cfg.Session != nil {
			if dir := cfg.Session.GetCurrentDir(); dir != "" {
				return dir
			}
		}
		// SubAgent 场景：使用 cfg.InitialCWD（无 session，通过闭包捕获）
		return cfg.InitialCWD
	})

	// ... Run() 循环 ...
}
```

**Step 3: 在 Run() 循环中集成注入点**

在 `engine.go` 的 Run() 函数中，**在 sys_reminder 注入之前**（约第 920 行 sys_reminder 注入点之前）插入：

```go
// --- Dynamic Context 注入（CWD 变化检测）---
dynamicInjector.InjectIfNeeded(messages)

// --- System Reminder 注入 ---
if len(response.ToolCalls) > 0 {
	// ... 现有 sys_reminder 代码 ...
}
```

**注入顺序说明**：`<dynamic-context>` 在 `<system-reminder>` 之前注入，因为它描述的是事实性环境变化，而 sys_reminder 是行为引导。

#### 4.2.3 注入内容优先级

| 信息项 | 是否纳入 DynamicContext | 理由 |
|--------|------------------------|------|
| CWD | ✅ P0 | 核心问题，Cd 后必须更新 |
| 时间戳 | ⚠️ P2（暂不纳入） | 可选扩展，长任务时有用但非关键 |
| Skills | ❌ | 运行期间不变 |
| Agents | ❌ | 运行期间不变 |
| Memory | ⚠️ P1（暂不纳入） | 可选扩展，但记忆变化频繁可能产生噪音，需谨慎评估 |

#### 4.2.4 与 LLM Cache 的兼容性

当前 system message 标记了 `CacheHint = "static"`（`middleware.go:155`），这是正确的——system message 本身不变。动态注入通过追加到 tool message content 中（与 sys_reminder 相同方式），不影响 system prompt 的 cache 命中。

#### 4.2.5 回滚方案

本方案影响面极小，回滚步骤：
1. 删除新文件 `agent/dynamic_context.go`（~50 行）
2. 删除 `Run()` 中 `NewDynamicContextInjector(...)` 创建语句（~10 行）
3. 删除 `Run()` 中 `dynamicInjector.InjectIfNeeded(messages)` 调用（1 行）

**总计约 61 行新增代码（含独立文件），删除即回滚，无数据库/配置变更。**

---

## 五、验证标准

| # | 验证项 | 验证方法 |
|---|--------|----------|
| 1 | Cd 后 CWD 更新 | 发送消息 → Cd 到子目录 → 继续操作 → 检查后续 tool message 中是否包含新目录 |
| 2 | 无变化时不注入 | 正常对话中不应出现空的 `<dynamic-context>` 标签 |
| 3 | system prompt cache 不受影响 | 验证 system message 的 CacheHint 仍为 "static" |
| 4 | 现有 sys_reminder 不受影响 | 正常对话中 sys_reminder 仍正常注入 |
| 5 | SubAgent 兼容 | SubAgent 使用 `cfg.InitialCWD`，不应 panic 或行为异常 |
| 6 | 测试覆盖 | 新增 `DynamicContextInjector` 的单元测试（首轮不注入、CWD 变化注入、CWD 不变不注入、SubAgent 场景） |
| 7 | context_edit 兼容 | context_edit 可删除最后一条消息，验证不会 panic |

## 六、风险与注意

1. **注入频率**: Cd 与其他写工具串行执行（读写分离逻辑），`InjectIfNeeded` 在所有工具执行完毕后统一检测差异，保证每个 iteration 最多注入一次
2. **内容精简**: 动态注入应尽量简洁，避免增加过多 context 消耗
3. **SubAgent 兼容**: SubAgent 无 session，通过 `getCWD` 闭包捕获 `cfg.InitialCWD`，即使被 Cd 闭包修改（`engine.go:1172`），也能正确反映最新值
4. **与 context_edit 的交互**: context_edit 可删除/修改历史消息，但 DynamicContextInjector 只关注 `messages` 切片最新状态，不受影响
5. **注入顺序**: `<dynamic-context>` 必须在 `<system-reminder>` 之前注入，确保事实信息先于行为引导

---

## 附录：门下省审核记录

### v1 审核（驳回）
- 编译错误：`fmt.Sprintf` 缺少 `%s` 占位符 → 已修复
- 初始化位置不明确 → 已明确为 Run() 局部变量
- SubAgent 兼容不够具体 → 已通过 `getCWD func() string` 解决
- 缺少回滚方案 → 已补充
- 未说明 Shell 执行目录实际正确 → 已补充说明

### v2 审核（通过）
- 代码引用全部经核实与源码吻合
- 技术实现正确且精简（~61 行）
- 回滚路径清晰，风险可控
- 3 条优化建议（非阻塞）：
  1. 独立文件 `agent/dynamic_context.go`（已采纳）
  2. 修正 Cd 并行描述（已采纳）
  3. 补充 context_edit 测试场景（已采纳）
