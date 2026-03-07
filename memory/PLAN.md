# SimpleMemory v1 实施计划

## 目标

实现结构化记忆模块 `SimpleMemory`，替代 `FlatMemory` 的全量注入模式。
核心改进：写入时结构化 + 读取时按需检索（core 全量 + FTS5 检索）。

## 约束

1. **FlatMemory 数据完全不动**——`long_term_memory` / `event_history` 表保持原样
2. **SimpleMemory 使用独立的表**——`memory_notes` + `memory_notes_fts`
3. **随时可切回 FlatMemory**——通过配置切换，两套数据独立共存
4. **MemoryProvider 接口不变**——`Recall()` / `Memorize()` / `Close()` 签名不改

## 架构

```
memory/
├── memory.go              # MemoryProvider 接口（已有，不改）
├── flat/
│   ├── flat.go            # FlatMemory（已有，不改）
│   └── flat_test.go
└── simple/
    ├── simple.go          # SimpleMemory 实现
    ├── simple_test.go
    └── store.go           # SQLite + FTS5 存储层
```

## 存储设计

### 新增表（schema v3 迁移）

```sql
CREATE TABLE memory_notes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    tenant_id INTEGER NOT NULL,
    category TEXT NOT NULL DEFAULT 'detail',  -- 'core' | 'event' | 'person' | 'detail'
    content TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE
);
CREATE INDEX idx_memory_notes_tenant_category ON memory_notes(tenant_id, category);

CREATE VIRTUAL TABLE memory_notes_fts USING fts5(
    content,
    content=memory_notes,
    content_rowid=id
);

-- FTS5 同步触发器
CREATE TRIGGER memory_notes_ai AFTER INSERT ON memory_notes BEGIN
    INSERT INTO memory_notes_fts(rowid, content) VALUES (new.id, new.content);
END;
CREATE TRIGGER memory_notes_ad AFTER DELETE ON memory_notes BEGIN
    INSERT INTO memory_notes_fts(memory_notes_fts, rowid, content) VALUES('delete', old.id, old.content);
END;
CREATE TRIGGER memory_notes_au AFTER UPDATE ON memory_notes BEGIN
    INSERT INTO memory_notes_fts(memory_notes_fts, rowid, content) VALUES('delete', old.id, old.content);
    INSERT INTO memory_notes_fts(rowid, content) VALUES (new.id, new.content);
END;
```

### 分类规则

| category | 内容 | Recall 方式 | 示例 |
|----------|------|-------------|------|
| `core` | 身份、项目、偏好、规则、配置 | **全量注入** | "xbot: Go项目, SQLite存储" |
| `event` | 做了什么、发生了什么 | FTS5 检索 | "[2026-03-04] PR #16 合并" |
| `person` | 人物信息 | FTS5 检索 | "张三: 后端开发, 偏好简洁" |
| `detail` | 临时细节 | FTS5 检索 | "服务器 IP: 43.139.60.232" |

## Recall 流程

```
Recall(ctx, userMessage):
  1. SELECT content FROM memory_notes WHERE tenant_id=? AND category='core'
     → 拼接为 core_text
  2. FTS5: SELECT content FROM memory_notes WHERE tenant_id=? AND memory_notes_fts MATCH ?
     → 取 rank 前 10 条 → related_text
  3. return "## Long-term Memory\n" + core_text + "\n## Related Context\n" + related_text
```

## Memorize 流程

```
Memorize(ctx, input):
  1. 格式化对话文本（复用 FlatMemory 的消息格式化逻辑）
  2. LLM 调用 extract_notes 工具，提取结构化 notes：
     [
       {"category": "core", "content": "...", "action": "upsert", "match_id": null},
       {"category": "event", "content": "...", "action": "insert", "match_id": null}
     ]
  3. 对每条 note：
     - action=insert → INSERT INTO memory_notes
     - action=upsert → 先 FTS5 查相似条目
       - 找到 → UPDATE content, updated_at
       - 没找到 → INSERT
     - action=delete → DELETE（LLM 判断某条已过时）
  4. 返回 MemorizeResult
```

### extract_notes 工具定义

```json
{
  "name": "extract_notes",
  "description": "Extract structured memory notes from conversation",
  "parameters": {
    "notes": [
      {
        "category": "core|event|person|detail",
        "content": "concise fact, one note per distinct fact",
        "action": "insert|upsert|delete",
        "match_hint": "keywords to find existing note to update (for upsert/delete)"
      }
    ]
  }
}
```

## 实施步骤

### Step 1: Schema 迁移（storage/sqlite/db.go）

- `schemaVersion` 2 → 3
- `migrateSchema` 添加 v2→v3：创建 `memory_notes` + `memory_notes_fts` + 触发器
- **不动** `long_term_memory` / `event_history` 表

### Step 2: 存储层（memory/simple/store.go）

- `NoteStore` struct，持有 `*sqlite.DB`
- 方法：
  - `InsertNote(tenantID, category, content) (int64, error)`
  - `UpdateNote(id, content) error`
  - `DeleteNote(id) error`
  - `GetCoreNotes(tenantID) ([]Note, error)` — 全量
  - `SearchNotes(tenantID, query, limit) ([]Note, error)` — FTS5
  - `FindSimilar(tenantID, hint string) (*Note, error)` — FTS5 找最相似的一条（用于 upsert）

### Step 3: SimpleMemory 实现（memory/simple/simple.go）

- `SimpleMemory` struct，实现 `memory.MemoryProvider`
- `Recall(ctx, query)`:
  1. `GetCoreNotes` → 全量
  2. `SearchNotes(query, 10)` → FTS5 检索
  3. 拼接返回
- `Memorize(ctx, input)`:
  1. 格式化消息
  2. LLM + extract_notes 工具
  3. 执行 insert/upsert/delete
- `Close()`: no-op

### Step 4: 接入（session/multitenant.go）

- 添加配置项 `MemoryType string`（"flat" | "simple"，默认 "flat"）
- `GetOrCreateSession` 中根据配置创建对应的 MemoryProvider
- SimpleMemory 需要 `*sqlite.DB`（直接传入）和 `llm.LLM`（用于 Memorize）

### Step 5: 环境变量

- `MEMORY_TYPE=simple` 启用 SimpleMemory
- 不设置或 `MEMORY_TYPE=flat` 保持现有行为

### Step 6: 测试

- `memory/simple/store_test.go` — 存储层单测（FTS5 检索准确性）
- `memory/simple/simple_test.go` — Memorize/Recall 集成测试（mock LLM）
- 手动验证：切换 flat ↔ simple，确认互不影响

## 迁移策略

**不自动迁移**。SimpleMemory 从空白开始积累，FlatMemory 数据保持不变。
如果需要迁移，后续提供一次性脚本：读 `long_term_memory` → LLM 拆分 → 写入 `memory_notes`。

## 风险

| 风险 | 缓解 |
|------|------|
| FTS5 中文分词差 | SQLite FTS5 默认 unicode61 tokenizer 对中文按字分词，基本可用；后续可换 jieba tokenizer |
| LLM extract_notes 不稳定 | 工具调用有 schema 约束；失败时 graceful fallback（不写入，不崩溃） |
| 冷启动无记忆 | 前几次对话 core 区为空，效果不如 FlatMemory；需要几轮对话积累 |
| FTS5 + 触发器性能 | 单租户 notes 量级 <10K，SQLite FTS5 完全够用 |

## 不做的事

- ❌ Embedding 向量检索（Phase 3）
- ❌ LLM Rerank（Phase 3）
- ❌ 自动压缩/淘汰
- ❌ 注入位置优化
- ❌ 迁移现有 FlatMemory 数据
