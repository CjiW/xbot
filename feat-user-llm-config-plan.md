# 实现计划：支持用户自定义 LLM API 和 Key

## Issue 链接
https://github.com/CjiW/xbot/issues/71

## 当前状态

已有部分实现：
- ✅ 数据库表 `user_llm_configs` 已创建
- ✅ `UserLLMConfigService` 服务层已完成（CRUD 操作）
- ❌ agent.go 中的命令处理代码位置错误（嵌套在 `/prompt` 分支内）
- ❌ 缺少 `handleSetLLM` 和 `handleGetLLM` 实现
- ❌ 缺少 LLM 客户端动态切换逻辑

## 实现步骤

### 1. 修复 agent.go 命令处理位置
**文件**: `agent/agent.go`

当前错误代码：
```go
if strings.HasPrefix(cmd, "/prompt") {
    if strings.HasPrefix(cmd, "/set-llm") {  // 错误：嵌套在 /prompt 内
        ...
    }
    return a.handlePromptQuery(ctx, msg, tenantSession)
}
```

正确代码：
```go
if strings.HasPrefix(cmd, "/set-llm") {
    return a.handleSetLLM(ctx, msg)
}
if cmd == "/llm" {
    return a.handleGetLLM(ctx, msg)
}
if strings.HasPrefix(cmd, "/prompt") {
    return a.handlePromptQuery(ctx, msg, tenantSession)
}
```

### 2. 添加 UserLLMConfigService 到 Agent
**文件**: `agent/agent.go`

- 在 `Agent` 结构体中添加 `llmConfigService *sqlite.UserLLMConfigService`
- 在 `New` 函数中初始化服务
- 添加 `GetUserLLMConfig(senderID)` 方法供外部调用

### 3. 实现 `/set-llm` 命令
**文件**: `agent/agent.go`

支持的 provider：
- `openai` - OpenAI API（需要 `--key`，可选 `--model`）
- `anthropic` - Anthropic Claude API（需要 `--key`，可选 `--model`）
- `deepseek` - DeepSeek API（需要 `--key`，可选 `--model`）
- `codebuddy` - CodeBuddy 企业版（需要 `--user-id`, `--enterprise-id`, `--domain`）
- `siliconflow` - 硅基流动（需要 `--key`，可选 `--model`）

命令格式：
```
/set-llm <provider> --key <api-key> [--model <model-name>]
/set-llm codebuddy --user-id <id> --enterprise-id <id> --domain <domain>
/llm              # 查看当前配置
```

### 4. 实现动态 LLM 客户端切换
**文件**: `agent/agent.go` 或新建 `agent/llm_factory.go`

方案选择：
- **方案 A**：在 Agent 初始化时创建多个 LLM 客户端缓存
- **方案 B**（推荐）：在 `runLoop` 中根据 senderID 动态获取 LLM 客户端

需要修改：
- `runLoop` 方法：在 LLM 调用前检查用户配置
- 添加 `getUserLLMClient(senderID)` 方法：返回用户配置的 LLM 客户端或默认客户端

### 5. 安全考虑
- API Key 不在日志中显示（mask 处理）
- `/llm` 命令只显示 provider 和 model，不显示完整 key（显示 `sk-***xxx` 格式）
- 配置存储在 SQLite 中，文件权限 0600

### 6. 测试验证
- `go fmt ./...` 通过
- `go vet ./...` 通过
- `go build ./...` 通过
- 手动测试命令功能

## 文件变更清单

| 文件 | 变更类型 | 说明 |
|------|----------|------|
| `agent/agent.go` | 修改 | 添加命令处理、动态 LLM 切换 |
| `storage/sqlite/db.go` | 已完成 | 数据库表和迁移 |
| `storage/sqlite/user_llm_config.go` | 已完成 | CRUD 服务 |

## 实现优先级

1. **P0** - 修复命令处理位置
2. **P0** - 实现 `handleSetLLM` 和 `handleGetLLM`
3. **P1** - 实现动态 LLM 客户端切换
4. **P2** - 安全加固（key masking）

## 预计工作量

- 核心功能：约 150 行代码
- 测试验证：10 分钟## Edit 工具错误记录

### 错误 1: 2026-03-13 23:45
```
Error: failed to replace text: sandbox command failed: exit status 1, output: 
[stderr] sed: -e expression #1, char 6: unknown option to `s'
```

**尝试的替换内容**:
```
old_string: // User LLM config service
	llmConfigSvc *sqlite.UserLLMConfigService
new_string: // User LLM config service and factory
	llmConfigSvc *sqlite.UserLLMConfigService
	llmFactory   *LLMFactory
```

**分析**: 这个错误是 sed 命令的语法问题。old_string 中包含换行符，而 sed 默认按行处理，无法正确匹配跨行内容。Edit 工具的实现可能需要改进以支持多行替换。

**解决方案**: 使用 Shell 直接编辑文件。
