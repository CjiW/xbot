package feishu_mcp

import (
	"context"
	"fmt"

	"xbot/oauth"

	lark "github.com/larksuite/oapi-sdk-go/v3"
)

// FeishuMCP provides access to Feishu APIs using the generic OAuth framework.
type FeishuMCP struct {
	oauth *oauth.Manager
}

// NewFeishuMCP creates a new Feishu MCP instance.
func NewFeishuMCP(oauthMgr *oauth.Manager) *FeishuMCP {
	return &FeishuMCP{
		oauth: oauthMgr,
	}
}

// Client wraps a Lark client with user access token.
type Client struct {
	lark        *lark.Client
	accessToken string
}

// GetClient returns a Lark client for the session.
// Returns TokenNeededError if token is missing or expired.
func (m *FeishuMCP) GetClient(ctx context.Context, channel, chatID string) (*Client, error) {
	clientWrapper, err := m.oauth.GetClientForSession(ctx, "feishu", channel, chatID)
	if err != nil {
		return nil, err
	}

	wrapper, ok := clientWrapper.(interface {
		Client() *lark.Client
		AccessToken() string
	})
	if !ok {
		return nil, fmt.Errorf("invalid client type from OAuth manager")
	}

	return &Client{
		lark:        wrapper.Client(),
		accessToken: wrapper.AccessToken(),
	}, nil
}

// Client returns the underlying Lark client.
func (c *Client) Client() *lark.Client {
	return c.lark
}

// AccessToken returns the user access token.
func (c *Client) AccessToken() string {
	return c.accessToken
}

// NeedTokenError is a convenience function for creating TokenNeededError.
func NeedTokenError(channel, chatID, reason string) *oauth.TokenNeededError {
	return &oauth.TokenNeededError{
		Provider: "feishu",
		Channel:  channel,
		ChatID:   chatID,
		Reason:   reason,
	}
}
