# OneBot v11 Channel 实现方案

> **For agentic workers:** Use subagent-driven-development to implement this plan.

**Goal:** 为 xbot 新增基于 OneBot v11 协议的 QQ 渠道，通过 go-cqhttp 接入，支持文本/图片/文件收发。

**Architecture:** 正向 WebSocket 连接 go-cqhttp 接收事件，HTTP API 发送消息。Channel name 为 "onebot"，与现有 qq.go 共存。

**Tech Stack:** Go, gorilla/websocket (已有依赖), OneBot v11 协议

---

## 文件结构

| 文件 | 职责 |
|------|------|
| `channel/onebot.go` | OneBotChannel 实现（核心） |
| `channel/onebot_test.go` | 单元测试 |
| `config/config.go` | 新增 OneBotConfig |
| `main.go` | 注册 OneBot channel |

## Task 1: 配置结构 (config/config.go)

**Files:**
- Modify: `config/config.go`

新增 OneBotConfig:
```go
// OneBotConfig OneBot v11 渠道配置（go-cqhttp）
type OneBotConfig struct {
    Enabled   bool
    WSUrl     string   // go-cqhttp 正向 WS 地址，如 "ws://127.0.0.1:8080"
    HTTPUrl   string   // go-cqhttp HTTP API 地址，如 "http://127.0.0.1:8080"
    Token     string   // access_token（可选）
    AllowFrom []string // 允许的 QQ 号列表（空则允许所有）
}
```

环境变量:
- `ONEBOT_ENABLED` (bool, default false)
- `ONEBOT_WS_URL` (string, default "ws://127.0.0.1:8080")
- `ONEBOT_HTTP_URL` (string, default "http://127.0.0.1:8080")
- `ONEBOT_TOKEN` (string, optional)
- `ONEBOT_ALLOW_FROM` (comma-separated QQ numbers)

在 Config struct 中添加 `OneBot OneBotConfig` 字段。
在 Load() 中添加对应的环境变量读取。

## Task 2: OneBotChannel 核心结构 (channel/onebot.go)

**Files:**
- Create: `channel/onebot.go`

### 结构体

```go
type OneBotChannel struct {
    cfg     OneBotConfig
    bus     *bus.MessageBus
    wsConn  *websocket.Conn
    httpCli *http.Client
    done    chan struct{}
    mu      sync.Mutex // 保护 wsConn
}

type OneBotConfig struct {
    WSUrl     string
    HTTPUrl   string
    Token     string
    AllowFrom []string
}
```

### 接口实现

```go
func (c *OneBotChannel) Name() string { return "onebot" }
func (c *OneBotChannel) Start() error  // 连接 WS，启动消息循环
func (c *OneBotChannel) Stop()         // 关闭连接
func (c *OneBotChannel) Send(msg bus.OutboundMessage) (string, error)
```

### 消息接收流程 (WS → InboundMessage)

1. 连接 go-cqhttp 正向 WS: `ws://host:port/`（带 access_token header）
2. 读取 JSON 事件
3. 过滤 `post_type == "message"` 的事件
4. 解析 `message_type`（"private" → "p2p", "group" → "group"）
5. 解析 CQ 码中的图片 `[CQ:image,file=xxx,url=http://...]`，下载到本地临时文件，放入 Media
6. 提取纯文本内容（去除 CQ 码）
7. 构造 InboundMessage 发送到 bus

OneBot v11 消息事件格式:
```json
{
    "post_type": "message",
    "message_type": "private" | "group",
    "message_id": 12345,
    "user_id": 123456789,
    "group_id": 987654321,
    "message": "[CQ:image,file=xxx,url=http://...]hello",
    "raw_message": "hello",
    "sender": {"user_id": 123, "nickname": "test"}
}
```

映射规则:
- `Channel` = "onebot"
- `SenderID` = strconv.FormatInt(user_id, 10)
- `SenderName` = sender.nickname
- `ChatID` = group_id (群) 或 user_id (私聊)
- `ChatType` = "group" 或 "p2p"
- `Content` = 去除 CQ 码后的纯文本
- `Media` = 下载的图片本地路径列表

### 消息发送流程 (OutboundMessage → HTTP API)

1. 判断 ChatType（从 Metadata 或 ChatID 格式推断）
2. 处理 Content 中的本地图片路径 → 转为 CQ 码 `[CQ:image,file=file:///path]`
3. 处理 Media 中的文件：
   - 图片 → CQ 码内嵌
   - 其他文件 → `/upload_group_file` API（仅群聊）或 `/upload_private_file`
4. 调用 HTTP API:
   - 私聊: `POST {HTTPUrl}/send_private_msg` body: `{"user_id": id, "message": "..."}`
   - 群聊: `POST {HTTPUrl}/send_group_msg` body: `{"group_id": id, "message": "..."}`
5. 返回 message_id

### 图片处理

**接收图片**:
- 解析 `[CQ:image,file=xxx,url=http://...]`
- 下载 url 到 `/tmp/onebot_images/` 目录
- 路径加入 Media 字段

**发送图片**:
- 扫描 Content 中的 Markdown 图片 `![](path)` 和本地文件路径
- 转换为 `[CQ:image,file=file:///absolute/path]` 或 `[CQ:image,file=base64://...]`
- Media 字段中的图片文件也转为 CQ 码追加

### 重连策略

复用现有 qq.go 的重连延迟策略:
```go
var reconnectDelays = []time.Duration{1s, 2s, 5s, 10s, 30s, 60s}
```
- WS 断开后按延迟递增重连
- 连续快速断开（5s 内 3 次）等待 60s
- 最大重连次数 100

### AllowFrom 过滤

- 如果 AllowFrom 非空，只处理 user_id 在列表中的消息
- 群消息也按 user_id 过滤（谁发的）

## Task 3: main.go 注册

**Files:**
- Modify: `main.go`

在 QQ channel 注册之后添加:
```go
// 注册 OneBot 渠道
if cfg.OneBot.Enabled {
    onebotCh := channel.NewOneBotChannel(channel.OneBotConfig{
        WSUrl:     cfg.OneBot.WSUrl,
        HTTPUrl:   cfg.OneBot.HTTPUrl,
        Token:     cfg.OneBot.Token,
        AllowFrom: cfg.OneBot.AllowFrom,
    }, msgBus)
    disp.Register(onebotCh)
}
```

## Task 4: 单元测试

**Files:**
- Create: `channel/onebot_test.go`

测试:
1. CQ 码解析（提取图片 URL、提取纯文本）
2. Markdown 图片转 CQ 码
3. 消息事件 JSON 解析
4. AllowFrom 过滤逻辑
5. ChatType 映射（private→p2p, group→group）

## 实现顺序

1. Task 1: 配置 → 可独立完成
2. Task 2: 核心实现 → 依赖 Task 1
3. Task 3: main.go 注册 → 依赖 Task 1, 2
4. Task 4: 测试 → 与 Task 2 并行（TDD）
