# memory/ — Pluggable Memory Providers

## Providers

| Directory | Provider | Description |
|-----------|----------|-------------|
| `flat/` | FlatMemory | In-memory blocks + grep-based archival search |
| `letta/` | LettaMemory | SQLite core memory + vector search (archival) + FTS5 |

## Key Interface

```go
// memory/memory.go
type MemoryProvider interface {
    CoreMemory(tenantID int64) CoreMemory
    ArchivalService() *vectordb.ArchivalService
}
```

## Letta Memory

- Core memory: persona, human, working_context blocks (stored in SQLite)
- Archival memory: vector DB with semantic search
- Each tenant has isolated memory
- `consolidate_memory` tool: moves working_context items to archival

## Flat Memory

- No database dependency
- Core memory blocks kept in memory only
- Archival: grep-based search over stored entries
- Suitable for lightweight/single-session use
