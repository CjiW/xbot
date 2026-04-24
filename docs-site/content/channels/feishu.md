---
title: "飞书"
weight: 22
---

# 飞书渠道

飞书是最常用的渠道。团队成员在飞书群里 @机器人即可与 Agent 对话，无需各自配置 API Key。

**需要 Server 模式。**

## 前置条件

1. xbot 已以 **Server 模式** 安装并运行
2. 拥有飞书开放平台的开发者权限
3. 一个飞书应用（App ID 和 App Secret）

## 创建飞书应用

1. 打开 [飞书开放平台](https://open.feishu.cn) → 创建企业自建应用
2. 记下 **App ID** 和 **App Secret**
3. 在「事件订阅」中配置：
   - 请求地址：填写你的服务器地址（公网可达）
   - 加密策略：如果启用，记下 **Encrypt Key** 和 **Verification Token**；不启用则留空
4. 添加事件订阅：
   - `im.message.receive_v1`（接收消息）
5. 发布应用

## 最小必需权限

在「权限管理」中开通以下权限：

| 权限 | 权限标识 | 用途 |
|------|----------|------|
| 获取与发送单聊、群组消息 | `im:message` | 收发消息 |
| 接收消息 | `im:message.receive_v1` | 接收用户消息事件 |
| 以应用的身份发消息 | `im:message:send_as_bot` | 发送回复 |
| 获取用户基本信息 | `contact:user.base:readonly` | 识别用户身份 |

> 💡 以上是**最小权限集**。如果需要 Agent 操作飞书文档、多维表格等，还需要额外权限（见下方扩展权限）。

### 扩展权限（可选）

| 功能 | 需要的权限 |
|------|-----------|
| 上传图片/文件 | `im:resource` |
| 操作文档 | `docx:document`、`wiki:wiki` |
| 操作多维表格 | `bitable:app` |
| 获取机器人信息 | `bot:bot:get_bot_info` |

## 配置

在 `~/.xbot/config.json` 中添加飞书配置：

### 最小配置

```json
{
  "feishu": {
    "enabled": true,
    "app_id": "cli_xxx",
    "app_secret": "your-app-secret"
  }
}
```

### 完整配置

```json
{
  "feishu": {
    "enabled": true,
    "app_id": "cli_xxx",
    "app_secret": "your-app-secret",
    "encrypt_key": "your-encrypt-key",
    "verification_token": "your-verification-token",
    "allow_from": []
  }
}
```

| 字段 | 必填 | 默认值 | 说明 |
|------|------|--------|------|
| `enabled` | ✅ | `false` | 启用飞书渠道 |
| `app_id` | ✅ | — | 飞书应用 App ID |
| `app_secret` | ✅ | — | 飞书应用 App Secret |
| `encrypt_key` | ❌ | `""` | 事件加密 Key（在飞书后台配置了加密才需要） |
| `verification_token` | ❌ | `""` | 验证 Token（在飞书后台配置了加密才需要） |
| `allow_from` | ❌ | `[]` | 允许的用户 open_id 列表，留空则允许所有人 |

## 全局 LLM：让全团队共享 API Key

这是飞书渠道最常见的使用方式：**管理员配置一次 LLM，所有飞书用户直接使用。**

### 配置步骤

1. 在 `~/.xbot/config.json` 中配置全局 LLM：

```json
{
  "llm": {
    "provider": "openai",
    "api_key": "sk-xxx",
    "base_url": "https://api.openai.com/v1",
    "model": "gpt-4o"
  },
  "feishu": {
    "enabled": true,
    "app_id": "cli_xxx",
    "app_secret": "xxx"
  }
}
```

2. 启动 Server：`xbot-cli serve`

3. 飞书用户在群里 @机器人 即可对话，无需任何额外配置。

### LLM 解析逻辑

- 用户没有个人订阅 → 使用全局 `llm` 配置（即上面的配置）
- 管理员可以为特定用户创建个人订阅（覆盖全局配置）
- 全局 LLM 的 API Key 在数据库中加密存储

## 使用方式

### 私聊

直接给机器人发消息即可。

### 群聊

在群里 @机器人 + 你的问题。不带内容的纯 @ 会被忽略。

### 消息卡片

Agent 可以发送交互式消息卡片（如设置面板、确认对话框等），用户可以直接点击卡片上的按钮操作。

## 网络要求

xbot 通过 **WebSocket** 连接飞书服务器（非 HTTP 回调），因此：

- Server 需要能**出站**访问飞书 WebSocket 端点
- **不需要**公网 IP 或入站端口（与 HTTP 回调模式不同）
- 如果服务器在 NAT/防火墙后面，只要能访问外网即可

## 常见问题

**Q: 飞书渠道不生效？**
- 确认 `feishu.enabled` 为 `true`
- 确认 App ID 和 App Secret 正确
- 确认 Server 正在运行：`xbot-cli serve`
- 检查日志：`~/.xbot/logs/`

**Q: 机器人收不到消息？**
- 确认已订阅 `im.message.receive_v1` 事件
- 确认应用已发布
- 群聊需要 @机器人

**Q: 用户报 "no LLM configured"？**
- 确认 `config.json` 中 `llm.api_key` 已配置
- 全局 LLM 配置会让所有无个人订阅的用户共享此 Key
