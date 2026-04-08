---
title: "plan-cli-optimization"
weight: 160
---

# 计划：CLI 全面优化

> 生成时间：2026-04-01
> 状态：待确认

## 背景与目标

CLI 用户不想额外安装 ollama/embedding 服务，所以 CLI 默认使用 flat memory 模式。
当前 flat 模式下 prompt.md 硬编码了 search_tools 和 letta 记忆工具的引导文字，
但这些工具在 flat 模式下根本不存在，导致 LLM 尝试调用不存在的工具。
同时有多个 CLI 体验问题需要修复和增强。

## 现状分析

### 关键文件
| 文件 | 职责 | 修改类型 |
|------|------|----------|
| `prompt.md` | 系统提示词模板，硬编码 search_tools 和 letta 记忆引导 | 修改 |
| `agent/context.go` | PromptData 定义 + PromptLoader | 修改 |
| `agent/middleware_builtin.go` | SystemPromptMiddleware 渲染 + UserMessage 引导 | 修改 |
| `agent/agent.go` | Tool 注册（letta 条件分支） | 修改 |
| `agent/engine.go:854` | ToolProgress 创建时未设 Iteration | 修改（1行） |
| `tools/embed_skills.go` | 现有 embed skill 机制 | 参考不改 |
| `tools/embed_agents.go` | 新建 embed agent 机制 | 新建 |
| `tools/embed_agents/*.md` | 新建 embed agent 文件 | 新建 |
| `agent/agents.go` | Agent catalog 生成 | 修改 |
| `tools/subagent_roles.go` | Agent 运行时加载 | 修改 |
| `agent/skills.go` | Skill catalog 生成 | 修改（同步 embed agents） |
| `tools/skill_sync.go` | Docker 沙箱同步 | 修改 |
| `tools/remote_sandbox.go` | Remote 沙箱同步 | 修改 |
| `cmd/xbot-cli/main.go` | CLI 入口 | 修改 |
| `channel/cli.go` | CLI TUI | 修改 |
| `channel/capability.go` | SettingsCapability 接口 | 不改 |
| `docs/cli-channel.md` | CLI 文档 | 修改 |
| `README.md` | 主文档 | 修改 |

### 发现的问题
1. **prompt.md 矛盾**：硬编码 search_tools 引导（13-15行）和 letta 记忆工具引导（19-56行），flat 模式下这些工具不存在
2. **Progress panel bug**：`agent/engine.go:854` 创建 ToolProgress 时未设 Iteration 字段，导致所有工具 Iteration=0，CLI 侧按迭代过滤失效
3. **AskUser 已存在**：`tools/ask_user.go` 已完整实现，无需额外工作
4. **Agent 无 embed**：Skill 有 embed.FS 机制，Agent 没有
5. **CLI 无首次引导**：无 first-run 检测或欢迎流程
6. **CLI 无 SettingsCapability**：/settings 命令 fallback 到"无设置项"
7. **文档路径错误**：README 和 docs/cli-channel.md 引用 `./cmd/cli`（实际是 `./cmd/xbot-cli`）

## 详细计划

### 阶段一：flat memory 优化 + progress 面板修复

- [ ] 1.1 在 `agent/context.go` 的 `PromptData` 中添加 `MemoryProvider` 字段
- [ ] 1.2 修改 `agent/middleware_builtin.go` 中 `SystemPromptMiddleware.Process` 传入 MemoryProvider
- [ ] 1.3 修改 `prompt.md`：
  - 将工具引导部分用 `{{if eq .MemoryProvider "letta"}}...{{end}}` 包裹 search_tools 引导
  - flat 模式下工具部分直接列出所有核心工具（不需要 search/load 流程）
  - 将"认识自己"、"认识每个人"、"记忆"整个章节用 letta 条件包裹
  - flat 模式下用简化版记忆说明（或完全省略）
- [ ] 1.4 修改 `agent/middleware_builtin.go` 中 `buildSystemGuideText`：flat 模式不需要 search_tools 提醒
- [ ] 1.5 修复 `agent/engine.go:854`：添加 `Iteration: i` 到 ToolProgress 初始化
- [ ] 1.6 编译 + 测试验证
- [ ] 1.7 commit + push

### 阶段二：embed agents 集成

- [ ] 2.1 创建 `tools/embed_agents.go`（参照 `tools/embed_skills.go`）
- [ ] 2.2 创建 `tools/embed_agents/` 目录，放入 agent .md 文件
- [ ] 2.3 修改 `agent/agents.go` — `GetAgentsCatalog` 增加 embed 扫描
- [ ] 2.4 修改 `tools/subagent_roles.go` — 增加 embed fallback
- [ ] 2.5 修改 `tools/skill_sync.go` — 增加 embed agents 同步（Docker）
- [ ] 2.6 修改 `tools/remote_sandbox.go` — 增加 embed agents 同步（Remote）
- [ ] 2.7 编译 + 测试验证
- [ ] 2.8 commit + push

### 阶段三：CLI 首次启动引导

- [ ] 3.1 在 `cmd/xbot-cli/main.go` 添加首次运行检测（config.json 不存在或 API Key 未配置）
- [ ] 3.2 实现交互式配置引导（provider → API key → base URL → model → sandbox mode）
- [ ] 3.3 引导完成后写入 `~/.xbot/config.json`
- [ ] 3.4 编译 + 测试验证
- [ ] 3.5 commit + push

### 阶段四：CLI settings 可视化配置

- [ ] 4.1 在 `channel/cli.go` 为 `CLIChannel` 实现 `SettingsCapability` 接口
- [ ] 4.2 定义 CLI 特有的 settings schema（theme、editor 等，或映射全局 config 项）
- [ ] 4.3 实现 `HandleSettingSubmit` 处理设置变更
- [ ] 4.4 编译 + 测试验证
- [ ] 4.5 commit + push

### 阶段五：文档更新

- [ ] 5.1 修复 `README.md` 和 `docs/cli-channel.md` 中的构建路径（`./cmd/cli` → `./cmd/xbot-cli`）
- [ ] 5.2 更新配置说明（首次运行引导、必填项、环境变量）
- [ ] 5.3 补充 CLI 功能说明（AskUser、settings、首次引导等新功能）
- [ ] 5.4 commit + push

## 验证方案

- 每个阶段完成后 `go build ./...` + `go test -race ./...`
- 阶段一：手动验证 flat 模式下 prompt 不含 search_tools 引导
- 阶段二：验证 embed agents 在 catalog 中出现
- 阶段三：删除 config.json 后运行 CLI，验证引导流程
- 阶段四：验证 /settings 命令显示可配置项

## 注意事项

- 阶段一和阶段二是独立修改，但阶段一影响最大，优先处理
- AskUser 工具已存在，TODO #5 已完成，无需额外工作
- prompt.md 使用 Go text/template，条件语法是 `{{if eq .Field "value"}}...{{end}}`
- embed agents 需要和 embed skills 保持一致的 API 风格
