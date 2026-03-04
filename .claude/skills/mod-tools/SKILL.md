---
name: mod-tools
description: Tools 模块知识库。修改 tools/*.go 时自动激活。
user-invokable: true
---

## 架构概览

`tools/` 目录实现 Agent 可调用的工具集，遵循统一的 `Tool` 接口：

```go
type Tool interface {
    Name() string
    Description() string
    Parameters() []llm.ToolParam
    Execute(toolCtx *ToolContext, input string) (*ToolResult, error)
}
```

工具通过 `DefaultRegistry()` 注册，Agent 在运行时查找并调用。

## 关键文件

| 文件 | 功能 |
|------|------|
| `interface.go` | Tool 接口定义、Registry、ToolContext、ToolResult |
| `mcp.go` | MCP 管理器、远程工具适配 |
| `mcp_common.go` | MCP 公共函数（连接、配置加载） |
| `session_mcp.go` | 会话级 MCP 管理（懒加载、超时卸载） |
| `feishu_mcp/*.go` | 飞书 MCP 工具封装（详见 feishu-mcp skill） |
| `card_builder.go` | 卡片构建器（详见 card-builder skill） |
| `card_tools.go` | 卡片工具（详见 card-builder skill） |

## 核心数据结构

### ToolResult

```go
type ToolResult struct {
    Summary     string // 精简结果，进入 LLM 上下文
    Detail      string // 详细内容，仅前端展示
    Tips        string // 操作指引，帮助 LLM 理解下一步
    WaitingUser bool   // 是否等待用户响应
}
```

**辅助函数**：
- `NewResult(content)` - 创建简单结果
- `NewResultWithDetail(summary, detail)` - 创建带详情的结果
- `NewResultWithTips(summary, tips)` - 创建带指引的结果
- `NewResultWithUserResponse(summary)` - 创建等待用户响应的结果

### ToolContext

```go
type ToolContext struct {
    Ctx                     context.Context
    WorkingDir              string
    Channel, ChatID, SenderID, SenderName string
    SendFunc                func(channel, chatID, content string) error
    Registry                *Registry
    // ... 其他字段
}
```

## 约定与规范

### Description 简洁原则
- 工具描述应简洁，只说明功能
- 不要在 Description 中写步骤指引（如 "STEP 1/2/3"）
- 操作指引应放在返回结果的 `Tips` 字段中

### 参数描述最佳实践

**核心原则**：让 LLM 理解参数从哪里来、格式是什么。

**必须包含**：
1. 参数的**来源**（从哪里获取）
2. **具体示例**（展示格式）
3. **反例**（避免什么）

```go
// ❌ 差的描述 - LLM 可能传入数字 ID
Description: "Node token (e.g., wikcnXXXXX)"

// ✅ 好的描述 - 明确从 URL 提取
Description: "Token from Feishu URL path. From https://xxx.feishu.cn/wiki/XXXXX, use XXXXX. NOT a numeric ID."
```

**更多飞书相关参数描述模板，详见 feishu-mcp skill**

### 返回结果
- 成功时使用 `NewResult()` 或 `NewResultWithTips()`
- `Tips` 字段告诉 LLM 下一步可以做什么
- 错误通过返回 `error` 传递，由 Agent 统一处理

### URL 构建规范
- 不要在代码中硬编码占位域名（如 `xxx.feishu.cn`）
- 使用 `Client.BuildURL(token, objType)` 构建完整 URL
- 企业域名在 OAuth 授权时自动获取并存储

## 相关 Skills

- **feishu-mcp** - 飞书 MCP 工具开发详解（URL 结构、token 格式、API 调用）
- **card-builder** - 卡片构建器和卡片工具

## 常见陷阱

### 1. 参数描述不清晰
**问题**：参数描述只写 "token (e.g., wikcnXXXXX)"，LLM 传入错误格式

**解决**：说明来源、给出示例、明确反例。详见 feishu-mcp skill

### 2. Description 过于冗长
**问题**：在 Description 中写步骤指引，浪费 LLM context

**解决**：Description 只写功能描述，操作指引放 Tips 字段

### 3. 忘记注册新工具
**问题**：实现了 Tool 接口但未注册

**解决**：在 `main.go` 中调用 `agentLoop.RegisterTool()`

### 4. 飞书相关陷阱
详见 **feishu-mcp skill**：
- Token 类型混用
- Wiki API obj_type 误用
- 硬编码占位域名

## 变更历史

- 2026-03-04: 分离 feishu-mcp 为独立 skill，简化本文档
- 2026-03-04: 添加参数描述最佳实践，添加"参数描述不清晰"陷阱
- 2026-03-04: 添加 Wiki API obj_type 参数误用陷阱
- 2026-03-04: 添加 Token 类型混用陷阱，修正企业域名获取方式
- 2026-03-04: 分离 card-builder 为独立 skill，添加 ExpectedInteractions 跟踪
- 2025-03-04: 添加 ToolResult.Tips 字段，优化飞书工具 Description，支持企业域名自动获取
- 2025-03-03: 修复 Windows 编译问题，分离 shell.go 为跨平台实现
