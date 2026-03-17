✅ 门下省审核通过 — 2026-03-18

📝 中书省修订完成（v2）— 2026-03-18

# xbot 全面 Code Review 优化方案

> **审查范围**: commit 4787346 (feat: SubAgent progress reporting + bug fixes #155)
> **审查时间**: 2026-03-18
> **修订时间**: 2026-03-18（v2，修正门下省审核意见）
> **代码规模**: 142 个 Go 文件, ~31,932 行非测试代码
> **审查人**: 中书省

---

## 一、高优先级问题（P0 — 逻辑缺陷 / 潜在 Bug / 安全隐患）

### P0-01: `agent/agent.go` — 文件 1947 行过大，建议继续拆分
- **文件**: `agent/agent.go`（1947 行），`agent/engine.go`（921 行）
- **问题**: `agent/agent.go` 文件包含约 30 个独立函数，总计 1947 行，认知负载高。`Run()` 函数实际位于 `agent/engine.go` 第 165-678 行（约 513 行），并非 1947 行的 God Function。但 `agent/agent.go` 中仍有多个大方法需要拆分：
  - `processMessage()` — 第 763-914 行（~150 行），消息路由主入口
  - `handlePromptQuery()` — 第 1018-1074 行（~56 行），LLM 对话处理
  - `handleCardResponse()` — 第 1510-1574 行（~64 行），卡片消息响应处理
  - `handleContext()` — 第 1229-1300 行（~71 行），上下文窗口管理
  - `handleCompress()` — 第 1130-1228 行（~98 行），上下文压缩
  - `compressContext()` — 第 1383-1509 行（~126 行），压缩执行逻辑
- **注意**: `handleBangCommand()` 已在 `agent/bang_command.go` 中独立实现，无需拆分。
- **影响**: 大文件增加维护难度，代码审查负担重，容易引入回归 Bug。
- **建议**: 继续从 `agent.go` 中拆分出 `agent/prompt_handler.go`（含 handlePromptQuery、handleNewSession）、`agent/card_handler.go`（含 handleCardResponse）、`agent/compress.go`（含 handleCompress、compressContext）等子文件。

### P0-02: `storage/sqlite/cron.go` — `rows.Err()` 缺失
- **文件**: `storage/sqlite/cron.go`, 第 118-133 行 (`ListCronJobs`), 第 160-175 行 (`ListCronJobsByNextRun`)
- **问题**: `for rows.Next()` 循环结束后没有调用 `rows.Err()`。如果遍历过程中发生网络错误或数据库错误，错误会被静默忽略，返回不完整的结果集。
- **影响**: 可能丢失定时任务，或返回部分数据导致定时调度异常。
- **建议**: 在 `for rows.Next()` 循环后添加:
  ```go
  if err := rows.Err(); err != nil {
      return nil, fmt.Errorf("iterate cron jobs: %w", err)
  }
  ```

### P0-03: `storage/sqlite/tenant.go` — `ListTenants` 缺失 `rows.Err()`
- **文件**: `storage/sqlite/tenant.go`, 第 107-120 行
- **问题**: 同 P0-02，`rows.Next()` 后未检查 `rows.Err()`。
- **建议**: 补充 `rows.Err()` 检查。

### P0-04: `oauth/storage.go` — 独立 SQLite 连接，缺少迁移管理
- **文件**: `oauth/storage.go`, 第 17-60 行
- **问题**: OAuth 包创建了自己的 `*sql.DB` 连接（`sql.Open("sqlite", dbPath)`），而项目其他部分使用 `storage/sqlite.DB` 进行统一的连接和迁移管理。这导致：
  1. 两个独立的数据库连接，没有迁移版本控制
  2. 第 57 行 `_ = db.Close()` — 关闭时静默忽略错误
  3. 如果路径相同会锁冲突；如果路径不同则数据分散
- **建议**: 统一使用 `storage/sqlite.DB` 管理 OAuth 表的 schema 迁移，删除独立的连接管理。

### P0-05: 多处工具 JSON 参数解析缺少字段校验
- **文件及位置**:
  - `tools/shell.go` 第 50 行 — `json.Unmarshal` 后仅检查 `params.Command == ""`，但未校验命令字符串是否包含控制字符或 null bytes
  - `tools/edit.go` 第 86 行 — `json.Unmarshal` 后未校验 `file`、`old_text`、`new_text` 是否为空
  - `tools/grep.go` 第 68 行 — `json.Unmarshal` 后未校验 `pattern` 是否为空（空正则会导致 panic）
  - `tools/subagent.go` 第 69 行 — `json.Unmarshal` 后未校验 `task` 是否为空
  - `agent/agent.go` 第 1825 行 — `formatToolProgress` 中 `json.Unmarshal` 到 `map[string]interface{}` 后直接使用 `get()` 辅助函数取值，缺少类型断言失败的日志记录
- **问题**: LLM 可能生成不合法的 JSON（字段缺失、类型错误），导致解析后空值传入后续逻辑，产生含糊的错误信息，难以调试。
- **建议**:
  1. 各工具 `Execute()` 方法在 `json.Unmarshal` 后检查 required 字段非零值
  2. 对 `grep.go` 的 `pattern` 参数增加空值/非法正则检测
  3. `agent/agent.go` 的 `formatToolProgress` 中增加解析失败时的 debug 日志

### P0-06: `tools/shell.go` — 命令注入风险，需细化拦截方案
- **文件**: `tools/shell.go`, `Execute()` 方法（第 40 行起）
- **问题**: ShellTool 直接执行用户/LLM 提供的命令字符串，当前无任何危险命令检测。虽然存在沙箱机制（`tools/sandbox_runner.go`），但沙箱并非所有场景都启用。
- **当前保护**: 已有 `sandbox.Wrap()` 做容器隔离，`cmd.Stdin = nil` 阻止交互。
- **缺失保护**: 无命令级别危险检测、无审计日志。
- **建议**: 在 `shell.go` 的 `Execute()` 中添加命令预检层：
  1. **危险命令黑名单拦截**（匹配后返回错误，不执行）：
     - `rm -rf /` / `rm -rf /*` / `rm -rf ~` — 删除根目录或家目录
     - `mkfs\.` — 格式化文件系统
     - `dd if=.*of=/dev/` — 直接写块设备
     - `:(){ :|:& };:` / `fork bomb` 类模式 — fork bomb
     - `chmod -R 777 /` — 全局提权
     - `mv / /dev/null` — 系统目录重定向
  2. **高危险命令告警**（执行但记录 warn 日志）：
     - `rm -rf` — 递归删除（非根目录时）
     - `DROP TABLE` / `DROP DATABASE` — SQL DDL
     - `shutdown` / `reboot` / `poweroff` / `halt` — 系统关机
     - `kill -9 1` — 杀 init 进程
     - `curl.*\|.*sh` / `wget.*\|.*sh` — 远程脚本管道执行
  3. **审计日志**（每次执行记录）：
     ```go
     log.WithFields(log.Fields{
         "sender_id": toolCtx.SenderID,
         "command":   params.Command,
         "timeout":   timeout,
     }).Info("Shell command executed")
     ```

### P0-07: `channel/qq.go` — WebSocket 断线重连逻辑的竞态条件
- **文件**: `channel/qq.go`, 第 359 行 (`connectTime`)、第 494/556 行 (`SetReadDeadline`)
- **问题**: WebSocket 连接管理涉及多个 goroutine（读写、重连、心跳），但断线重连逻辑中使用 `time.Now()` 和 `disconnectTimes` 切片做速率限制，缺少原子性保护。`SetReadDeadline` 在心跳和消息读取中都会被调用，可能互相覆盖。
- **建议**: 使用 channel 或 mutex 保护连接状态机，将心跳和消息读取的超时管理分离。

### P0-08: `llm/anthropic.go` + `llm/openai.go` + `llm/codebuddy.go` — 流式响应 body leak
- **文件**:
  - `llm/anthropic.go` 第 347, 502 行
  - `llm/openai.go` 第 459 行及 SSE 读取循环
  - `llm/codebuddy.go` 第 383, 405, 693 行
- **问题**: 流式响应使用 `defer resp.Body.Close()`，但如果 SSE 解析过程中 `ctx` 被取消或发生 panic，可能导致 body 未完全读取。部分实现（如 OpenAI 的 SSE）使用 `bufio.Scanner` 读取，scanner 错误后 body 可能遗留未读数据。
- **建议**: 使用 `io.ReadAll` 或 `io.Copy(io.Discard, resp.Body)` 在关闭前排空 body；或在 defer 中添加 drain 逻辑。

### P0-09: `agent/interactive.go` — RunConfig 浅拷贝的 slice 字段共享底层数组
- **文件**: `agent/interactive.go`, 第 135 行
- **代码**:
  ```go
  cfg := *ia.cfg // 浅拷贝
  ```
- **问题**: `RunConfig` 结构体包含多个 slice 字段（`Messages []llm.ChatMessage`、`ReadOnlyRoots []string`、`SkillsDirs []string` 等）。浅拷贝后这些 slice 字段与原始 `ia.cfg` 共享底层数组。虽然当前场景下由于 mutex 保护（`ia.mu.Lock()`）且拷贝后仅做字段覆盖赋值（`cfg.LLMClient`、`cfg.Model` 等非 slice 字段），不会发生实际的数据竞争。但如果未来有人在拷贝后修改 slice 内容，将导致数据泄露到模板配置中。
- **影响**: 当前安全（mutex + 只读访问），但属于脆弱约束，应标记为已知限制。
- **建议**: 在第 135 行添加注释标记此约束：
  ```go
  // 注意：此处为浅拷贝，slice 字段（Messages, ReadOnlyRoots, SkillsDirs 等）
  // 与 ia.cfg 共享底层数组。当前安全因 mutex 保护且拷贝后仅做非 slice 字段覆盖，
  // 但如果需要修改 slice 内容，必须先深拷贝。
  cfg := *ia.cfg // 浅拷贝
  ```

---

## 二、中优先级问题（P1 — 代码质量 / 可维护性）

### P1-01: `oauth/providers/feishu.go` — `ExchangeCode` 和 `RefreshToken` 大量重复
- **文件**: `oauth/providers/feishu.go`
- **问题**: `ExchangeCode`（第 156-277 行）和 `RefreshToken`（第 280-350 行）两个方法的 token 构建逻辑几乎完全相同：
  ```go
  expiresIn := 7200 // default 2 hours
  if resp.Data.ExpiresIn != nil { expiresIn = *resp.Data.ExpiresIn }
  refreshExpiresIn := 2592000 // default 30 days
  if resp.Data.RefreshExpiresIn != nil { refreshExpiresIn = *resp.Data.RefreshExpiresIn }
  token := &oauth.Token{...}
  if resp.Data.RefreshToken != nil { token.RefreshToken = *resp.Data.RefreshToken }
  if resp.Data.TokenType != nil { token.Raw["token_type"] = *resp.Data.TokenType }
  if resp.Data.Scope != nil { token.Scopes = strings.Fields(*resp.Data.Scope) }
  ```
  共约 20 行完全重复。
- **建议**: 提取 `buildTokenFromOIDCResponse(data *OIDCResponse) *oauth.Token` 私有方法消除重复。

### P1-02: `channel/feishu.go` 和 `channel/qq.go` — 各 1700+ 行，缺乏子模块拆分
- **文件**: `channel/feishu.go` (1736行), `channel/qq.go` (1800行)
- **问题**: 每个 channel 实现都是单一大文件，包含：
  - 消息发送/接收
  - 消息格式转换
  - 权限校验
  - WebSocket 管理（qq）
  - 事件处理
  - 图片/文件下载
  - 卡片消息构建
- **建议**: 按 `channel/feishu/` 子包拆分：
  - `receiver.go` — 消息接收
  - `sender.go` — 消息发送
  - `converter.go` — 消息格式转换
  - `handler.go` — 事件处理
  - `feishu.go` — 主入口

### P1-03: `tools/feishu_mcp/docx.go` — 746 行，工具函数缺乏抽象
- **文件**: `tools/feishu_mcp/docx.go`
- **问题**: 文档操作工具全部集中在一个文件中，包含多个独立的工具实现（读取文档、更新文档、创建文档等），每个工具都有独立的参数解析和错误处理模式。
- **建议**: 按功能拆分为 `docx_read.go`, `docx_write.go`, `docx_create.go` 等。

### P1-04: `agent/middleware_builtin.go` — 硬编码中文系统引导
- **文件**: `agent/middleware_builtin.go`, 第 159-170 行
- **问题**: UserMessageMiddleware 中硬编码了中文引导文本 `[系统引导]`，包括工具使用提示。这些内容应该可配置或移入 prompt.md 模板。
  ```go
  userMsg = fmt.Sprintf("%s\n\n[系统引导] 在执行任何操作前，**必须**先用`search_tools`搜索工具库尝试寻找工具。\n"+
      "- 搜索实时信息 → web_search（搜索引擎，不是浏览网页）\n"...
  ```
- **建议**: 将系统引导文本移到 `prompt.md` 或独立的配置文件中，支持多语言。

### P1-05: `tools/memory_tools.go` — 错误静默忽略
- **文件**: `tools/memory_tools.go`, 第 219 行
- **问题**:
  ```go
  _ = ctx.MemorySvc.AppendHistory(tenantID, entry)
  ```
  `AppendHistory` 的错误被完全忽略。如果记忆写入失败（数据库满、磁盘故障等），用户不会得到任何反馈，可能丢失重要信息。
- **建议**: 至少记录 warn 日志，或在工具返回值中提示用户。

### P1-06: ~~`storage/sqlite/db.go` — `Exec` 返回值不一致~~ （已删除）
> **v2 修正**: 经门下省审核指出，`storage/sqlite.DB` 没有自定义 `Exec` 方法，只有 `Conn()` 返回 `*sql.DB`（第 71 行）。原方案中描述的"DB.Exec()"方法不存在，此条为误报，已删除。

### P1-07: `tools/sandbox_runner.go` — Docker 容器生命周期管理的资源泄漏风险
- **文件**: `tools/sandbox_runner.go`
- **问题**:
  1. 容器创建后如果后续步骤失败，已创建的容器可能不会被清理
  2. `Close()` 方法只尝试一次 `docker stop`，如果失败则遗留容器
  3. 缺少定期清理孤立容器的机制
- **建议**:
  1. 使用 defer 保证容器清理
  2. 添加 startup 时清理孤立容器的逻辑（基于 label 或命名前缀）
  3. `Close()` 增加重试和 force remove

### P1-08: `tools/session_mcp.go` — `sessionLastUsed` 与 per-server `lastActive` 语义重叠
- **文件**: `tools/session_mcp.go`, 第 28 行、第 104 行、第 133 行、第 138-170 行
- **代码结构**:
  - `sessionLastUsed`（第 28 行）— 会话级活跃时间，在 `GetTools()`（第 104 行）和 `MarkActive()`（第 133 行）中设置
  - `lastActive map[string]time.Time`（第 27 行）— 每个 MCP server 的最后活跃时间
  - `UnloadInactiveServers()`（第 138 行）— 检查 per-server `lastActive` 卸载不活跃 server，返回 `sessionLastUsed` 供上层判断会话级超时
- **问题**: 两个时间戳语义不同但更新时机高度重叠（`MarkActive()` 同时更新两者），存在冗余。`UnloadInactiveServers()` 返回 `sessionLastUsed` 用于 `session/multitenant.go` 的 `cleanupInactiveResources()` 判断会话是否过期（第 24 小时），而 per-server `lastActive` 用于判断 MCP server 级别的超时（第 30 分钟）。两者在同一 mutex 保护下，不存在竞态，但语义不够清晰。
- **建议**: 添加注释明确两个时间戳的用途和差异，或考虑移除 `sessionLastUsed`，改为在 `UnloadInactiveServers()` 中返回 `max(lastActive...)` 作为会话最后活跃时间。

### P1-09: `agent/interactive.go` — SubAgent 会话管理缺乏超时
- **文件**: `agent/interactive.go`
- **问题**: Interactive SubAgent 会话存储在 `interactiveSubAgents map[string]*interactiveAgent` 中，没有 TTL 或超时清理机制。如果用户开始一个 SubAgent 会话后不再继续，会话将永远占用内存。
- **建议**: 添加会话 TTL（如 30 分钟无活动后自动清理），使用定时器或后台 goroutine 清理。

### P1-10: `config/config.go` — `init()` 加载 .env 文件
- **文件**: `config/config.go`, 第 10-12 行
- **问题**: 使用 `init()` 函数加载 `.env` 文件。`init()` 的执行顺序不可控，且无法处理加载失败。在测试中也会加载 `.env`，可能干扰测试环境。
- **建议**: 改为显式的 `LoadEnv()` 函数调用，在 `main()` 中初始化；测试中使用 `t.Setenv()` 覆盖。

### P1-11: ~~`llm/tokenizer.go` — 全局单例 tokenizer 缺少并发保护~~ （已降为 P2-16）
> **v2 修正**: 经门下省审核指出，此问题影响较小（不会导致 crash，仅可能创建多余的 tokenizer 实例浪费内存），降为 P2 级别。详见 P2-16。

### P1-12: `tools/card_builder.go` + `tools/card_tools.go` — CardBuilder 单例管理不够健壮
- **文件**: `tools/card_builder.go`, `tools/card_tools.go`
- **问题**: CardBuilder 使用全局变量 `defaultCardBuilder`，在 `GetCardBuilder()` 中 lazy 初始化。虽然使用了 `sync.RWMutex`，但：
  1. 全局状态使得测试困难
  2. `card_tools.go` 中 692 行的卡片工具实现全部依赖全局单例
- **建议**: 将 CardBuilder 作为依赖注入到需要它的工具中，通过 `ToolContext` 传递。

---

## 三、低优先级问题（P2 — 代码风格 / 可读性 / 微优化）

### P2-01: `tools/` 包文件数量过多（30+ 文件）
- **问题**: `tools/` 包包含 30+ 文件，涵盖文件操作、网络搜索、记忆管理、沙箱、MCP、卡片构建等完全不同领域的功能。
- **建议**: 按 `tools/fs/` (文件系统)、`tools/mcp/` (MCP 工具)、`tools/memory/` (记忆工具)、`tools/web/` (网络工具)、`tools/sandbox/` (沙箱) 拆分子包。

### P2-02: 命名不一致
- **文件**: 多处
- **问题**:
  1. `tools/subagent.go` — `InteractiveSubAgentManager` 接口 vs `tools/subagent_roles.go` — `SubAgentRole` 结构体（Agent vs Role 混用）
  2. `memory/` 包导出接口为 `MemoryProvider` 但实现叫 `FlatMemory` / `LettaMemory`（命名模式不统一）
  3. `bus/bus.go` — `MetadataReplyPolicy` 等 metadata 常量使用字符串字面量而非枚举类型
  4. `llm/types.go` — `NewSystemMessage` / `NewUserMessage` / `NewAssistantMessage` 构造函数风格一致，但 `NewToolCallMessage` 缺少
- **建议**: 统一命名约定：
  - 接口后缀: `Provider` / `Service` / `Manager` / `Handler` 各有语义
  - 常量枚举: 使用 `type ReplyPolicy string` + iota 常量

### P2-03: 错误信息缺乏上下文
- **文件**: `storage/sqlite/` 多个文件
- **问题**: 部分 SQL 查询的错误返回值缺乏上下文信息：
  ```go
  return nil, fmt.Errorf("list cron jobs: %w", err)  // 好
  return nil, err  // 缺乏上下文
  ```
- **建议**: 所有错误返回统一使用 `fmt.Errorf("operation: %w", err)` 包装。

### P2-04: `channel/feishu.go` — 正则表达式重复编译
- **文件**: `channel/feishu.go`, 顶部定义了多个 `regexp.MustCompile`
- **问题**: 正则在包级别编译（好），但部分正则仅在特定消息类型处理中使用，每次进程启动都会编译所有正则，包括不启用的 channel 的正则。
- **建议**: 影响极小，可忽略。但若追求极致，可延迟到首次使用时编译。

### P2-05: `tools/read.go`, `tools/grep.go`, `tools/glob.go` — 参数解析模式重复
- **文件**: `tools/read.go`, `tools/grep.go`, `tools/glob.go`
- **问题**: 每个工具的 `Execute()` 方法都以几乎相同的模式开始：
  ```go
  var args XxxArgs
  if err := json.Unmarshal([]byte(input), &args); err != nil {
      return "", fmt.Errorf("parse args: %w", err)
  }
  ```
  这个模式在整个 `tools/` 包中重复了 50+ 次。
- **建议**: 提取泛型辅助函数：
  ```go
  func parseToolArgs[T any](input string) (*T, error) {
      var args T
      if err := json.Unmarshal([]byte(input), &args); err != nil {
          return nil, fmt.Errorf("parse args: %w", err)
      }
      return &args, nil
  }
  ```

### P2-06: `llm/types.go` — `ChatMessage` 使用 `map[string]any` 存储元数据
- **文件**: `llm/types.go`, 第 15-70 行
- **问题**: `ChatMessage.Metadata` 类型为 `map[string]any`，缺乏类型安全。调用方可能传入错误类型的值。
- **建议**: 定义 `MessageMetadata` 结构体，包含已知的元数据字段。

### P2-07: `logger/logger.go` — 过度封装
- **文件**: `logger/logger.go`
- **问题**: 整个文件只是对 `logrus` 的类型别名包装，没有增加任何功能。增加了间接层但没有带来价值。
  ```go
  type Fields = log.Fields
  type Entry = log.Entry
  type JSONFormatter = log.JSONFormatter
  ```
- **建议**:
  - 方案 A: 直接在代码中使用 `logrus`，删除 logger 包
  - 方案 B: 如果保留是为了将来替换日志库，添加文档说明意图

### P2-08: 未导出的函数/类型可以提取为内部测试辅助
- **文件**: 多处
- **问题**: 部分仅被测试使用的辅助函数应该移到 `*_test.go` 中或标记为内部 API。
- **建议**: 审查所有 `export_test.go` 文件，确保内部导出有明确的 `//go:build` 保护。

### P2-09: `storage/vectordb/archival.go` — `ToolIndexService` 与 `ArchivalService` 高度相似
- **文件**: `storage/vectordb/archival.go`
- **问题**: `ToolIndexService` 和 `ArchivalService` 共享大量逻辑（collection 管理、embedding 检查、搜索模式），但各自独立实现。`SearchTools` 方法返回匿名结构体切片而非命名类型。
  ```go
  func (s *ToolIndexService) SearchTools(...) ([]struct {
      ID, Content string
      Similarity float32
      Metadata map[string]string
  }, error)
  ```
- **建议**:
  1. 提取公共的 `collectionService` 基础结构
  2. 为 `ToolSearchResult` 定义命名类型替代匿名结构体
  3. `ToolIndexEntry` 别名已在底部定义但 `SearchTools` 返回值未使用

### P2-10: `tools/feishu_mcp/` — 错误处理模式不一致
- **文件**: `tools/feishu_mcp/errors.go`, 以及 `docx.go`, `wiki.go`, `file.go`
- **问题**: 虽然定义了 `errors.go` 错误工具，但各文件的错误处理风格不一致：有的返回 `fmt.Errorf`，有的使用工具函数，有的直接返回 Lark SDK 的错误。
- **建议**: 统一使用 `errors.go` 中的错误包装函数，确保错误信息格式一致。

### P2-11: `main.go` — 初始化逻辑集中但缺乏优雅降级
- **文件**: `main.go`, 第 30-100 行
- **问题**: 所有组件（DB、OAuth、Channel、Agent）的初始化都是串行的，任何一个失败都会导致进程退出。缺少组件级别的优雅降级策略（如 OAuth 初始化失败但 CLI 模式仍可用）。
- **建议**: 按功能域分组初始化，非关键组件失败时降级而非退出。

### P2-12: `agent/engine.go` — `buildMainRunConfig` 与 `buildSubAgentRunConfig` 参数差异不显式
- **文件**: `agent/engine.go`, `agent/engine_wire.go`
- **问题**: `RunConfig` 的构建通过两个独立的函数完成（`buildMainRunConfig` 在 `engine.go`, `buildSubAgentRunConfig` 在 `engine_wire.go`），配置项差异通过代码注释说明，不够显式。
- **建议**: 使用 Builder 模式或选项模式构建 `RunConfig`，使配置差异更清晰。

### P2-13: `cron/scheduler.go` — 定时任务精度依赖轮询间隔
- **文件**: `cron/scheduler.go`, 第 150 行
- **问题**: Scheduler 使用 `time.NewTicker(1 * time.Second)` 每秒轮询检查任务是否到期（非原方案中误写的"1 分钟"）。每秒轮询的精度对现有定时任务场景（分钟级调度）完全足够，但存在持续的 CPU 开销。
- **建议**: 当前 1 秒精度可以保留。如果未来需要减少轮询开销，可考虑使用 `robfig/cron` 库或 `time.Timer` 精确触发。标记为已知限制。

### P2-14: 缺少 `context.Context` 传播
- **文件**: `storage/sqlite/memory.go`, `storage/sqlite/cron.go`, `storage/sqlite/tenant.go`
- **问题**: 部分 DB 查询方法不接受 `context.Context` 参数，无法支持查询超时或取消。
  ```go
  func (s *MemoryService) ListLongTermMemory(tenantID int64) ([]MemoryEntry, error)
  // 应该是:
  func (s *MemoryService) ListLongTermMemory(ctx context.Context, tenantID int64) ([]MemoryEntry, error)
  ```
- **建议**: 逐步为所有 DB 查询方法添加 `ctx context.Context` 参数。

### P2-15: 测试覆盖率不均
- **问题**:
  - `agent/`, `llm/`, `tools/` 有较好的测试覆盖
  - `config/`, `cron/`, `logger/`, `memory/`, `session/` 无测试文件
  - `channel/feishu.go`, `channel/qq.go` 大文件缺少测试
- **建议**: 优先为 `session/`, `memory/` 添加单元测试；`channel/` 添加集成测试。

### P2-16: `llm/tokenizer.go` — 全局单例 tokenizer 可能重复初始化（原 P1-11 降级）
- **文件**: `llm/tokenizer.go`, 第 10-12 行
- **问题**: `var tokenizers sync.Map` 使用全局 sync.Map 存储 tokenizer 实例。`getTokenizer()` 中首次创建时存在竞态：两个 goroutine 可能同时创建相同 model 的 tokenizer。
- **影响**: 不会导致 crash（sync.Map 的 LoadOrStore 是原子的），但可能创建多余的 tokenizer 实例浪费内存。实际影响很小。
- **建议**: 使用 `sync.Once` per model 或 `singleflight` 避免重复初始化。优先级低。

---

## 四、架构层面建议

### A-01: 引入依赖注入框架或手动 DI 容器
- **现状**: 组件依赖通过 `main.go` 中的函数参数手动组装，`engine_wire.go` 承担了部分 "依赖注入" 职责但不是真正的 wire。
- **建议**: 考虑使用 `uber/fx` 或维持手动 DI 但建立清晰的组件接口和生命周期管理。

### A-02: 统一错误处理策略
- **现状**: 错误处理风格不统一——有的用 `fmt.Errorf` + `%w`，有的用 `errors.New`，有的直接返回底层错误。
- **建议**:
  1. 定义项目级错误包装规范：所有 public 函数必须用 `fmt.Errorf("pkg.Func: %w", err)` 包装
  2. 对可预期的业务错误定义哨兵错误（sentinel errors）或自定义错误类型
  3. LLM 调用层统一错误分类（网络/限流/内容过滤/模型错误）

### A-03: 工具注册机制重构
- **现状**: 工具通过 `Registry.Register()` 逐个注册，缺少自动发现机制。新增工具需要修改多处代码。
- **建议**: 考虑基于接口的自动注册（类似 `database/sql.Register`），或使用代码生成。

---

## 五、执行优先级路线图

### Phase 1: 紧急修复（1-2 天）
| 编号 | 任务 | 预估工时 |
|------|------|---------|
| P0-02 | cron.go 补充 rows.Err() | 0.5h |
| P0-03 | tenant.go 补充 rows.Err() | 0.5h |
| P0-05 | 多处工具 JSON 解析增加字段校验 | 3h |
| P0-08 | LLM 流式响应 body leak | 1h |

### Phase 2: 核心重构（3-5 天）
| 编号 | 任务 | 预估工时 |
|------|------|---------|
| P0-01 | 拆分 agent.go 大方法到子文件 | 2-3 天 |
| P0-04 | OAuth 存储统一到 storage/sqlite | 0.5 天 |
| P0-06 | Shell 命令安全拦截层 + 审计日志 | 0.5 天 |
| P0-07 | QQ WebSocket 连接状态机重构 | 1 天 |

### Phase 3: 质量提升（3-5 天）
| 编号 | 任务 | 预估工时 |
|------|------|---------|
| P0-09 | interactive.go RunConfig 浅拷贝添加约束注释 | 0.5h |
| P1-01 | feishu OAuth token 构建去重 | 0.5h |
| P1-02 | channel 包子模块拆分 | 1-2 天 |
| P1-03 | feishu_mcp/docx.go 拆分 | 0.5 天 |
| P1-04 | 系统引导文本外置 | 1h |
| P1-05 | memory_tools 错误处理 | 0.5h |
| P1-07 | sandbox 容器清理 | 0.5 天 |
| P1-09 | SubAgent 会话超时 | 0.5 天 |

### Phase 4: 代码优化（持续）
| 编号 | 任务 | 预估工时 |
|------|------|---------|
| P2-01 | tools 包子包拆分 | 1 天 |
| P2-02 | 命名一致性 | 1 天 |
| P2-05 | 工具参数解析泛型辅助 | 1h |
| P2-09 | archival/tool service 抽象 | 0.5 天 |
| P2-14 | DB 查询添加 context | 1 天 |
| P2-15 | 补充测试覆盖 | 2-3 天 |

---

## 六、总结

### 整体评价
xbot 项目在架构设计上有清晰的分层意识（agent/tools/llm/memory/session/bus），SubAgent 系统和 MCP 工具集成设计精巧。代码整体质量中上，Go 惯例遵循较好，无编译错误和 vet 警告。主要问题集中在：

1. **单文件过大** — `agent.go`（1947行）、`qq.go`（1800行）、`feishu.go`（1736行）三个文件占了总代码量的 17%
2. **错误处理不完整** — 部分 DB 查询缺少 `rows.Err()`，部分错误被静默忽略
3. **代码重复** — OAuth token 构建、工具参数解析等模式重复出现
4. **测试覆盖不均** — 核心 session/memory 层缺少测试

### 好的实践（值得保持）
- ✅ Middleware 模式的消息处理管道设计优秀
- ✅ LLM 接口抽象得当，支持多 provider
- ✅ Memory 系统的 Provider 接口设计灵活
- ✅ SubAgent 系统的 progress reporting 和 spawn 机制
- ✅ 沙箱容器的隔离设计
- ✅ 统一的 Tool 接口和 Registry 模式

### v2 修订说明
| 修订项 | 类型 | 说明 |
|--------|------|------|
| P0-01 | 事实修正 | `Run()` 实际在 `agent/engine.go`（513行），非 agent.go 全文件；移除 handleBangCommand 拆分建议（已在 bang_command.go） |
| P1-06 | 删除 | `storage/sqlite.DB` 无自定义 Exec 方法，问题不存在 |
| P2-13 | 事实修正 | 轮询间隔为 1 秒（`time.NewTicker(1 * time.Second)`），非 1 分钟 |
| P0-05 | 细化 | 补充了 shell.go、edit.go、grep.go、subagent.go、agent.go 中的具体函数名和行号 |
| P0-06 | 细化 | 添加了具体危险命令黑名单和告警列表 |
| P1-08 | 修正 | 重写描述，明确 sessionLastUsed 和 per-server lastActive 的语义差异 |
| P1-11→P2-16 | 降级 | tokenizer 竞态影响极小，降为 P2 |
| P0-09 | 新增 | interactive.go RunConfig 浅拷贝 slice 共享约束 |
| Phase 2 P0-01 | 工时调整 | 从 1-2 天调整为 2-3 天 |
