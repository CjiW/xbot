# Web Channel V2 — 圆桌会议纪要

> 日期：2026-03-27
> 分支：`feat/web-fancy`（基于 master `aa7fa0c`）
> 参会方：方案拥护者、魔鬼代言人、工程实践者、用户视角

---

## 一、共识结论

### ✅ 批准事项

| 功能 | 决议 | 备注 |
|------|------|------|
| **代码块语法高亮 + 复制按钮** | ✅ P0 | highlight.js，纯前端，1-2天 |
| **停止生成按钮** | ✅ P0 | 后端 `/cancel` 已就绪，前端 1天 |
| **结构化进度展示** | ✅ P1 | 补 `ProgressEventHandler` 管道，3-4天 |
| **文件上传下载** | ✅ P2 | HTTP 通道，10MB 限制，5-8天 |
| **增强输入区** | ✅ P2 | Markdown 工具栏 + 预览，2-3天 |
| **Settings 页面** | ✅ P2 | localStorage，主题+字号，2天 |
| **WS 断线重连** | ✅ P1 | 前端 exponential backoff，1天 |

### ❌ 否决事项

| 功能 | 决议 | 理由 |
|------|------|------|
| **Tiptap 富文本编辑器** | ❌ 全员反对 | 富文本→纯文本有损、LLM 侧无收益、竞品均用纯文本、维护成本高 |
| **framer-motion 动画库** | ❌ | 纯 CSS @keyframes 可替代 |
| **shadcn/ui / MUI** | ❌ | 过重，手写组件足够 |
| **文件走 WS 传输** | ❌ | WS 64KB 限制（client→server），HTTP 更标准 |

### ⚠️ 延后事项

| 功能 | 决议 | 理由 |
|------|------|------|
| **真流式输出（打字机效果）** | ⏸️ 延后 | engine.go `CollectStream` 贪心消费 + Run 循环多轮迭代架构约束，改 engine 核心循环风险大，ROI 不如进度展示 |
| **Sidebar 会话列表** | ⏸️ 延后 | 功能填充不足前不做布局重构 |

---

## 二、关键代码发现

### 发现 1：`ProgressEventHandler` 是空壳

```go
// engine.go:91-92 — 字段定义
ProgressEventHandler func(event *ProgressEvent)

// engine.go:367-373 — engine 内部调用
if cfg.ProgressEventHandler != nil {
    cfg.ProgressEventHandler(&ProgressEvent{...})
}

// engine_wire.go:136-143 — 只注入了 ProgressNotifier
if autoNotify {
    cfg.ProgressNotifier = func(lines []string) { ... }
    // ← ProgressEventHandler 从未被注入！
}
```

**影响**：`StructuredProgress`（Phase/ActiveTools/CompletedTools）已构建但被丢弃，需在 `buildMainRunConfig` 中补接。

### 发现 2：停止生成后端已就绪

```go
// agent.go:960 — /cancel 命令拦截
if strings.TrimSpace(strings.ToLower(msg.Content)) == "/cancel" {
    cancelKey := msg.Channel + ":" + msg.ChatID + ":" + msg.SenderID
    ch.(chan struct{}) <- struct{}{}  // → reqCancel() → context cancellation
}
```

前端只需发送 `{"type":"message","content":"/cancel"}` 即可触发。

### 发现 3：进度消息堆积 Bug（P0）

当前前端把每条 `type:"progress"` WS 消息当作独立气泡追加，导致 5-20 条进度消息淹没聊天区。需改为"进度区域"概念，独立于消息列表。

### 发现 4：`setLoading(false)` 时机错误

当前收到任意 WS 消息就关闭 loading，应在收到最终回复（text/card）时才关闭。

### 发现 5：LLM 流式被 CollectStream 吞掉

```go
// engine.go:245-254
func generateResponse(...) (*llm.LLMResponse, error) {
    eventCh, _ := streaming.GenerateStream(...)
    return llm.CollectStream(ctx, eventCh)  // ← 所有 StreamEvent 被聚合
}
```

真流式需要穿透此处，但 Run 循环的多轮迭代（tool_calls vs final text）使改造成本巨大。

### 发现 6：WS 64KB 限制方向

`c.conn.SetReadLimit(65536)` 只限 client→server 方向，server→client 无限制。文件传输走 HTTP 不受影响。

---

## 三、分阶段实施计划

### Phase 1：基础体验补全（P0，预计 3-4 天）

| # | 任务 | 层 | 天数 | 依赖 |
|---|------|----|------|------|
| 1 | 代码块语法高亮 + 复制按钮 | 前端 | 1 | 无 |
| 2 | 停止生成按钮 | 前端+后端 | 1 | 无 |
| 3 | 修复进度消息堆积 Bug | 前端 | 0.5 | 无 |
| 4 | 修复 loading 状态时机 | 前端 | 0.25 | 无 |
| 5 | 消息时间戳显示 | 前端 | 0.25 | 无 |
| 6 | 智能滚动（上滚暂停自动滚动） | 前端 | 0.5 | 无 |
| 7 | WS 断线自动重连 | 前端 | 0.5 | 无 |

### Phase 2：差异化功能（P1，预计 4-5 天）

| # | 任务 | 层 | 天数 | 依赖 |
|---|------|----|------|------|
| 8 | 接通 ProgressEventHandler 管道 | 后端 | 1 | Phase 1 |
| 9 | WS 协议扩展（progress_structured 类型） | 后端 | 0.5 | #8 |
| 10 | 前端进度面板（折叠式工具列表） | 前端 | 2 | #9 |
| 11 | 进度区到最终回复的过渡动画 | 前端 | 0.5 | #10 |

### Phase 3：能力扩展（P2，预计 7-10 天）

| # | 任务 | 层 | 天数 | 依赖 |
|---|------|----|------|------|
| 12 | 文件下载（GET /api/files/:id） | 后端 | 1 | Phase 2 |
| 13 | 文件上传（POST /api/files/upload + 拖拽+粘贴+按钮） | 后端+前端 | 3 | #12 |
| 14 | 增强输入区（Markdown 工具栏 + 预览切换） | 前端 | 2 | 无 |
| 15 | Settings 页面（主题+字号，localStorage） | 前端 | 2 | 无 |
| 16 | Todo checklist 渲染（前端解析 [x]/[ ]） | 前端 | 1 | 无 |
| 17 | 消息历史分页/虚拟滚动 | 前端+后端 | 2 | 无 |

### Phase 4：进阶体验（P3，按需）

| # | 任务 | 层 | 天数 | 备注 |
|---|------|----|------|------|
| 18 | 真流式输出（token 级打字机） | 全栈 | 10-15 | 需改造 engine.go 核心 |
| 19 | Sidebar 布局 + 会话列表 | 全栈 | 3-5 | 需后端会话管理 API |
| 20 | 移动端适配 | 前端 | 2-3 | 响应式布局 |

**总工期估算：14-19 天（Phase 1-3），不含 Phase 4**

---

## 四、后端改动汇总

### WS 协议扩展

```go
// 服务端 → 客户端（wsMessage 扩展）
type wsMessage struct {
    Type      string               `json:"type"`                         // "text","progress","progress_structured","card","file"
    ID        string               `json:"id,omitempty"`
    Content   string               `json:"content,omitempty"`
    TS        int64                `json:"ts,omitempty"`
    Progress  *progressPayload     `json:"progress,omitempty"`           // 新增：结构化进度
    File      *filePayload         `json:"file,omitempty"`               // 新增：文件元数据
}

type progressPayload struct {
    Phase          string           `json:"phase"`            // thinking|tool_exec|compressing|done
    Iteration      int              `json:"iteration"`
    ActiveTools    []toolProgress   `json:"active_tools"`
    CompletedTools []toolProgress   `json:"completed_tools"`
}

type toolProgress struct {
    Name    string `json:"name"`
    Label   string `json:"label"`
    Status  string `json:"status"`   // running|done|error
    Elapsed int    `json:"elapsed_ms"`
}

type filePayload struct {
    ID   string `json:"id"`
    Name string `json:"name"`
    Size int64  `json:"size"`
    Mime string `json:"mime"`
    URL  string `json:"url"`
}
```

```go
// 客户端 → 服务端（wsClientMessage 扩展）
type wsClientMessage struct {
    Type    string   `json:"type"`                // "message","cancel"
    Content string   `json:"content,omitempty"`
    FileIDs []string `json:"file_ids,omitempty"`  // 新增：附件引用
}
```

### 新增 API 端点

| 端点 | 方法 | 说明 |
|------|------|------|
| `/api/files/upload` | POST | multipart 文件上传，10MB 限制 |
| `/api/files/:id` | GET | 文件下载 |

---

## 五、风险与缓解

| 风险 | 严重度 | 缓解 |
|------|--------|------|
| 停止按钮只断 WS 不发 /cancel | 🔴 高 | 测试用例覆盖；前端走 WS 消息不 close |
| 进度推送频率过高卡顿 | 🟡 中 | 后端节流 ≥500ms；前端 requestAnimationFrame 合并 |
| ProgressEventHandler 影响飞书渠道 | 🟡 中 | 按 channel 条件注入，飞书不设置则不触发 |
| 打字机动画不完整 Markdown | 🟡 中 | 最终回复直接显示，不做打字机（准流式方案已弃用打字机） |
| 文件上传恶意文件 | 🟡 中 | MIME 校验 + 大小硬限 + 存储隔离 |

---

## 六、WS 协议向后兼容

新增字段全部 `omitempty`，新增 type 值不影响旧客户端。旧客户端忽略未知 type 即可。WS 协议为真流式预留 `stream_start`/`stream_delta`/`stream_end` type（不实现，协议兼容）。

---

*会议结束，待陛下批准后启动实施。*
