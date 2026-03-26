# xbot

An extensible AI Agent built with Go, featuring a message bus + plugin architecture. Supports IM channels like Feishu and QQ, with tool calling, pluggable memory, skills, and scheduled tasks.

## Features

- **Multi-channel** — Message bus architecture with Feishu (WebSocket) and QQ (WebSocket) support
- **Built-in tools** — Shell, file I/O, Glob/Grep, web search, cron, subagent, download
- **Feishu integration** — Interactive cards, doc/wiki/bitable access, file upload
- **Skills system** — OpenClaw-style progressive skill loading
- **Pluggable memory** — Dual-mode: Flat (simple) or Letta (three-tier MemGPT)
- **Multi-tenant** — Channel + chatID based isolation
- **MCP protocol** — Global + user-private config, session-level lazy loading
- **Workspace isolation** — File ops limited to user workspace, commands run in Linux sandbox
- **OAuth** — Generic OAuth 2.0 for user-level authorization
- **SubAgent** — Delegate tasks to sub-agents with predefined roles
- **Hot-reload prompts** — System prompts as Go templates
- **KV-Cache optimized** — Context ordering maximizes LLM cache hits
- **Encryption** — AES-256-GCM encryption for stored API keys and OAuth tokens
- **Cron scheduling** — Scheduled tasks via cron expressions and one-shot `at` syntax
- **Context management** — Auto-compression, topic isolation, configurable token limits

## Architecture

```
┌─────────┐     ┌────────────┐     ┌───────┐     ┌─────────┐
│  Feishu │────▶│ MessageBus │────▶│ Agent │────▶│   LLM   │
│ Channel │◀────│            │◀────│       │◀────│         │
└─────────┘     └────────────┘     │       │     └─────────┘
                                   │       │
┌─────────┐                        │       │────▶ Tools
│   QQ    │                        │       │
└─────────┘                        └───────┘
```

### Core Components

- **bus/** — Inbound/Outbound message channels
- **channel/** — IM channels (feishu, qq), dispatcher
- **agent/** — Agent loop: LLM → tool calls → response
- **llm/** — LLM clients (OpenAI-compatible, Anthropic)
- **tools/** — Tool registry and implementations
- **memory/** — Memory providers (flat/letta)
- **config/** — Configuration loading from environment variables / `.env`
- **cron/** — Scheduled task scheduler
- **crypto/** — AES-256-GCM encryption for API keys and OAuth tokens
- **logger/** — Structured logging with file rotation
- **oauth/** — OAuth 2.0 framework
- **pprof/** — Optional pprof debug endpoint
- **session/** — Multi-tenant session management
- **storage/** — SQLite persistence (sessions, memory, tenants)
- **version/** — Build version info
- **docs/** — Design documents and architecture notes
- **scripts/** — Development helper scripts

## Quick Start

```bash
# Clone and setup
git clone https://github.com/CjiW/xbot.git
cd xbot
cp .env.example .env

# Build and run
make build
./xbot

# Or development mode
make dev
```

### Makefile Commands

```bash
make dev      # Run in development mode
make build    # Build binary
make run      # Build and run
make test     # Run tests with race detection
make fmt      # Format code
make lint     # Run golangci-lint
make ci       # lint → build → test
make clean    # Remove binary and coverage output
make clean-memory # Clear .xbot data
```

## Configuration

All config via environment variables or `.env`:

### LLM

| Variable | Description | Default |
|----------|-------------|---------|
| `LLM_PROVIDER` | LLM provider (`openai`/`anthropic`) | `openai` |
| `LLM_BASE_URL` | API URL | — |
| `LLM_API_KEY` | API key | — |
| `LLM_MODEL` | Model name | `gpt-4o` |
| `LLM_RETRY_ATTEMPTS` | Retry count on LLM failure | `5` |
| `LLM_RETRY_DELAY` | Initial retry delay | `1s` |
| `LLM_RETRY_MAX_DELAY` | Max retry delay | `30s` |

### Agent

| Variable | Description | Default |
|----------|-------------|---------|
| `AGENT_MAX_ITERATIONS` | Max tool-call iterations | `100` |
| `AGENT_MAX_CONCURRENCY` | Max concurrent LLM calls | `3` |
| `AGENT_MEMORY_WINDOW` | Memory consolidation trigger | `50` |
| `AGENT_MAX_CONTEXT_TOKENS` | Max context tokens | `100000` |
| `AGENT_ENABLE_AUTO_COMPRESS` | Enable auto context compression | `true` |
| `AGENT_COMPRESSION_THRESHOLD` | Token ratio to trigger compression | `0.7` |
| `AGENT_CONTEXT_MODE` | Context management mode | — |
| `AGENT_ENABLE_TOPIC_ISOLATION` | Enable topic partition isolation (experimental) | `false` |
| `MAX_SUBAGENT_DEPTH` | SubAgent max nesting depth | `6` |
| `SUBAGENT_LLM_TIMEOUT` | SubAgent single LLM call timeout | `3m` |

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

### Infrastructure

| Variable | Description | Default |
|----------|-------------|---------|
| `WORK_DIR` | Working directory | `.` |
| `PROMPT_FILE` | Custom prompt template | `prompt.md` |
| `SINGLE_USER` | Single-user mode | `false` |
| `SANDBOX_MODE` | Sandbox mode (`docker`/`none`) | `docker` |
| `SANDBOX_DOCKER_IMAGE` | Docker sandbox image | `ubuntu:22.04` |
| `SANDBOX_IDLE_TIMEOUT_MINUTES` | Sandbox idle timeout (0 to disable) | `30` |
| `OAUTH_ENABLE` | Enable OAuth | `false` |
| `OAUTH_HOST` | OAuth server bind address | `127.0.0.1` |
| `OAUTH_PORT` | OAuth server port | `8081` |
| `OAUTH_BASE_URL` | OAuth callback base URL (public HTTPS) | — |
| `XBOT_ENCRYPTION_KEY` | AES-256-GCM key (base64 32 bytes) | — |
| `TAVILY_API_KEY` | Tavily web search API key | — |
| `MCP_INACTIVITY_TIMEOUT` | MCP idle timeout | `30m` |
| `MCP_CLEANUP_INTERVAL` | MCP cleanup scan interval | `5m` |
| `SESSION_CACHE_TIMEOUT` | Session cache timeout | `24h` |
| `STARTUP_NOTIFY_CHANNEL` | Auto-notify channel on startup | — |
| `STARTUP_NOTIFY_CHAT_ID` | Auto-notify chat ID on startup | — |
| `ADMIN_CHAT_ID` | Admin chat ID for sensitive ops | — |
| `PPROF_ENABLE` | Enable pprof debug endpoint | `false` |
| `PPROF_HOST` | pprof bind host | `localhost` |
| `PPROF_PORT` | pprof port | `6060` |
| `LOG_LEVEL` | Log level | `info` |
| `LOG_FORMAT` | Log format | `json` |
| `SERVER_HOST` | HTTP server bind address | `0.0.0.0` |
| `SERVER_PORT` | HTTP server port | `8080` |

## Memory System

Set via `MEMORY_PROVIDER`:

### Flat (default)

Simple dual-layer: long-term memory blob + event history (Grep-searchable)

### Letta (three-tier MemGPT)

| Layer | Storage | Description |
|-------|---------|-------------|
| Core Memory | SQLite | Structured blocks always in system prompt |
| Archival Memory | chromem-go vectors | Long-term semantic search |
| Recall Memory | FTS5 | Full-text event history search |

6 Letta tools: `core_memory_append`, `core_memory_replace`, `rethink`, `archival_memory_insert`, `archival_memory_search`, `recall_memory_search`

Auto-consolidation triggers at `AGENT_MEMORY_WINDOW` (default 50 messages).

## Skills

Skills use OpenClaw-style progressive loading:

```
.xbot/skills/
└── my-skill/
    ├── SKILL.md          # Required: name + description
    ├── scripts/          # Optional
    ├── references/      # Optional
    └── assets/          # Optional
```

Users can also install/publish shared skills via `/publish`, `/browse`, `/install`, `/uninstall`, and `/my` commands.

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

Use `ManageTools` tool at runtime. Supports lazy loading, inactivity timeout, and stdio/HTTP transport.

## SubAgent

Delegate tasks to sub-agents:

```
SubAgent(task="...", role="code-reviewer")
```

Predefined roles: `code-reviewer`, `explorer`, `tester`

Role definitions are stored in `.xbot/agents/`.

## Commands

| Command | Description |
|---------|-------------|
| `/new` | Archive memory and reset session |
| `/version` | Show version |
| `/help` | Show help |
| `/prompt <query>` | Preview full prompt (dry run without calling LLM) |
| `/set-llm` | Set custom LLM API (per-user) |
| `/unset-llm` | Clear custom LLM configuration |
| `/llm` | Show current LLM configuration |
| `/models` | List available models from current API |
| `/set-model <model>` | Set the model to use |
| `/compress` | Manually trigger context compression |
| `/context info` | Show token usage statistics |
| `/context mode` | View/switch compression mode |
| `/cancel` | Cancel the current processing request |
| `!<command>` | Quick execute command (skip LLM, run directly in sandbox) |
| `/publish` | Publish a skill to the shared marketplace |
| `/unpublish` | Remove a published skill |
| `/browse` | Browse available shared skills |
| `/install` | Install a shared skill |
| `/uninstall` | Uninstall a skill |
| `/my` | List your installed/published skills |
| `/settings` | User settings |
| `/menu` | Show interactive menu |

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

Note: Requires Docker installed on host for sandbox execution.

## License

MIT
