# 🤖 xbot

一个可扩展的 AI Agent 助手，基于 Go 构建，采用消息总线 + 插件化架构。支持飞书等 IM 渠道接入，具备工具调用、长期记忆、技能系统和定时任务等能力。

## ✨ 特性

- **多渠道接入** — 消息总线架构，目前支持飞书，易于扩展其他 IM 平台
- **丰富的内置工具** — Shell 执行、文件读写编辑、Glob/Grep 搜索、Web 搜索、定时任务、飞书卡片构建
- **技能系统 (Skills)** — 可热加载的 Markdown 技能包，动态扩展 Agent 能力
- **双层记忆** — SQLite 持久化的长期记忆 + 可检索的事件日志，自动合并归档
- **用户画像** — 自动记录用户偏好和沟通风格，跨会话持久化
- **多租户** — 基于 channel + chatID 的租户隔离，支持多群组/多用户独立会话
- **飞书交互卡片** — 渐进式卡片构建系统，支持表格、图表、表单、多列布局等丰富组件
- **MCP 协议支持** — 通过 `mcp.json` 配置接入 [MCP](https://modelcontextprotocol.io/) 工具服务器
- **提示词外置** — 系统提示词模板化（Go template），支持热加载，无需重启即可调整
- **KV-Cache 优化** — 精心设计的上下文拼接顺序，最大化 LLM 推理缓存命中率
- **多 LLM 后端** — 支持 OpenAI 兼容 API（DeepSeek 等）及 CodeBuddy

## 🏗️ 架构

```
┌─────────┐     ┌────────────┐     ┌───────┐     ┌─────────┐
│  飞书    │────▶│ MessageBus │────▶│ Agent │────▶│   LLM   │
│ Channel  │◀────│   (消息总线) │◀────│       │◀────│         │
└─────────┘     └────────────┘     │       │     └─────────┘
                                   │       │
┌─────────┐                        │       │────▶ Tools
│  更多    │                        │       │     ├─ Shell
│ Channel  │                        │       │     ├─ Glob / Grep / Read / Edit
└─────────┘                        │       │     ├─ WebSearch
                                   │       │     ├─ Cron (定时任务)
                                   │       │     ├─ Skill (技能管理)
                                   │       │     ├─ Card (飞书卡片)
                                   │       │     ├─ ChatHistory
                                   │       │     ├─ UserProfile / SelfProfile
                                   │       │     ├─ ManageTools
                                   │       │     └─ MCP (外部工具)
                                   └───┬───┘
                                       │
                               ┌───────┴───────┐
                               │    SQLite     │
                               │  (多租户存储)  │
                               │ Sessions      │
                               │ Memory        │
                               │ UserProfiles  │
                               └───────────────┘
```

## 📦 项目结构

```
xbot/
├── main.go                  # 入口：初始化各组件并启动
├── prompt.md                # 系统提示词模板（Go template，可热加载）
├── Makefile                 # 构建 / 测试 / CI 命令
├── .env.example             # 环境变量配置模板
├── .github/workflows/ci.yml # GitHub Actions CI（lint + build + test）
│
├── agent/                   # Agent 核心引擎
│   ├── agent.go             # 主循环、工具调用迭代、消息发送
│   ├── context.go           # 上下文构建、提示词加载与渲染
│   └── memory.go            # 记忆合并与归档
│
├── bus/                     # 消息总线
│   └── bus.go               # Inbound / Outbound 消息通道
│
├── channel/                 # IM 渠道
│   ├── channel.go           # Channel 接口定义
│   ├── dispatcher.go        # 消息分发器
│   ├── feishu.go            # 飞书渠道实现（消息收发、卡片回调）
│   └── feishu_token.go      # 飞书 Token 管理
│
├── llm/                     # LLM 客户端
│   ├── interface.go         # LLM 接口与类型定义
│   ├── openai.go            # OpenAI 兼容 API 客户端
│   ├── codebuddy.go         # CodeBuddy 客户端
│   ├── types.go             # 消息、工具调用等数据结构
│   └── mock.go              # 测试用 Mock 客户端
│
├── tools/                   # 内置工具
│   ├── interface.go         # Tool 接口、Registry 注册表
│   ├── shell.go             # Shell 命令执行
│   ├── glob.go              # 文件 Glob 搜索
│   ├── grep.go              # 文件内容 Grep 搜索
│   ├── read.go              # 文件读取
│   ├── edit.go              # 文件编辑（创建 / 替换 / 行编辑 / 正则）
│   ├── web_search.go        # Web 搜索（Tavily API）
│   ├── cron.go              # 定时任务调度
│   ├── skill.go             # 技能管理工具
│   ├── skill_store.go       # 技能存储与加载
│   ├── card_builder.go      # 飞书卡片构建器（Session / Element 模型）
│   ├── card_tools.go        # 卡片工具集（create / add / preview / send）
│   ├── chat_history.go      # 聊天历史查询
│   ├── user_profile.go      # 用户画像 & Bot 自画像
│   ├── manage_tools.go      # 动态工具 / MCP / 技能管理
│   ├── mcp.go               # MCP 协议工具桥接
│   ├── notify.go            # 进度通知（已由自动通知机制替代）
│   └── subagent.go          # SubAgent 子代理
│
├── session/                 # 会话管理
│   ├── session.go           # 会话持久化
│   ├── multitenant.go       # 多租户会话管理器
│   ├── tenant.go            # 租户会话（消息历史 + 记忆）
│   └── memory.go            # 租户记忆读写
│
├── storage/                 # 存储层
│   ├── migrate.go           # 数据库迁移
│   └── sqlite/              # SQLite 实现
│       ├── db.go            # 数据库连接与初始化
│       ├── session.go       # 会话存储
│       ├── memory.go        # 记忆存储
│       ├── tenant.go        # 租户存储
│       └── user_profile.go  # 用户画像存储
│
├── config/                  # 配置加载
│   └── config.go            # 环境变量 → 结构体
│
├── logger/                  # 日志
│   └── logger.go            # logrus 封装
│
└── pprof/                   # 性能分析
    └── pprof.go             # pprof HTTP 服务器（可选启用）
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

### Makefile 命令

```bash
make fmt      # 格式化代码
make lint     # golangci-lint 检查
make test     # 运行测试（-race + 覆盖率）
make build    # 编译二进制
make run      # 编译并运行
make dev      # go run 开发模式
make clean    # 清理构建产物
make ci       # 本地模拟完整 CI（lint → build → test）
```

### 使用 systemd 部署

```bash
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
| `FEISHU_ENCRYPT_KEY` | 飞书事件加密密钥 | — |
| `FEISHU_VERIFICATION_TOKEN` | 飞书验证 Token | — |
| `FEISHU_ALLOW_FROM` | 允许的用户 ID（逗号分隔） | 空（允许所有） |
| `AGENT_MAX_ITERATIONS` | 单次对话最大工具调用轮数 | `20` |
| `AGENT_MEMORY_WINDOW` | 上下文保留的历史消息数 | `50` |
| `WORK_DIR` | 工作目录 | `.` |
| `PROMPT_FILE` | 自定义提示词模板路径 | 空（使用内置） |
| `LOG_LEVEL` | 日志级别 | `info` |
| `LOG_FORMAT` | 日志格式（`text` / `json`） | `json` |
| `PPROF_ENABLE` | 启用 pprof 性能分析 | `false` |
| `PPROF_HOST` | pprof 监听地址 | `localhost` |
| `PPROF_PORT` | pprof 监听端口 | `6060` |
| `SERVER_HOST` | 服务监听地址 | `0.0.0.0` |
| `SERVER_PORT` | 服务监听端口 | `8080` |

## 🧠 记忆系统

xbot 采用双层记忆架构，基于 SQLite 多租户存储：

- **MEMORY**（长期记忆）— 持续更新的事实性知识，如用户偏好、项目信息、待办事项。每次对话时注入系统提示词。
- **HISTORY**（历史日志）— 按时间追加的事件记录，支持检索。用于回溯过去发生的事情。

当会话消息超过 `AGENT_MEMORY_WINDOW` 时，Agent 自动触发异步记忆合并：LLM 将旧消息摘要写入 MEMORY 和 HISTORY，然后释放上下文空间。

### 用户画像

Agent 可通过 `update_user_profile` 工具记录对用户的观察（沟通风格、偏好等），通过 `update_self_profile` 更新自身画像。画像跨会话持久化，帮助 Agent 提供个性化服务。

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
description: 技能简介（用于自动匹配和发现）
---

（Markdown 正文，激活后加载到系统提示词中）
```

通过对话中的 Skill 工具管理：创建、激活、停用、删除。Agent 会根据对话主题自动匹配并激活相关技能。

## 🎴 飞书交互卡片

xbot 内置渐进式卡片构建系统，通过工具调用逐步构建复杂的飞书交互卡片：

```
card_create → card_add_content / card_add_interactive / card_add_container → card_preview → card_send
```

支持的组件：

| 类别 | 组件 |
|------|------|
| **展示** | Markdown、文本、图片、分割线、表格、图表、人员 |
| **交互** | 按钮、输入框、下拉选择、人员选择、日期/时间选择器、复选框 |
| **布局** | 多列布局、表单、折叠面板、可点击容器 |

卡片工具按需动态注册：只有调用 `card_create` 后才会注册其余卡片工具，发送完成后自动注销。

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

启动时自动连接并注册 MCP 工具到 Agent 工具集。可通过 `ManageTools` 工具在运行时动态管理。

## 📝 命令

对话中可使用的斜杠命令：

| 命令 | 说明 |
|------|------|
| `/new` | 归档记忆并重置会话 |
| `/help` | 显示帮助信息 |

## 🔄 CI

项目使用 GitHub Actions 进行持续集成，在 push 到 master 或 PR 时自动运行：

- **Lint** — golangci-lint 代码检查
- **Build** — 编译验证
- **Test** — 单元测试（race 检测 + 覆盖率）

本地可通过 `make ci` 模拟完整 CI 流程。

## 📄 License

MIT
