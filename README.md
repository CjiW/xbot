# 🤖 xbot

一个可扩展的 AI Agent 助手，基于 Go 构建，采用消息总线 + 插件化架构。支持飞书等 IM 渠道接入，具备工具调用、长期记忆、技能系统和定时任务等能力。

## ✨ 特性

- **多渠道接入** — 消息总线架构，目前支持飞书，易于扩展其他 IM 平台
- **丰富的内置工具** — Shell 执行、文件读写编辑、Glob/Grep 搜索、Web 搜索、定时任务
- **技能系统 (Skills)** — 可热加载的 Markdown 技能包，动态扩展 Agent 能力
- **双层记忆** — MEMORY.md（长期事实记忆）+ HISTORY.md（可检索的事件日志），自动合并归档
- **MCP 协议支持** — 通过 `mcp.json` 配置接入 [MCP](https://modelcontextprotocol.io/) 工具服务器
- **提示词外置** — 系统提示词模板化（Go template），支持热加载，无需重启即可调整
- **KV-Cache 优化** — 精心设计的上下文拼接顺序，最大化 LLM 推理缓存命中率
- **多 LLM 后端** — 支持 OpenAI 兼容 API（DeepSeek 等）及 CodeBuddy

## 🏗️ 架构

```
┌─────────┐     ┌────────────┐     ┌───────┐     ┌─────┐     ┌───────┐
│  飞书    │────▶│ MessageBus │────▶│ Agent │────▶│ LLM │     │ Tools │
│ Channel  │◀────│   (消息总线) │◀────│       │◀────│     │     │       │
└─────────┘     └────────────┘     │       │────▶│Shell│
                                   │       │────▶│Glob │
┌─────────┐                        │       │────▶│Grep │
│  更多    │                        │       │────▶│Read │
│ Channel  │                        │       │────▶│Edit │
└─────────┘                        │       │────▶│Cron │
                                   │       │────▶│Skill│
                                   │       │────▶│ Web │
                                   │       │────▶│ MCP │
                                   └───────┘     └─────┘
                                      │
                              ┌───────┴───────┐
                              │   Memory      │
                              │ MEMORY.md     │
                              │ HISTORY.md    │
                              └───────────────┘
```

## 📦 项目结构

```
xbot/
├── main.go              # 入口：初始化各组件并启动
├── prompt.md            # 系统提示词模板（可热加载）
├── Makefile             # 构建/运行/测试命令
├── .env.example         # 环境变量配置模板
│
├── agent/               # Agent 核心引擎
│   ├── agent.go         # Agent 主循环、工具调用迭代、SubAgent
│   ├── context.go       # 上下文构建、提示词加载与渲染
│   └── memory.go        # 双层记忆系统（合并、归档）
│
├── bus/                 # 消息总线
│   └── bus.go           # Inbound/Outbound 消息通道
│
├── channel/             # IM 渠道
│   ├── channel.go       # Channel 接口定义
│   ├── dispatcher.go    # 消息分发器
│   └── feishu.go        # 飞书渠道实现
│
├── llm/                 # LLM 客户端
│   ├── interface.go     # LLM 接口与类型定义
│   ├── openai.go        # OpenAI 兼容 API 客户端
│   ├── codebuddy.go     # CodeBuddy 客户端
│   └── types.go         # 消息、工具调用等数据结构
│
├── tools/               # 内置工具
│   ├── interface.go     # Tool 接口、Registry 注册表
│   ├── shell.go         # Shell 命令执行
│   ├── glob.go          # 文件 Glob 搜索
│   ├── grep.go          # 文件内容 Grep 搜索
│   ├── read.go          # 文件读取
│   ├── edit.go          # 文件编辑（创建/替换/行编辑/正则）
│   ├── web_search.go    # Web 搜索（Tavily API）
│   ├── cron.go          # 定时任务调度
│   ├── skill.go         # 技能管理工具
│   ├── skill_store.go   # 技能存储与加载
│   ├── mcp.go           # MCP 协议工具桥接
│   └── subagent.go      # SubAgent 子代理
│
├── session/             # 会话管理
│   └── session.go       # 会话持久化（JSONL）
│
├── config/              # 配置加载
│   └── config.go        # 环境变量 → 结构体
│
└── logger/              # 日志
    └── logger.go        # logrus 封装
```

## 🚀 快速开始

### 前置要求

- Go 1.25+
- 飞书开放平台应用（用于飞书渠道）
- LLM API Key（DeepSeek / OpenAI 兼容 / CodeBuddy）

### 安装与运行

```bash
# 克隆仓库
git clone https://github.com/CjiW/xbot.git
cd xbot

# 配置环境变量
cp .env.example .env
# 编辑 .env，填写 LLM API Key、飞书应用凭证等

# 构建并运行
make build
./xbot

# 或直接开发模式运行
make dev
```

### 使用 systemd 部署（推荐）

```bash
# 创建 service 文件
sudo vim /etc/systemd/system/xbot.service
```

```ini
[Unit]
Description=xbot AI Assistant
After=network.target

[Service]
Type=simple
WorkingDirectory=/path/to/workdir
ExecStart=/usr/local/bin/xbot
Restart=always
RestartSec=5
EnvironmentFile=/path/to/workdir/.env

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now xbot
```

## ⚙️ 配置

所有配置通过环境变量或 `.env` 文件设置：

| 变量 | 说明 | 默认值 |
|------|------|--------|
| `LLM_PROVIDER` | LLM 提供商（`openai` / `codebuddy`） | `openai` |
| `LLM_BASE_URL` | API 地址 | — |
| `LLM_API_KEY` | API 密钥 | — |
| `LLM_MODEL` | 模型名称 | `deepseek-chat` |
| `FEISHU_ENABLED` | 启用飞书渠道 | `false` |
| `FEISHU_APP_ID` | 飞书应用 ID | — |
| `FEISHU_APP_SECRET` | 飞书应用密钥 | — |
| `FEISHU_ALLOW_FROM` | 允许的用户 ID（逗号分隔） | 空（允许所有） |
| `AGENT_MAX_ITERATIONS` | 单次对话最大工具调用轮数 | `20` |
| `AGENT_MEMORY_WINDOW` | 上下文保留的历史消息数 | `50` |
| `WORK_DIR` | 工作目录 | `.` |
| `PROMPT_FILE` | 自定义提示词模板路径 | 空（使用内置） |
| `LOG_LEVEL` | 日志级别 | `info` |

## 🧠 记忆系统

xbot 采用双层记忆架构：

- **MEMORY.md**（长期记忆）— 持续更新的事实性知识，如用户偏好、项目信息、待办事项。每次对话时注入系统提示词。
- **HISTORY.md**（历史日志）— 按时间追加的事件记录，支持 Grep 检索。用于回溯过去发生的事情。

当会话消息超过 `AGENT_MEMORY_WINDOW` 时，Agent 自动触发异步记忆合并：LLM 将旧消息摘要写入 MEMORY.md 和 HISTORY.md，然后释放上下文空间。

## 🔧 技能系统

技能（Skill）是可热加载的 Markdown 文件，激活后注入系统提示词，动态扩展 Agent 能力。

```
.xbot/skills/
└── my-skill/
    ├── SKILL.md          # 技能定义（YAML frontmatter + Markdown 指令）
    ├── scripts/          # 可选：脚本文件
    └── references/       # 可选：参考资料
```

SKILL.md 格式：

```markdown
---
name: my-skill
description: 技能简介
---

（Markdown 正文，激活后加载到系统提示词中）
```

通过对话中的 Skill 工具管理：创建、激活、停用、删除。

## 🔌 MCP 支持

在工作目录下创建 `mcp.json` 配置 MCP 工具服务器：

```json
{
  "mcpServers": {
    "server-name": {
      "command": "npx",
      "args": ["-y", "@some/mcp-server"],
      "env": {
        "API_KEY": "xxx"
      }
    }
  }
}
```

启动时自动连接并注册 MCP 工具到 Agent 工具集。

## 📝 命令

对话中可使用的斜杠命令：

| 命令 | 说明 |
|------|------|
| `/new` | 归档记忆并重置会话 |
| `/help` | 显示帮助信息 |

## 📄 License

MIT
