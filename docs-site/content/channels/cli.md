---
title: "CLI"
weight: 21
---

# CLI 渠道

CLI 是 xbot 的默认渠道，一个功能完整的终端 UI（TUI），基于 [Bubble Tea](https://github.com/charmbracelet/bubbletea) 构建。

## 两种模式

### Standalone 模式（本地）

CLI 直接在本地运行 Agent，所有处理都在你的电脑上完成。

```bash
xbot-cli                # 启动交互式 TUI
xbot-cli "你的问题"      # 一次性问答
echo "问题" | xbot-cli   # 管道模式
```

### Remote 模式（远程）

CLI 通过 WebSocket 连接到 Server，Agent 运行在服务端。

```bash
# 使用安装器自动配置的连接
xbot-cli

# 手动指定 Server 地址
xbot-cli --server ws://your-server:8082
```

Remote 模式下，你的 CLI 和其他用户的 CLI / 飞书 / Web 共享同一个 Agent 实例。

## Setup 向导

首次运行会自动弹出配置向导。也可以随时输入 `/setup` 重新配置。

> ⚠️ **输入框操作技巧**：用方向键选中输入框 → 按 **Enter** 进入编辑模式 → 此时才能输入内容 → 再按 **Enter** 确认。直接打字是不会生效的。

## 常用命令

在 TUI 中输入以下斜杠命令：

| 命令 | 说明 |
|------|------|
| `/setup` | 重新运行配置向导 |
| `/settings` | 打开设置面板（沙箱模式、记忆模式等） |
| `/channel` | **频道配置面板** — 可视化配置 Web/飞书/QQ/NapCat 频道 |
| `/model` | 查看/切换当前 LLM 模型 |
| `/models` | 列出可用的模型 |
| `/new` | 开始新对话 |
| `/rewind` | 回退对话 |
| `/clear` | 清空对话和记忆 |

## 主题

CLI 支持 9 种配色方案，在 Setup 向导或 `/settings` 中切换：

`midnight` · `dracula` · `catppuccin` · `nord` · `gruvbox` · `tokyo` · `rose` · `rosepine` · `default`

## 键盘快捷键

| 快捷键 | 说明 |
|--------|------|
| `Ctrl+C` | 取消当前生成 |
| `Ctrl+D` | 退出 |
| `Enter` | 发送消息（或在输入框中进入/确认编辑） |
| `↑` / `↓` | 浏览历史消息 |
