---
name: feishu-mcp
description: Use when modifying tools/feishu_mcp/*.go or adding new Feishu MCP tools
user-invokable: true
---

## 架构概览

飞书 MCP 工具封装飞书开放平台 API，通过 OAuth 获取用户授权后调用。

```
FeishuMCP → OAuth Manager → FeishuProvider → Lark Client
                ↓
         Token.Raw["tenant_domain"]  ← 自动获取企业域名
```

## 关键文件

| 文件 | 功能 |
|------|------|
| `feishu_mcp.go` | FeishuMCP 结构、Client 封装、OAuth 集成 |
| `tools.go` | SearchWikiTool |
| `search.go` | Wiki 相关工具（ListSpaces, ListNodes, GetNode, MoveNode, CreateNode） |
| `wiki.go` | Bitable 相关工具（Fields, Records, CreateRecord, UpdateRecord） |
| `docx.go` | 文档相关工具（Upload, Download） |
| `drive.go` | 云空间相关工具（CreateDocx） |
| `errors.go` | API 错误封装、URL 构建函数 |

## 核心数据结构

### Client

```go
type Client struct {
    lark         *lark.Client
    accessToken  string
    tenantDomain string  // 企业域名，如 "example.feishu.cn"
}

// 构建完整 URL（含域名）
func (c *Client) BuildURL(token, objType string) string
```

### 获取 Client

```go
client, err := t.MCP.GetClient(ctx.Ctx, ctx.Channel, ctx.ChatID)
if err != nil {
    return nil, err  // 可能是 TokenNeededError
}

// 使用 user access token 调用 API
resp, err := client.Client().Wiki.V2.Space.GetNode(ctx.Ctx, req,
    larkcore.WithUserAccessToken(client.AccessToken()))
```

## 飞书 URL 结构

```
https://example.feishu.cn/wiki/VYaWwsuYZiTMhxk8sMhc0T4vnMc
                       ↑     ↑
                    obj_type  token
```

| URL 路径 | obj_type | token 示例 |
|----------|----------|------------|
| `/wiki/XXXXX` | wiki | `VYaWwsuYZiTMhxk8sMhc0T4vnMc` |
| `/docx/XXXXX` | docx | `doxcnXXXXX` |
| `/base/XXXXX` | bitable | `bascXXXXX` |
| `/sheet/XXXXX` | sheet | `shtcnXXXXX` |
| `/mindnote/XXXXX` | mindnote | `ndtbnXXXXX` |
| `/slides/XXXXX` | slides | `pptcnXXXXX` |
| `/file/XXXXX` | file | `filcnXXXXX` |

**注意**：URL path token 和 API 内部 token 格式可能不同：
- URL path: `VYaWwsuYZiTMhxk8sMhc0T4vnMc`（用户可见）
- API node token: `wikcn7005355247189501441`（API 返回）

## 参数描述最佳实践

**核心原则**：让 LLM 理解参数从哪里来、格式是什么。

### 必须包含

1. **来源说明**：从哪里获取这个参数
2. **具体示例**：展示真实格式
3. **反例警告**：避免传入什么

### 示例

```go
// ❌ 差 - LLM 可能传入数字 ID
Description: "Node token (e.g., wikcnXXXXX)"

// ✅ 好 - 明确从 URL 提取
Description: "Token from Feishu URL path. From https://xxx.feishu.cn/wiki/XXXXX, use XXXXX. NOT a numeric ID."

// ✅ 好 - Space ID 说明
Description: "Wiki space ID (numeric string like '7123456789012345678', from feishu_wiki_list_spaces). NOT the URL path token."
```

### 不同类型参数的描述模板

```go
// URL path token
Description: "Token from Feishu URL path. From https://xxx.feishu.cn/wiki/XXXXX, use XXXXX. NOT a numeric ID."

// Space ID（数字字符串）
Description: "Wiki space ID (numeric string from feishu_wiki_list_spaces)."

// Bitable token
Description: "Bitable app token from URL (e.g., bascXXXXX)."

// Table ID
Description: "Table ID from URL (e.g., tblXXXXX)."
```

## Token 类型自动检测

| 前缀 | 类型 | obj_type 处理 |
|------|------|---------------|
| `wikcn` | wiki node | 不传 obj_type，让 API 自动检测 |
| `doxcn` | docx | obj_type = "docx" |
| `basc` | bitable | obj_type = "bitable" |
| `shtcn` | sheet | obj_type = "sheet" |
| `ndtbn` | mindnote | obj_type = "mindnote" |
| `pptcn` | slides | obj_type = "slides" |
| `filcn` | file | obj_type = "file" |

**重要**：`obj_type` 描述的是**文档类型**，不是节点类型。对于 wiki node token，不应传 `obj_type`。

## Wiki 创建节点 (feishu_wiki_create_node)

创建 Wiki 节点时，必须指定 `node_type`：

| node_type | 用途 | 必需参数 |
|-----------|------|----------|
| `origin` | 创建新文档 | `space_id`, `obj_type` |
| `shortcut` | 创建现有文档的快捷方式 | `space_id`, `obj_type`, `origin_node_token` |

### API 请求体示例

```json
// 创建新文档（作为子节点）：
{
    "obj_type": "docx",
    "parent_node_token": "wikcnKQ1k3p...",
    "node_type": "origin"
}

// 创建新文档（作为空间一级节点）：
{
    "obj_type": "docx",
    "node_type": "origin"
}

// 创建快捷方式：
{
    "obj_type": "docx",
    "parent_node_token": "wikcnKQ1k3p...",
    "node_type": "shortcut",
    "origin_node_token": "wikcnABC123..."
}
```

### SDK 调用

```go
nodeBuilder := wikiv2.NewNodeBuilder().
    ObjType("docx").
    NodeType("origin")  // 必须设置！

if args.ParentNodeToken != "" {
    nodeBuilder.ParentNodeToken(args.ParentNodeToken)
}

req := wikiv2.NewCreateSpaceNodeReqBuilder().
    SpaceId(args.SpaceID).
    Node(nodeBuilder.Build()).
    Build()
```

**注意**：如果不设置 `node_type`，API 会返回 `field validation failed (code: 99992402)` 错误。

## 常见陷阱

### 1. 参数描述不清晰

**问题**：LLM 传入数字 ID（如 `wikcn7005355247189501441`）而非 URL path token

**解决**：描述中明确说明从 URL 提取，给出示例

### 2. Token 类型混用

**问题**：在 app-level client 上使用 `larkcore.WithUserAccessToken()` 导致：
```
tenant token type not match user access token
```

**解决**：app-level client 使用自动管理的 tenant_access_token，不要混用

### 3. Wiki API obj_type 误用

**问题**：对 wiki node token 传入 `obj_type=wiki`，导致 API 返回 not found

**解决**：`obj_type` 是文档类型，对于 wiki node token 不传 obj_type

### 4. 硬编码占位域名

**问题**：使用 `xxx.feishu.cn` 导致 LLM 输出无效 URL

**解决**：使用 `client.BuildURL(token, objType)` 构建真实 URL

### 5. 忘记 WithUserAccessToken

**问题**：调用 API 时忘记传 user access token

**解决**：
```go
resp, err := client.Client().Wiki.V2.Space.GetNode(ctx.Ctx, req,
    larkcore.WithUserAccessToken(client.AccessToken()))
```

### 6. Wiki 创建节点缺少 node_type

**问题**：调用 wiki create node API 时忘记设置 `node_type`，返回错误：
```
field validation failed (code: 99992402)
```

**解决**：必须设置 `node_type` 为 `"origin"`（新建文档）或 `"shortcut"`（快捷方式）：
```go
nodeBuilder := wikiv2.NewNodeBuilder().
    ObjType("docx").
    NodeType("origin")  // 必须设置！
```

## 返回结果规范

### 使用 Tips 指引下一步

```go
return tools.NewResultWithTips(
    "No matching results found",
    "Try different search keywords or use feishu_wiki_list_spaces to browse all Wiki spaces.",
), nil
```

### 返回 URL 而非 token

```go
node := map[string]any{
    "node_token": nodeToken,
    "url":        client.BuildURL(nodeToken, objType),  // 用户可直接点击
}
```

## 变更历史

- 2026-03-04: 添加 Wiki 创建节点 `node_type` 必填说明、新增陷阱 6
- 2026-03-04: 初始创建，包含参数描述最佳实践、URL 结构说明
