---
title: "xbot"
weight: 0
---

**xbot** is a Go framework for building AI agents. It provides a message bus + plugin architecture where an **Agent** (LLM + tools + memory) receives messages from any **Channel** (CLI, Feishu, QQ, Web) through a **Bus**, processes them in a multi-turn loop with tool calling, and sends replies back.

```
Channel → Bus → Agent → LLM ↔ Tools → Bus → Channel
```

Designed for self-hosted deployments. Supports **OpenAI** and **Anthropic** as native LLM providers, plus any OpenAI-compatible API via the `openai` provider with a custom `base_url`.

## Quick Start

```bash
git clone https://github.com/CjiW/xbot.git && cd xbot
make build && make run
```

See [Channels](/xbot/channels/) for channel-specific setup guides.
