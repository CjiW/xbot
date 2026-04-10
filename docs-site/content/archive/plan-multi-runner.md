---
title: "plan-multi-runner"
weight: 180
---


> 圆桌会议讨论结果，2026-03-29

## 1. 目标

- 一个用户可以有 N 个 runner（不同机器/不同类型）
- 用户可以在前端选择当前活跃 runner
- 不同 runner 可以有不同 sandbox 模式（docker/native/remote）
- 代码架构清晰简洁，改动范围最小

## 2. 现状分析

### 当前限制

- `tools/remote_sandbox.go` 的 `connections sync.Map`：key 是 `userID`，**一个用户只能有一个 runner**
- `tools/runner_tokens.go` 的 DB 表 `runner_tokens(id, user_id, token, name, created_at)`：`user_id` 是 PRIMARY KEY（一对一）
- Runner 连接时通过 WebSocket 注册，服务端用 userID 作为唯一 key
- 前端 SettingsPanel 只有一个 runner token 区域

### 关键文件

| 文件 | 职责 |
|------|------|
| `tools/remote_sandbox.go` | Runner WebSocket 服务端，连接管理 |
| `tools/runner_tokens.go` | Token 生成/验证，DB 操作 |
| `tools/sandbox_router.go` | Sandbox 路由，按 senderID 找到对应 Sandbox |
| `tools/runner_protocol.go` | Runner 协议消息定义 |
| `channel/web_api.go` | 前端 API 端点 |
| `cmd/runner/` | Runner 端（独立二进制） |

## 3. 设计方案

### 3.1 核心思路：加映射层，不改 connections key

**否决方案**：改 connections key 为 `userID:runnerName`
- 竞态风险，影响面大，SandboxRouter 所有调用点都要改

**采纳方案**：加一层 `userRunners` 映射

```
connections sync.Map
  key:   userID (string)
  value: *userRunnersEntry {
      mu      sync.RWMutex
      runners map[string]*runnerConnection  // runnerName → conn
      active  string                        // 当前活跃 runnerName
  }
```

### 3.2 DB Schema 变更

**v17 migration**：新建 `runners` 表，迁移 `runner_tokens` 数据

```sql
CREATE TABLE IF NOT EXISTS runners (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id    TEXT    NOT NULL,
    name       TEXT    NOT NULL,
    token      TEXT    NOT NULL UNIQUE,
    mode       TEXT    NOT NULL DEFAULT 'native',     -- native/docker
    docker_image TEXT  NOT NULL DEFAULT 'ubuntu:22.04',
    workspace  TEXT    NOT NULL DEFAULT '',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(user_id, name)
);

-- 迁移旧数据
INSERT OR IGNORE INTO runners (user_id, name, token, mode, workspace, created_at)
    SELECT user_id, 'default', token, 'native', '', created_at
    FROM runner_tokens;
```

**活跃状态**：复用 `user_settings` 表

```sql
INSERT OR IGNORE INTO user_settings (user_id, key, value)
    SELECT user_id, 'active_runner', 'default'
    FROM runner_tokens;
```

### 3.3 核心数据结构

```go
// tools/remote_sandbox.go

type userRunnersEntry struct {
    mu      sync.RWMutex
    runners map[string]*runnerConnection  // runnerName → conn
    active  string                        // 当前活跃的 runnerName
}

// connections 改名为 userRunners
// key:   userID (string)
// value: *userRunnersEntry
userRunners sync.Map

// 活跃状态持久化（接口）
activeStore ActiveRunnerStore  // 底层用 user_settings 表
```

### 3.4 连接协议（向后兼容）

```go
// internal/runnerproto/runner_proto.go

type RegisterRequest struct {
    UserID     string `json:"user_id"`
    AuthToken  string `json:"auth_token"`
    Workspace  string `json:"workspace,omitempty"`
    Shell      string `json:"shell,omitempty"`
    RunnerName string `json:"runner_name,omitempty"` // 新增，可选
}
```

服务端注册流程：
1. 解析 userID from URL: `/ws/{userID}`
2. Upgrade + 读注册消息
3. 验证 token → 查 `runners` 表得到 runnerName
4. 如果请求带 `runner_name` 且不匹配 DB → 拒绝
5. 如果请求不带 `runner_name` → 用 DB 中的 name（兼容旧 runner）
6. 存入 `userRunners`
7. 如果该用户没有 active runner → 设为当前 runner

### 3.5 Sandbox 路由

```go
func (rs *RemoteSandbox) getActiveRunner(userID string) (*runnerConnection, error) {
    val, ok := rs.userRunners.Load(userID)
    if !ok {
        return nil, fmt.Errorf("no runner connected for user %q", userID)
    }
    entry := val.(*userRunnersEntry)
    entry.mu.RLock()
    defer entry.mu.RUnlock()

    // 优先用 active
    if conn, ok := entry.runners[entry.active]; ok {
        return conn, nil
    }
    // fallback: 用第一个可用的
    for _, conn := range entry.runners {
        return conn, nil
    }
    return nil, fmt.Errorf("no runner connected for user %q", userID)
}
```

### 3.6 API 设计

| Method | Path | 说明 |
|--------|------|------|
| `GET` | `/api/runners` | 列出所有 runner（含在线状态） |
| `POST` | `/api/runners` | 创建 runner |
| `DELETE` | `/api/runners/{name}` | 删除 runner |
| `PUT` | `/api/runners/{name}/active` | 设为活跃 |
| `GET` | `/api/runners/active` | 获取当前活跃 runner |

向后兼容端点（保留一个版本后移除）：

| Method | Path | 映射到 |
|--------|------|--------|
| `GET` | `/api/runner/token` | `GET /api/runners` + 取 active 的 command |
| `POST` | `/api/runner/token` | `POST /api/runners` with name="default" |
| `DELETE` | `/api/runner/token` | `DELETE /api/runners/default` |

### 3.7 连接命令

**格式不变**。Token 已经是 per-runner 唯一的，server 靠 token 查 DB 确定 runner 身份。

```bash
# 旧命令（仍然工作）
./xbot-runner --server ws://host/ws/web-5 --token abc123

# 新命令（格式一样）
./xbot-runner --server ws://host/ws/web-5 --token abc123
```

### 3.8 前端变更

#### RunnerPanel UI

```
┌─────────────────────────────────────────┐
│  🖥️ 工作环境                             │
├─────────────────────────────────────────┤
│                                         │
│  ┌─────────────────────────────────┐    │
│  │ 🟢 MacBook Pro    [活跃]  [⋯]  │    │  ← 绿点=在线，活跃标记
│  │ 本地开发 · ~/workspace          │    │
│  └─────────────────────────────────┘    │
│                                         │
│  ┌─────────────────────────────────┐    │
│  │ ⚫ Build Server         [⋯]    │    │  ← 黑点=离线
│  │ Docker · ubuntu:22.04           │    │
│  └─────────────────────────────────┘    │
│                                         │
│  [+ 添加工作环境]                        │    │
│                                         │
│  ┌─ 快速模板 ─────────────────────┐    │
│  │ 📦 本地开发  🐳 Docker隔离     │    │
│  │ ✏️ 自定义                       │    │
│  └─────────────────────────────────┘    │
└─────────────────────────────────────────┘
```

交互：
- 点击卡片 → 设为活跃（如果在线）
- `⋯` 菜单 → 复制连接命令 / 编辑 / 删除
- 删除时：如果 runner 在线，先断开连接

切换确认（agent 正在工作时）：
```
⚠️ Agent 正在工作
切换后，当前操作将在原环境完成，后续操作将在新环境执行。
[取消]  [确认切换]
```

## 4. 实施计划

### Phase 1：后端核心

| 步骤 | 文件 | 改动 |
|------|------|------|
| 1.1 | `storage/sqlite/db.go` | v17 migration: create `runners` table, migrate data |
| 1.2 | `tools/runner_tokens.go` | 重写为 `RunnerTokenStore`，新增 `FindByToken/List/Create/Delete` |
| 1.3 | `internal/runnerproto/runner_proto.go` | `RegisterRequest` 加 `RunnerName` 字段 |
| 1.4 | `tools/remote_sandbox.go` | `connections` → `userRunners`，新增 `getActiveRunner`，`SetActive` |
| 1.5 | `tools/sandbox_router.go` | 微调路由逻辑（`HasUser` 改为检查 userRunnersEntry） |
| 1.6 | `channel/web_api.go` | 新增 `/api/runners` 系列端点，保留旧端点兼容 |
| 1.7 | `main.go` | 更新 callbacks 绑定 |
| 1.8 | 测试 | 现有测试 + 新增多 runner 测试 |

### Phase 2：前端重构

| 步骤 | 文件 | 改动 |
|------|------|------|
| 2.1 | 拆分 SettingsPanel | 提取 AppearanceTab, LLMTab, MarketTab, RunnerPanel |
| 2.2 | `RunnerPanel.tsx` | 新 UI：runner 卡片列表 + 模板 + 切换 |
| 2.3 | API 层 | `/api/runner/token` → `/api/runners` 迁移 |

### Phase 3：飞书端适配

| 步骤 | 文件 | 改动 |
|------|------|------|
| 3.1 | `channel/feishu_settings.go` | Settings Card 展示多 runner 列表 |
| 3.2 | Card 交互 | 切换活跃 runner 的按钮 |

## 5. 关键决策

| 决策 | 选择 | 否决方案 | 理由 |
|------|------|----------|------|
| connections key | 不改，加映射层 | 改 key 为 composite | 零迁移风险，向后兼容 |
| Runner 身份识别 | token 查 DB | URL 加 runnerName | 不改 URL 格式，不增加 CLI 参数 |
| 活跃状态持久化 | user_settings 表 | 新建 active_runners 表 | 复用现有机制 |
| DB 迁移 | 新建 runners + 迁移 | ALTER runner_tokens | SQLite ALTER 限制多，新建更干净 |
| 前端术语 | "工作环境" | "Runner" | 用户友好 |
| 切换时忙碌处理 | 警告 + 允许切换 | 阻止切换 | 工具调用层面天然安全 |

## 6. 风险与缓解

| 风险 | 缓解 |
|------|------|
| 旧版 runner 连新版 server | `RunnerName` 字段可选，fallback 到 DB 查找 |
| 同名 runner 并发连接 | `userRunnersEntry.mu` 保护，后者踢掉前者 |
| 用户删除在线 runner | API 层检查在线状态，在线时先断开再删 |
| DB 迁移失败 | `CREATE TABLE IF NOT EXISTS` + `INSERT OR IGNORE` |
