# .xbot.example

xbot 运行时配置的参考示例。首次部署时复制到工作目录：

```bash
cp -r .xbot.example /path/to/workdir/.xbot
```

## 目录结构

```
.xbot/
├── agents/          # 全局 SubAgent 角色定义（所有用户共享）
│   └── *.md
├── skills/          # 技能目录（每个技能一个子目录）
│   └── <name>/
│       └── SKILL.md # 技能描述与指令（必需）
└── xbot.db          # SQLite 数据库（运行时自动创建）
```

## Agents

SubAgent 角色定义文件（`*.md`），YAML frontmatter + Markdown body：

```markdown
---
name: my-agent
description: "When to use this agent"
tools:
  - Read
  - Grep
---

System prompt for the agent...
```

- `name`：角色标识，主 agent 通过此名称调用
- `tools`：工具白名单（可选，不设则允许所有）

### 个人 Agent

每个用户的个人 agent 存放在其 **workspace 根目录** 的 `agents/` 目录下：

```
{workspace}/agents/*.md
```

个人 agent 会覆盖同名全局 agent。agent 可通过 `agent-creator` skill 在此目录创建新角色。

### 加载优先级

1. **个人 agents** — `{workspace}/agents/*.md`（同名覆盖全局）
2. **全局 agents** — `.xbot/agents/*.md`（所有用户共享）

## Skills

技能通过 `SKILL.md` 定义，启动时扫描注册，按需加载：

```markdown
---
name: my-skill
description: What it does and when to use it
---

Detailed instructions...
```

## 自定义

- 添加个人 agent：在 workspace 的 `agents/` 下创建 `.md` 文件
- 添加全局 agent：在 `.xbot/agents/` 下创建 `.md` 文件
- 添加 skill：`skills/` 下创建子目录，放入 `SKILL.md`
- 技能可包含 `scripts/`、`references/`、`assets/` 辅助目录
