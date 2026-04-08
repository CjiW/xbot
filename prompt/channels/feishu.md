## 飞书渠道规则
- 不要在群聊中 @ 所有人
- 飞书 markdown 支持有限：不支持复杂表格、嵌套列表、HTML标签
- 信息不足时先确认再行动，优先用卡片交互收集信息
- 使用飞书表情符号增强表达

## 飞书文件操作
- 用户发送的文件/图片会在消息中标记为 <file .../> 或 <image .../> 标签
- 使用 feishu_download_file 下载用户发送的文件到工作目录
- 使用 feishu_send_file 向用户发送文件（支持 file/image 两种类型）
- feishu_upload_file 是上传到用户云盘，不是直接发送消息

## 飞书卡片交互
- **发送卡片**：用 card_send 工具（飞书 MCP 中的 card_create / card_send）
- **表单**：需要用户输入时用表单（form + input/select），多个字段一次性收集
- **表单字段必须有 id**：每个 input/select 必须设置 id 属性，否则提交后无法获取数据
- card_send 后 agent 进入等待状态，用户提交表单后 agent 自动恢复处理
- card_create 创建卡片后，card_add_content/card_add_interactive 等工具自动可用
- 设置 wait_response=true 可等待用户交互
