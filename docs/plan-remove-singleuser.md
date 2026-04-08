# 计划：彻底移除 singleUser + "default" 归一化

> 生成时间：2026-04-08 19:17
> 状态：待确认

## 背景与目标

`singleUser` 模式将所有 senderID 归一化为 `"default"`，引入了"双 ID"体系（senderID="default" vs rawSenderID="cli_user"）。
这个设计反复导致 key 不匹配 bug（settingsSvc、token usage、SubAgent stream 等），每次都要用 rawSenderID workaround 打补丁。

**目标**：彻底移除 singleUser 标志和 senderID 归一化逻辑，让真实 senderID 贯穿全系统，
同时删除所有 rawSenderID workaround 代码。

## 现状分析

### 归一化机制（要删除的）

```
CLI (senderID="cli_user")
  → Agent.Run() bus 入口: msg.SenderID = "default", metadata["raw_sender_id"] = "cli_user"
  → buildBaseRunConfig: senderID="default", rawSenderID="cli_user"
  → settingsSvc.GetSettings("cli", rawSenderID)  ← workaround 1
  → recordMetrics(rawSenderID, ...)               ← workaround 2
  → /settings set: 用 msg.SenderID="default" 写入  ← BUG: 与读取 key 不一致
  → LanguageMiddleware: 用 "default" 读 language   ← BUG: 与 TUI 设置不一致
```

### 关键文件清单

| 文件 | 修改类型 | 说明 |
|------|----------|------|
| `agent/agent.go` | 修改 | 删除 singleUser 字段、NormalizeSenderID()、bus 入口归一化、workspaceRoot() 分支 |
| `agent/engine.go` | 修改 | 删除 RunConfig.RawSenderID 字段 |
| `agent/engine_wire.go` | 修改 | 删除 rawSenderID 参数传递、简化 settings 读取 |
| `agent/engine_run.go` | 修改 | 简化 recordMetrics（直接用 SenderID） |
| `agent/agent_config.go` | 修改 | 删除 SingleUser 字段，加 DirectWorkspace |
| `config/config.go` | 修改 | 删除 SingleUser 字段和 SINGLE_USER 环境变量 |
| `cmd/xbot-cli/main.go` | 修改 | 删除 SingleUser:true，加 DirectWorkspace |
| `main.go` | 修改 | 删除 SingleUser 传递和 NormalizeSenderID 注入 |
| `channel/web.go` | 修改 | 删除 NormalizeSenderID 字段和消费 |
| `channel/web_auth.go` | 修改 | 删除 NormalizeSenderID 消费 |
| `channel/feishu.go` | 修改 | 删除 NormalizeSenderID 字段和消费 |
| `storage/sqlite/migrations.go` | 修改 | 添加 v26 迁移：default → cli_user |
| `agent/single_user_test.go` | 删除 | 整个文件 |

### 风险点

1. **🔴 workspace 路径变化**：移除 singleUser 后 `workspaceRoot("cli_user")` 返回
   `{workDir}/.xbot/users/cli_user/workspace` 而非 `{workDir}`。需要保留 workspace 直达机制。
2. **🔴 数据库 key 不匹配**：已有数据以 `sender_id="default"` 存储（LLM config、
   settings、core memory、cron jobs、usage 等）。需要迁移。
3. **🟡 ProcessDirect senderID**：当前用 `"user"` 归一化为 `"default"`，移除后需改为 `"cli_user"`。
4. **🟢 Docker 容器/镜像名**：从 `xbot-default` 变为 `xbot-cli_user`。旧容器自动成为孤儿，无需手动处理。

## 详细计划

### 阶段一：核心清理 — 移除归一化逻辑

- [ ] 1.1：`agent/agent_config.go` — 删除 `SingleUser bool` 字段，添加 `DirectWorkspace string` 字段
  （当非空时，`workspaceRoot()` 直接返回此值，替代 singleUser 的 workspace 短路）
- [ ] 1.2：`agent/agent.go` — 删除 `singleUser bool` 私有字段和构造函数赋值
- [ ] 1.3：`agent/agent.go:1020-1029` — 删除 bus 入口的归一化 if 块（不再修改 msg.SenderID、不再设置 raw_sender_id）
- [ ] 1.4：`agent/agent.go:1066-1073` — 删除 `NormalizeSenderID()` 方法
- [ ] 1.5：`agent/agent.go:1078-1083` — 重写 `workspaceRoot()`：
  ```go
  func (a *Agent) workspaceRoot(senderID string) string {
      if a.directWorkspace != "" {
          return a.directWorkspace
      }
      return tools.UserWorkspaceRoot(a.workDir, senderID)
  }
  ```
- [ ] 1.6：`agent/agent.go:2130` — `ProcessDirect` 中 `SenderID` 从 `a.NormalizeSenderID("user")` 改为 `"cli_user"`
- [ ] 1.7：`agent/agent.go:964` — 日志中删除 `"single_user"` 字段

### 阶段二：清理 RunConfig 和 engine 层

- [ ] 2.1：`agent/engine.go:55` — 删除 `RawSenderID string` 字段
- [ ] 2.2：`agent/engine_wire.go` — `buildBaseRunConfig` 签名删除 `rawSenderID` 参数
- [ ] 2.3：`agent/engine_wire.go:92` — 删除 `RawSenderID: rawSenderID` 赋值
- [ ] 2.4：`agent/engine_wire.go:153` — 删除 `rawSenderID := msg.Metadata["raw_sender_id"]`
- [ ] 2.5：`agent/engine_wire.go:164` — `buildBaseRunConfig` 调用去掉 rawSenderID 参数
- [ ] 2.6：`agent/engine_wire.go:379-384` — 简化 settings 读取：
  删除 `settingsSenderID := rawSenderID` fallback，直接用 `senderID`
- [ ] 2.7：`agent/engine_run.go:222-231` — 简化 recordMetrics：
  删除 RawSenderID 分支，直接用 `s.cfg.SenderID`
- [ ] 2.8：`agent/engine_wire.go:551` — SubAgent language 读取保持用 `originUserID`（正确，无需改）

### 阶段三：清理 Config 和入口

- [ ] 3.1：`config/config.go:149` — 删除 `SingleUser bool` 字段
- [ ] 3.2：`config/config.go:370-373` — 删除 `SINGLE_USER` 环境变量读取
- [ ] 3.3：`cmd/xbot-cli/main.go:133` — 删除 `SingleUser: true`，加 `DirectWorkspace: cwd`
- [ ] 3.4：`main.go:482` — 删除 `SingleUser: cfg.Agent.SingleUser`（服务端不需要）
- [ ] 3.5：`main.go:296-298` — 删除 Web 的 `NormalizeSenderID` 注入
- [ ] 3.6：`main.go:662-664` — 删除 Feishu 的 `NormalizeSenderID` 注入
- [ ] 3.7：`main.go:409,418` — Runner 推送直接传 `userID`，不再调用 NormalizeSenderID

### 阶段四：清理 Channel 层

- [ ] 4.1：`channel/web.go` — 删除 `WebCallbacks.NormalizeSenderID` 字段定义
- [ ] 4.2：`channel/web.go:687-689` — 删除归一化 if 判断
- [ ] 4.3：`channel/web_auth.go:338-340` — 删除归一化 if 判断
- [ ] 4.4：`channel/feishu.go` — 删除 `SettingsCallbacks.NormalizeSenderID` 字段定义
- [ ] 4.5：`channel/feishu.go:1215-1217` — 删除归一化 if 判断

### 阶段五：数据库迁移

- [ ] 5.1：`storage/sqlite/migrations.go` — 添加 v26 迁移函数
- [ ] 5.2：迁移逻辑：将所有表中 `sender_id = "default"` 的记录更新为 `"cli_user"`
  - `user_llm_config`
  - `user_llm_subscriptions`
  - `user_settings`
  - `user_token_usage` + `daily_token_usage`
  - `core_memory_blocks` (user_id)
  - `cron_jobs`
  - `event_triggers`
  - `user_profiles`
- [ ] 5.3：`storage/sqlite/db.go` — 更新 schemaVersion 为 26

### 阶段六：清理测试和文档

- [ ] 6.1：删除 `agent/single_user_test.go`
- [ ] 6.2：更新 `docs/ARCHITECTURE.md` 中提及 singleUser 的部分

## 验证方案

1. `go build ./...` — 编译通过
2. `go test ./...` — 所有测试通过
3. CLI 启动后 `/usage` 能看到历史数据（迁移正确）
4. CLI 启动后 `/settings` 能读取已有设置
5. SubAgent 使用流式模式（不再 fallback 到 non-stream）
6. workspace 路径不变（`DirectWorkspace` 生效）

## 回滚策略

- git revert 即可。数据库迁移是单向的（default → cli_user），但即使回滚代码，
  cli_user 的数据不会被 default 覆盖（因为归一化只处理 `!= "default"` 的 senderID）。
  如需严格回滚，手动 `UPDATE ... SET sender_id = 'default' WHERE sender_id = 'cli_user'`。

## 注意事项

- `LanguageMiddleware` 和 `/settings set` 之间的 key 不一致 bug 会**自动修复**
  （移除归一化后所有地方都用同一个 senderID）
- `originUserID` / `FeishuUserID` 机制与 singleUser 无关，保持不变
- 服务端多用户模式从未使用 singleUser，完全不受影响
- SubAgent 的 Stream 继承已在上一个 commit 修复（通过 ToolContext.Stream），
  但移除 RawSenderID 后需确认 recordMetrics 仍使用正确的 senderID
