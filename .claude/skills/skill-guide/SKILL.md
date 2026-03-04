---
name: skill-guide
description: Use when creating or updating skills to ensure correct format and structure
---

## Skill 格式规范

### 文件结构

```
.claude/skills/
  skill-name/
    SKILL.md              # 大写SKILL.md，必须
```

### SKILL.md 必须格式

```markdown
---
name: skill-name-with-hyphens
description: Use when [触发条件，何时使用]
user-invokable: true      # 可选，允许用户直接调用
---

## 标题

内容...
```

### Frontmatter 规则

| 字段 | 要求 |
|------|------|
| `name` | 只用字母、数字、连字符（无括号、特殊字符） |
| `description` | 第三人称，描述**何时使用**（不是做什么），以"Use when..."开头 |
| `user-invokable` | 可选，true/false |

### Description 写法

```yaml
# ❌ 错误：描述做什么
description: 帮助创建飞书卡片

# ❌ 错误：第一人称
description: 我可以帮助你处理卡片

# ✅ 正确：描述何时使用
description: Use when modifying tools/card_builder.go, tools/card_tools.go or card handling code

# ✅ 正确：触发条件 + 症状
description: Use when tests have race conditions, timing dependencies, or pass/fail inconsistently
```

### 内容结构建议

```markdown
## 架构概览
简短说明系统/模块结构

## 关键文件
| 文件 | 功能 |
表格列出核心文件

## 核心数据结构
重要的 struct、interface 定义

## 约定与规范
编码约定、最佳实践

## 常见陷阱
容易犯的错误和解决方案

## 变更历史
- 日期: 变更描述
```

## 常见错误

### 1. 文件名错误
- ❌ `skill.md`（小写）
- ✅ `SKILL.md`（大写）

### 2. Frontmatter 缺失
- ❌ 直接以 `# 标题` 开头
- ✅ 以 `---` 包围的 YAML frontmatter 开头

### 3. Description 过于详细
- ❌ 在 description 中写流程步骤
- ✅ 只写触发条件，流程写在正文中

### 4. Name 使用特殊字符
- ❌ `card-builder（卡片构建）`
- ✅ `card-builder`

## 更新 Skill

1. 编辑 SKILL.md
2. 在变更历史中添加条目
3. 如果是新模块，考虑创建独立 skill

## 变更历史

- 2026-03-04: 初始创建
