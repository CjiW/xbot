---
title: "Three Provinces Optimization [Completed]"
weight: 10
---


## 目标

1. **将三省六部 agent 从系统提示词硬编码迁移为 `.xbot/agents/*.md` 文件定义**，使角色可热更新、版本化
2. **强化分工和工作流程**：明确各部职责边界，建立标准化的圣旨→方案→执行→验证→交付 流水线
3. **解决文档生命周期问题**：方案文档过期/未清理，需要在流水线中强制执行文档生命周期管理

## 现状分析

### 当前问题

| 问题 | 根因 |
|------|------|
| 三省六部 agent 定义在飞书配置的系统提示词中 | 不是 `.xbot/agents/` 文件，无法热更新、无法版本管理 |
| agent 无工具集 | 系统提示词中 agent 只有 description，无 tools 白名单 |
| 文档过期/堆积 | 产出文档后无清理机制，`docs/` 和 `docs/plans/` 下积累了大量过时文档 |
| 工作流无强制约束 | 圣旨执行靠口口相传，无结构化的流水线检查点 |

### 现有 agent 定义

- `.xbot/agents/` 目录下只有 `code-reviewer.md`、`tester.md`、`explorer.md`（通用角色）
- 三省六部角色（crown-prince、secretariat、chancellery 等）只在飞书系统提示词中声明

### Agent 定义机制

- 文件位置：`.xbot/agents/<name>.md`
- 格式：YAML frontmatter（name, description, tools, capabilities）+ Markdown 正文（SystemPrompt）
- 支持热更新：每次 `GetSubAgentRole` 重新从文件加载
- `AgentStore.GetAgentsCatalog()` 自动扫描目录生成 `<available_agents>` 列表

## 设计方案

### 1. Agent 文件定义

创建 10 个 agent 文件，取代飞书系统提示词中的硬编码：

| 角色 | 文件名 | 职责 | 关键工具 |
|------|--------|------|---------|
| 吴王 | `crown-prince.md` | 分拣政务、派发任务、汇总复奏 | SubAgent, Edit, Read, Glob, Grep, Shell |
| 中书省 | `secretariat.md` | 制定方案、设计架构 | Read, Glob, Grep, WebSearch, Edit |
| 门下省 | `chancellery.md` | 审核方案、复核结果 | Read, Glob, Grep |
| 尚书省 | `department-state.md` | 派发任务、协调六部、验证流程 | SubAgent, Edit, Read, Glob, Grep |
| 吏部 | `ministry-personnel.md` | 环境搭建、项目结构 | Shell, Read, Glob, Grep, Edit |
| 户部 | `ministry-revenue.md` | 数据处理、日志分析 | Shell, Read, Glob, Grep |
| 礼部 | `ministry-rites.md` | 文档撰写、发布 | Edit, Read, Glob, Grep, Shell |
| 兵部 | `ministry-defense.md` | 安全审计、漏洞扫描 | Shell, Read, Glob, Grep |
| 刑部 | `ministry-justice.md` | 测试编写、执行 | Shell, Read, Glob, Grep, Edit |
| 工部 | `ministry-works.md` | 代码编写、工程实施 | Edit, Read, Glob, Grep, Shell |

### 2. 工作流水线（核心改进）

#### 标准流程

```
陛下圣旨 → 上柱国 → 吴王(crown-prince)
                         ↓
                    分拣政务（按圣旨类型分流）
                    ├── 简单任务 → 直接指派六部
                    └── 复杂任务 → 中书省(secretariat) 制定方案
                                       ↓
                                  门下省(chancellery) 审核方案
                                       ↓ ✅ 通过
                                  尚书省(department-state) 派发执行
                                       ↓
                                  六部执行（工部/刑部/...）
                                       ↓
                                  刑部(ministry-justice) 验证
                                       ↓
                                  门下省(chancellery) 复核结果
                                       ↓ ✅ 通过
                                  礼部(ministry-rites) 清理文档 + 上报
                                       ↓
                                  吴王汇总复奏 → 上柱国 → 陛下
```

#### 文档生命周期管理（重点）

**问题**：方案文档在 `docs/plans/` 生成后，任务完成后无人清理，导致过期文档堆积。

**解决**：在流水线中强制嵌入文档生命周期检查：

1. **创建阶段**：中书省制定方案时，文档头部必须包含元数据：
   ```
   ---
   status: draft | approved | in-progress | completed | archived
   created: 2026-03-24
   owner: secretariat
   related_pr: null | #xxx
   ---
   ```
2. **执行阶段**：尚书省派发任务时，在 TODO 中包含"完成后更新方案文档状态为 completed"
3. **收尾阶段**：**礼部** 负责执行文档收尾：
   - 已合并的 PR → 方案文档移到 `docs/plans/archive/` 或删除
   - 已废弃的方案 → 标记 `status: archived` 并归档
   - 过期文档（>30 天 completed）→ 清理
4. **周期性审计**：上柱国定期（每周）触发文档审计，检查 `docs/plans/` 下的过期文档

#### 关键：礼部职责扩展

礼部不仅是"文案撰写"，更是**文档生命周期管理者**：
- 任务交付前必须确认：相关方案文档已归档/清理
- 拒绝交付未清理文档的任务（门下省复核项之一）

### 3. Agent SystemPrompt 设计要点

#### 吴王（crown-prince）— 核心枢纽

```
你是吴王，太子，上柱国之下的最高执行官。
职责：
- 接收上柱国转发的圣旨，分析任务类型和复杂度
- 简单任务直接指派六部，复杂任务提交中书省制定方案
- 汇总各部门复奏，向上柱国呈报
- 跟踪所有在途任务状态

派发原则：
- 方案设计类 → 中书省
- 代码实现类 → 工部
- 测试验证类 → 刑部
- 文档撰写类 → 礼部
- 安全审计类 → 兵部
- 环境搭建类 → 吏部
- 数据分析类 → 户部

强制规则：
- 复杂任务（涉及多文件/多模块/架构变更）必须先走中书省出方案
- 方案必须经门下省审核后才可执行
- 执行完毕后必须经门下省复核才可交付
- 所有任务完成后，相关文档由礼部收尾
```

#### 门下省（chancellery）— 质量把关

```
你是门下省，负责审核把关。
职责：
- 审核中书省的方案（可行性、完整性、风险评估）
- 复核六部的执行结果（是否满足方案要求）
- 审核文档收尾情况

审核方案时检查：
1. 方案是否覆盖所有需求点
2. 任务拆分是否合理
3. 是否有文档元数据（status, owner, created）
4. 风险点和边界条件是否考虑

复核结果时检查：
1. 实现是否与方案一致
2. 编译是否通过、测试是否通过
3. 方案文档状态是否已更新
4. 是否有遗留的临时文件/文档需要清理

退回条件（任一满足即退回）：
- 方案有重大遗漏
- 实现与方案不一致
- 编译或测试失败
- 文档未清理
```

#### 礼部（ministry-rites）— 文档生命周期

```
你是礼部，负责文档撰写与文档生命周期管理。
职责：
1. 撰写文档：README、CHANGELOG、API 文档、发布公告
2. 文档收尾：任务完成后清理和归档相关文档
3. 文档审计：定期检查过期文档

文档生命周期规则：
- 方案文档必须有 YAML 元数据（status/created/owner/related_pr）
- PR 合并后，对应方案文档必须归档或删除
- 超过 30 天处于 completed 状态的文档自动清理
- docs/ 目录下无 owner 的文档视为孤儿文档，需确认归属

文档收尾流程（每次任务交付前执行）：
1. 检查本次任务相关的所有方案文档
2. 更新文档状态为 completed
3. 关联 PR 编号到 related_pr
4. 将已完成的方案移到 docs/plans/archive/ 或删除
5. 确认无遗留临时文件
```

### 4. 过期文档清理

方案生效后，立即执行一次清理：

| 文档 | 状态 | 操作 |
|------|------|------|
| `docs/claude-code-gap-analysis.md` | 已读过时（v1.0） | 归档到 archive |
| `docs/project-awareness-proposal.md` | 已读过时（v3.0，项目感知已实现） | 归档到 archive |
| `docs/phase2-implementation-plan.md` | 已完成 | 归档到 archive |
| `docs/phase1.5-compress-plan.md` | 已完成 | 归档到 archive |
| `docs/plan-subagent-interactive.md` | 已完成 | 归档到 archive |
| `docs/plan-subagent-memory.md` | 已完成 | 归档到 archive |
| `docs/plan-tool-hooks.md` | 已完成 | 归档到 archive |
| `docs/concurrent-subagent-design.md` | 设计已实现 | 归档到 archive |
| 其他 context/design 文档 | 需逐一评估 | 按状态处理 |

## 任务拆分

### 1. 创建 Agent 定义文件
- 目标：创建 10 个 `.xbot/agents/*.md` 文件
- 涉及文件：`.xbot/agents/crown-prince.md` 等共 10 个新文件
- 预计改动：每个文件包含 frontmatter（name, description, tools, capabilities）+ SystemPrompt 正文

### 2. 清理飞书系统提示词中的硬编码 agent 定义
- 目标：飞书配置中移除 `<available_agents>` 的三省六部硬编码，改为由 AgentStore 自动生成
- 涉及文件：飞书后台配置
- 预计改动：移除手写的 agent 列表

### 3. 文档清理
- 目标：将已完成/过期的文档归档
- 涉及文件：`docs/` 目录下多个文档
- 预计改动：创建 `docs/archive/` 目录，移动过期文档

### 4. 验证
- 目标：确认 agent 文件能被正确加载
- 涉及文件：无新文件
- 预计改动：启动 xbot 检查 agent catalog 输出

## 执行顺序

1. 创建 10 个 agent 定义文件
2. 文档清理（创建 archive 目录 + 移动过期文档）
3. 编译验证（确保无代码变更影响）
4. 通知陛下更新飞书系统提示词配置（移除硬编码 agent）

## 待确认

- [ ] agent 描述是否用中文？frontmatter description 建议**中文**，便于系统提示词中展示
- [ ] 是否需要给 agent 分配 memory 能力？（建议：吴王、中书省、门下省分配 memory=true，其余=false）
- [ ] 文档清理范围：是否所有 docs/ 下的设计文档都归档？还是保留部分活跃的？
