---
title: "Configuration"
weight: 60
---

# Configuration Reference

All configuration is done via `config.json`. See [`config.example.json`](https://github.com/CjiW/xbot/blob/master/config.example.json) for a complete template.

## LLM

| Field | JSON Path | Default | Description |
|-------|-----------|---------|-------------|
| Provider | `llm.provider` | `openai` | `openai` or `anthropic` |
| Base URL | `llm.base_url` | `https://api.openai.com/v1` | API endpoint |
| API Key | `llm.api_key` | — | API key |
| Model | `llm.model` | `gpt-4o` | Model name |
| Max Output Tokens | `llm.max_output_tokens` | `0` | Max tokens in LLM response (0 = model default) |
| Retry Attempts | `agent.llm_retry_attempts` | `5` | Retry count on failure |
| Retry Delay | `agent.llm_retry_delay` | `1s` | Initial retry backoff |
| Retry Max Delay | `agent.llm_retry_max_delay` | `30s` | Max retry backoff |
| Retry Timeout | `agent.llm_retry_timeout` | `120s` | Per-call timeout |

## Agent

| Field | JSON Path | Default | Description |
|-------|-----------|---------|-------------|
| Max Iterations | `agent.max_iterations` | `2000` | Max tool-call iterations per turn |
| Max Concurrency | `agent.max_concurrency` | `3` | Max concurrent LLM calls |
| Max Context Tokens | `agent.max_context_tokens` | `200000` | Max context window tokens |
| Auto Compress | `agent.enable_auto_compress` | `true` | Auto context compression |
| Compression Threshold | `agent.compression_threshold` | `0.7` | Token ratio to trigger compression |
| Context Mode | `agent.context_mode` | — | Custom context management mode |
| Purge Old Messages | `agent.purge_old_messages` | `false` | Purge old messages after compression |
| SubAgent Depth | `agent.max_sub_agent_depth` | `6` | SubAgent max nesting depth |
| Memory Provider | `agent.memory_provider` | `flat` | `flat` or `letta` |

## Embedding (Letta Mode)

| Field | JSON Path | Default | Description |
|-------|-----------|---------|-------------|
| Provider | `embedding.provider` | `openai` | Embedding provider |
| Base URL | `embedding.base_url` | — | Embedding API endpoint |
| API Key | `embedding.api_key` | — | Embedding API key |
| Model | `embedding.model` | `text-embedding-3-small` | Embedding model name |
| Max Tokens | `embedding.max_tokens` | `2048` | Max embedding tokens |

## Sandbox

| Field | JSON Path | Default | Description |
|-------|-----------|---------|-------------|
| Mode | `sandbox.mode` | `none` | `none` / `docker` / `remote` |
| Docker Image | `sandbox.docker_image` | `ubuntu:22.04` | Docker image for sandbox |
| Idle Timeout | `sandbox.idle_timeout` | `30m` | Idle timeout (0 = disabled) |
| WS Port | `sandbox.ws_port` | `8080` | Remote sandbox WebSocket port |
| Auth Token | `sandbox.auth_token` | — | Runner authentication token |
| Public URL | `sandbox.public_url` | — | Public URL for runner connections |

## Channels

### Feishu

| Field | JSON Path | Default | Description |
|-------|-----------|---------|-------------|
| Enabled | `feishu.enabled` | `false` | Enable Feishu channel |
| App ID | `feishu.app_id` | — | Feishu App ID |
| App Secret | `feishu.app_secret` | — | Feishu App Secret |
| Encrypt Key | `feishu.encrypt_key` | — | Event encryption key |
| Verification Token | `feishu.verification_token` | — | Verification token |
| Allow From | `feishu.allow_from` | — | Allowed user open_id list |
| Domain | `feishu.domain` | — | Tenant domain |

### QQ

| Field | JSON Path | Default | Description |
|-------|-----------|---------|-------------|
| Enabled | `qq.enabled` | `false` | Enable QQ channel |
| App ID | `qq.app_id` | — | QQ App ID |
| Client Secret | `qq.client_secret` | — | QQ Client Secret |
| Allow From | `qq.allow_from` | — | Allowed openid list |

### NapCat

| Field | JSON Path | Default | Description |
|-------|-----------|---------|-------------|
| Enabled | `napcat.enabled` | `false` | Enable NapCat channel |
| WS URL | `napcat.ws_url` | — | WebSocket URL |
| Token | `napcat.token` | — | Auth token |
| Allow From | `napcat.allow_from` | — | Allowed QQ numbers |

### Web

| Field | JSON Path | Default | Description |
|-------|-----------|---------|-------------|
| Enabled | `web.enable` | `false` | Enable Web channel |
| Host | `web.host` | `0.0.0.0` | Bind address |
| Port | `web.port` | `8082` | Port |
| Static Dir | `web.static_dir` | — | Frontend static files |
| Upload Dir | `web.upload_dir` | — | File upload directory |
| Persona Isolation | `web.persona_isolation` | `true` | Per-user persona isolation |
| Invite Only | `web.invite_only` | `false` | Invite-only mode |

## OAuth

| Field | JSON Path | Default | Description |
|-------|-----------|---------|-------------|
| Enable | `oauth.enable` | `false` | Enable OAuth server |
| Host | `oauth.host` | `127.0.0.1` | OAuth bind address |
| Port | `oauth.port` | `8081` | OAuth port |
| Base URL | `oauth.base_url` | — | OAuth callback base URL |

## Infrastructure

| Field | JSON Path | Default | Description |
|-------|-----------|---------|-------------|
| Server Host | `server.host` | `0.0.0.0` | HTTP server bind address |
| Server Port | `server.port` | `8080` | HTTP server port |
| Work Dir | `agent.work_dir` | `.` | Working directory |
| Prompt File | `agent.prompt_file` | `prompt.md` | Custom prompt template |
| Log Level | `log.level` | `info` | Log level |
| Log Format | `log.format` | `json` | Log format |
| Encryption Key | `XBOT_ENCRYPTION_KEY` env | — | AES-256-GCM key (base64, 32 bytes) |
| Tavily API Key | `tavily_api_key` | — | Tavily web search API key |
| Pprof Enable | `pprof.enable` | `false` | Enable pprof endpoint |
| Pprof Host | `pprof.host` | `localhost` | pprof bind address |
| Pprof Port | `pprof.port` | `6060` | pprof port |
