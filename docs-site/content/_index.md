---
title: "xbot"
weight: 0
---

**xbot** is a Go framework for building AI agents. It provides a message bus + plugin architecture where an **Agent** (LLM + tools + memory) receives messages from any **Channel** (CLI, Feishu, QQ, Web) through a **Bus**, processes them in a multi-turn loop with tool calling, and sends replies back. Designed for self-hosted deployments, it supports **OpenAI** and **Anthropic** as native LLM providers, plus any OpenAI-compatible API (DeepSeek, Qwen, Ollama, etc.) via the `openai` provider with a custom `base_url`.

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

## Install

### curl (Linux / macOS)

```bash
# Default: installs xbot-cli to /usr/local/bin
curl -fsSL https://raw.githubusercontent.com/CjiW/xbot/master/scripts/install.sh | bash

# Specific version
VERSION=v0.0.7 curl -fsSL https://raw.githubusercontent.com/CjiW/xbot/master/scripts/install.sh | bash

# Custom install path
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

## Features

- **Multi-channel** — Pluggable channel adapters: CLI (TUI), Feishu (Lark), QQ, NapCat (OneBot 11), Web
- **Tools** — 50+ built-in tools: Shell, File I/O, Web fetch/search, Context editing, SubAgent, Cron scheduling, Download, Feishu MCP, and more
- **Memory** — Pluggable providers: **Flat** (in-memory blocks + grep archival) and **Letta/MemGPT** (SQLite core + vector search + FTS5)
- **Skills & Agents** — Markdown-defined skill packages; role-based SubAgents with custom roles, max nesting depth 6
- **MCP Protocol** — Global and session-scoped MCP servers, stdio and HTTP transports, lazy cleanup
- **Permission Control** — OS user-based permission control with approval workflows for privileged operations
- **Multi-tenant** — Channel + chatID isolation
- **OAuth 2.0** — Built-in OAuth server for web channel authentication
- **Hot-reload prompts** — Go templates with channel-specific overrides
- **KV-Cache optimized** — Context ordering maximizes LLM cache hits

## Documentation

| Section | Description |
|---------|-------------|
| [Architecture](/architecture/) | System design and data flow |
| [Channels](/channels/) | Channel setup guides |
| [Guides](/guides/) | Sandbox, Permission Control, Memory, MCP, Skills & Agents |
| [Tools](/tools/) | Built-in tools reference |
| [Configuration](/configuration/) | Environment variables and config reference |
| [Design](/design/) | Design documents |

## Channels

Each channel is a pluggable adapter on the message bus. See the [Channels](/xbot/channels/) page for setup guides and configuration details.
