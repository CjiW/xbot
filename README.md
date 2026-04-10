<p align="center">
  <strong>xbot</strong> — pluggable AI Agent framework
</p>

## What is xbot

xbot is a Go framework for building AI agents. It provides a message bus + plugin architecture where an **Agent** (LLM + tools + memory) receives messages from any **Channel** (CLI, Feishu, QQ, Web) through a **Bus**, processes them in a multi-turn loop with tool calling, and sends replies back.

```
Channel → Bus → Agent → LLM ↔ Tools → Bus → Channel
```

Designed for self-hosted deployments. Supports **OpenAI** and **Anthropic** as native LLM providers, plus any OpenAI-compatible API (DeepSeek, Qwen, Ollama, etc.) via the `openai` provider with a custom `base_url`.

## Quick Start

### Install CLI

```bash
# Linux / macOS (amd64, arm64) — installs xbot-cli only
curl -fsSL https://raw.githubusercontent.com/CjiW/xbot/master/scripts/install.sh | bash

# Specific version
VERSION=v0.0.7 curl -fsSL https://raw.githubusercontent.com/CjiW/xbot/master/scripts/install.sh | bash

# Custom install path (default: /usr/local/bin)
INSTALL_PATH=~/.local/bin curl -fsSL https://raw.githubusercontent.com/CjiW/xbot/master/scripts/install.sh | bash
```

### Build from Source

```bash
git clone https://github.com/CjiW/xbot.git && cd xbot
make build          # Builds xbot (server + runner)
make run            # Build and run server
```

To build `xbot-cli` only:

```bash
go build -o xbot-cli ./cmd/xbot-cli
```

### Configure

On first run, `xbot-cli` launches a setup wizard. Or edit `~/.xbot/config.json`:

**OpenAI (or any compatible API):**

```json
{
  "llm": {
    "provider": "openai",
    "api_key": "sk-xxx",
    "base_url": "https://api.openai.com/v1",
    "model": "gpt-4o"
  },
  "sandbox": { "mode": "none" },
  "agent": { "memory_provider": "flat" }
}
```

**Anthropic:**

```json
{
  "llm": {
    "provider": "anthropic",
    "api_key": "sk-ant-xxx",
    "model": "claude-sonnet-4-20250514"
  },
  "sandbox": { "mode": "none" },
  "agent": { "memory_provider": "flat" }
}
```

## Channels

Each channel is a pluggable adapter on the message bus. Enable channels via environment variables.

### CLI (TUI)

The default channel — a full-featured terminal UI built with [Bubble Tea](https://github.com/charmbracelet/bubbletea).

```bash
xbot-cli                # Interactive TUI
xbot-cli "your prompt"  # One-shot mode
echo "prompt" | xbot-cli # Pipe mode
```

**Keyboard shortcuts:**

| Key | Action |
|-----|--------|
| `Enter` | Send message |
| `Ctrl+Enter` / `Ctrl+J` | Insert newline |
| `Tab` | Autocomplete (`/` commands, `@` file paths) |
| `↑` `↓` | Input history / scroll messages |
| `PgUp` `PgDn` | Page up / down |
| `Home` `End` | Jump to top / bottom |
| `Esc` | Cancel / clear input |
| `Ctrl+C` | Interrupt current operation |
| `Ctrl+K` | Context editing (trim history by turns) |
| `Ctrl+O` | Toggle tool summary expand/collapse |
| `Ctrl+E` | Toggle long message folding |
| `^` | Background task panel |

**Slash commands:** `/settings` `/setup` `/update` `/new` `/clear` `/compact` `/context` `/model` `/models` `/cancel` `/search` `/tasks` `/su` `/help` `/exit`

**Features:** streaming output, markdown + Mermaid rendering, 6 color themes, background tasks, message search, built-in skill/agent creator.

See [docs/cli-channel.md](docs/cli-channel.md) for full documentation.

### Feishu (Lark)

WebSocket-based. Supports interactive message cards, doc/wiki/bitable read-write, file upload, and thread replies.

| Variable | Description |
|----------|-------------|
| `FEISHU_ENABLED` | Set `true` to enable |
| `FEISHU_APP_ID` | App ID |
| `FEISHU_APP_SECRET` | App Secret |
| `FEISHU_ENCRYPT_KEY` | Event encryption key |
| `FEISHU_VERIFICATION_TOKEN` | Verification token |
| `FEISHU_ALLOW_FROM` | Allowed `open_id` list (comma-separated) |

### QQ

Native QQ WebSocket channel.

| Variable | Description |
|----------|-------------|
| `QQ_ENABLED` | Set `true` to enable |
| `QQ_APP_ID` | App ID |
| `QQ_CLIENT_SECRET` | Client Secret |
| `QQ_ALLOW_FROM` | Allowed `openid` list (comma-separated) |

### NapCat (OneBot 11)

Compatible with [NapCat](https://github.com/NapNeko/NapCatQQ) and other OneBot 11 implementations.

| Variable | Description |
|----------|-------------|
| `NAPCAT_ENABLED` | Set `true` to enable |
| `NAPCAT_WS_URL` | WebSocket URL (no default) |
| `NAPCAT_TOKEN` | Auth token |
| `NAPCAT_ALLOW_FROM` | Allowed QQ numbers (comma-separated) |

### Web

Browser-based chat with optional login, invite-only mode, and persona isolation.

| Variable | Description |
|----------|-------------|
| `WEB_ENABLED` | Set `true` to enable |
| `WEB_HOST` | Bind address (default `0.0.0.0`) |
| `WEB_PORT` | Port (default `8082`) |
| `WEB_STATIC_DIR` | Frontend static files |
| `WEB_UPLOAD_DIR` | File upload directory |
| `WEB_PERSONA_ISOLATION` | Per-user persona isolation |
| `WEB_INVITE_ONLY` | Invite-only mode |

## Features

### Tools

Built-in tools the agent can call during a conversation:

- **Shell** — Execute commands in sandbox (Docker / remote / none)
- **File I/O** — Read, write, Glob, Grep with workspace isolation
- **Web** — Fetch pages, Tavily web search
- **Context** — Edit conversation context mid-session
- **SubAgent** — Delegate tasks to specialized sub-agents
- **Cron** — Schedule tasks (cron expressions, one-shot `at`)
- **Download** — Download files from URLs
- **Feishu MCP** — Feishu API tools (doc, wiki, bitable, drive)
- **Runner** — Manage sandbox runner connections

### Memory

Two pluggable providers:

| | Flat (default) | Letta (MemGPT) |
|--|----------------|----------------|
| Core | In-memory blocks | SQLite (always in prompt) |
| Archival | Grep-searchable blob | Vector search (chromem-go) |
| Recall | Event history | FTS5 full-text search |
| Dependencies | None | Embedding model required |

Set via `MEMORY_PROVIDER=flat` or `MEMORY_PROVIDER=letta`. Letta also requires embedding config (`LLM_EMBEDDING_PROVIDER`, `LLM_EMBEDDING_MODEL`, etc.).

### Skills & Agents

- **Skills** — Markdown-defined capability packages loaded from `~/.xbot/skills/`. Two built-in: `skill-creator`, `agent-creator`.
- **SubAgents** — Delegate tasks to role-based sub-agents (e.g. `explore`, `code-reviewer`). Custom roles in `~/.xbot/agents/`. Max nesting depth: 6 (`MAX_SUBAGENT_DEPTH`).

### MCP Protocol

- **Global**: `.xbot/mcp.json` for always-on servers
- **Session**: Dynamic loading at runtime via `ManageTools` tool
- Supports stdio and HTTP transports, inactivity timeout, lazy cleanup

### Other

- **Multi-tenant** — Channel + chatID isolation
- **Hot-reload prompts** — Go templates with channel-specific overrides
- **KV-Cache optimized** — Context ordering maximizes LLM cache hits
- **OAuth 2.0** — Built-in OAuth server for web channel authentication

## Architecture

```
┌──────────┐     ┌──────────────┐     ┌────────┐     ┌──────────┐
│  Feishu  │────▶│  Dispatcher  │────▶│ Agent  │────▶│   LLM    │
│  QQ      │◀────│  (channel/)  │◀────│ (agent/)│◀────│ (llm/)   │
│  NapCat  │     └──────────────┘     │        │     └──────────┘
│  Web     │                          │        │
│  CLI     │                          │        │────▶ Tools
└──────────┘                          │        │      (tools/)
                                      │        │
                                      │        │────▶ Memory
                                      │        │      (memory/)
                                      └────────┘
```

| Package | Role |
|---------|------|
| `bus/` | Inbound/outbound message channels |
| `channel/` | Channel adapters and message dispatcher |
| `agent/` | Agent loop (LLM → tools → response) |
| `llm/` | LLM clients (OpenAI, Anthropic) |
| `tools/` | Tool registry and implementations |
| `memory/` | Memory providers (flat / letta) |
| `config/` | Environment-based configuration |
| `storage/` | SQLite persistence (sessions, memory, tenants) |
| `session/` | Multi-tenant session management |
| `cron/` | Scheduled task execution |
| `oauth/` | OAuth 2.0 framework |
| `crypto/` | AES-256-GCM encryption for API keys |
| `logger/` | Structured logging with rotation |
| `web/` | React 19 + Vite + TailwindCSS 4 frontend |
| `agents/` | Embedded agent role definitions |
| `cmd/` | Entrypoints (`xbot-cli`, sandbox runner) |
| `prompt/` | Default system prompt template |

## Configuration

> ⚠️ **Deprecated**: 环境变量配置将在未来版本中移除。请迁移到 `config.json`。参见 [docs-site Configuration](https://cjiw.github.io/xbot/configuration/) 获取最新的配置方式。

All config via environment variables or `.env` file. See [`.env.example`](.env.example) for a complete template.

### LLM

| Variable | Default | Description |
|----------|---------|-------------|
| `LLM_PROVIDER` | `openai` | `openai` or `anthropic` |
| `LLM_BASE_URL` | `https://api.openai.com/v1` | API endpoint (openai default; optional override for anthropic) |
| `LLM_API_KEY` | — | API key |
| `LLM_MODEL` | `gpt-4o` | Model name |
| `LLM_RETRY_ATTEMPTS` | `5` | Retry count on failure |
| `LLM_RETRY_DELAY` | `1s` | Initial retry backoff |
| `LLM_RETRY_MAX_DELAY` | `30s` | Max retry backoff |
| `LLM_RETRY_TIMEOUT` | `120s` | Per-call timeout |

### Agent

| Variable | Default | Description |
|----------|---------|-------------|
| `AGENT_MAX_ITERATIONS` | `2000` | Max tool-call iterations per turn |
| `AGENT_MAX_CONCURRENCY` | `3` | Max concurrent LLM calls |
| `AGENT_MAX_CONTEXT_TOKENS` | `200000` | Max context window tokens |
| `AGENT_ENABLE_AUTO_COMPRESS` | `true` | Auto context compression |
| `AGENT_COMPRESSION_THRESHOLD` | `0.7` | Token ratio to trigger compression |
| `AGENT_CONTEXT_MODE` | — | Custom context management mode |
| `AGENT_PURGE_OLD_MESSAGES` | `false` | Purge old messages after compression |
| `MAX_SUBAGENT_DEPTH` | `6` | SubAgent max nesting depth |

### Sandbox

| Variable | Default | Description |
|----------|---------|-------------|
| `SANDBOX_MODE` | `none` | `none` / `docker` / `remote` |
| `SANDBOX_DOCKER_IMAGE` | `ubuntu:22.04` | Docker image for sandbox |
| `SANDBOX_IDLE_TIMEOUT_MINUTES` | `30` | Idle timeout (0 = disabled) |
| `SANDBOX_WS_PORT` | `8080` | Remote sandbox WebSocket port |
| `SANDBOX_AUTH_TOKEN` | — | Runner authentication token |
| `SANDBOX_PUBLIC_URL` | — | Public URL for runner connections |

### Infrastructure

| Variable | Default | Description |
|----------|---------|-------------|
| `WORK_DIR` | `.` | Working directory |
| `PROMPT_FILE` | `prompt.md` | Custom prompt template |
| `XBOT_ENCRYPTION_KEY` | — | AES-256-GCM key (base64, 32 bytes) |
| `TAVILY_API_KEY` | — | Tavily web search API key |
| `OAUTH_ENABLE` | `false` | Enable OAuth server |
| `OAUTH_HOST` | `127.0.0.1` | OAuth bind address |
| `OAUTH_PORT` | `8081` | OAuth port |
| `OAUTH_BASE_URL` | — | OAuth callback base URL |
| `SERVER_HOST` | `0.0.0.0` | HTTP server bind address |
| `SERVER_PORT` | `8080` | HTTP server port |
| `LOG_LEVEL` | `info` | Log level |
| `LOG_FORMAT` | `json` | Log format |
| `PPROF_ENABLE` | `false` | Enable pprof endpoint |

## Deployment

### Docker

> ⚠️ **Deprecated**: 以下 Docker 部署示例使用环境变量配置，将在未来版本中改为挂载 `config.json`。

```bash
docker run -d --name xbot --restart unless-stopped \
  --security-opt seccomp=unconfined --cap-add SYS_ADMIN \
  -v /opt/xbot/.xbot:/data/.xbot \
  -e WORK_DIR=/data \
  -e LLM_PROVIDER=openai \
  -e LLM_BASE_URL=https://api.openai.com/v1 \
  -e LLM_API_KEY=your_key \
  -e LLM_MODEL=gpt-4o-mini \
  xbot:latest
```

### Makefile

```bash
make dev    # Development mode
make build  # Build binary
make run    # Build and run
make test   # Test with race detection
make fmt    # Format code
make lint   # golangci-lint
make ci     # lint → build → test
make clean  # Remove build artifacts
```

## Documentation

Full documentation is available at [cjiw.github.io/xbot](https://cjiw.github.io/xbot/).

- [Architecture](https://cjiw.github.io/xbot/architecture/) — System design and data flow
- [Channels](https://cjiw.github.io/xbot/channels/) — Channel setup guides (CLI, Feishu, Web, QQ/NapCat)
- [Guides](https://cjiw.github.io/xbot/guides/) — Sandbox, Permission Control, Memory, MCP, Skills & Agents
- [Tools](https://cjiw.github.io/xbot/tools/) — Built-in tools reference
- [Configuration](https://cjiw.github.io/xbot/configuration/) — Environment variables and config reference
- [Design](https://cjiw.github.io/xbot/design/) — Design documents
- [CHANGELOG](CHANGELOG.md) — Release history

## License

MIT
