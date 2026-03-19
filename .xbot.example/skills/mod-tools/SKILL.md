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
| `feishu_mcp/*.go` | 飞书 MCP 工具封装 |

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

### 返回结果
- 成功时使用 `NewResult()` 或 `NewResultWithTips()`
- `Tips` 字段告诉 LLM 下一步可以做什么
- 错误通过返回 `error` 传递，由 Agent 统一处理

### URL 构建规范
- 不要在代码中硬编码占位域名（如 `xxx.feishu.cn`）
- 使用 `Client.BuildURL(token, objType)` 构建完整 URL
- 企业域名在 OAuth 授权时自动获取并存储

## 飞书 MCP 工具

### 架构

```
FeishuMCP → OAuth Manager → FeishuProvider → Lark Client
                ↓
         Token.Raw["tenant_domain"]  ← 自动获取企业域名
```

### Client 结构

```go
type Client struct {
    lark         *lark.Client
    accessToken  string
    tenantDomain string  // 企业域名，如 "example.feishu.cn"
}

// 构建完整 URL
func (c *Client) BuildURL(token, objType string) string
```

### Token 类型自动检测

| 前缀 | 类型 |
|------|------|
| `wikcn` | wiki |
| `doxcn` | docx |
| `basc` | bitable |
| `shtcn` | sheet |
| `ndtbn` | mindnote |
| `pptcn` | slides |
| `filcn` | file |

### 企业域名获取

在 `oauth/providers/feishu.go` 中，OAuth 授权成功后自动调用：

```go
resp, err := p.client.Tenant.V2.Tenant.Query(ctx, larkcore.WithUserAccessToken(accessToken))
// 存储到 token.Raw["tenant_domain"]
```

## 常见陷阱

### 1. Description 过于冗长
**问题**：在 Description 中写步骤指引，浪费 LLM context

**解决**：Description 只写功能描述，操作指引放 Tips 字段

### 2. 硬编码占位域名
**问题**：使用 `xxx.feishu.cn` 会导致 LLM 输出无效 URL

**解决**：使用 `client.BuildURL()` 构建真实 URL

### 3. 忘记注册新工具
**问题**：实现了 Tool 接口但未注册

**解决**：在 `main.go` 中调用 `agentLoop.RegisterTool()`

## 变更历史

- 2025-03-04: 添加 ToolResult.Tips 字段，优化飞书工具 Description，支持企业域名自动获取
- 2025-03-03: 修复 Windows 编译问题，分离 shell.go 为跨平台实现
