# Core Memory 隔离设计

## 概述

Core Memory Service 负责管理三种核心记忆块：
- **persona**: 机器人身份/性格
- **human**: 当前用户观察
- **working_context**: 活跃的工作事实/会话上下文

## 存储隔离策略

| Block | 存储策略 | 说明 |
|-------|---------|------|
| **persona** | tenantID = 0 (全局) | 所有 tenant 共享同一个 persona |
| **human** | tenantID = 0 + userID | 跨 tenant 共享，按 userID 区分 |
| **working_context** | tenantID (当前) | 按会话隔离，每个 tenant 独立 |

### 设计原因

1. **persona**: 机器人身份应该是全局一致的，不应因会话不同而变化
2. **human**: 用户信息应该在私聊和群聊中共享，让机器人对用户有统一的认知
3. **working_context**: 每个会话(tenant)有独立的工作上下文，避免会话污染

## 实现细节

### GetBlock / SetBlock / GetAllBlocks

所有读写接口都遵循相同的隔离策略：

```go
switch blockName {
case "persona":
    effectiveTenantID = 0  // 固定为0
case "human":
    effectiveTenantID = 0  // 固定为0
    uid = userID           // 使用传入的 userID
case "working_context":
    // 使用传入的 tenantID
}
```

### InitBlocks

初始化时也遵循相同策略：

```go
switch name {
case "persona":
    effectiveTenantID = 0
case "human":
    effectiveTenantID = 0
    uid = userID
case "working_context":
    // 使用传入的 tenantID
}
```

## 数据迁移

### 迁移策略

旧数据（各 tenantID 分散存储）需要迁移到新架构：

1. **persona**: 从所有 tenant 中选择最长的内容，合并到 tenantID=0
2. **human**: 对每个 userID，从所有 tenant 中选择最长的内容，合并到 tenantID=0
3. 迁移完成后清理旧数据（tenantID != 0 的记录）

### 迁移实现

- 使用 `sync.Once` 确保迁移只执行一次
- 迁移 marker 放在迁移成功之后，防止迁移失败但 marker 已写入
- 使用 `ROW_NUMBER()` 窗口函数确保选择最长内容

## 测试

详见 `storage/sqlite/core_memory_test.go`

### 回归测试用例

1. **TestCoreMemoryService_TenantIsolation_Persona**: 验证 persona 全局共享
2. **TestCoreMemoryService_TenantIsolation_Human**: 验证 human 跨 tenant 共享
3. **TestCoreMemoryService_TenantIsolation_WorkingContext**: 验证 working_context 按 tenant 隔离
4. **TestCoreMemoryService_ReadWriteConsistency**: 验证读写一致性
5. **TestCoreMemoryService_DefaultBlocks**: 验证默认 block 创建
6. **TestCoreMemoryService_CharLimit**: 验证字符限制
7. **TestCoreMemoryService_DifferentUsersHaveDifferentHuman**: 验证不同用户有不同 human
8. **TestCoreMemoryService_MigrationKeepsLongest**: 验证迁移保留最长内容

## 注意事项

### 不要修改隔离策略

修改以下代码可能导致数据混乱：

1. **GetBlock / SetBlock / GetAllBlocks** 中的 `effectiveTenantID` 计算
2. **InitBlocks** 中的默认 block 创建逻辑
3. **migrateLegacyData** 中的迁移逻辑

如果需要修改隔离策略，必须：
1. 先修改代码
2. 运行回归测试确保通过
3. 添加数据迁移（如需要）
4. 更新本文档
