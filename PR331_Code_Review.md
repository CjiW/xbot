# PR #331 Code Review Report

**PR**: feat: add NapCat (OneBot 11) channel for QQ via NapCat  
**仓库**: CjiW/xbot  
**Files**: 4 files, +1283/-0  
**Author**: CjiW  
**审查时间**: 2026-03-26

---

## 📋 Files Changed

| 文件 | 变更 | 说明 |
|------|------|------|
| `channel/napcat.go` | +881 | 新增 NapCat Channel 实现 |
| `channel/napcat_test.go` | +377 | 单元测试 |
| `config/config.go` | +15 | 配置 + 环境变量加载 |
| `main.go` | +10 | Channel 注册 |

---

## 🔴 严重问题（必须修复）

### P0-1 | `buildOutboundMessage` 所有媒体类型都当作图片发送

**位置**: `channel/napcat.go` L460-471

```go
for _, url := range media {
    segments = append(segments, map[string]any{
        "type": "image",  // ❌ record/video/file 都用 image 类型
        "data": map[string]string{
            "file": url,
        },
    })
}
```

**问题**: `record`/`video`/`file` 类型的媒体全部使用 `type: image`，NapCat/OneBot 协议会拒绝或静默失败。语音、视频、文件完全不可用。

**建议**: 根据 media 来源（`record`/`video`/`file`/`image`）映射 OneBot 类型：
- `image` → `"image"`
- `record` → `"record"`
- `video` → `"video"`
- `file` → `"file"`

---

### P0-2 | `callAPI` 超时/Stop 时 `pending` 删除存在 goroutine 泄漏

**位置**: `channel/napcat.go` L130-158

```go
select {
case data := <-ch:
    // 读取 data
case <-time.After(30 * time.Second):
    n.pendingMu.Lock()
    delete(n.pending, echo)  // ⚠️ 直接删除
    n.pendingMu.Unlock()
    return nil, fmt.Errorf(...)
case <-n.stopCh:
    n.pendingMu.Lock()
    delete(n.pending, echo)
    n.pendingMu.Unlock()
    return nil, errors.New("napcat channel stopped")
}
```

**问题**: `delete` 后，发送方 goroutine 仍持有 `ch`（无缓冲 channel），goroutine 永远阻塞在 `ch <- data` 上，无法退出 → **goroutine 泄漏**。

**建议修复**: 在 `delete` 前先 `close(ch)`，让发送方 goroutine panic 并退出：
```go
case <-time.After(30 * time.Second):
    n.pendingMu.Lock()
    if ch, ok := n.pending[echo]; ok {
        close(ch)
    }
    delete(n.pending, echo)
    n.pendingMu.Unlock()
```

---

## 🟡 中等问题

### P1-1 | `isQuickDisconnectLoop` 重置后重连窗口统计丢失

**位置**: `channel/napcat.go` L326

```go
func (n *NapCatChannel) isQuickDisconnectLoop() bool {
    n.disconnectMu.Lock()
    defer n.disconnectMu.Unlock()
    // ...
    n.disconnectTimes = nil  // ⚠️ 置 nil
    return true
}
```

**问题**: `disconnectTimes = nil` 后，第一次新断连的 `append(nil, t)` 会生成新 slice，重连窗口统计逻辑不够干净。

**风险**: 低（实际影响有限）

---

### P1-2 | 数字 QQ 号的 `at` 段被静默忽略

**位置**: `channel/napcat.go` L375

```go
QQ string `json:"qq"`
```

**问题**: `{"qq": 123456}` JSON 数字 unmarshal 到 `QQ: "0"`，不匹配任何条件，被静默忽略。用户 @数字 用户名时消息显示不正确。

**建议**: 处理 JSON 数字格式：
```go
type obAtData struct {
    QQ any `json:"qq"`  // 支持 string 或 number
}
```

---

### P1-3 | 测试缺少 `callAPI` 边界覆盖

**位置**: `channel/napcat_test.go`

**问题**: 22 个测试只覆盖消息解析和去重，无 `callAPI` 超时、stopCh 关闭场景的测试。

---

## 📝 小问题

| # | 问题 | 说明 |
|---|------|------|
| P2-1 | `napcatReconnectDelays` 与 `qq.go` 常量各自独立定义 | 未共享，行为可能不一致 |
| P2-2 | `truncate` 函数重复实现 | 项目中可能有现成工具可复用 |

---

## ✅ 优点

- `sync.Once` 防止 `Stop()` 重复调用导致 panic
- WebSocket 写操作有 `connMu` 锁保护
- 消息去重 LRU window 设计合理
- 快速断连检测防重连风暴
- API 调用 UUID 匹配响应机制正确
- 22 个单元测试覆盖核心逻辑

---

## 📊 总结

| 严重度 | 数量 | 必须修复 |
|--------|------|----------|
| 🔴 严重 | 2 | ✅ P0-1 媒体类型错误、P0-2 goroutine 泄漏 |
| 🟡 中等 | 3 | P1-1~3 建议修复 |
| 📝 小 | 2 | 可后续优化 |

**结论**: 🔴 2 个 P0 必须修复后再合并。语音/视频/文件功能完全不可用影响核心功能。P0-2 的 goroutine 泄漏在高频调用下可能导致内存持续增长。
