package feishu_mcp

import (
	"encoding/json"
	"fmt"
	"strconv"

	"xbot/llm"
	"xbot/tools"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	docxv1 "github.com/larksuite/oapi-sdk-go/v3/service/docx/v1"
)

// DocxInsertBlockTool writes Markdown content to a document using Feishu's native Markdown API.
type DocxInsertBlockTool struct {
	FeishuToolBase
	MCP *FeishuMCP
}

func (t *DocxInsertBlockTool) Name() string { return "feishu_docx_insert_block" }

func (t *DocxInsertBlockTool) Description() string {
	return "Insert content into a document at a specific block index. Content is in Markdown format and will be converted to native blocks. Use `feishu_docx_list_blocks` or `feishu_docx_find_block` to find block indices."
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
	FeishuToolBase
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
