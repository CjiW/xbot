# 飞书消息解析优化 - 调研报告

## 需求背景

当前代码解析用户发送的富文本卡片内容时，很多内容种类都无法正确识别，只能识别少量信息。

## 飞书消息类型列表

根据飞书开放平台 API 文档，消息的 `msg_type` 包括：

| msg_type | 说明 | 当前支持 |
|----------|------|----------|
| text | 文本消息 | ✅ |
| post | 富文本消息（标题+内容块） | ✅ |
| image | 图片消息 | ✅ |
| file | 文件消息 | ✅ |
| audio | 音频消息 | ❌ |
| video | 视频消息 | ❌ |
| media | 媒体消息（视频带封面） | ❌ |
| sticker | 表情包 | ❌ |
| share_chat | 分享群聊 | ❌ |
| share_user | 分享用户 | ❌ |
| interactive | 卡片消息（收到的卡片） | ❌ |

## 当前代码问题

文件：`channel/feishu.go`

### 1. parseContent 函数

```go
switch msgType {
case "text":
    // ✅ 已支持
case "post":
    // ✅ 已支持
case "file":
    // ✅ 已支持
case "image":
    // ✅ 已支持
default:
    return fmt.Sprintf("[%s]", msgType)  // ❌ 未识别类型直接返回类型名
}
```

### 2. extractPostText 函数（富文本解析）

当前只处理部分元素类型：
- `text`, `a`, `at`, `img` ✅

未处理的元素类型：
- `emotion` - 表情
- `media` - 媒体（音视频）
- `hr` - 分割线
- `button` - 按钮
- `select_static` - 下拉选择
- `overflow` - 更多菜单
- `date_picker` - 日期选择器
- `checkbox` - 复选框
- `radio` - 单选框
- `note` - 备注

## 修复方案

### 1. 扩展 parseContent 函数

```go
case "audio":
    // 解析音频消息
    fileKey, _ := contentJSON["file_key"].(string)
    return fmt.Sprintf(`<audio file_key="%s" />`, fileKey)

case "video":
    // 解析视频消息
    fileKey, _ := contentJSON["file_key"].(string)
    return fmt.Sprintf(`<video file_key="%s" />`, fileKey)

case "media":
    // 解析媒体消息（视频带封面）
    fileKey, _ := contentJSON["file_key"].(string)
    imageKey, _ := contentJSON["image_key"].(string)
    return fmt.Sprintf(`<media file_key="%s" image_key="%s" />`, fileKey, imageKey)

case "sticker":
    // 解析表情包
    fileKey, _ := contentJSON["file_key"].(string)
    return fmt.Sprintf(`<sticker file_key="%s" />`, fileKey)

case "share_chat":
    // 解析分享群聊
    chatID, _ := contentJSON["chat_id"].(string)
    return fmt.Sprintf(`<share_chat chat_id="%s" />`, chatID)

case "share_user":
    // 解析分享用户
    userID, _ := contentJSON["user_id"].(string)
    return fmt.Sprintf(`<share_user user_id="%s" />`, userID)

case "interactive":
    // 解析卡片消息
    return f.extractInteractiveContent(contentJSON)
```

### 2. 扩展 extractPostText 函数

增加对更多元素类型的支持：

```go
case "emotion":
    if emojiType, ok := elemMap["emoji_type"].(string); ok {
        parts = append(parts, fmt.Sprintf("[emoji:%s]", emojiType))
    }

case "media":
    if fileKey, ok := elemMap["file_key"].(string); ok {
        parts = append(parts, fmt.Sprintf(`<media file_key="%s" />`, fileKey))
    }

case "hr":
    parts = append(parts, "---")

case "button":
    if text, ok := elemMap["text"].(map[string]any); ok {
        if btnText, ok := text["content"].(string); ok {
            parts = append(parts, fmt.Sprintf("[按钮: %s]", btnText))
        }
    }

case "note":
    // 备注内容通常用于补充说明，可选择忽略或提取
    continue
```

## 待确认问题

1. 音频/视频文件是否需要下载？
2. 分享的群聊/用户信息是否需要查询详情？
3. 卡片消息 (interactive) 的内容结构需要进一步确认

## 优先级

1. **P0**: 扩展 parseContent 支持所有基础消息类型
2. **P1**: 扩展 extractPostText 支持更多富文本元素
3. **P2**: 音频/视频/卡片消息的深度处理（根据实际需求）
