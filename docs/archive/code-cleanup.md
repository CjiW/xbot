# xbot 代码清理 — 实施方案

> 上柱国拟旨，中书省/门下省会审  
> 时间：2026-03-20  
> 代码基线：master@10871a3（Fix/settings agent #200）

---

## 一、目标

对 xbot 全量代码进行 CR，完成：
1. 删除死代码
2. 抽象重复逻辑
3. 修复潜在 Bug
4. 提交 PR

---

## 二、审查发现

### 🔴 P0 — 死代码（21 个不可达函数）

| # | 文件 | 死函数 | 处理方式 |
|---|------|--------|---------|
| 1 | `pprof/pprof.go` | **整个文件**（DefaultConfig, NewServer, Start, Shutdown, statsHandler, gcHandler） | **删除整个文件** — main.go 无引用，config 仅有 PProfConfig struct 但未使用 |
| 2 | `logger/logger.go` | `SetFormatter`, `SetLevel` | 删除 — Init() 直接用 log.SetFormatter/SetLevel |
| 3 | `logger/logger.go` | `Errorf`, `Fatal`, `Fatalf` | 删除 — 全项目仅 pprof 用 Errorf（本身也是死代码） |
| 4 | `llm/mock.go` | `NewMockLLM` | 删除 — 测试中未使用 |
| 5 | `session/multitenant.go` | `WithArchivalService`, `WithToolIndexService`, `NewMultiTenantWithOptions` | 删除 — 三个 Option 函数无调用方 |
| 6 | `tools/mcp_common.go` | `ConvertMCPParams`, `LoadMCPConfig` | 删除 — 无调用方 |
| 7 | `tools/sandbox_runner.go` | `SetSandbox` | 删除 — 无调用方 |
| 8 | `tools/feishu_mcp/errors.go` | `IsAPIError`, `IsTokenError`, `IsPermissionError`, `IsNotFoundError` | 删除 — 四个错误判断函数无调用方 |
| 9 | `tools/feishu_mcp/feishu_mcp.go` | `NeedTokenError` | 删除 — 无调用方 |

**预估删除量：~200 行**

### 🟡 P1 — 重复逻辑

#### 1.1 channel/ 三渠道 isDuplicate 完全重复（~60 行冗余）
- `channel/feishu.go:1730` / `channel/onebot.go:813` / `channel/qq.go:1467`
- **方案**：提取到 `channel/common.go` 作为独立函数 `CheckDuplicate`

#### 1.2 channel/ feishu+qq 的 isAllowed 完全重复（~20 行冗余）
- `channel/feishu.go:1751` / `channel/qq.go:1492`
- **方案**：提取到 `channel/common.go` 作为 `IsSenderAllowed`

#### 1.3 tools/ 45 处重复的 json.Unmarshal 参数解析（最大冗余源）
- 几乎每个工具都有相同的模式：
  ```go
  var args XxxArgs
  if err := json.Unmarshal([]byte(input), &args); err != nil {
      return nil, fmt.Errorf("...")
  }
  ```
- **方案**：在 `tools/args.go` 中新增泛型辅助函数：
  ```go
  func ParseArgs[T any](input string) (*T, error)
  ```
- 减少约 180 行样板代码

### 🟢 P2 — 代码质量问题

#### 2.1 超大文件（需拆分）

| 文件 | 行数 | 建议 |
|------|------|------|
| `channel/feishu.go` | 2050 | 拆分 feishu_handler.go, feishu_settings.go（已部分拆分） |
| `channel/qq.go` | 1800 | 拆分 qq_handler.go, qq_api.go |
| `agent/agent.go` | 1770 | 拆分 agent_helpers.go |
| `agent/engine.go` | 1090 | 拆分 engine_run.go（Run 函数 634 行太长） |

> ⚠️ 文件拆分风险较高，本次暂不执行，记录到 Issue 中作为后续优化

#### 2.2 超长函数

| 文件 | 函数 | 行数 | 建议 |
|------|------|------|------|
| `agent/engine.go` | `Run()` | 634 | 应拆分为子函数 |
| `agent/agent.go` | `New()` | 222 | 可提取初始化步骤 |
| `agent/agent.go` | `formatToolProgress()` | 143 | 可简化 |

> ⚠️ 同上，本次暂不动

#### 2.3 `_ =` 忽略返回值（20 处）

大部分是合理的（如 `os.MkdirAll`、类型断言），但以下需关注：
- `tools/fetch.go:117,142` — `truncateByTokens` 返回 error 被忽略
- `tools/card_builder.go:481,494,498,520,587` — 类型断言无 ok 检查（可接受，JSON 值类型已知）
- `tools/logs.go:304` — `_ = time.Now()` 无用导入 hack

---

## 三、Bug 修复

### 🔴 B0-1 — Phase 1.5 压缩 missing structured markers（3 个 BUG 叠加）

**症状**：每次 Phase 1.5 压缩报 `missing_markers=457, quality_score=0.43, retention_rate=0.14`

**因果链**：
```
extractErrorMessages 从所有消息盲目提取含 error 的行（含代码片段、注释中的 "error"）
  → KeyInfoFingerprint.Errors 有 400+ 噪音项
  → ValidateMarkers 检查 457 项 → 几乎全部 missing
  → retention_rate 被拉低到 0.14
  → LLM 不遵守 @file: 格式 → countStructuredMarkers = 0
  → markerScore = 0 → quality_score 被拉低到 0.43
  → quality < 0.6 && retentionRate < 0.8 → 触发无意义的重试
```

**修复**：
1. `extractErrorMessages` 只从 tool result 提取 error，限制上限 20 条
2. `countStructuredMarkers` 放宽格式检测
3. `ValidateMarkers` 对 error 检查关键实体而非整行匹配

### 🔴 B0-2 — SubAgent 静默失败（Error 被吞）

**症状**：子 agent 经常"遇到瓶颈"，但进度无错误显示

**根因**：
| 位置 | 问题 |
|------|------|
| `engine_wire.go:754` | `spawnSubAgent` 返回 `out.OutboundMessage, nil` — Error 始终 nil |
| `interactive.go:122,223` | Interactive SubAgent 同理吞 Error |
| `retry.go` | `context.DeadlineExceeded` 不重试，直接失败 |
| SubAgent LLMTimeout=3min | 复杂任务偏短，超时后部分结果传回主 agent，主 agent LLM 自行描述为"遇到瓶颈" |

**修复**：
1. `spawnSubAgent` / Interactive 传播 `out.Error`
2. LLMTimeout 调整为 5-10min
3. `context.DeadlineExceeded` 允许重试 1 次

### 🟡 B1 — 类型断言无 comma-ok 保护
- `agent/llm_factory.go:84` — `val.(bool)` 应改为 `val, ok := val.(bool); if !ok { return false }`
- 影响低（sync.Map 中只存 bool），但不符合最佳实践

### 🟢 B2 — `tools/logs.go:304` 无用 import
- `_ = time.Now()` 仅为了让 time 包被导入，应改为 `_ "time"` 或删除（如果确实无其他引用）

---

## 四、执行计划

### 阶段 1：删除死代码（低风险）
- [ ] 删除 `pprof/pprof.go` 整个文件
- [ ] 删除 `logger/logger.go` 中 5 个死函数
- [ ] 删除 `llm/mock.go` 中 `NewMockLLM`
- [ ] 删除 `session/multitenant.go` 中 3 个死函数
- [ ] 删除 `tools/mcp_common.go` 中 2 个死函数
- [ ] 删除 `tools/sandbox_runner.go` 中 `SetSandbox`
- [ ] 删除 `tools/feishu_mcp/errors.go` 中 4 个死函数
- [ ] 删除 `tools/feishu_mcp/feishu_mcp.go` 中 `NeedTokenError`

### 阶段 2：抽象重复逻辑（中风险）
- [ ] 新建 `tools/args.go` 泛型辅助函数 `ParseArgs[T]`
- [ ] 重构 tools/ 中 45 处 json.Unmarshal 调用
- [ ] 新建 `channel/common.go`，提取 `CheckDuplicate` + `IsSenderAllowed`
- [ ] 重构三渠道引用

### 阶段 3：Bug 修复（低风险）
- [ ] 修复 `agent/llm_factory.go:84` 类型断言
- [ ] 修复 `tools/logs.go:304` 无用 import

### 阶段 4：Phase 1.5 压缩 markers 修复（中风险）
- [ ] `extractErrorMessages` 收敛误识别：只从 tool result 消息中提取 error，限制上限 20 条
- [ ] `countStructuredMarkers` 放宽格式检测：不要求严格 `@file:` 格式，改为检测结构化章节或文件路径/函数签名
- [ ] `ValidateMarkers` 对 error 类型只检查关键实体而非整行匹配
- [ ] 过滤 warning 中 error 类型的 missing（减少误报）

### 阶段 5：SubAgent 静默失败修复（中风险）
- [ ] 🔴 `spawnSubAgent` 传播 Error（当前始终返回 nil）
- [ ] 🔴 Interactive SubAgent 同理传播 Error
- [ ] 🟡 SubAgent LLMTimeout 从 3min 调整为 5-10min
- [ ] 🟡 对 context.DeadlineExceeded 允许重试 1 次
- [ ] 🟡 SubAgent 提示词强调"必须返回最终总结性结果文本"

### 阶段 6：验证与提交
- [ ] 运行 `go vet ./...`
- [ ] 运行 `go build ./...`
- [ ] 运行 `go test -race ./...`
- [ ] 创建分支，提交 PR

---

## 五、风险评估

| 阶段 | 风险等级 | 理由 |
|------|---------|------|
| 阶段 1 | 🟢 低 | deadcode 工具确认不可达，删除无副作用 |
| 阶段 2 | 🟡 中 | 泛型重构涉及大量文件改动，需充分测试 |
| 阶段 3 | 🟢 低 | 单点修复，影响范围小 |
| 阶段 4 | 🟡 中 | Phase 1.5 压缩质量检测逻辑修改，需验证不影响正常压缩 |
| 阶段 5 | 🟡 中 | SubAgent Error 传播涉及核心调用链，需充分测试 |
| 阶段 6 | 🟢 低 | 标准流程 |

---

## 六、预估改动

- 删除约 200 行死代码
- 净减少约 100 行重复代码（泛型替代后）
- 新增约 20 行公共代码
- 涉及约 40 个文件改动
- **净效果：-280 行，代码更精简**
- **新增修复**：Phase 1.5 压缩 markers 误报（3 个 BUG）、SubAgent Error 吞没（2 处严重 BUG + 3 处风险优化）
