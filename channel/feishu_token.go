package channel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	log "xbot/logger"
)

const feishuTokenURL = "https://open.feishu.cn/open-apis/authen/v2/oauth/token"

// FeishuTokenConfig 飞书 UAT 刷新配置
type FeishuTokenConfig struct {
	AppID         string
	AppSecret     string
	UAT           string
	RefreshToken  string
	MCPConfigPath string // mcp.json 文件路径
	TokenFilePath string // token 持久化文件路径（如 .xbot/feishu_tokens.json）
}

// FeishuTokenRefresher 自动刷新飞书 user_access_token
type FeishuTokenRefresher struct {
	appID         string
	appSecret     string
	uat           string
	refreshToken  string
	mcpConfigPath string
	tokenFilePath string
	mu            sync.Mutex
	onRefresh     func(newUAT string)
}

// feishuTokenFile token 文件结构
type feishuTokenFile struct {
	UserAccessToken string `json:"user_access_token"`
	RefreshToken    string `json:"refresh_token"`
}

type tokenRequest struct {
	GrantType    string `json:"grant_type"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	RefreshToken string `json:"refresh_token"`
}

type tokenResponse struct {
	Code                  int    `json:"code"`
	AccessToken           string `json:"access_token"`
	ExpiresIn             int    `json:"expires_in"`
	RefreshToken          string `json:"refresh_token"`
	RefreshTokenExpiresIn int    `json:"refresh_token_expires_in"`
	TokenType             string `json:"token_type"`
	Scope                 string `json:"scope"`
	Error                 string `json:"error"`
	ErrorDescription      string `json:"error_description"`
}

// LoadFeishuTokens 从 token 文件加载已持久化的 token。
// 返回 (uat, refreshToken, err)，文件不存在时返回空字符串和 nil。
func LoadFeishuTokens(path string) (string, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", nil
		}
		return "", "", err
	}
	var tf feishuTokenFile
	if err := json.Unmarshal(data, &tf); err != nil {
		return "", "", err
	}
	return tf.UserAccessToken, tf.RefreshToken, nil
}

func NewFeishuTokenRefresher(cfg FeishuTokenConfig) *FeishuTokenRefresher {
	return &FeishuTokenRefresher{
		appID:         cfg.AppID,
		appSecret:     cfg.AppSecret,
		uat:           cfg.UAT,
		refreshToken:  cfg.RefreshToken,
		mcpConfigPath: cfg.MCPConfigPath,
		tokenFilePath: cfg.TokenFilePath,
	}
}

// SetOnRefresh sets a callback invoked after each successful token refresh.
// Typically used to reconnect MCP servers that depend on UAT.
func (r *FeishuTokenRefresher) SetOnRefresh(fn func(newUAT string)) {
	r.onRefresh = fn
}

// Start runs the token refresh loop: immediate refresh + every interval.
// Blocks until ctx is cancelled.
func (r *FeishuTokenRefresher) Start(ctx context.Context) {
	log.Info("Feishu UAT refresher started")

	r.doRefresh()

	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info("Feishu UAT refresher stopped")
			return
		case <-ticker.C:
			r.doRefresh()
		}
	}
}

func (r *FeishuTokenRefresher) doRefresh() {
	r.mu.Lock()
	defer r.mu.Unlock()

	reqBody := tokenRequest{
		GrantType:    "refresh_token",
		ClientID:     r.appID,
		ClientSecret: r.appSecret,
		RefreshToken: r.refreshToken,
	}
	body, _ := json.Marshal(reqBody)

	log.WithFields(log.Fields{
		"url":           feishuTokenURL,
		"grant_type":    reqBody.GrantType,
		"client_id":     reqBody.ClientID,
		"refresh_token": reqBody.RefreshToken,
	}).Info("Feishu UAT refresh: sending request")

	resp, err := http.Post(feishuTokenURL, "application/json; charset=utf-8", bytes.NewReader(body))
	if err != nil {
		log.WithError(err).Error("Feishu UAT refresh: HTTP request failed")
		return
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		log.WithError(err).Error("Feishu UAT refresh: read response body failed")
		return
	}

	log.WithFields(log.Fields{
		"status": resp.StatusCode,
		"body":   string(respBody),
	}).Debug("Feishu UAT refresh: response received")

	var result tokenResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		log.WithError(err).Error("Feishu UAT refresh: decode response failed")
		return
	}

	if result.Code != 0 {
		log.WithFields(log.Fields{
			"code":  result.Code,
			"error": result.Error,
			"desc":  result.ErrorDescription,
		}).Error("Feishu UAT refresh: API returned error")
		return
	}

	r.uat = result.AccessToken
	r.refreshToken = result.RefreshToken

	log.WithFields(log.Fields{
		"expires_in":               result.ExpiresIn,
		"refresh_token_expires_in": result.RefreshTokenExpiresIn,
		"access_token":             r.uat,
		"refresh_token":            r.refreshToken,
	}).Info("Feishu UAT refreshed successfully")

	if err := r.saveTokenFile(); err != nil {
		log.WithError(err).Error("Feishu UAT refresh: failed to save token file")
	}

	if err := r.updateMCPConfig(); err != nil {
		log.WithError(err).Error("Feishu UAT refresh: failed to update mcp.json")
	}

	os.Setenv("FEISHU_UAT", r.uat)
	os.Setenv("FEISHU_REFRESH_TOKEN", r.refreshToken)

	if r.onRefresh != nil {
		r.onRefresh(r.uat)
	}
}

func (r *FeishuTokenRefresher) updateMCPConfig() error {
	data, err := os.ReadFile(r.mcpConfigPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read mcp.json: %w", err)
	}

	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("parse mcp.json: %w", err)
	}

	servers, ok := config["mcpServers"].(map[string]any)
	if !ok {
		return nil
	}

	larkMCP, ok := servers["lark-mcp"].(map[string]any)
	if !ok {
		return nil
	}

	args, ok := larkMCP["args"].([]any)
	if !ok {
		return nil
	}

	for i, arg := range args {
		if s, ok := arg.(string); ok && s == "-u" && i+1 < len(args) {
			args[i+1] = r.uat
			break
		}
	}

	larkMCP["args"] = args

	output, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal mcp.json: %w", err)
	}

	return os.WriteFile(r.mcpConfigPath, append(output, '\n'), 0644)
}

func (r *FeishuTokenRefresher) saveTokenFile() error {
	if r.tokenFilePath == "" {
		return nil
	}

	dir := filepath.Dir(r.tokenFilePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create token dir: %w", err)
	}

	data, err := json.MarshalIndent(feishuTokenFile{
		UserAccessToken: r.uat,
		RefreshToken:    r.refreshToken,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal tokens: %w", err)
	}

	return os.WriteFile(r.tokenFilePath, append(data, '\n'), 0600)
}
