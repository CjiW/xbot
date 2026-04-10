---
title: "Configuration"
weight: 60
---


All configuration is done via environment variables or a `.env` file. See [`.env.example`](https://github.com/CjiW/xbot/blob/master/.env.example) for a complete template.

## LLM

| Variable | Default | Description |
|----------|---------|-------------|
| `LLM_PROVIDER` | `openai` | `openai` or `anthropic` |
| `LLM_BASE_URL` | `https://api.openai.com/v1` | API endpoint |
| `LLM_API_KEY` | — | API key |
| `LLM_MODEL` | `gpt-4o` | Model name |
| `LLM_RETRY_ATTEMPTS` | `5` | Retry count on failure |
| `LLM_RETRY_DELAY` | `1s` | Initial retry backoff |
| `LLM_RETRY_MAX_DELAY` | `30s` | Max retry backoff |
| `LLM_RETRY_TIMEOUT` | `120s` | Per-call timeout |

## Agent

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
| `MEMORY_PROVIDER` | `flat` | `flat` or `letta` |

## Embedding (Letta Mode)

| Variable | Default | Description |
|----------|---------|-------------|
| `LLM_EMBEDDING_PROVIDER` | `openai` | Embedding provider |
| `LLM_EMBEDDING_BASE_URL` | — | Embedding API endpoint |
| `LLM_EMBEDDING_API_KEY` | — | Embedding API key |
| `LLM_EMBEDDING_MODEL` | — | Embedding model name |
| `LLM_EMBEDDING_MAX_TOKENS` | — | Max embedding tokens |

## Sandbox

| Variable | Default | Description |
|----------|---------|-------------|
| `SANDBOX_MODE` | `none` | `none` / `docker` / `remote` |
| `SANDBOX_DOCKER_IMAGE` | `ubuntu:22.04` | Docker image for sandbox |
| `SANDBOX_IDLE_TIMEOUT_MINUTES` | `30` | Idle timeout (0 = disabled) |
| `SANDBOX_WS_PORT` | `8080` | Remote sandbox WebSocket port |
| `SANDBOX_AUTH_TOKEN` | — | Runner authentication token |
| `SANDBOX_PUBLIC_URL` | — | Public URL for runner connections |

## Channels

### Feishu

| Variable | Default | Description |
|----------|---------|-------------|
| `FEISHU_ENABLED` | `false` | Enable Feishu channel |
| `FEISHU_APP_ID` | — | Feishu App ID |
| `FEISHU_APP_SECRET` | — | Feishu App Secret |
| `FEISHU_ENCRYPT_KEY` | — | Event encryption key |
| `FEISHU_VERIFICATION_TOKEN` | — | Verification token |
| `FEISHU_ALLOW_FROM` | — | Allowed user open_id list |
| `FEISHU_DOMAIN` | — | Tenant domain |

### QQ

| Variable | Default | Description |
|----------|---------|-------------|
| `QQ_ENABLED` | `false` | Enable QQ channel |
| `QQ_APP_ID` | — | QQ App ID |
| `QQ_CLIENT_SECRET` | — | QQ Client Secret |
| `QQ_ALLOW_FROM` | — | Allowed openid list |

### NapCat

| Variable | Default | Description |
|----------|---------|-------------|
| `NAPCAT_ENABLED` | `false` | Enable NapCat channel |
| `NAPCAT_WS_URL` | — | WebSocket URL |
| `NAPCAT_TOKEN` | — | Auth token |
| `NAPCAT_ALLOW_FROM` | — | Allowed QQ numbers |

### Web

| Variable | Default | Description |
|----------|---------|-------------|
| `WEB_ENABLED` | `false` | Enable Web channel |
| `WEB_HOST` | `0.0.0.0` | Bind address |
| `WEB_PORT` | `8082` | Port |
| `WEB_STATIC_DIR` | — | Frontend static files |
| `WEB_UPLOAD_DIR` | — | File upload directory |
| `WEB_PERSONA_ISOLATION` | `true` | Per-user persona isolation |
| `WEB_INVITE_ONLY` | `false` | Invite-only mode |

## OAuth

| Variable | Default | Description |
|----------|---------|-------------|
| `OAUTH_ENABLE` | `false` | Enable OAuth server |
| `OAUTH_HOST` | `127.0.0.1` | OAuth bind address |
| `OAUTH_PORT` | `8081` | OAuth port |
| `OAUTH_BASE_URL` | — | OAuth callback base URL |

## Infrastructure

| Variable | Default | Description |
|----------|---------|-------------|
| `SERVER_HOST` | `0.0.0.0` | HTTP server bind address |
| `SERVER_PORT` | `8080` | HTTP server port |
| `WORK_DIR` | `.` | Working directory |
| `PROMPT_FILE` | `prompt.md` | Custom prompt template |
| `LOG_LEVEL` | `info` | Log level |
| `LOG_FORMAT` | `json` | Log format |
| `XBOT_ENCRYPTION_KEY` | — | AES-256-GCM key (base64, 32 bytes) |
| `TAVILY_API_KEY` | — | Tavily web search API key |
| `PPROF_ENABLE` | `false` | Enable pprof endpoint |
| `PPROF_HOST` | `localhost` | pprof bind address |
| `PPROF_PORT` | `6060` | pprof port |
