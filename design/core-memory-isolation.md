# Core Memory 隔离设计

## 概述

Core Memory（核心记忆）存储 bot 的身份设定、用户画像和工作上下文。采用分层隔离策略：persona 全局共享，human 跨 tenant 按用户共享，working_context 按会话隔离。

## 存储策略

| Block | TenantID | UserID | 说明 |
|-------|----------|--------|------|
| persona | 0 (固定) | "" | 全局共享，所有用户看到同一个 bot 身份 |
| human | 0 (固定) | userID | 跨 tenant 共享，同一用户在不同会话共享画像 |
| working_context | tenantID | "" | 按会话隔离，不同群聊/私聊独立 |

## 读写一致性

**关键原则：读写使用相同的 key**

- GetBlock / SetBlock / GetAllBlocks 对同一 block type 使用相同的 effectiveTenantID 和 uid 计算逻辑
- 任何修改必须同时保证 Get/Set/GetAll 行为一致

### 代码位置

```
storage/sqlite/core_memory.go
```

### 关键逻辑

```go
switch blockName {
case "persona":
    effectiveTenantID = 0  // 固定
case "human":
    effectiveTenantID = 0  // 固定
    uid = userID           // 按用户区分
case "working_context":
    effectiveTenantID = tenantID  // 按会话隔离
    uid = ""
}
```

## 历史数据迁移

### 问题

旧版本 persona 和 human 存储在各自 tenantID 下，导致：
1. 私聊和群聊的 human 不共享
2. persona 在不同 tenant 下有不同副本

### 迁移策略

1. **合并 persona**：所有 tenantID 的 persona 取最长内容，合并到 tenantID=0
2. **合并 human**：每个 userID 取最长内容，合并到 tenantID=0
3. **清理旧数据**：迁移后删除 tenantID != 0 的 persona/human 记录

### SQL 实现

```sql
-- persona: 取最长内容
INSERT OR REPLACE INTO core_memory_blocks (...)
SELECT 0, 'persona', '', content, char_limit, CURRENT_TIMESTAMP
FROM core_memory_blocks
WHERE block_name = 'persona' AND user_id = ''
ORDER BY LENGTH(content) DESC
LIMIT 1;

-- human: 按 userID 取最长内容
INSERT OR REPLACE INTO core_memory_blocks (...)
SELECT 0, 'human', user_id, content, char_limit, CURRENT_TIMESTAMP
FROM (
    SELECT user_id, content, char_limit,
        ROW_NUMBER() OVER (PARTITION BY user_id ORDER BY LENGTH(content) DESC) AS rn
    FROM core_memory_blocks
    WHERE block_name = 'human' AND user_id != ''
)
WHERE rn = 1;

-- 清理旧数据
DELETE FROM core_memory_blocks
WHERE block_name IN ('persona', 'human') AND tenant_id != 0;
```

## 回归测试

测试文件：`storage/sqlite/core_memory_test.go`

| 测试 | 验证场景 |
|------|----------|
| TestCoreMemoryService_PersonaGlobal | 不同 tenant 写入/读取同一 persona |
| TestCoreMemoryService_HumanCrossTenant | 不同 tenant 下同一 userID 共享 human |
| TestCoreMemoryService_WorkingContextPerTenant | 不同 tenant 独立的 working_context |
| TestCoreMemoryService_ReadWriteConsistency | 写入能读回 |
| TestCoreMemoryService_DefaultBlocks | 默认字符限制正确 |
| TestCoreMemoryService_CharLimit | 超过限制报错 |
| TestCoreMemoryService_DifferentUsersDifferentHuman | 不同用户 human 独立 |
| TestCoreMemoryService_MigrationKeepsLongest | 迁移保留最长内容 |

## 常见错误

### 读写 key 不一致

**错误示例**：
```go
// SetBlock 中 human 没有设 effectiveTenantID = 0
case "human":
    // 漏了 effectiveTenantID = 0
    uid = userID
```

**后果**：写入用 tenantID=群聊ID，读取用 tenantID=0，读不到写入的数据。

**修复**：确保 GetBlock / SetBlock / InitBlocks 中相同 block type 使用相同的 effectiveTenantID 计算逻辑。
