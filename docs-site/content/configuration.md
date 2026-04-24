---
title: "配置参考"
weight: 15
---

# 配置参考

所有配置通过 `~/.xbot/config.json` 文件管理。**不推荐使用环境变量**，请直接编辑配置文件。

## 配置文件位置

- **默认位置**：`~/.xbot/config.json`
- 可通过环境变量 `XBOT_HOME` 覆盖（如 `XBOT_HOME=/opt/xbot`）
- Server 模式下通过 `xbot-cli serve --config /path/to/config.json` 指定

## 最小配置示例

### Standalone 模式（个人使用）

```json
{
  "llm": {
    "provider": "openai",
    "api_key": "sk-xxx",
    "model": "gpt-4o"
  },
  "sandbox": {
    "mode": "none"
  }
}
```

### Server 模式 + 飞书（团队使用）

```json
{
  "llm": {
    "provider": "openai",
    "api_key": "sk-xxx",
    "model": "gpt-4o"
  },
  "feishu": {
    "enabled": true,
    "app_id": "cli_xxx",
    "app_secret": "xxx"
  },
  "web": {
    "enable": true
  }
}
```

## 完整配置参考

### LLM 配置

```json
{
  "llm": {
    "provider": "openai",
    "api_key": "",
    "base_url": "https://api.openai.com/v1",
    "model": "gpt-4o",
    "vanguard_model": "",
    "balance_model": "",
    "swift_model": "",
    "max_output_tokens": 0,
    "thinking_mode": ""
  }
}
```

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `provider` | string | `"openai"` | LLM 提供商：`openai` 或 `anthropic` |
| `api_key` | string | `""` | API Key |
| `base_url` | string | `"https://api.openai.com/v1"` | API 地址（兼容服务时修改） |
| `model` | string | `"gpt-4o"` | 默认模型 |
| `vanguard_model` | string | `""` | SubAgent vanguard 级别模型 |
| `balance_model` | string | `""` | SubAgent balance 级别模型 |
| `swift_model` | string | `""` | SubAgent swift 级别模型 |
| `max_output_tokens` | int | `0`（=8192） | 最大输出 token 数 |
| `thinking_mode` | string | `""`（=auto） | 思考模式：`auto` / `enabled` / `disabled` |

**使用兼容 API 示例：**

```json
{
  "llm": {
    "provider": "openai",
    "api_key": "your-key",
    "base_url": "https://api.deepseek.com/v1",
    "model": "deepseek-chat"
  }
}
```

### Agent 配置

```json
{
  "agent": {
    "max_iterations": 2000,
    "max_concurrency": 3,
    "memory_provider": "flat",
    "work_dir": ".",
    "prompt_file": "prompt.md",
    "max_context_tokens": 200000,
    "enable_auto_compress": true,
    "compression_threshold": 0.7,
    "context_mode": "",
    "purge_old_messages": false,
    "max_sub_agent_depth": 6,
    "llm_retry_attempts": 5,
    "llm_retry_delay": "1s",
    "llm_retry_max_delay": "30s",
    "llm_retry_timeout": "120s",
    "mcp_inactivity_timeout": "30m",
    "mcp_cleanup_interval": "5m",
    "session_cache_timeout": "24h"
  }
}
```

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `max_iterations` | int | `2000` | 单次对话最大工具调用次数 |
| `max_concurrency` | int | `3` | 最大并发 LLM 调用数 |
| `memory_provider` | string | `"flat"` | 记忆系统：`flat` 或 `letta` |
| `work_dir` | string | `"."` | 工作目录 |
| `prompt_file` | string | `"prompt.md"` | 自定义系统 prompt 文件 |
| `max_context_tokens` | int | `200000` | 最大上下文窗口 token |
| `enable_auto_compress` | bool | `true` | 自动压缩上下文 |
| `compression_threshold` | float | `0.7` | 触发压缩的 token 比例 |
| `context_mode` | string | `""` | 上下文管理模式 |
| `purge_old_messages` | bool | `false` | 压缩后清除旧消息 |
| `max_sub_agent_depth` | int | `6` | SubAgent 最大嵌套深度 |
| `llm_retry_attempts` | int | `5` | LLM 调用失败重试次数 |
| `llm_retry_delay` | duration | `"1s"` | 重试初始延迟 |
| `llm_retry_max_delay` | duration | `"30s"` | 重试最大延迟 |
| `llm_retry_timeout` | duration | `"120s"` | 单次 LLM 调用超时 |
| `mcp_inactivity_timeout` | duration | `"30m"` | MCP Server 非活动超时 |
| `mcp_cleanup_interval` | duration | `"5m"` | MCP 清理间隔 |
| `session_cache_timeout` | duration | `"24h"` | Session 缓存超时 |

### 沙箱配置

```json
{
  "sandbox": {
    "mode": "docker",
    "docker_image": "ubuntu:22.04",
    "host_work_dir": "",
    "idle_timeout": "30m",
    "ws_port": 8080,
    "auth_token": "",
    "public_url": ""
  }
}
```

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `mode` | string | `"docker"` | 沙箱模式：`none` / `docker` |
| `docker_image` | string | `"ubuntu:22.04"` | Docker 镜像 |
| `host_work_dir` | string | `""` | 宿主机工作目录 |
| `idle_timeout` | duration | `"30m"` | 空闲超时（0 = 禁用） |
| `ws_port` | int | `8080` | 远程沙箱 WebSocket 端口 |
| `auth_token` | string | `""` | Runner 认证 Token |
| `public_url` | string | `""` | Runner 连接的公共 URL |

### 渠道配置

详见各渠道文档：

- [飞书](/xbot/channels/feishu/)
- [QQ](/xbot/channels/qq/)
- [NapCat](/xbot/channels/napcat/)
- [Web](/xbot/channels/web/)
- [CLI](/xbot/channels/cli/)

### Server 配置

```json
{
  "server": {
    "host": "0.0.0.0",
    "port": 8082,
    "read_timeout": "30s",
    "write_timeout": "120s"
  }
}
```

### CLI 配置（Remote 模式）

```json
{
  "cli": {
    "server_url": "ws://127.0.0.1:8082",
    "token": "your-admin-token"
  }
}
```

Server 模式安装时自动配置，一般不需要手动修改。

### 管理员配置

```json
{
  "admin": {
    "token": "random-generated-token",
    "chat_id": ""
  }
}
```

| 字段 | 说明 |
|------|------|
| `token` | 管理员 Token（安装时自动生成） |
| `chat_id` | 管理员 chat ID（用于接收启动通知） |

### Embedding 配置（Letta 记忆模式需要）

```json
{
  "embedding": {
    "provider": "openai",
    "base_url": "https://api.openai.com/v1",
    "api_key": "",
    "model": "text-embedding-3-small",
    "max_tokens": 2048
  }
}
```

### 日志配置

```json
{
  "log": {
    "level": "info",
    "format": "json"
  }
}
```

### 其他配置

```json
{
  "tavily_api_key": "",
  "oauth": {
    "enable": false,
    "host": "127.0.0.1",
    "port": 8081,
    "base_url": ""
  },
  "pprof": {
    "enable": false,
    "host": "localhost",
    "port": 6060
  }
}
```

| 字段 | 说明 |
|------|------|
| `tavily_api_key` | Tavily 网页搜索 API Key（配置后 Agent 可搜索网页） |
| `oauth` | OAuth 2.0 服务配置（Web 渠道认证用） |
| `pprof` | 性能分析端点（开发调试用） |

### 多 LLM 订阅（CLI 模式）

CLI 模式支持配置多个 LLM 订阅，通过 `/model` 切换：

```json
{
  "subscriptions": [
    {
      "name": "GPT-4o",
      "provider": "openai",
      "api_key": "sk-xxx",
      "base_url": "https://api.openai.com/v1",
      "model": "gpt-4o",
      "active": true
    },
    {
      "name": "Claude",
      "provider": "anthropic",
      "api_key": "sk-ant-xxx",
      "model": "claude-sonnet-4-20250514",
      "active": false
    }
  ]
}
```
