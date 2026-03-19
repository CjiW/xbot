# xbot 五大方向重构 — 终审报告

**审核机构**：门下省  
**审核日期**：2026-03-19  
**审核范围**：6 大代码变更方向，24 个新增/修改文件  
**审核基准**：`/workspace/xbot/plan/xbot-major-refactor-design.md` (v3)

---

## Phase 1：编译与静态检查

| 检查项 | 结果 |
|--------|------|
| `go build ./...` | ✅ 全量通过 |
| `go vet ./...` | ✅ 全量通过 |
| `go test ./...` | ✅ 全量通过 |

所有包编译、静态分析、测试均无错误。

---

## Phase 2：逐方向深入审核

### 方向 1：Channel 特化 Prompt 隔离

**涉及文件**：`agent/channel_prompt.go`、`agent/channel_prompt_test.go`、`agent/middleware.go`、`agent/agent.go`、`channel/feishu.go`、`main.go`

#### ✅ 正确性

| 检查点 | 结论 |
|--------|------|
| `ChannelPromptProvider` 接口定义在 `agent` 包内 | ✅ 正确，不依赖 `channel` 包 |
| `ChannelPromptMiddleware` 实现 `MessageMiddleware` 接口 | ✅ 优先级 50，插入在基础 prompt (0) 和 skills/agents (100+) 之间 |
| `feishuPromptAdapter` 桥接模式（main.go 底部） | ✅ 正确避免 agent ↔ channel 循环依赖 |
| `main.go` 已注入 `SetChannelPromptProviders` | ✅ line 154，仅在 `feishuCh != nil` 时注入 |
| `FeishuChannel.ChannelSystemParts()` 返回 `map[string]string` | ✅ 与 `SystemParts` key-value 模式一致 |

#### ✅ 完整性

| 检查点 | 结论 |
|--------|------|
| 测试覆盖：mock provider + middleware 行为验证 | ✅ 3 个测试用例覆盖匹配/不匹配/空 parts |
| `ChannelSystemParts` key 命名规范 | ✅ 使用 `"00_feishu_rules"` 前缀，符合 `BuildSystemPrompt` 字典序约定 |

#### ✅ 循环依赖

| 依赖路径 | 结论 |
|----------|------|
| `agent` → `channel` | ❌ 无直接依赖（通过 `feishuPromptAdapter` 在 main.go 桥接） |
| `channel` → `agent` | ❌ 无依赖 |
| `channel` → `bus` | ✅ channel 只依赖 bus（OK） |

#### 裁定：**通过** ✅

---

### 方向 2：LLM Encode 缓存优化

**涉及文件**：`llm/types.go`、`llm/anthropic.go`、`llm/anthropic_test.go`、`agent/middleware.go`

#### ✅ 正确性

| 检查点 | 结论 |
|--------|------|
| `ChatMessage.CacheHint` 字段 (`llm/types.go:31`) | ✅ `"static"` / `""` 两值语义清晰 |
| `Assemble()` 设置 `sysMsg.CacheHint = "static"` (`middleware.go:150`) | ✅ system prompt 始终标记为可缓存 |
| `buildAnthropicSystem()` 处理 CacheHint (`anthropic.go`) | ✅ `static` → `cache_control: {"type": "ephemeral"}`，非 static 不标注 |
| `cache_control` 格式符合 Anthropic API 规范 | ✅ `{"type": "ephemeral"}` 为官方标准格式 |
| 单条无缓存 system 返回 string（非 array） | ✅ 向后兼容，不破坏旧 API |
| 多条或含缓存标注时返回 array | ✅ 符合 Anthropic multi-block system 格式 |

#### ✅ 完整性

| 检查点 | 结论 |
|--------|------|
| 测试覆盖 (`anthropic_test.go`) | ✅ 5 个测试：无系统消息 / 单条无缓存 / 静态缓存 / 混合 / 多条动态 |
| OpenAI provider 不处理 CacheHint | ✅ `openai.go` 中无 `CacheHint` 引用，OpenAI 不支持 prompt caching（正确忽略） |

#### ✅ 风险评估

| 风险点 | 评估 |
|--------|------|
| 非缓存 system 仍可被 Anthropic KV-cache | ✅ 无风险，KV-cache 是隐式的 |
| 缓存断开后首次请求额外延迟 | ✅ Anthropic 文档已说明，设计预期内 |
| CacheHint 字段序列化到 session | ✅ `json:"cache_hint,omitempty"` — 为空时不序列化，不污染持久化数据 |

#### 裁定：**通过** ✅

---

### 方向 3：LLM 回复与进度报告增强

**涉及文件**：`agent/progress.go`、`agent/progress_test.go`、`agent/engine.go`、`agent/reply.go`、`agent/reply_test.go`

#### 3a. 结构化进度事件系统

##### ✅ 正确性

| 检查点 | 结论 |
|--------|------|
| `ProgressEvent` / `StructuredProgress` / `ToolProgress` / `ToolStatus` 类型定义 | ✅ 结构合理，常量定义清晰（`PhaseThinking`/`PhaseToolExec`） |
| `RunConfig.ProgressEventHandler` 字段 (`engine.go:77`) | ✅ 回调签名 `func(event *ProgressEvent)` |
| `Run()` 中 `structuredProgress` 初始化与生命周期管理 | ✅ 仅在 `ProgressEventHandler != nil` 时创建 |
| 工具执行时状态流转 `ToolPending → ToolRunning → ToolDone/ToolError` | ✅ 每个工具独立跟踪，含耗时记录 |
| `ProgressNotifier` 与 `ProgressEventHandler` 并存 | ✅ 旧回调保持不变，新回调是增强（非替代） |

##### ✅ 完整性

| 检查点 | 结论 |
|--------|------|
| 测试覆盖 (`progress_test.go`) | ✅ 验证事件创建、字段赋值、零值安全 |
| 迭代计数更新 | ✅ 每轮循环更新 `structuredProgress.Iteration` |
| 思考内容记录 | ✅ `structuredProgress.ThinkingContent = cleanContent` |
| 完成工具归档 | ✅ `CompletedTools = append(CompletedTools, ActiveTools...)` 后清空 |

##### ⚠️ 注意事项

| 项 | 说明 |
|----|------|
| `ProgressEventHandler` 尚未被上层消费 | 当前 main.go 未注入 `ProgressEventHandler`，飞书卡片进度渲染需后续接入 |

#### 3b. ExtractFinalReply

##### ✅ 正确性

| 检查点 | 结论 |
|--------|------|
| 短内容（< 500 字符）原样返回 | ✅ |
| 长内容取最后一段 | ✅ |
| 最后一段过短（< 50 字符）时合并倒数两段 | ✅ |

##### ✅ 完整性

| 检查点 | 结论 |
|--------|------|
| 测试覆盖 (`reply_test.go`) | ✅ 3 个用例：短内容 / 正常长内容 / 末段过短 |

##### ⚠️ 问题

| 项 | 严重度 | 说明 |
|----|--------|------|
| **死代码** | ⚠️ 中 | `ExtractFinalReply` 仅在测试中被调用，无任何生产代码引用。函数定义合理但尚未集成 |

#### 裁定：**通过** ✅（附建议：尽快集成 `ExtractFinalReply` 或标记为 planned）

---

### 方向 4：全局 Skill/Agent Registry

**涉及文件**：`agent/registry.go`、`agent/command_builtin.go`、`storage/sqlite/registry.go`、`storage/sqlite/registry_test.go`、`storage/sqlite/db.go`

#### ✅ 正确性

| 检查点 | 结论 |
|--------|------|
| `RegistryManager` 结构体与 CRUD 方法 | ✅ Publish / Unpublish / Browse / Install / Uninstall / MyEntries 完整 |
| `SharedSkillRegistry` 存储层 (`storage/sqlite/registry.go`) | ✅ 标准 SQLite CRUD，SQL 注入安全（参数化查询） |
| type 约束 `CHECK(type IN ('skill', 'agent'))` | ✅ 数据库层防护 |
| sharing 约束 `CHECK(sharing IN ('private', 'public'))` | ✅ |
| v12 → v13 migration (`db.go`) | ✅ `CREATE TABLE IF NOT EXISTS` 幂等，含索引 |
| 新建数据库 schema 直接包含 v13 表 | ✅ `createSchema()` 中已包含 `shared_registry` 和 `user_settings` |

#### ✅ 完整性

| 检查点 | 结论 |
|--------|------|
| 测试覆盖 (`registry_test.go`) | ✅ CRUD + 边界（重复发布、未找到、类型过滤） |
| 内置命令注册 (`command_builtin.go`) | ✅ `/publish`、`/unpublish`、`/browse`、`/install`、`/uninstall`、`/my` 均已注册 |
| nil 保护 (`command_builtin.go:265`) | ✅ `a.registryManager == nil` 检查后返回提示 |

#### ⚠️ 问题

| 项 | 严重度 | 说明 |
|----|--------|------|
| **RegistryManager 未注入** | 🔴 高 | `Agent.registryManager` 字段已声明（agent.go:254）但**无公开 setter**、**main.go 未注入**。`/publish` 等命令运行时会因 nil 而静默失败 |
| 缺少 setter 方法 | 🔴 高 | `Agent` 没有 `SetRegistryManager(*RegistryManager)` 方法，外部无法注入 |
| Unpublish 权限校验 | ⚠️ 低 | 仅检查 `entry.Author == senderID`，未校验 `entry.Type` 与请求类型匹配（但 data layer 已有 type 过滤） |

#### 裁定：**驳回修改** ⚠️

**修改意见**：
1. 在 `agent/agent.go` 添加 `SetRegistryManager(rm *RegistryManager)` 方法
2. 在 `main.go` 初始化 `RegistryManager` 并注入到 `Agent`
3. 注入时机：在 `agentLoop := agent.New(...)` 之后、`Run()` 之前

---

### 方向 5：Channel Settings Capability

**涉及文件**：`channel/capability.go`、`channel/capability_test.go`、`agent/settings.go`、`agent/settings_test.go`、`storage/sqlite/user_settings.go`、`agent/command_builtin.go`、`channel/feishu.go`

#### ✅ 正确性

| 检查点 | 结论 |
|--------|------|
| `SettingsCapability` 接口 (`channel/capability.go`) | ✅ `SettingsSchema()` + `HandleSettingSubmit()` + `BuildSettingsUI()` + `BuildProgressUI()` |
| `UIBuilder` 接口 | ✅ 与 SettingsCapability 组合使用 |
| `SettingDefinition` 结构体 | ✅ key / label / type / options / required / default 字段完整 |
| `SettingsService` (`agent/settings.go`) | ✅ 封装 `UserSettingsService`，增加 channel 层 UI 能力查询 |
| `UserSettingsService` (`storage/sqlite/user_settings.go`) | ✅ 标准 CRUD，UNIQUE(channel, sender_id, key) |
| `FeishuChannel` 实现 SettingsCapability | ✅ 4 个方法均已实现 |
| 内置命令 `/set`、`/menu` (`command_builtin.go`) | ✅ 已注册，nil 保护完备 |
| `user_settings` 表 v13 migration | ✅ 幂等，含索引 |

#### ⚠️ 问题

| 项 | 严重度 | 说明 |
|----|--------|------|
| **SettingsService 未注入** | 🔴 高 | `Agent.settingsSvc` 字段已声明（agent.go:257）但**无公开 setter**、**main.go 未注入**。`/set`、`/menu` 命令运行时因 nil 静默失败 |
| `agent/settings.go` import "xbot/channel" | ⚠️ 中 | 虽然 `channel` 不反向依赖 `agent`（无循环），但 `agent` 包直接依赖 `channel` 包打破了既有的分层架构。目前通过 import 只用了 `SettingsCapability` 接口和 `UIBuilder` 接口。**建议**：将这两个接口提取到独立包（如 `channel/types` 或 `channel/capability` 已在此包中，但 agent 仍直接引用 channel 包） |

#### ⚠️ 循环依赖检查

| 依赖路径 | 状态 |
|----------|------|
| `agent` → `channel` | ⚠️ 新增（`settings.go`） |
| `channel` → `agent` | ❌ 无 |
| 结论 | **当前无循环依赖**，但 `agent → channel` 是架构上的方向反转 |

#### 裁定：**驳回修改** ⚠️

**修改意见**：
1. 在 `agent/agent.go` 添加 `SetSettingsService(svc *SettingsService)` 方法
2. 在 `main.go` 初始化 `SettingsService` 并注入到 `Agent`
3. （架构建议）考虑将 `SettingsCapability` / `UIBuilder` 接口从 `channel` 包提取为独立接口包，避免 `agent → channel` 的直接依赖

---

### 方向 6：LLM 层类型增强

**涉及文件**：`llm/types.go`、`llm/anthropic.go`、`llm/anthropic_test.go`、`llm/openai.go`

已在方向 2 中一并审核。补充：

| 检查点 | 结论 |
|--------|------|
| `ChatMessage` 新增 `CacheHint` 字段 | ✅ `json:"cache_hint,omitempty"` — 空值不序列化，向后兼容 |
| `TokenUsage.Add()` 方法 | ✅ 值类型语义正确 |
| `ToolCallDelta` 新增 `ID` 和 `Name` 字段 | ✅ 支持流式工具调用增量 |
| `StreamEventType` 枚举扩展 | ✅ 含 `EventReasoningContent` 支持 DeepSeek 推理模型 |

#### 裁定：**通过** ✅

---

## 全局问题汇总

### 🔴 必须修复（阻塞性）

| # | 问题 | 影响范围 | 修复方案 |
|---|------|----------|----------|
| 1 | `RegistryManager` 无 setter、main.go 未注入 | `/publish`、`/unpublish`、`/browse`、`/install`、`/uninstall`、`/my` 6 个命令全部静默失败 | 添加 setter + main.go 注入 |
| 2 | `SettingsService` 无 setter、main.go 未注入 | `/set`、`/menu` 2 个命令全部静默失败 | 添加 setter + main.go 注入 |

### ⚠️ 建议改进（非阻塞）

| # | 问题 | 建议 |
|---|------|------|
| 3 | `ExtractFinalReply` 为死代码（无生产调用点） | 尽快集成到 reply 发送路径，或添加 `// Planned: ...` 注释说明意图 |
| 4 | `ProgressEventHandler` 未被 main.go 消费 | 飞书卡片进度渲染的接入需排入后续迭代 |
| 5 | `agent/settings.go` 直接 import `channel` 包 | 考虑提取接口到独立包，维持分层清晰 |
| 6 | Unpublish 缺少 type 匹配校验 | 建议在 Service 层增加校验（低优先级） |

### ✅ 确认无问题的架构要点

| 项 | 结论 |
|----|------|
| 循环依赖 | ✅ 无循环（`agent ↔ channel` 通过 main.go 桥接，settings.go 的新依赖是单向的） |
| Go 接口惯用法 | ✅ 小接口（1-3 方法）、显式接口满足、nil 安全 |
| 向后兼容 | ✅ `CacheHint` omitempty、schema migration 幂等、旧 ProgressNotifier 保持不变 |
| 测试覆盖 | ✅ 每个新增模块均有对应测试文件，覆盖正常路径和边界条件 |
| 并发安全 | ✅ `MessagePipeline` 读写锁、`Agent` 字段初始化在 Run() 前 |

---

## 终审裁定

### 总评：⚠️ **驳回修改（附 2 项必须修复）**

方向 1-3、6 实现质量优秀，代码风格一致，架构设计合理。**但方向 4 和 5 存在同一类系统性遗漏**：新增的服务字段（`registryManager`、`settingsSvc`）在 `Agent` struct 中声明、在 `command_builtin.go` 中消费，但缺少公开 setter 方法且 main.go 未注入初始化代码。这导致 8 个新增命令（`/publish`、`/unpublish`、`/browse`、`/install`、`/uninstall`、`/my`、`/set`、`/menu`）在运行时全部因 nil check 而静默返回提示信息，功能完全不生效。

### 修复清单（预计工作量：~30 行代码）

1. **`agent/agent.go`** — 添加两个 setter：
   ```go
   func (a *Agent) SetRegistryManager(rm *RegistryManager) { a.registryManager = rm }
   func (a *Agent) SetSettingsService(svc *SettingsService) { a.settingsSvc = svc }
   ```

2. **`main.go`** — 在 `agentLoop := agent.New(...)` 之后添加初始化：
   ```go
   // 注入 RegistryManager
   sharedDB, _ := sqlite.Open(dbPath)  // 复用已有连接
   agentLoop.SetRegistryManager(agent.NewRegistryManager(sharedDB, xbotDir))

   // 注入 SettingsService
   agentLoop.SetSettingsService(agent.NewSettingsService(
       sqlite.NewUserSettingsService(sharedDB),
       disp,  // channel.Dispatcher 用于查询 SettingsCapability
   ))
   ```

修复完成后可免审提交（门下省确认修改范围可控）。

---

*门下省 · 审核完毕*
