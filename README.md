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
- **llm/** — LLM clients (OpenAI-compatible, CodeBuddy)
- **tools/** — Tool registry and implementations
- **memory/** — Memory providers (flat/letta)
- **session/** — Multi-tenant session management
- **storage/** — SQLite persistence (sessions, memory, tenants)
- **oauth/** — OAuth 2.0 framework

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
make test     # Run tests with race detection
make fmt      # Format code
make lint     # Run golangci-lint
make ci       # lint → build → test
make clean-db # Clear .xbot data
```

## Configuration

All config via environment variables or `.env`:

| Variable | Description | Default |
|----------|-------------|---------|
| `LLM_PROVIDER` | LLM provider (`openai`/`codebuddy`) | `openai` |
| `LLM_BASE_URL` | API URL | — |
| `LLM_API_KEY` | API key | — |
| `LLM_MODEL` | Model name | `deepseek-chat` |
| `MEMORY_PROVIDER` | Memory (`flat`/`letta`) | `flat` |
| `LLM_EMBEDDING_*` | Embedding API for Letta | — |
| `FEISHU_ENABLED` | Enable Feishu | `false` |
| `FEISHU_APP_ID` | Feishu app ID | — |
| `FEISHU_APP_SECRET` | Feishu app secret | — |
| `QQ_ENABLED` | Enable QQ | `false` |
| `WORK_DIR` | Working directory | `.` |
| `PROMPT_FILE` | Custom prompt template | — |
| `OAUTH_ENABLE` | Enable OAuth | `false` |

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
.claude/skills/
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

Use `ManageTools` tool at runtime. Supports lazy loading, inactivity timeout, and stdio/HTTP transport.

## SubAgent

Delegate tasks to sub-agents:

```
SubAgent(task="...", role="code-reviewer")
```

Predefined roles: `code-reviewer`

## Commands

| Command | Description |
|---------|-------------|
| `/new` | Archive memory and reset session |
| `/version` | Show version |
| `/help` | Show help |
| `/prompt` | Show current prompt (dry run without calling LLM) |

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
