package tools

import (
	"encoding/json"
	"fmt"
	"xbot/llm"
)

// UserProfileTool allows the bot to update its observations about the current sender
type UserProfileTool struct{}

func (t *UserProfileTool) Name() string { return "update_user_profile" }
func (t *UserProfileTool) Description() string {
	return "Update your observations about the current message sender. Records communication style, preferences, personality traits. Persists across all conversations and groups. Keep it concise — bullet points, not essays."
}
func (t *UserProfileTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{
			Name:        "profile",
			Type:        "string",
			Description: "Full updated profile as concise markdown bullet points. Include existing observations plus new ones. Keep under ~500 chars.",
			Required:    true,
		},
	}
}

type profileArgs struct {
	Profile string `json:"profile"`
}

func (t *UserProfileTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	if ctx.SenderID == "" {
		return NewResult("No sender identified for this message, cannot update profile."), nil
	}
	if ctx.SaveUserProfile == nil {
		return NewResult("User profile storage is not available."), nil
	}

	var args profileArgs
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return nil, fmt.Errorf("parse arguments: %w", err)
	}
	if args.Profile == "" {
		return NewResult("Profile content is empty, nothing to save."), nil
	}

	if err := ctx.SaveUserProfile(args.Profile); err != nil {
		return nil, fmt.Errorf("save user profile: %w", err)
	}

	name := ctx.SenderName
	if name == "" {
		name = ctx.SenderID
	}
	return NewResult(fmt.Sprintf("Profile for %s updated.", name)), nil
}

// SelfProfileTool allows the bot to update its own self-profile (__me__)
type SelfProfileTool struct{}

func (t *SelfProfileTool) Name() string { return "update_self_profile" }
func (t *SelfProfileTool) Description() string {
	return "Update your own self-profile — your personality, values, preferences, current mood, things you've learned about yourself. This is YOUR persistent identity that carries across all conversations. Keep it concise — bullet points, not essays."
}
func (t *SelfProfileTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{
			Name:        "profile",
			Type:        "string",
			Description: "Full updated self-profile as concise markdown bullet points. Include existing content plus new reflections. Keep under ~500 chars.",
			Required:    true,
		},
	}
}

func (t *SelfProfileTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	if ctx.SaveSelfProfile == nil {
		return NewResult("Self-profile storage is not available."), nil
	}

	var args profileArgs
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return nil, fmt.Errorf("parse arguments: %w", err)
	}
	if args.Profile == "" {
		return NewResult("Profile content is empty, nothing to save."), nil
	}

	if err := ctx.SaveSelfProfile(args.Profile); err != nil {
		return nil, fmt.Errorf("save self profile: %w", err)
	}

	return NewResult("Self-profile updated."), nil
}
