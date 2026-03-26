package feishu_mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"xbot/llm"
	log "xbot/logger"
	"xbot/tools"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	drivev1 "github.com/larksuite/oapi-sdk-go/v3/service/drive/v1"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// UploadFileTool uploads a file to the user's cloud space.
type UploadFileTool struct {
	FeishuToolBase
	MCP *FeishuMCP
}

func (t *UploadFileTool) Name() string { return "feishu_upload_file" }

func (t *UploadFileTool) Description() string {
	return "Upload a file to the user's Feishu cloud space."
}

func (t *UploadFileTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{
			Name:        "file_path",
			Type:        "string",
			Description: "Path to the file to upload",
			Required:    true,
		},
		{
			Name:        "parent_token",
			Type:        "string",
			Description: "Parent folder token (optional, defaults to root)",
			Required:    false,
		},
		{
			Name:        "file_name",
			Type:        "string",
			Description: "Custom file name (optional, defaults to original filename)",
			Required:    false,
		},
	}
}

func (t *UploadFileTool) Execute(ctx *tools.ToolContext, input string) (*tools.ToolResult, error) {
	var args struct {
		FilePath    string `json:"file_path"`
		ParentToken string `json:"parent_token"`
		FileName    string `json:"file_name"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}

	client, err := t.MCP.GetClient(ctx.Ctx, ctx.Channel, ctx.ChatID)
	if err != nil {
		return nil, err
	}

	// Resolve path (sandbox-aware validation)
	resolvedPath, err := tools.ResolveReadPath(ctx, args.FilePath)
	if err != nil {
		return nil, fmt.Errorf("resolve path: %w", err)
	}

	// Read file via Sandbox
	var data []byte
	if tools.ShouldUseSandbox(ctx) {
		userID := ctx.OriginUserID
		if userID == "" {
			userID = ctx.SenderID
		}
		sandboxCtx, sandboxCancel := tools.SandboxCtx()
		defer sandboxCancel()
		data, err = ctx.Sandbox.ReadFile(sandboxCtx, resolvedPath, userID)
	} else {
		data, err = os.ReadFile(resolvedPath)
	}
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}

	// Determine file name and size
	fileName := args.FileName
	if fileName == "" {
		fileName = filepath.Base(resolvedPath)
	}
	fileSize := len(data)

	// Detect MIME type
	mimeType := mime.TypeByExtension(filepath.Ext(fileName))
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	// Validate MIME type against allowlist
	var allowedMIMETypes = map[string]bool{
		// Common document types
		"application/pdf":    true,
		"application/msword": true,
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document": true,
		"application/vnd.ms-excel": true,
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         true,
		"application/vnd.ms-powerpoint":                                             true,
		"application/vnd.openxmlformats-officedocument.presentationml.presentation": true,
		"application/json":         true,
		"application/xml":          true,
		"text/plain":               true,
		"text/csv":                 true,
		"text/html":                true,
		"text/markdown":            true,
		"application/zip":          true,
		"application/gzip":         true,
		"application/x-tar":        true,
		"application/octet-stream": true,
	}

	// Allow all image/* and text/* types
	if !strings.HasPrefix(mimeType, "image/") && !strings.HasPrefix(mimeType, "text/") && !allowedMIMETypes[mimeType] {
		return nil, fmt.Errorf("file type %q is not allowed for upload", mimeType)
	}

	// Validate data size (limited to 100MB to prevent OOM)
	if len(data) > 100*1024*1024 {
		return nil, fmt.Errorf("file size exceeds 100MB limit")
	}

	// Prepare upload request body
	bodyBuilder := drivev1.NewUploadAllMediaReqBodyBuilder().
		FileName(fileName).
		ParentType("explorer").
		Size(fileSize)

	if args.ParentToken != "" {
		bodyBuilder.ParentNode(args.ParentToken)
	}

	body := bodyBuilder.
		File(bytes.NewReader(data)).
		Build()

	req := drivev1.NewUploadAllMediaReqBuilder().
		Body(body).
		Build()

	resp, err := client.Client().Drive.Media.UploadAll(ctx.Ctx, req,
		larkcore.WithUserAccessToken(client.AccessToken()))

	if err != nil {
		return nil, fmt.Errorf("upload file: %w", err)
	}
	if !resp.Success() {
		return nil, NewAPIError(resp.CodeError)
	}

	fileToken := ""
	if resp.Data.FileToken != nil {
		fileToken = *resp.Data.FileToken
	}

	summary := fmt.Sprintf("File uploaded successfully\nFile Token: %s\nName: %s\nSize: %d bytes\nType: %s",
		fileToken, fileName, fileSize, mimeType)
	return tools.NewResult(summary), nil
}

// ListFilesTool lists files in a folder.
type ListFilesTool struct {
	FeishuToolBase
	MCP *FeishuMCP
}

func (t *ListFilesTool) Name() string { return "feishu_list_files" }

func (t *ListFilesTool) Description() string {
	return "List files and folders in a Feishu cloud space folder."
}

func (t *ListFilesTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{
			Name:        "folder_token",
			Type:        "string",
			Description: "Folder token to list (optional, defaults to root)",
			Required:    false,
		},
	}
}

func (t *ListFilesTool) Execute(ctx *tools.ToolContext, input string) (*tools.ToolResult, error) {
	var args struct {
		FolderToken string `json:"folder_token"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}

	// Since there's no direct "list files" API in the SDK,
	// we return a helpful message for the user
	return tools.NewResultWithTips(
		"File listing requires using specific folder tokens. Navigate to a folder in Feishu and copy the token from the URL.",
		"Use feishu_upload_file to upload files, or feishu_add_permission to share files with others.",
	), nil
}

// AddPermissionTool adds a collaborator to a file or folder.
type AddPermissionTool struct {
	FeishuToolBase
	MCP *FeishuMCP
}

func (t *AddPermissionTool) Name() string { return "feishu_add_permission" }

func (t *AddPermissionTool) Description() string {
	return "Add a collaborator (permission) to a file or folder."
}

func (t *AddPermissionTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{
			Name:        "token",
			Type:        "string",
			Description: "File or folder token",
			Required:    true,
		},
		{
			Name:        "member_type",
			Type:        "string",
			Description: "Member type: user, chat, department, or email",
			Required:    true,
		},
		{
			Name:        "member_id",
			Type:        "string",
			Description: "Member ID (user_id, open_id, email, etc.)",
			Required:    true,
		},
		{
			Name:        "perm",
			Type:        "string",
			Description: "Permission level: view, edit, or full_access",
			Required:    true,
		},
		{
			Name:        "type",
			Type:        "string",
			Description: "Resource type: docx, sheet, file, bitable, wiki, folder, etc.",
			Required:    false,
		},
	}
}

func (t *AddPermissionTool) Execute(ctx *tools.ToolContext, input string) (*tools.ToolResult, error) {
	var args struct {
		Token      string `json:"token"`
		MemberType string `json:"member_type"`
		MemberID   string `json:"member_id"`
		Perm       string `json:"perm"`
		Type       string `json:"type"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}

	client, err := t.MCP.GetClient(ctx.Ctx, ctx.Channel, ctx.ChatID)
	if err != nil {
		return nil, err
	}

	// Auto-detect type from token prefix if not provided
	docType := args.Type
	if docType == "" {
		switch {
		case len(args.Token) >= 6 && strings.HasPrefix(args.Token, "doxcn"):
			docType = "docx"
		case len(args.Token) >= 6 && strings.HasPrefix(args.Token, "wikcn"):
			docType = "wiki"
		case len(args.Token) >= 4 && strings.HasPrefix(args.Token, "basc"):
			docType = "bitable"
		default:
			docType = "file" // default
		}
	}

	// Validate permission level
	if args.Perm != "view" && args.Perm != "edit" && args.Perm != "full_access" {
		return nil, fmt.Errorf("invalid permission level: %s (must be view, edit, or full_access)", args.Perm)
	}

	// Map member types
	memberType := args.MemberType
	switch memberType {
	case "openid":
		memberType = "open_id"
	case "userid":
		memberType = "user_id"
	}

	// Build base member
	baseMember := drivev1.NewBaseMemberBuilder().
		MemberType(memberType).
		MemberId(args.MemberID).
		Perm(args.Perm).
		Build()

	// Build request
	req := drivev1.NewCreatePermissionMemberReqBuilder().
		Token(args.Token).
		Type(docType).
		BaseMember(baseMember).
		Build()

	resp, err := client.Client().Drive.PermissionMember.Create(ctx.Ctx, req,
		larkcore.WithUserAccessToken(client.AccessToken()))
	if err != nil {
		return nil, fmt.Errorf("add permission: %w", err)
	}
	if !resp.Success() {
		return nil, NewAPIError(resp.CodeError)
	}

	summary := fmt.Sprintf("Permission added successfully\nMember: %s (%s)\nPermission: %s\nType: %s",
		args.MemberID, args.MemberType, args.Perm, docType)
	return tools.NewResult(summary), nil
}

// SendFileTool sends a file or image directly to the current Feishu chat.
// Reads file via Sandbox and uploads directly via Feishu API.
type SendFileTool struct {
	FeishuToolBase
	MCP *FeishuMCP
}

func (t *SendFileTool) Name() string { return "feishu_send_file" }

func (t *SendFileTool) Description() string {
	return `Send a file or image to the current Feishu chat. Supports any file type (pdf, doc, etc.) and images (png, jpg, gif).
Parameters (JSON):
  - file_path: string, absolute or relative path to the file/image to send
  - type: string, optional, "file" (default) or "image"
Example: {"file_path": "report.pdf"}
Example: {"file_path": "chart.png", "type": "image"}`
}

func (t *SendFileTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "file_path", Type: "string", Description: "Path to the file or image to send", Required: true},
		{Name: "type", Type: "string", Description: `Message type: "file" (default) or "image"`, Required: false},
	}
}

func (t *SendFileTool) Execute(ctx *tools.ToolContext, input string) (*tools.ToolResult, error) {
	var args struct {
		FilePath string `json:"file_path"`
		Type     string `json:"type"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}
	if args.FilePath == "" {
		return nil, fmt.Errorf("file_path is required")
	}
	if args.Type == "" {
		args.Type = "file"
	}

	// Resolve path
	resolvedPath, err := tools.ResolveReadPath(ctx, args.FilePath)
	if err != nil {
		return nil, fmt.Errorf("resolve path: %w", err)
	}

	// Read file via Sandbox
	var data []byte
	userID := ctx.OriginUserID
	if userID == "" {
		userID = ctx.SenderID
	}
	if tools.ShouldUseSandbox(ctx) {
		sandboxCtx, sandboxCancel := tools.SandboxCtx()
		defer sandboxCancel()
		data, err = ctx.Sandbox.ReadFile(sandboxCtx, resolvedPath, userID)
	} else {
		data, err = os.ReadFile(resolvedPath)
	}
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}

	client := t.MCP.LarkClient()
	if client == nil {
		return nil, fmt.Errorf("feishu client not available")
	}

	fileName := filepath.Base(resolvedPath)
	chatID := ctx.ChatID
	if chatID == "" {
		return nil, fmt.Errorf("not in a chat context")
	}

	receiveIDType := "chat_id"
	if !strings.HasPrefix(chatID, "oc_") {
		receiveIDType = "open_id"
	}

	switch args.Type {
	case "image":
		imageKey, err := t.uploadImage(client, data)
		if err != nil {
			return nil, fmt.Errorf("upload image: %w", err)
		}
		imgContent, _ := json.Marshal(map[string]string{"image_key": imageKey})
		req := larkim.NewCreateMessageReqBuilder().
			ReceiveIdType(receiveIDType).
			Body(larkim.NewCreateMessageReqBodyBuilder().
				ReceiveId(chatID).
				MsgType("image").
				Content(string(imgContent)).
				Build()).
			Build()
		resp, err := client.Im.Message.Create(context.Background(), req)
		if err != nil {
			return nil, fmt.Errorf("send image message: %w", err)
		}
		if !resp.Success() {
			return nil, fmt.Errorf("feishu API error: code=%d, msg=%s", resp.Code, resp.Msg)
		}
		log.WithFields(log.Fields{"chat_id": chatID, "image_key": imageKey}).Info("Image sent via direct API")

	default: // file
		fileKey, err := t.uploadFile(client, data, fileName)
		if err != nil {
			return nil, fmt.Errorf("upload file: %w", err)
		}
		fileContent, _ := json.Marshal(map[string]string{"file_key": fileKey, "file_name": fileName})
		req := larkim.NewCreateMessageReqBuilder().
			ReceiveIdType(receiveIDType).
			Body(larkim.NewCreateMessageReqBodyBuilder().
				ReceiveId(chatID).
				MsgType("file").
				Content(string(fileContent)).
				Build()).
			Build()
		resp, err := client.Im.Message.Create(context.Background(), req)
		if err != nil {
			return nil, fmt.Errorf("send file message: %w", err)
		}
		if !resp.Success() {
			return nil, fmt.Errorf("feishu API error: code=%d, msg=%s", resp.Code, resp.Msg)
		}
		log.WithFields(log.Fields{"chat_id": chatID, "file_key": fileKey}).Info("File sent via direct API")
	}

	return tools.NewResult(fmt.Sprintf("File sent: %s (type: %s, size: %d bytes)", fileName, args.Type, len(data))), nil
}

// uploadImage uploads an image to Feishu, returns image_key.
func (t *SendFileTool) uploadImage(client *lark.Client, data []byte) (string, error) {
	req := larkim.NewCreateImageReqBuilder().
		Body(&larkim.CreateImageReqBody{
			ImageType: ptrString("message"),
			Image:     bytes.NewReader(data),
		}).
		Build()
	resp, err := client.Im.Image.Create(context.Background(), req)
	if err != nil {
		return "", fmt.Errorf("upload image API: %w", err)
	}
	if !resp.Success() {
		return "", fmt.Errorf("upload image error: code=%d, msg=%s", resp.Code, resp.Msg)
	}
	return *resp.Data.ImageKey, nil
}

// uploadFile uploads a file to Feishu, returns file_key.
func (t *SendFileTool) uploadFile(client *lark.Client, data []byte, fileName string) (string, error) {
	fileType := detectFileType(fileName)
	req := larkim.NewCreateFileReqBuilder().
		Body(&larkim.CreateFileReqBody{
			FileType: &fileType,
			FileName: &fileName,
			File:     bytes.NewReader(data),
		}).
		Build()
	resp, err := client.Im.File.Create(context.Background(), req)
	if err != nil {
		return "", fmt.Errorf("upload file API: %w", err)
	}
	if !resp.Success() {
		return "", fmt.Errorf("upload file error: code=%d, msg=%s", resp.Code, resp.Msg)
	}
	return *resp.Data.FileKey, nil
}

// detectFileType detects Feishu file type from extension.
func detectFileType(fileName string) string {
	ext := strings.ToLower(filepath.Ext(fileName))
	switch ext {
	case ".mp4":
		return "mp4"
	case ".pdf":
		return "pdf"
	case ".doc", ".docx":
		return "doc"
	case ".xls", ".xlsx":
		return "xls"
	case ".ppt", ".pptx":
		return "ppt"
	case ".opus":
		return "opus"
	default:
		return "stream"
	}
}

func ptrString(s string) *string { return &s }
