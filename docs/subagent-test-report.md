# SubAgent 系统测试报告

**日期**: 2026-03-18
**分支**: master (3152fee)
**测试目标**: 验证 SubAgent 系统的整体可用性，特别是 agent 间相互调用能力

---

## 1. 调研摘要

### 1.1 agents catalog 注入机制

PR #152 (`471f579`) 已实现 agents catalog 注入，覆盖主 Agent 和 SubAgent：

| 注入目标 | 机制 | 代码位置 |
|----------|------|----------|
| 主 Agent | `AgentsCatalogMiddleware` 中间件 → `SystemParts["15_agents"]` | `agent/middleware_builtin.go` |
| SubAgent | `buildSubAgentRunConfig` 直接拼接到 system prompt 末尾 | `agent/engine_wire.go` L243-245 |

格式为 `<available_agents>` XML，包含每个 agent 的 name、description、tools。

### 1.2 SubAgent 工具解析

- 参数名: `role`（非 `roleName`）
- 搜索顺序: 用户私有 → 同步全局 → 全局目录
- 加载方式: 按需从文件加载，无缓存

### 1.3 PR #154 修复

PR #154 (`3152fee`) 修复了 `Registry.Clone()` 未复制 `coreTools` 的问题，该问题会导致 SubAgent 获得 0 个工具定义。

---

## 2. 测试用例与结果

### 测试 1: agents catalog 感知

**方法**: 派发 SubAgent (secretariat)，让其检查 system prompt 中的 `<available_agents>` 部分。

**结果**: ✅ 通过

SubAgent 成功在 system prompt 中感知到 7 个可用 agent（XML 格式注入）。

### 测试 2: SubAgent 调用另一个 SubAgent

**方法**: 派发 SubAgent (secretariat)，让其通过 SubAgent 工具调用 code-reviewer。

**结果**: ❌ 失败（Bug 已修复）

**根因**: `buildSubAgentRunConfig` 中 `allowedTools` 过滤逻辑 Bug。当 agent 的 frontmatter `tools:` 不包含 "SubAgent" 时（如 secretariat 定义为 `tools: [Shell, Read, Grep, Glob, WebSearch]`），即使 `spawn_agent: true`（默认值），SubAgent 工具也会被 `allowedTools` 白名单过滤掉。

**代码路径**:
```
buildSubAgentRunConfig
  → subTools.Clone()
  → if !caps.SpawnAgent { Unregister("SubAgent") }  // SpawnAgent=true, 跳过
  → for tool in subTools.List():
      if !allowed[tool.Name()] { Unregister }  // "SubAgent" 不在 allowed 中 → 被移除!
```

### 测试 3: Shell 工具可用性

**结果**: ✅ 通过

SubAgent 的 Shell 工具正常工作。

### 测试 4: 基础工具操作（Glob/Grep）

**方法**: 让 SubAgent 使用 Glob 查找文件。

**结果**: ✅ 通过

---

## 3. 发现的 Bug 及修复

### Bug #1: allowedTools 过滤导致 SubAgent 工具丢失

**严重程度**: 🔴 高 — 直接导致 SubAgent 无法调用其他 SubAgent

**描述**: 当 agent 定义 `tools:` 白名单不包含 "SubAgent" 时，即使 `spawn_agent: true`（默认值），SubAgent 工具也会被过滤掉。这是因为 `allowedTools` 过滤在 `SpawnAgent` 检查之后执行，且不受 `caps.SpawnAgent` 约束。

**修复**: 在 `allowedTools` 过滤时，当 `caps.SpawnAgent = true` 时强制将 "SubAgent" 加入 allowed 集合。

```go
// agent/engine_wire.go
if len(allowedTools) > 0 {
    allowed := make(map[string]bool, len(allowedTools))
    for _, name := range allowedTools {
        allowed[name] = true
    }
    // 新增：确保能力相关工具始终可用
    if caps.SpawnAgent {
        allowed["SubAgent"] = true
    }
    // ...
}
```

### Bug #2: SubAgent 非核心工具不出现在 LLM tool definitions 中

**严重程度**: 🟡 中 — 影响 allowedTools 中非核心工具的可用性

**描述**: SubAgent 使用 `defaultToolExecutor`（无会话激活机制），但 `AsDefinitionsForSession()` 只返回核心工具（coreTools）和已激活工具。如果 allowedTools 包含非核心工具（如某些 MCP 工具），这些工具虽然存在于 globalTools 中，但不会出现在 LLM 的 tool definitions 中，导致 LLM 无法看到和调用它们。

**修复**: 新增 `Registry.ForceCoreTools()` 方法，在 allowedTools 过滤后将所有剩余工具标记为核心工具。

```go
// tools/interface.go
func (r *Registry) ForceCoreTools() {
    r.mu.Lock()
    defer r.mu.Unlock()
    for name := range r.globalTools {
        r.coreTools[name] = true
    }
}

// agent/engine_wire.go — 在 allowedTools 过滤后调用
subTools.ForceCoreTools()
```

---

## 4. 观察到的其他问题（未修复，仅记录）

### 问题 #1: SubAgent prompt 模板与工具机制不匹配

**描述**: `subagentSystemPromptTemplate` 中提到"大部分工具没有启用"并建议使用 `search_tools` / `load_tools` 激活工具。但 SubAgent 的工具是通过 `allowedTools` 白名单预配置的，`defaultToolExecutor` 也不检查激活状态。这可能导致 SubAgent 浪费轮次调用 `search_tools` / `load_tools`。

**建议**: 考虑为 SubAgent 提供不同的 prompt 模板，直接列出可用工具，或者去掉 search_tools/load_tools 的引导语。

### 问题 #2: agents catalog 始终注入，不区分 SpawnAgent 能力

**描述**: 当前实现中，`agents catalog` 始终注入到 SubAgent 的 system prompt 中（只要 catalog 非空），即使 `spawn_agent: false`。这会浪费 tokens。

**建议**: 仅在 `caps.SpawnAgent = true` 时注入 catalog。

### 问题 #3: EnsureSynced 全局单次同步

**描述**: `globalSkillSyncer.synced` 是包级变量，按 senderID 只同步一次。进程运行期间全局 agents 的更新不会重新同步到已同步的用户 workspace。

---

## 5. 代码变更摘要

| 文件 | 变更 |
|------|------|
| `agent/engine_wire.go` | `allowedTools` 过滤时保留 SpawnAgent 能力工具；过滤后调用 `ForceCoreTools()` |
| `tools/interface.go` | 新增 `Registry.ForceCoreTools()` 方法 |
| `tools/load_tools_test.go` | 新增 `TestForceCoreTools_MakesAllToolsCore` 和 `TestForceCoreTools_CloneAndFilter` 测试 |

**测试结果**: 全部 3 个 package 的测试通过，无回归。

---

## 6. 后续建议

1. **部署验证**: 代码修改需要重新部署后，重新执行 SubAgent 互调测试以验证修复效果
2. **集成测试**: 建议添加端到端集成测试，验证 SubAgent → SubAgent 的完整调用链
3. **prompt 模板优化**: 为 SubAgent 设计更精准的 prompt 模板，避免误导性的 search_tools/load_tools 引导
