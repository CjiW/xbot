package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"
	"xbot/llm"
	log "xbot/logger"
)

// validIDPattern validates that message_id and file_key only contain safe characters.
var validIDPattern = regexp.MustCompile(`^[\w.\-]+$`)

// DownloadFileTool downloads files/images sent by users in chat.
// Supports: feishu (via Message Resource API) and web/OSS (via URL).
type DownloadFileTool struct {
	appID     string
	appSecret string
}

// NewDownloadFileTool 创建下载文件工具（注入飞书凭证）
func NewDownloadFileTool(appID, appSecret string) *DownloadFileTool {
	return &DownloadFileTool{
		appID:     appID,
		appSecret: appSecret,
	}
}

func (t *DownloadFileTool) Name() string {
	return "DownloadFile"
}

func (t *DownloadFileTool) Description() string {
	return `Download files/images sent by users in chat.
Supports two sources:
  1. Web/OSS files: use "url" parameter to download from a signed URL
  2. Feishu files: use "message_id" and "file_key" parameters
Activate when: user sends a file in chat and you need to save it locally for processing, or user asks to download/save a file from the conversation.
Parameters (JSON):
  - url: string, the file URL to download (for web/OSS files, from "访问URL:" in message content)
  - output_path: string, where to save the file (relative to working directory or absolute)
  - message_id: string, the Feishu message ID (for Feishu files only)
  - file_key: string, the file_key or image_key (for Feishu files only)
  - type: string, optional, "file" (default) or "image" (for Feishu files only)
Example (web): {"url": "https://cdn.example.com/uploads/web-1/xxx.jpg?token=xxx", "output_path": "downloads/photo.jpg"}
Example (feishu): {"message_id": "om_xxx", "file_key": "file_v3_xxx", "output_path": "downloads/report.pdf"}`
}

func (t *DownloadFileTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "url", Type: "string", Description: "The file URL to download (for web/OSS files)", Required: false},
		{Name: "output_path", Type: "string", Description: "Where to save the file (relative to working directory or absolute)", Required: true},
		{Name: "message_id", Type: "string", Description: "The Feishu message ID (for Feishu files only)", Required: false},
		{Name: "file_key", Type: "string", Description: "The file_key or image_key (for Feishu files only)", Required: false},
		{Name: "type", Type: "string", Description: "Resource type: \"file\" (default) or \"image\" (Feishu only)", Required: false},
	}
}

func (t *DownloadFileTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	params, err := parseToolArgs[struct {
		URL        string `json:"url"`
		MessageID  string `json:"message_id"`
		FileKey    string `json:"file_key"`
		OutputPath string `json:"output_path"`
		Type       string `json:"type"`
	}](input)
	if err != nil {
		return nil, err
	}

	if params.OutputPath == "" {
		return nil, fmt.Errorf("output_path is required")
	}

	// Resolve output path (sandbox-aware)
	outputPath, err := ResolveWritePath(ctx, params.OutputPath)
	if err != nil {
		return nil, err
	}

	displayPath := outputPath

	// Auto-detect source: URL takes priority
	if params.URL != "" {
		return t.downloadFromURL(ctx, params.URL, outputPath, displayPath)
	}

	// Feishu fallback
	if params.MessageID != "" && params.FileKey != "" {
		if !validIDPattern.MatchString(params.MessageID) {
			return nil, fmt.Errorf("invalid message_id format")
		}
		if !validIDPattern.MatchString(params.FileKey) {
			return nil, fmt.Errorf("invalid file_key format")
		}
		if params.Type == "" {
			params.Type = "file"
		}
		switch ctx.Channel {
		case "feishu":
			return t.downloadFeishu(ctx, params.MessageID, params.FileKey, params.Type, outputPath, displayPath)
		default:
			return nil, fmt.Errorf("feishu file download not supported for channel: %s", ctx.Channel)
		}
	}

	return nil, fmt.Errorf("must provide url (for web/OSS files) or both message_id and file_key (for Feishu files)")
}

// downloadFromURL downloads a file from a URL (used for web/OSS files).
// When a sandbox is active, delegates the download to the sandbox (runner-side download)
// to avoid routing large files through the server.
func (t *DownloadFileTool) downloadFromURL(ctx *ToolContext, fileURL, outputPath, displayPath string) (*ToolResult, error) {
	if shouldUseSandbox(ctx) {
		return t.downloadFromURLViaSandbox(ctx, fileURL, outputPath, displayPath)
	}
	return t.downloadFromURLDirect(ctx, fileURL, outputPath, displayPath)
}

// downloadFromURLViaSandbox delegates the download to the sandbox (runner/container).
// The sandbox downloads the file directly, avoiding the server as a proxy.
func (t *DownloadFileTool) downloadFromURLViaSandbox(ctx *ToolContext, fileURL, outputPath, displayPath string) (*ToolResult, error) {
	userID := ctx.OriginUserID
	if userID == "" {
		userID = ctx.SenderID
	}
	sandboxCtx, sandboxCancel := SandboxCtx()
	defer sandboxCancel()

	if err := ctx.Sandbox.DownloadFile(sandboxCtx, fileURL, outputPath, userID); err != nil {
		return nil, fmt.Errorf("sandbox download: %w", err)
	}

	log.WithFields(log.Fields{
		"url":         fileURL,
		"output_path": outputPath,
		"user_id":     userID,
		"mode":        "sandbox",
	}).Info("File downloaded from URL (via sandbox)")

	return NewResult(fmt.Sprintf("Downloaded: %s (sandbox-side)", displayPath)), nil
}

// downloadFromURLDirect downloads a file on the server side (no sandbox).
func (t *DownloadFileTool) downloadFromURLDirect(ctx *ToolContext, fileURL, outputPath, displayPath string) (*ToolResult, error) {
	req, err := http.NewRequest("GET", fileURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := downloadHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("download failed: HTTP %d, body: %s", resp.StatusCode, string(body))
	}

	// Read response body
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDownloadSize))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if len(data) >= maxDownloadSize {
		return nil, fmt.Errorf("downloaded file exceeds maximum allowed size (100MB)")
	}

	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return nil, fmt.Errorf("create output directory: %w", err)
	}
	if err := os.WriteFile(outputPath, data, 0o644); err != nil {
		return nil, fmt.Errorf("write file: %w", err)
	}

	log.WithFields(log.Fields{
		"url":         fileURL,
		"output_path": outputPath,
		"size":        len(data),
		"mode":        "direct",
	}).Info("File downloaded from URL")

	return NewResult(fmt.Sprintf("Downloaded: %s (%d bytes)", displayPath, len(data))), nil
}

// maxDownloadSize is the maximum allowed download size (100MB).
const maxDownloadSize = 100 * 1024 * 1024

// downloadHTTPClient is a dedicated HTTP client with timeout for file downloads.
var downloadHTTPClient = &http.Client{
	Timeout: 60 * time.Second,
}

// tokenHTTPClient is a dedicated HTTP client with timeout for token requests.
var tokenHTTPClient = &http.Client{
	Timeout: 30 * time.Second,
}

// downloadFeishu downloads a file/image from Feishu via Message Resource API.
func (t *DownloadFileTool) downloadFeishu(ctx *ToolContext, messageID, fileKey, fileType, outputPath, displayPath string) (*ToolResult, error) {
	token, err := t.getFeishuTenantToken()
	if err != nil {
		return nil, fmt.Errorf("get tenant token: %w", err)
	}

	apiURL := fmt.Sprintf("https://open.feishu.cn/open-apis/im/v1/messages/%s/resources/%s?type=%s",
		messageID, fileKey, fileType)

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := downloadHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("feishu API error: HTTP %d, body: %s", resp.StatusCode, string(body))
	}

	// Read response body
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDownloadSize))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if len(data) >= maxDownloadSize {
		return nil, fmt.Errorf("downloaded file exceeds maximum allowed size (100MB)")
	}

	// Write to output path (sandbox-aware)
	if shouldUseSandbox(ctx) {
		userID := ctx.OriginUserID
		if userID == "" {
			userID = ctx.SenderID
		}
		sandboxCtx, sandboxCancel := SandboxCtx()
		defer sandboxCancel()
		if err := ctx.Sandbox.MkdirAll(sandboxCtx, filepath.Dir(outputPath), 0o755, userID); err != nil {
			return nil, fmt.Errorf("create output directory: %w", err)
		}
		if err := ctx.Sandbox.WriteFile(sandboxCtx, outputPath, data, 0o644, userID); err != nil {
			return nil, fmt.Errorf("write file: %w", err)
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
			return nil, fmt.Errorf("create output directory: %w", err)
		}
		if err := os.WriteFile(outputPath, data, 0o644); err != nil {
			return nil, fmt.Errorf("write file: %w", err)
		}
	}

	log.WithFields(log.Fields{
		"message_id":  messageID,
		"file_key":    fileKey,
		"output_path": outputPath,
		"size":        len(data),
	}).Info("File downloaded from Feishu")

	return NewResult(fmt.Sprintf("Downloaded: %s (%d bytes)", displayPath, len(data))), nil
}

// getFeishuTenantToken obtains a tenant_access_token using app credentials from environment.
func (t *DownloadFileTool) getFeishuTenantToken() (string, error) {
	appID := t.appID
	appSecret := t.appSecret
	if appID == "" || appSecret == "" {
		return "", fmt.Errorf("FEISHU_APP_ID and FEISHU_APP_SECRET must be configured")
	}

	reqBody, _ := json.Marshal(map[string]string{
		"app_id":     appID,
		"app_secret": appSecret,
	})

	resp, err := tokenHTTPClient.Post(
		"https://open.feishu.cn/open-apis/auth/v3/tenant_access_token/internal",
		"application/json; charset=utf-8",
		bytes.NewReader(reqBody),
	)
	if err != nil {
		return "", fmt.Errorf("request tenant token: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Code              int    `json:"code"`
		Msg               string `json:"msg"`
		TenantAccessToken string `json:"tenant_access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode token response: %w", err)
	}
	if result.Code != 0 {
		return "", fmt.Errorf("tenant token API error: code=%d, msg=%s", result.Code, result.Msg)
	}
	if result.TenantAccessToken == "" {
		return "", fmt.Errorf("empty tenant_access_token in response")
	}

	return result.TenantAccessToken, nil
}
