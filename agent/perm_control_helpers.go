package agent

import (
	"context"
	"xbot/internal/ctxkeys"
	"xbot/llm"
)

func withPermControlEnabled(ctx context.Context, enabled bool) context.Context {
	return ctxkeys.WithPermControlEnabled(ctx, enabled)
}

func withApprovalTarget(ctx context.Context, chatID, senderID string) context.Context {
	return ctxkeys.WithApprovalTarget(ctx, chatID, senderID)
}

// toolDefFilter wraps a tool definition and can hide selected params without
// mutating the global registry copy.
type toolDefFilter struct {
	base       llm.ToolDefinition
	hiddenArgs map[string]bool
}

func (t *toolDefFilter) Name() string        { return t.base.Name() }
func (t *toolDefFilter) Description() string { return t.base.Description() }
func (t *toolDefFilter) Parameters() []llm.ToolParam {
	params := t.base.Parameters()
	if len(t.hiddenArgs) == 0 {
		return params
	}
	out := make([]llm.ToolParam, 0, len(params))
	for _, p := range params {
		if t.hiddenArgs[p.Name] {
			continue
		}
		out = append(out, p)
	}
	return out
}

func isPermControlEnabledFor(settingsSvc *SettingsService, channel, senderID string) bool {
	if settingsSvc == nil {
		return false
	}
	return IsPermControlEnabled(settingsSvc.GetPermUsers(channel, senderID))
}

func visibleToolDefs(defs []llm.ToolDefinition, settingsSvc *SettingsService, channel, senderID string) []llm.ToolDefinition {
	if isPermControlEnabledFor(settingsSvc, channel, senderID) {
		return defs
	}
	out := make([]llm.ToolDefinition, 0, len(defs))
	for _, d := range defs {
		switch d.Name() {
		case "Shell", "FileCreate", "FileReplace":
			out = append(out, &toolDefFilter{base: d, hiddenArgs: map[string]bool{"run_as": true, "reason": true}})
		default:
			out = append(out, d)
		}
	}
	return out
}
