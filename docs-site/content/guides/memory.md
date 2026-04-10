---
title: "Memory"
weight: 20
---

# Memory System

xbot supports pluggable memory providers. The agent uses memory to persist information across conversations, recall past interactions, and maintain user-specific context.

## Providers

| | Flat (default) | Letta (MemGPT) |
|--|----------------|----------------|
| Core | In-memory blocks | SQLite (always in prompt) |
| Archival | Grep-searchable blob | Vector search (chromem-go) |
| Recall | Event history | FTS5 full-text search |
| Dependencies | None | Embedding model required |

## Configuration

```bash
MEMORY_PROVIDER=flat    # or: letta
```

For Letta mode, configure an embedding model:

```bash
LLM_EMBEDDING_PROVIDER=openai
LLM_EMBEDDING_BASE_URL=https://api.openai.com/v1
LLM_EMBEDDING_API_KEY=sk-xxx
LLM_EMBEDDING_MODEL=text-embedding-3-small
```

## Flat Provider

All long-term memories are stored as a single text blob and injected into the system prompt on every request. On `/new`, the LLM consolidates the memory blob.

**Tools:** `core_memory_append`, `core_memory_replace`, `archival_memory_insert`, `archival_memory_search`, `recall_memory_search`

## Letta Provider

Three-tier memory architecture inspired by [MemGPT](https://memgpt.ai/):

### Core Memory

Three blocks always present in the system prompt:

| Block | Purpose | Isolation |
|-------|---------|-----------|
| `persona` | Agent's identity and personality | Global (shared across all users) |
| `human` | Per-user observations and preferences | Per-user (cross-tenant) |
| `working_context` | Current task state and active context | Per-session |

**Tools:** `core_memory_append`, `core_memory_replace`, `rethink`

### Archival Memory

Long-term vector-backed storage for detailed facts, events, and context. Stored in SQLite with chromem-go embeddings.

**Tools:** `archival_memory_insert`, `archival_memory_search`

### Recall Memory

Full conversation history searchable by date range. Powered by SQLite FTS5.

**Tools:** `recall_memory_search`

## Memory Consolidation

When starting a new conversation (`/new` command):

- **Flat**: LLM merges and consolidates the memory blob
- **Letta**: Core memory persists; archival memory is retained; working_context is cleared

## Core Memory Isolation

In multi-tenant deployments (server mode with multiple channels):

- `persona` is **global** — shared across all users
- `human` is **per-user** — isolated by sender ID
- `working_context` is **per-session** — reset on `/new`

See [Core Memory Isolation Design](/design/core-memory-isolation/) for implementation details.
