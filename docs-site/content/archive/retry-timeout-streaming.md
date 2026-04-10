---
title: "retry-timeout-streaming"
weight: 240
---

# Retry Timeout + Streaming Engine 实施方案

## 目标

1. **设置 RetryConfig.Timeout = 120s**：确保每次 LLM 重试有独立的 120 秒超时窗口
2. **Engine 切换流式 LLM 调用**：将 engine.go 的 `Generate()` 改为 `GenerateStream()`，收集 stream events 组装为 `LLMResponse`，过程中实时更新进度

## 任务 1：RetryConfig.Timeout = 120s

### 分析

- `DefaultRetryConfig()` 中 `Timeout` 字段当前为 0（不设超时）
- `perAttemptCtx` 在 Timeout=0 时会尝试从父 ctx deadline 推导，父 ctx 也无 deadline 则不设超时
- 需要：DefaultRetryConfig 设置 Timeout=120s，同时支持通过环境变量覆盖

### 改动

| 文件 | 改动 |
|------|------|
| `llm/retry.go` | `DefaultRetryConfig()` 增加 `Timeout: 120 * time.Second` |
| `config/config.go` | 新增 `LLMRetryTimeout time.Duration` 配置项 |
| `config/config.go` | `Default()` 增加默认值 `120s` + 环境变量 `LLM_RETRY_TIMEOUT` |
| `main.go` | `createLLM` 调用处传入 `Timeout: cfg.Agent.LLMRetryTimeout` |
| `llm/retry_test.go` | 更新 `TestDefaultRetryConfig` 断言 |

## 任务 2：Engine 切换流式 LLM

### 分析

当前 engine.go 调用 `cfg.LLMClient.Generate()`，等待完整响应后才返回。
流式方案需要：
1. 检查 LLMClient 是否实现 `StreamingLLM` 接口
2. 调用 `GenerateStream()` 获取 `<-chan StreamEvent`
3. 遍历 events 收集 content、tool_calls、usage，组装为 `LLMResponse`
4. 遍历过程中，**每收到一段 content 就实时更新进度**（可选，不影响功能）

### 关键设计决策

**Q: 是否需要回退到非流式？**
是的。如果 LLMClient 不实现 StreamingLLM（如某些自定义实现），回退到 `Generate()`。

**Q: 流式收集逻辑放哪里？**
新增 `llm/stream.go`，提供 `CollectStream(ctx, eventCh)` 函数，将 `<-chan StreamEvent` 转为 `(*LLMResponse, error)`。可被 engine 复用，也可被其他调用方复用。

**Q: 进度展示？**
流式过程中，每收到一批 content delta，更新进度行：`> 💭 正在思考...` → `> 💭 正在思考: [前100字摘要]...`
收到 tool_call 时显示 `> 🔧 调用工具: Read, Shell, ...`

### 改动

| 文件 | 改动 |
|------|------|
| `llm/stream.go` | 新增 `CollectStream(ctx, <-chan StreamEvent) (*LLMResponse, error)` |
| `llm/stream_test.go` | 新增测试 |
| `agent/engine.go` | LLM 调用处：先尝试 `GenerateStream` + `CollectStream`，回退 `Generate` |

### CollectStream 逻辑

```go
func CollectStream(ctx context.Context, eventCh <-chan StreamEvent) (*LLMResponse, error) {
    var resp LLMResponse
    var content strings.Builder
    var reasoningContent strings.Builder
    toolCalls := make(map[int]*ToolCallDelta)  // index → accumulated delta
    
    for ev := range eventCh {
        switch ev.Type {
        case EventContent:
            content.WriteString(ev.Content)
        case EventReasoningContent:
            reasoningContent.WriteString(ev.ReasoningContent)
        case EventToolCall:
            tc := toolCalls[ev.ToolCall.Index]
            if tc == nil {
                tc = &ToolCallDelta{Index: ev.ToolCall.Index}
                toolCalls[ev.ToolCall.Index] = tc
            }
            tc.ID = firstNonEmpty(tc.ID, ev.ToolCall.ID)
            tc.Name = firstNonEmpty(tc.Name, ev.ToolCall.Name)
            tc.Arguments += ev.ToolCall.Arguments
        case EventUsage:
            resp.Usage = *ev.Usage
        case EventDone:
            resp.FinishReason = ev.FinishReason
        case EventError:
            return nil, fmt.Errorf("stream error: %s", ev.Error)
        }
    }
    
    resp.Content = content.String()
    resp.ReasoningContent = reasoningContent.String()
    // 将 map 转为有序 slice
    for i := 0; i < len(toolCalls); i++ {
        if tc, ok := toolCalls[i]; ok {
            resp.ToolCalls = append(resp.ToolCalls, ToolCall{
                ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments,
            })
        }
    }
    return &resp, nil
}
```

### Engine 调用改动

```go
// engine.go Run() 主循环中的 LLM 调用
var response *llm.LLMResponse
if streaming, ok := cfg.LLMClient.(llm.StreamingLLM); ok {
    eventCh, streamErr := streaming.GenerateStream(retryNotifyCtx, cfg.Model, messages, toolDefs, cfg.ThinkingMode)
    if streamErr != nil {
        err = streamErr
    } else {
        response, err = llm.CollectStream(ctx, eventCh)
    }
} else {
    response, err = cfg.LLMClient.Generate(retryNotifyCtx, cfg.Model, messages, toolDefs, cfg.ThinkingMode)
}
```

同理，input-too-long 重试路径也做相同改造。

### 不做的事

- **不做实时进度推送**：流式过程中不频繁 notifyProgress，因为 LLM 响应通常 10-60 秒，粒度太细反而刷屏。现有的 "💭 思考中..." 占位已经足够。后续可以在流式过程中逐步更新 thinking content 作为增强。
- **不改 RunConfig.LLMClient 类型**：保持 `llm.LLM` 接口，运行时 type assert 检查 StreamingLLM

## 执行顺序

1. `llm/retry.go`: DefaultRetryConfig 增加 Timeout=120s
2. `llm/stream.go` + `llm/stream_test.go`: CollectStream 实现 + 测试
3. `config/config.go` + `main.go`: LLMRetryTimeout 配置
4. `agent/engine.go`: LLM 调用切换到流式
5. `llm/retry_test.go`: 更新测试断言
6. `go build ./...` + `go test ./...` 验证
