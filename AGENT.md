# xbot

> Go AI Agent framework with message bus + plugin architecture. Supports Feishu/QQ/CLI/Web channels, tool calling, pluggable memory, skills, subagents, MCP integration.

## Quick Reference

- Entry points: `cmd/xbot-cli/` (CLI), `cmd/runner/` (remote sandbox)
- Build: `go build ./...` | Test: `go test ./...` | Lint: `golangci-lint run ./...`
- Config: `~/.xbot/config.json`, env var overrides
- Pre-commit: gofmt → golangci-lint → go build → go test

## Knowledge Files

- `docs/agent/architecture.md` — package map, message flow, middleware pipeline, tool execution, agent hierarchy, key interfaces, concurrency model
- `docs/agent/conventions.md` — error handling, logging, testing, interfaces, concurrency, naming, build
- `docs/agent/gotchas.md` — tool schema pitfalls, LLM streaming bugs, SubAgent deadlocks, concurrency traps, SQLite patterns, Hugo docs site

## Project Context

`ProjectContextMiddleware` auto-loads this file into system prompt. `knowledge-management` skill guides maintenance. System-reminder nudges after file modifications.
