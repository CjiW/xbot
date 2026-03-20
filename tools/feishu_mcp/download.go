package feishu_mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"xbot/llm"
	"xbot/tools"

	log "xbot/logger"
)

// DownloadFileTool downloads files/images sent by users in Feishu chat via Message Resource API.
type DownloadFileTool struct {
	FeishuToolBase
	MCP *FeishuMCP
}

func (t *DownloadFileTool) Name() string { return "feishu_download_file" }

func (t *DownloadFileTool) Description() string {
	return `Download files/images sent by users in Feishu chat.
Activate when: (1) user sends a file <file .../> or image <image .../> in chat, (2) user asks to download/save a file from the conversation.
Parameters (JSON):
  - message_id: string, the Feishu message ID containing the resource (from XML tag attribute)
  - file_key: string, the file_key or image_key to download (from XML tag attribute)
  - output_path: string, where to save the file (relative to working directory or absolute)
  - type: string, optional, "file" (default) or "image"
Example: {"message_id": "om_xxx", "file_key": "file_v3_xxx", "output_path": "downloads/report.pdf"}
Example: {"message_id": "om_xxx", "file_key": "img_v3_xxx", "output_path": "downloads/photo.png", "type": "image"}`
}

func (t *DownloadFileTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "message_id", Type: "string", Description: "The Feishu message ID containing the resource", Required: true},
		{Name: "file_key", Type: "string", Description: "The file_key or image_key to download", Required: true},
		{Name: "output_path", Type: "string", Description: "Where to save the file (relative to working directory or absolute)", Required: true},
		{Name: "type", Type: "string", Description: "Resource type: \"file\" (default) or \"image\"", Required: false},
	}
}

func (t *DownloadFileTool) Execute(ctx *tools.ToolContext, input string) (*tools.ToolResult, error) {
	var args struct {
		MessageID  string `json:"message_id"`
		FileKey    string `json:"file_key"`
		OutputPath string `json:"output_path"`
		Type       string `json:"type"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return nil, fmt.Errorf("parse arguments: %w", err)
	}

	if args.MessageID == "" {
		return nil, fmt.Errorf("message_id is required")
	}
	if args.FileKey == "" {
		return nil, fmt.Errorf("file_key is required")
	}
	if args.OutputPath == "" {
		return nil, fmt.Errorf("output_path is required")
	}
	if args.Type == "" {
		args.Type = "file"
	}

	// Resolve output path with sandbox path guard
	outputPath, err := tools.ResolveWritePath(ctx, args.OutputPath)
	if err != nil {
		return nil, err
	}

	// Built-in tools run on host; translate sandbox path → host path
	hostPath := tools.SandboxToHostPath(ctx, outputPath)

	// For display: show sandbox-visible path
	displayPath := tools.HostToSandboxPath(ctx, hostPath)

	token, err := t.getTenantToken()
	if err != nil {
		return nil, fmt.Errorf("get tenant token: %w", err)
	}

	apiURL := fmt.Sprintf("https://open.feishu.cn/open-apis/im/v1/messages/%s/resources/%s?type=%s",
		args.MessageID, args.FileKey, args.Type)

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("feishu API error: HTTP %d, body: %s", resp.StatusCode, string(body))
	}

	// Ensure output directory exists
	if err := os.MkdirAll(filepath.Dir(hostPath), 0755); err != nil {
		return nil, fmt.Errorf("create output directory: %w", err)
	}

	outFile, err := os.Create(hostPath)
	if err != nil {
		return nil, fmt.Errorf("create output file: %w", err)
	}
	defer outFile.Close()

	written, err := io.Copy(outFile, resp.Body)
	if err != nil {
		return nil, fmt.Errorf("write file: %w", err)
	}

	log.WithFields(log.Fields{
		"message_id":  args.MessageID,
		"file_key":    args.FileKey,
		"output_path": outputPath,
		"size":        written,
	}).Info("File downloaded from Feishu via MCP tool")

	return tools.NewResult(fmt.Sprintf("Downloaded: %s (%d bytes)", displayPath, written)), nil
}

// getTenantToken obtains a tenant_access_token using app credentials from environment.
func (t *DownloadFileTool) getTenantToken() (string, error) {
	appID := os.Getenv("FEISHU_APP_ID")
	appSecret := os.Getenv("FEISHU_APP_SECRET")
	if appID == "" || appSecret == "" {
		return "", fmt.Errorf("FEISHU_APP_ID and FEISHU_APP_SECRET must be set")
	}

	reqBody, _ := json.Marshal(map[string]string{
		"app_id":     appID,
		"app_secret": appSecret,
	})

	resp, err := http.Post(
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
