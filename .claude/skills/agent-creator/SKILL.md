---
name: agent-creator
description: 创建、更新或删除 xbot SubAgent 角色定义。当用户要求创建新 agent、修改已有 agent 角色定义、或讨论 agent 设计时使用。
---

# Agent Creator

## Required Tools
After loading this skill, immediately call `load_tools` for these tools:
- Edit

## Agent 定义文件格式

SubAgent 角色定义存放在 `.xbot/agents/*.md`，格式为 YAML frontmatter + Markdown 正文：

```markdown
---
name: role-name
description: 一句话描述角色用途
tools:
  - Glob
  - Grep
  - Read
  - Shell
capabilities:
  memory: true
  send_message: false
  spawn_agent: false
---

这里是 System Prompt 正文。
定义角色的行为准则、输出格式、工作方式等。
```

### Frontmatter 字段

| 字段 | 必填 | 说明 |
|------|------|------|
| `name` | 否（缺省用文件名） | 角色名，`SubAgentTool` 的 `role` 参数值，**小写连字符** |
| `description` | 是 | 角色用途描述，会出现在 `<available_agents>` 系统提示中 |
| `tools` | 否 | 白名单工具名列表。留空则继承父 Agent 的全部工具 |
| `capabilities` | 否 | 能力声明，见下表 |

### capabilities 子字段

| 字段 | 默认值 | 说明 |
|------|--------|------|
| `memory` | false | 可访问 Letta memory（core/archival/recall） |
| `send_message` | false | 可直接向 IM 渠道发送消息 |
| `spawn_agent` | false | 可创建子 Agent（受递归深度限制） |

### System Prompt 正文

frontmatter `---` 之后的全部内容作为 System Prompt，注入给 SubAgent。
- 500 词以内，设定角色边界和输出格式
- 不需要重复工具说明（工具由 `tools` 字段控制）

### 文件存放路径

| 路径 | 作用 |
|------|------|
| `.xbot/agents/*.md` | 全局角色，所有用户可见 |
| `.xbot/users/{senderID}/agents/*.md` | 用户私有角色，同名覆盖全局 |

## 工作流

### 创建 Agent

1. 用卡片或对话确认需求：角色名、用途、需要哪些工具、是否需要 memory/send_message/spawn_agent
2. 创建文件：`Edit(mode=create, path=".xbot/agents/{name}.md", content=...)`
3. 验证：用 `Shell` 执行 `grep -c 'name:' .xbot/agents/{name}.md` 确认文件存在

### 更新 Agent

1. `Edit(mode=read, path=".xbot/agents/{name}.md")` 查看当前定义
2. `Edit(mode=replace, ...)` 修改
3. 提示用户角色支持热更新（下次 `GetSubAgentRole` 调用自动生效）

### 删除 Agent

1. `Shell` 执行 `rm .xbot/agents/{name}.md`
2. 确认删除

## 命名规范

- **小写连字符**：`code-reviewer`、`data-analyst`
- 角色或动词导向：`reviewer`、`explorer`、`tester`、`researcher`

## 常见工具组合参考

| 角色场景 | 推荐工具 |
|----------|----------|
| 代码审查 | Glob, Grep, Read, Shell |
| 文件探索 | Glob, Grep, Read |
| 测试执行 | Glob, Grep, Read, Shell |
| 文档编写 | Edit, Shell |
| 网页调研 | Shell (curl), Fetch |
