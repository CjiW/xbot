---
title: "Web Channel"
weight: 30
---


Browser-based chat interface with user authentication, REST API, WebSocket real-time communication, file upload, and marketplace support.

## Setup

### Enable

```bash
WEB_ENABLED=true
WEB_HOST=0.0.0.0
WEB_PORT=8082
WEB_PERSONA_ISOLATION=true
```

### Authentication

Two modes:

1. **Open** — Anyone can register and use the web interface.
2. **Invite-only** — Admin creates accounts via `/runner` commands or REST API.

```bash
WEB_INVITE_ONLY=true
```

### Feishu SSO

Users can link their account with Feishu OAuth for seamless login:

```bash
OAUTH_ENABLE=true
OAUTH_HOST=127.0.0.1
OAUTH_PORT=8081
```

## Features

### Real-time Chat

- WebSocket-based streaming responses
- Markdown rendering
- Code syntax highlighting
- Message history

### File Upload

- Max file size: 10MB
- Upload via REST API or drag-and-drop in the UI

### REST API

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/api/auth/register` | POST | Register new user |
| `/api/auth/login` | POST | Login |
| `/api/auth/feishu-link` | POST | Link account to Feishu |
| `/api/history` | GET | Get message history |
| `/api/history` | DELETE | Clear history |
| `/api/settings` | GET/PUT | User settings |
| `/api/llm-config` | GET/POST/DELETE | LLM configuration |
| `/api/llm-config/model` | POST | Switch model |
| `/api/llm-max-context` | GET/POST | Max context tokens |
| `/api/files/upload` | POST | Upload file |
| `/api/search` | GET | Global search |
| `/api/runner/token` | GET/POST/DELETE | Runner token management |
| `/api/runners` | GET/POST | List/create runners |
| `/api/market/*` | * | Marketplace (browse/install/publish) |

### Persona Isolation

When `WEB_PERSONA_ISOLATION=true`, each web user gets an isolated agent persona — their core memory, settings, and conversation history are separate from other users.

### Marketplace

Users can browse, install, publish, and uninstall skills and agents through the marketplace API.

## Frontend

The web frontend is built with:

- **React 19** + **Vite** + **TailwindCSS 4**
- Located in `web/` directory
- Served as static files via `WEB_STATIC_DIR`

## Security Notes

- Web users (`web-*` IDs) are blocked from server-side sandbox access by default
- They must connect their own remote runner for tool execution
- Override with `WEB_USER_SERVER_RUNNER=true` (not recommended for production)
