package tools

import (
	"encoding/json"
	"fmt"
	"xbot/llm"
	"xbot/oauth"
)

// OAuthTool sends an OAuth authorization card to the user.
// This is used when a tool requires OAuth authorization from a provider.
type OAuthTool struct {
	Manager *oauth.Manager
	BaseURL string // Base URL for callback (e.g., https://your-domain.com)
}

// Name returns the tool name.
func (t *OAuthTool) Name() string {
	return "oauth_authorize"
}

// Description returns the tool description.
func (t *OAuthTool) Description() string {
	return "Send an OAuth authorization card to the user. " +
		"Use this when an operation requires OAuth authorization from a provider. " +
		"IMPORTANT: Do NOT specify scopes unless absolutely necessary - the default scopes cover all common operations." +
		"IMPORTANT: Use it only when you have strong reason to believe that the user needs to authorize the tool(do not use it when errcode is 404)."
}

// Parameters returns the tool parameters.
func (t *OAuthTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{
			Name:        "provider",
			Type:        "string",
			Description: "OAuth provider name (e.g., feishu, github)",
			Required:    true,
		},
		{
			Name:        "reason",
			Type:        "string",
			Description: "Why authorization is needed (e.g., 'to access your bitable tables')",
			Required:    true,
		},
		{
			Name:        "scopes",
			Type:        "array",
			Description: "OAuth scopes (LEAVE EMPTY to use default scopes which cover all common operations)",
			Required:    false,
			Items: &llm.ToolParamItems{
				Type: "string",
			},
		},
	}
}

// Execute sends an OAuth authorization card.
func (t *OAuthTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	var args struct {
		Provider string   `json:"provider"`
		Reason   string   `json:"reason"`
		Scopes   []string `json:"scopes"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}

	if t.Manager == nil {
		return nil, fmt.Errorf("OAuth manager not configured")
	}

	// Start OAuth flow
	authURL, state, err := t.Manager.StartFlow(
		args.Provider,
		ctx.Channel,
		ctx.ChatID,
		args.Scopes,
	)
	if err != nil {
		return nil, fmt.Errorf("start OAuth flow: %w", err)
	}

	// Build authorization card with proper prefix
	cardJSON := t.buildAuthCard(args.Provider, args.Reason, authURL, state)
	cardContent := "__FEISHU_CARD__::" + cardJSON

	// Send card
	if err := ctx.SendFunc(ctx.Channel, ctx.ChatID, cardContent); err != nil {
		return nil, fmt.Errorf("send card: %w", err)
	}

	return NewResultWithUserResponse(
		fmt.Sprintf("OAuth authorization card sent for %s. Please click the button to authorize.", args.Provider),
	), nil
}

// buildAuthCard builds a Feishu interactive card for OAuth authorization.
func (t *OAuthTool) buildAuthCard(provider, reason, authURL, state string) string {
	providerDisplay := provider
	if provider == "feishu" {
		providerDisplay = "飞书"
	}

	card := map[string]any{
		"config": map[string]any{
			"wide_screen_mode": true,
		},
		"header": map[string]any{
			"template": "blue",
			"title": map[string]any{
				"content": "授权 " + providerDisplay + " 访问",
				"tag":     "plain_text",
			},
		},
		"elements": []any{
			map[string]any{
				"tag": "div",
				"text": map[string]any{
					"tag":     "lark_md",
					"content": "需要授权 **" + providerDisplay + "** 才能继续操作。\n\n点击下方按钮完成授权。",
				},
			},
			map[string]any{
				"tag": "action",
				"actions": []any{
					map[string]any{
						"tag":  "button",
						"text": map[string]any{"tag": "plain_text", "content": "点击授权"},
						"type": "primary",
						"url":  authURL,
					},
				},
			},
			map[string]any{
				"tag": "hr",
			},
			map[string]any{
				"tag": "div",
				"text": map[string]any{
					"tag":     "plain_text",
					"content": "授权完成后，您可以继续之前的操作。",
				},
			},
		},
	}

	data, err := json.Marshal(card)
	if err != nil {
		// Fallback: return minimal safe card
		return `{"header":{"title":{"tag":"plain_text","content":"OAuth Authorization"}},"elements":[]}`
	}
	return string(data)
}
