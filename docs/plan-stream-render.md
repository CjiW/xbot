# Plan: Stream Render Compatibility

> Generated: 2026-04-13
> Status: Pending Review

## Background & Goal

Currently, when `enable_stream=true`, the agent calls `llm.CollectStream()` which **synchronously blocks** until all stream events are collected, then returns a complete `LLMResponse`. The CLI already has infrastructure to handle `IsPartial=true` messages (streaming UI with blinking cursor, 100ms tick refresh), but **never receives partial messages** because the agent collects the entire stream first.

**Goal**: Allow channels to opt into real-time stream rendering. When enabled, content deltas are pushed to the channel as they arrive from the LLM, enabling live text display instead of waiting for the full response.

## Current State Analysis

### Key Files
| File | Role | Change Type |
|------|------|-------------|
| `llm/stream.go` | `CollectStream()` — synchronous event collector | Modify: add `CollectStreamWithCallback` |
| `agent/engine.go` | `generateResponse()` — stream bridge point | Modify: accept stream callback |
| `agent/engine.go` | `RunConfig` — agent configuration | Modify: add `StreamContentFunc` field |
| `agent/engine_run.go` | `callLLM()` — LLM invocation | Modify: pass callback to `generateResponse` |
| `agent/engine_wire.go` | RunConfig assembly + `enable_stream` wiring | Modify: wire `StreamContentFunc` from channel |
| `agent/agent.go` | `sendMessage()` — outbound message dispatch | Modify: add `sendStreamContent()` method |
| `channel/capability.go` | Optional channel interfaces | Modify: add `StreamRenderer` interface |
| `channel/cli.go` | CLI channel | Modify: implement `StreamRenderer` |

### Current Streaming Data Flow (Broken)

```
LLM SSE → processStream goroutine → eventCh(100)
  → CollectStream() [BLOCKS until EventDone]
    → returns complete LLMResponse
      → agent processes response (tool calls / final reply)
        → sends single OutboundMessage via bus
          → CLI receives complete message
```

### Proposed Streaming Data Flow (Fixed)

```
LLM SSE → processStream goroutine → eventCh(100)
  → CollectStreamWithCallback(eventCh, onContent)
    → on each EventContent: accumulate + call onContent(accText)
      → sendStreamContent(channel, chatID, accText) [IsPartial=true]
        → bus.Outbound → Dispatcher → CLI.Send()
          → CLI updates streaming message in viewport (100ms tick)
    → on EventDone: return complete LLMResponse
      → agent processes response normally (tool calls / final reply)
        → sends final OutboundMessage [IsPartial=false]
          → CLI marks streaming message as complete
```

### Risks
- **Progress events vs stream content**: The agent sends progress notifications (tool status) during the run loop. Stream content and progress must not conflict in the CLI viewport. Current design already separates them (streaming message vs progress block), so this should be safe.
- **Tool call responses**: When the LLM responds with tool calls (not text), `EventContent` may be empty. The callback should handle empty content gracefully (no-op).
- **Reasoning/thinking content**: `EventReasoningContent` should NOT be streamed to the channel (thinking is stripped in the final response). Only `EventContent` triggers the callback.
- **Performance**: Each content delta triggers a bus message + CLI render cycle. The 100ms tick already batches updates, so this is acceptable. For non-CLI channels, we may want throttling.
- **Backward compatibility**: `StreamContentFunc` is nil by default → `generateResponse` falls back to `CollectStream` → no behavior change for existing code.

## Detailed Plan

### Phase 1: LLM Layer — Add callback-based stream collection

- [ ] **Step 1.1**: Add `CollectStreamWithCallback` to `llm/stream.go`
  - Signature: `func CollectStreamWithCallback(ctx context.Context, eventCh <-chan StreamEvent, onContent func(content string)) (*LLMResponse, error)`
  - Same logic as `CollectStream`, but on each `EventContent`, after accumulating, calls `onContent(content.String())`
  - Does NOT call `onContent` for `EventReasoningContent` (thinking tokens)
  - Handles `EventError` the same way (returns partial content)

- [ ] **Step 1.2**: Add unit test for `CollectStreamWithCallback` in `llm/stream_test.go`
  - Verify callback is called with accumulated content on each `EventContent`
  - Verify callback is NOT called for reasoning content
  - Verify error handling returns partial content

### Phase 2: Agent Layer — Wire stream callback through RunConfig

- [ ] **Step 2.1**: Add `StreamContentFunc func(content string)` to `RunConfig` in `agent/engine.go`
  - When set, `generateResponse` uses `CollectStreamWithCallback` instead of `CollectStream`
  - Called with accumulated text content on each content delta
  - Not called for thinking/reasoning content

- [ ] **Step 2.2**: Modify `generateResponse()` in `agent/engine.go`
  - Add `streamContentFn func(string)` parameter
  - When `stream=true && streamContentFn != nil`: use `CollectStreamWithCallback`
  - When `stream=true && streamContentFn == nil`: use `CollectStream` (backward compat)
  - When `stream=false`: use `Generate` (unchanged)

- [ ] **Step 2.3**: Update `callLLM()` in `agent/engine_run.go`
  - Pass `s.cfg.StreamContentFunc` to `generateResponse()`

- [ ] **Step 2.4**: Add `sendStreamContent()` method to `Agent` in `agent/agent.go`
  - Sends `OutboundMessage{IsPartial: true}` through bus or `directSend`
  - Separate from `sendMessage()` to avoid `sessionFinalSent` check and metadata pollution

- [ ] **Step 2.5**: Wire `StreamContentFunc` in `engine_wire.go`
  - When building `buildMainRunConfig`: if `cfg.Stream == true`, set `StreamContentFunc` to call `a.sendStreamContent(channel, chatID, content)`
  - This means: streaming is only enabled when both `enable_stream=true` AND the channel supports it (the bus/dispatcher will deliver `IsPartial` messages)

### Phase 3: Channel Layer — StreamRenderer capability

- [ ] **Step 3.1**: Add `StreamRenderer` interface to `channel/capability.go`
  ```go
  // StreamRenderer is implemented by channels that support real-time stream rendering.
  // When a channel implements this interface AND enable_stream=true in settings,
  // the agent pushes content deltas as IsPartial messages during LLM streaming.
  type StreamRenderer interface {
      // SupportsStreamRender returns true if the channel can render stream content in real-time.
      SupportsStreamRender() bool
  }
  ```

- [ ] **Step 3.2**: Implement `StreamRenderer` on `CLIChannel` in `channel/cli.go`
  - `SupportsStreamRender() bool` returns `true`

### Phase 4: CLI — Verify existing streaming UI works

- [ ] **Step 4.1**: Test end-to-end flow
  - Enable `enable_stream=true` in settings
  - Send a message → verify content appears incrementally (not all at once)
  - Verify final message is marked complete (no `...` suffix, no blinking cursor)
  - Verify tool call responses still work (no content delta → no partial message)
  - Verify progress notifications still display correctly alongside streaming content

### Phase 5: Non-CLI channels (Feishu, Web) — Optional opt-in

- [ ] **Step 5.1**: Feishu channel — do NOT implement `StreamRenderer` initially
  - Feishu's card-based UI doesn't benefit from per-token updates (API rate limits, card re-rendering cost)
  - Can be added later if needed

- [ ] **Step 5.2**: Web channel — implement `StreamRenderer` if WebSocket supports it
  - WebSocket can easily push partial content
  - Check if web frontend handles `IsPartial` messages
  - If not, defer to future work

## Verification Plan

1. **Unit test**: `CollectStreamWithCallback` — callback fires correctly for content, skips reasoning
2. **Integration test**: Agent with `StreamContentFunc` set → verify callback is called during `Run()`
3. **Manual test (CLI)**:
   - `enable_stream=true` → type a question → observe text appearing word-by-word
   - `enable_stream=false` (default) → text appears all at once (no regression)
   - During streaming: verify progress block (tool status) still shows correctly
   - After streaming: verify final message renders properly with markdown

## Rollback Strategy

- All changes are additive (new function, new interface, new field)
- `StreamContentFunc` defaults to nil → zero behavior change when not set
- `StreamRenderer` is an optional interface → channels that don't implement it are unaffected
- To rollback: revert the branch; no data migration needed

## Notes

- The CLI already has full streaming UI support (`updateStreamingOnly()`, blinking cursor, 100ms tick). The only missing piece is the agent actually sending partial messages. This is a **wiring fix**, not a UI rewrite.
- `sendStreamContent` should go through bus (not `directSend`) — streaming content is transient UI-only, doesn't need message ID tracking or `sessionFinalSent` check.
- For non-CLI channels, `IsPartial=true` messages should be dropped or handled gracefully. The dispatcher already delivers to the registered channel; channels that don't handle `IsPartial` will just display them as regular messages (acceptable behavior, not a crash).

✅ Self-review passed
