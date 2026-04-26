---
title: "xbot"
weight: 0
---

**xbot** 是一个自托管 AI Agent 框架。部署在你自己的服务器上，通过飞书 / QQ / 终端 / 浏览器与它对话，它能调用工具完成实际工作。

## 解决什么问题

你想让 AI 帮团队做事——写代码、查文档、跑命令、操作飞书表格——但又不想把数据交给第三方 SaaS。xbot 让你在自己的服务器上运行一个全功能 AI Agent，团队成员通过他们已有的通讯工具与 Agent 对话。

## 核心特性

- 🧠 **多轮对话 + 工具调用** — Shell、文件读写、网页搜索、定时任务、子 Agent 委派
- 📱 **多渠道接入** — 同一个 Agent，飞书 / QQ / 终端 / 浏览器不同入口
- 🔑 **团队共享 LLM** — 管理员配置一次 API Key，全团队直接使用
- 🏠 **完全自托管** — 数据不出你的服务器
- 🧩 **可扩展** — Skills、SubAgents、MCP 协议

## 我该用哪个渠道

| 渠道 | 适合谁 | 特点 |
|------|--------|------|
| **CLI** | 开发者、终端用户 | 全功能 TUI，流式输出，工具调用 |
| **飞书** | 团队协作 | 在群里 @机器人 对话，支持消息卡片 |
| **QQ / NapCat** | 个人或小圈子 | QQ 聊天窗口交互 |
| **Web** | 任何有浏览器的人 | 网页聊天，注册/登录，邀请制 |

> 💡 **最常见场景**：部署 Server 模式 → 配置飞书应用 → 全团队在飞书群里 @机器人对话。

## 快速开始

```bash
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install.sh | bash

# Windows (PowerShell)
irm https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install.ps1 | iex
```

安装完成后运行 `xbot-cli`，首次会弹出 Setup 向导引导你配置 API Key。

详见 [安装指南](/xbot/installation/)。

## 架构

```
┌──────────┐     ┌──────────────┐     ┌────────┐     ┌──────────┐
│  飞书    │────▶│  Dispatcher  │────▶│ Agent  │────▶│   LLM    │
│  QQ      │◀────│  (channel/)  │◀────│ (agent/)│◀────│ (llm/)   │
│  NapCat  │     └──────────────┘     │        │     └──────────┘
│  Web     │                          │        │
│  CLI     │                          │        │────▶ 工具
└──────────┘                          │        │      (tools/)
                                      │        │
                                      │        │────▶ 记忆
                                      │        │      (memory/)
                                      └────────┘
```
