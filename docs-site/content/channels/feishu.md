---
title: "Feishu Channel"
weight: 20
---


WebSocket-based Feishu enterprise messaging bot. Supports interactive message cards, rich text, file/image handling, settings UI, and approval workflows.

## Setup

### 1. Create a Feishu App

1. Open [Feishu Open Platform](https://open.feishu.cn/) and create a new application.
2. In **App Credentials**, note the **App ID** and **App Secret**.
3. Under **Event Subscriptions**, enable WebSocket mode (recommended over HTTP callback).
4. Subscribe to the required events:
   - `im.message.receive_v1` — receive messages
   - `card.action.triggered` — interactive card callbacks
5. Under **Permissions**, enable the required scopes (messages, contacts, etc.).

### 2. Configure Environment

```bash
FEISHU_ENABLED=true
FEISHU_APP_ID=cli_xxxxxxxx
FEISHU_APP_SECRET=xxxxxxxxxxxxxxxxxx
```

Optional for encrypted event subscriptions:

```bash
FEISHU_ENCRYPT_KEY=xxxxxxxxxxxxxxxxxx
FEISHU_VERIFICATION_TOKEN=xxxxxxxxxxxxxxxxxx
```

### 3. Restrict Access (Optional)

Limit the bot to specific users by their Open ID:

```bash
FEISHU_ALLOW_FROM=ou_xxx1,ou_xxx2,ou_xxx3
```

## Features

### Message Types

- Text messages (plain text)
- Rich text (markdown rendering)
- Image messages (download and send)
- File messages (download and send)
- Post messages (threaded replies)

### Interactive Cards

Feishu cards (schema V2) with buttons, forms, and interactive elements:

- **Settings UI** — Configure per-user settings via interactive card forms
- **Approval Cards** — Permission control approval workflow with approve/deny buttons
- **Card Tools** — Agent can create and send interactive cards programmatically via tools

### Slash Commands

All standard commands are supported via Feishu messages:

| Command | Description |
|---------|-------------|
| `/settings` | View current settings |
| `/settings set <key> <value>` | Update a setting |
| `/new` | Start a new conversation |
| `/help` | Show help |

### Feishu MCP Tools

When the Feishu channel is active, the agent gains access to 20+ Feishu API tools:

- **Wiki** — list spaces, nodes, read/create/move wiki pages
- **Bitable** — CRUD on Bitable apps, tables, and records
- **DocX** — create/read/write Feishu documents with block-level operations
- **Drive** — upload, list, manage files and permissions
- **Download** — download files and images from chat messages

These tools require user-level OAuth authorization. The agent sends an authorization card when first accessing a protected resource.

## Architecture

```
Feishu WebSocket → Event Parser → MessageBus → Agent → Tool Execution
                                                                      ↓
                               Card Callback ← Interactive Cards ← Agent
```

## Troubleshooting

| Issue | Solution |
|-------|----------|
| Bot doesn't receive messages | Check WebSocket connection in logs; verify event subscriptions |
| Card buttons don't respond | Verify `card.action.triggered` event subscription is enabled |
| Settings commands fail | Check that settings keys exist in the schema — use `/settings` to list |
| MCP tools unauthorized | User must complete OAuth authorization when prompted |
