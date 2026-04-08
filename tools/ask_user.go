package tools

import (
	"encoding/json"
	"fmt"

	"xbot/llm"
)

// AskUserTool allows the agent to ask the user a question in CLI mode.
// It sends the question via SendFunc and pauses execution until the user responds.
// Only available in CLI channel (implements ChannelProvider).
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
				Type:       "object",
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

	// Send via SendFunc for non-CLI channels
	if ctx.Channel != "cli" && ctx.SendFunc != nil {
		for i, q := range args.Questions {
			msg := fmt.Sprintf("❓ %s", q.Question)
			for j, opt := range q.Options {
				msg += fmt.Sprintf("\n  %d. %s", j+1, opt)
			}
			if i < len(args.Questions)-1 {
				msg += "\n"
			}
			if err := ctx.SendFunc(ctx.Channel, ctx.ChatID, msg); err != nil {
				return nil, fmt.Errorf("send question: %w", err)
			}
		}
	}

	return &ToolResult{
		Summary:     fmt.Sprintf("Asked %d question(s)", len(args.Questions)),
		WaitingUser: true,
		Metadata:    metadata,
	}, nil
}

// SupportedChannels implements ChannelProvider interface - CLI only
func (t *AskUserTool) SupportedChannels() []string {
	return []string{"cli"}
}
