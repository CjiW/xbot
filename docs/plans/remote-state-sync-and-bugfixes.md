# Plan: Remote Mode State Sync + Bug Fixes

## Summary

Remote mode (CLI → server via WS) has two categories of issues:
1. **Critical bugs**: Ctrl+C cancel doesn't work, RPCs leak on disconnect, cancel chatID mismatch
2. **Missing feature**: Mid-session reconnect doesn't restore active turn state (typing, progress, subagents)

## Changes

### Bug Fix 1: Ctrl+C cancel broken (CRITICAL)

**Root cause chain**:
- `RemoteBackend.SendInbound()` always sends `type="message"` — server only cancels on `type="cancel"` (web.go:933)
- Even if type were correct, server's cancel handler uses `chatID = c.userID` (= "admin") instead of resolving from WS message fields like the message handler does (web.go:1042-1054)
- Agent cancel key is `channel:chatID:senderID` — mismatch means cancel signal never reaches the running request

**`agent/backend_remote.go` — SendInbound()**
- Detect `/cancel` content, send `type: "cancel"` with correct `channel`/`chat_id`/`sender_id` fields

**`channel/web.go` — readPump cancel handler (L933-947)**
- Resolve `msgChannel`/`msgChatID`/`msgSenderID` from WS message fields (same logic as message handler L1042-1054) instead of hardcoding `c.userID`

### Bug Fix 2: RPCs leak on WS disconnect (HIGH)

**Root cause**: `readPump()` exits on WS error but doesn't clean up `b.pending` RPC map. Pending RPC callers block until timeout.

**`agent/backend_remote.go` — readPump() error path**
- Close all pending RPC channels on disconnect (same pattern as `Stop()`)

### Bug Fix 3: No history sync on reconnect (MEDIUM)

**Root cause**: `reconnectLoop()` just restarts `readPump` after reconnect. No history fetch, so messages sent during disconnect are lost.

**`agent/backend_remote.go` — reconnectLoop()**
- After successful reconnect, call `GetHistory` RPC and notify via callback so CLI can reload
- Add `OnReconnect` callback for this purpose

**`cmd/xbot-cli/main.go` — wire OnReconnect**
- Register callback to reload history via `cliCh.LoadHistory()`

### Bug Fix 4: SendInbound error silently swallowed (MEDIUM)

**Root cause**: `sendInboundFn` in main.go always returns `true`, errors only logged. CLI thinks cancel/message was sent even when WS is down.

**`cmd/xbot-cli/main.go` — sendInboundFn**
- For `/cancel` specifically, do synchronous send with short timeout so error can be returned to caller
- For normal messages, keep async but show toast on failure

### Feature: Mid-session reconnect restores active turn state (MEDIUM)

**Approach**: When a new CLI connects and there's an active agent turn on the server, push the current progress state to the new client.

**`serverapp/server.go` — handleCLIRPC**
- Add `get_active_progress` RPC: returns current `CLIProgressPayload` for the given chat if an agent turn is in progress

**`agent/backend.go` — AgentBackend interface**
- Add `GetActiveProgress(ch, chatID string) (*channel.CLIProgressPayload, error)`

**`agent/backend_local.go` — GetActiveProgress()**
- Return current progress from agent's progress tracker if turn is active

**`agent/backend_remote.go` — GetActiveProgress()**
- RPC wrapper

**`cmd/xbot-cli/main.go` — post-connect flow**
- After `GetHistory()`, call `GetActiveProgress()` — if non-nil, set `m.typing=true` and `m.progress=payload`

### Bug Fix 5: Server shutdown not detected by CLI (LOW)

**Root cause**: `reconnectLoop` retries forever with no user notification.

**`agent/backend_remote.go` — reconnectLoop()**
- After 3+ consecutive failures, send system message via `OnOutbound` callback to notify user

## Risks

- **Cancel chatID resolution**: Must use same field-mapping logic as message handler. Test with both web and CLI clients.
- **GetActiveProgress race**: Agent progress is updated from goroutines. Need read lock or atomic snapshot.
- **OnReconnect callback timing**: History reload must not race with incoming live messages. Use `LoadHistory()` which is already mutex-protected.

## Definition of Done

- [ ] Ctrl+C in remote mode cancels the running request (typing indicator clears)
- [ ] WS disconnect cleans up pending RPCs (no goroutine leaks)
- [ ] Reconnect after disconnect reloads history (no lost messages)
- [ ] Mid-session reconnect shows active progress if agent is running
- [ ] SendInbound failure shows user-visible error (not silent)
- [ ] 3+ consecutive reconnect failures show notification in TUI
- [ ] `go build ./...` ✅ `go test ./...` ✅ `golangci-lint run ./...` ✅

## Open Questions

- Should `GetActiveProgress` also return the current streaming text (StreamContent)? This would allow the new CLI to show partial response. → Yes, include it.
