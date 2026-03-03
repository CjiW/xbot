package providers

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	log "xbot/logger"
	"xbot/oauth"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkauthen "github.com/larksuite/oapi-sdk-go/v3/service/authen/v1"
)

// FeishuProvider implements the OAuth Provider interface for Feishu (Lark).
type FeishuProvider struct {
	appID       string
	appSecret   string
	redirectURI string
	client      *lark.Client
}

// NewFeishuProvider creates a new Feishu OAuth provider.
func NewFeishuProvider(appID, appSecret, redirectURI string) *FeishuProvider {
	return &FeishuProvider{
		appID:       appID,
		appSecret:   appSecret,
		redirectURI: redirectURI,
		client:      lark.NewClient(appID, appSecret),
	}
}

// Name returns the provider name.
func (p *FeishuProvider) Name() string {
	return "feishu"
}

// BuildAuthURL generates the Feishu authorization URL.
// Feishu OAuth docs: https://open.feishu.cn/document/common-capabilities/sso/api/get-user-info
func (p *FeishuProvider) BuildAuthURL(state string, scopes []string) string {
	// Default scopes for Feishu
	if len(scopes) == 0 {
		scopes = []string{
			"bitable:app", // Access to bitable apps
			"bitable:app:readonly",
			"docx:document", // Access to documents
			"docx:document:readonly",
			"wiki:wiki", // Access to wiki
			"wiki:wiki:readonly",
		}
	}

	authURL, _ := url.Parse("https://open.feishu.cn/open-apis/authen/v1/authorize")
	params := authURL.Query()
	params.Set("app_id", p.appID)
	params.Set("redirect_uri", p.redirectURI)
	params.Set("scope", joinScopes(scopes))
	params.Set("state", state)
	authURL.RawQuery = params.Encode()

	log.WithFields(log.Fields{
		"app_id":       p.appID,
		"redirect_uri": p.redirectURI,
		"state":        state,
		"scopes":       scopes,
	}).Info("Feishu OAuth URL generated")

	return authURL.String()
}

// ExchangeCode exchanges the authorization code for tokens.
// Uses Feishu OIDC endpoint for user access token.
func (p *FeishuProvider) ExchangeCode(ctx context.Context, code string) (*oauth.Token, error) {
	// Use Lark SDK's authen service for OIDC token exchange
	// The SDK automatically handles app authentication
	body := larkauthen.NewCreateOidcAccessTokenReqBodyBuilder().
		GrantType("authorization_code").
		Code(code).
		Build()

	req := larkauthen.NewCreateOidcAccessTokenReqBuilder().
		Body(body).
		Build()

	resp, err := p.client.Authen.V1.OidcAccessToken.Create(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("create OIDC access token: %w", err)
	}

	if !resp.Success() {
		return nil, fmt.Errorf("feishu API error: %s (code: %d)", resp.Msg, resp.Code)
	}

	if resp.Data.AccessToken == nil {
		return nil, fmt.Errorf("missing access_token in response")
	}

	expiresIn := 7200 // default 2 hours
	if resp.Data.ExpiresIn != nil {
		expiresIn = *resp.Data.ExpiresIn
	}

	refreshExpiresIn := 2592000 // default 30 days
	if resp.Data.RefreshExpiresIn != nil {
		refreshExpiresIn = *resp.Data.RefreshExpiresIn
	}

	token := &oauth.Token{
		AccessToken:  *resp.Data.AccessToken,
		RefreshToken: "",
		ExpiresAt:    time.Now().Add(time.Duration(expiresIn) * time.Second),
		Scopes:       []string{},
		Raw: map[string]any{
			"token_type":               "Bearer",
			"expires_in":               expiresIn,
			"refresh_token_expires_in": refreshExpiresIn,
		},
	}

	if resp.Data.RefreshToken != nil {
		token.RefreshToken = *resp.Data.RefreshToken
	}
	if resp.Data.TokenType != nil {
		token.Raw["token_type"] = *resp.Data.TokenType
	}
	if resp.Data.Scope != nil {
		token.Scopes = strings.Fields(*resp.Data.Scope)
	}

	// Get tenant info to fetch the enterprise domain
	tenantInfo, err := p.getTenantInfo(ctx, token.AccessToken)
	if err != nil {
		log.WithError(err).Warn("Failed to get tenant info, continuing without domain")
	} else if tenantInfo != nil {
		token.Raw["tenant_domain"] = tenantInfo.Domain
		token.Raw["tenant_name"] = tenantInfo.Name
		log.WithFields(log.Fields{
			"tenant_domain": tenantInfo.Domain,
			"tenant_name":   tenantInfo.Name,
		}).Info("Feishu tenant info retrieved")
	}

	log.WithFields(log.Fields{
		"expires_in":               expiresIn,
		"refresh_token_expires_in": refreshExpiresIn,
	}).Info("Feishu OAuth token exchanged successfully")

	return token, nil
}

// TenantInfo holds tenant (enterprise) information
type TenantInfo struct {
	Domain string
	Name   string
}

// getTenantInfo retrieves tenant information using user access token
func (p *FeishuProvider) getTenantInfo(ctx context.Context, accessToken string) (*TenantInfo, error) {
	resp, err := p.client.Tenant.V2.Tenant.Query(ctx, larkcore.WithUserAccessToken(accessToken))
	if err != nil {
		return nil, fmt.Errorf("query tenant: %w", err)
	}

	if !resp.Success() {
		return nil, fmt.Errorf("tenant query failed: %s (code: %d)", resp.Msg, resp.Code)
	}

	info := &TenantInfo{}
	if resp.Data.Tenant != nil {
		if resp.Data.Tenant.Domain != nil {
			info.Domain = *resp.Data.Tenant.Domain
		}
		if resp.Data.Tenant.Name != nil {
			info.Name = *resp.Data.Tenant.Name
		}
	}

	if info.Domain == "" {
		return nil, fmt.Errorf("tenant domain is empty")
	}

	return info, nil
}

// RefreshToken uses a refresh token to get a new access token.
// Uses Feishu OIDC endpoint for user access token.
func (p *FeishuProvider) RefreshToken(ctx context.Context, refreshToken string) (*oauth.Token, error) {
	body := larkauthen.NewCreateOidcRefreshAccessTokenReqBodyBuilder().
		GrantType("refresh_token").
		RefreshToken(refreshToken).
		Build()

	req := larkauthen.NewCreateOidcRefreshAccessTokenReqBuilder().
		Body(body).
		Build()

	resp, err := p.client.Authen.V1.OidcRefreshAccessToken.Create(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("refresh OIDC access token: %w", err)
	}

	if !resp.Success() {
		return nil, fmt.Errorf("feishu API error: %s (code: %d)", resp.Msg, resp.Code)
	}

	if resp.Data.AccessToken == nil {
		return nil, fmt.Errorf("missing access_token in response")
	}

	expiresIn := 7200 // default 2 hours
	if resp.Data.ExpiresIn != nil {
		expiresIn = *resp.Data.ExpiresIn
	}

	refreshExpiresIn := 2592000 // default 30 days
	if resp.Data.RefreshExpiresIn != nil {
		refreshExpiresIn = *resp.Data.RefreshExpiresIn
	}

	token := &oauth.Token{
		AccessToken:  *resp.Data.AccessToken,
		RefreshToken: "",
		ExpiresAt:    time.Now().Add(time.Duration(expiresIn) * time.Second),
		Scopes:       []string{},
		Raw: map[string]any{
			"token_type":               "Bearer",
			"expires_in":               expiresIn,
			"refresh_token_expires_in": refreshExpiresIn,
		},
	}

	if resp.Data.RefreshToken != nil {
		token.RefreshToken = *resp.Data.RefreshToken
	}
	if resp.Data.TokenType != nil {
		token.Raw["token_type"] = *resp.Data.TokenType
	}
	if resp.Data.Scope != nil {
		token.Scopes = strings.Fields(*resp.Data.Scope)
	}

	return token, nil
}

// GetClient returns a Lark client wrapper with the user access token.
// The actual user access token should be passed as a request option using
// larkcore.WithUserAccessToken(accessToken) when making API calls.
func (p *FeishuProvider) GetClient(accessToken string) any {
	return &LarkClientWrapper{
		client:      p.client,
		accessToken: accessToken,
	}
}

// GetLarkClient returns the underlying Lark client (for tenant queries).
func (p *FeishuProvider) GetLarkClient() *lark.Client {
	return p.client
}

// LarkClientWrapper wraps a Lark client with a user access token.
type LarkClientWrapper struct {
	client      *lark.Client
	accessToken string
}

// Client returns the underlying Lark client.
func (w *LarkClientWrapper) Client() *lark.Client {
	return w.client
}

// AccessToken returns the user access token.
func (w *LarkClientWrapper) AccessToken() string {
	return w.accessToken
}

// joinScopes joins scopes into a space-separated string.
func joinScopes(scopes []string) string {
	if len(scopes) == 0 {
		return ""
	}
	var b strings.Builder
	for i, s := range scopes {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(s)
	}
	return b.String()
}
