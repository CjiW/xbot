package channel

import (
	"context"
	"xbot/internal/ctxkeys"
	"xbot/prompt"
)

// CliPromptProvider 实现 agent.ChannelPromptProvider 接口。
// 为 CLI 渠道注入特化的 prompt 片段（AskUser 使用提示等）。
type CliPromptProvider struct{}

func (p *CliPromptProvider) ChannelPromptName() string { return "cli" }

func (p *CliPromptProvider) ChannelSystemParts(ctx context.Context, _, _ string) map[string]string {
	parts := map[string]string{
		"05_channel_cli": prompt.CLIChannel,
	}
	if ctxkeys.PermControlEnabledFromContext(ctx) {
		parts["06_channel_cli_perm"] = "\n## CLI Permission Control\n\n- Never write raw `sudo` in commands.\n- When privilege switching is needed, use `run_as` and `reason` together.\n- Use the default execution user for routine work.\n- Use the privileged user only when genuinely necessary.\n- If approval is denied and the user provides feedback, respect that feedback and adjust your plan.\n"
	}
	return parts
}
