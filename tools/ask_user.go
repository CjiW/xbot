package tools

import (
	"encoding/json"
	"fmt"
	"strings"

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
			Name:        "question",
			Type:        "string",
			Description: "The question to ask the user",
			Required:    true,
		},
		{
			Name:        "options",
			Type:        "array",
			Description: "Optional list of choices for multiple-choice questions. If provided, user can select from these options. Each option is a string.",
			Required:    false,
		},
	}
}

type askUserArgs struct {
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
}

func (t *AskUserTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	args, err := parseToolArgs[askUserArgs](input)
	if err != nil {
		return nil, fmt.Errorf("parse arguments: %w", err)
	}

	question := strings.TrimSpace(args.Question)
	if question == "" {
		return nil, fmt.Errorf("question parameter is required")
	}

	// Send the question via SendFunc for non-CLI channels
	// CLI uses the interactive panel (reads from Metadata), so skip SendFunc
	if ctx.Channel != "cli" {
		if ctx.SendFunc != nil {
			msg := "❓ " + question
			if len(args.Options) > 0 {
				for i, opt := range args.Options {
					msg += fmt.Sprintf("\n  %d. %s", i+1, opt)
				}
			}
			if err := ctx.SendFunc(ctx.Channel, ctx.ChatID, msg); err != nil {
				return nil, fmt.Errorf("send question: %w", err)
			}
		}
	}

	// Build result with optional choices metadata
	result := &ToolResult{
		Summary:     fmt.Sprintf("Asked user: %s", question),
		WaitingUser: true,
	}
	if len(args.Options) > 0 {
		optsJSON, _ := json.Marshal(args.Options)
		result.Metadata = map[string]string{
			"ask_options": string(optsJSON),
		}
	}
	return result, nil
}

// SupportedChannels implements ChannelProvider interface - CLI only
func (t *AskUserTool) SupportedChannels() []string {
	return []string{"cli"}
}
