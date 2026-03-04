---
name: card-builder
description: 飞书卡片构建系统知识库。修改 tools/card_builder.go、tools/card_tools.go 或 channel/feishu.go 卡片处理代码时自动激活。
user-invokable: true
---

## 架构概览

卡片构建系统实现飞书交互卡片的动态创建、发送和回调处理：

```
Agent → card_create → card_add_* → card_send → FeishuChannel
                                    ↓
                            CardBuilder (存储元数据)
                                    ↓
用户交互 → FeishuChannel.onCardAction → handleCardBuilderAction → Agent
```

## 关键文件

| 文件 | 功能 |
|------|------|
| `tools/card_builder.go` | CardBuilder、CardSession、CardElement 数据结构和元素构建函数 |
| `tools/card_tools.go` | 卡片工具实现：card_create、card_add_content、card_add_interactive、card_add_container、card_preview、card_send |
| `channel/feishu.go` | 卡片发送（`__FEISHU_CARD__:` 前缀）、回调处理（`handleCardBuilderAction`）、跳过卡片处理 |

## 核心数据结构

### CardBuilder

```go
type CardBuilder struct {
    sessions             map[string]*CardSession
    descriptions         sync.Map // card_id -> description（回调上下文）
    expectedInteractions sync.Map // card_id -> []string（期望的交互类型）
    activeCards          sync.Map // chat_id -> card_id（跳过卡片处理）
    elementOptions       sync.Map // card_id -> map[elementName]options（选项描述）
}
```

### CardSession

```go
type CardSession struct {
    ID                  string
    Header, Config      map[string]any
    Elements            []*CardElement
    Containers          map[string]*CardElement  // parent_id 查找
    Channel, ChatID     string
    SendFunc            func(channel, chatID, content string) error
    ExpectedInteractions []string  // 期望处理的交互类型
}
```

### CardElement

```go
type CardElement struct {
    ID, Tag    string
    Properties map[string]any
    Children   []*CardElement
}
```

## 卡片工具流程

### 1. card_create
创建 CardSession，注册动态工具（card_add_*、card_preview、card_send）

### 2. card_add_content
添加展示组件：markdown、div、image、img_combination、divider、table、chart、person、person_list

### 3. card_add_interactive
添加交互组件：button、input、select_static、multi_select_static、select_person、multi_select_person、date_picker、picker_time、picker_datetime、overflow、checker、select_img

### 4. card_add_container
添加布局容器：column_set、form、collapsible_panel、interactive_container

### 5. card_send
- 调用 `CollectExpectedInteractions()` 收集期望的交互类型
- 保存元数据到 CardBuilder（description、expectedInteractions、elementOptions、activeCards）
- 发送卡片（`__FEISHU_CARD__:card_id:json`）
- 清理 Session

## 交互类型跟踪

### CollectExpectedInteractions()

扫描卡片元素，记录期望处理的交互类型：

| 元素类型 | 处理方式 |
|----------|----------|
| button | 始终处理 |
| form_submit | 始终处理（表单提交按钮） |
| select_static, multi_select_static 等 | 仅在**非表单内**时立即处理 |
| 表单内的交互元素 | 仅在提交时处理 |

```go
// 表单内的 select 不单独处理，等待 form_submit
// 表单外的 select 立即触发回调
```

## 回调处理

### handleCardBuilderAction

处理卡片交互事件：

1. **form_submit** - 收集所有表单字段值，patch 卡片为"已提交"状态
2. **button** - 提取按钮 name 和 value
3. **select_static / multi_select_static** - 检查是否期望此交互，提取选中值和可用选项
4. **overflow / checker / select_img** - 检查是否期望此交互，提取数据

回调消息格式：
```
[Card Action: card_123] select_static
- element_name: color_selector
- selected: red
- available_options: Red (value: red), Blue (value: blue), Green (value: green)
```

## 跳过卡片处理

当用户在卡片活跃期间发送文本消息时：

1. 检查 `activeCards[chatID]` 是否存在
2. 通过 `cardMsgIDs` 反查 message_id
3. Patch 卡片为 "⚠️ 用户选择直接回复，卡片已关闭"
4. 清除 `activeCards[chatID]`
5. 继续正常处理文本消息

## 常见陷阱

### 1. 表单提交按钮缺少 action_type
**问题**：按钮点击不会收集表单数据

**解决**：设置 `properties.action_type="form_submit"`

### 2. 忘记调用 CollectExpectedInteractions
**问题**：standalone select 事件被忽略

**解决**：在 `card_send` 前调用 `session.CollectExpectedInteractions()`

### 3. CardBuilder 未注入 FeishuChannel
**问题**：回调时无法获取元数据

**解决**：在 `main.go` 中调用 `feishuCh.SetCardBuilder(agentLoop.GetCardBuilder())`

### 4. 元素 name 未去重
**问题**：飞书要求全局唯一的 name

**解决**：`deduplicateNames()` 自动处理，但最好在构建时使用有意义的唯一名称

## 变更历史

- 2026-03-04: 添加 ExpectedInteractions 跟踪、select 事件处理、跳过卡片处理
- 2025-03-04: 初始卡片构建系统实现
