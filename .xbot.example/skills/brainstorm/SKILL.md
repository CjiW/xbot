---
name: brainstorm
description: 启动脑力激荡子代理进行交互式深度讨论与争辩。当需要设计方案讨论、架构选型辩论、方案质量挑战、探索替代方案时激活。
---

# Brainstorm — 脑力激荡

启动交互式 `brainstorm` 子代理，与其进行多轮深度讨论，产出最优设计方案。

## 使用方式

```python
# 创建交互式会话
SubAgent(task="你的设计问题或方案", role="brainstorm", interactive=True)

# 继续讨论
SubAgent(task="你的论点或反驳", role="brainstorm", action="send")

# 讨论结束，收集结论
SubAgent(task="总结最终方案", role="brainstorm", action="unload")
```

## 讨论流程

1. **提出问题** — 将设计问题、方案草案、技术选型等发给 brainstorm agent
2. **多轮辩论** — agent 会质疑假设、提出替代方案，你进行回应或反驳
3. **充分探索** — 至少 2-3 轮交锋，确保核心问题被充分讨论
4. **收敛结论** — 当双方达成共识或无更多异议时，用 `action="unload"` 结束并获取最终推荐

## 最佳实践

| 场景 | 建议 |
|------|------|
| 方案设计 | 先给出初步方案，让 agent 挑战和完善 |
| 架构选型 | 列出候选方案和你的倾向，让 agent 从反面论证 |
| 方案评审 | 将完整方案发给 agent，要求找漏洞和盲点 |
| 技术决策 | 描述约束条件和目标，让 agent 提出你没考虑的角度 |

## 注意事项

- 每次讨论聚焦**一个核心问题**，不要试图一次讨论太多
- 给 agent 足够的上下文（代码结构、技术栈、约束条件）
- 讨论结束后，将结论整理成方案文档再进入执行流程
- brainstorm agent 有 memory 能力，会记得历史讨论
