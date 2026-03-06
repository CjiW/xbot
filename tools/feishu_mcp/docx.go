package feishu_mcp

import (
	"encoding/json"
	"fmt"
	"strconv"

	"xbot/llm"
	"xbot/tools"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkdocs "github.com/larksuite/oapi-sdk-go/v3/service/docs/v1"
	docxv1 "github.com/larksuite/oapi-sdk-go/v3/service/docx/v1"
)

// DocxGetContentTool gets document content in Markdown format.
type DocxGetContentTool struct {
	MCP *FeishuMCP
}

func (t *DocxGetContentTool) Name() string { return "feishu_docx_get_content" }

func (t *DocxGetContentTool) Description() string {
	return "Get document content in Markdown format."
}

func (t *DocxGetContentTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{
			Name:        "document_id",
			Type:        "string",
			Description: "Document ID (e.g., doxcnXXXXX)",
			Required:    true,
		},
	}
}

func (t *DocxGetContentTool) Execute(ctx *tools.ToolContext, input string) (*tools.ToolResult, error) {
	var args struct {
		DocumentID string `json:"document_id"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}

	client, err := t.MCP.GetClient(ctx.Ctx, ctx.Channel, ctx.ChatID)
	if err != nil {
		return nil, err
	}
	req := larkdocs.NewGetContentReqBuilder().
		DocToken(args.DocumentID).
		DocType(`docx`).
		ContentType(`markdown`).
		Build()

	resp, err := client.Client().Docs.V1.Content.Get(ctx.Ctx, req,
		larkcore.WithUserAccessToken(client.AccessToken()))
	if err != nil {
		return nil, fmt.Errorf("get document: %w", err)
	}
	if !resp.Success() {
		return nil, NewAPIError(resp.CodeError)
	}

	// Convert blocks to Markdown
	markdown := *resp.Data.Content

	// Truncate if too long (limit to ~10k chars for LLM context)
	const maxLen = 10000
	if len(markdown) > maxLen {
		markdown = markdown[:maxLen] + "\n\n... (content truncated)"
	}

	return tools.NewResultWithTips(
		fmt.Sprintf("Document content:\n\n%s", markdown),
		"Some special nodes e.g. mermaid gragh may disappear in markdown. You can use `feishu_docx_get_block` to get detailed block content for those nodes.",
	), nil
}

type DocxGetBlockTool struct {
	MCP *FeishuMCP
}

func (t *DocxGetBlockTool) Name() string { return "feishu_docx_get_block" }

func (t *DocxGetBlockTool) Description() string {
	return "Get a specific document block's content and metadata by block ID."
}

func (t *DocxGetBlockTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{
			Name:        "document_id",
			Type:        "string",
			Description: "Document ID (e.g., doxcnXXXXX)",
			Required:    true,
		},
		{
			Name:        "block_id",
			Type:        "string",
			Description: "Block ID to retrieve (use feishu_docx_list_blocks to find block IDs)",
			Required:    true,
		},
	}
}

func (t *DocxGetBlockTool) Execute(ctx *tools.ToolContext, input string) (*tools.ToolResult, error) {
	var args struct {
		DocumentID string `json:"document_id"`
		BlockID    string `json:"block_id"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}

	client, err := t.MCP.GetClient(ctx.Ctx, ctx.Channel, ctx.ChatID)
	if err != nil {
		return nil, err
	}

	req := docxv1.NewGetDocumentBlockReqBuilder().
		DocumentId(args.DocumentID).
		BlockId(args.BlockID).
		Build()

	resp, err := client.Client().Docx.DocumentBlock.Get(ctx.Ctx, req,
		larkcore.WithUserAccessToken(client.AccessToken()))
	if err != nil {
		return nil, fmt.Errorf("get document block: %w", err)
	}
	if !resp.Success() {
		return nil, NewAPIError(resp.CodeError)
	}

	block := resp.Data.Block
	detail, _ := json.MarshalIndent(block, "", "  ")
	return tools.NewResultWithDetail("Document block content and metadata", string(detail)), nil
}

// DocxListBlocksTool lists document block structure.
type DocxListBlocksTool struct {
	MCP *FeishuMCP
}

func (t *DocxListBlocksTool) Name() string { return "feishu_docx_list_blocks" }

func (t *DocxListBlocksTool) Description() string {
	return "List the block structure of a document. Shows the hierarchical structure."
}

func (t *DocxListBlocksTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{
			Name:        "document_id",
			Type:        "string",
			Description: "Document ID (e.g., doxcnXXXXX)",
			Required:    true,
		},
		{
			Name:        "offset",
			Type:        "integer",
			Description: "Offset for pagination (default 0)",
			Required:    false,
		},
		{
			Name:        "limit",
			Type:        "integer",
			Description: "Limit for pagination (max 50, default 50)",
			Required:    false,
		},
	}
}

func (t *DocxListBlocksTool) Execute(ctx *tools.ToolContext, input string) (*tools.ToolResult, error) {
	var args struct {
		DocumentID string `json:"document_id"`
		Offset     int    `json:"offset"`
		Limit      int    `json:"limit"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}

	client, err := t.MCP.GetClient(ctx.Ctx, ctx.Channel, ctx.ChatID)
	if err != nil {
		return nil, err
	}

	// Fetch all pages of blocks
	var allItems []*docxv1.Block
	pageToken := ""
	for {
		reqBuilder := docxv1.NewListDocumentBlockReqBuilder().
			DocumentId(args.DocumentID).
			PageSize(500)
		if pageToken != "" {
			reqBuilder.PageToken(pageToken)
		}
		req := reqBuilder.Build()

		resp, err := client.Client().Docx.DocumentBlock.List(ctx.Ctx, req,
			larkcore.WithUserAccessToken(client.AccessToken()))
		if err != nil {
			return nil, fmt.Errorf("list document blocks: %w", err)
		}
		if !resp.Success() {
			return nil, NewAPIError(resp.CodeError)
		}

		allItems = append(allItems, resp.Data.Items...)

		if resp.Data.HasMore == nil || !*resp.Data.HasMore {
			break
		}
		if resp.Data.PageToken != nil {
			pageToken = *resp.Data.PageToken
		} else {
			break
		}
	}

	if len(allItems) == 0 {
		return tools.NewResultWithTips("Document is empty", "Use feishu_docx_insert_block to add content to this document."), nil
	}

	// Build block summary
	var blocks []map[string]any
	childMap := make(map[string]struct{})
	i := 0
	for _, block := range allItems {
		// 神秘飞书把文档也当作block返回
		if block.BlockId == nil || *block.BlockId == args.DocumentID {
			continue
		}
		trackChildren(block, childMap)
		// check if is child
		if _, ok := childMap[*block.BlockId]; ok {
			continue
		}
		blockType := 0
		if block.BlockType != nil {
			blockType = *block.BlockType
		}
		parentId := ""
		if block.ParentId != nil {
			parentId = *block.ParentId
		}
		blockId := ""
		if block.BlockId != nil {
			blockId = *block.BlockId
		}

		if i >= args.Offset && (args.Limit <= 0 || i < args.Offset+args.Limit) {
			blocks = append(blocks, map[string]any{
				"block_id":        blockId,
				"block_type":      blockType,
				"block_type_desc": GetBlockTypeDesc(blockType),
				"block_type_name": GetBlockTypeName(blockType),
				"content_summary": GetBlockText(block),
				"parent_id":       parentId,
				"index":           i, // Position among siblings
			})
		}
		i++
	}

	summary := fmt.Sprintf("Document has %d block(s)", i)
	detail, _ := json.MarshalIndent(blocks, "", "  ")
	return tools.NewResultWithDetail(summary, string(detail)).WithTips("If you want to know what's in a non-text block, you may use `feishu_docx_get_block`"), nil
}

func trackChildren(block *docxv1.Block, childMap map[string]struct{}) {
	if block.Children != nil {
		for _, child := range block.Children {
			childMap[child] = struct{}{}
		}
	}
}

// DocxCreateTool creates a new document.
type DocxCreateTool struct {
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

// DocxInsertBlockTool writes Markdown content to a document using Feishu's native Markdown API.
type DocxInsertBlockTool struct {
	MCP *FeishuMCP
}

func (t *DocxInsertBlockTool) Name() string { return "feishu_docx_insert_block" }

func (t *DocxInsertBlockTool) Description() string {
	return "Insert content into a document at a specific block index. Content is in Markdown format and will be converted to native blocks. Use feishu_docx_list_blocks to find block indices."
}

func (t *DocxInsertBlockTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{
			Name:        "document_id",
			Type:        "string",
			Description: "Document ID (e.g., doxcnXXXXX)",
			Required:    true,
		},
		{
			Name:        "content",
			Type:        "string",
			Description: "Markdown content to write to the document",
			Required:    true,
		},
		{
			Name:        "insert_index",
			Type:        "integer",
			Description: "Index to insert the content at (0-based)",
			Required:    true,
		},
	}
}

func (t *DocxInsertBlockTool) Execute(ctx *tools.ToolContext, input string) (*tools.ToolResult, error) {
	var args struct {
		DocumentID  string `json:"document_id"`
		Content     string `json:"content"`
		InsertIndex int    `json:"insert_index"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}

	if args.Content == "" {
		return nil, fmt.Errorf("content is required")
	}

	client, err := t.MCP.GetClient(ctx.Ctx, ctx.Channel, ctx.ChatID)
	if err != nil {
		return nil, err
	}

	// Step 1: Convert Markdown to blocks using Feishu's native API
	convertBody := docxv1.NewConvertDocumentReqBodyBuilder().
		ContentType("markdown").
		Content(args.Content).
		Build()

	convertReq := docxv1.NewConvertDocumentReqBuilder().
		Body(convertBody).
		Build()

	convertResp, err := client.Client().Docx.Document.Convert(ctx.Ctx, convertReq,
		larkcore.WithUserAccessToken(client.AccessToken()))
	if err != nil {
		return nil, fmt.Errorf("convert markdown to blocks: %w", err)
	}
	if !convertResp.Success() {
		return nil, NewAPIError(convertResp.CodeError)
	}

	// Check if we got blocks back
	if len(convertResp.Data.Blocks) == 0 {
		return tools.NewResult("No content to write"), nil
	}

	// Step 2: Clean blocks for Descendant API
	// IMPORTANT: Keep block_id and children, remove parent_id and read-only fields
	for _, block := range convertResp.Data.Blocks {
		cleanBlockForDescendant(block)
	}

	// Step 3: Find root block IDs (blocks with empty parent_id)
	rootBlockIDs := convertResp.Data.FirstLevelBlockIds

	// Step 4: Insert blocks using Descendant API
	// The Descendant API supports nested structures like tables

	descendantBody := docxv1.NewCreateDocumentBlockDescendantReqBodyBuilder().
		Descendants(convertResp.Data.Blocks).
		ChildrenId(rootBlockIDs).
		Index(args.InsertIndex). // Insert at specified index
		Build()

	descendantReq := docxv1.NewCreateDocumentBlockDescendantReqBuilder().
		DocumentId(args.DocumentID).
		BlockId(args.DocumentID). // For root level, block_id equals document_id
		Body(descendantBody).
		Build()

	descendantResp, err := client.Client().Docx.DocumentBlockDescendant.Create(ctx.Ctx, descendantReq,
		larkcore.WithUserAccessToken(client.AccessToken()))
	if err != nil {
		return nil, fmt.Errorf("insert blocks to document: %w", err)
	}
	if !descendantResp.Success() {
		return nil, NewAPIError(descendantResp.CodeError)
	}

	summary := fmt.Sprintf("Inserted %d block(s) to document at index %d", len(convertResp.Data.Blocks), args.InsertIndex)
	return tools.NewResult(summary).WithTips("If you've done editing, you may use feishu_docx_get_content to verify document content."), nil
}

// cleanBlockForDescendant cleans a block for Descendant API
// Keeps: block_id, children (needed for hierarchy)
// Removes: parent_id, merge_info, mention_doc.title
func cleanBlockForDescendant(block *docxv1.Block) {
	if block == nil {
		return
	}

	// Clean table read-only fields
	if block.Table != nil {
		// Remove cells - this is a read-only field, children array is used instead
		block.Table.Cells = nil

		if block.Table.Property != nil {
			// Remove merge_info (read-only)
			block.Table.Property.MergeInfo = nil
			// Remove column_width - may cause schema mismatch
			block.Table.Property.ColumnWidth = nil
		}
	}
	if IsMermaidCode(block) {
		content := GetTextContent(block.Code)
		block.Code = nil
		block.AddOns = docxv1.NewAddOnsBuilder().ComponentTypeId(MermaidAddOnsComponentTypeID).Record(
			fmt.Sprintf(`{"data":%s,"theme":"default","view":"codeChart"}`, strconv.Quote(content)),
		).Build()
		*block.BlockType = BlockTypeAddOns
	}
}

// DocxDeleteBlocksTool deletes blocks from a document by index range.
type DocxDeleteBlocksTool struct {
	MCP *FeishuMCP
}

func (t *DocxDeleteBlocksTool) Name() string { return "feishu_docx_delete_blocks" }

func (t *DocxDeleteBlocksTool) Description() string {
	return "Delete multiple blocks from a document by specifying an index range. Indices are 0-based, end_index is exclusive."
}

func (t *DocxDeleteBlocksTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{
			Name:        "document_id",
			Type:        "string",
			Description: "Document ID (e.g., doxcnXXXXX)",
			Required:    true,
		},
		{
			Name:        "parent_block_id",
			Type:        "string",
			Description: "Parent block ID (use document_id for root level blocks)",
			Required:    true,
		},
		{
			Name:        "start_index",
			Type:        "integer",
			Description: "Start index of blocks to delete (0-based)",
			Required:    true,
		},
		{
			Name:        "end_index",
			Type:        "integer",
			Description: "End index of blocks to delete (exclusive, like Python slicing)",
			Required:    true,
		},
	}
}

func (t *DocxDeleteBlocksTool) Execute(ctx *tools.ToolContext, input string) (*tools.ToolResult, error) {
	var args struct {
		DocumentID    string `json:"document_id"`
		ParentBlockID string `json:"parent_block_id"`
		StartIndex    int    `json:"start_index"`
		EndIndex      int    `json:"end_index"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}

	if args.StartIndex < 0 || args.EndIndex <= args.StartIndex {
		return nil, fmt.Errorf("invalid index range: start_index=%d, end_index=%d (must have start_index >= 0 and end_index > start_index)",
			args.StartIndex, args.EndIndex)
	}

	client, err := t.MCP.GetClient(ctx.Ctx, ctx.Channel, ctx.ChatID)
	if err != nil {
		return nil, err
	}

	// Build the delete request
	body := docxv1.NewBatchDeleteDocumentBlockChildrenReqBodyBuilder().
		StartIndex(args.StartIndex).
		EndIndex(args.EndIndex).
		Build()

	req := docxv1.NewBatchDeleteDocumentBlockChildrenReqBuilder().
		DocumentId(args.DocumentID).
		BlockId(args.ParentBlockID).
		Body(body).
		Build()

	resp, err := client.Client().Docx.DocumentBlockChildren.BatchDelete(ctx.Ctx, req,
		larkcore.WithUserAccessToken(client.AccessToken()))
	if err != nil {
		return nil, fmt.Errorf("delete blocks: %w", err)
	}
	if !resp.Success() {
		return nil, NewAPIError(resp.CodeError)
	}

	count := args.EndIndex - args.StartIndex
	return tools.NewResult(fmt.Sprintf("✅ Deleted %d block(s) from index %d to %d",
		count, args.StartIndex, args.EndIndex)), nil
}
