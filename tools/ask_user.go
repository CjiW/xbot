package tools

import (
	"encoding/json"
	"fmt"

	"xbot/llm"
)

// AskUserTool allows the agent to ask the user a question and wait for their response.
// Supported channels: CLI, Feishu, Web.
// In CLI, opens an interactive TUI panel. In Feishu, sends an interactive card with buttons/options.
// In Web, sends a WebSocket message that renders a form.
// Only available in channels that support interactive responses (implements ChannelProvider).
type AskUserTool struct{}

func (t *AskUserTool) Name() string { return "AskUser" }

func (t *AskUserTool) Description() string {
	return "Ask the user a question and wait for their response. Use this when you need confirmation, clarification, or additional information from the user. Only available in CLI mode. Supports optional choices for multiple-choice questions."
}

func (t *AskUserTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{
			Name:        "questions",
			Type:        "array",
			Description: `Array of questions to ask the user. Each item is an object with "question" (string, required, supports multi-line) and "options" (array of strings, optional) fields. Example: [{"question":"Choose a theme","options":["dark","light"]},{"question":"Any other preferences?"}]`,
			Required:    true,
			Items: &llm.ToolParamItems{
				Type: "object",
				Properties: map[string]any{
					"question": map[string]any{"type": "string", "description": "The question to ask the user (supports multi-line)"},
					"options":  map[string]any{"type": "array", "items": map[string]string{"type": "string"}, "description": "Optional choices for multiple-choice questions"},
				},
				Required: []string{"question"},
			},
		},
	}
}

type askUserArgs struct {
	Questions []askQItem `json:"questions"`
}

type askQItem struct {
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
}

func (t *AskUserTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	args, err := parseToolArgs[askUserArgs](input)
	if err != nil {
		return nil, fmt.Errorf("parse arguments: %w", err)
	}

	if len(args.Questions) == 0 {
		return nil, fmt.Errorf("questions parameter is required")
	}

	qJSON, _ := json.Marshal(args.Questions)
	metadata := map[string]string{
		"ask_questions": string(qJSON),
	}

	// For CLI, the engine sends OutboundMessage{WaitingUser:true} to the channel
	// adapter which opens the TUI panel. For Feishu, the channel adapter builds
	// and sends an interactive card. No SendFunc needed here.
	_ = ctx // ctx is available for future use but not needed currently

	return &ToolResult{
		Summary:     fmt.Sprintf("Asked %d question(s)", len(args.Questions)),
		WaitingUser: true,
		Metadata:    metadata,
	}, nil
}

// SupportedChannels implements ChannelProvider interface.
func (t *AskUserTool) SupportedChannels() []string {
	return []string{"cli", "feishu"}
}
