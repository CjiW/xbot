<p align="center">
  <strong>xbot</strong> — 自托管 AI Agent，接入你的飞书 / QQ / 终端 / 浏览器
</p>

> [!WARNING]
**项目已迁移**：本仓库已迁移至 [ai-pivot/xbot](https://github.com/ai-pivot/xbot)，后续开发和 Issue 请移步新仓库。此仓库不再维护。

<p align="center">
<img alt="Streaming" src="docs-site/static/img/cli/streaming.gif" width="720">
</p>

## xbot 解决什么问题

你想让 AI 帮团队做事——写代码、查文档、跑命令、操作飞书表格——但又不想把数据交给第三方 SaaS。xbot 让你**在自己的服务器上部署一个全功能 AI Agent**，通过飞书/QQ/终端/浏览器与它对话，它能调用工具完成实际工作。

**核心能力：**
- 🧠 多轮对话 + 工具调用（Shell、文件读写、网页搜索、定时任务…）
- 📱 多渠道接入（飞书、QQ、终端 TUI、Web 浏览器）——同一套 Agent，不同入口
- 🔑 团队共享 LLM Key（管理员配置一次，所有人直接用）
- 🧩 可扩展（Skills、SubAgents、MCP 协议）
- 🏠 完全自托管，数据不出你的服务器

## 我该用哪个渠道

| 渠道 | 适合谁 | 特点 |
|------|--------|------|
| **CLI** | 开发者、终端重度用户 | 全功能 TUI，支持流式输出、工具调用、SubAgent |
| **飞书** | 团队协作 | 在群里 @机器人 即可对话，支持消息卡片交互 |
| **QQ / NapCat** | 个人或小圈子 | 通过 QQ 聊天窗口与 Agent 交互 |
| **Web** | 任何有浏览器的人 | 网页聊天界面，支持注册/登录和邀请制 |

> 💡 **飞书场景最常见**：部署 server 模式 → 配置飞书应用 → 全团队在飞书群里 @机器人对话，无需各自配置 API Key。

## 快速开始

### 1. 安装

安装器会问你选哪种模式（区别见下文）：

```bash
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install.sh | bash

# Windows (PowerShell)
irm https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install.ps1 | iex
```

指定版本或安装路径：

```bash
VERSION=v0.0.24 curl -fsSL ... | bash          # 指定版本
INSTALL_PATH=~/.local/bin curl -fsSL ... | bash  # 自定义路径
```

### 2. 两种安装模式

安装器会让你选择：

| | Standalone（单机） | Server（服务端） |
|--|---------------------|------------------|
| **架构** | CLI 直接运行 Agent | 后台 Server + CLI 远程连接 |
| **适合** | 个人使用 | 团队使用、多渠道接入 |
| **多渠道** | ❌ 仅 CLI | ✅ 飞书/QQ/Web 同时启用 |
| **Web UI** | ❌ | ✅ 浏览器聊天界面 |
| **LLM 共享** | 各自配置 | 管理员配一次，全团队用 |
| **后台运行** | 关终端就停 | 系统服务，开机自启 |

> **大多数团队应选 Server 模式。** 个人开发者想快速体验可选 Standalone。

### 3. 首次运行 & 配置 API Key

安装完成后直接运行：

```bash
xbot-cli
```

首次运行会自动弹出 **Setup 向导**，引导你配置：
1. LLM 提供商（OpenAI / Anthropic）
2. API Key（必填）
3. API 地址（用 OpenAI 兼容服务时修改，如 DeepSeek、Qwen）
4. 模型名称
5. 沙箱模式和记忆模式

> ⚠️ **Setup 向导中的输入框**：选中输入框后，需要先按 **Enter** 进入编辑模式，然后才能输入内容。输入完毕后再按 **Enter** 确认。

也可以随时用 `/setup` 命令重新配置。

### 4. 直接编辑配置文件

配置文件位于 `~/.xbot/config.json`，也可以手动编辑：

**OpenAI 或兼容 API（DeepSeek、Qwen、Ollama 等）：**

```json
{
  "llm": {
    "provider": "openai",
    "api_key": "sk-xxx",
    "base_url": "https://api.openai.com/v1",
    "model": "gpt-4o"
  }
}
```

**Anthropic：**

```json
{
  "llm": {
    "provider": "anthropic",
    "api_key": "sk-ant-xxx",
    "model": "claude-sonnet-4-20250514"
  }
}
```

**使用 DeepSeek：**

```json
{
  "llm": {
    "provider": "openai",
    "api_key": "your-deepseek-key",
    "base_url": "https://api.deepseek.com/v1",
    "model": "deepseek-chat"
  }
}
```

## 渠道配置

每个渠道通过 `~/.xbot/config.json` 启用，无需环境变量。

### 飞书

在 [飞书开放平台](https://open.feishu.cn) 创建应用后：

```json
{
  "feishu": {
    "enabled": true,
    "app_id": "cli_xxx",
    "app_secret": "xxx"
  }
}
```

最小必需的应用权限：`im:message`、`im:message.receive_v1`、`im:message:send_as_bot`、`contact:user.base:readonly`

详见 [飞书配置指南](https://cjiw.github.io/xbot/channels/feishu/)。

### QQ / NapCat / Web

详见各渠道的配置指南：[Channels](https://cjiw.github.io/xbot/channels/)

## 从源码构建

```bash
git clone https://github.com/ai-pivot/xbot.git && cd xbot
make build          # 构建 xbot (server + runner)
make run            # 构建并运行 server
```

仅构建 CLI：

```bash
go build -o xbot-cli ./cmd/xbot-cli
```

## 功能概览

### 内置工具

Agent 在对话中可以调用这些工具：

- **Shell** — 在沙箱中执行命令（支持 Docker / 远程 / 无沙箱）
- **文件操作** — 读写文件、搜索内容、Glob 匹配
- **网页** — 抓取网页内容、Tavily 搜索
- **上下文** — 编辑对话上下文（裁剪/替换/删除）
- **SubAgent** — 委派任务给专门化的子 Agent
- **定时任务** — Cron 表达式或一次性定时
- **下载** — 从 URL 下载文件
- **飞书工具** — 操作飞书文档、多维表格、云盘
- **Runner** — 管理远程沙箱连接

### 记忆系统

| | Flat（默认） | Letta (MemGPT) |
|--|-------------|----------------|
| 核心记忆 | 内存块 | SQLite（始终在上下文中） |
| 归档记忆 | Grep 搜索 | 向量搜索 |
| 依赖 | 无 | 需要嵌入模型 |

### Skills & SubAgents

- **Skills** — Markdown 定义的能力包，放在 `~/.xbot/skills/`
- **SubAgents** — 基于角色的子 Agent（探索、代码审查等），自定义角色放 `~/.xbot/agents/`

### MCP 协议

支持全局和会话级 MCP Server，stdio 和 HTTP 传输。

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

## 文档

完整文档：[cjiw.github.io/xbot](https://cjiw.github.io/xbot/)

| 文档 | 说明 |
|------|------|
| [安装指南](https://cjiw.github.io/xbot/installation/) | 两种模式详解、Setup 向导 |
| [渠道配置](https://cjiw.github.io/xbot/channels/) | 飞书 / QQ / NapCat / Web / CLI |
| [配置参考](https://cjiw.github.io/xbot/configuration/) | config.json 完整字段说明 |
| [沙箱指南](https://cjiw.github.io/xbot/guides/sandbox-docker/) | Docker / 远程沙箱配置 |
| [架构](https://cjiw.github.io/xbot/architecture/) | 系统设计和数据流 |
| [CHANGELOG](CHANGELOG.md) | 版本历史 |

## License

MIT
