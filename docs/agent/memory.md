# memory/ — Pluggable Memory Providers

## Architecture: Three-Layer Memory

| Layer | Scope | Storage | Tools |
|-------|-------|---------|-------|
| **Project Memory** | Project-level, all providers | `.xbot/knowledge/` (md files, git-trackable) | `knowledge_write`, `knowledge_list`, `Read` |
| **Flat Memory** | Per-user, non-project | `~/.xbot/memory/{tenantID}/` (md files) | `memory_read`, `memory_write`, `memory_list` |
| **Letta Memory** | Per-user, full-featured | SQLite + vector DB | `core_memory_*`, `archival_memory_*`, `recall_memory_search` |

Project memory is provider-agnostic — works with both flat and letta.

## Key Interface

```go
// memory/memory.go
type MemoryProvider interface {
    Recall(ctx context.Context, query string) (string, error)
    Memorize(ctx context.Context, input MemorizeInput) (MemorizeResult, error)
    Close() error
}
```

## Flat Memory (`flat/`)

- **File-based**: `MEMORY.md` (≤1000 chars, injected into system prompt) + `HISTORY.md` (event timeline) + `knowledge/` (personal notes)
- Directory: `~/.xbot/memory/{tenantID}/`
- `Recall()`: reads MEMORY.md + lists knowledge/ files for on-demand access
- `Memorize()`: LLM consolidation with `save_memory` tool; supports `knowledge_updates` for auto-maintaining knowledge files
- Tool search: simple substring match (no vector DB)
- No SQLite dependency

## Letta Memory (`letta/`)

- Core memory: persona, human, working_context blocks (stored in SQLite)
- Archival memory: vector DB with semantic search
- Each tenant has isolated memory
- `consolidate_memory` tool: moves working_context items to archival
