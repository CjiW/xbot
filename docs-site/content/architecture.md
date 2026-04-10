---
title: "Architecture"
weight: 10
---

# xbot 代码架构文档

## 全局架构图

```
┌─────────────────────────────────────────────────────────────────────┐
│                           用户消息入口                               │
│  Feishu / QQ / NapCat / Web / CLI / Cron / !bang_command          │
└─────────────┬───────────────────────────────────────────────────────┘
              │ InboundMessage (cap=64)
              ▼
┌─────────────────────────────────────────────────────────────────────┐
│                        Agent.processMessage()                        │
│  ┌──────────────┐    ┌──────────────┐    ┌──────────────────────┐ │
│  │ CommandRegistry│ → │ Pipeline    │ → │ engine.Run()         │ │
│  │ 斜杠/!命令分发 │    │ 中间件链     │    │ LLM ↔ Tool 循环     │ │
│  └──────────────┘    └──────────────┘    └──────────────────────┘ │
│         │                    │                     │                │
│         │              system prompt          上下文管理            │
│         │              + 记忆注入              ├─ Offload (Layer1) │
│         │              + 工具目录              ├─ Masking (Layer2) │
│         │              + 用户消息              ├─ ContextEdit (L3) │
│         │                                    └─ Compress          │
│         ▼                                                        │
│  OutboundMessage → Dispatcher → Channel.Send()                    │
└─────────────────────────────────────────────────────────────────────┘
              │
     ┌────────┴────────┐
     ▼                 ▼
  SQLite (.xbot/xbot.db)    向量DB (.xbot/vectordb/)
  会话/记忆/配置/Token        Archival 记忆嵌入
```

---

## 1. 整体架构

### 1.1 入口文件

xbot 有三个独立的可执行程序：

| 入口 | 文件 | 用途 |
|------|------|------|
| **xbot** (server) | `main.go` | 主服务：LLM Agent 引擎 + 多渠道消息网关 |
| **xbot-cli** | `cmd/xbot-cli/main.go` | 独立终端聊天界面（单用户，直接操作文件系统） |
| **xbot-runner** | `cmd/runner/main.go` | 远程沙箱 Runner：通过 WebSocket 连接 server |

### 1.2 主服务启动流程

```
config.Load() → createLLM() → bus.NewMessageBus()
→ storage.MigrateIfNeeded() → OAuth 初始化
→ tools.InitSandbox() → agent.New(cfg)        ← 沙箱在 Agent 之前初始化（sync.Once）
→ 注册工具 → IndexGlobalTools()
→ channel.NewDispatcher(msgBus) → 注册各 Channel
→ 启动 Agent 事件循环和 Channel
```

### 1.3 核心模块划分

```
xbot/
├── main.go                  # 主服务入口
├── cmd/
│   ├── xbot-cli/            # 独立 CLI 客户端
│   └── runner/              # 远程沙箱 Runner
├── config/                  # 配置系统（JSON + 环境变量）
├── bus/                     # 消息总线（InboundMessage / OutboundMessage）
├── channel/                 # 渠道层（抽象 + 各渠道实现 + Dispatcher）
├── agent/                   # Agent 核心引擎
├── llm/                     # LLM 抽象层（OpenAI / Anthropic / Proxy）
├── session/                 # 多租户会话管理
├── memory/                  # 记忆系统（flat / letta）
├── storage/
│   ├── sqlite/              # SQLite 数据服务层
│   └── vectordb/            # 向量数据库（chromem-go）
├── tools/                   # 工具系统
├── internal/
│   ├── runnerclient/        # Runner 客户端（Runner 端使用）
│   └── runnerproto/         # Runner 协议定义
├── oauth/                   # OAuth 服务
├── event/                   # 事件系统（Webhook 接收 + Trigger 路由 + 消息注入）
├── cron/                    # 定时任务调度
├── crypto/                  # 加密工具
├── prompt/                  # 嵌入式 Prompt 模板（go:embed）
├── version/                 # 版本信息
├── agents/                  # [运行时目录] Agent 角色定义（~/.xbot/agents/），非源码包
└── web/                     # 前端静态资源（React 19 + Vite + TailwindCSS 4 + Tiptap），独立部署（不使用 go:embed）
```

### 1.4 消息流转路径

```
用户消息
  │
  ▼
Channel 实现（feishu / qq / napcat / web / cli）
  │ 解析平台消息 → 构造 bus.InboundMessage
  │ 注入 bus.Inbound（cap=64 缓冲区）
  ▼
Agent.processMessage()
  │ 1. 归一化 SenderID（singleUser 模式 → "default"）
  │ 2. CommandRegistry.Match() — 斜杠命令/!命令分发
  │    ├─ Concurrent()=true → goroutine 立即执行（不占信号量）
  │    └─ Concurrent()=false → msgCh 串行队列（cap=32）
  │ 3. 获取/创建 TenantSession
  │ 4. 获取历史消息 + eager-save 用户消息
  │ 5. Pipeline.Run() → 组装 system prompt + history + user message
  │ 6. engine.Run()
  ▼
engine.Run() — 核心循环（见 §2.2）
  │
  ▼
OutboundMessage → bus.Outbound → Dispatcher.Run()
  │
  ▼
Channel.Send(msg) → 平台 API 发送
```

### 1.5 Channel 抽象层

**接口**:
```go
type Channel interface {
    Name() string
    Start() error
    Stop()
    Send(msg bus.OutboundMessage) (string, error)
}
```

**Dispatcher**: 从 `bus.Outbound` 读取消息，按 `msg.Channel` 路由到对应 Channel。支持观察者模式和 `SendDirect()` 同步发送。

| Channel | 协议 | 重连策略 |
|---------|------|---------|
| Feishu | Lark SDK (WebSocket) | SDK 内部处理 |
| QQ | QQ Bot WebSocket | 指数退避（1s→60s），快速断连检测（5s 内 3 次 → 60s 冷却），Intent 降级，Resume 支持 |
| NapCat | OneBot 11 WebSocket | 指数退避（同 QQ），30s 长连接重置计数 |
| Web | HTTP + WebSocket | 客户端负责重连；服务端 WS Ping 30s，ReadDeadline 60s，离线 ringBuffer(50) |
| CLI | 终端 | N/A |

**Web Channel 文件**: `channel/web.go`（核心 + WS）、`channel/web_api.go`（HTTP API）、`channel/web_auth.go`（认证）、`channel/web_file.go`（文件上传）。前端静态文件通过 `SetStaticDir()` 从外部目录提供，**不使用 go:embed**。

### 1.6 斜杠命令与 Bang 命令

**CommandRegistry** (`agent/command.go`): 线性遍历按注册顺序匹配，先注册的优先。

**Bang 命令** (`!` 前缀): 在 sandbox 内直接执行 shell 命令，标记 `Concurrent()=true`（不占信号量）。超时 120s，输出超 4000 字符写入文件。

---

## 2. Agent / Loop 架构

### 2.1 Agent 结构体

`Agent` (`agent/agent.go`) 是系统核心，持有：
- `bus` — 消息总线引用
- `multiSession` — 多租户会话管理
- `tools` — 工具注册表（Registry）
- `contextManager` — 上下文管理器（接口，可运行时热切换）
- `offloadStore` — 大结果卸载存储
- `maskStore` — 观察遮蔽存储
- `contextEditor` — 上下文编辑器
- `llmFactory` — 用户自定义 LLM 工厂
- `skills/agents` — Skill/Agent 目录
- `registryManager` — 市场/共享
- `settingsSvc` — 用户设置
- `interactiveSubAgents` — Interactive SubAgent 会话池
- `hookChain` — 工具执行钩子链（与 SubAgent 共享同一实例）
- `bgTaskMgr` — 后台任务管理

### 2.2 Engine Run 循环

`engine.Run()` 入口在 `agent/engine.go`，是统一的 Agent 循环，主 Agent 和 SubAgent 共享同一个实现。内部子函数拆分到 `agent/engine_run.go`（runState、压缩、LLM 调用等），工具执行和 SubAgent spawn 在 `agent/engine_wire.go`。

**RunConfig 差异化注入**:
- **主 Agent**: ToolExecutor=完整版(含 session MCP + hook), ProgressNotifier=sendMessage, ContextManager=全局, Memory=有, EnableReadWriteSplit=true
- **SubAgent**: ToolExecutor=简化版, ProgressNotifier=nil, ContextManager=独立 phase1, Memory=可选

**循环核心逻辑** (`engine.go`):
```
for i := 0; i < maxIter; i++ {
    ① maybeCompress()              ← 75% token 阈值 + 5轮冷却
    ② LLM Generate()               ← perAttemptCtx 120s 独立超时
    ③ 无 tool_calls → 退出循环
    ④ 工具执行（读写分离并行或串行）
    ⑤ MaybeOffload()               ← Layer1: 大结果卸载
    ⑥ InvalidateStaleReads()       ← 检测 Read offload 是否过期
    ⑦ PurgeStaleMessages()         ← 替换过期 offload 为警告
    ⑧ DynamicContext 注入          ← stale 检测结果
    ⑨ System Reminder 注入         ← 每轮 strip 旧 reminder
    ⑩ 增量持久化: Session.AddMessage()
}
```

**Token 计数恢复**: 从内存 `lastPromptTokens` 恢复（进程内），或从 `tenant_state` 表恢复（重启后）。Run 结束后写回 DB + 内存。

**读写分离并行** (`EnableReadWriteSplit`):
- Phase 0: SubAgent 并发（SubAgentSem 约束）
- Phase 1: 只读工具并行（Read/Grep/Glob/WebSearch/ChatHistory，maxParallel=8）
- Phase 2: 写工具串行

**只读工具集合**（硬编码 `readOnlyTools` map，新增需手动添加）。

### 2.3 Pipeline 中间件链

按 Priority 排序执行，组装 system prompt 各部分到 `SystemParts` map，最终按 key 字典序拼接：

| 顺序 | 中间件 | Priority | SystemParts Key | 职责 |
|------|--------|----------|----------------|------|
| 1 | SystemPromptMiddleware | 0 | `00_base` | 渲染 prompt.md（支持 hot reload） |
| 2 | ProjectHintMiddleware | 1 | `05_project_hint` | 注入已知项目知识卡片 |
| 3 | SkillsCatalogMiddleware | 100 | `10_skills` | 可用 Skills 目录 |
| 4 | AgentsCatalogMiddleware | 110 | `15_agents` | 可用 Agents 目录 |
| 5 | MemoryMiddleware | 120 | `20_memory` | 长期记忆（Recall） |
| 6 | SenderInfoMiddleware | 130 | `30_sender` | 发送者名称 |
| 7 | LanguageMiddleware | 135 | `32_language` | 语言指令 |
| 8 | UserMessageMiddleware | 200 | — | 时间戳 + 引导文本 |

**CacheHint**: system prompt 标记为 `"static"`，Anthropic 下转为 `cache_control: {type: "ephemeral"}`。

### 2.4 多租户会话管理

**MultiTenantSession** (`session/multitenant.go`): 按 `(channel, chatID)` 隔离会话，内部缓存支持 24h TTL 过期，**无容量上限**。

**TenantSession** (`session/tenant.go`): 持有 tenantID、MemoryProvider、SessionMCPManager、cwd 状态。

**租户隔离**:
- IM 用户: `(channel, chatID)` → 正数 tenantID
- SubAgent: SHA-256 哈希 → 负数 tenantID

### 2.5 SubAgent 机制

**一次性 SubAgent**: 通过 `bus.InboundMessage{Channel="agent", AllowedTools, SystemPrompt, RoleName}` 创建。可选能力: `memory`, `send_message`, `spawn_agent`。

**Interactive SubAgent**: 持久会话存储在 `sync.Map`，支持多轮对话，30 分钟无活动自动清理。Key: `channel:chatID/roleName[:instance]`。

---

## 3. 上下文架构

### 3.1 消息类型

```go
type ChatMessage struct {
    Role             string     // "system" / "user" / "assistant" / "tool"
    Content          string     // LLM 可见内容
    ReasoningContent string     // DeepSeek/OpenAI reasoning 模型
    ToolCallID/Name/Arguments   // 工具调用/结果标识
    ToolCalls        []ToolCall // assistant 的工具调用列表
    Detail           string     // 完整内容（持久化到 DB，Web/CLI 展示用）
    DisplayOnly      bool       // 仅展示（不加载到 LLM 上下文）
    CacheHint        string     // "static" = 跨请求可缓存
}
```

### 3.2 四层上下文管理策略

四层策略在 engine.Run() 中按不同时机触发，形成**分层防御**：

```
单条工具结果太大
    ↓ 超过 2000 tokens 或 10240 bytes
Layer 1: Offload → 替换为 📂 [offload:ol_xxxx] + 摘要
    ↓ 文件被修改后自动标记 STALE

旧工具结果太多
    ↓ 每次压缩后触发（token 达到 60%）
Layer 2: Masking → 替换为 📂 [masked:mk_xxxx] + 摘要
    ↓ 按连续工具组分组，保留最近活跃组

上下文整体太大
    ↓ totalTokens >= 75% maxTokens
Layer 3: Compress → LLM 结构化压缩

LLM 主动编辑
    ↓ 任意时机
Layer 4: ContextEdit → 精确删除/裁剪/替换
```

#### Layer 1: Offload（大结果卸载）

- **触发**: `MaybeOffload()` — 单条工具结果超过阈值
- **存储**: 文件系统 `.xbot/offload_store/{sessionKey}/id.json`
- **摘要生成**: 按工具类型（Read/Grep/Shell/Glob/默认）生成规则摘要
- **Read 特殊**: 计算 ContentHash (SHA256)，用于 stale 检测
- **Stale 检测**: 每次工具执行后重新读取文件比对 hash，不一致则替换为过期警告
- **恢复**: `offload_recall` 工具，支持分页（offset/limit，max 16000 runes）
- **防递归**: offload_recall 和 recall_masked 的结果不会被再次 offload
- **Read 偏移保护**: 带 offset/limit 的 Read 结果不 offload
- **SubAgent 路径**: SubAgent 的 offload 存储在父 session 目录下（RootSessionKey）

#### Layer 2: Observation Masking（旧结果遮蔽）

- **触发**: `MaskOldToolResults()` — 在 `maybeCompress()` 内部调用（token 达到 60%）
- **分组策略**: assistant[tool_calls] + 连续 tool messages = 1 group
- **保留策略**: 按 ratio 动态计算 keepGroups（3/5/8/12 组）
- **活跃文件保护**: 最近 3 轮涉及的文件不会被 mask
- **折叠优化**: 纯工具组（assistant.Content 为空）折叠为一对消息，减少 message 数量
- **存储**: 纯内存 `ObservationMaskStore`，FIFO 淘汰，双重限制（200 条 / 2MB chars）
- **恢复**: `recall_masked` 工具
- **压缩后清理**: `CleanOldEntries(cutoff)` 删除压缩点之前的记录

#### Layer 3: Context Edit（精确编辑）

- **工具**: `context_edit` — LLM 主动调用
- **操作**: `delete_turn` / `delete` / `truncate` / `replace`（支持正则）
- **安全**: 不可编辑 system 消息、不可删除最后 3 条消息

### 3.3 上下文压缩

**触发条件**（四种路径）:

| 路径 | 触发条件 | 方式 |
|------|----------|------|
| 自动压缩 | `totalTokens >= 75% maxTokens` + 冷却 5 轮 | `cm.Compress()` |
| 输入超限 | LLM 返回 InputTooLong 错误 | `cm.ManualCompress()` |
| 上下文窗口超限 | `finish_reason = context_window_exceeded` | `cm.Compress()` |
| 手动 /compress | 用户命令 | `cm.ManualCompress()` |

**Phase1 压缩流程**:
1. 找 tail 切点（最后一个 user/assistant 消息）
2. 从尾部向前按 token 预算选择要压缩的消息
3. LLM 调用（最多 10 轮 multi-turn，可调用 memory tools 整理记忆）
4. 构建结果：LLMView + SessionView（不含 system/display_only）
5. **持久化**: `Session.Clear()` + 逐条 `AddMessage(SessionView)`
6. **清理**: 删除压缩点之前的 offload 文件和 mask 条目

### 3.4 消息持久化

**SessionService**: 追加到 `session_messages` 表。

| 时机 | 方式 |
|------|------|
| 用户消息 | Run() 前 eager-save |
| 每轮迭代结束后 | 增量持久化 `messages[lastPersistedCount:]`（跳过 system + strip reminder） |
| 压缩后 | `Session.Clear()` + 逐条 `AddMessage(SessionView)` |
| Masking 后 | `UpdateMessageContent(idx)` in-place 更新 |
| Cancel 后 | 保存被中断的迭代进度 |
| Run() 返回后 | 保存最终 assistant 回复 |

**GetHistory**: 以 user 消息为 turn 边界，从第 N 条 user 消息开始取后续消息。

---

## 4. 存储架构

### 4.1 SQLite

单个 SQLite 文件，WAL 模式，`MaxOpenConns=1`（SQLite 单写者模型），`BusyTimeout=5000ms`。Schema Version 21。

| 表名 | 用途 |
|------|------|
| `tenants` | 租户（channel+chatID → tenantID） |
| `session_messages` | 会话消息历史 |
| `tenant_state` | 租户状态（last_consolidated, token 计数） |
| `long_term_memory` | 长期记忆（flat 模式） |
| `event_history` | 事件历史（Letta recall，FTS5 索引） |
| `user_profiles` | 用户画像 |
| `core_memory_blocks` | 核心记忆块（Letta persona/human/working_context） |
| `archival_memory` | 归档记忆（向量索引 BLOB） |
| `cron_jobs` | 定时任务 |
| `runners` | Runner 管理 |
| `shared_registry` | Skill/Agent 市场 |
| `web_users` | Web 用户认证 |
| `user_settings` | 用户设置 |
| `user_llm_configs` | 用户自定义 LLM 配置 |
| `user_token_usage` | 用户 Token 使用量统计 |
| `schema_version` | Schema 版本 |

**数据迁移**: `storage.MigrateIfNeeded()` 通过 `schema_version` 表跟踪版本，逐步执行增量迁移。

**DB 路径**: 服务端为 `{workDir}/.xbot/xbot.db`，CLI 为 `~/.xbot/xbot.db`，**两者数据不共享**。

### 4.2 记忆系统

**MemoryProvider 接口**:
```go
type MemoryProvider interface {
    Recall(ctx, query) (string, error)
    Memorize(ctx, MemorizeInput) (MemorizeResult, error)
    Close() error
}
```

| 模式 | Recall | Memorize | 适用场景 |
|------|--------|----------|---------|
| `flat` | 直接返回 long_term_memory 全文 | LLM 压缩对话 → 写入 long_term_memory | 简单场景 |
| `letta` | 返回 Core Memory 结构化块 | LLM rethink → 更新 Core + 写入 Archival | 复杂长期记忆 |

**Memorize 触发时机**（⚠️ 不在每次对话结束后触发）:
- `/new` 命令（切换会话）— `ArchiveAll=true`
- SubAgent 退出 — `ArchiveAll=true`
- 压缩时通过 memory tools 由 LLM 自主调用

**Letta Memory**:
- **Core Memory**: 3 个结构化块（persona / human / working_context），注入 system prompt
- **Archival Memory**: 嵌入向量存储，按需通过工具检索，支持去重（搜索相似度 > 0.5 的已有记忆）
- **Recall Memory**: 基于时间范围的会话历史搜索（FTS5）

### 4.3 向量数据库

使用 `chromem-go` 嵌入式向量数据库，持久化到 `.xbot/vectordb/`。支持 OpenAI 和 Ollama embedding provider，自动截断超长内容。

---

## 5. 工具系统

### 5.1 工具分类

| 分类 | 工具 |
|------|------|
| **文件操作** | Read, Edit, Glob, Grep, CD, DownloadFile |
| **命令执行** | Shell |
| **搜索** | WebSearch, ChatHistory |
| **记忆** | save_memory (flat), core_memory_*, archival_memory_*, recall_memory_search (letta) |
| **上下文管理** | offload_recall, recall_masked, context_edit |
| **SubAgent** | SubAgent（一次性 + Interactive） |
| **Skill/Agent** | Skill, manage_tools, search_tools, load_tools |
| **卡片** | card_create, card_add_content, card_add_interactive, card_add_container, card_preview, card_send |
| **任务** | todo_write/todo_list, cron, task_start/task_status/task_cancel |
| **管理** | Logs, ask_user, OAuth |
| **飞书 MCP** | 20+ 工具（多维表格/知识库/文档/云盘） |

### 5.2 HookChain

工具生命周期拦截器，Agent 和 SubAgent 共享同一实例。

```go
type ToolHook interface {
    PreToolUse(ctx, toolName, args) error   // 返回 error 阻止执行
    PostToolUse(ctx, toolName, args, result, err, elapsed)
}
```

- 默认 hooks: `LoggingHook`（日志）+ `TimingHook`（耗时统计）
- Pre 遍历所有 hook，**不短路**（记录第一个 error）
- Post 保证所有 hook 执行（即使前面 panic）
- 执行前复制 hooks 切片，释放读锁后遍历

### 5.3 工具激活/失效机制

**coreTools vs sessionActivated**:

| 属性 | coreTools | sessionActivated |
|------|-----------|-----------------|
| 出现在 tool definitions | 始终 | 激活且未过期时 |
| 过期 | 不过期 | 连续 5 轮未使用后自动失效 |
| flat 模式 | 同上 | Register 等同 RegisterCore，永不过期 |

- `TickSession()`: 每次处理新用户消息时 round++
- `TouchTool()`: 工具执行前刷新活跃时间
- `DeactivateSession()`: session 从缓存驱逐时清理

### 5.4 MCP 集成

**全局 MCP**: `GetMCPCatalog()` 工具名前缀 `mcp_{server}_{tool}`。
**会话 MCP**: `SessionMCPManager` 每用户独立，懒加载（首次访问时连接），不活跃 30 分钟卸载。

**Stub 机制**: 未激活的 MCP 工具 `Parameters()` 返回 nil（LLM 不可见），激活后暴露完整 schema。`search_tools` 可获取所有工具的完整 schema 用于语义搜索。

### 5.5 Sandbox 系统

**Sandbox 接口**: Name, Workspace, Exec, ReadFile, WriteFile, Stat, ReadDir, MkdirAll, Close。

**SandboxRouter** (`tools/sandbox_router.go`): 统一沙箱入口，按用户级别路由到不同的后端。实现了 `Sandbox` 和 `SandboxResolver` 接口。路由规则（per-user，由 `user_settings.active_runner` 决定）:
- `active_runner == "__docker__"` → DockerSandbox（如启用）
- `active_runner == 特定远程 Runner 名称` → 对应的 RemoteSandbox 连接（如已连接）
- 兜底: Remote → Docker → None

支持同时持有 Docker 和 Remote 实例（dual-mode），可按用户独立路由。用户可在设置面板中切换活跃 Runner。

**路由策略**: 优先 Remote Runner → Docker → None。

**Remote Runner / 多 Runner 支持**: RemoteSandbox 支持多个 Runner 同时连接，每个 Runner 有独立名称和 Token。Runner 通过 WebSocket 连接 server，Token 认证（`subtle.ConstantTimeCompare`），支持 `runners` 表管理 Token。Runner 可在用户设置中按名称选择（`active_runner`），实现多用户各自绑定不同 Runner。同步配置支持将 skills/agents 目录同步到 Runner 端。

**DockerSandbox**: 每用户独立容器，stop 不 rm 下次复用，DinD 模式路径翻译。

**RemoteSandbox**: WebSocket 服务器等待 runner 连接，Token 认证（`subtle.ConstantTimeCompare`），请求-响应模式，支持 stdio 流式输出和 Runner 本地 LLM。

---

## 6. 配置系统

### 6.1 配置加载优先级

```
1. .env 文件（godotenv）          ← 最低
2. config.json（LoadFromFile，覆盖 JSON 中存在的非零值字段）
3. 环境变量覆盖（applyEnvOverrides）  ← 最高
```

### 6.2 XBOT_HOME 与路径推导

```go
func XbotHome() string {
    dir := os.Getenv("XBOT_HOME")       // 优先级1: 环境变量
    if dir == "" {
        dir = filepath.Join(home, ".xbot")  // 优先级2: ~/.xbot
    }
    os.MkdirAll(dir, 0o755)
    return dir
}
```

**用途**: `config.json`、`xbot.db`、`offload_store/`、`skills/`、`agents/`。

### 6.3 运行时配置

- **用户设置**: `user_settings` 表，Web/飞书设置面板修改
- **用户 LLM**: `user_llm_configs` 表，支持 per-user 自定义 LLM provider/model
- **Context 模式**: 可运行时热切换（`SetContextMode`）
- **MaxConcurrency**: 可运行时调整（settings 面板）

---

## 7. 错误处理与容错

### 7.1 LLM 重试策略

| 参数 | 默认值 | 环境变量 |
|------|--------|---------|
| 最大尝试次数 | 5 | `LLM_RETRY_ATTEMPTS` |
| 初始延迟 | 1s | `LLM_RETRY_DELAY` |
| 最大延迟 | 30s | `LLM_RETRY_MAX_DELAY` |
| 单次调用超时 | 120s | `LLM_RETRY_TIMEOUT` |

- **退避**: 指数退避 + 随机抖动 + 429 额外退避（`2^(min(n,4))` s）
- **perAttemptCtx**: 每次重试创建全新 120s 超时上下文，不继承父 ctx deadline，但传播取消信号
- **可重试**: 429 / 5xx / 网络错误 / 超时；不可重试: context.Canceled / 4xx
- **输入超长**: 检测到 `InputTooLong` 后强制压缩，不走重试框架
- **重试通知**: 通过 `RetryNotifyFunc` 推送进度消息 `⚠️ LLM 请求失败 (rate limit)，重试中 2/5 ...`

### 7.2 Per-Tenant LLM 并发

| LLM 类型 | 默认并发上限 |
|----------|------------|
| 全局 LLM | 5 |
| 用户自定义 LLM | 3 |
| SubAgent | 3 |

通过 `LLMSemaphoreManager` per-tenant 信号量控制。Generate 完成后立即释放（不 defer）。

### 7.3 工具执行失败处理

- **execErr**: 错误转为 tool message `"Error: <details>\nPlease fix the issue and try again."`，循环继续
- **IsError**: 提示 `"Do NOT retry the same command. Analyze the error, fix the root cause."`
- **超时**: Shell 默认 120s（最大 600s），超时后自动转为后台任务（BgTaskManager.Adopt/Start）
- **OAuth**: 匹配 OAuth 流程时返回替代内容，进入等待用户交互状态

### 7.4 消息总线

Inbound/Outbound 均为容量 64 的缓冲 channel。**无背压处理**，满了发送方阻塞。Web channel 的 `sendToClient()` 使用 select + default 非阻塞写入，满则降级到离线 ringBuffer(50)。

---

## 8. 安全模型

### 8.1 加密存储

Token 明文存储优先（DB 与服务同进程），如果设置了 `XBOT_ENCRYPTION_KEY` 则仍加密（AES-256-GCM，向后兼容旧数据）。

加密数据: OAuth access_token / refresh_token、用户 LLM api_key。

### 8.2 认证

| 机制 | 实现 |
|------|------|
| Web 登录 | bcrypt 哈希，内存 session（30 天），HttpOnly + SameSite=Lax |
| Runner Token | 256 位随机数，`subtle.ConstantTimeCompare`，双重验证（tokenStore + authToken） |
| Admin | 单一 ADMIN_CHAT_ID（回退到 STARTUP_NOTIFY_CHAT_ID），Logs 工具权限检查 |
| AllowFrom | 精确匹配白名单（逗号分隔），空白名单 = 允许所有人 |
| OAuth CSRF | 16 字节随机 state token |

### 8.3 输入验证

- **PathGuard** (`internal/runnerclient/pathguard.go`): `filepath.EvalSymlinks` 解析符号链接后检查前缀
- **Docker 容器名**: 正则校验 `^[a-z0-9][a-z0-9_.-]{0,127}$`
- **进程组**: `Setpgid=true`，超时时 kill 整个进程组

---

## 9. 性能与限流

### 9.1 五层限流体系

```
Layer 1: 全局并发信号量 (MaxConcurrency=3)
Layer 2: Per-Chat 队列 (cap=32，满则丢弃)
Layer 2.5: 用户级独立信号量 (自定义 LLM 用户，cap=1)
Layer 3: LLM Per-Tenant 信号量 (Global=5, Personal=3, SubAgent=3)
Layer 4: LLM 重试 + 单次超时 (120s × 5 = 最大 600s)
Layer 5: 工具超时 (120s) + 后台任务 (24h 安全上限)
```

### 9.2 超时汇总

| 组件 | 超时 |
|------|------|
| Shell 工具 | 默认 120s，最大 600s |
| Docker 命令 | 30s（慢操作 120s） |
| Remote exec | 60s + 5s |
| Remote WebSocket | Ping 30s, Pong 60s |
| Web HTTP | Read 10s, Write 60s, Idle 120s |
| Web WebSocket | Ping 30s, ReadDeadline 60s |
| 后台任务 | 24h 安全上限，50KB 输出截断 |
| LLM 单次调用 | 120s |
| LLM 单次调用 (Anthropic HTTP Client) | 300s |
