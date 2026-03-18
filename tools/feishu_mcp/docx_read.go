package feishu_mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"xbot/llm"
	"xbot/tools"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkdocs "github.com/larksuite/oapi-sdk-go/v3/service/docs/v1"
	docxv1 "github.com/larksuite/oapi-sdk-go/v3/service/docx/v1"
)

// fetchAllBlocks fetches all blocks of a document using pagination.
func fetchAllBlocks(ctx context.Context, client *Client, documentID string) ([]*docxv1.Block, error) {
	var allItems []*docxv1.Block
	pageToken := ""
	for {
		reqBuilder := docxv1.NewListDocumentBlockReqBuilder().
			DocumentId(documentID).
			PageSize(500)
		if pageToken != "" {
			reqBuilder.PageToken(pageToken)
		}
		req := reqBuilder.Build()

		resp, err := client.Client().Docx.DocumentBlock.List(ctx, req,
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
	return allItems, nil
}

// DocxGetContentTool gets document content in Markdown format.
type DocxGetContentTool struct {
	FeishuToolBase
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
		"Some special nodes e.g. mermaid graph may disappear in markdown. You can use `feishu_docx_get_block` to get detailed block content for those nodes.",
	), nil
}

type DocxGetBlockTool struct {
	FeishuToolBase
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
	FeishuToolBase
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
			Description: "Limit for pagination (max 100, default 100)",
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
	if args.Offset < 0 {
		args.Offset = 0
	}
	if args.Limit <= 0 || args.Limit > 100 {
		args.Limit = 100
	}

	client, err := t.MCP.GetClient(ctx.Ctx, ctx.Channel, ctx.ChatID)
	if err != nil {
		return nil, err
	}

	allItems, err := fetchAllBlocks(ctx.Ctx, client, args.DocumentID)
	if err != nil {
		return nil, err
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

// DocxFindBlockTool searches blocks in a document by content.
type DocxFindBlockTool struct {
	FeishuToolBase
	MCP *FeishuMCP
}

func (t *DocxFindBlockTool) Name() string { return "feishu_docx_find_block" }

func (t *DocxFindBlockTool) Description() string {
	return "Search for blocks in a document whose content contains a given string (case-insensitive). Returns matching top-level blocks."
}

func (t *DocxFindBlockTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{
			Name:        "document_id",
			Type:        "string",
			Description: "Document ID (e.g., doxcnXXXXX)",
			Required:    true,
		},
		{
			Name:        "query",
			Type:        "string",
			Description: "Text to search for in block content (case-insensitive, auto-trimmed)",
			Required:    true,
		},
	}
}

func (t *DocxFindBlockTool) Execute(ctx *tools.ToolContext, input string) (*tools.ToolResult, error) {
	var args struct {
		DocumentID string `json:"document_id"`
		Query      string `json:"query"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}
	args.Query = strings.TrimSpace(args.Query)
	if args.Query == "" {
		return nil, fmt.Errorf("query is required")
	}

	client, err := t.MCP.GetClient(ctx.Ctx, ctx.Channel, ctx.ChatID)
	if err != nil {
		return nil, err
	}

	allItems, err := fetchAllBlocks(ctx.Ctx, client, args.DocumentID)
	if err != nil {
		return nil, err
	}

	if len(allItems) == 0 {
		return tools.NewResult("Document is empty, no blocks to search."), nil
	}

	// Build block map and identify top-level blocks
	blockMap := make(map[string]*docxv1.Block)
	childMap := make(map[string]struct{})
	var topLevelIDs []string

	for _, block := range allItems {
		if block.BlockId == nil || *block.BlockId == args.DocumentID {
			continue
		}
		blockMap[*block.BlockId] = block
		trackChildren(block, childMap)
	}

	// Collect top-level block IDs in order
	for _, block := range allItems {
		if block.BlockId == nil || *block.BlockId == args.DocumentID {
			continue
		}
		if _, isChild := childMap[*block.BlockId]; !isChild {
			topLevelIDs = append(topLevelIDs, *block.BlockId)
		}
	}

	// For each top-level block, check if its subtree text content contains the query
	queryLower := strings.ToLower(args.Query)
	var matchedBlocks []map[string]any

	for i, tlID := range topLevelIDs {
		tlBlock := blockMap[tlID]
		// Collect this block and all descendants, check text content
		if !subtreeContainsText(tlBlock, blockMap, queryLower) {
			continue
		}

		blockType := 0
		if tlBlock.BlockType != nil {
			blockType = *tlBlock.BlockType
		}
		parentId := ""
		if tlBlock.ParentId != nil {
			parentId = *tlBlock.ParentId
		}

		matchedBlocks = append(matchedBlocks, map[string]any{
			"block_id":        tlID,
			"block_type":      blockType,
			"block_type_desc": GetBlockTypeDesc(blockType),
			"block_type_name": GetBlockTypeName(blockType),
			"content_summary": GetBlockText(tlBlock),
			"parent_id":       parentId,
			"index":           i,
		})
	}

	if len(matchedBlocks) == 0 {
		return tools.NewResult(fmt.Sprintf("No blocks found matching %q", args.Query)), nil
	}

	summary := fmt.Sprintf("Found %d block(s) matching %q", len(matchedBlocks), args.Query)
	detail, _ := json.MarshalIndent(matchedBlocks, "", "  ")
	return tools.NewResultWithDetail(summary, string(detail)), nil
}

// subtreeContainsText checks if a block or any of its descendants contain the
// query string (already lowercased) in their text content.
func subtreeContainsText(block *docxv1.Block, blockMap map[string]*docxv1.Block, queryLower string) bool {
	if block == nil {
		return false
	}
	if strings.Contains(strings.ToLower(GetTextContent(getBlockTextBody(block))), queryLower) {
		return true
	}
	if block.Children == nil {
		return false
	}
	for _, childID := range block.Children {
		if child, ok := blockMap[childID]; ok {
			if subtreeContainsText(child, blockMap, queryLower) {
				return true
			}
		}
	}
	return false
}
