# xbot

> Go AI Agent framework with message bus + plugin architecture. Supports Feishu/QQ/CLI/Web channels, tool calling, pluggable memory, skills, subagents, MCP integration.

## Quick Reference

- Entry points: `cmd/xbot-cli/` (CLI), `cmd/runner/` (remote sandbox)
- Build: `go build ./...` | Test: `go test ./...` | Lint: `golangci-lint run ./...`
- Config: `~/.xbot/config.json`, env var overrides
- Pre-commit: gofmt → golangci-lint → go build → go test

## Knowledge Files

- `docs/agent/architecture.md` — package map, message flow, pipeline, key interfaces, concurrency
- `docs/agent/agent.md` — agent loop, middleware, SubAgent, context management, masking
- `docs/agent/llm.md` — LLM clients, streaming pitfalls, retry behavior
- `docs/agent/tools.md` — built-in tools, hooks, sandbox types
- `docs/agent/channel.md` — CLI, Feishu, Web, QQ adapters
- `docs/agent/memory.md` — letta vs flat providers
- `docs/agent/conventions.md` — error handling, logging, testing, naming, build
- `docs/agent/gotchas.md` — **MUST READ before any code change.** cross-cutting pitfalls, driver quirks, per-package traps

## Project Context

`ProjectContextMiddleware` auto-loads this file into system prompt. After code changes, update relevant Knowledge Files to keep documentation in sync.

### Mandatory: Read Gotchas Before Modifying Code

**Every code modification MUST start by reading `docs/agent/gotchas.md`.** This file contains hard-won lessons about driver serialization bugs, concurrency traps, and framework-specific pitfalls that are invisible from code alone. Skipping this step leads to wasted effort fixing already-solved problems.
