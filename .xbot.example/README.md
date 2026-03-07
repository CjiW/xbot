# .xbot.example

xbot 运行时配置的参考示例。首次部署时复制到工作目录：

```bash
cp -r .xbot.example /path/to/workdir/.xbot
```

## 目录结构

```
.xbot/
├── agents/          # SubAgent 角色定义（Markdown + YAML frontmatter）
│   └── *.md
├── skills/          # 技能目录（每个技能一个子目录）
│   └── <name>/
│       └── SKILL.md # 技能描述与指令（必需）
└── xbot.db          # SQLite 数据库（运行时自动创建）
```

## Agents

SubAgent 角色定义，格式兼容 Claude Code：

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

- 添加 agent：`agents/` 下创建 `.md` 文件
- 添加 skill：`skills/` 下创建子目录，放入 `SKILL.md`
- 技能可包含 `scripts/`、`references/`、`assets/` 辅助目录
