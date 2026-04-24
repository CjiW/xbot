---
title: "安装指南"
weight: 5
---

# 安装指南

## 安装方式

### 一键安装（推荐）

```bash
# Linux / macOS (amd64, arm64)
curl -fsSL https://raw.githubusercontent.com/CjiW/xbot/master/scripts/install.sh | bash

# Windows (PowerShell)
irm https://raw.githubusercontent.com/CjiW/xbot/master/scripts/install.ps1 | iex
```

指定版本或安装路径：

```bash
VERSION=v0.0.24 curl -fsSL ... | bash          # 指定版本
INSTALL_PATH=~/.local/bin curl -fsSL ... | bash  # 自定义安装路径
```

### 从源码构建

```bash
git clone https://github.com/CjiW/xbot.git && cd xbot
make build          # 构建 xbot (server + runner)
make run            # 构建并运行 server
```

## 两种安装模式

安装器会让你选择 **Standalone** 或 **Server** 模式。

### Standalone（单机模式）

CLI 直接在本地运行 Agent，不依赖后台服务。

- ✅ 简单，安装即用
- ✅ 无后台进程
- ❌ 关终端就停
- ❌ 仅 CLI 渠道，不支持飞书/QQ/Web
- ❌ 不能团队共享 LLM

**适合**：个人开发者快速体验。

### Server（服务端模式）

后台运行一个 Server 进程，CLI 通过 WebSocket 远程连接。同时启用飞书/QQ/Web 等渠道。

- ✅ Agent 常驻运行，开机自启
- ✅ 支持飞书/QQ/Web 多渠道同时接入
- ✅ Web 浏览器聊天界面
- ✅ 管理员配置 LLM Key，全团队共享使用
- ✅ 多个 CLI 客户端可同时连接

**适合**：团队使用、需要飞书/QQ 接入、需要 Web 界面的场景。

> 💡 **大多数团队应选 Server 模式。**

### Server 模式的服务管理

安装器会自动配置系统服务（无需 sudo）：

| 平台 | 服务方式 |
|------|----------|
| Linux | systemd --user（用户级服务） |
| macOS | launchd（LaunchAgent） |
| Windows | Startup 文件夹 / 计划任务 / nssm 服务 |

Server 启动命令：`xbot-cli serve`

### 安装器做了什么

1. 下载 `xbot-cli` 二进制到 `~/.local/bin/`（或你指定的路径）
2. 生成随机 admin token
3. 写入/更新 `~/.xbot/config.json`
4. Server 模式额外：安装系统服务 + 下载 Web UI

## 首次配置

安装完成后运行：

```bash
xbot-cli
```

### Setup 向导

首次运行会自动弹出 Setup 向导，分三步配置：

**第一步：LLM 配置**
1. 选择 LLM 提供商（OpenAI / Anthropic）
2. 输入 API Key（**必填**）
3. 输入 API 地址（默认 `https://api.openai.com/v1`，使用兼容服务时修改）
4. 输入模型名称（默认 `gpt-4o`）
5. Tavily 搜索 Key（可选，不填则无法使用网页搜索）

> ⚠️ **重要：输入框操作**：用方向键选中输入框 → 按 **Enter** 进入编辑 → 输入内容 → 按 **Enter** 确认。直接打字是不会生效的。

**第二步：环境配置**
- 沙箱模式（默认 `none`，Docker 用户选 `docker`）
- 记忆模式（默认 `flat`）

**第三步：外观**
- 配色方案（9 种可选）

配置完成后即可开始对话。随时可用 `/setup` 命令重新配置。

### 手动编辑配置

配置文件位于 `~/.xbot/config.json`，也可以直接编辑。详见 [配置参考](/xbot/configuration/)。

**OpenAI 或兼容 API 的最小配置：**

```json
{
  "llm": {
    "provider": "openai",
    "api_key": "sk-xxx",
    "model": "gpt-4o"
  }
}
```

**使用 OpenAI 兼容服务（DeepSeek、Qwen、Ollama 等）：**

```json
{
  "llm": {
    "provider": "openai",
    "api_key": "your-key",
    "base_url": "https://api.deepseek.com/v1",
    "model": "deepseek-chat"
  }
}
```

## 验证安装

```bash
# 查看版本
xbot-cli --version

# Server 模式检查服务状态
# Linux:
systemctl --user status xbot-server
# macOS:
launchctl list | grep xbot
```
