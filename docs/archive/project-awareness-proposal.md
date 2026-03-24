# 项目感知与知识库系统设计方案

> **版本**: v3.0  
> **日期**: 2026-03-20  
> **状态**: 待审核  
> **方案来源**: 中书省（secretariat）

---

## 目录

1. [问题诊断](#1-问题诊断)
2. [调研结果](#2-调研结果)
3. [方案总览](#3-方案总览)
4. [Phase 1: Cd 工具增强](#4-phase-1-cd-工具增强)
5. [Phase 2: 项目知识卡片与 Prompt 引导](#5-phase-2-项目知识卡片与-prompt-引导)
6. [Phase 3: 项目提示自动注入中间件](#6-phase-3-项目提示自动注入中间件)
7. [Phase 4: SubAgent 项目知识共享](#7-phase-4-subagent-项目知识共享)
8. [Phase 5: 知识库维护与治理](#8-phase-5-知识库维护与治理)
9. [边界情况处理](#9-边界情况处理)
10. [业界参考](#10-业界参考)
11. [影响分析](#11-影响分析)
12. [验证标准](#12-验证标准)

---

## 1. 问题诊断

### 1.1 工作目录迷航

**现状**：每次 Agent 启动时，`SandboxWorkDir` 固定为 `/workspace`（`engine_wire.go:52, 160, 312, 405`），Shell 工具每次都在 `/workspace` 执行。LLM 在上下文中看不到「当前在哪个项目」，需要先用 `ls`、`find` 等命令探测目录结构，浪费 2-4 次工具调用。

**根本原因**：system prompt 中缺少项目路径的上下文提示，LLM 没有足够的「暗示」去记忆或快速定位项目目录。

### 1.2 Cd 工具使用率低

**现状**：`CdTool`（`tools/cd.go`）是一个工作正常的工具——切换工作目录后通过 `ToolContext.CurrentDir` 持久化，后续 Shell 工具调用会使用该目录。但 LLM 仍然很少主动使用它。

**原因分析**：
1. **Prompt 缺少引导**：`prompt.md` 和 SubAgent 模板（`agent/subagent_prompt.go`）中没有提到 Cd 工具的使用场景
2. **Cd 工具返回值信息量不足**：当前只返回「已切换到 /path」，没有提供目录上下文信息（这是什么项目？关键文件有哪些？）
3. **沉没成本**：LLM 已经习惯用 Shell + `ls` 探索，没有理由改变行为

### 1.3 缺少项目级长期记忆

**现状**：xbot 有完善的 Letta 三层记忆系统（`memory/letta/letta.go`）：
- **Core Memory**（persona + human）：每次对话注入 system prompt
- **Archival Memory**（`storage/vectordb/archival.go`）：embed 向量存储，语义检索
- **Recall Memory**（`storage/vectordb/recall.go`）：对话历史搜索

但 archival memory 当前主要用于存储对话片段和 LLM 自行决定的信息，没有结构化的项目知识管理模式。每次新对话开始，LLM 对「用户在做什么项目、项目在哪、项目结构如何」一无所知。

---

## 2. 调研结果

### 2.1 关键代码文件

| 文件 | 作用 | 关键发现 |
|------|------|----------|
| `memory/letta/letta.go` | Letta 记忆实现 | `Recall()` 从 archival 按 query 语义检索，`Memorize()` 用 LLM 提取关键信息 |
| `memory/memory.go` | 记忆接口定义 | `MemoryProvider` 接口：Recall/Memorize/MemorizeInput |
| `storage/vectordb/archival.go` | 向量存储实现 | `ArchivalService`: Insert/Search/Delete/Count；chromem-go 的 `Query(ctx, query, limit, where, whereDocument)` 支持 `map[string]string` 过滤 |
| `tools/cd.go` | Cd 工具 | `CdTool` 通过 `ToolContext.CurrentDir` 持久化 CWD |
| `tools/memory_tools.go` | 记忆工具 | CoreMemoryAppend/Replace/Rethink + ArchivalMemoryInsert/Search + RecallMemorySearch |
| `agent/engine_wire.go` | RunConfig 构建 | SubAgent 记忆通过 `buildSubAgentMemory()` 构建独立 TenantID；`OriginUserID` 为 string 类型 |
| `agent/subagent_prompt.go` | SubAgent prompt 模板 | 通用模板 + 角色描述，注入 CWD 和 working dir 信息 |
| `agent/context.go` | System prompt 构建 | `defaultSystemPrompt` 使用 `{{.WorkDir}}` 模板变量 |
| `agent/middleware.go` | 中间件接口 | `Middleware` 接口：`Name() string`, `Priority() int`, `Process(*MessageContext) error` |
| `agent/middleware_builtin.go` | 内置中间件 | `SystemPromptMiddleware` Priority=0；`MemoryInjectMiddleware` Priority=100 |
| `agent/channel_prompt.go` | Channel 特化 prompt | `ChannelPromptMiddleware` Priority=5，通过 `ChannelPromptProvider` 注入渠道特化片段 |
| `.xbot.example/agents/*.md` | SubAgent 定义 | explorer、code-reviewer、tester 三个示例角色 |
| `agent/subagent_tenant.go` | SubAgent TenantID 推导 | `deriveSubAgentTenantID()` 从 parentTenantID + parentAgentID + roleName 生成独立 ID |

### 2.2 中间件管道与优先级

现有中间件按 Priority 排序执行：

| Priority | 中间件 | 文件 | 行为 |
|----------|--------|------|------|
| 0 | `SystemPromptMiddleware` | `middleware_builtin.go` | 注入 prompt.md 渲染结果 |
| 5 | `ChannelPromptMiddleware` | `channel_prompt.go` | 注入渠道特化 prompt 片段 |
| 100 | `MemoryInjectMiddleware` | `middleware_builtin.go` | 注入记忆 Recall 结果到 system prompt |

### 2.3 chromem-go Query API 精确签名

```go
// storage/vectordb/archival.go:410,534
results, err := coll.Query(ctx, query, limit, where, whereDocument)
```

- `where`: `map[string]string`，metadata 过滤（chromem-go 支持 JSON 数组值，如 `{"tags": "[\"go\",\"web\"]"}`）
- `whereDocument`: `map[string]string`，文档内容过滤（支持 `$contains` 操作符）
- 当前代码调用时 `where` 和 `whereDocument` 均传 `nil`，未使用过滤能力

### 2.4 SubAgent 记忆隔离机制

SubAgent 记忆通过 `deriveSubAgentTenantID()` 完全隔离（`agent/subagent_tenant.go`）：
- 输入：`parentTenantID (int64)` + `parentAgentID (string)` + `roleName (string)`
- 输出：确定性的独立 `tenantID`
- 结果：每个 SubAgent 角色有独立的 core/archival/recall 记忆空间

**关键**：`buildToolContextExtras(channel, chatID)` 通过 `multiSession.GetOrCreateSession()` → `LettaMemory.TenantID()` 获取 TenantID（int64），与 `OriginUserID`（string）是不同的维度。跨 Agent 共享需要通过父 Agent 的 channel+chatID 间接获取 TenantID。

### 2.5 业界实践参考

| 系统 | 方法 | 可借鉴点 |
|------|------|----------|
| **Claude Code** | `CLAUDE.md` 文件，分层加载（项目级→用户级→全局） | 项目级上下文的标准化表示 |
| **Cursor** | `.cursorrules` + 项目索引 + 自动上下文注入 | 自动检测项目类型并加载相关规则 |
| **MemGPT/Letta** | 分层记忆（Core/Recall/Archival）+ 记忆合并 | 结构化记忆 schema + 冲突解决 |
| ** Devin** | 自动项目地图 + 文件变更追踪 | 维护项目拓扑作为长期上下文 |
| **Windsurf** | 项目级 Cascade + 上下文窗口管理 | 多文件上下文按需加载 |

---

## 3. 方案总览

```
Phase 1: Cd 工具增强（低风险，高收益）
    ↓
Phase 2: 项目知识卡片 + Prompt 引导（核心能力）
    ↓
Phase 3: 项目提示自动注入中间件（自动化）
    ↓
Phase 4: SubAgent 项目知识共享（一致性）
    ↓
Phase 5: 知识库维护与治理（长期稳定性）
```

**设计原则**：
- **Prompt-first**：优先通过 prompt 引导改变 LLM 行为，而非硬编码逻辑
- **渐进式**：每个 Phase 独立可部署、可回滚
- **最小改动**：不改变现有记忆系统的核心架构，只在 archival memory 上层建立知识管理模式
- **LLM 自治**：让 LLM 自主决定何时创建/更新/查询知识卡片，不强制自动化

---

## 4. Phase 1: Cd 工具增强

### 4.1 修改 `CdTool.Execute()` 返回值

**文件**: `tools/cd.go`
**改动**: 增强 Cd 工具的返回摘要，从简单的路径确认变为提供项目上下文。

**当前行为**（`tools/cd.go:35-47`）:
```go
return &tools.ToolResult{
    Summary: fmt.Sprintf("已切换到 %s", absPath),
}, nil
```

**目标行为**:
```go
// Cd 后自动检测项目特征，返回丰富的上下文
summary := fmt.Sprintf("已切换到 %s", absPath)
// 检测项目标记文件
projectInfo := detectProjectHints(absPath)
if projectInfo != "" {
    summary += "\n\n" + projectInfo
}
return &tools.ToolResult{Summary: summary}, nil
```

**`detectProjectHints` 逻辑**（新增私有函数，`tools/cd.go`）：

```go
// projectMarkers 定义项目类型标记文件及其含义
var projectMarkers = []struct {
    file     string
    hint     string
}{
    {"go.mod", "Go 项目"},
    {"package.json", "Node.js 项目"},
    {"Cargo.toml", "Rust 项目"},
    {"pyproject.toml", "Python 项目"},
    {"pom.xml", "Java/Maven 项目"},
    {"build.gradle", "Java/Gradle 项目"},
    {"Makefile", "C/C++ 项目（可能有）"},
    {"docker-compose.yml", "Docker 编排项目"},
    {".git", "Git 仓库"},
}

func detectProjectHints(dir string) string {
    // 1. 检测项目类型
    var hints []string
    for _, m := range projectMarkers {
        if _, err := os.Stat(filepath.Join(dir, m.file)); err == nil {
            hints = append(hints, m.hint)
        }
    }
    
    // 2. 读取项目名称（从 go.mod/package.json 等提取）
    name := detectProjectName(dir)
    
    // 3. 列出顶层目录结构（仅一级，最多 10 项）
    entries := listTopLevel(dir, 10)
    
    // 4. 组装
    var sb strings.Builder
    if name != "" {
        sb.WriteString(fmt.Sprintf("项目: %s", name))
    }
    if len(hints) > 0 {
        if sb.Len() > 0 { sb.WriteString(" | ") }
        sb.WriteString(strings.Join(hints, ", "))
    }
    if len(entries) > 0 {
        sb.WriteString(fmt.Sprintf("\n目录结构:\n%s", entries))
    }
    // 5. 建议 LLM 记住这个项目
    sb.WriteString(fmt.Sprintf("\n💡 提示：如果这是你经常工作的项目，可以用 archival_memory_insert 将项目信息存入知识库（包含路径 %s 和项目特征），下次对话可直接查询。", dir))
    
    return sb.String()
}
```

### 4.2 Prompt 引导 Cd 使用

**文件**: `prompt.md`（主 Agent prompt）
**改动**: 在「环境信息」段落中增加 Cd 使用提示。

**增加内容**（插入到 `## 环境` 段落末尾）:
```markdown
### 目录导航习惯
- 你有一个 `Cd` 工具可以切换工作目录，切换后所有后续 Shell 命令都会在新目录执行
- **强烈建议**：当你发现自己在 `/workspace` 下频繁使用 `ls`/`find` 寻找项目时，用 `Cd` 切换到项目根目录
- Cd 工具会自动返回目录的项目类型和结构信息，帮你快速建立上下文
- 切换后你不再需要每次都写完整路径
```

### 4.3 验证标准

- [ ] Cd 到一个 Go 项目目录后，返回摘要包含「Go 项目」和顶层目录结构
- [ ] Cd 到非项目目录时，正常返回路径信息，不报错
- [ ] 主 Agent prompt 中包含 Cd 使用引导文本

---

## 5. Phase 2: 项目知识卡片与 Prompt 引导

### 5.1 知识卡片 Schema

知识卡片是存储在 archival memory 中的半结构化文本。**不引入新数据结构**，完全复用现有 `archival_memory_insert` 工具。

**标准格式**（通过 prompt 引导 LLM 遵循）:

```markdown
[PROJECT_CARD]
项目名称: xbot
项目路径: /workspace/xbot
项目类型: Go
技术栈: Go 1.22, SQLite, chromem-go, OpenAI/Anthropic API, Docker
关键入口:
  - 主入口: cmd/xbot/main.go
  - Agent 核心: agent/engine.go, agent/engine_wire.go
  - 记忆系统: memory/letta/letta.go, storage/vectordb/archival.go
  - 工具系统: tools/*.go
常用命令:
  - go test ./...  — 运行全部测试
  - go run cmd/xbot/main.go  — 启动
  - go build ./...  — 编译检查
项目描述: Go 语言 AI Agent 框架，支持 SubAgent、MCP 工具、三层记忆系统
最后更新: 2026-03-20
[END_PROJECT_CARD]
```

### 5.2 Prompt 引导策略

**文件**: `prompt.md`
**位置**: 在「三层记忆系统」段落之后新增「项目知识库」段落

**新增内容**:
```markdown
## 项目知识库

你有一个长期的项目知识库，存储在 archival memory 中。你可以用它记住用户的项目信息，避免每次对话都重新探索。

### 知识卡片管理规则

**何时创建知识卡片**：
- 当你在一个新项目中工作了 3 次以上，且每次都要用 Glob/Grep/Read 探索项目结构时
- 当用户明确说「记住这个项目」时
- 当你用 Cd 切换到项目目录，Cd 返回的项目信息很有价值时

**何时查询知识卡片**：
- 对话开始时，如果用户的消息暗示某个项目（提到文件路径、项目名、技术栈关键词）
- 当用户问「那个项目在哪」时
- 当你需要回忆项目结构、技术栈、常用命令时

**何时更新知识卡片**：
- 发现项目新增了重要模块或目录时
- 项目技术栈发生变化时（如新增依赖、切换框架）
- 你发现卡片中的信息已过时时

**知识卡片格式**：
用 `archival_memory_insert` 工具存储，格式以 `[PROJECT_CARD]` 开头、`[END_PROJECT_CARD]` 结尾。
更新时：先 `archival_memory_search` 找到旧卡片，用返回的 ID 通过对话上下文提醒自己需要更新，然后插入新卡片（archival memory 不支持原地更新，新卡片会自然取代旧卡片的检索排名）。

**查询方式**：
用 `archival_memory_search` 工具搜索项目名、路径、技术栈关键词。例如：
- `archival_memory_search("xbot 项目路径和结构")`
- `archival_memory_search("Go agent 框架")`
```

### 5.3 记忆合并与冲突解决

**策略**：不实现自动合并，依赖 LLM 自治 + 自然淘汰。

1. **不删除旧卡片**：archival memory 的 `Delete` 工具存在但不在知识卡片工作流中使用
2. **自然淘汰**：新卡片总是比旧卡片更相关（时间更新 + 内容更新），检索时自然排在前面
3. **LLM 自决**：LLM 在搜索时看到多个版本的卡片，自行判断哪个是最新的
4. **冗余容忍**：少量冗余卡片不会显著影响检索质量，因为 embedding 相似度排序会把最相关的放在前面

**为什么不自动合并**：
- LLM 能很好地处理「看到两个版本的卡片，选最新的」这个任务
- 自动合并逻辑复杂且容易出错（合并冲突、信息丢失）
- 自然淘汰策略简单可靠

### 5.4 向量检索优化

**现状**：`ArchivalService.Search()` 调用 `coll.Query(ctx, query, limit, nil, nil)`（`archival.go:410`），不使用 `where` 过滤。

**Phase 2 不修改检索逻辑**，依赖以下自然机制保证质量：
- `[PROJECT_CARD]` 标记让卡片内容在 embedding 空间中与其他记忆天然区分
- LLM 的查询通常包含项目名/路径等强信号词
- 向量相似度排序会把最相关的项目卡片排在前面

**Phase 3 会考虑** metadata 过滤优化（见第 6 节）。

### 5.5 验证标准

- [ ] `prompt.md` 包含「项目知识库」引导段落
- [ ] 引导段落包含创建/查询/更新三个时机和标准格式
- [ ] 在新对话中告诉 LLM 「用户有一个叫 xbot 的 Go 项目」，LLM 会主动查询 archival memory
- [ ] LLM 创建的知识卡片包含 `[PROJECT_CARD]` 标记和所有必填字段

---

## 6. Phase 3: 项目提示自动注入中间件

### 6.1 设计思路

新增一个中间件，在对话开始时自动检测 archival memory 中是否有项目知识卡片，如果有则注入到 system prompt 中。

**关键约束**：这个中间件**只作用于主 Agent**（通过 pipeline），SubAgent 不使用 pipeline，需单独处理（Phase 4）。

### 6.2 新增 `ProjectHintMiddleware`

**文件**: `agent/project_hint.go`（新文件）

**接口实现**：
```go
type ProjectHintMiddleware struct {
    archivalSvc *vectordb.ArchivalService
}

func NewProjectHintMiddleware(archivalSvc *vectordb.ArchivalService) *ProjectHintMiddleware {
    return &ProjectHintMiddleware{archivalSvc: archivalSvc}
}

func (m *ProjectHintMiddleware) Name() string { return "project_hint" }

// Priority=1: 在 SystemPromptMiddleware(0) 之后、ChannelPromptMiddleware(5) 之前
// 确保项目提示作为基础上下文的一部分，在记忆注入(100)之前
func (m *ProjectHintMiddleware) Priority() int { return 1 }

func (m *ProjectHintMiddleware) Process(mc *MessageContext) error {
    if m.archivalSvc == nil {
        return nil // 无 archival 服务时跳过
    }
    
    // 从 MessageContext 获取 TenantID
    tenantID := mc.TenantID
    if tenantID == 0 {
        return nil
    }
    
    // 查询项目知识卡片
    // 使用 chromem-go 的 whereDocument 过滤，只搜索包含 [PROJECT_CARD] 标记的内容
    // chromem-go whereDocument 支持 $contains 操作符
    entries, err := m.archivalSvc.SearchByDocumentContains(
        mc.Ctx, tenantID, "PROJECT_CARD", 3,
    )
    if err != nil || len(entries) == 0 {
        return nil
    }
    
    // 构建注入文本
    var sb strings.Builder
    sb.WriteString("\n## 已知项目\n\n")
    sb.WriteString("以下是你在之前对话中了解到的用户项目信息。你可以直接参考，无需重新探索。\n\n")
    for i, entry := range entries {
        sb.WriteString(fmt.Sprintf("### 项目 %d\n%s\n\n", i+1, entry.Content))
    }
    
    // 注入到 SystemPrompt 的 Parts 中（追加而非替换）
    mc.SystemPromptParts = append(mc.SystemPromptParts, sb.String())
    
    return nil
}
```

### 6.3 `ArchivalService` 新增过滤方法

**文件**: `storage/vectordb/archival.go`
**新增方法**：

```go
// SearchByDocumentContents searches archival entries where document content
// contains the specified substring, using chromem-go's whereDocument filter.
func (s *ArchivalService) SearchByDocumentContains(ctx context.Context, tenantID int64, contains string, limit int) ([]ArchivalEntry, error) {
    coll, err := s.getOrCreateCollection(tenantID)
    if err != nil {
        return nil, fmt.Errorf("get collection: %w", err)
    }
    
    count := coll.Count()
    if count == 0 {
        return nil, nil
    }
    if limit > count {
        limit = count
    }
    
    // chromem-go whereDocument 支持 $contains 操作符
    // 查找文档内容包含指定标记的条目
    results, err := coll.Query(ctx, contains, limit, nil, map[string]string{
        "$contains": contains,
    })
    if err != nil {
        return nil, fmt.Errorf("query by document contains: %w", err)
    }
    
    entries := make([]ArchivalEntry, len(results))
    for i, r := range results {
        createdAt, _ := time.Parse(time.RFC3339, r.Metadata["created_at"])
        entries[i] = ArchivalEntry{
            ID:          r.ID,
            TenantID:    tenantID,
            Content:     r.Content,
            Similarity:  r.Similarity,
            CreatedAt:   createdAt,
            IsCore:      r.Metadata["is_core"] == "true",
            SenderID:    r.Metadata["sender_id"],
            ToolCallID:  r.Metadata["tool_call_id"],
        }
    }
    return entries, nil
}
```

### 6.4 MessageContext 需要的扩展

当前 `MessageContext`（`agent/middleware.go`）需要暴露 `TenantID` 和 `Ctx` 字段供中间件使用。

**验证**：查看现有代码，`MessageContext` 已有：
- `Ctx context.Context` ✅
- `TenantID int64` — 需确认

让我确认：`MessageContext` 结构体的字段：确认 `MessageContext` 中已有 `TenantID` 字段（从 `middleware_builtin.go` 的 `MemoryInjectMiddleware.Process()` 中可以看到它访问 `mc.TenantID`）。✅ 已确认存在。

### 6.5 注册中间件

**文件**: `agent/agent.go`（pipeline 构建处）

在 pipeline 初始化时（`SystemPromptMiddleware` 之后），注册 `ProjectHintMiddleware`：

```go
// 在 NewAgent() 的 pipeline 构建逻辑中
pipeline.Use(NewProjectHintMiddleware(a.multiSession.ArchivalService()))
```

### 6.6 Phase 3 完整的中间件优先级

| Priority | 中间件 | Phase |
|----------|--------|-------|
| 0 | SystemPromptMiddleware | 现有 |
| **1** | **ProjectHintMiddleware** | **Phase 3 新增** |
| 5 | ChannelPromptMiddleware | 现有 |
| 100 | MemoryInjectMiddleware | 现有 |

### 6.7 验证标准

- [ ] `ProjectHintMiddleware` 实现 `Middleware` 接口（`Name()`, `Priority()`, `Process()`）
- [ ] Priority=1，在 SystemPrompt 之后、ChannelPrompt 之前
- [ ] archival 中有 `[PROJECT_CARD]` 时，system prompt 中自动出现「已知项目」段落
- [ ] archival 中无项目卡片时，不影响 system prompt（不注入空段落）
- [ ] `ArchivalService` 新增 `SearchByDocumentContains` 方法，使用 chromem-go 的 `whereDocument` 过滤
- [ ] 中间件只在主 Agent pipeline 中注册（SubAgent 不走 pipeline）

---

## 7. Phase 4: SubAgent 项目知识共享

### 7.1 问题分析

SubAgent **不使用 pipeline**（`engine_wire.go:350` 注释：「SubAgent 不使用 pipeline，需手动调用 Recall」），因此 `ProjectHintMiddleware` 不会对 SubAgent 生效。

SubAgent 的记忆通过 `deriveSubAgentTenantID()` 完全隔离——每个 (parentTenantID, parentAgentID, roleName) 组合产生独立 TenantID，意味着 SubAgent 检索的 archival memory 是**自己的**，不是用户的。

### 7.2 方案：在 SubAgent prompt 中注入项目知识

**修改点**: `agent/engine_wire.go` → `buildSubAgentRunConfig()`

**方案**：在构建 SubAgent system prompt 时（约 L247-272 区域），手动检索父 Agent 的项目知识卡片并注入。

```go
// 在 buildSubAgentRunConfig 的 sysPrompt 构建之后、messages 构建之前
// 检索父 Agent 的项目知识卡片
if parentExtras := a.buildToolContextExtras(parentCtx.Channel, parentCtx.ChatID); parentExtras.TenantID != 0 {
    if projEntries, err := a.multiSession.ArchivalService().SearchByDocumentContains(
        ctx, parentExtras.TenantID, "PROJECT_CARD", 3,
    ); err == nil && len(projEntries) > 0 {
        var sb strings.Builder
        sb.WriteString("\n## 用户项目背景\n\n")
        sb.WriteString("以下是你所属的 Agent 之前了解到的用户项目信息，供你参考：\n\n")
        for i, entry := range projEntries {
            sb.WriteString(fmt.Sprintf("### 项目 %d\n%s\n\n", i+1, entry.Content))
        }
        sysPrompt += sb.String()
    }
}
```

**关键路径说明**：
1. 通过 `parentCtx.Channel` + `parentCtx.ChatID` → `buildToolContextExtras()` → `multiSession.GetOrCreateSession()` → `LettaMemory.TenantID()` 获取父 Agent 的 TenantID
2. 用该 TenantID 调用 `ArchivalService.SearchByDocumentContains()` 检索项目卡片
3. 将结果拼接到 SubAgent 的 system prompt 末尾

### 7.3 SubAgent Prompt 引导

**文件**: `.xbot.example/agents/*.md`（示例 SubAgent 定义）

在通用 SubAgent 模板（`agent/subagent_prompt.go` 的 `subagentSystemPromptTemplate`）中增加一条提示：

```
你可以通过 archival_memory_search 查询用户的项目信息（搜索 "PROJECT_CARD" 或项目名）。
```

注意：这仅在 SubAgent 的 `tools` 列表包含 `archival_memory_search` 时有意义（即 `caps.Memory=true`）。当前 SubAgent 的 archival memory 是隔离的，搜索的是自己的空间。

**决策**：Phase 4 **不**修改 SubAgent 的 archival memory 隔离策略。原因：
1. 改变隔离策略需要修改 `deriveSubAgentTenantID()` 逻辑，影响面大
2. SubAgent 的生命周期通常是一次性的，项目知识通过 prompt 注入已经足够
3. SubAgent 如果有 memory 能力，它的 archival 用于存储自己的角色经验，不应与用户项目知识混在一起

### 7.4 验证标准

- [ ] SubAgent（有 memory 能力）的 system prompt 中包含「用户项目背景」段落
- [ ] SubAgent（无 memory 能力）不受影响
- [ ] 项目知识来自父 Agent 的 archival memory，非 SubAgent 自己的
- [ ] 修改点仅在 `buildSubAgentRunConfig()` 中，不改变记忆隔离架构

---

## 8. Phase 5: 知识库维护与治理

### 8.1 知识过期与更新机制

**策略**：基于时间戳 + LLM 自检。

1. **卡片中包含 `最后更新` 时间戳**（Phase 2 prompt 引导中已包含）
2. **Memorize 时自检**：Letta 的 `Memorize()`（`letta.go:130-200`）在每次对话结束时提取关键信息并写入 archival。可以在 prompt 引导中建议 LLM 在 memorize 时检查项目卡片是否需要更新。
3. **不实现 TTL 自动清理**：避免过度工程化。项目卡片不会快速膨胀（每个用户通常只有 2-5 个项目），少量过时卡片不会影响检索质量。

### 8.2 知识库膨胀控制

**风险**：如果 LLM 过度积极地创建项目卡片，可能导致 archival 中有大量重复或低质量卡片。

**缓解措施**：
1. **Prompt 引导的阈值**：在 Phase 2 的引导中明确「工作 3 次以上」才创建
2. **检索限制**：`ProjectHintMiddleware` 只取 top 3，即使有更多卡片也不会撑爆 prompt
3. **Archival count 监控**：通过 `ArchivalService.Count(tenantID)` 可以在后台监控，但不做自动清理
4. **未来可选**：如果发现膨胀问题，可以增加一个 `/project list` 命令让用户手动清理

### 8.3 embedding 质量保障

**现状**：embedding 通过 `embeddingFunc`（在 `ArchivalService` 中配置）完成，通常使用 text-embedding-3-small 或类似模型。

**知识卡片的 embedding 质量天然较好**，因为：
1. 卡片内容结构化、信息密度高（包含项目名、路径、技术栈等关键词）
2. `[PROJECT_CARD]` 标记在 embedding 空间中形成自然聚类
3. 查询通常也包含项目名/路径等强信号词

**不需要特殊优化**。

### 8.4 验证标准

- [ ] LLM 创建的知识卡片包含 `最后更新` 时间戳
- [ ] `ProjectHintMiddleware` 限制最多注入 3 个项目卡片
- [ ] 单个用户有 10+ 项目卡片时，检索和注入性能可接受（< 500ms）

---

## 9. 边界情况处理

### 9.1 用户同时操作多个项目

**场景**：用户在同一个对话中先讨论项目 A，然后切换到项目 B。

**处理**：
- **知识卡片**：每个项目一张独立卡片，通过路径和项目名区分
- **Cd 工具**：每次 Cd 切换后返回新项目的上下文，帮助 LLM 意识到切换
- **Prompt 注入**：`ProjectHintMiddleware` 注入所有已知项目（top 3），LLM 自行判断哪个是当前相关的
- **System prompt 提示**：「如果你需要在不同项目间切换，记得用 Cd 工具」

### 9.2 项目目录结构变化

**场景**：项目新增目录、文件迁移、重构等。

**处理**：
- **被动更新**：LLM 在工作中发现目录结构与卡片记录不一致时，通过 prompt 引导更新卡片
- **不自动同步**：避免后台监控文件系统变化的复杂度
- **Cd 提示**：每次 Cd 到项目目录时返回最新的顶层结构，作为「检查并更新」的信号

### 9.3 知识库为空

**场景**：新用户第一次使用，archival 中没有任何项目卡片。

**处理**：
- `ProjectHintMiddleware` 检测到无卡片时，不注入任何内容（`return nil`）
- Cd 工具的提示建议 LLM 创建知识卡片
- 首次 Cd 到项目时，Cd 的丰富返回值给 LLM 足够的信息来创建第一张卡片

### 9.4 多用户隔离

**场景**：不同用户的 Agent 实例不应看到彼此的项目知识。

**处理**：
- **天然隔离**：archival memory 按 `tenantID`（对应 channel:chatID）隔离，不同用户的 TenantID 不同
- **无需额外处理**：现有架构已保证隔离

### 9.5 SubAgent 深度链中的项目知识传递

**场景**：main → crown-prince → secretariat → chancellery（4 层）。

**处理**：
- Phase 4 的方案在 `buildSubAgentRunConfig()` 中注入项目知识，每一层 SubAgent 都会自动获取
- **但注意**：如果 SubAgent 嵌套过深，项目知识可能在每一层都重复注入，占用 token
- **缓解**：限制注入的卡片数量（top 3），且只在 system prompt 中注入一次

### 9.6 embedding 模型不可用

**场景**：embedding 服务宕机或未配置。

**处理**：
- `ArchivalService.Insert()` 在 `embeddingFunc == nil` 时直接返回错误（`archival.go:349-353`）
- `ProjectHintMiddleware` 在搜索失败时 `return nil`，不影响正常对话
- Cd 工具增强不依赖 embedding，仍然可以正常工作

### 9.7 项目路径变化（如沙箱重建）

**场景**：Docker 沙箱重建后，项目路径从 `/workspace/xbot` 变为 `/workspace/xbot`（通常不变，但理论上可能）。

**处理**：
- 知识卡片中的路径是绝对路径，如果路径变化则卡片信息过时
- LLM 在 Cd 到新路径时会收到提示，可以更新卡片
- **未来可选**：存储相对路径（相对于 SandboxWorkDir）而非绝对路径

---

## 10. 业界参考

### 10.1 Claude Code 的 CLAUDE.md 机制

Claude Code 使用分层 CLAUDE.md 文件：
- `CLAUDE.md`（项目级，提交到 git）：团队共享的项目上下文
- `CLAUDE.local.md`（项目级，gitignored）：个人项目上下文
- `~/.claude/CLAUDE.md`（全局）：个人偏好

**借鉴**：
- 项目上下文应该有「团队共享」和「个人」两个层次
- xbot 可以考虑未来支持 `~/.xbot/PROJECTS.md` 作为全局项目索引
- 但当前阶段通过 archival memory 更灵活（支持向量检索、自动更新）

### 10.2 MemGPT/Letta 的记忆管理

MemGPT 的核心创新是分层记忆 + LLM 自治管理：
- Core memory：始终在上下文中的关键信息（类似 xbot 的 core memory）
- Archival memory：大量长期记忆，按需检索（xbot 已实现）
- **关键借鉴**：LLM 自行决定记忆的存取，不依赖硬编码规则

**xbot 的方案与 MemGPT 的一致性**：
- 项目知识卡片存储在 archival memory 中 ✅
- LLM 自行决定何时创建/查询/更新 ✅
- 不实现自动化的记忆触发器 ✅

### 10.3 Cursor 的项目索引

Cursor 自动索引项目代码库，在需要时自动注入相关文件内容。

**差异**：
- Cursor 有文件级索引（代码搜索），xbot 目前依赖 Grep/Glob 工具
- Cursor 的索引是自动的，xbot 的知识卡片需要 LLM 主动创建
- **未来可选**：xbot 可以增加自动化的项目扫描（类似 Phase 1.5 的 fingerprint 机制）

---

## 11. 影响分析

### 11.1 修改文件清单

| 文件 | 改动类型 | Phase | 风险 |
|------|----------|-------|------|
| `tools/cd.go` | 修改（增强返回值） | 1 | 🟢 低 |
| `prompt.md` | 修改（增加引导段落） | 1+2 | 🟢 低 |
| `agent/project_hint.go` | **新增** | 3 | 🟡 中 |
| `storage/vectordb/archival.go` | 修改（新增方法） | 3 | 🟢 低 |
| `agent/agent.go` | 修改（注册中间件） | 3 | 🟢 低 |
| `agent/engine_wire.go` | 修改（SubAgent 注入） | 4 | 🟡 中 |
| `agent/subagent_prompt.go` | 可能修改（模板提示） | 4 | 🟢 低 |
| `.xbot.example/agents/*.md` | 可能修改 | 4 | 🟢 低 |

### 11.2 不影响的组件

| 组件 | 原因 |
|------|------|
| `memory/memory.go` (接口) | 不修改记忆系统接口 |
| `memory/letta/letta.go` | 不修改 Letta 实现逻辑 |
| `memory/flat/flat.go` | 不涉及 flat memory |
| `storage/vectordb/recall.go` | 不修改 recall memory |
| `storage/sqlite/*.go` | 不涉及数据库 schema |
| `agent/subagent_tenant.go` | 不修改 TenantID 推导逻辑 |
| Core memory blocks | 项目知识走 archival，不走 core |
| Session 管理 | 不影响会话创建和持久化 |

### 11.3 回滚方案

- Phase 1-2：删除 prompt.md 中新增的段落，还原 cd.go 返回值
- Phase 3：从 pipeline 中移除 `ProjectHintMiddleware`，删除 `agent/project_hint.go`
- Phase 4：还原 `buildSubAgentRunConfig()` 中的修改
- 所有 Phase 的修改互不依赖，可以独立回滚

---

## 12. 验证标准

### 12.1 Phase 1 验证

| # | 验证项 | 方法 |
|---|--------|------|
| 1.1 | Cd 到 Go 项目返回项目类型和目录结构 | 手动测试：`Cd("/workspace/xbot")` |
| 1.2 | Cd 到空目录正常返回 | 手动测试：`Cd("/tmp")` |
| 1.3 | prompt.md 包含 Cd 引导 | 文本检查 |

### 12.2 Phase 2 验证

| # | 验证项 | 方法 |
|---|--------|------|
| 2.1 | prompt.md 包含项目知识库引导 | 文本检查 |
| 2.2 | LLM 能创建标准格式知识卡片 | 对话测试：让 LLM 记住一个项目 |
| 2.3 | LLM 能查询已有知识卡片 | 新对话中问「xbot 在哪」 |
| 2.4 | LLM 能更新过时卡片 | 告诉 LLM 项目新增了目录 |

### 12.3 Phase 3 验证

| # | 验证项 | 方法 |
|---|--------|------|
| 3.1 | 中间件实现正确 | 单元测试 |
| 3.2 | 有卡片时自动注入 | 手动插入卡片，检查 system prompt |
| 3.3 | 无卡片时不注入 | 清空 archival，检查 system prompt |
| 3.4 | Priority=1 正确 | 检查注入顺序 |
| 3.5 | SearchByDocumentContains 正确 | 单元测试 |

### 12.4 Phase 4 验证

| # | 验证项 | 方法 |
|---|--------|------|
| 4.1 | SubAgent prompt 包含项目背景 | 创建带 memory 的 SubAgent，检查 prompt |
| 4.2 | 无 memory 的 SubAgent 不受影响 | 创建不带 memory 的 SubAgent |
| 4.3 | 嵌套 SubAgent 正常传递 | 2 层 SubAgent 链测试 |

### 12.5 回归测试

| # | 验证项 | 方法 |
|---|--------|------|
| R1 | 现有 archival_memory_insert/search 不受影响 | 原有功能测试 |
| R2 | 现有 Cd 工具基本功能不受影响 | Cd 后 Shell 在正确目录执行 |
| R3 | SubAgent 记忆隔离不受影响 | SubAgent archival 仍独立 |
| R4 | 性能：中间件增加的延迟 < 500ms | 基准测试 |

---

## 附录 A: chromem-go whereDocument 语法备忘

```go
// chromem-go 的 Query 方法签名
Query(ctx context.Context, query string, nResults int, where, whereDocument map[string]string) ([]Document, error)

// whereDocument 支持的操作符（作为 key 的前缀）
map[string]string{
    "$contains": "PROJECT_CARD",   // 文档内容包含指定字符串
}

// 注意：whereDocument 的 value 是普通字符串（非 JSON），$contains 操作符做子串匹配
// chromem-go 文档：https://github.com/philippgille/chromem-go
```

## 附录 B: 知识卡片示例

### 示例 1: Go 后端项目

```markdown
[PROJECT_CARD]
项目名称: xbot
项目路径: /workspace/xbot
项目类型: Go
技术栈: Go 1.22, SQLite, chromem-go (embed vector DB), OpenAI/Anthropic API, Docker
关键入口:
  - 主入口: cmd/xbot/main.go
  - Agent 核心: agent/engine.go, agent/engine_wire.go
  - 记忆系统: memory/letta/letta.go, storage/vectordb/archival.go
  - 工具系统: tools/*.go
  - 提示词模板: prompt.md, agent/subagent_prompt.go
常用命令:
  - go test ./...  — 运行全部测试
  - go run cmd/xbot/main.go  — 启动服务
  - go build ./...  — 编译检查
项目描述: Go 语言 AI Agent 框架，支持 SubAgent 嵌套、MCP 工具协议、Letta 三层记忆系统、多渠道（飞书/QQ）接入。
代码规模: ~142 个 Go 文件，~32k 行非测试代码
最后更新: 2026-03-20
[END_PROJECT_CARD]
```

### 示例 2: 前端项目

```markdown
[PROJECT_CARD]
项目名称: my-react-app
项目路径: /workspace/my-react-app
项目类型: TypeScript/React
技术栈: React 18, TypeScript, Vite, Tailwind CSS, React Query
关键入口:
  - 入口页面: src/App.tsx
  - 路由配置: src/router/index.ts
  - API 层: src/api/client.ts
  - 组件目录: src/components/
常用命令:
  - pnpm dev  — 启动开发服务器
  - pnpm build  — 生产构建
  - pnpm test  — 运行测试
项目描述: React SPA 管理后台，使用 Tailwind CSS 样式、React Query 数据管理。
最后更新: 2026-03-20
[END_PROJECT_CARD]
```

## 附录 C: Issue 模板

> **注意**：GitHub CLI 未认证，以下 Issue 内容需手动创建或认证后执行。

**标题**: `[Proposal] Project Awareness & Knowledge Base System`

**标签**: `enhancement`, `design`

**内容**:

```markdown
## Problem

xbot currently suffers from **working directory disorientation**:
1. Every command executes in `/workspace`; the LLM doesn't remember project paths across conversations
2. The `Cd` tool exists but is rarely used by the LLM
3. Project context is lost after each conversation (not persisted to archival memory)

## Proposal

Design a **Project Knowledge Base** system that leverages the existing archival memory (embed vector store) to let the LLM build and maintain project awareness.

### Key Design Decisions

1. **Prompt-first approach**: Guide the LLM via prompt to autonomously create/query/update project knowledge cards
2. **No new data structures**: Project cards are structured text stored in archival memory via existing `archival_memory_insert`
3. **Natural deduplication**: New cards naturally outrank old ones in vector similarity search
4. **Phase 1: Cd tool enhancement** — return project context on directory change
5. **Phase 2: Knowledge card schema + prompt guidance** — teach the LLM when/how to create cards
6. **Phase 3: Auto-injection middleware** — `ProjectHintMiddleware` (Priority=1) injects known projects into system prompt
7. **Phase 4: SubAgent knowledge sharing** — inject parent's project cards into SubAgent system prompt
8. **Phase 5: Knowledge governance** — stale card handling, size limits, embedding quality

### Full Proposal Document

📄 [`docs/project-awareness-proposal.md`](./docs/project-awareness-proposal.md) (v3.0, 1133 lines)

### Architecture Impact

- **New files**: `agent/project_hint.go` (middleware)
- **Modified files**: `tools/cd.go`, `prompt.md`, `storage/vectordb/archival.go`, `agent/engine_wire.go`
- **No breaking changes**: All changes are additive; existing memory/session/tool systems untouched
- **No schema changes**: No database migration needed
```
