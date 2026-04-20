# 计划：CLI Sessions 面板 + 命令体系重构

> 生成时间：2026-04-20
> 状态：待确认

## 背景与目标

用户反馈三个问题：
1. **后台任务面板看不到** — 后台真的有任务，但底部不显示指示器，`^` 键无法打开面板
2. **需要新增 Sessions 面板** — 统一管理所有聊天室（主会话 + SubAgent 会话），替代旧的 Agent tab
3. **命令体系不合理** — `/su` 塞了太多功能，需要拆分出独立的 session/chat 管理命令

目标：修复 bug + 新增 Sessions 面板 + 重构命令

## 现状分析

### Bug: 后台任务面板不可见

**分析**：`^` 键触发条件是 `m.bgTaskCount > 0 || m.agentCount > 0`（`cli_update_handlers.go:100`）。`bgTaskCount` 只在 progress 事件和 injection 事件时从 `bgTaskCountFn` 刷新。`bgTaskCountFn` 使用 `bgSessionKey`（初始化时固定为 `"cli:" + cliCfg.ChatID`）调用 `ListRunning(key)`。

可能的根因：
- **sessionKey 不匹配**：`/su chat:new` 改了 `m.chatID`，新后台任务用 `cfg.Channel + ":" + cfg.ChatID` 注册，但 `bgSessionKey` 还是旧的
- **刷新时机不够**：`bgTaskCount` 只在 progress/injection 事件时刷新，如果后台任务在两次事件之间完成，count 可能不对
- **`bgTaskCountFn` 为 nil**：初始化时 `bgSessionKey` 为空导致没设置

**修复方案**：
1. `^` 键触发条件去掉 gate，始终可打开面板
2. 面板 `openBgTasksPanel` 内部自行刷新 `panelBgTasks`（已有），所以打开后数据是最新的

### 关键文件

| 文件 | 职责 | 修改类型 |
|------|------|----------|
| `channel/cli_update_handlers.go` | 按键处理 | 修改：`^` 去掉 gate |
| `channel/cli_panel.go` | 面板渲染 | 修改：BgTasks 去掉 Agent 条目 + 新增 Sessions 面板 |
| `channel/cli_message.go` | 命令处理 | 修改：拆分 /su + 新增 /ss /chat 命令 |
| `channel/cli_model.go` | 数据模型 | 修改：新增 Sessions 面板状态 |
| `channel/cli_types.go` | 类型定义 | 修改：新增 callback |
| `channel/cli.go` | CLI 初始化 | 修改：注入新 callback |
| `channel/cli_view.go` | 底部提示 | 修改：新增 sessions 快捷键提示 |
| `cmd/xbot-cli/main.go` | 入口 | 修改：接线新 callback |

## 详细计划

### 阶段一：修复后台任务面板

- [ ] **1.1** `cli_update_handlers.go` — `^` 键触发条件去掉 `bgTaskCount > 0 || agentCount > 0` gate，改为 `panelMode == ""` 即可打开
- [ ] **1.2** `cli_panel.go` — BgTasks 面板去掉主会话条目（index 0 的 main chatroom）和 Agent 条目，只保留后台 Shell 任务列表
- [ ] **1.3** `cli_panel.go` — 恢复 cursor 索引为直接对应 `panelBgTasks`（不再偏移 +1）
- [ ] **1.4** `cli_view.go` — 底部 hint 中 `^` 的显示条件改为始终显示（或保持 bgTaskCount > 0，因为面板始终可打开了，但 hint 没必要一直显示）

### 阶段二：新增 Sessions 面板

- [ ] **2.1** `cli_model.go` — 新增 `panelSessions` 相关状态字段：
  ```
  panelMode = "sessions"
  panelSessionCursor int
  panelSessionItems []sessionItem  // 结构：{type: "main"|"agent", chatID, label, role, instance, parentChatID}
  panelSessionViewing bool
  panelSessionLogLines []string
  ```
- [ ] **2.2** `cli_types.go` — 新增 `SessionsList` callback 类型：
  ```go
  SessionsList func() []SessionInfo
  ```
  `SessionInfo` 包含：chatID, label, type(main/agent), role, instance, parentChatID
- [ ] **2.3** `cli_panel.go` — 新增 `openSessionsPanel()` / `updateSessionsPanel()` / `viewSessionsPanel()` 三个方法
  - 列表展示：主聊天室（当前 channel:chatID）+ 每个 chatID 下的活跃 SubAgent
  - Enter：查看 session 消息
  - Esc：关闭
  - Tab/Enter on main session：切换到该 session（改变 chatID）
- [ ] **2.4** `cli_update_handlers.go` — 新增快捷键绑定（`Ctrl+S` 或 `Tab`），待讨论
- [ ] **2.5** `cli.go` — 注入 `SessionsList` callback
- [ ] **2.6** `cmd/xbot-cli/main.go` — 实现 `SessionsList` callback，组装 session 列表

### 阶段三：命令体系重构

- [ ] **3.1** `/su` 回归纯 switch user — 删除 chat:new / chat:id / web:xxx 分支
- [ ] **3.2** 新增 `/ss`（`/sessions`）命令 — 打开 Sessions 面板（同快捷键）
- [ ] **3.3** 新增 `/chat new [label]` — 创建新会话
- [ ] **3.4** 新增 `/chat <id>` — 切换到指定会话
- [ ] **3.5** 新增 `/chat ls` — 列出所有会话（文字版）
- [ ] **3.6** `/tasks` 保持不变（已有，纯文字版后台任务列表）

### 阶段四：清理与验证

- [ ] **4.1** 删除 `openBgTasksPanel` 中关于 Agent 列表的代码
- [ ] **4.2** 编译 + 测试通过
- [ ] **4.3** 更新 AGENT.md knowledge files

## 验证方案

- `go build ./...` 编译通过
- `go test ./channel/...` 测试通过
- 手动验证：
  - `^` 键始终可打开 BgTasks 面板（无论是否有任务）
  - BgTasks 面板只显示 Shell 后台任务
  - Sessions 面板显示主会话 + SubAgent 会话
  - `/su` 只切用户，`/chat new` 创建会话，`/ss` 开面板

## 注意事项

- `panelMode` 值现在是 `"bgtasks"`，新增 `"sessions"` 不会冲突
- 快捷键选择：`Ctrl+S` 是保存（可能冲突），`Tab` 是补全（已有），考虑用 `Ctrl+T`（tasks→sessions）或直接只用 `/ss` 命令
