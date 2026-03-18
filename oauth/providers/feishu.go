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

// allowedScopes defines all permitted OAuth scopes for Feishu.
// These are the only scopes that can be requested.
var allowedScopes = map[string]bool{
	"bitable:app":                         true,
	"bitable:app:readonly":                true,
	"board:whiteboard:node:create":        true,
	"board:whiteboard:node:delete":        true,
	"board:whiteboard:node:read":          true,
	"board:whiteboard:node:update":        true,
	"docs:document.comment:create":        true,
	"docs:document.comment:read":          true,
	"docs:document.comment:update":        true,
	"docs:document.comment:write_only":    true,
	"docs:document.content:read":          true,
	"docs:document.media:upload":          true,
	"docs:document.subscription":          true,
	"docs:document.subscription:read":     true,
	"docs:document:copy":                  true,
	"docs:event.document_deleted:read":    true,
	"docs:event.document_edited:read":     true,
	"docs:event.document_opened:read":     true,
	"docs:event:subscribe":                true,
	"docs:permission.member":              true,
	"docs:permission.member:auth":         true,
	"docs:permission.member:create":       true,
	"docs:permission.member:delete":       true,
	"docs:permission.member:readonly":     true,
	"docs:permission.member:retrieve":     true,
	"docs:permission.member:transfer":     true,
	"docs:permission.member:update":       true,
	"docs:permission.setting":             true,
	"docs:permission.setting:read":        true,
	"docs:permission.setting:readonly":    true,
	"docs:permission.setting:write_only":  true,
	"docx:document":                       true,
	"docx:document.block:convert":         true,
	"docx:document:create":                true,
	"docx:document:readonly":              true,
	"docx:document:write_only":            true,
	"drive:drive.metadata:readonly":       true,
	"drive:drive:version":                 true,
	"drive:drive:version:readonly":        true,
	"drive:file.like:readonly":            true,
	"drive:file.meta.sec_label.read_only": true,
	"drive:file:upload":                   true,
	"drive:file:view_record:readonly":     true,
	"im:message":                          true,
	"im:message.pins:read":                true,
	"im:message.pins:write_only":          true,
	"im:message.reactions:read":           true,
	"im:message.reactions:write_only":     true,
	"im:message.urgent.status:write":      true,
	"im:message:readonly":                 true,
	"im:message:recall":                   true,
	"im:message:update":                   true,
	"offline_access":                      true,
	"search:docs:read":                    true,
	"sheets:spreadsheet":                  true,
	"sheets:spreadsheet:create":           true,
	"wiki:wiki":                           true,
	"wiki:wiki:readonly":                  true,
}

// defaultScopes defines commonly used scopes as the default set.
// This is a subset of allowedScopes to stay under the 50 scope limit.
// Covers all MCP tool requirements: bitable, docx, wiki, drive, sheets, etc.
var defaultScopes = []string{
	// Bitable (多维表格)
	"bitable:app",
	"bitable:app:readonly",
	// Docx (文档)
	"docx:document",
	"docx:document:readonly",
	"docs:document.content:read",
	"docx:document:write_only",
	"docx:document:create",
	"docx:document.block:convert",
	// Wiki (知识库)
	"wiki:wiki",
	"wiki:wiki:readonly",
	// Drive (云盘)
	"drive:file:upload",
	"drive:drive.metadata:readonly",
	// Sheets (表格)
	"sheets:spreadsheet",
	"sheets:spreadsheet:create",
	// Docs (旧版文档权限，用于权限管理)
	"docs:permission.member",
	"docs:permission.member:create",
	// Search (搜索)
	"search:docs:read",
	// IM (消息)
	"im:message",
	"im:message:readonly",
	// Offline (离线访问)
	"offline_access",
}

// validateScopes checks if all scopes are allowed and within the limit.
// Returns validated scopes or logs warning for invalid ones.
func validateScopes(scopes []string) []string {
	if len(scopes) > 50 {
		log.WithField("count", len(scopes)).Warn("Too many scopes requested, limiting to 50")
		scopes = scopes[:50]
	}

	valid := make([]string, 0, len(scopes))
	for _, s := range scopes {
		if allowedScopes[s] {
			valid = append(valid, s)
		} else {
			log.WithField("scope", s).Warn("Invalid scope requested, skipping")
		}
	}
	return valid
}

// BuildAuthURL generates the Feishu authorization URL.
// Feishu OAuth docs: https://open.feishu.cn/document/common-capabilities/sso/api/get-user-info
func (p *FeishuProvider) BuildAuthURL(state string, scopes []string) string {
	// Default to common scopes if none specified
	if len(scopes) == 0 {
		scopes = defaultScopes
	} else {
		scopes = validateScopes(scopes)
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

// buildTokenFromOIDCResponse 从 OIDC 响应数据构建 oauth.Token。
// ExchangeCode 和 RefreshToken 共用此方法以消除重复逻辑。
func (p *FeishuProvider) buildTokenFromOIDCResponse(data *OIDCResponse) *oauth.Token {
	expiresIn := 7200 // default 2 hours
	if data.ExpiresIn != nil {
		expiresIn = *data.ExpiresIn
	}

	refreshExpiresIn := 2592000 // default 30 days
	if data.RefreshExpiresIn != nil {
		refreshExpiresIn = *data.RefreshExpiresIn
	}

	token := &oauth.Token{
		AccessToken:  *data.AccessToken,
		RefreshToken: "",
		ExpiresAt:    time.Now().Add(time.Duration(expiresIn) * time.Second),
		Scopes:       []string{},
		Raw: map[string]any{
			"token_type":               "Bearer",
			"expires_in":               expiresIn,
			"refresh_token_expires_in": refreshExpiresIn,
		},
	}

	if data.RefreshToken != nil {
		token.RefreshToken = *data.RefreshToken
	}
	if data.TokenType != nil {
		token.Raw["token_type"] = *data.TokenType
	}
	if data.Scope != nil {
		token.Scopes = strings.Fields(*data.Scope)
	}

	return token
}

// OIDCResponse 封装 Feishu OIDC token 响应的公共字段，
// 用于 ExchangeCode 和 RefreshToken 的统一 token 构建。
type OIDCResponse struct {
	AccessToken      *string
	RefreshToken     *string
	TokenType        *string
	Scope            *string
	ExpiresIn        *int
	RefreshExpiresIn *int
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

	resp, err := p.client.Authen.OidcAccessToken.Create(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("create OIDC access token: %w", err)
	}

	if !resp.Success() {
		return nil, fmt.Errorf("feishu API error: %s (code: %d)", resp.Msg, resp.Code)
	}

	if resp.Data.AccessToken == nil {
		return nil, fmt.Errorf("missing access_token in response")
	}

	token := p.buildTokenFromOIDCResponse(&OIDCResponse{
		AccessToken:      resp.Data.AccessToken,
		RefreshToken:     resp.Data.RefreshToken,
		TokenType:        resp.Data.TokenType,
		Scope:            resp.Data.Scope,
		ExpiresIn:        resp.Data.ExpiresIn,
		RefreshExpiresIn: resp.Data.RefreshExpiresIn,
	})

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
		"expires_in":               token.Raw["expires_in"],
		"refresh_token_expires_in": token.Raw["refresh_token_expires_in"],
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
	resp, err := p.client.Tenant.Tenant.Query(ctx, larkcore.WithUserAccessToken(accessToken))
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

	resp, err := p.client.Authen.OidcRefreshAccessToken.Create(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("refresh OIDC access token: %w", err)
	}

	if !resp.Success() {
		return nil, fmt.Errorf("feishu API error: %s (code: %d)", resp.Msg, resp.Code)
	}

	if resp.Data.AccessToken == nil {
		return nil, fmt.Errorf("missing access_token in response")
	}

	token := p.buildTokenFromOIDCResponse(&OIDCResponse{
		AccessToken:      resp.Data.AccessToken,
		RefreshToken:     resp.Data.RefreshToken,
		TokenType:        resp.Data.TokenType,
		Scope:            resp.Data.Scope,
		ExpiresIn:        resp.Data.ExpiresIn,
		RefreshExpiresIn: resp.Data.RefreshExpiresIn,
	})

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
