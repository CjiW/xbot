---
title: "CLI Channel"
weight: 10
---

# CLI Channel

xbot 的终端交互界面，基于 [Bubble Tea](https://github.com/charmbracelet/bubbletea) TUI 框架构建。不只是聊天窗口——是一个完整的 Agent 工作台。

## 快速开始

```bash
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/CjiW/xbot/master/scripts/install.sh | bash

# Windows (PowerShell)
irm https://raw.githubusercontent.com/CjiW/xbot/master/scripts/install.ps1 | iex
```

### 三种使用模式

```bash
xbot-cli                # 交互模式 — 完整 TUI
xbot-cli "your prompt"  # 单次执行
echo "prompt" | xbot-cli # 管道模式
```

首次运行自动进入配置引导，支持 OpenAI / Anthropic / 任何 OpenAI 兼容 API。

## 核心特性

### 实时流式输出

Token 级别的流式渲染——每一帧都实时更新，带闪烁光标效果。工具调用进度内联显示，推理/思考链直接可见。

{{< figure src="img/cli/streaming.gif" alt="Streaming output" width="100%" >}}

**渲染架构：** 三级缓存系统确保高性能——流式消息只重新渲染当前行，历史消息完全跳过。即使在密集的工具调用场景下也能保持流畅。

### SubAgent 彩色树

每个 SubAgent 角色获得基于名称哈希的唯一颜色。进度面板以树状结构展示正在运行的 Agent，带波浪动画和实时计时。

{{< figure src="img/cli/subagents.gif" alt="SubAgent color tree" width="100%" >}}

### 多订阅 & 模型分级

管理多个 API Key / Provider，随时切换：

- **`Ctrl+P`** — 快速切换订阅（Quick Switch 面板）
- **`Ctrl+N`** — 循环切换模型
- **`/settings`** — 完整设置面板，添加/删除/重命名订阅

**模型分级**（SubAgent 成本优化）：

| 等级 | 用途 | 示例 |
|------|------|------|
| Vanguard | 高强度任务（代码编写、架构设计） | Claude Opus, GPT-4o |
| Balance | 均衡任务（分析、审查） | Claude Sonnet, GPT-4o-mini |
| Swift | 轻量任务（搜索、格式化） | GPT-4o-mini, Haiku |

通过 `/settings` 为每个等级分配不同模型，SubAgent 根据任务类型自动选用。

{{< figure src="img/cli/quick-switch.png" alt="Quick Switch panel" width="100%" >}}

### Rewind & 文件回滚

`/rewind` 回退到任意历史轮次。不只是对话上下文——文件也会恢复到当时的 checkpoint 状态。

回滚完成后显示恢复/创建/删除的文件清单。

### 消息队列

Agent 处理中？继续打字。消息自动排队，Agent 完成后按顺序发送。

```
📬 2 queued  — ↑ recall · Esc cancel
```

### 9 种主题

| 主题 | 风格 |
|------|------|
| `midnight` | Material Design Indigo（默认） |
| `ocean` | 深海蓝 + 青色高亮 |
| `forest` | 北欧森林绿 |
| `sunset` | 暖琥珀 + 珊瑚色 |
| `rose` | 柔和粉紫 |
| `mono` | 纯灰阶 + 红色点缀（极客风） |
| `nord` | Nord 配色（极地冷调） |
| `dracula` | 经典暗紫 |
| `catppuccin` | Catppuccin Mocha（柔粉暗色） |

通过 `/settings` 或 `Ctrl+N` 切换。所有主题均为暗色终端优化。

### Markdown + Mermaid + 语法高亮

完整 [glamour](https://github.com/charmbracelet/glamour) 渲染：代码块、表格、列表、标题。Mermaid 图表自动转换为 ASCII art。

{{< figure src="img/cli/mermaid.png" alt="Mermaid diagram in terminal" width="100%" >}}

### 后台任务面板

长时间运行的命令自动转入后台。按 `^` 打开管理面板——查看日志、终止任务。

{{< figure src="img/cli/bg-tasks.gif" alt="Background task panel" width="100%" >}}

### 上下文管理

| 功能 | 说明 |
|------|------|
| `/context` | 查看当前上下文状态（token 用量、消息数） |
| `/compact` | 压缩对话上下文 |
| `Ctrl+E` | 批量折叠/展开长消息（>20行） |
| `Ctrl+O` | 展开/折叠工具调用摘要 |
| `/rewind` | 回退到历史轮次 + 文件回滚 |

### 交互式问答面板

Agent 调用 AskUser 工具时，自动弹出交互面板：

- 多题切换（←→ / Tab）
- 选项高亮 + 空格/回车选择
- 自由文本输入
- "Other" 自定义选项

{{< figure src="img/cli/askuser.png" alt="AskUser panel" width="100%" >}}

### 文件引用

输入 `@` 触发文件路径补全（Tab 循环 / Enter 确认）。引用的文件作为附件随消息发送。

### 消息搜索

`/search` 进入搜索模式，大小写不敏感匹配历史消息。↑↓ 导航结果，自动滚动到匹配位置。

### 国际化

支持 3 种语言：中文（默认）、English、日本語。通过 `/settings` 切换。所有 UI 文本、设置面板、帮助信息均完整翻译。

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
| `Ctrl+O` | 切换 tool summary 展开/折叠 |
| `Ctrl+E` | 切换长消息折叠 |
| `Ctrl+P` | 快速切换订阅 |
| `Ctrl+N` | 循环切换模型 |
| `^` | 后台任务面板 |

## 斜杠命令

| 命令 | 说明 |
|------|------|
| `/settings` | 查看和修改配置（模型、主题、订阅管理等） |
| `/setup` | 重新运行配置引导 |
| `/update` | 检查并安装更新 |
| `/new` | 创建新会话 |
| `/clear` | 清空当前会话消息 |
| `/compact` | 压缩对话上下文 |
| `/context` | 查看当前上下文状态 |
| `/rewind` | 回退到历史轮次（含文件回滚） |
| `/model` | 查看/切换当前模型 |
| `/models` | 列出可用模型 |
| `/cancel` | 取消当前请求 |
| `/search` | 搜索历史消息 |
| `/tasks` | 查看后台任务列表 |
| `/su` | 切换用户身份 |
| `/usage` | 查看 token 用量统计 |
| `/help` | 显示帮助信息 |
| `/exit` | 退出 CLI |

## 配置

CLI 使用 `~/.xbot/config.json` 管理配置。首次运行自动引导创建。

### 兼容第三方 API

| 服务 | Provider | Base URL | Model |
|------|----------|----------|-------|
| OpenAI | openai | `https://api.openai.com/v1` | `gpt-4o` |
| Anthropic | anthropic | `https://api.anthropic.com` | `claude-sonnet-4-20250514` |
| DeepSeek | openai | `https://api.deepseek.com/v1` | `deepseek-chat` |
| 通义千问 | openai | `https://dashscope.aliyuncs.com/compatible-mode/v1` | `qwen-max` |
| Ollama (local) | openai | `http://localhost:11434/v1` | `qwen2.5-coder` |

## 界面布局

```
┌──────────────────────────────────────────────────────────────────┐
│  🤖 xbot CLI                              Enter 发送 | Esc 退出  │
├──────────────────────────────────────────────────────────────────┤
│                                                                  │
│                      消息显示区域 (Viewport)                      │
│              支持 Markdown / Mermaid / 语法高亮渲染                │
│                                                                  │
│  ┌────────────────────────────────────────────────────────────┐  │
│  │ 🤖 Assistant                                         14:30 │  │
│  │                                                          │  │
│  │  # Iteration 1                                           │  │
│  │    ✓ Read main.go (12ms)                                 │  │
│  │    ◉ Grep "func main" (running...)                       │  │
│  │                                                          │  │
│  │  Stream content here...▋                                 │  │
│  │    ├── 🎨 code-reviewer (running...)                     │  │
│  │    └── 🔍 explore (done)                                 │  │
│  └────────────────────────────────────────────────────────────┘  │
│                                                                  │
├──────────────────────────────────────────────────────────────────┤
│  ✓ Ready                                          [1 task 1 ag] │
├──────────────────────────────────────────────────────────────────┤
│  💬 ┃ Input message...                                           │
│     ┃                                                            │
└──────────────────────────────────────────────────────────────────┘
```

## 技术栈

| 组件 | 库 | 用途 |
|------|-----|------|
| TUI 框架 | [Bubble Tea v2](https://github.com/charmbracelet/bubbletea) | 终端界面状态管理 |
| 样式引擎 | [lipgloss v2](https://github.com/charmbracelet/lipgloss) | 样式定义与布局 |
| Markdown 渲染 | [glamour](https://github.com/charmbracelet/glamour) | 富文本渲染 |
| 输入组件 | [bubbles/textarea](https://github.com/charmbracelet/bubbles) | 多行输入框 |

## 命令行参数

| 参数 | 说明 |
|------|------|
| （无参数） | 恢复上次会话 |
| `--new` | 创建新会话 |
| `-p "text"` | 单次执行模式 |
| （直接文本） | 非交互模式 |

## 配置文件

CLI 使用 `~/.xbot/config.json` 管理配置，首次运行自动引导创建。

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

### 记忆模式

| 模式 | 说明 | 依赖 |
|------|------|------|
| `flat` | 全量上下文注入，工具始终可用 | 无额外依赖（推荐） |
| `letta` | 分层记忆，search tool + archival | 需要 embedding 服务 |

通过 `/settings` 或直接编辑 `~/.xbot/config.json` 修改。

## 故障排查

### 终端显示异常（颜色/边框乱码）

1. 确保终端支持 256 色：`echo $TERM`（应为 `xterm-256color` 或类似）
2. 尝试设置环境变量：`export TERM=xterm-256color`
3. 使用现代终端模拟器（如 iTerm2、Alacritty、Windows Terminal）

### 中文显示问题（方框/乱码）

1. 确保终端字体支持中文（如 Nerd Font、Sarasa Mono）
2. 检查系统 locale 设置：`locale`

### 输入无响应

1. 检查状态栏是否显示「思考中...」— 此时输入框暂时禁用
2. 按 `Ctrl+C` 强制退出后重新启动
3. 检查 LLM API 是否正常响应

### Markdown 渲染异常

1. 确保终端窗口足够宽（建议 80 列以上）
2. 尝试调整终端字体大小
