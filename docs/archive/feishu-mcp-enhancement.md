# 飞书 MCP 增强：Download + Card 迁移 + SendFile + Prompt 完善

## 目标

1. 给飞书 MCP 新增 **download** 能力（复用 `tools/download.go` 逻辑）
2. 将 **card 相关工具**（`card_create` 等 6 个）从 `tools/` 迁移到 `tools/feishu_mcp/`
3. 新增 **发送文件给用户** 工具（`feishu_send_file`）
4. 完善 **飞书渠道专属 prompt**

## 分析

### 现状

| 组件 | 位置 | 说明 |
|------|------|------|
| `DownloadFile` | `tools/download.go` | 核心工具，用 tenant_access_token 下载消息资源 |
| `card_create/add/send` 等 6 个 | `tools/card_tools.go` + `tools/card_builder.go` | 飞书专属，但在通用 tools 包 |
| `feishu_send_card` | `tools/feishu_mcp/wiki.go` | 已在 MCP 中，发送原始 card JSON |
| `feishu_upload_file` | `tools/feishu_mcp/file.go` | 上传到云空间（不是发到聊天） |
| 发送文件到聊天 | `channel/feishu.go:sendFile` | 内部方法，未暴露为工具 |
| 飞书 prompt | `channel/feishu.go:ChannelSystemParts` | 仅 5 行 |

### 架构决策

**Q: download 应该用 tenant_access_token 还是 user_access_token？**
- 消息资源 API（`/im/v1/messages/{id}/resources/{key}`）需要的是 **tenant_access_token**
- 现有 `download.go` 通过 env vars 获取 tenant_access_token
- feishu_mcp 中的 `FeishuMCP` 结构体持有 `lark.Client`，可复用其 tenant token 能力
- **方案**：在 `FeishuMCP` 中新增 `GetTenantToken()` 方法，复用 lark client 的 app credential

**Q: card_builder.go 是否整体迁移？**
- `CardBuilder` 被 `channel/feishu.go` 引用（回调处理）
- 如果迁移到 `feishu_mcp/`，`channel/` 需要导入 `tools/feishu_mcp`，但这会引入不需要的依赖
- **方案**：`card_builder.go` 留在 `tools/`（它是 session 状态管理器，不涉及飞书 API），只迁移 `card_tools.go` 中的 6 个 Tool 定义到 `tools/feishu_mcp/`

## 任务拆分

### 1. feishu_mcp 新增 DownloadFile 工具

- **目标**：在 `tools/feishu_mcp/file.go` 新增 `DownloadFileTool`
- **复用**：`tools/download.go` 中的 HTTP 请求逻辑
- **改动**：
  - `tools/feishu_mcp/file.go`：新增 `DownloadFileTool`（使用 `FeishuMCP` 的 tenant token）
  - `tools/feishu_mcp/feishu_mcp.go`：新增 `GetTenantToken()` 方法（通过 lark client 获取）
  - `main.go`：注册 `feishu_mcp.DownloadFileTool`
  - 保留原 `tools/download.go` 中的 `DownloadFileTool` 作为 core 工具（兼容非飞书渠道和自动触发场景）

### 2. 迁移 card 工具到 feishu_mcp

- **目标**：`card_create` 等 6 个 Tool 定义从 `tools/card_tools.go` 迁移到 `tools/feishu_mcp/card_tools.go`
- **改动**：
  - 新建 `tools/feishu_mcp/card_tools.go`：6 个 Tool 定义（`CardCreateTool` 等）
  - `tools/card_tools.go`：删除 Tool 定义，保留动态注册/注销辅助函数
  - `tools/card_builder.go`：**保留不动**（session 管理器，被 channel 和 feishu_mcp 共用）
  - `agent/agent.go`：更新 import 和注册路径
  - `main.go`：注册路径从 `tools.NewCardCreateTool` → `feishu_mcp.NewCardCreateTool`
  - 所有 6 个 Tool 需要实现 `SupportedChannels() → ["feishu"]`

### 3. 新增 feishu_send_file 工具

- **目标**：Agent 可主动调用，将本地文件发送到飞书聊天
- **改动**：
  - `tools/feishu_mcp/file.go`：新增 `SendFileTool`
  - 参数：`file_path`（必填）、`chat_id`（选填，默认当前）、`type`（选填，"file"/"image"，自动检测）
  - 实现：通过 `ctx.SendFunc` 发送 `__FEISHU_FILE__::path` 或直接用 lark IM API
  - 注意：需要与 `channel/feishu.go` 中的 `sendFile` 方法协调，避免代码重复
  - `main.go`：注册 `feishu_mcp.SendFileTool`

### 4. 完善飞书渠道 prompt

- **目标**：扩充 `ChannelSystemParts` 内容，覆盖关键使用指南
- **改动**：
  - `channel/feishu.go:ChannelSystemParts`：扩展 prompt
  - 内容要点：
    - 飞书 Markdown 限制（不支持复杂表格、嵌套列表）
    - 文件/图片处理（`<file>` `<image>` 标签 + download 工具）
    - 卡片使用（`card_create` 构建交互卡片，适用于表单、选择、按钮交互）
    - 发送文件（`feishu_send_file` 发送本地文件给用户）
  - **长度控制**：不超过 40 行

## 执行顺序

1. **任务 1**：DownloadFile（独立，无依赖）
2. **任务 3**：SendFile（独立，无依赖）
3. **任务 2**：Card 迁移（改动面最大，涉及 import 调整）
4. **任务 4**：Prompt 完善（最后做，确保所有工具就位后编写）
5. **验证**：build / test / lint

## 风险评估

| 任务 | 风险 | 等级 |
|------|------|------|
| DownloadFile | 复用现有逻辑，低风险 | 🟢 |
| Card 迁移 | import 变更面广，需仔细处理循环依赖 | 🟡 |
| SendFile | 需协调 channel 和 mcp 之间的文件发送逻辑 | 🟢 |
| Prompt | 纯文本修改 | 🟢 |
