# CLI Channel

xbot 的终端交互界面，基于 Bubble Tea TUI 框架构建，提供现代化的命令行聊天体验。

## 概述

CLI Channel 是 xbot 的内置渠道之一，允许用户直接在终端中与 AI Agent 进行交互。它非常适合：

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

### 开发模式

```bash
# 在 xbot 项目根目录执行
go run ./cmd/cli
```

### 编译运行

```bash
# 编译
go build -o xbot-cli ./cmd/cli

# 运行
./xbot-cli
```

### 通过 Makefile

```bash
# 开发模式（热重载）
make dev-cli
```

## 快捷键

| 快捷键 | 功能 |
|--------|------|
| `Enter` | 发送消息 |
| `Esc` | 退出程序 |
| `Ctrl+C` | 强制退出 |

> **提示**：消息发送后，输入框会暂时禁用，直到 Agent 完成回复。

## 功能特性

### 流式输出

Agent 回复采用流式输出，实时显示生成内容：

```
🤖 Assistant (streaming...)
正在思考...
```

流式消息会以橙色边框高亮显示，完成后恢复正常样式。

### Markdown 渲染

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

CLI Channel 目前使用默认配置，无需额外设置。

### 未来可扩展配置项

```env
# CLI 渠道配置（规划中）
CLI_ENABLED=true
CLI_THEME=dark          # dark | light
CLI_MAX_HISTORY=1000    # 消息历史上限
```

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

## 开发指南

### 源码结构

```
channel/cli.go
├── CLIChannel          — 渠道实现（Channel 接口）
├── cliModel            — Bubble Tea 状态模型
├── cliMessage          — 消息结构
└── 样式定义            — lipgloss 样式
```

### 扩展样式

修改 `View()` 和 `updateViewportContent()` 函数中的样式定义：

```go
// 自定义用户消息气泡
userBubbleStyle := lipgloss.NewStyle().
    Background(lipgloss.Color("#your-color")).
    Foreground(lipgloss.Color("#ffffff")).
    Padding(0, 1).
    Border(lipgloss.RoundedBorder()).
    BorderForeground(lipgloss.Color("#border-color"))
```

### 添加新功能

1. 在 `cliModel` 结构体中添加状态字段
2. 在 `Update()` 函数中处理新的消息类型
3. 在 `View()` 函数中渲染新 UI 元素

## 相关文档

- [Bubble Tea 教程](https://github.com/charmbracelet/bubbletea#tutorial)
- [lipgloss 样式指南](https://github.com/charmbracelet/lipgloss)
- [glamour Markdown 渲染](https://github.com/charmbracelet/glamour)
