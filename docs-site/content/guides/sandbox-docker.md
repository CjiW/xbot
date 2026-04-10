---
title: "Sandbox Docker"
weight: 10
---

# Sandbox Guide

## Overview

xbot supports three sandbox modes, managed by a `SandboxRouter` that selects the appropriate backend on a **per-user** basis:

| Mode | Backend | Description |
|------|---------|-------------|
| `none` | `NoneSandbox` | No isolation — commands execute directly on the host |
| `docker` | `DockerSandbox` | Each user gets an isolated Docker container with persistent filesystem |
| `remote` | `RemoteSandbox` | Commands execute on the user's own machine via WebSocket-connected `xbot-runner` |

All three modes can coexist simultaneously. The router decides which backend to use for each user according to the following priority:

1. If the user has set `active_runner` to the built-in Docker name (`__docker__`) → Docker
2. If the user has a connected remote runner matching their `active_runner` name → Remote
3. If the user has any connected remote runner → Remote
4. Fallback → Docker (if enabled), then None

> **Note:** Web users (`web-*` IDs) are blocked from all sandbox access unless `WEB_USER_SERVER_RUNNER=true` is set. They must connect their own remote runner.
>
> ⚠️ **Deprecated**: `WEB_USER_SERVER_RUNNER` 环境变量将在未来版本中移除。请迁移到 `config.json` 中的 `sandbox.allow_web_user_server_runner` 字段。参见 [Configuration](/configuration/) 获取最新的配置方式。

---

## Docker Mode

Docker mode provides per-user container isolation on the server. Each user gets their own container with a persistent filesystem.

### Prerequisites

```bash
# Install Docker
sudo apt-get update && sudo apt-get install -y docker.io
sudo systemctl start docker && sudo systemctl enable docker
sudo usermod -aG docker $USER  # re-login required
```

### Configuration

> ⚠️ **Deprecated**: 环境变量配置将在未来版本中移除。请迁移到 `config.json`。参见 [Configuration](/configuration/) 获取最新的配置方式。

```bash
# .env file

# Sandbox mode: none / docker
SANDBOX_MODE=docker

# Docker image (optional, default: ubuntu:22.04)
SANDBOX_DOCKER_IMAGE=ubuntu:22.04

# Host working directory (optional)
HOST_WORK_DIR=/tmp/xbot-work

# Container idle timeout before commit (optional, in minutes)
SANDBOX_IDLE_TIMEOUT_MINUTES=30
```

### Container Lifecycle

Each user's container follows this lifecycle:

```
┌─────────────────────────────────────────────────────────────┐
│  1. First use                                                │
│     Base image (e.g. ubuntu:22.04)                           │
│              │                                                │
│              ▼                                                │
│     Create container xbot-{user_id}                           │
│              │                                                │
│              ▼                                                │
│     User operations (apt install, pip install, etc.)         │
│                                                              │
│  2. On close / idle timeout                                  │
│     docker export | docker import → xbot-{user_id}:latest    │
│     stop + rm container                                       │
│                                                              │
│  3. Next use                                                  │
│     Detect user image xbot-{user_id}:latest                   │
│              │                                                │
│              ▼                                                │
│     Create new container from user image — env fully restored│
└─────────────────────────────────────────────────────────────┘
```

### Persistence via Export/Import

The server uses `docker export | docker import` (piped, no intermediate tar file) to persist the container's filesystem as a user-specific image:

- **All filesystem changes are saved**: system packages (`apt-get install`), language packages (`pip install`, `npm install -g`), compiled software, config file edits
- **Transparent restore**: the next container is created from the committed image automatically
- **Startup cleanup**: stale export temp files and dangling Docker images are pruned on server start

### Manual Cleanup

```bash
# Remove a specific user's container and image
docker rm -f xbot-{user_id}
docker rmi xbot-{user_id}:latest

# List all xbot containers
docker ps -a --filter "name=xbot-"

# Bulk cleanup
docker rm -f $(docker ps -aq --filter "name=xbot-")
docker rmi $(docker images --format '{{.Repository}}:{{.Tag}}' | grep '^xbot-')
```

### Troubleshooting

```bash
# Check Docker service
sudo systemctl status docker

# Check permissions
docker ps

# View container logs
docker logs xbot-{user_id}

# List images
docker images
```

---

## Remote Runner Mode

Remote runner mode lets users connect their own machine as an execution environment via WebSocket. The server manages runners and routes commands to them.

### Architecture

```
┌──────────────┐         WebSocket          ┌──────────────────┐
│  xbot server │◄──────────────────────────►│  xbot-runner     │
│              │   /ws/{user_id}            │  (user's machine)│
│  RemoteSandbox                            │                  │
│  (router)    │  commands, file ops, LLM   │  native / docker │
└──────────────┘                            └──────────────────┘
```

The `xbot-runner` binary connects to the server's WebSocket endpoint and executes commands locally or inside a local Docker container.

### Runner Modes

Each `xbot-runner` instance can operate in one of two modes:

| Runner Mode | Flag | Description |
|-------------|------|-------------|
| `native` | `--mode native` (default) | Commands execute directly on the runner's host |
| `docker` | `--mode docker --docker-image <image>` | Commands execute inside a local Docker container on the runner's machine |

### Connecting a Runner

1. **Create a runner** via the runner management API (see below). This generates a token and a connect command.
2. **Run the connect command** on the user's machine:
   ```bash
   ./xbot-runner --server <server-url>/ws/<user-id> --token <token>
   ```
3. The runner auto-detects its user ID from the server URL path, or set it explicitly with `--user-id`.

### Runner CLI Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--server` | *(required)* | WebSocket server URL |
| `--token` | *(required)* | Authentication token |
| `--user-id` | *(from URL)* | User ID |
| `--workspace` | `/workspace` | Workspace root directory |
| `--mode` | `native` | Runner mode: `native` or `docker` |
| `--docker-image` | `ubuntu:22.04` | Docker image (docker mode) |
| `--full-control` | `false` | Disable path restrictions |
| `--v` | `false` | Verbose logging |
| `--llm-provider` | | LLM provider (`openai`, `anthropic`, etc.) |
| `--llm-api-key` | | LLM API key |
| `--llm-model` | | LLM model name |
| `--llm-base-url` | | Custom LLM API base URL |

### Multi-Runner Support

Each user can register **multiple runners** with different names. The server tracks which runner is active per user via `user_settings.active_runner`. The `SandboxRouter` routes commands to the user's active runner.

When a runner connects, the server automatically **syncs** global skills and agent definitions from the server to the runner's workspace, ensuring the runner has the same tools available.

### Runner Management API

The server exposes these management functions (used by the web UI and agent tools):

| Operation | Description |
|-----------|-------------|
| `RunnerList` | List all runners for a user, including online status and the built-in Docker runner |
| `RunnerCreate` | Create a new named runner, returns token and connect command |
| `RunnerDelete` | Delete a runner and disconnect it if online |
| `RunnerGetActive` | Get the currently active runner name |
| `RunnerSetActive` | Set the active runner for routing |

When creating a runner, the returned command includes all necessary flags (mode, workspace, LLM config if specified).

### Reconnection

The runner uses exponential backoff (`1s` → `60s` max) with infinite retries on connection failure.

---

## ProxyLLM

Runners can optionally serve as **LLM proxies** — when a runner has local LLM configuration, the server transparently forwards LLM API calls through the runner instead of calling the LLM provider directly.

### How It Works

1. A runner is created with LLM settings (`--llm-provider`, `--llm-model`, `--llm-api-key`, `--llm-base-url`), or LLM settings are configured later via the web UI.
2. When the runner connects, the server detects its LLM capability and stores the configuration.
3. When the agent processes a request for that user, `injectProxyLLM` checks if the user's active runner has LLM configured.
4. If so, a `ProxyLLM` wrapper is injected into the agent's LLM factory:
   - **`GenerateFunc`**: LLM generate requests are forwarded to the runner via WebSocket (`LLMGenerate`)
   - **`ListModelsFunc`**: Model listing is forwarded to the runner (`LLMModels`)
5. If the runner disconnects or has no LLM config, the proxy is cleared and the server's own LLM is used.

### Use Cases

- Users who want to use their own API keys without sharing them with the server
- Runners behind firewalls that can access private LLM endpoints
- Per-user LLM provider/model customization

---

## Configuration Reference

### Sandbox Environment Variables

> ⚠️ **Deprecated**: 以下环境变量将在未来版本中移除。请迁移到 `config.json`。参见 [Configuration](/configuration/) 获取最新的配置方式。

| Variable | Default | Description |
|----------|---------|-------------|
| `SANDBOX_MODE` | `none` | Primary sandbox mode: `none`, `docker`, or `remote` |
| `SANDBOX_REMOTE_MODE` | | Set to `remote` to enable remote runner WebSocket server alongside Docker |
| `SANDBOX_DOCKER_IMAGE` | `ubuntu:22.04` | Docker image for container creation |
| `HOST_WORK_DIR` | | Host directory for sandbox working files |
| `SANDBOX_IDLE_TIMEOUT_MINUTES` | | Container idle timeout before auto-commit (in minutes) |
| `SANDBOX_WS_PORT` | `8080` | WebSocket listen port for remote runner connections |
| `SANDBOX_AUTH_TOKEN` | | Global auth token for runner authentication (when not using per-user tokens) |
| `SANDBOX_PUBLIC_URL` | | Public URL of the server, used to construct runner connect commands |
| `WEB_USER_SERVER_RUNNER` | `false` | Allow web users to use server-side Docker sandbox (default: denied) — 迁移至 `config.json` 的 `sandbox.allow_web_user_server_runner` |

### Mode Combinations

| `SANDBOX_MODE` | `SANDBOX_REMOTE_MODE` | Docker | Remote | Effect |
|----------------|----------------------|--------|--------|--------|
| `docker` | *(unset)* | ✅ | ❌ | Docker only |
| `none` | `remote` | ❌ | ✅ | Remote only |
| `docker` | `remote` | ✅ | ✅ | Both (router selects per user) |
| `none` | *(unset)* | ❌ | ❌ | No sandbox (NoneSandbox) |

### Docker-Specific Notes

- **Cold start**: First execution requires pulling the image and creating a container (~10–30 seconds)
- **Hot execution**: Once running, command execution has negligible overhead
- **Export/import**: Typically completes in 1–2 seconds (only filesystem diff layers)
- **Resource usage**: Each active user occupies one container; user images consume disk space (incremental storage)
- **Stale cleanup**: On server start, temp export files older than 10 minutes and dangling Docker images are automatically pruned
