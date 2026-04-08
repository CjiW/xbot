package channel

import "context"

const defaultCLIChannelPrompt = `## CLI 渠道规则

### 向用户提问
- 使用 ` + "`AskUser`" + ` 工具向用户提问（需要确认、需要额外信息时）
- 调用后 agent 会暂停，CLI 会打开交互式输入面板，等待用户回复后自动恢复处理
- AskUser 支持 choices 参数提供多选选项
- 在 CLI 中，AskUser 会直接打开交互式面板，不需要通过消息发送问题
`

// CliPromptProvider 实现 agent.ChannelPromptProvider 接口。
// 为 CLI 渠道注入特化的 prompt 片段（AskUser 使用提示等）。
type CliPromptProvider struct{}

func (p *CliPromptProvider) ChannelPromptName() string { return "cli" }

func (p *CliPromptProvider) ChannelSystemParts(_ context.Context, _, _ string) map[string]string {
	return map[string]string{
		"05_channel_cli": defaultCLIChannelPrompt,
	}
}
