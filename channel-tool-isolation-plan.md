# 渠道工具隔离 + 飞书上传文件方案

**状态**: ✅ 已完成（2026-03-16）

## 背景

当前问题：
1. 飞书工具在 QQ 渠道能搜出来，反之亦然（渠道不隔离）
2. 缺少飞书上传文件工具

## 目标

1. **渠道隔离**：飞书工具仅对飞书渠道可见，QQ 工具仅对 QQ 渠道可见 ✅
2. **飞书上传文件**：支持上传文件到飞书云空间 ✅

---

## 方案一：渠道工具隔离 ✅

### 已实现

#### 1. 新增接口 (`tools/interface.go`)

```go
// ChannelProvider 渠道提供者接口
type ChannelProvider interface {
    SupportedChannels() []string // 返回支持的渠道列表，空则表示所有渠道
}

// IsChannelSupported 检查工具是否支持指定渠道
func IsChannelSupported(tool Tool, channel string) bool
```

#### 2. 飞书工具实现 (`tools/feishu_mcp/feishu_mcp.go`)

```go
func (b FeishuToolBase) SupportedChannels() []string {
    return []string{"feishu"}
}
```

#### 3. Registry 方法扩展 (`tools/interface.go`)

- `GetToolGroupsForChannel(channel string) []ToolGroupEntry`
- `GetToolSchemasForChannel(sessionKey string, toolNames []string, channel string) []ToolSchema`

#### 4. 搜索过滤 (`tools/search_tools.go`)

- 使用 `GetToolGroupsForChannel(ctx.Channel)` 替代 `GetToolGroups()`

#### 5. 加载过滤 (`tools/load_tools.go`)

- 使用 `GetToolSchemasForChannel(sessionKey, toolNames, ctx.Channel)` 替代 `GetToolSchemas()`

### 改动文件

| 文件 | 改动 | 状态 |
|------|------|------|
| `tools/interface.go` | 新增 `ChannelProvider` 接口 + `IsChannelSupported` + 渠道过滤方法 | ✅ |
| `tools/feishu_mcp/feishu_mcp.go` | 实现 `SupportedChannels()` 返回 `["feishu"]` | ✅ |
| `tools/search_tools.go` | 使用渠道过滤 | ✅ |
| `tools/load_tools.go` | 使用渠道过滤 | ✅ |

---

## 方案二：飞书上传文件工具 ✅

### 已实现

`UploadFileTool` 已存在于 `tools/feishu_mcp/file.go`：

```go
type UploadFileTool struct {
    FeishuToolBase
    MCP *FeishuMCP
}

func (t *UploadFileTool) Name() string { return "feishu_upload_file" }
```

参数：
- `file_path`: 本地文件路径（必需）
- `parent_token`: 父文件夹 token（可选）
- `file_name`: 自定义文件名（可选）

已在 `main.go` 注册。

---

## 编译验证

```bash
go build ./...
```

编译通过 ✅
