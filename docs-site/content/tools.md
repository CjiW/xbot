---
title: "Tools"
weight: 10
---


xbot includes ~50 built-in tools the agent can call during conversations. This page provides a reference for all available tools.

## File Operations

| Tool | Description |
|------|-------------|
| `Read` | Read file content with line numbers, optional offset/limit |
| `FileCreate` | Create new files (errors if file already exists) |
| `FileReplace` | Search-and-replace in files (exact or RE2 regex, line range, replace_all) |
| `Glob` | Find files by glob pattern (`**` recursive matching) |
| `Grep` | Search file contents with RE2 regex, include filter, ignore_case, context_lines |
| `Cd` | Change working directory (persists across tool calls) |
| `DownloadFile` | Download files from Feishu messages or web/OSS URLs |

## Execution

| Tool | Description |
|------|-------------|
| `Shell` | Execute shell commands. Configurable timeout (default 120s), background mode, `run_as` user switching |
| `shell_env` | Environment variable management (`export VAR=value`), auto-persisted to `~/.xbot_env` |

## Web & Search

| Tool | Description |
|------|-------------|
| `Fetch` | Fetch web URL content, convert HTML to markdown via readability + tiktoken truncation |
| `WebSearch` | Web search via Tavily API with configurable depth and max results |

## Context & Session

| Tool | Description |
|------|-------------|
| `context_edit` | Edit conversation context: list turns, delete turn/message, truncate, regex replace |
| `ChatHistory` | Retrieve recent messages from group chats |
| `recall` | Retrieve offloaded or masked observation content with pagination |
| `recall_masked` | Retrieve masked observations only |
| `offload_recall` | Retrieve offloaded tool result content by offload ID |

## Scheduling & Events

| Tool | Description |
|------|-------------|
| `Cron` | Manage scheduled tasks: add (interval/delay/cron_expr/at), list, remove |
| `EventTrigger` | Manage webhook event triggers with Go template support and HMAC-SHA256 verification |

## Interactive Cards (Feishu)

| Tool | Description |
|------|-------------|
| `card_create` | Create interactive card sessions |
| `card_add_content` | Add content (markdown, div, image, table, chart) |
| `card_add_interactive` | Add interactive elements (button, input, select, date_picker) |
| `card_add_container` | Add containers (column_set, form, collapsible_panel) |
| `card_preview` | Preview card JSON |
| `card_send` | Send card to chat |

## Memory (Letta Mode)

| Tool | Description |
|------|-------------|
| `core_memory_append` | Append to core memory blocks (persona/human/working_context) |
| `core_memory_replace` | Replace content in core memory blocks |
| `rethink` | Re-examine and evolve core memory (A-Mem style) |
| `archival_memory_insert` | Insert into archival (vector-backed) long-term memory |
| `archival_memory_search` | Semantic search archival memory |
| `recall_memory_search` | Search conversation history by date range |

## SubAgents & Skills

| Tool | Description |
|------|-------------|
| `SubAgent` | Delegate tasks to sub-agents (one-shot or interactive multi-turn) |
| `Skill` | Discover and load skills from workspace |

## Background Tasks

| Tool | Description |
|------|-------------|
| `task_status` | Check background task status |
| `task_kill` | Terminate a running background task |
| `task_read` | Read background task output |

## MCP Integration

| Tool | Description |
|------|-------------|
| `ManageTools` | Add/remove/list/reload MCP servers |
| `load_tools` | Activate MCP tools by name to load parameter schemas |
| `search_tools` | Semantic search for available tools using embedding similarity |

## Other

| Tool | Description |
|------|-------------|
| `AskUser` | Ask user a multiple-choice question |
| `TodoWrite` / `TodoList` | In-memory TODO list management per session |
| `Logs` | List/read xbot log files with filtering |
| `oauth_authorize` | Send OAuth authorization card to user |

## Permission Control Parameters

When permission control is enabled, `Shell`, `FileCreate`, and `FileReplace` gain additional parameters:

| Parameter | Description |
|-----------|-------------|
| `run_as` | OS user to execute as (e.g. `root`) |
| `reason` | Required reason for execution (must be provided with `run_as`) |
