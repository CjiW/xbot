# 🤖 xbot

一个可扩展的 AI Agent 助手，基于 Go 构建，采用消息总线 + 插件化架构。支持飞书、QQ 等 IM 渠道接入，具备工具调用、可插拔记忆系统、技能系统和定时任务等能力。

## ✨ 特性

- **多渠道接入** — 消息总线架构，支持飞书（HTTP 回调）和 QQ（WebSocket），易于扩展
- **丰富的内置工具** — Shell 执行、文件读写编辑、Glob/Grep 搜索、Web 搜索、定时任务、子代理、文件下载
- **飞书深度集成** — 交互卡片构建、文档/知识库/多维表格读写、文件上传（基于 OAuth 用户授权）
- **技能系统 (Skills)** — OpenClaw 风格的渐进式技能加载，Markdown 技能包按需注入
- **可插拔记忆** — `MemoryProvider` 接口，当前实现 FlatMemory（全量注入），可扩展为分层/检索式记忆
- **用户画像** — 自动记录用户偏好和沟通风格，跨会话持久化
- **多租户** — 基于 channel + chatID 的租户隔离，支持多群组/多用户独立会话
- **MCP 协议支持** — 全局配置 + 会话级懒加载，支持 stdio 和 HTTP 两种传输模式
- **OAuth 框架** — 通用 OAuth 2.0 授权流程，支持飞书等第三方服务的用户级授权
- **子代理 (SubAgent)** — 可委派独立任务给子代理执行，支持预定义角色（如 code-reviewer）
- **提示词外置** — 系统提示词模板化（Go template），支持热加载
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
│   QQ    │                        │       │     ├─ Shell / Read / Edit
│ Channel  │                        │       │     ├─ Glob / Grep
└─────────┘                        │       │     ├─ WebSearch
                                   │       │     ├─ Cron (定时任务)
┌─────────┐                        │       │     ├─ SubAgent (子代理)
│  更多    │                        │       │     ├─ DownloadFile
│ Channel  │                        │       │     ├─ Card (飞书卡片)
└─────────┘                        │       │     ├─ ChatHistory
                                   │       │     ├─ UserProfile / SelfProfile
                                   │       │     ├─ OAuth (授权工具)
                                   │       │     ├─ ManageTools
                                   │       │     ├─ Feishu MCP (文档/知识库)
                                   │       │     └─ MCP (外部工具)
                                   └───┬───┘
                                       │
                               ┌───────┴───────┐
                               │    SQLite     │
                               │  (多租户存储)  │
                               │ Sessions      │
                               │ Memory        │
                               │ UserProfiles  │
                               │ OAuth Tokens  │
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
│   ├── agent.go             # 主循环、工具调用（只读并行/写串行）、消息发送
│   ├── context.go           # 上下文构建、提示词加载与渲染
│   └── skills.go            # 技能发现与加载（OpenClaw 风格渐进式）
│
├── bus/                     # 消息总线
│   └── bus.go               # Inbound / Outbound 消息通道
│
├── channel/                 # IM 渠道
│   ├── channel.go           # Channel 接口定义
│   ├── dispatcher.go        # 消息分发器
│   ├── feishu.go            # 飞书渠道（HTTP 回调、卡片回调、文件发送）
│   ├── feishu_token.go      # 飞书 Token 管理
│   └── qq.go                # QQ 渠道（WebSocket、支持私聊/群聊/频道）
│
├── llm/                     # LLM 客户端
│   ├── interface.go         # LLM 接口与类型定义
│   ├── openai.go            # OpenAI 兼容 API 客户端
│   ├── codebuddy.go         # CodeBuddy 客户端
│   ├── types.go             # 消息、工具调用等数据结构
│   └── mock.go              # 测试用 Mock 客户端
│
├── memory/                  # 可插拔记忆系统
│   ├── memory.go            # MemoryProvider 接口定义
│   └── flat/
│       └── flat.go          # FlatMemory 实现（全量注入）
│
├── tools/                   # 内置工具
│   ├── interface.go         # Tool 接口、Registry 注册表
│   ├── shell.go             # Shell 命令执行
│   ├── glob.go              # 文件 Glob 搜索
│   ├── grep.go              # 文件内容 Grep 搜索
│   ├── read.go              # 文件读取
│   ├── edit.go              # 文件编辑（创建 / 替换 / 行编辑 / 正则）
│   ├── download.go          # 飞书聊天文件/图片下载
│   ├── web_search.go        # Web 搜索（Tavily API）
│   ├── cron.go              # 定时任务调度
│   ├── subagent.go          # 子代理工具
│   ├── subagent_roles.go    # 子代理预定义角色
│   ├── card_builder.go      # 飞书卡片构建器（Session / Element 模型）
│   ├── card_tools.go        # 卡片工具集（create / add / preview / send）
│   ├── chat_history.go      # 聊天历史查询
│   ├── user_profile.go      # 用户画像 & Bot 自画像
│   ├── oauth.go             # OAuth 授权工具
│   ├── manage_tools.go      # 动态工具 / MCP / 技能管理
│   ├── mcp.go               # MCP 协议工具桥接（全局）
│   ├── mcp_common.go        # MCP 共享基础设施（配置、连接管理）
│   ├── session_mcp.go       # 会话级 MCP 管理（懒加载、超时清理）
│   └── feishu_mcp/          # 飞书工作台工具集
│       ├── feishu_mcp.go    # 核心：OAuth 客户端获取、工具注册
│       ├── docx.go          # 飞书文档读写（Markdown 互转）
│       ├── wiki.go          # 知识库/多维表格操作
│       ├── search.go        # 知识库搜索
│       ├── file.go          # 文件上传
│       ├── tools.go         # 工具定义
│       ├── block_helper.go  # 飞书 Block 类型映射与文本提取
│       ├── block_type_map.go
│       └── errors.go        # 飞书 API 错误处理
│
├── oauth/                   # OAuth 2.0 框架
│   ├── provider.go          # Provider 接口、Token 类型
│   ├── manager.go           # 授权流程管理（CSRF、pending flows）
│   ├── server.go            # OAuth 回调 HTTP 服务器
│   ├── storage.go           # Token 持久化（SQLite）
│   └── providers/
│       └── feishu.go        # 飞书 OAuth Provider
│
├── session/                 # 会话管理
│   ├── multitenant.go       # 多租户会话管理器
│   └── tenant.go            # 租户会话（消息历史 + 记忆）
│
├── storage/                 # 存储层
│   ├── migrate.go           # 数据库迁移
│   └── sqlite/              # SQLite 实现（纯 Go，无 CGO）
│       ├── db.go            # 数据库连接与初始化
│       ├── session.go       # 会话存储
│       ├── memory.go        # 记忆存储
│       ├── tenant.go        # 租户存储
│       └── user_profile.go  # 用户画像存储
│
├── config/                  # 配置加载
│   └── config.go            # 环境变量 → 结构体
│
├── version/                 # 版本信息
│   └── version.go           # 构建时注入（Version / Commit / BuildTime）
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
- 飞书开放平台应用 和/或 QQ 开放平台应用
- LLM API Key（DeepSeek / OpenAI 兼容 / CodeBuddy）

### 安装与运行

```bash
# 克隆仓库
git clone https://github.com/CjiW/xbot.git
cd xbot

# 配置环境变量
cp .env.example .env
# 编辑 .env，填写 LLM API Key、飞书/QQ 应用凭证等

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
make build    # 编译二进制（注入版本信息）
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

### LLM

| 变量 | 说明 | 默认值 |
|------|------|--------|
| `LLM_PROVIDER` | LLM 提供商（`openai` / `codebuddy`） | `openai` |
| `LLM_BASE_URL` | API 地址 | — |
| `LLM_API_KEY` | API 密钥 | — |
| `LLM_MODEL` | 模型名称 | `deepseek-chat` |
| `LLM_USER_ID` | CodeBuddy 用户 ID | — |
| `LLM_ENTERPRISE_ID` | CodeBuddy 企业 ID | — |
| `LLM_DOMAIN` | CodeBuddy 域名 | — |

### 飞书

| 变量 | 说明 | 默认值 |
|------|------|--------|
| `FEISHU_ENABLED` | 启用飞书渠道 | `false` |
| `FEISHU_APP_ID` | 飞书应用 ID | — |
| `FEISHU_APP_SECRET` | 飞书应用密钥 | — |
| `FEISHU_ENCRYPT_KEY` | 事件加密密钥 | — |
| `FEISHU_VERIFICATION_TOKEN` | 验证 Token | — |
| `FEISHU_ALLOW_FROM` | 允许的用户 ID（逗号分隔） | 空（允许所有） |
| `FEISHU_DOMAIN` | 飞书域名（用于文档链接） | — |

### QQ

| 变量 | 说明 | 默认值 |
|------|------|--------|
| `QQ_ENABLED` | 启用 QQ 渠道 | `false` |
| `QQ_APP_ID` | QQ 应用 ID | — |
| `QQ_CLIENT_SECRET` | QQ 应用密钥 | — |
| `QQ_ALLOW_FROM` | 允许的用户 ID（逗号分隔） | 空（允许所有） |

### Agent

| 变量 | 说明 | 默认值 |
|------|------|--------|
| `AGENT_MAX_ITERATIONS` | 单次对话最大工具调用轮数 | `20` |
| `AGENT_MEMORY_WINDOW` | 上下文保留的历史消息数 | `50` |
| `WORK_DIR` | 工作目录 | `.` |
| `PROMPT_FILE` | 自定义提示词模板路径 | 空（使用内置） |

### MCP 会话管理

| 变量 | 说明 | 默认值 |
|------|------|--------|
| `MCP_INACTIVITY_TIMEOUT` | MCP 连接不活跃超时 | `30m` |
| `MCP_CLEANUP_INTERVAL` | MCP 清理扫描间隔 | `5m` |
| `SESSION_CACHE_TIMEOUT` | 会话缓存超时 | `24h` |

### OAuth

| 变量 | 说明 | 默认值 |
|------|------|--------|
| `OAUTH_ENABLE` | 启用 OAuth 功能 | `false` |
| `OAUTH_PORT` | OAuth 回调端口 | `8081` |
| `OAUTH_BASE_URL` | OAuth 回调基础 URL（需公网可达） | — |

### 服务器 & 日志

| 变量 | 说明 | 默认值 |
|------|------|--------|
| `SERVER_HOST` | 服务监听地址 | `0.0.0.0` |
| `SERVER_PORT` | 服务监听端口 | `8080` |
| `LOG_LEVEL` | 日志级别 | `info` |
| `LOG_FORMAT` | 日志格式（`text` / `json`） | `json` |
| `PPROF_ENABLE` | 启用 pprof 性能分析 | `false` |
| `PPROF_HOST` | pprof 监听地址 | `localhost` |
| `PPROF_PORT` | pprof 监听端口 | `6060` |

## 🧠 记忆系统

xbot 采用可插拔的记忆架构（`MemoryProvider` 接口），当前实现为 FlatMemory：

- **MEMORY**（长期记忆）— 持续更新的事实性知识，如用户偏好、项目信息、待办事项。每次对话时全量注入系统提示词。
- **HISTORY**（历史日志）— 按时间追加的事件记录，支持检索。用于回溯过去发生的事情。

当会话消息超过 `AGENT_MEMORY_WINDOW` 时，Agent 自动触发异步记忆合并：LLM 将旧消息摘要写入 MEMORY 和 HISTORY，然后释放上下文空间。

```go
// 扩展记忆系统只需实现 MemoryProvider 接口
type MemoryProvider interface {
    Recall(ctx context.Context, query string) (string, error)
    Memorize(ctx context.Context, input MemorizeInput) (MemorizeResult, error)
    Close() error
}
```

### 用户画像

Agent 可通过 `update_user_profile` 工具记录对用户的观察（沟通风格、偏好等），通过 `update_self_profile` 更新自身画像。画像跨会话持久化，帮助 Agent 提供个性化服务。

## 🔧 技能系统

技能（Skill）采用 OpenClaw 风格的渐进式加载：启动时扫描目录生成技能目录，对话时 Agent 按需通过 `Read` 工具加载相关技能。

```
.xbot/skills/
└── my-skill/
    ├── SKILL.md          # 技能定义（必需）
    ├── scripts/          # 可选：脚本文件
    ├── references/       # 可选：参考资料
    └── assets/           # 可选：资源文件
```

SKILL.md 格式：

```markdown
---
name: my-skill
description: 技能简介（用于自动匹配和发现）
---

（Markdown 正文，加载后注入系统提示词）
```

技能目录在系统提示词中以 XML 格式列出，Agent 根据对话主题自主决定是否加载。

## 🎴 飞书交互卡片

内置渐进式卡片构建系统，通过工具调用逐步构建复杂的飞书交互卡片：

```
card_create → card_add_content / card_add_interactive / card_add_container → card_send
```

支持的组件：

| 类别 | 组件 |
|------|------|
| **展示** | Markdown、文本、图片、分割线、表格、图表、人员 |
| **交互** | 按钮、输入框、下拉选择、人员选择、日期/时间选择器、复选框 |
| **布局** | 多列布局、表单、折叠面板、可点击容器 |

卡片工具按需动态注册：只有调用 `card_create` 后才会注册其余卡片工具，发送完成后自动注销。

## 📄 飞书文档 & 知识库

通过 OAuth 用户授权，xbot 可以直接操作飞书工作台：

- **文档读写** — 读取飞书文档内容（转为 Markdown）、写入/更新文档
- **知识库搜索** — 搜索知识库空间和节点
- **多维表格** — 读取字段定义、查询/创建/更新记录
- **文件上传** — 上传文件到飞书云盘

需要配置 OAuth（`OAUTH_ENABLE=true`）并完成飞书应用的 OAuth 权限配置。

## 🔌 MCP 支持

xbot 支持两层 MCP 工具管理：

### 全局 MCP

在工作目录下创建 `mcp.json` 配置全局 MCP 工具服务器，启动时自动连接：

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

### 会话级 MCP

通过 `ManageTools` 工具在运行时动态添加/移除 MCP 服务器。会话级 MCP 连接支持：

- **懒加载** — 首次使用时才建立连接
- **不活跃超时** — 超过 `MCP_INACTIVITY_TIMEOUT` 自动断开
- **自动清理** — 定期扫描并回收空闲连接

支持 **stdio** 和 **HTTP** 两种 MCP 传输模式。

## 🤖 子代理 (SubAgent)

可将独立任务委派给子代理执行，子代理拥有完整的工具集但不能创建更多子代理：

```
SubAgent(task="审查 agent.go 的改动", role="code-reviewer")
```

预定义角色：

| 角色 | 说明 |
|------|------|
| `code-reviewer` | 代码审查专家，对照计划/需求审查实现，按严重程度分类问题 |

也可通过 `system_prompt` 参数自定义子代理行为。

## 📝 命令

对话中可使用的斜杠命令：

| 命令 | 说明 |
|------|------|
| `/new` | 归档记忆并重置会话 |
| `/version` | 显示版本信息 |
| `/help` | 显示帮助信息 |

## 🔄 CI

项目使用 GitHub Actions 进行持续集成，在 push 到 master 或 PR 时自动运行：

- **Lint** — golangci-lint 代码检查
- **Build** — 编译验证
- **Test** — 单元测试（race 检测 + 覆盖率）

本地可通过 `make ci` 模拟完整 CI 流程。

## 📄 License

MIT
