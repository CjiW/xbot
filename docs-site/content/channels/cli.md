---
title: "CLI Channel"
weight: 10
---

# CLI Channel

xbot 的终端交互界面，基于 [Bubble Tea](https://github.com/charmbracelet/bubbletea) TUI 框架构建，提供现代化的命令行聊天体验。

## 概述

CLI Channel 是 xbot 的默认渠道，允许用户直接在终端中与 AI Agent 进行交互。它非常适合：

- **开发调试** — 快速测试 Agent 行为和工具调用
- **本地使用** — 无需配置 IM 平台即可使用
- **CI/CD 集成** — 在自动化流程中调用 Agent

### 技术栈

| 组件 | 库 | 用途 |
|------|-----|------|
| TUI 框架 | [Bubble Tea](https://github.com/charmbracelet/bubbletea) | 终端界面状态管理 |
| 样式引擎 | [lipgloss](https://github.com/charmbracelet/lipgloss) | 样式定义与布局 |
| Markdown 渲染 | [glamour](https://github.com/charmbracelet/glamour) | 富文本渲染 |
| 输入组件 | [bubbles/textarea](https://github.com/charmbracelet/bubbles) | 多行输入框 |

## 安装与启动

### 一键安装

```bash
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/CjiW/xbot/master/scripts/install.sh | bash
```

```powershell
# Windows (PowerShell)
irm https://raw.githubusercontent.com/CjiW/xbot/master/scripts/install.ps1 | iex
```

安装脚本会自动检测平台并下载对应版本。也支持从 [GitHub Releases](https://github.com/CjiW/xbot/releases) 手动下载。

> **Windows 用户**：安装后如需使用 Shell 工具，建议使用 Windows Terminal 以获得最佳体验。Shell 工具在 none 沙箱模式下使用 PowerShell。

### 编译

```bash
# 在 xbot 项目根目录执行
go build -o xbot-cli ./cmd/xbot-cli

# 运行
./xbot-cli
```

### 首次配置

首次运行会自动进入配置引导：

```
╔══════════════════════════════════════════════════╗
║            🚀 xbot CLI 首次配置引导              ║
╚══════════════════════════════════════════════════╝

支持的 LLM 提供商：
  1. OpenAI (及兼容 API，如 DeepSeek、通义千问等)
  2. Anthropic (Claude)

选择提供商 (1/2) [1]:
API Key: sk-xxx
Base URL [https://api.openai.com/v1]:
模型名称 [gpt-4o]:
...
```

### 使用模式

CLI 支持三种使用模式：

```bash
# 交互模式 — 完整 TUI 界面
xbot-cli

# 单次执行模式 — 直接传入 prompt
xbot-cli "your prompt"

# 管道模式 — 从 stdin 读取
echo "explain this" | xbot-cli
```

### 命令行参数

| 参数 | 说明 |
|------|------|
| （无参数） | 恢复上次会话 |
| `--new` | 创建新会话 |
| `-p "text"` | 单次执行模式 |
| （直接文本） | 非交互模式 |

## 快捷键

| 快捷键 | 功能 |
|--------|------|
| `Enter` | 发送消息 |
| `Ctrl+Enter` / `Ctrl+J` | 换行 |
| `Tab` | 自动补全（`/` 命令，`@` 文件路径） |
| `↑` `↓` | 浏览输入历史 / 滚动消息 |
| `PgUp` `PgDn` | 翻页 |
| `Home` `End` | 跳到顶部 / 底部 |
| `Esc` | 取消 / 清空输入 |
| `Ctrl+C` | 中断当前操作 |
| `Ctrl+K` | 上下文编辑（按轮次截断对话历史） |
| `Ctrl+O` | 切换 tool summary 展开/折叠 |
| `Ctrl+E` | 切换长消息折叠 |
| `^` | 打开后台任务面板 |

> **提示**：消息发送后，输入框会暂时禁用，直到 Agent 完成回复。

## 斜杠命令

CLI 内置以下斜杠命令，输入 `/` 后按 `Tab` 可自动补全：

| 命令 | 说明 |
|------|------|
| `/settings` | 查看和修改配置 |
| `/setup` | 重新运行配置引导 |
| `/update` | 检查并安装更新 |
| `/new` | 创建新会话 |
| `/clear` | 清空当前会话消息 |
| `/compact` | 压缩对话上下文 |
| `/context` | 查看当前上下文状态 |
| `/model` | 查看/切换当前模型 |
| `/models` | 列出可用模型 |
| `/cancel` | 取消当前正在进行的请求 |
| `/search` | 搜索历史消息 |
| `/tasks` | 查看后台任务列表 |
| `/su` | 切换用户身份 |
| `/help` | 显示帮助信息 |
| `/exit` | 退出 CLI |

### /settings 命令

```
/settings                        — 查看所有设置项
/settings set <key> <value>      — 修改设置
```

可配置项：`llm_model`、`llm_base_url`、`context_mode`、`max_iterations`、`theme`

## 功能特性

### 流式输出

Agent 回复采用流式输出，实时显示生成内容：

```
🤖 Assistant (streaming...)
正在思考...
```

流式消息会以橙色边框高亮显示，完成后恢复正常样式。

### Markdown + Mermaid 渲染

支持完整的 Markdown 语法渲染：

- **代码块** — 语法高亮
- **列表** — 有序/无序列表
- **表格** — 对齐表格
- **标题** — 多级标题
- **链接** — 可点击链接（终端支持时）

````markdown
```python
def hello():
    print("Hello, xbot!")
```
````

此外还支持 **Mermaid** 图表渲染，可直接在终端中显示流程图、时序图等。

### 6 种配色主题

CLI 内置 6 种颜色主题，可通过 `/settings set theme <name>` 切换，适配不同终端背景和用户偏好。

### 后台任务

长时间运行的命令会自动转入后台执行，不阻塞对话。按 `^` 打开后台任务面板查看进度和结果。

### 消息搜索

使用 `/search` 命令可在当前会话的历史消息中搜索关键词，快速定位之前的内容。

### 内置 Skill / Agent 创建器

CLI 内置两个创建工具：

- **skill-creator** — 创建和管理 Skills（Markdown 定义的能力包，存储在 `~/.xbot/skills/`）
- **agent-creator** — 创建自定义 SubAgent 角色（存储在 `~/.xbot/agents/`）

### 进度显示

实时显示 Agent 执行状态：

```
⠋ 执行工具... (迭代 2)
  ⚙ Read file.go (150ms)
  ⚙ Grep pattern (89ms)

  🔄 code-reviewer: 审查代码质量
  ✅ tester: 测试通过
```

状态图标：

| 图标 | 含义 |
|------|------|
| `⠋` | 正在处理（spinner 动画） |
| `⚙` | 工具执行中 |
| `🔄` | 子 Agent 运行中 |
| `✅` | 子 Agent 完成 |
| `❌` | 子 Agent 出错 |

### 消息气泡样式

用户与 Agent 消息采用差异化视觉设计：

```
                    ┌──────────────────────────────┐
                    │ 👤 You              14:30:25 │
                    │ 你好，请帮我分析这段代码        │
                    └──────────────────────────────┘

┌──────────────────────────────────────────────────────┐
│ 🤖 Assistant                                 14:30:26 │
│                                                      │
│ 好的，我来帮你分析。首先需要了解代码的结构...          │
│                                                      │
│ ```go                                                │
│ func main() {                                        │
│     fmt.Println("Hello")                             │
│ }                                                    │
│ ```                                                  │
└──────────────────────────────────────────────────────┘
```

- **用户消息**：右对齐，蓝色背景
- **Agent 消息**：左对齐，绿色背景

### 时间戳显示

每条消息附带时间戳（格式：`HH:MM:SS`），便于追溯对话时序。

## 界面布局

```
┌──────────────────────────────────────────────────────────────────┐
│  🤖 xbot CLI                              Enter 发送 | Esc 退出  │
├──────────────────────────────────────────────────────────────────┤
│                                                                  │
│                      消息显示区域 (Viewport)                      │
│                                                                  │
│                    ┌────────────────────────┐                    │
│                    │ 👤 You          14:30   │                    │
│                    │ 用户消息内容...         │                    │
│                    └────────────────────────┘                    │
│                                                                  │
│  ┌────────────────────────────────────────────────────────────┐  │
│  │ 🤖 Assistant                                         14:30 │  │
│  │ Agent 回复内容...                                          │  │
│  └────────────────────────────────────────────────────────────┘  │
│                                                                  │
├──────────────────────────────────────────────────────────────────┤
│  ✓ 就绪                                                          │
├──────────────────────────────────────────────────────────────────┤
│  💬 ┃ 输入消息，Enter 发送...                                     │
│     ┃                                                           │
└──────────────────────────────────────────────────────────────────┘
```

### 区域说明

| 区域 | 说明 |
|------|------|
| 标题栏 | 显示程序名称和快捷键提示 |
| 消息区 | 可滚动的对话历史，支持鼠标滚轮 |
| 状态栏 | 显示当前状态（就绪/思考中/执行工具） |
| 输入框 | 多行文本输入，支持粘贴 |

## 配置说明

CLI 使用 `~/.xbot/config.json` 管理配置，首次运行会自动引导创建。

### 配置文件示例

```json
{
  "llm": {
    "provider": "openai",
    "api_key": "sk-xxx",
    "base_url": "https://api.openai.com/v1",
    "model": "gpt-4o"
  },
  "sandbox": {
    "mode": "none"
  },
  "agent": {
    "memory_provider": "flat"
  }
}
```

### 配置项说明

| 配置项 | 说明 | 默认值 |
|--------|------|--------|
| `llm.provider` | LLM 提供商：`openai` / `anthropic` | `openai` |
| `llm.api_key` | API 密钥（也可用 `XBOT_LLM_API_KEY` 环境变量） | — |
| `llm.base_url` | API 地址（兼容 OpenAI 格式的第三方服务） | `https://api.openai.com/v1` |
| `llm.model` | 模型名称 | `gpt-4o` |
| `sandbox.mode` | 沙箱模式：`none`（推荐 CLI）/ `docker` | `none` |
| `agent.memory_provider` | 记忆模式：`flat`（无需 embedding）/ `letta` | `flat` |

### 兼容第三方 API

CLI 默认使用 OpenAI 格式，兼容所有 OpenAI 兼容 API：

| 服务 | base_url | model |
|------|----------|-------|
| OpenAI | `https://api.openai.com/v1` | `gpt-4o` |
| DeepSeek | `https://api.deepseek.com/v1` | `deepseek-chat` |
| 通义千问 | `https://dashscope.aliyuncs.com/compatible-mode/v1` | `qwen-max` |
| Anthropic | `https://api.anthropic.com` | `claude-sonnet-4-20250514` |

### 记忆模式

| 模式 | 说明 | 依赖 |
|------|------|------|
| `flat` | 全量上下文注入，工具始终可用 | 无额外依赖（推荐） |
| `letta` | 分层记忆，search tool + archival | 需要 embedding 服务 |

## 故障排查

### 终端显示异常

**症状**：颜色显示不正确、边框乱码

**解决方案**：
1. 确保终端支持 256 色：`echo $TERM`（应为 `xterm-256color` 或类似）
2. 尝试设置环境变量：`export TERM=xterm-256color`
3. 使用现代终端模拟器（如 iTerm2、Alacritty、Windows Terminal）

### 中文显示问题

**症状**：中文字符显示为方框或乱码

**解决方案**：
1. 确保终端字体支持中文（如 Nerd Font、Sarasa Mono）
2. 检查系统 locale 设置：`locale`

### 输入无响应

**症状**：按键无反应

**解决方案**：
1. 检查状态栏是否显示「思考中...」— 此时输入框暂时禁用
2. 按 `Ctrl+C` 强制退出后重新启动
3. 检查 LLM API 是否正常响应

### Markdown 渲染异常

**症状**：代码块或表格显示错乱

**解决方案**：
1. 确保终端窗口足够宽（建议 80 列以上）
2. 尝试调整终端字体大小

## 与其他渠道对比

| 特性 | CLI | Feishu | Web |
|------|-----|--------|-----|
| 安装复杂度 | 低 | 中 | 中 |
| 多媒体支持 | 无 | 完整 | 完整 |
| 多用户支持 | 否 | 是 | 是 |
| 适用场景 | 开发调试 | 团队协作 | 通用 |

## 相关文档

- [Bubble Tea 教程](https://github.com/charmbracelet/bubbletea#tutorial)
- [lipgloss 样式指南](https://github.com/charmbracelet/lipgloss)
- [glamour Markdown 渲染](https://github.com/charmbracelet/glamour)
