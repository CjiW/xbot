# SubAgent 工作进度上报 + Bug 修复 实施方案

## 目标

1. **主功能**：让 SubAgent 的工作进度（工具调用状态、思考过程）能实时反映给用户
2. **Bug 修复**：修复之前发现的 3 个 SubAgent 相关 bug

## 架构分析

### 当前进度通知机制

主 Agent 进度链路：
```
Run() → notifyProgress() → ProgressNotifier → sendMessage(channel, chatID, content) → Patch 更新
```

- `buildMainRunConfig` 设置 `cfg.ProgressNotifier = func(lines []string) { sendMessage(channel, chatID, lines[0]) }`
- `autoNotify := cfg.ProgressNotifier != nil`
- 每轮 LLM 循环发送进度：思考中 → 工具调用(⏳) → 工具完成(✅/❌)

### SubAgent 现状

- `buildSubAgentRunConfig` **不设置 ProgressNotifier** → SubAgent 零进度上报
- `spawnSubAgent` 中可通过 `resolveOriginIDs(msg)` 获取用户聊天的 channel/chatID
- `a.sendMessage(originChannel, originChatID, content)` 可直接向用户聊天发送消息
- **无需修改 ToolContext 或 InboundMessage**——直接在 `spawnSubAgent` 中构建 notifer

## 实施方案

### Bug 1: spawn_agent=false 时仍注入 agents catalog

**文件**: `agent/engine_wire.go` → `buildSubAgentRunConfig` (L245-247)

```go
// 现状：无条件注入
if agentsCatalog := a.agents.GetAgentsCatalog(parentCtx.SenderID); agentsCatalog != "" {
    sysPrompt += "\n" + agentsCatalog
}

// 修复：条件注入
if caps.SpawnAgent {
    if agentsCatalog := a.agents.GetAgentsCatalog(parentCtx.SenderID); agentsCatalog != "" {
        sysPrompt += "\n" + agentsCatalog
    }
}
```

### Bug 2: SubAgent prompt 模板引导 LLM 浪费轮次

**文件**: `agent/subagent_prompt.go`

**现状**: 模板固定告诉 SubAgent "大部分工具没有启用，需要 search_tools/load_tools"。
当 SubAgent 通过 `allowedTools` 白名单 + `ForceCoreTools()` 已经有了明确的工具集时，
这个提示导致 LLM 浪费 1-2 轮去调 search_tools/load_tools。

**修复**: 根据 `allowedTools` 是否非空选择不同模板：
- 有白名单 → 精简模板（不提 search_tools/load_tools，工具已全部可用）
- 无白名单 → 保持现有模板

在 `buildSubAgentRunConfig` 中：
```go
if len(allowedTools) > 0 {
    sysPrompt = fmt.Sprintf(subagentSystemPromptTemplateConcise, workDir, roleName, now)
} else {
    sysPrompt = fmt.Sprintf(subagentSystemPromptTemplate, workDir, roleName, now)
}
```

### Bug 3: 全局 agents 同步只执行一次

**文件**: `tools/skill_sync.go` → `skillSyncer`

**现状**: `synced map[string]bool` 永久缓存，每个用户只同步一次。

**修复**: `bool` → `time.Time`，每 5 分钟允许重新同步：
```go
type skillSyncer struct {
    mu     sync.Mutex
    synced map[string]time.Time // senderID → last sync time
}

func (s *skillSyncer) shouldSync(senderID string) bool {
    s.mu.Lock()
    defer s.mu.Unlock()
    last, ok := s.synced[senderID]
    if !ok || time.Since(last) > 5*time.Minute {
        s.synced[senderID] = time.Now()
        return true
    }
    return false
}
```

### 主功能: SubAgent 进度上报

**核心改动**（2 处）：

**1. `spawnSubAgent` (engine_wire.go)**：
```go
// 在构建 cfg 之后、Run 之前，设置 ProgressNotifier
originChannel, originChatID, _ := resolveOriginIDs(msg)
if originChannel != "" && originChatID != "" {
    rn := roleName // 闭包捕获
    cfg.ProgressNotifier = func(lines []string) {
        if len(lines) > 0 {
            prefixed := "📋 [" + rn + "] " + lines[0]
            _ = a.sendMessage(originChannel, originChatID, prefixed)
        }
    }
}
```

**2. `SpawnInteractiveSession` (interactive.go)**：
同样的逻辑，interactive session 也需要进度上报。

## 文件改动清单

| 文件 | 改动 |
|------|------|
| `tools/skill_sync.go` | synced bool → time.Time 定期重新同步 |
| `agent/subagent_prompt.go` | 新增精简版模板 |
| `agent/engine_wire.go` | bug1+2 修复；spawnSubAgent 设置 ProgressNotifier |
| `agent/interactive.go` | SpawnInteractiveSession 设置 ProgressNotifier |

## 执行顺序

1. Bug 1: agents catalog 条件注入（engine_wire.go, 1 行改动）
2. Bug 2: 精简版 SubAgent prompt 模板（subagent_prompt.go + engine_wire.go）
3. Bug 3: 定期重新同步（skill_sync.go）
4. 主功能: SubAgent 进度上报（engine_wire.go + interactive.go）
5. `go build` 验证编译
6. `go test ./...` 验证测试
7. 提交 PR
