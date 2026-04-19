# Plan: Remote Mode Architecture — Proper Channel Registration + Bug Fixes

## Summary

The remote mode (CLI→WS→server) has a fundamental routing problem: the server's dispatcher doesn't know about `channel=cli`, so all `a.bus.Outbound` calls with `channel=cli` get silently dropped. The current fix (`directSend` monkey-patch) only covers `sendMessage` paths, leaving raw `bus.Outbound` calls broken. This plan properly registers `cli` in the dispatcher and fixes the two reported bugs (cancel spam, rewind not persisting).

## Root Cause Analysis

### 1. Dispatcher Routing Gap
```
Agent.a.bus.Outbound ←msg{Channel:"cli"}→ Dispatcher.Run()
  → d.channels["cli"] = nil → "Unknown channel, dropping message"
```
Current workaround (server.go L1409-1416):
```go
directSend = func(msg) {
    if msg.Channel == "cli" → webCh.Send(msg)  // hijack
    else → disp.SendDirect(msg)                // normal
}
```
This only works when callers use `a.directSend`. Any raw `a.bus.Outbound` call is still broken.

### 2. Cancel Spam (8x "Cancel request sent")
- `sendCancel()` called once per Ctrl+C → `sendInbound("/cancel")` → server
- Server receives `/cancel`, agent loop handles it → sends "Request cancelled." via `directSend` ✓
- The 8 local "Cancel request sent" messages = 8 calls to `sendCancel()`. User likely held Ctrl+C or it fired repeatedly.
- **Real bug**: No client-side dedup. Each Ctrl+C fires `sendCancel()` independently. Should debounce.

### 3. Rewind Doesn't Delete Server Records
- `/rewind` (Ctrl+K) calls `trimHistoryFn` → `RemoteBackend.TrimHistory()` → RPC `trim_history`
- This works! `PurgeNewerThanOrEqual()` deletes from DB.
- **But**: `context_edit` tool (LLM-initiated) only modifies in-memory `messages` slice, never persists to DB. After reconnect, deleted messages reappear.
- User's "rewind" likely means context_edit (the tool the agent uses to manage its own context), not Ctrl+K.

## Changes

### 1. Register virtual CLI channel in dispatcher (`channel/web.go`)
- **What**: Add a `remoteCLIChannel` struct that wraps the web channel's hub and implements `Channel` interface. Its `Name()` returns `"cli"`, and `Send()` routes to the correct WS client via `hub.sendToClient()`.
- **Why**: Makes the dispatcher aware of `channel=cli` so ALL outbound messages (including raw `bus.Outbound`) route correctly. Eliminates the need for the `directSend` monkey-patch's `cli` special case.
- **How**: The `remoteCLIChannel` needs access to the hub and a way to resolve `msg.ChatID` → `targetClientID` (currently done in `WebChannel.Send` via `transport_chat_id` / `transport_sender_id` metadata). Extract this resolution logic into a helper.

### 2. Simplify `directSend` (`serverapp/server.go`)
- **What**: Remove the `if msg.Channel == "cli"` special case from `directSend`. Let it uniformly call `disp.SendDirect(msg)` since `"cli"` is now a registered channel.
- **Why**: The dispatcher handles routing correctly now. No more special-casing.

### 3. Fix chatWorker cancel handler (`agent/agent.go`)
- **What**: Remove the duplicate `/cancel` interceptor in `chatWorker()` (L1321-1341). It's dead code — the agent loop at L1085 already intercepts `/cancel` before it reaches the queue.
- **Why**: Dead code that uses `a.bus.Outbound` (broken for cli). Removing it eliminates confusion and potential double-response.

### 4. Fix normal response routing in chatProcessLoop (`agent/agent.go`)
- **What**: Change L1469 `a.bus.Outbound <- *response` to use `a.sendMessage()` or `a.directSend`. Non-WaitingUser responses should also go through the proper dispatch path.
- **Why**: This is the main remaining path where `bus.Outbound` drops `channel=cli` messages. After the dispatcher fix, this is technically no longer broken, but using `sendMessage` is more consistent (supports Patch updates, message ID tracking).

### 5. Debounce cancel on CLI side (`channel/cli_message.go`)
- **What**: Add a `lastCancelTime` field to `cliModel`. In `sendCancel()`, skip if less than 2 seconds since last cancel. Reset on new agent turn.
- **Why**: Prevents cancel spam from repeated Ctrl+C or key repeat.

### 6. Persist context_edit to database (`agent/context_edit.go`, `agent/agent.go`)
- **What**: After `HandleRequest()` modifies the messages slice, call a new `persistEdits()` method that syncs deletions/modifications to the session store. For `delete`/`delete_turn`/`truncate`, call `sessionSvc.PurgeNewerThanOrEqual()` or a new batch delete method. For `replace`, call `sessionSvc.UpdateMessageContent()`.
- **Why**: Without this, context_edit changes are lost on restart/reconnect in both local and remote mode.
- **Scope note**: This is a bigger change. Could be deferred to a follow-up PR if we want to keep this PR focused.

## Risks
- **Virtual CLI channel**: Must correctly resolve `targetClientID` from message metadata. If the mapping is wrong, messages go to the wrong WS client or get buffered. Mitigation: reuse the exact resolution logic from `WebChannel.Send`.
- **context_edit persistence**: `delete_turn` removes multiple messages — need a batch DB operation or loop. Current `PurgeNewerThanOrEqual` works by timestamp, not by index. May need a new `DeleteMessagesByIndex()` method.
- **Breaking change**: Removing the `directSend` cli special case changes behavior for ALL cli messages. Must verify normal replies still work.

## Definition of Done
- [ ] `channel=cli` messages route correctly via dispatcher (no "Unknown channel" logs)
- [ ] Ctrl+C cancel sends exactly 1 request (debounced)
- [ ] Cancel confirmation ("Request cancelled.") reaches CLI in remote mode
- [ ] Normal agent replies reach CLI in remote mode via all code paths
- [ ] `context_edit` deletions persist to database (survive restart/reconnect)
- [ ] `go build ./...` passes
- [ ] `go test ./...` passes

## Decisions
- **context_edit persistence**: Include in this PR
- **CLI channel registration**: At server startup, always registered
- **Cancel spam**: Debounce on client side (user may press multiple times)

## Open Questions
None — all resolved.
