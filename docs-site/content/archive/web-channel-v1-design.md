---
title: "Web Channel v1 Design [Superseded]"
weight: 90
---

---
title: "Web Channel Design"
weight: 20
---


> 圆桌会议共识 — 4 位大臣（拥护者、魔鬼代言人、工程实践者、全局架构师）经 2 轮讨论后收敛

## 1. 架构总览

```
Browser                          xbot Core
┌─────────────┐  WebSocket/HTTP  ┌──────────────────────┐
│ React + Vite │◄══════════════►│ WebChannel (channel/) │
│ Chat UI     │  REST API       │ Hub: clients map     │
└─────────────┘                 │ Auth: HttpOnly Cookie│
                                └──────────┬───────────┘
                                           │ InboundMessage
                                    ┌──────▼──────┐
                                    │ MessageBus   │
                                    └──────┬──────┘
                                           │ OutboundMessage
                                    ┌──────▼──────┐
                                    │ Dispatcher   │
                                    └─────────────┘
```

## 2. 关键设计决策

| # | 决策 | 方案 | 理由 |
|---|------|------|------|
| 1 | Channel 接口 | ✅ 实现 Channel 接口 | 保持架构一致性，Send() 同步语义与 WS write 匹配 |
| 2 | sender_id | `"web:" + strconv.Itoa(dbID)` | 跨渠道唯一 + 不可变 + 路径安全 |
| 3 | chat_id | = sender_id（p2p 模式） | Web 默认单聊，关闭浏览器再开恢复 session |
| 4 | 认证 | HttpOnly Cookie + bcrypt | 防 XSS，WS 自动带 cookie，自部署场景够用 |
| 5 | 离线消息 | 内存 ring buffer (50条/用户) | 进度消息过时无意义，最终回复走 session 历史 |
| 6 | __FEISHU_CARD__ | Send 内部剥离前缀，best-effort 转 Markdown | 永远不展示原始 JSON |
| 7 | 前端 | React + Vite + TailwindCSS | go:embed 嵌入 dist/，单二进制部署 |
| 8 | 端口 | 独立端口，默认 8082 | WebChannel 是可选功能 |
| 9 | 连接管理 | Hub-Spokes: 2 goroutine/conn + channel 事件驱动 | 比 write-pump 简单正确 |
| 10 | Send 非阻塞 | 写入 buffered channel (cap=64) 立即返回 | 绝不能拖垮 agent pipeline |

## 3. 文件清单

### 新增文件

| 文件 | 行数 | 职责 |
|------|------|------|
| `channel/web.go` | ~350 | WebChannel struct + Channel 接口 + Hub + Client |
| `channel/web_auth.go` | ~120 | login/register/validateSession + 中间件 |
| `channel/web_api.go` | ~80 | REST API: /api/history |
| `channel/web_test.go` | ~250 | 单元测试 + 集成测试 |
| `web/` (前端项目) | ~800 | React + Vite SPA |

### 修改文件

| 文件 | 改动 |
|------|------|
| `config/config.go` | +WebConfig struct + 字段 |
| `main.go` | +WebChannel 注册 (~20行) |
| `storage/sqlite/db.go` | +web_users 建表 |

### 不改动文件

channel/channel.go, channel/dispatcher.go, bus/bus.go, bus/address.go, agent/agent.go

## 4. 数据库 Schema

```sql
CREATE TABLE IF NOT EXISTS web_users (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    username   TEXT NOT NULL UNIQUE,
    password   TEXT NOT NULL,  -- bcrypt hash
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
```

## 5. API 设计

### 认证

```
POST /api/auth/register  {username, password} → 201 Created
POST /api/auth/login     {username, password} → Set-Cookie: xbot_session
POST /api/auth/logout    → Clear Cookie
```

### WebSocket

```
GET /ws (cookie auth)
→ 客户端: {"type":"message","content":"hello"}
← 服务端: {"type":"text","id":"uuid","content":"reply...","ts":1234567890}
← 服务端: {"type":"progress","id":"uuid","content":"⏳ Reading...","ts":...}
← 服务端: {"type":"card","id":"uuid","content":"# Title\n...","ts":...}
```

### REST

```
GET /api/history?limit=50 → session 消息历史
```

## 6. 配置

```go
type WebConfig struct {
    Enable bool
    Host   string  // default "0.0.0.0"
    Port   int     // default 8082
}
```

环境变量: `WEB_ENABLED=true`, `WEB_HOST=0.0.0.0`, `WEB_PORT=8082`

## 7. 实现顺序

1. web_users 建表 + CRUD
2. WebConfig + config.go 改动
3. WebChannel struct + Channel 接口实现（含 Hub）
4. 认证（HttpOnly Cookie）
5. __FEISHU_CARD__ 协议适配
6. main.go 注册
7. 前端 React 项目（chat UI + auth）
8. go:embed 嵌入前端
9. 测试
