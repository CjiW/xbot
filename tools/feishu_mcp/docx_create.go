package feishu_mcp

import (
	"encoding/json"
	"fmt"

	"xbot/llm"
	"xbot/tools"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	docxv1 "github.com/larksuite/oapi-sdk-go/v3/service/docx/v1"
)

// DocxCreateTool creates a new document.
type DocxCreateTool struct {
	FeishuToolBase
	MCP *FeishuMCP
}

func (t *DocxCreateTool) Name() string { return "feishu_docx_create" }

func (t *DocxCreateTool) Description() string {
	return "Create a new document in the user's cloud space."
}

func (t *DocxCreateTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{
			Name:        "title",
			Type:        "string",
			Description: "Document title",
			Required:    true,
		},
		{
			Name:        "folder_token",
			Type:        "string",
			Description: "Parent folder token (optional, defaults to root)",
			Required:    false,
		},
	}
}

func (t *DocxCreateTool) Execute(ctx *tools.ToolContext, input string) (*tools.ToolResult, error) {
	var args struct {
		Title       string `json:"title"`
		FolderToken string `json:"folder_token"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}

	client, err := t.MCP.GetClient(ctx.Ctx, ctx.Channel, ctx.ChatID)
	if err != nil {
		return nil, err
	}

	// Build request body
	bodyBuilder := docxv1.NewCreateDocumentReqBodyBuilder().
		Title(args.Title)

	if args.FolderToken != "" {
		bodyBuilder.FolderToken(args.FolderToken)
	}

	// Create document
	req := docxv1.NewCreateDocumentReqBuilder().
		Body(bodyBuilder.Build()).
		Build()

	resp, err := client.Client().Docx.Document.Create(ctx.Ctx, req,
		larkcore.WithUserAccessToken(client.AccessToken()))
	if err != nil {
		return nil, fmt.Errorf("create document: %w", err)
	}
	if !resp.Success() {
		return nil, NewAPIError(resp.CodeError)
	}

	documentID := ""
	if resp.Data.Document != nil && resp.Data.Document.DocumentId != nil {
		documentID = *resp.Data.Document.DocumentId
	}

	summary := fmt.Sprintf("Document created with ID: %s", documentID)
	detail, _ := json.MarshalIndent(resp.Data.Document, "", "  ")
	return tools.NewResultWithDetail(summary, string(detail)), nil
}
