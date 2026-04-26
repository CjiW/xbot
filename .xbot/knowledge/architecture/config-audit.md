# 🔍 xbot Config Architecture Audit Report

**审查人**: 太子（代理监国）  
**日期**: 2026-04-26  
**范围**: config/config.go, cmd/xbot-cli/main.go, serverapp/setting_handlers.go, channel/cli_helpers.go, agent/llm_factory.go

---

## Executive Summary

xbot 的配置系统存在 **5 个架构级缺陷** 和 **12 个具体问题**。根因可以归结为一句话：

> **配置项的读写路径不唯一，数据散布在 config.json、SQLite DB (user_settings / user_llm_subscriptions)、环境变量、内存缓存 四个位置，没有任何一个权威层（Authority Layer）来统一裁决。**

---

## 缺陷 #1: LLM 配置三源写入 — 最危险的缺陷

### 现象
一个用户的 LLM 凭证 (provider/base_url/api_key/model) 可以存在于以下位置：
1. **config.json** 的 `LLM` 字段（或 `Subscriptions[]` 数组）
2. **SQLite DB** 的 `user_llm_subscriptions` 表
3. **SQLite DB** 的 `user_llm_config` 表（旧单配置，已部分弃用但仍被读取）
4. **LLMFactory 内存缓存**（clients/models/maxOutputTokens/thinkingModes map）

### 读写路径
```
写入路径 (至少 5 条):
  1. ApplySettings → updateActiveSubscription → DB user_llm_subscriptions
  2. ApplySettings → applyCLISettingsToConfig → cfg.LLM.* (内存) → saveCLIConfig → config.json
  3. loadLLMFromDBSubscription → cfg.LLM.* (从 DB 覆盖内存 config)
  4. syncLLMFromActiveSub → cfg.LLM.* (从 config.json Subscriptions[] 覆盖)
  5. LLMFactory.SwitchSubscription → f.defaultLLM (直接修改工厂默认值)

读取路径 (至少 4 条):
  1. GetLLM() → cache → subscriptionSvc.GetDefault → defaultLLM (3 级 fallback)
  2. GetCurrentValues() → cfg.LLM.* + activeSub.* (两源合并)
  3. loadLLMFromLocalDB → user_llm_config 表 (旧路径)
  4. currentActiveSubscription → DB default → config Subscriptions[] (fallback)
```

### 为什么出 bug
- **写入路径 2** (config.json) 和 **写入路径 1** (DB subscription) 保存的是同一个逻辑值的不同副本
- 系统启动时 `loadLLMFromDBSubscription` 把 DB 值写回 cfg.LLM，但 `syncLLMFromActiveSub` 又把 config.json 的值覆盖回去
- **执行顺序决定最终结果**，而不是数据源优先级
- `saveCLIConfig` 保存的 `cfg.LLM` 可能已经被 DB 值污染，下次启动时 config.json 和 DB 的值相互覆盖

### 影响范围
- 几十次 config 相关 bug 的主要来源
- 修改 API Key 后不生效（被旧副本覆盖）
- 订阅切换后 config.json 写回旧值

---

## 缺陷 #2: Setting Scope 分类散乱，无编译期保障

### 现象
一个 setting key 的 scope（global/user/subscription/action）由 4 个独立的 map 定义：
```go
// channel/cli_helpers.go
var cliUserScopedSettingKeys      = map[string]struct{}{...}
var cliGlobalScopedSettingKeys    = map[string]struct{}{...}
var cliActionSettingKeys          = map[string]struct{}{...}
var cliSubscriptionScopedSettingKeys = map[string]struct{}{...}
```

加上两套 handler registry：
```go
// serverapp/setting_handlers.go
var settingHandlerRegistry = map[string]settingHandler{...}

// cmd/xbot-cli/setting_handlers.go
var cliSettingHandlers = map[string]cliSettingHandler{...}
```

以及一个 runtime key list：
```go
var CLIRuntimeSettingKeys = []string{...}
```

### 为什么出 bug
- 新增 setting key 需要同时修改 **5 个地方**，遗漏任何一个都无编译期错误
- `isKnownNonRuntimeKey()` 在 CLI 端硬编码了另一个子集
- 没有单一枚举来定义"所有合法的 setting key"

---

## 缺陷 #3: GetLLMForModel 的 4 级 Fallback 链

### 现象
`GetLLMForModel()` 的查找逻辑：
```
1. tierModel 精确匹配 → cache (modelMap)
2. config.json subscription 精确匹配
3. config.json subscription provider 猜测匹配
4. DB subscription CachedModels 精确匹配
5. DB subscription API 动态加载匹配
6. DB subscription provider 猜测匹配
7. 最终 fallback: 用默认 LLM + 默认 model（忽略 targetModel）
```

### 为什么出 bug
- **7 级 fallback**，任何一级的错误都会被下一级掩盖
- `guessProvider` 基于 model 名称子串匹配（`strings.Contains(model, "gpt")`），极易误判
- API 动态加载（`LoadModelsFromAPI`）有 15 秒超时，在查找路径中执行 I/O 是架构违规
- 最终 fallback 丢弃 targetModel，静默使用错误模型

---

## 缺陷 #4: applyEnvOverrides 的零值检测是隐式 fallback

### 现象
`config/config.go` 的 `applyEnvOverrides` 中，部分字段只在 config 值为零时才覆盖：
```go
if v := os.Getenv("AGENT_MAX_ITERATIONS"); v != "" {
    if i, err := strconv.Atoi(v); err == nil && cfg.Agent.MaxIterations == 0 {  // 零值检测!
        cfg.Agent.MaxIterations = i
    }
}
```

但其他字段没有零值检测：
```go
if v := os.Getenv("LLM_MODEL"); v != "" {
    cfg.LLM.Model = v  // 直接覆盖，无论原值
}
```

### 为什么出 bug
- 同一个函数内两种策略混用，无文档说明哪些字段有零值检测
- 用户在 config.json 设置 `max_iterations: 100`，环境变量 `AGENT_MAX_ITERATIONS=50` 会被忽略（因为 100 ≠ 0）
- 但用户设置 `model: gpt-4o`，环境变量 `LLM_MODEL=gpt-4.1` 会生效
- 行为不一致，难以预测

---

## 缺陷 #5: enable_auto_compress 与 context_mode 的别名冲突

### 现象
`enable_auto_compress` 和 `context_mode` 是同一个逻辑值的两种表示：
- `enable_auto_compress=true` → `context_mode=auto`
- `enable_auto_compress=false` → `context_mode=none`

为了处理两者同时出现的情况，`applyRuntimeSettings` 有特殊排序逻辑：
```go
// Process all keys except context_mode first
for k, v := range values {
    if k == "context_mode" { continue }  // skip!
    ...
}
// Process context_mode last so it overrides enable_auto_compress
if v, ok := values["context_mode"]; ok ...
```

### 为什么出 bug
- Map 遍历顺序在 Go 中是不确定的，这迫使使用特殊排序
- 两处（serverapp + CLI）都需要复制这个排序逻辑
- `enable_auto_compress` 已标记为 legacy 但仍然并存

---

## 具体问题清单

| # | 问题 | 位置 | 严重性 |
|---|------|------|--------|
| 1 | `loadLLMFromLocalDB` 读取旧表 `user_llm_config`，与 subscription 系统并存 | main.go:236 | 🔴 High |
| 2 | `loadLLMFromDBSubscription` 把 DB 值写回 `cfg.LLM.*`，污染 config 原始值 | main.go:275 | 🔴 High |
| 3 | `syncLLMFromActiveSub` 从 config.json subscriptions 覆盖 cfg.LLM | main.go:2108 | 🟡 Medium |
| 4 | `saveCLIConfig` 写入 `cfg.LLM` 和 `cfg.Agent`，可能包含 DB 污染的值 | main.go:139 | 🔴 High |
| 5 | `LLMFactory.GetLLM` 有 3 级查找链 (cache → DB sub → default) | llm_factory.go:89 | 🟡 Medium |
| 6 | `HasCustomLLM` 查 3 个源 (cache → configSvc → subscriptionSvc) | llm_factory.go:166 | 🟡 Medium |
| 7 | `SwitchSubscription` 对 `cli_user` 特殊修改 `defaultLLM` | llm_factory.go:280 | 🟡 Medium |
| 8 | `GetLLMForModel` 7 级 fallback 含 I/O 操作和猜测匹配 | llm_factory.go:646 | 🔴 High |
| 9 | `GetCurrentValues` 同时读取 cfg.LLM 和 activeSub，两源合并 | main.go:804 | 🟡 Medium |
| 10 | `refreshRemoteValuesCache` 硬编码默认值 (`"none"`, `"flat"`, `"30"`) | main.go:87-101 | 🟡 Medium |
| 11 | `saveServerConfig` 和 `saveCLIConfig` 保存不同字段子集 | server/server.go:952, main.go:127 | 🟡 Medium |
| 12 | `guessProvider` 基于子串匹配，`"o1"` 和 `"o3"` 会误匹配含 "o1" 的非 OpenAI 模型 | llm_factory.go:961 | 🟡 Medium |

---

## 修复架构设计

### 核心原则
1. **每个配置项只有一个权威数据源** (Single Source of Truth)
2. **写入路径唯一** — 每个配置项只有一条写入路径
3. **读取通过统一的 ConfigProvider 接口** — 消费者不关心数据在哪
4. **编译期保障** — 新增 setting key 只需改一处

### Phase 1: 统一 LLM 配置数据源

**目标**: 消除 config.json 和 DB 的 LLM 凭证双写

**方案**:
- **启动时**: config.json `Subscriptions[]` → seed 到 DB → 清空 config.json 的 `LLM.*` 副本
- **运行时**: 只从 DB `user_llm_subscriptions` 读写，cfg.LLM 不再保存凭证
- **保存时**: `saveCLIConfig` / `saveServerConfig` 不再写 `cfg.LLM`

### Phase 2: Setting Key Registry（编译期安全保障）

**方案**: 定义统一的 Setting Key 类型系统：
```go
// channel/setting_keys.go
type SettingKey string

const (
    SettingLLMProvider      SettingKey = "llm_provider"
    SettingLLMModel         SettingKey = "llm_model"
    // ...
)

type SettingScope int
const (
    ScopeGlobal       SettingScope = iota
    ScopeUser
    ScopeSubscription
    ScopeAction
)

type SettingDef struct {
    Key    SettingKey
    Scope  SettingScope
    Runtime bool  // needs runtime apply beyond DB persist
}

// AllSettingDefs is the single registry
var AllSettingDefs = []SettingDef{...}

// 自动生成 scope maps, runtime keys, knownNonRuntimeKeys
```

### Phase 3: 消除 GetLLMForModel 的 Fallback 链

**方案**: 
- SubAgent 模型解析不再猜测，只走精确匹配
- Tier model 必须在某个 subscription 的 CachedModels 中，否则报错
- 去掉 `guessProvider`

### Phase 4: 消除 enable_auto_compress 别名

**方案**: 
- 去掉 `enable_auto_compress`，统一用 `context_mode`
- 启动时自动转换旧值

### Phase 5: 统一 applyEnvOverrides 策略

**方案**: 
- 环境变量始终覆盖 config.json（当前行为不一致的原因是部分代码试图避免覆盖"有意义的零值"）
- 用 `*int` / `*bool` 指针类型区分"未设置"和"值为零"

---

*审查完毕。修复工作将按 Phase 顺序执行。*

---

## 📋 修复进度 (2026-04-26)

### ✅ Phase 1: LLM 凭证双写修复
**Commit**: `0025206 fix: prevent LLM credential double-write between config.json and DB`
- `saveCLIConfig` 和 `saveServerConfig` 不再将 `cfg.LLM` 凭证写回 config.json（当 subscriptions 存在时）
- 只写 tier model 字段 (VanguardModel/BalanceModel/SwiftModel)
- 消除了 `loadLLMFromDBSubscription → cfg.LLM → saveCLIConfig → config.json` 的数据循环

### ✅ Phase 2: Setting Key Registry
**Commit**: `c248bc8 refactor: unify setting key registry into channel/setting_keys.go`
- 新建 `channel/setting_keys.go`：所有 28 个 setting key 的单一注册表
- 从 `cli_helpers.go` 删除 4 个冗余 scope map + runtime key list
- Scope 分类、runtime key 列表全部从 `AllSettingDefs` 自动推导

### ✅ Phase 3: GetLLMForModel 简化
**Commit**: `175a6b2 refactor: simplify GetLLMForModel — remove 7-level fallback chain`
- 从 7 级 fallback 简化为 4 步精确查找
- 移除 `guessProvider` 在查找路径中的使用（基于子串匹配不可靠）
- 删除 76 行复杂的 fallback 逻辑

### ✅ Phase 4: applyEnvOverrides 一致性
**Commit**: `473bf4f fix: unify applyEnvOverrides strategy — env vars always override config.json`
- 移除 6 个字段的零值检测（AGENT_MAX_ITERATIONS 等）
- 统一策略：环境变量始终覆盖 config.json

### ⏳ Phase 5: enable_auto_compress 别名消除
**状态**: 未实施 — 需要数据库迁移，风险较高，建议单独 PR

---

*审查 + 修复完成。4 个 commits，+517/-247 行，所有测试通过。*
