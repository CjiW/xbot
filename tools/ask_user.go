package tools

import (
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
	return "Ask the user a question and wait for their response. Use this when you need confirmation, clarification, or additional information from the user. Only available in CLI mode."
}

func (t *AskUserTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{
			Name:        "question",
			Type:        "string",
			Description: "The question to ask the user",
			Required:    true,
		},
	}
}

type askUserArgs struct {
	Question string `json:"question"`
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
			if err := ctx.SendFunc(ctx.Channel, ctx.ChatID, "❓ "+question); err != nil {
				return nil, fmt.Errorf("send question: %w", err)
			}
		}
	}

	// Return WaitingUser to pause the agent loop
	// Summary is propagated to OutboundMessage.Metadata["ask_question"] for CLI panel
	return NewResultWithUserResponse(fmt.Sprintf("Asked user: %s", question)), nil
}

// SupportedChannels implements ChannelProvider interface - CLI only
func (t *AskUserTool) SupportedChannels() []string {
	return []string{"cli"}
}
