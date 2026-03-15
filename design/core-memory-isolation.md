# Core Memory Isolation Design

## Overview

`CoreMemoryService` manages three structured memory blocks for the Letta memory
provider. Each block type has a distinct isolation scope so that the right data
is shared or isolated across tenants and users.

## Block Types and Isolation Scope

| Block            | Storage key (tenant_id, block_name, user_id) | Scope                          |
|------------------|----------------------------------------------|--------------------------------|
| `persona`        | `(0, "persona", "")`                         | Global — all tenants and users |
| `human`          | `(0, "human", <userID>)`                     | Per-user — cross-tenant        |
| `working_context`| `(<tenantID>, "working_context", "")`         | Per-tenant                     |

### persona — Global Share

The bot's identity and personality are the same for every conversation.
All reads and writes are routed to `tenant_id = 0` so no tenant-specific copy
can diverge.

### human — Per-user, Cross-tenant

Observations about a specific user accumulate regardless of which chat/channel
the user is in. By storing the block at `tenant_id = 0` and keying by `user_id`,
any tenant that hosts that user sees the same human context.

### working_context — Per-tenant Isolation

Short-lived task state (e.g., "currently reviewing PR #42") is tied to a
specific chat room or session. Using the real `tenantID` keeps each tenant's
context independent.

## Database Schema

```sql
CREATE TABLE core_memory_blocks (
    tenant_id  INTEGER NOT NULL,
    block_name TEXT    NOT NULL,
    user_id    TEXT    NOT NULL DEFAULT '',
    content    TEXT    NOT NULL DEFAULT '',
    char_limit INTEGER NOT NULL DEFAULT 2000,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (tenant_id, block_name, user_id),
    FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE
);
```

The composite primary key `(tenant_id, block_name, user_id)` enforces the
isolation policy at the database level. Violating the scoping rules (e.g.,
writing persona under a non-zero `tenant_id`) would create a siloed copy that is
never read back, introducing a hard-to-detect bug.

## Character Limits

| Block            | Default char_limit |
|------------------|--------------------|
| `persona`        | 2000               |
| `human`          | 2000               |
| `working_context`| 4000               |

`SetBlock` enforces the limit and returns an error if content exceeds it.

## Initialization

`InitBlocks(tenantID, userID)` creates all three default blocks with `INSERT OR
IGNORE`, so calling it multiple times is safe. It also triggers the one-time
data migration (see below).

## Data Migration

When upgrading from a version that stored `persona` and `human` under each
tenant's own `tenant_id`, `migrateLegacyData` consolidates those rows:

1. **persona** — select the row with the longest content from all tenants and
   upsert it to `(tenant_id=0, "persona", "")`.
2. **human** — for each distinct `user_id`, select the row with the longest
   content across all tenants and upsert it to
   `(tenant_id=0, "human", <user_id>)`.
3. **cleanup** — delete all `persona` and `human` rows where `tenant_id != 0`.
4. A `core_memory_migrations` table tracks completed migrations so the operation
   runs exactly once per database.

### Why "keep longest"?

When multiple copies of a block exist the longest one is the most information-
rich. Silently discarding data by choosing a shorter copy would be worse than
the rare case where a shorter update was deliberately intended.

## Regression Tests

`storage/sqlite/core_memory_test.go` contains eight tests that lock in the
isolation policy:

| Test                                                  | What it verifies                                              |
|-------------------------------------------------------|---------------------------------------------------------------|
| `TestCoreMemoryService_TenantIsolation_Persona`       | Writing persona under tenant1 is visible under tenant2        |
| `TestCoreMemoryService_TenantIsolation_Human`         | Writing human for a user under tenant1 is visible under tenant2 |
| `TestCoreMemoryService_TenantIsolation_WorkingContext`| Each tenant has an independent working_context                |
| `TestCoreMemoryService_ReadWriteConsistency`          | GetBlock and GetAllBlocks return exactly what was written      |
| `TestCoreMemoryService_DefaultBlocks`                 | InitBlocks creates all three blocks with correct limits        |
| `TestCoreMemoryService_CharLimit`                     | Over-limit content is rejected; at-limit content is accepted   |
| `TestCoreMemoryService_DifferentUsersHaveDifferentHuman` | Different users have independent human blocks              |
| `TestCoreMemoryService_MigrationKeepsLongest`         | Migration merges legacy data keeping the longest content       |

Run them with:

```bash
go test -v ./storage/sqlite/... -run "TestCoreMemory"
```

## Changing the Isolation Policy

Any change to the scoping rules **must**:

1. Update the routing logic in `GetBlock`, `SetBlock`, `InitBlocks`, and
   `GetAllBlocks` consistently.
2. Write a new database migration if existing rows need to be relocated.
3. Update or add regression tests in `core_memory_test.go` to cover the new
   behaviour.
4. Update this document.
