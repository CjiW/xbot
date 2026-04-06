# xbot

An extensible AI Agent built with Go. Features a message bus + plugin architecture with multi-channel support (CLI, Feishu, QQ), tool calling, pluggable memory, skills, and scheduled tasks.

## Quick Start

### Install

```bash
# Install latest release (Linux/macOS, amd64/arm64)
curl -fsSL https://raw.githubusercontent.com/CjiW/xbot/master/scripts/install.sh | bash

# Install a specific version
VERSION=v0.0.7 curl -fsSL https://raw.githubusercontent.com/CjiW/xbot/master/scripts/install.sh | bash

# Custom install path (default: /usr/local/bin)
INSTALL_PATH=~/.local/bin curl -fsSL https://raw.githubusercontent.com/CjiW/xbot/master/scripts/install.sh | bash
```

### Build from Source

```bash
git clone https://github.com/CjiW/xbot.git
cd xbot
make build
./xbot
```

### First Run

Launch `xbot-cli` and an interactive setup wizard appears — configure your LLM provider, API key, model, and sandbox mode through a TUI panel. Or edit `~/.xbot/config.json` directly:

```json
{
  "llm": {
    "provider": "openai",
    "api_key": "sk-xxx",
    "base_url": "https://api.openai.com/v1",
    "model": "gpt-4o"
  },
  "sandbox": {
    "mode": "none"
  },
  "agent": {
    "memory_provider": "flat"
  }
}
```

Works with any OpenAI-compatible API (DeepSeek, Qwen, Ollama, etc.) — just change `base_url`.

## CLI Channel

The CLI is the primary interface — a rich terminal UI powered by [Bubble Tea](https://github.com/charmbracelet/bubbletea).

> **Platform**: Linux and macOS (amd64 / arm64)

### Usage

```bash
# Interactive mode — launch the TUI
xbot-cli

# One-shot mode — pass a prompt directly
xbot-cli "explain this concept"

# Pipe mode
echo "what does this do" | xbot-cli
```

### Keyboard Shortcuts

| Shortcut | Action |
|----------|--------|
| `Enter` | Send message |
| `Ctrl+Enter` / `Ctrl+J` | Insert newline |
| `Tab` | Autocomplete (`/` commands, `@` file paths) |
| `↑` / `↓` | Browse input history / scroll messages |
| `PgUp` / `PgDn` | Page up / down |
| `Home` / `End` | Jump to top / bottom |
| `Esc` | Cancel / clear input |
| `Ctrl+C` | Interrupt current operation |
| `Ctrl+K` | Context editing (trim conversation history by turns) |
| `Ctrl+O` | Toggle tool summary expand/collapse |
| `Ctrl+E` | Toggle long message folding |
| `^` | Open background task panel |

### CLI Commands

| Command | Description |
|---------|-------------|
| `/settings` | Open settings panel |
| `/setup` | Re-run initial configuration wizard |
| `/update` | Check and install latest version |
| `/new` | Start a new session |
| `/clear` | Clear screen |
| `/compact` | Manually trigger context compression |
| `/context` | Show context and token usage |
| `/model` | Switch model |
| `/models` | List available models |
| `/cancel` | Cancel current processing |
| `/search` | Search message history |
| `/tasks` | Open background task panel |
| `/help` | Show help |
| `/exit` / `/quit` | Exit |
| `!<command>` | Pass-through (run directly in sandbox, skip LLM) |

### Features

- **Streaming output** — Real-time AI response display
- **Markdown rendering** — Syntax highlighting, tables, lists
- **Mermaid diagrams** — Render Mermaid code blocks as ASCII art
- **Progress tracking** — Tool execution status, SubAgent state, iteration counter
- **6 color themes** — Midnight, Ocean, Forest, Sunset, Rose, Mono
- **Background tasks** — Shell commands run in background, `^` to inspect, `/tasks` to manage
- **Context editing** — `Ctrl+K` to trim history by turns, synced to database
- **Message search** — `/search` with match highlighting
- **AskUser** — Agent can prompt the user for input mid-conversation
- **Built-in skills/agents** — `skill-creator`, `agent-creator` shipped with the binary

See [docs/cli-channel.md](docs/cli-channel.md) for details.

## Features

- **Multi-channel** — Message bus with Feishu (WebSocket), QQ (WebSocket), NapCat (OneBot 11), Web, and CLI (TUI) support
- **Built-in tools** — Shell, file I/O, Glob/Grep, web search, cron, SubAgent, download, context editing
- **Feishu integration** — Interactive cards, doc/wiki/bitable access, file upload
- **Skills system** — Progressive skill loading with embedded built-in skills
- **Pluggable memory** — Dual-mode: Flat (simple, default) or Letta (three-tier MemGPT)
- **Multi-tenant** — Channel + chatID based isolation
- **MCP protocol** — Global + user-private config, session-level lazy loading
- **Workspace isolation** — File ops scoped to user workspace, commands run in sandbox
- **SubAgent** — Delegate tasks to sub-agents with predefined roles
- **Hot-reload prompts** — System prompts as Go templates with channel-specific overrides
- **KV-Cache optimized** — Context ordering maximizes LLM cache hits
- **Cron scheduling** — Scheduled tasks via cron expressions and one-shot `at` syntax

## Architecture

```
┌─────────┐     ┌────────────┐     ┌───────┐     ┌─────────┐
│  Feishu │────▶│ MessageBus │────▶│ Agent │────▶│   LLM   │
│ Channel │◀────│            │◀────│       │◀────│         │
└─────────┘     └────────────┘     │       │     └─────────┘
                                   │       │
┌─────────┐                        │       │────▶ Tools
│   QQ    │                        │       │
└─────────┘                        │       │
                                   │       │
┌─────────┐                        │       │
│ NapCat  │                        │       │
└─────────┘                        └───────┘
```

### Core Components

| Directory | Description |
|-----------|-------------|
| `bus/` | Inbound/Outbound message channels |
| `channel/` | IM channels (feishu, qq, napcat, web, cli), dispatcher |
| `agent/` | Agent loop: LLM → tool calls → response |
| `llm/` | LLM clients (OpenAI-compatible, Anthropic) |
| `tools/` | Tool registry and implementations |
| `memory/` | Memory providers (flat/letta) |
| `config/` | Configuration from env vars / `.env` |
| `cron/` | Scheduled task scheduler |
| `crypto/` | AES-256-GCM encryption for API keys |
| `logger/` | Structured logging with file rotation |
| `oauth/` | OAuth 2.0 framework |
| `session/` | Multi-tenant session management |
| `storage/` | SQLite persistence (sessions, memory, tenants) |
| `web/` | Web frontend (React 19 + Vite + TailwindCSS 4 + Tiptap) |
| `agents/` | Embedded agent role definitions |
| `cmd/` | Subcommands (xbot-cli, sandbox runner) |
| `prompt/` | Embedded default system prompt template |
| `internal/` | Internal packages (runner protocol) |

## Configuration

All config via environment variables or `.env` file. See `.env.example` for a full template.

### LLM

| Variable | Description | Default |
|----------|-------------|---------|
| `LLM_PROVIDER` | LLM provider (`openai`/`anthropic`) | `openai` |
| `LLM_BASE_URL` | API URL | `https://api.openai.com/v1` |
| `LLM_API_KEY` | API key | — |
| `LLM_MODEL` | Model name | `gpt-4o` |
| `LLM_RETRY_ATTEMPTS` | Retry count on LLM failure | `5` |
| `LLM_RETRY_DELAY` | Initial retry delay | `1s` |
| `LLM_RETRY_MAX_DELAY` | Max retry delay | `30s` |
| `LLM_RETRY_TIMEOUT` | Single LLM call timeout | `120s` |

### Agent

| Variable | Description | Default |
|----------|-------------|---------|
| `AGENT_MAX_ITERATIONS` | Max tool-call iterations | `2000` |
| `AGENT_MAX_CONCURRENCY` | Max concurrent LLM calls | `3` |
| `AGENT_MAX_CONTEXT_TOKENS` | Max context tokens | `200000` |
| `AGENT_ENABLE_AUTO_COMPRESS` | Enable auto context compression | `true` |
| `AGENT_COMPRESSION_THRESHOLD` | Token ratio to trigger compression | `0.7` |
| `AGENT_CONTEXT_MODE` | Context management mode | — |
| `AGENT_PURGE_OLD_MESSAGES` | Purge old messages after compression | `false` |
| `MAX_SUBAGENT_DEPTH` | SubAgent max nesting depth | `6` |

### Memory

| Variable | Description | Default |
|----------|-------------|---------|
| `MEMORY_PROVIDER` | Memory (`flat`/`letta`) | `flat` |
| `LLM_EMBEDDING_PROVIDER` | Embedding provider (`openai`/`ollama`) | — |
| `LLM_EMBEDDING_BASE_URL` | Embedding API URL | — |
| `LLM_EMBEDDING_API_KEY` | Embedding API key | — |
| `LLM_EMBEDDING_MODEL` | Embedding model name | — |
| `LLM_EMBEDDING_MAX_TOKENS` | Embedding model max tokens | `2048` |

### Channels

| Variable | Description | Default |
|----------|-------------|---------|
| `FEISHU_ENABLED` | Enable Feishu | `false` |
| `FEISHU_APP_ID` | Feishu app ID | — |
| `FEISHU_APP_SECRET` | Feishu app secret | — |
| `FEISHU_ENCRYPT_KEY` | Feishu event encryption key | — |
| `FEISHU_VERIFICATION_TOKEN` | Feishu verification token | — |
| `FEISHU_ALLOW_FROM` | Allowed user open_id list (comma-separated) | — |
| `FEISHU_DOMAIN` | Feishu domain for doc links | — |
| `QQ_ENABLED` | Enable QQ | `false` |
| `QQ_APP_ID` | QQ app ID | — |
| `QQ_CLIENT_SECRET` | QQ client secret | — |
| `QQ_ALLOW_FROM` | Allowed QQ openid list (comma-separated) | — |
| `NAPCAT_ENABLED` | Enable NapCat (OneBot 11) | `false` |
| `NAPCAT_WS_URL` | NapCat WebSocket URL | `ws://localhost:3001` |
| `NAPCAT_TOKEN` | NapCat auth token | — |
| `NAPCAT_ALLOW_FROM` | Allowed QQ number whitelist (comma-separated) | — |
| `WEB_ENABLED` | Enable Web channel | `false` |
| `WEB_HOST` | Web channel bind address | `0.0.0.0` |
| `WEB_PORT` | Web channel port | `8082` |
| `WEB_STATIC_DIR` | Frontend static files directory | — |
| `WEB_UPLOAD_DIR` | File upload directory | — |
| `WEB_PERSONA_ISOLATION` | Enable persona isolation per web user | `false` |
| `WEB_INVITE_ONLY` | Enable invite-only mode | `false` |

### Infrastructure

| Variable | Description | Default |
|----------|-------------|---------|
| `WORK_DIR` | Working directory | `.` |
| `PROMPT_FILE` | Custom prompt template | `prompt.md` |
| `SINGLE_USER` | Single-user mode | `false` |
| `SANDBOX_MODE` | Sandbox mode (`docker`/`remote`/`none`) | `docker` |
| `SANDBOX_DOCKER_IMAGE` | Docker sandbox image | `ubuntu:22.04` |
| `SANDBOX_IDLE_TIMEOUT_MINUTES` | Sandbox idle timeout (0 to disable) | `30` |
| `SANDBOX_WS_PORT` | Remote sandbox WebSocket port | `8080` |
| `SANDBOX_AUTH_TOKEN` | Sandbox runner auth token | — |
| `SANDBOX_PUBLIC_URL` | Public URL for runner connections | — |
| `OAUTH_ENABLE` | Enable OAuth | `false` |
| `OAUTH_HOST` | OAuth server bind address | `127.0.0.1` |
| `OAUTH_PORT` | OAuth server port | `8081` |
| `OAUTH_BASE_URL` | OAuth callback base URL (public HTTPS) | — |
| `XBOT_ENCRYPTION_KEY` | AES-256-GCM key (base64 32 bytes) | — |
| `TAVILY_API_KEY` | Tavily web search API key | — |
| `MCP_INACTIVITY_TIMEOUT` | MCP idle timeout | `30m` |
| `MCP_CLEANUP_INTERVAL` | MCP cleanup scan interval | `5m` |
| `SESSION_CACHE_TIMEOUT` | Session cache timeout | `24h` |
| `LOG_LEVEL` | Log level | `info` |
| `LOG_FORMAT` | Log format | `json` |
| `SERVER_HOST` | HTTP server bind address | `0.0.0.0` |
| `SERVER_PORT` | HTTP server port | `8080` |
| `PPROF_ENABLE` | Enable pprof debug endpoint | `false` |

## Memory System

### Flat (default)

Simple dual-layer: long-term memory blob + event history (grep-searchable). No external services required.

### Letta (three-tier MemGPT)

| Layer | Storage | Description |
|-------|---------|-------------|
| Core Memory | SQLite | Structured blocks always in system prompt |
| Archival Memory | chromem-go vectors | Long-term semantic search |
| Recall Memory | FTS5 | Full-text event history search |

6 Letta tools: `core_memory_append`, `core_memory_replace`, `rethink`, `archival_memory_insert`, `archival_memory_search`, `recall_memory_search`

## Skills

```
.xbot/skills/
└── my-skill/
    ├── SKILL.md          # Required: name + description
    ├── scripts/          # Optional
    ├── references/      # Optional
    └── assets/          # Optional
```

## MCP Support

### Global MCP

Create `.xbot/mcp.json`:

```json
{
  "mcpServers": {
    "server-name": {
      "command": "npx",
      "args": ["-y", "@some/mcp-server"]
    }
  }
}
```

### Session MCP

Use the `ManageTools` tool at runtime. Supports lazy loading, inactivity timeout, and stdio/HTTP transport.

## SubAgent

Delegate tasks to sub-agents with predefined roles:

```
SubAgent(task="...", role="code-reviewer")
```

Roles are resolved in priority order: user-private → global → embedded defaults. Users can add custom roles in `.xbot/agents/`.

## Deployment

### Docker

```bash
docker run -d --name xbot --restart unless-stopped \
  --security-opt seccomp=unconfined \
  --cap-add SYS_ADMIN \
  -v /opt/xbot/.xbot:/data/.xbot \
  -e WORK_DIR=/data \
  -e LLM_PROVIDER=openai \
  -e LLM_BASE_URL=https://api.openai.com/v1 \
  -e LLM_API_KEY=your_key \
  -e LLM_MODEL=gpt-4o-mini \
  -e FEISHU_ENABLED=true \
  -e FEISHU_APP_ID=your_app_id \
  -e FEISHU_APP_SECRET=your_secret \
  xbot:latest
```

> Note: Requires Docker installed on host for sandbox execution.

### Makefile

```bash
make dev       # Run in development mode
make build     # Build binary
make run       # Build and run
make test      # Run tests with race detection
make fmt       # Format code
make lint      # Run golangci-lint
make ci        # lint → build → test
make clean     # Remove binary and coverage output
```

## License

MIT
