package channel

import (
	"context"
	"xbot/prompt"
)

// CliPromptProvider 实现 agent.ChannelPromptProvider 接口。
// 为 CLI 渠道注入特化的 prompt 片段（AskUser 使用提示等）。
type CliPromptProvider struct{}

func (p *CliPromptProvider) ChannelPromptName() string { return "cli" }

func (p *CliPromptProvider) ChannelSystemParts(_ context.Context, _, _ string) map[string]string {
	return map[string]string{
		"05_channel_cli": prompt.CLIChannel,
	}
}
