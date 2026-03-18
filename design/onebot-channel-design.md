# OneBot v11 Channel 实现方案

## 1. 文件结构与职责

```
channel/
├── onebot.go          # OneBotChannel 主实现（WS 连接、消息收发、CQ 码处理）
├── onebot_test.go     # 单元测试（CQ 码解析、消息转换等）
config/
├── config.go          # 新增 OneBotConfig 结构体 + 环境变量加载
main.go                # 新增 OneBot channel 注册逻辑
```

**不新增额外文件**——OneBot v11 协议相对简单，所有逻辑集中在 `onebot.go` 一个文件中（与 `qq.go` 的模式一致）。如果后续 CQ 码处理逻辑膨胀，可拆分 `onebot_cqcode.go`。

---

## 2. OneBotConfig 配置结构与环境变量

### 2.1 Config 结构体

```go
// config/config.go 新增

// OneBotConfig OneBot v11 渠道配置（go-cqhttp / Lagrange）
type OneBotConfig struct {
    Enabled     bool
    WSURL       string   // go-cqhttp 正向 WebSocket 地址，如 "ws://127.0.0.1:6700"
    HTTPURL     string   // go-cqhttp HTTP API 地址，如 "http://127.0.0.1:5700"
    AccessToken string   // 鉴权 token（可选，对应 go-cqhttp 的 access_token）
    AllowFrom   []string // 允许的 QQ 号白名单（空则允许所有）
    DownloadDir string   // 图片下载目录（默认 /tmp/onebot_media）
}
```

### 2.2 环境变量

| 环境变量 | 说明 | 默认值 |
|---------|------|--------|
| `ONEBOT_ENABLED` | 是否启用 OneBot 渠道 | `false` |
| `ONEBOT_WS_URL` | 正向 WebSocket 地址 | `ws://127.0.0.1:6700` |
| `ONEBOT_HTTP_URL` | HTTP API 地址 | `http://127.0.0.1:5700` |
| `ONEBOT_ACCESS_TOKEN` | 鉴权 token | `""` |
| `ONEBOT_ALLOW_FROM` | 允许的 QQ 号（逗号分隔） | `""` (允许所有) |
| `ONEBOT_DOWNLOAD_DIR` | 媒体文件下载目录 | `/tmp/onebot_media` |

### 2.3 config.go 中的加载代码

```go
// Config struct 新增字段
OneBot OneBotConfig

// Load() 中新增
OneBot: OneBotConfig{
    Enabled:     getEnvBoolOrDefault("ONEBOT_ENABLED", false),
    WSURL:       getEnvOrDefault("ONEBOT_WS_URL", "ws://127.0.0.1:6700"),
    HTTPURL:     getEnvOrDefault("ONEBOT_HTTP_URL", "http://127.0.0.1:5700"),
    AccessToken: getEnvOrDefault("ONEBOT_ACCESS_TOKEN", ""),
    AllowFrom:   splitEnv("ONEBOT_ALLOW_FROM"),
    DownloadDir: getEnvOrDefault("ONEBOT_DOWNLOAD_DIR", "/tmp/onebot_media"),
},
```

---

## 3. OneBotChannel 核心结构体设计

```go
package channel

// OneBotConfig OneBot v11 渠道配置
type OneBotConfig struct {
    WSURL       string   // 正向 WebSocket 地址
    HTTPURL     string   // HTTP API 地址
    AccessToken string   // 鉴权 token
    AllowFrom   []string // 允许的 QQ 号白名单
    DownloadDir string   // 媒体文件下载目录
}

// OneBotChannel OneBot v11 渠道实现（go-cqhttp / Lagrange）
type OneBotChannel struct {
    config  OneBotConfig
    msgBus  *bus.MessageBus

    // WebSocket
    conn    *websocket.Conn
    connMu  sync.Mutex
    stopCh  chan struct{}
    running atomic.Bool

    // HTTP client（复用，带超时）
    httpClient *http.Client

    // 消息去重
    processedIDs   map[int64]struct{} // message_id 是 int64
    processedOrder []int64
    processedMu    sync.Mutex
    maxProcessed   int

    // 自身 QQ 号（从 /get_login_info 获取，用于过滤自己的消息）
    selfID int64

    // 心跳监控
    lastHeartbeat atomic.Value // time.Time
}
```

### 3.1 关键设计决策

1. **Channel Name**: `"onebot"` — 与现有 `"qq"` 渠道独立共存
2. **正向 WebSocket**: xbot 主动连接 go-cqhttp 的 WS 端点，接收事件推送
3. **HTTP API 发送**: 通过 HTTP POST 调用 go-cqhttp 的 API 发送消息（比 WS 发送更简单可靠）
4. **ChatType 映射**: OneBot `"private"` → xbot `"p2p"`, `"group"` → xbot `"group"`
5. **ChatID 规则**: 私聊用 `user_id` 字符串，群聊用 `group_id` 字符串
6. **Address 格式**: `im://onebot/{user_id}` 或 `im://onebot/{group_id}`

---

## 4. 消息接收流程（WS 事件 → InboundMessage）

### 4.1 WebSocket 事件结构

```go
// onebotEvent OneBot v11 通用事件
type onebotEvent struct {
    PostType      string       `json:"post_type"`      // "message", "notice", "request", "meta_event"
    MessageType   string       `json:"message_type"`   // "private", "group"
    SubType       string       `json:"sub_type"`       // "friend", "normal", "anonymous"
    MessageID     int64        `json:"message_id"`
    UserID        int64        `json:"user_id"`
    GroupID       int64        `json:"group_id,omitempty"`
    RawMessage    string       `json:"raw_message"`    // 含 CQ 码的原始消息
    Sender        onebotSender `json:"sender"`
    SelfID        int64        `json:"self_id"`
    Time          int64        `json:"time"`           // Unix 时间戳
    MetaEventType string       `json:"meta_event_type,omitempty"` // "heartbeat", "lifecycle"
}

// onebotSender 发送者信息
type onebotSender struct {
    UserID   int64  `json:"user_id"`
    Nickname string `json:"nickname"`
    Card     string `json:"card,omitempty"`     // 群名片
    Role     string `json:"role,omitempty"`     // "owner", "admin", "member"
}
```

### 4.2 接收流程

```
go-cqhttp WS 推送
    ↓
OneBotChannel.readLoop() — 持续读取 WS 消息
    ↓
json.Unmarshal → onebotEvent
    ↓
分支: post_type == "meta_event" → 更新心跳时间，跳过
分支: post_type != "message"   → 跳过
    ↓
过滤: self_id == user_id → 跳过（自己发的消息）
    ↓
去重: isDuplicate(message_id) → 跳过
    ↓
权限: isAllowed(user_id) → 拒绝
    ↓
解析 CQ 码 (parseCQMessage):
  - 提取 [CQ:image,...,url=xxx] → 下载图片到 DownloadDir，路径放入 Media
  - 提取 [CQ:at,qq=xxx] → 如果是 @自己则忽略，否则转为 "@昵称" 文本
  - 提取 [CQ:reply,id=xxx] → 记录到 Metadata["reply_to"]
  - 清理剩余未识别 CQ 码
  - 剩余文本 → Content
    ↓
构造 InboundMessage:
  Channel:    "onebot"
  SenderID:   strconv.FormatInt(user_id, 10)
  SenderName: sender.card (优先) || sender.nickname
  ChatID:     private → strconv.FormatInt(user_id, 10)
              group   → strconv.FormatInt(group_id, 10)
  ChatType:   private → "p2p"
              group   → "group"
  Content:    解析后的纯文本
  Media:      下载的图片路径列表
  From:       bus.NewIMAddress("onebot", senderID)
  To:         bus.NewIMAddress("onebot", chatID)
  Metadata:   {
    "message_id": "...",
    "chat_type":  "p2p" | "group",
    "user_id":    "...",
    "group_id":   "..." (仅群聊)
  }
    ↓
msgBus.Inbound <- inboundMsg
```

### 4.3 CQ 码解析函数

```go
// parseCQMessage 解析 OneBot 消息中的 CQ 码
// 返回: (纯文本内容, 图片URL列表, 额外metadata)
func parseCQMessage(rawMessage string, selfID int64) (text string, imageURLs []string, meta map[string]string)

// CQ 码正则
var (
    // [CQ:image,file=xxx,url=http://...]  — url 字段可能在不同位置
    cqImageRe = regexp.MustCompile(`\[CQ:image,[^\]]*url=([^\],]+)[^\]]*\]`)

    // [CQ:at,qq=123456]
    cqAtRe = regexp.MustCompile(`\[CQ:at,qq=(\d+)\]`)

    // [CQ:reply,id=123456]
    cqReplyRe = regexp.MustCompile(`\[CQ:reply,id=(-?\d+)\]`)

    // 匹配任意 CQ 码（用于清理未处理的 CQ 码）
    cqAnyRe = regexp.MustCompile(`\[CQ:[^\]]+\]`)
)
```

### 4.4 parseCQMessage 实现逻辑

```go
func parseCQMessage(rawMessage string, selfID int64) (string, []string, map[string]string) {
    meta := make(map[string]string)
    var imageURLs []string
    text := rawMessage

    // 1. 提取 reply
    if subs := cqReplyRe.FindStringSubmatch(text); len(subs) > 1 {
        meta["reply_to"] = subs[1]
    }
    text = cqReplyRe.ReplaceAllString(text, "")

    // 2. 提取图片 URL
    for _, subs := range cqImageRe.FindAllStringSubmatch(text, -1) {
        if len(subs) > 1 {
            imageURLs = append(imageURLs, subs[1])
        }
    }
    text = cqImageRe.ReplaceAllString(text, "")

    // 3. 处理 @
    text = cqAtRe.ReplaceAllStringFunc(text, func(match string) string {
        subs := cqAtRe.FindStringSubmatch(match)
        if len(subs) > 1 {
            qqNum, _ := strconv.ParseInt(subs[1], 10, 64)
            if qqNum == selfID {
                return "" // 忽略 @自己
            }
            return "@" + subs[1]
        }
        return match
    })

    // 4. 清理剩余 CQ 码
    text = cqAnyRe.ReplaceAllString(text, "")

    // 5. 清理多余空白
    text = strings.TrimSpace(text)

    return text, imageURLs, meta
}
```

---

## 5. 消息发送流程（OutboundMessage → HTTP API + CQ 码）

### 5.1 发送流程

```
OutboundMessage
    ↓
判断 ChatType（从 Metadata["chat_type"] 获取）
    ↓
处理 Content:
  1. 提取 Markdown 图片 ![alt](path) → 转为 [CQ:image,file=file:///abs/path]
  2. 处理 Media 列表:
     - 图片文件 → 追加 [CQ:image,file=file:///path] 到消息末尾
     - 非图片 + 群聊 → 调用 /upload_group_file
     - 非图片 + 私聊 → 追加 "📎 文件名" 提示
    ↓
调用 HTTP API:
  p2p   → POST {HTTPURL}/send_private_msg  {"user_id": xxx, "message": "..."}
  group → POST {HTTPURL}/send_group_msg    {"group_id": xxx, "message": "..."}
    ↓
解析响应 {"data": {"message_id": 123}} → 返回 message_id 字符串
```

### 5.2 核心发送函数签名

```go
// Send 发送消息到 OneBot
func (o *OneBotChannel) Send(msg bus.OutboundMessage) (string, error)

// sendPrivateMsg 发送私聊消息
func (o *OneBotChannel) sendPrivateMsg(userID int64, message string) (string, error)

// sendGroupMsg 发送群消息
func (o *OneBotChannel) sendGroupMsg(groupID int64, message string) (string, error)

// callAPI 通用 HTTP API 调用
func (o *OneBotChannel) callAPI(endpoint string, body any) (json.RawMessage, error)
```

### 5.3 callAPI 实现

```go
// onebotAPIResponse go-cqhttp API 统一响应
type onebotAPIResponse struct {
    Status  string          `json:"status"`  // "ok" or "failed"
    RetCode int             `json:"retcode"` // 0 = success
    Data    json.RawMessage `json:"data"`
    Msg     string          `json:"msg,omitempty"`
    Wording string          `json:"wording,omitempty"`
}

func (o *OneBotChannel) callAPI(endpoint string, body any) (json.RawMessage, error) {
    apiURL := strings.TrimRight(o.config.HTTPURL, "/") + endpoint

    jsonBody, err := json.Marshal(body)
    if err != nil {
        return nil, fmt.Errorf("onebot: marshal body: %w", err)
    }

    req, err := http.NewRequest("POST", apiURL, bytes.NewReader(jsonBody))
    if err != nil {
        return nil, fmt.Errorf("onebot: create request: %w", err)
    }
    req.Header.Set("Content-Type", "application/json")
    if o.config.AccessToken != "" {
        req.Header.Set("Authorization", "Bearer "+o.config.AccessToken)
    }

    resp, err := o.httpClient.Do(req)
    if err != nil {
        return nil, fmt.Errorf("onebot: API request failed: %w", err)
    }
    defer resp.Body.Close()

    respData, err := io.ReadAll(resp.Body)
    if err != nil {
        return nil, fmt.Errorf("onebot: read response: %w", err)
    }

    var result onebotAPIResponse
    if err := json.Unmarshal(respData, &result); err != nil {
        return nil, fmt.Errorf("onebot: parse response: %w (body: %s)", err, string(respData))
    }

    if result.RetCode != 0 {
        return nil, fmt.Errorf("onebot: API error: retcode=%d msg=%s wording=%s",
            result.RetCode, result.Msg, result.Wording)
    }

    return result.Data, nil
}
```

### 5.4 Send 实现

```go
func (o *OneBotChannel) Send(msg bus.OutboundMessage) (string, error) {
    if msg.Content == "" && len(msg.Media) == 0 {
        return "", nil
    }

    chatType := ""
    if msg.Metadata != nil {
        chatType = msg.Metadata["chat_type"]
    }

    content := msg.Content

    // 1. Markdown 图片 → CQ 码
    content = convertLocalImagesToCQ(content)

    // 2. Media 附件处理
    for _, mediaPath := range msg.Media {
        ext := strings.ToLower(filepath.Ext(mediaPath))
        absPath, _ := filepath.Abs(mediaPath)

        if onebotImageExtensions[ext] {
            // 图片 → CQ 码追加
            content += fmt.Sprintf("\n[CQ:image,file=file://%s]", absPath)
        } else if chatType == "group" {
            // 非图片 + 群聊 → 上传群文件
            groupID, _ := strconv.ParseInt(msg.ChatID, 10, 64)
            if err := o.uploadGroupFile(groupID, absPath, filepath.Base(mediaPath)); err != nil {
                log.WithError(err).Warn("OneBot: failed to upload group file")
            }
        } else {
            // 非图片 + 私聊 → 文本提示
            content += fmt.Sprintf("\n📎 %s", filepath.Base(mediaPath))
        }
    }

    content = strings.TrimSpace(content)
    if content == "" {
        return "", nil
    }

    chatID, _ := strconv.ParseInt(msg.ChatID, 10, 64)

    switch chatType {
    case "p2p":
        return o.sendPrivateMsg(chatID, content)
    case "group":
        return o.sendGroupMsg(chatID, content)
    default:
        // 尝试从 metadata 推断
        if msg.Metadata != nil && msg.Metadata["group_id"] != "" {
            return o.sendGroupMsg(chatID, content)
        }
        return o.sendPrivateMsg(chatID, content)
    }
}
```

---

## 6. 图片/文件处理方案

### 6.1 接收图片（入站）

```go
// downloadImage 下载 OneBot 图片到本地
func (o *OneBotChannel) downloadImage(imageURL string) (string, error) {
    // 确保下载目录存在
    if err := os.MkdirAll(o.config.DownloadDir, 0755); err != nil {
        return "", fmt.Errorf("create download dir: %w", err)
    }

    // 从 URL 推断扩展名，默认 .jpg
    ext := filepath.Ext(imageURL)
    if ext == "" || len(ext) > 5 || !onebotImageExtensions[ext] {
        ext = ".jpg"
    }
    filename := filepath.Join(o.config.DownloadDir, uuid.New().String()+ext)

    resp, err := o.httpClient.Get(imageURL)
    if err != nil {
        return "", fmt.Errorf("download image: %w", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        return "", fmt.Errorf("download image: status %d", resp.StatusCode)
    }

    f, err := os.Create(filename)
    if err != nil {
        return "", fmt.Errorf("create file: %w", err)
    }
    defer f.Close()

    if _, err := io.Copy(f, resp.Body); err != nil {
        os.Remove(filename)
        return "", fmt.Errorf("write file: %w", err)
    }

    return filename, nil
}
```

### 6.2 发送图片（出站）

```go
// onebotImageExtensions 图片文件扩展名集合
var onebotImageExtensions = map[string]bool{
    ".jpg": true, ".jpeg": true, ".png": true, ".gif": true,
    ".bmp": true, ".webp": true,
}

// onebotMdImageRe 匹配 markdown 图片语法 ![alt](path)
var onebotMdImageRe = regexp.MustCompile(`!\[([^\]]*)\]\(([^)]+)\)`)

// convertLocalImagesToCQ 将 Markdown 图片语法转为 CQ 码
func convertLocalImagesToCQ(content string) string {
    return onebotMdImageRe.ReplaceAllStringFunc(content, func(match string) string {
        subs := onebotMdImageRe.FindStringSubmatch(match)
        if len(subs) < 3 {
            return match
        }
        imgPath := subs[2]

        // 跳过 URL
        if strings.HasPrefix(imgPath, "http://") || strings.HasPrefix(imgPath, "https://") {
            return match
        }

        // 跳过不存在的文件
        if _, err := os.Stat(imgPath); err != nil {
            return match
        }

        ext := strings.ToLower(filepath.Ext(imgPath))
        if !onebotImageExtensions[ext] {
            return match
        }

        absPath, _ := filepath.Abs(imgPath)
        return fmt.Sprintf("[CQ:image,file=file://%s]", absPath)
    })
}
```

### 6.3 发送文件（出站 - 仅群聊）

```go
// uploadGroupFile 上传群文件
func (o *OneBotChannel) uploadGroupFile(groupID int64, filePath, fileName string) error {
    absPath, _ := filepath.Abs(filePath)
    _, err := o.callAPI("/upload_group_file", map[string]any{
        "group_id": groupID,
        "file":     absPath,
        "name":     fileName,
    })
    return err
}
```

---

## 7. 重连策略

### 7.1 重连参数

```go
var onebotReconnectDelays = []time.Duration{
    1 * time.Second,
    2 * time.Second,
    5 * time.Second,
    10 * time.Second,
    30 * time.Second,
    60 * time.Second,
}

const onebotMaxReconnectAttempts = 100
```

### 7.2 Start() 主循环

```go
func (o *OneBotChannel) Start() error {
    if o.config.WSURL == "" || o.config.HTTPURL == "" {
        return fmt.Errorf("onebot: ws_url and http_url are required")
    }

    o.running.Store(true)
    log.Info("OneBot channel starting...")

    // 获取自身 QQ 号（用于过滤自己的消息）
    o.fetchSelfID()

    attempt := 0
    for o.running.Load() {
        if attempt >= onebotMaxReconnectAttempts {
            return fmt.Errorf("onebot: exceeded max reconnect attempts (%d)", onebotMaxReconnectAttempts)
        }

        err := o.connectAndRun()
        if !o.running.Load() {
            return nil // graceful shutdown
        }

        if err != nil {
            log.WithError(err).Warn("OneBot: WebSocket disconnected")
        }

        delay := onebotReconnectDelays[min(attempt, len(onebotReconnectDelays)-1)]
        log.WithFields(log.Fields{
            "attempt": attempt + 1,
            "delay":   delay,
        }).Info("OneBot: reconnecting...")

        if !o.sleepOrStop(delay) {
            return nil
        }
        attempt++
    }
    return nil
}
```

### 7.3 connectAndRun()

```go
func (o *OneBotChannel) connectAndRun() error {
    // 构造 WS URL（带 access_token 参数）
    wsURL := o.config.WSURL
    if o.config.AccessToken != "" {
        sep := "?"
        if strings.Contains(wsURL, "?") {
            sep = "&"
        }
        wsURL += sep + "access_token=" + o.config.AccessToken
    }

    conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
    if err != nil {
        return fmt.Errorf("ws dial: %w", err)
    }

    o.connMu.Lock()
    o.conn = conn
    o.connMu.Unlock()
    defer o.closeConn()

    log.WithField("url", o.config.WSURL).Info("OneBot: WebSocket connected")

    // 初始化心跳时间
    o.lastHeartbeat.Store(time.Now())

    // 启动心跳监控
    go o.heartbeatWatchdog()

    // 连接成功，重置重连计数（通过返回 nil 让外层重置）
    // 读取消息循环
    for o.running.Load() {
        _, data, err := conn.ReadMessage()
        if err != nil {
            if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
                return fmt.Errorf("ws closed: %w", err)
            }
            return fmt.Errorf("ws read: %w", err)
        }

        o.handleEvent(data)
    }
    return nil
}
```

### 7.4 心跳监控

go-cqhttp 正向 WS 会自动发送 `meta_event` 心跳事件（默认 5 秒间隔）。xbot 侧**不需要主动发心跳**，只需监控：

```go
func (o *OneBotChannel) heartbeatWatchdog() {
    ticker := time.NewTicker(15 * time.Second)
    defer ticker.Stop()

    for {
        select {
        case <-ticker.C:
            last, ok := o.lastHeartbeat.Load().(time.Time)
            if !ok {
                continue
            }
            if time.Since(last) > 30*time.Second {
                log.Warn("OneBot: heartbeat timeout, closing connection")
                o.closeConn()
                return
            }
        case <-o.stopCh:
            return
        }
    }
}
```

### 7.5 fetchSelfID

```go
// fetchSelfID 获取机器人自身 QQ 号
func (o *OneBotChannel) fetchSelfID() {
    data, err := o.callAPI("/get_login_info", struct{}{})
    if err != nil {
        log.WithError(err).Warn("OneBot: failed to get login info")
        return
    }

    var info struct {
        UserID   int64  `json:"user_id"`
        Nickname string `json:"nickname"`
    }
    if err := json.Unmarshal(data, &info); err != nil {
        log.WithError(err).Warn("OneBot: failed to parse login info")
        return
    }

    o.selfID = info.UserID
    log.WithFields(log.Fields{
        "user_id":  info.UserID,
        "nickname": info.Nickname,
    }).Info("OneBot: bot identity obtained")
}
```

---

## 8. main.go 改动

在 QQ 渠道注册之后新增：

```go
// 注册 OneBot v11 渠道
if cfg.OneBot.Enabled {
    onebotCh := channel.NewOneBotChannel(channel.OneBotConfig{
        WSURL:       cfg.OneBot.WSURL,
        HTTPURL:     cfg.OneBot.HTTPURL,
        AccessToken: cfg.OneBot.AccessToken,
        AllowFrom:   cfg.OneBot.AllowFrom,
        DownloadDir: cfg.OneBot.DownloadDir,
    }, msgBus)
    disp.Register(onebotCh)
}
```

---

## 9. 分任务拆解

### Task 1: 配置层（config/config.go）
- 新增 `OneBotConfig` 结构体到 config 包
- 在 `Config` struct 中添加 `OneBot OneBotConfig` 字段
- 在 `Load()` 中添加环境变量读取
- **可独立实现和测试**：无依赖

### Task 2: OneBotChannel 骨架 + WS 连接 + 重连（channel/onebot.go）
- 结构体定义 `OneBotChannel`、`OneBotConfig`
- 构造函数 `NewOneBotChannel(cfg, msgBus)`
- `Name()` 返回 `"onebot"`
- `Start()` / `Stop()` 生命周期管理
- `connectAndRun()` WebSocket 连接和读取循环
- 重连逻辑（指数退避）
- `closeConn()`, `sleepOrStop()` 辅助函数
- `fetchSelfID()` 获取机器人 QQ 号
- **依赖**: Task 1

### Task 3: 消息接收 + CQ 码解析（channel/onebot.go）
- `handleEvent(data)` 事件分发
- `handleMessage(event)` 消息处理（私聊 + 群聊）
- `parseCQMessage(rawMessage, selfID)` CQ 码解析
- CQ 码正则定义
- 消息去重 `isDuplicate(messageID)`
- 权限检查 `isAllowed(userID)`
- 心跳监控 `heartbeatWatchdog()`
- **依赖**: Task 2

### Task 4: 图片下载（channel/onebot.go）
- `downloadImage(imageURL)` 从 URL 下载图片到本地
- 集成到 `handleMessage` 中：解析 CQ 码图片 URL → 下载 → 放入 Media
- **依赖**: Task 3

### Task 5: 消息发送 + HTTP API（channel/onebot.go）
- `Send(msg)` 实现
- `callAPI(endpoint, body)` HTTP API 封装
- `sendPrivateMsg(userID, message)` 私聊发送
- `sendGroupMsg(groupID, message)` 群聊发送
- `convertLocalImagesToCQ(content)` Markdown 图片转 CQ 码
- Media 附件处理逻辑
- **依赖**: Task 2

### Task 6: 文件上传（channel/onebot.go）
- `uploadGroupFile(groupID, filePath, fileName)` 群文件上传
- 集成到 `Send()` 中：从 Media 提取非图片文件 → 群聊上传
- **依赖**: Task 5

### Task 7: 单元测试（channel/onebot_test.go）
- `TestParseCQMessage_*` — CQ 码解析各场景
- `TestConvertLocalImagesToCQ_*` — Markdown 图片转 CQ 码
- `TestIsDuplicate_*` — 消息去重
- `TestIsAllowed_*` — 权限检查
- `TestOneBotSender_*` — 发送者名称解析（card 优先于 nickname）
- **可与 Task 3-6 并行编写**

### Task 8: main.go 集成 + 端到端验证
- main.go 注册 OneBot channel
- .env.example 更新
- 端到端测试（需要 go-cqhttp 实例）
- **依赖**: Task 1-6 全部完成

### 任务依赖图

```
Task 1 (config)
    ↓
Task 2 (骨架 + WS + 重连)
    ↓
  ┌─────┼─────┐
  ↓     ↓     ↓
Task 3  Task 5  Task 7 (测试，可并行)
(接收)  (发送)
  ↓     ↓
Task 4  Task 6
(图片)  (文件)
  ↓     ↓
  └──┬──┘
     ↓
  Task 8 (集成)
```

---

## 附录 A: 与现有 qq.go 的对比

| 维度 | qq.go (QQ 官方 API) | onebot.go (OneBot v11) |
|------|---------------------|------------------------|
| 协议 | QQ 官方 WebSocket + REST | OneBot v11 (go-cqhttp) |
| 认证 | OAuth2 access_token | 简单 access_token (可选) |
| WS 方向 | 反向（QQ 推送到 xbot） | 正向（xbot 连接 go-cqhttp） |
| WS 握手 | Hello → Identify → Ready | 直接连接即可 |
| 心跳 | xbot 主动发 | go-cqhttp 主动发，xbot 监控 |
| 消息格式 | JSON 结构体 | CQ 码字符串 |
| 发送方式 | REST API (QQ 官方) | HTTP API (go-cqhttp 本地) |
| 图片发送 | base64 上传 → file_info → media msg | CQ 码 file:// 路径 |
| 文件发送 | base64 上传 → media msg | /upload_group_file 本地路径 |
| Channel Name | "qq" | "onebot" |
| ID 类型 | openid (字符串) | QQ 号 (int64→string) |

## 附录 B: go-cqhttp 部署要求

xbot 与 go-cqhttp 需部署在同一台机器（或同一网络），因为：
1. 正向 WS 需要 xbot 能访问 go-cqhttp 的端口
2. 文件发送使用本地路径（`file:///path`），需要共享文件系统
3. 图片下载的 URL 可能是内网地址

推荐 go-cqhttp 配置（`config.yml`）：
```yaml
servers:
  - ws:
      host: 0.0.0.0
      port: 6700
  - http:
      host: 0.0.0.0
      port: 5700
```

## 附录 C: 复用与差异

**可复用 qq.go 的模式**:
- 重连策略（延迟梯度、最大重试次数）
- 消息去重（processedIDs map + order slice）
- 权限检查（AllowFrom 白名单）
- Markdown 图片提取正则
- sleepOrStop 辅助函数

**不可复用，需独立实现**:
- WS 连接流程（正向 WS 无需 Identify/Resume）
- 消息解析（CQ 码 vs JSON 结构体）
- 消息发送（HTTP API + CQ 码 vs REST API + JSON）
- 心跳机制（被动监控 vs 主动发送）
- 图片处理（file:// 路径 vs base64 上传）
