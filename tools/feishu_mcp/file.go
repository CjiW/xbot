package feishu_mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"xbot/llm"
	"xbot/tools"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	drivev1 "github.com/larksuite/oapi-sdk-go/v3/service/drive/v1"
)

// UploadFileTool uploads a file to the user's cloud space.
type UploadFileTool struct {
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

	// Open the file
	file, err := os.Open(args.FilePath)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	defer file.Close()

	// Get file info
	fileInfo, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("get file info: %w", err)
	}

	// Determine file name and size
	fileName := args.FileName
	if fileName == "" {
		fileName = filepath.Base(args.FilePath)
	}
	fileSize := int(fileInfo.Size())

	// Detect MIME type
	mimeType := mime.TypeByExtension(filepath.Ext(fileName))
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	// Read file content
	fileContent, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
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
		File(bytes.NewReader(fileContent)).
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
