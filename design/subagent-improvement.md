# SubAgent 改进计划

## 目标

1. 让 SubAgent 功能正常工作（解决目录位置问题）
2. 创建默认的 SubAgent roles
3. 确保主 Agent 能知道有哪些 SubAgent 可用
4. 让主 Agent 更主动地调用 SubAgent

## 调研结果

### 1. LLM 如何知道有哪些 SubAgent 可用

**方案**：在系统 prompt 中通过 `<available_agents>` 标签动态注入。

参考 Claude Code 实践：
- 在系统 prompt 中列出所有可用的 subagent（name + description + tools）
- LLM 根据 description 决定何时调用哪个 subagent

### 2. 如何让 LLM 更主动调用 SubAgent

**方案**：在系统 prompt 中添加 SubAgent 调用指导。

参考 Claude Code 的 `Task Management` 和 `Tool Usage Policy` 部分：
- 明确告诉 LLM 什么场景应该使用 subagent
- 提供清晰的 description，让 LLM 能根据任务性质自主判断

### 3. 如何新建 SubAgent

**方案**：创建 `.xbot/agents/*.md` 文件，YAML frontmatter 定义元信息。

参考：
- Claude Code: `.claude/agents/*.md`
- Cursor: `.cursor/agents/*.md`
- Builder.io: `.builder/agents/`

文件格式：
```markdown
---
name: explorer
description: 探索项目结构，查找文件和代码
tools:
  - Glob
  - Grep
  - Read
  - Shell
---

你是一个项目探索助手...
```

**辅助方案**：提供 skill-creator skill 帮助用户创建新的 SubAgent。

---

## 分析

### 当前状态

| 组件 | 状态 | 说明 |
|------|------|------|
| SubAgent 工具 | ✅ 已实现 | `tools/subagent.go` |
| Role 加载器 | ✅ 已实现 | `tools/subagent_loader.go` |
| Role 缓存 | ✅ 已实现 | `tools/subagent_roles.go` |
| 系统 prompt 生成 | ✅ 已实现 | `agent/agents.go` - 生成 `<available_agents>` |

### 目录位置问题

| 配置 | 路径 |
|------|------|
| 默认 WORK_DIR | `.` (当前目录) |
| 代码期望的 agents 目录 | `{WORK_DIR}/.xbot/agents/` |
| 用户实际放置位置 | `/workspace/xbot/agents/` |

**根因**：代码使用 `{WORK_DIR}/.xbot/agents/`，而用户期望在项目根目录创建 `agents/` 目录。

### 可用 SubAgent 展示

系统通过 `<available_agents>` 标签在系统 prompt 中展示可用 roles：

```xml
<available_agents>
  <agent>
    <name>explorer</name>
    <description>探索项目结构，查找文件和代码</description>
    <tools>Glob, Grep, Read, Shell</tools>
  </agent>
</available_agents>
```

只要 agents 目录位置正确，SubAgent 工具就能自动发现并展示这些 roles。

---

## 任务拆分

### 1. 移动 agents 目录到正确位置

- **目标**：让 xbot 能加载用户创建的 explorer role
- **涉及文件**：目录结构变更
- **操作**：
  ```bash
  mkdir -p /workspace/.xbot/agents
  mv /workspace/xbot/agents/* /workspace/.xbot/agents/
  rmdir /workspace/xbot/agents
  ```

### 2. 创建默认 SubAgent roles

- **目标**：提供开箱即用的 SubAgent 模板
- **涉及文件**：`/workspace/.xbot/agents/*.md`
- **创建的 roles**：
  1. `explorer` - 项目探索（已有）
  2. `code-reviewer` - 代码审查
  3. `tester` - 测试辅助

### 3. 优化系统 prompt - 添加 SubAgent 调用指导

- **目标**：让 LLM 更主动使用 SubAgent
- **涉及文件**：`agent/agents.go` 或创建新的 prompt 模板
- **内容**：在系统 prompt 中添加：
  ```markdown
  ## SubAgent 使用指南
  
  当遇到以下情况时，考虑使用 SubAgent：
  - 复杂的多步骤任务，需要并行处理
  - 需要深入探索代码库结构
  - 需要专门的代码审查视角
  - 任务需要与当前对话不同的专业技能
  
  可用 SubAgent：
  <available_agents>
  ...
  </available_agents>
  ```

### 4. 创建 skill-creator skill（可选）

- **目标**：帮助用户创建新的 SubAgent
- **涉及文件**：`skills/skill-creator/` 或新的 skill
- **功能**：
  - 引导用户输入 name、description、tools
  - 自动生成 `.xbot/agents/*.md` 文件

### 5. 更新 .xbot.example 目录结构

- **目标**：让新用户知道正确的 agents 目录位置
- **涉及文件**：`.xbot.example/`
- **操作**：确保示例目录包含正确的 agents 结构

### 6. 验证 SubAgent 功能

- **目标**：确认 SubAgent 工具能正常工作
- **验证方法**：调用 SubAgent 工具使用 explorer role

---

## 执行顺序

1. 移动 agents 目录到正确位置
2. 创建默认 SubAgent roles
3. 优化系统 prompt（可选，后续迭代）
4. 更新 .xbot.example
5. 重启 xbot 并测试

---

## 待确认

- [ ] 是否需要创建额外的默认 roles？
- [ ] 是否需要现在优化系统 prompt？（可选，后续迭代）
- [ ] 是否需要 skill-creator skill？（可选）
