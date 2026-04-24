---
title: "QQ"
weight: 23
---

# QQ 渠道

通过 QQ 官方机器人协议接入。支持私聊、群聊和频道消息。

**需要 Server 模式。**

## 前置条件

1. xbot 已以 **Server 模式** 安装并运行
2. 在 [QQ 开放平台](https://q.qq.com) 注册机器人应用
3. 获取 App ID 和 Client Secret
4. 启用 WebSocket 连接方式

## 配置

```json
{
  "qq": {
    "enabled": true,
    "app_id": "your-app-id",
    "client_secret": "your-client-secret"
  }
}
```

| 字段 | 必填 | 默认值 | 说明 |
|------|------|--------|------|
| `enabled` | ✅ | `false` | 启用 QQ 渠道 |
| `app_id` | ✅ | — | QQ Bot App ID |
| `client_secret` | ✅ | — | QQ Bot Client Secret |
| `allow_from` | ❌ | `[]` | 允许的 openid 列表，留空则允许所有人 |

## 使用方式

- **私聊**：直接给机器人发消息
- **群聊**：@机器人 + 你的问题
- **频道**：在频道中 @机器人

## 注意事项

- QQ 渠道不支持消息更新（patch），因此 Agent 的流式渲染和进度通知不可见
- Markdown 格式如果发送失败会自动降级为纯文本
- 支持断线自动重连（指数退避）
