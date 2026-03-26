package feishu_mcp

import (
	"context"
	"fmt"

	log "xbot/logger"
	"xbot/oauth"

	lark "github.com/larksuite/oapi-sdk-go/v3"
)

const (
	// FeishuGroupName 飞书工具组名称
	FeishuGroupName = "Feishu"
	// FeishuGroupInstructions 飞书工具组使用说明
	FeishuGroupInstructions = `Feishu (飞书) integration tools for Wiki, Bitable, Docs, and file operations.`
)

// FeishuToolBase 飞书工具基类，实现 ToolGroupProvider 和 ChannelProvider 接口
type FeishuToolBase struct{}

func (b FeishuToolBase) GroupName() string           { return FeishuGroupName }
func (b FeishuToolBase) GroupInstructions() string   { return FeishuGroupInstructions }
func (b FeishuToolBase) SupportedChannels() []string { return []string{"feishu"} }

// FeishuMCP provides access to Feishu APIs using the generic OAuth framework.
type FeishuMCP struct {
	oauth      *oauth.Manager
	larkClient *lark.Client // 用于获取租户信息
	appID      string       // 飞书应用 ID（用于获取 tenant_access_token）
	appSecret  string       // 飞书应用密钥
}

// NewFeishuMCP creates a new Feishu MCP instance.
func NewFeishuMCP(oauthMgr *oauth.Manager, appID, appSecret string) *FeishuMCP {
	return &FeishuMCP{
		oauth:     oauthMgr,
		appID:     appID,
		appSecret: appSecret,
	}
}

// SetLarkClient sets the lark client for tenant queries.
func (m *FeishuMCP) SetLarkClient(client *lark.Client) {
	m.larkClient = client
}

// LarkClient returns the underlying lark.Client for direct API calls.
func (m *FeishuMCP) LarkClient() *lark.Client {
	return m.larkClient
}

// Client wraps a Lark client with user access token and tenant domain.
type Client struct {
	lark         *lark.Client
	accessToken  string
	tenantDomain string
}

// GetClient returns a Lark client for the session.
// Returns TokenNeededError if token is missing or expired.
func (m *FeishuMCP) GetClient(ctx context.Context, channel, chatID string) (*Client, error) {
	// First get the token to extract tenant domain
	token, err := m.oauth.GetToken("feishu", channel, chatID)
	if err != nil {
		return nil, err
	}

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

	tenantDomain := ""
	if token != nil && token.Raw != nil {
		if domain, ok := token.Raw["tenant_domain"].(string); ok {
			tenantDomain = domain
		}
	}

	// 如果没有域名，尝试获取并更新 Token（兼容旧数据）
	if tenantDomain == "" && m.larkClient != nil {
		domain, err := m.fetchTenantDomain(ctx)
		if err != nil {
			log.WithError(err).Warn("Failed to fetch tenant domain")
		} else if domain != "" {
			tenantDomain = domain
			// 更新 Token 中的域名
			if token.Raw == nil {
				token.Raw = make(map[string]any)
			}
			token.Raw["tenant_domain"] = domain
			if err := m.oauth.SetToken("feishu", channel, chatID, token); err != nil {
				log.WithError(err).Warn("Failed to update token with tenant domain")
			} else {
				log.WithField("domain", domain).Info("Tenant domain fetched and cached")
			}
		}
	}

	return &Client{
		lark:         wrapper.Client(),
		accessToken:  wrapper.AccessToken(),
		tenantDomain: tenantDomain,
	}, nil
}

// fetchTenantDomain 获取租户域名 using the app's tenant_access_token
// Returns empty string if domain is not set (some tenants don't have custom domains)
func (m *FeishuMCP) fetchTenantDomain(ctx context.Context) (string, error) {
	resp, err := m.larkClient.Tenant.Tenant.Query(ctx)
	if err != nil {
		return "", fmt.Errorf("query tenant: %w", err)
	}

	if !resp.Success() {
		return "", fmt.Errorf("tenant query failed: %s", resp.Msg)
	}

	if resp.Data.Tenant != nil && resp.Data.Tenant.Domain != nil && *resp.Data.Tenant.Domain != "" {
		return *resp.Data.Tenant.Domain, nil
	}

	// Some tenants don't have custom domains - return empty string (not an error)
	return "", nil
}

// Client returns the underlying Lark client.
func (c *Client) Client() *lark.Client {
	return c.lark
}

// AccessToken returns the user access token.
func (c *Client) AccessToken() string {
	return c.accessToken
}

// TenantDomain returns the tenant domain (e.g., "example.feishu.cn").
func (c *Client) TenantDomain() string {
	return c.tenantDomain
}

// BuildURL constructs a full Feishu URL with the tenant domain.
func (c *Client) BuildURL(token, objType string) string {
	path := BuildFeishuURL(token, objType)
	if c.tenantDomain == "" {
		return path // Return path only if no domain
	}
	return fmt.Sprintf("https://%s%s", c.tenantDomain, path)
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
