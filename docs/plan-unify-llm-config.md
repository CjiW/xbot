# 计划：统一 LLM 配置数据架构 — 消除三源写冲突

> 生成时间：2026-04-21
> 状态：待确认

## 背景与目标

### 当前问题

LLM 配置（provider, model, base_url, api_key）分散存储在三处，各自有独立的读写路径，互相不同步：

| 存储位置 | 角色 | 问题 |
|----------|------|------|
| `config.json` LLM 字段 | 启动种子 + 持久化回写 | 远程模式下不该写但仍被部分路径写入；与 DB 互相覆盖 |
| DB `user_settings` (key=value) | Settings 面板键值对 | LLM 值写入后被 `get_settings` 用订阅表覆盖，是"写后即弃"的影子数据 |
| DB `user_llm_subscriptions` | **主数据源（多订阅系统）** | `update_subscription` 时 `is_default` 被客户端零值覆盖为 false |

症状：
1. Settings 改模型 → 保存 → 重新打开显示旧值（`is_default` 被清零）
2. 切换订阅后 Settings 里的 LLM 字段不更新（`GetCurrentValues` 读的是缓存）
3. 远程/本地模式代码路径不同，修一个另一个坏

### 目标

**单一数据源：`user_llm_subscriptions` 是 LLM 配置的唯一真相。**

- `config.json` 的 LLM 字段只在启动种子时读取，之后不再回写 LLM 配置
- `user_settings` 不再存储 `llm_provider/model/base_url/api_key` 这 4 个 key
- Settings 面板打开时直接从订阅管理器获取当前活跃订阅的值
- Settings 面板保存时直接写入订阅管理器，不再绕道 `SetSetting`
- 所有修改统一走 `SubscriptionManager` 接口

---

## 现状分析

### 关键文件

| 文件 | 职责 | 修改类型 |
|------|------|----------|
| `cmd/xbot-cli/main.go` | `ApplySettings`/`GetCurrentValues`/`refreshRemoteValuesCache`/`persistActiveSubscription` | **重写** |
| `serverapp/server.go` | RPC handlers: `get_settings`/`set_setting`/`get_default_model`/`update_subscription` | **重写** |
| `serverapp/setting_handlers.go` | `settingHandlerRegistry` | **删除 LLM 相关 handler** |
| `cmd/xbot-cli/setting_handlers.go` | `cliSettingHandlers` | **删除 LLM 相关 handler** |
| `channel/cli_helpers.go` | `mergeCLISettingsValues`/`openSettingsFromQuickSwitch` | **重写** |
| `channel/cli_panel.go` | Settings 面板 schema 定义、combo 选项注入 | 修改 |
| `channel/cli_types.go` | `Subscription` struct、`SubscriptionManager` 接口 | 修改 |
| `channel/i18n.go` | Settings schema 国际化定义 | 修改 |
| `agent/llm_factory.go` | `GetLLM`/`SwitchSubscription`/`SetDefaults` | 清理旧路径 |
| `storage/sqlite/user_llm_config.go` | 旧 `UserLLMConfigService` | **废弃** |

### 数据流（改造前 vs 改造后）

**改造前**（3 条写入路径，互相不同步）：
```
Settings 面板保存 → persistActiveSubscription() → user_llm_subscriptions
                 → SetSetting() (跳过 LLM key) → user_settings
                 → applyCLISettingsToBackend() → 无 LLM handler
                 [config.json 不写，但 GetLLM 还读 configSvc]
```

**改造后**（1 条写入路径）：
```
Settings 面板保存 → SubscriptionManager.Update() → user_llm_subscriptions
                 → SwitchSubscription() → LLM factory 缓存刷新
                 [config.json / user_settings 完全不涉及 LLM]
```

### 风险点

- **风险 1**：Settings 面板的 schema 定义散布在 `i18n.go`，LLM 字段与其他字段混在一起，需要精确定位删除
- **风险 2**：远程模式下 `refreshRemoteValuesCache` 5 秒轮询 `GetSettings`，需要改为从订阅获取 LLM 值
- **风险 3**：Server 端 `get_settings` RPC 仍被 Web 管理面板使用，不能直接删除，但要确保不再返回 LLM key
- **风险 4**：旧 `UserLLMConfigService`（`user_llm_configs` 表）的读路径还在 `GetLLM` 中，需要确认废弃

---

## 详细计划

### 阶段一：Settings 面板 LLM 字段直连订阅管理器

Settings 面板中的 `llm_provider/model/base_url/api_key` 不再走 generic `SetSetting` 路径，而是直接调用 `SubscriptionManager.Update()`。

- [ ] **步骤 1.1**：在 `channel/cli_types.go` 的 `SubscriptionManager` 接口中确认 `Update(id, sub)` 方法签名足够用
- [ ] **步骤 1.2**：修改 `channel/cli_helpers.go` 的 `mergeCLISettingsValues()`：LLM 4 key 不再从 `GetCurrentValues` 读取，改为从 `subscriptionMgr.GetDefault()` 获取当前活跃订阅
- [ ] **步骤 1.3**：修改 `cmd/xbot-cli/main.go` 的 `ApplySettings` 回调：当 `llm_provider/model/base_url/api_key` 变更时，直接调 `subscriptionMgr.Update(activeSubID, updatedSub)` + `subscriptionMgr.SetDefault(activeSubID)`（如果是切换），不再调 `persistActiveSubscription` 和 `backend.SetSetting`
- [ ] **步骤 1.4**：修改 `channel/cli_helpers.go` 的 `openSettingsFromQuickSwitch()`：用新方法获取 LLM 值

### 阶段二：服务端 RPC 统一

服务端的 `get_settings` 不再返回 LLM key；`set_setting` 不再处理 LLM key；`update_subscription` 保留 `is_default` 并触发缓存刷新。

- [ ] **步骤 2.1**：修改 `serverapp/server.go` 的 `get_settings` handler：删除用订阅覆盖 LLM 值的逻辑（line 215-227），改为直接不返回 LLM key
- [ ] **步骤 2.2**：修改 `serverapp/server.go` 的 `set_setting` handler：如果 key 是 LLM 相关的，返回错误或忽略（告知客户端走 `update_subscription`）
- [ ] **步骤 2.3**：修改 `serverapp/server.go` 的 `update_subscription` handler：保留 `is_default`（已有）+ 调 `SwitchSubscription` 刷新缓存（已有）
- [ ] **步骤 2.4**：删除 `serverapp/setting_handlers.go` 中的 `llm_provider/llm_api_key/llm_model/llm_base_url` handler
- [ ] **步骤 2.5**：删除 `cmd/xbot-cli/setting_handlers.go` 中的同 4 个 handler

### 阶段三：CLI 远程模式缓存刷新

远程模式下 `GetCurrentValues` 和 `refreshRemoteValuesCache` 不再依赖 `GetSettings` RPC 获取 LLM 值。

- [ ] **步骤 3.1**：修改 `cmd/xbot-cli/main.go` 的 `refreshRemoteValuesCache()`：LLM 值改为从 `backend.GetDefaultSubscription("cli_user")` 获取，不再从 `GetSettings` 读
- [ ] **步骤 3.2**：修改 `cmd/xbot-cli/main.go` 的 `GetCurrentValues`：远程模式返回缓存中的 LLM 值（已由 3.1 填充）
- [ ] **步骤 3.3**：Settings 保存后立即刷新远程缓存（不等 5 秒轮询），调用 `refreshRemoteValuesCache()`

### 阶段四：清理 config.json LLM 回写

`config.json` 的 LLM 字段只作为启动种子，不再被运行时回写。

- [ ] **步骤 4.1**：修改 `serverapp/server.go` 的 `saveServerConfig()`：不再写入 `cfg.LLM`（或标记为启动后只写一次）
- [ ] **步骤 4.2**：修改 `cmd/xbot-cli/main.go` 的 `saveCLIConfig()`：不再写入 `cfg.LLM`（远程模式已不写，本地模式也改为写订阅）
- [ ] **步骤 4.3**：修改服务端启动流程：`migrateConfigSubscriptions` 后 `loadLLMFromDBSubscription` 是唯一的 LLM 初始化路径，删除 `applyRuntimeSettings` 中对 LLM key 的处理

### 阶段五：废弃旧系统

- [ ] **步骤 5.1**：确认 `UserLLMConfigService`（`user_llm_configs` 表）不再被任何活跃代码路径使用
- [ ] **步骤 5.2**：从 `GetLLM` 中移除 `configSvc` 查找路径（路径 2），直接走 `subscriptionSvc`
- [ ] **步骤 5.3**：删除 `persistActiveSubscription` 函数（已被阶段一的直接订阅更新取代）
- [ ] **步骤 5.4**：删除 `findSubscriptionByModel` 函数
- [ ] **步骤 5.5**：清理 debug logging（`GetLLM`/`list_models`/`SwitchSubscription` 的 verbose log）

---

## 验证方案

- **编译**：`go build ./...` 通过
- **全量测试**：`go test ./...` 通过
- **手动验证**：
  1. 启动 xbot-server → 启动 xbot-cli（远程模式）
  2. 打开 Settings → 修改 `llm_model` → 保存 → 重新打开 Settings → 确认值已更新
  3. Ctrl+P 切换订阅 → 打开 Settings → 确认 provider/model/base_url 显示新订阅的值
  4. Settings 中修改 provider → 保存 → 确认当前活跃订阅被更新
  5. 重启 server → 确认默认订阅正确加载
  6. Ctrl+N → 确认模型列表正确（openai 走 API，anthropic 只返回当前模型）
  7. 检查 DB：`SELECT id, sender_id, name, model, is_default FROM user_llm_subscriptions WHERE sender_id='cli_user'` → 确认正确的订阅是 `is_default=1`

## 回滚策略

所有改动在 `refactor/channel-subagent` 分支上。如果出问题：
1. `git revert` 整个 commit
2. DB 结构未变，无需 migration 回滚
3. `config.json` 不受影响（只是不再被写入）

## 注意事项

- **Settings schema 定义在 `i18n.go`**：LLM 相关的 4 个 field 定义保留在 schema 中（用户仍需看到和编辑这些字段），但值来源和保存路径改变
- **`channel.Subscription` vs `sqlite.LLMSubscription`**：客户端用 `channel.Subscription`，服务端用 `sqlite.LLMSubscription`。`Active` 字段在客户端映射到 `IsDefault`。需要确认序列化映射正确
- **Web 管理面板**：如果 Web 面板也用 `get_settings` 显示 LLM 信息，需要同步更新
- **不要动非 LLM 的 settings**：theme、language、max_iterations 等走 `SetSetting` 的路径不变
