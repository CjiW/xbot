---
title: "NapCat"
weight: 24
---

# NapCat 渠道

通过 [NapCat](https://github.com/NapNeko/NapCatQQ)（OneBot 11 协议）接入。兼容任何 OneBot 11 实现。

**需要 Server 模式。**

## 与 QQ 渠道的区别

| | QQ 渠道 | NapCat 渠道 |
|--|---------|-------------|
| 协议 | QQ 官方机器人 API | OneBot 11 |
| 需要 | QQ 开放平台注册 | 运行 NapCat 实例 |
| 账号 | 机器人应用 | 个人 QQ 号 |
| 稳定性 | 官方支持 | 取决于 NapCat |

## 前置条件

1. xbot 已以 **Server 模式** 安装并运行
2. 运行 NapCat 实例（通过 QQNT + NapCat 插件）
3. NapCat 配置中开启 WebSocket 服务端

## 配置

```json
{
  "napcat": {
    "enabled": true,
    "ws_url": "ws://localhost:3001"
  }
}
```

| 字段 | 必填 | 默认值 | 说明 |
|------|------|--------|------|
| `enabled` | ✅ | `false` | 启用 NapCat 渠道 |
| `ws_url` | ✅ | `ws://localhost:3001` | NapCat WebSocket 地址 |
| `token` | ❌ | `""` | 鉴权 Token（如果 NapCat 配置了 token） |
| `allow_from` | ❌ | `[]` | 允许的 QQ 号列表，留空则允许所有人 |

## 使用方式

- **私聊**：直接给 QQ 号发消息
- **群聊**：@机器人 QQ 号 + 你的问题

## 注意事项

- NapCat 渠道不支持消息更新，流式渲染不可见
- 支持发送图片和文件
- 支持断线自动重连
