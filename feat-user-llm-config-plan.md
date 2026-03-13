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
## Edit 工具 Bug 分析

### 根本原因
`tools/edit.go` 的 `sandboxReplace` 函数使用 `sed` 命令实现替换：
```go
cmd = fmt.Sprintf("sed -i 's/%s/%s/' '%s'", escapedOld, escapedNew, path)
```

**问题 1: sed 不支持多行匹配**
- sed 默认按行处理，无法匹配包含换行符的 `oldStr`
- 当 `oldStr` 包含 `
` 时，sed 会报错 `unknown option to 's'`

**问题 2: 分隔符冲突**
- 使用 `/` 作为 sed 分隔符
- 如果 `oldStr` 或 `newStr` 包含 `/`，sed 会解析错误

**问题 3: 转义不完整**
- 只转义了单引号 `'` -> `'\''`
- 未转义 sed 特殊字符：`/`、`&`、`\` 等

### 复现测试
### 复现测试结果（2026-03-13）

**测试环境**: Docker 沙箱

| 测试项 | 结果 | 错误信息 |
|--------|------|----------|
| 多行替换 | ❌ 失败 | `sed: -e expression #1, char 7: unterminated 's' command` |
| 包含 `/` 的内容 | ❌ 失败 | `sed: -e expression #1, char 11: unknown option to 's'` |
| 使用 `#` 分隔符 | ✅ 成功 | - |
| awk 多行处理 | ✅ 成功 | - |

### 修复方案

**方案 A（推荐）: 读取文件 → Go 中替换 → 写回**

修改 `sandboxReplace` 函数：
1. 使用 `cat` 读取文件内容
2. 在 Go 代码中使用 `strings.Replace` 处理（天然支持多行和特殊字符）
3. 使用 `printf` 写回文件

优点：
- 不依赖额外工具
- 复用已有逻辑（`doReplace` 函数）
- 完全解决多行和特殊字符问题

**方案 B: 使用 awk**

用 awk 替代 sed，可以处理多行但语法复杂。

**方案 C: 安装 Python**

使用 Python 进行文本处理，最灵活但增加依赖。

### 实现代码（方案 A）

```go
func (t *EditTool) sandboxReplace(ctx *ToolContext, path, oldStr, newStr string, replaceAll bool) (*ToolResult, error) {
    // 读取文件内容
    readCmd := fmt.Sprintf("cat '%s'", path)
    content, err := RunInSandboxWithShell(ctx, readCmd)
    if err != nil {
        return nil, fmt.Errorf("failed to read file: %v", err)
    }

    // 检查要替换的文本是否存在
    if !strings.Contains(content, oldStr) {
        return nil, fmt.Errorf("text not found: %q", oldStr)
    }

    // 在 Go 中进行替换（天然支持多行和特殊字符）
    var newContent string
    if replaceAll {
        newContent = strings.ReplaceAll(content, oldStr, newStr)
    } else {
        newContent = strings.Replace(content, oldStr, newStr, 1)
    }

    // 写回文件
    escapedContent := strings.ReplaceAll(newContent, "'", "'\''")
    writeCmd := fmt.Sprintf("printf '%%s' '%s' > '%s'", escapedContent, path)
    _, err = RunInSandboxWithShell(ctx, writeCmd)
    if err != nil {
        return nil, fmt.Errorf("failed to write file: %v", err)
    }

    summary := fmt.Sprintf("Successfully replaced in %s", path)
    diff := generateUnifiedDiff(content, newContent, path)
    return &ToolResult{Summary: summary, Detail: diff}, nil
}
```

### 需要修复的文件

- `tools/edit.go`: `sandboxReplace` 函数
- `tools/edit.go`: `sandboxRegexReplace` 函数（可能也有类似问题）

---

## 修复总结（2026-03-14 00:15）

### 问题根因
`tools/edit.go` 的 `sandboxReplace` 函数使用 `sed` 命令实现文本替换，存在以下问题：

1. **sed 不支持多行匹配** - 当 `old_string` 包含 `\n` 时直接报错
2. **分隔符冲突** - 内容包含 `/` 时 sed 解析失败
3. **转义不完整** - 只转义了单引号，未处理 `&`、`\` 等 sed 特殊字符

### 修复方案
**采用方案 A**：读取文件 → Go 中替换 → 写回

修改后的 `sandboxReplace` 函数：
```go
func (t *EditTool) sandboxReplace(...) (*ToolResult, error) {
    // 1. 读取文件内容
    content, err := RunInSandboxWithShell(ctx, "cat '"+path+"'")
    
    // 2. 在 Go 中进行替换（天然支持多行和特殊字符）
    newContent := strings.Replace(content, oldStr, newStr, 1)
    
    // 3. 使用 heredoc 写回文件
    writeCmd := fmt.Sprintf("cat > '%s' << 'XBOT_EOF'\n%s\nXBOT_EOF", path, newContent)
    _, err = RunInSandboxWithShell(ctx, writeCmd)
}
```

### 测试结果
所有测试通过 ✅

```
=== RUN   TestSandboxReplaceMultiline
    edit_test.go:26: ✅ 多行替换成功: "replaced\nline3"
=== RUN   TestSandboxReplaceWithSlash
    edit_test.go:43: ✅ 包含 / 的替换成功: "replaced\nanother/line"
=== RUN   TestSandboxReplaceWithSpecialChars
    ✅ ampersand, backslash, dollar, multiline_with_special 全部通过
=== RUN   TestSandboxReplaceAll
    edit_test.go:108: ✅ 全部替换成功
```

### 文件变更
- `tools/edit.go` - 修复 `sandboxReplace` 函数
- `tools/edit_test.go` - 新增单元测试
- `go.mod` - 修复 go 版本格式

### 提交记录
修复代码已就绪，待创建 PR 提交。
