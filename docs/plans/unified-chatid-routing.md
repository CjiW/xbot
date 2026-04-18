# Plan: Unified chatID Routing (Remove transport_chat_id/admin Conflation)

## Summary
Remove all `transport_chat_id/transport_channel/transport_sender_id` metadata and `sessionTransportMeta` caching. Hub routes by **business chatID** directly. `admin`/`userID` becomes pure auth identity, never used for routing.

## Architecture

### Before (broken)
```
Hub: clients[senderID="admin"][clientID] → Client
Route table: chatID="/home/smith/src/xbot" → senderID="admin"
sendMessage: overrides msg.ChatID with transport_chat_id="admin"
resolveTargetClientID: reads transport_chat_id from metadata
```

### After (clean)
```
Hub: conns[clientID] → Client (lifecycle)
Hub: subs[chatID="/home/smith/src/xbot"] → {clientID, ...} (routing)
sendMessage: msg.ChatID stays as business chatID, no overrides
remoteCLIChannel.Send(): hub.sendToClient(msg.ChatID, wsMsg) directly
```

## Changes

### channel/web.go — Hub restructure
- Remove `routes` map, `addRoute`, `removeRoute`, `resolveRoute`
- `clients map[string]map[string]*Client` → `conns map[string]*Client` + `subs map[string]map[string]bool`
- `addClient(senderID, clientID, c)` → `addClient(clientID, c)` (lifecycle only)
- `removeClient(senderID, clientID)` → `removeClient(clientID)` (cleanup conns + all subs)
- New: `subscribe(clientID, chatID)` / `unsubscribe(clientID, chatID)`
- `sendToClient(chatID, msg)` → broadcast to all clientIDs in `subs[chatID]`, offline buffer by chatID
- `getClient`/`getClients` → `getClientsByChat(chatID)` returns subscribed clients
- `stopAll` → iterate `conns`

### channel/web.go — readPump
- Remove `metadata["transport_chat_id"] = c.userID` (L1121)
- Remove `metadata["transport_sender_id"] = c.userID` (L1122)
- Remove `metadata["transport_channel"] = "web"` (L1120)
- Remove `hub.addRoute(msgChatID, c.userID)` (L1157)
- Add `hub.subscribe(c.id, msgChatID)` on each message (idempotent)

### channel/web.go — remoteCLIChannel.Send()
- Remove `resolveTargetClientID()` calls
- Use `msg.ChatID` directly as hub routing key

### channel/web.go — WebChannel.Send()
- Remove `resolveTargetClientID()` calls
- Use `msg.ChatID` directly

### channel/web.go — resolveTargetClientID()
- Delete the function entirely

### agent/agent.go — sendMessage
- Remove `sessionTransportMeta` Store (L1547-1550 in processMessage)
- Remove `sessionTransportMeta` Store (L1294-1302 in concurrent handler)
- Remove `sessionTransportMeta` Load + merge in sendMessage (L2120-2127)
- Remove `transport_chat_id` → `msg.ChatID` override (L2132-2133)
- Remove `transport_channel` → `msg.Channel` override (L2129-2130)
- Remove `__waiting_user` transport hack (now unnecessary since WaitingUser is preserved naturally)
- Keep `sessionMsgIDs`, `sessionReplyTo`, `sessionFinalSent` (business-keyed, not transport)

### agent/agent.go — processMessage
- Remove transport metadata extraction (L1538-1550)

### agent/agent.go — concurrent command handler
- Remove transport metadata extraction (L1294-1302)

### agent/agent.go — WaitingUser dispatch
- Simplify: no longer need __waiting_user metadata hack, just check response.WaitingUser

### agent/backend_remote.go — SendInbound
- Remove `transport_chat_id` override (L197-198)
- Remove `transport_sender_id` override (L200-201)
- Remove `transport_channel` extraction (L203-207)
- Remove cancel-specific `!isCancel` guard (no longer needed)

### channel/web_test.go
- Update Hub tests for new API

## Risks
- Web clients: chatID = feishuUserID, routing by chatID still works (1:1 mapping)
- Multiple CLI with same chatID: both subscribe to same chatID, broadcast to both ✓
- Feishu message patch: uses sessionMsgIDs keyed by channel:chatID, unaffected ✓
- Cancel: cancelKey = channel:chatID:senderID, now consistent since chatID is never overridden ✓

## Definition of Done
- [ ] Zero occurrences of transport_chat_id/transport_sender_id/transport_channel in codebase
- [ ] Zero occurrences of sessionTransportMeta Store/Load
- [ ] Zero occurrences of resolveTargetClientID
- [ ] Hub routes purely by business chatID
- [ ] go build ./... passes
- [ ] go test ./channel/... ./agent/... ./serverapp/... passes
