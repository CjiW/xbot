package feishu_mcp

import (
	"encoding/json"
	"fmt"
	"strings"

	"xbot/llm"
	"xbot/tools"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	docxv1 "github.com/larksuite/oapi-sdk-go/v3/service/docx/v1"
)

// DocxGetContentTool gets document content in Markdown format.
type DocxGetContentTool struct {
	MCP *FeishuMCP
}

func (t *DocxGetContentTool) Name() string { return "feishu_docx_get_content" }

func (t *DocxGetContentTool) Description() string {
	return "Get document content and convert it to Markdown format."
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

	// Get document blocks
	req := docxv1.NewListDocumentBlockReqBuilder().
		DocumentId(args.DocumentID).
		Build()

	resp, err := client.Client().Docx.V1.DocumentBlock.List(ctx.Ctx, req,
		larkcore.WithUserAccessToken(client.AccessToken()))
	if err != nil {
		return nil, fmt.Errorf("list document blocks: %w", err)
	}
	if !resp.Success() {
		return nil, NewAPIError(resp.Code, resp.Msg)
	}

	if len(resp.Data.Items) == 0 {
		return tools.NewResultWithTips("Document is empty", "Use feishu_docx_write to add content to this document."), nil
	}

	// Convert blocks to Markdown
	markdown := formatBlocksToMarkdown(resp.Data.Items)

	// Truncate if too long (limit to ~10k chars for LLM context)
	const maxLen = 10000
	if len(markdown) > maxLen {
		markdown = markdown[:maxLen] + "\n\n... (content truncated)"
	}

	return tools.NewResultWithTips(
		fmt.Sprintf("Document content:\n\n%s", markdown),
		"Use feishu_docx_write to add more content, or feishu_docx_update_block to modify specific blocks.",
	), nil
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
	}
}

func (t *DocxListBlocksTool) Execute(ctx *tools.ToolContext, input string) (*tools.ToolResult, error) {
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

	req := docxv1.NewListDocumentBlockReqBuilder().
		DocumentId(args.DocumentID).
		Build()

	resp, err := client.Client().Docx.V1.DocumentBlock.List(ctx.Ctx, req,
		larkcore.WithUserAccessToken(client.AccessToken()))
	if err != nil {
		return nil, fmt.Errorf("list document blocks: %w", err)
	}
	if !resp.Success() {
		return nil, NewAPIError(resp.Code, resp.Msg)
	}

	if len(resp.Data.Items) == 0 {
		return tools.NewResultWithTips("Document is empty", "Use feishu_docx_write to add content to this document."), nil
	}

	// Build block summary
	var blocks []map[string]any
	for _, block := range resp.Data.Items {
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
		blocks = append(blocks, map[string]any{
			"block_id":   blockId,
			"block_type": blockType,
			"parent_id":  parentId,
		})
	}

	summary := fmt.Sprintf("Document has %d block(s)", len(blocks))
	detail, _ := json.MarshalIndent(blocks, "", "  ")
	return tools.NewResultWithDetail(summary, string(detail)), nil
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

	resp, err := client.Client().Docx.V1.Document.Create(ctx.Ctx, req,
		larkcore.WithUserAccessToken(client.AccessToken()))
	if err != nil {
		return nil, fmt.Errorf("create document: %w", err)
	}
	if !resp.Success() {
		return nil, NewAPIError(resp.Code, resp.Msg)
	}

	documentID := ""
	if resp.Data.Document != nil && resp.Data.Document.DocumentId != nil {
		documentID = *resp.Data.Document.DocumentId
	}

	summary := fmt.Sprintf("Document created with ID: %s", documentID)
	detail, _ := json.MarshalIndent(resp.Data.Document, "", "  ")
	return tools.NewResultWithDetail(summary, string(detail)), nil
}

// DocxRawContentTool gets document plain text content.
type DocxRawContentTool struct {
	MCP *FeishuMCP
}

func (t *DocxRawContentTool) Name() string { return "feishu_docx_raw_content" }

func (t *DocxRawContentTool) Description() string {
	return "Get document plain text content (without formatting)."
}

func (t *DocxRawContentTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{
			Name:        "document_id",
			Type:        "string",
			Description: "Document ID (e.g., doxcnXXXXX)",
			Required:    true,
		},
	}
}

func (t *DocxRawContentTool) Execute(ctx *tools.ToolContext, input string) (*tools.ToolResult, error) {
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

	// Get document blocks and extract text
	req := docxv1.NewListDocumentBlockReqBuilder().
		DocumentId(args.DocumentID).
		Build()

	resp, err := client.Client().Docx.V1.DocumentBlock.List(ctx.Ctx, req,
		larkcore.WithUserAccessToken(client.AccessToken()))
	if err != nil {
		return nil, fmt.Errorf("get document content: %w", err)
	}
	if !resp.Success() {
		return nil, NewAPIError(resp.Code, resp.Msg)
	}

	// Extract plain text from blocks
	var content strings.Builder
	for _, block := range resp.Data.Items {
		content.WriteString(extractTextFromBlock(block))
		content.WriteString("\n")
	}

	// Truncate if too long
	const maxLen = 10000
	result := content.String()
	if len(result) > maxLen {
		result = result[:maxLen] + "\n\n... (content truncated)"
	}

	return tools.NewResult(fmt.Sprintf("Document content:\n\n%s", result)), nil
}

// formatBlocksToMarkdown converts document blocks to Markdown format.
func formatBlocksToMarkdown(blocks []*docxv1.Block) string {
	var builder strings.Builder

	for _, block := range blocks {
		if block.BlockType == nil {
			continue
		}

		switch *block.BlockType {
		case 1: // Page
			builder.WriteString(formatPageBlock(block))
		case 2: // Text
			builder.WriteString(formatTextBlock(block))
		case 3: // Heading 1
			builder.WriteString(fmt.Sprintf("# %s\n\n", extractTextFromBlock(block)))
		case 4: // Heading 2
			builder.WriteString(fmt.Sprintf("## %s\n\n", extractTextFromBlock(block)))
		case 5: // Heading 3
			builder.WriteString(fmt.Sprintf("### %s\n\n", extractTextFromBlock(block)))
		case 6: // Heading 4
			builder.WriteString(fmt.Sprintf("#### %s\n\n", extractTextFromBlock(block)))
		case 7: // Heading 5
			builder.WriteString(fmt.Sprintf("##### %s\n\n", extractTextFromBlock(block)))
		case 8: // Heading 6
			builder.WriteString(fmt.Sprintf("###### %s\n\n", extractTextFromBlock(block)))
		case 9: // Heading 7
			builder.WriteString(fmt.Sprintf("####### %s\n\n", extractTextFromBlock(block)))
		case 10: // Heading 8
			builder.WriteString(fmt.Sprintf("######## %s\n\n", extractTextFromBlock(block)))
		case 11: // Heading 9
			builder.WriteString(fmt.Sprintf("######### %s\n\n", extractTextFromBlock(block)))
		case 12: // Bullet
			builder.WriteString(fmt.Sprintf("- %s\n", extractTextFromBlock(block)))
		case 13: // Ordered
			builder.WriteString(fmt.Sprintf("1. %s\n", extractTextFromBlock(block)))
		case 14: // Code
			builder.WriteString(formatCodeBlock(block))
		case 15: // Quote
			builder.WriteString(fmt.Sprintf("> %s\n\n", extractTextFromBlock(block)))
		case 16: // Equation
			builder.WriteString(fmt.Sprintf("$$%s$$\n\n", extractTextFromBlock(block)))
		case 17: // Todo
			builder.WriteString(fmt.Sprintf("- [ ] %s\n", extractTextFromBlock(block)))
		case 18: // Divider
			builder.WriteString("---\n\n")
		case 19: // Image
			builder.WriteString(formatImageBlock(block))
		case 20: // Table
			builder.WriteString("[Table]\n\n")
		case 21: // Callout
			builder.WriteString(fmt.Sprintf("> **Note**: %s\n\n", extractTextFromBlock(block)))
		case 22: // View
			builder.WriteString("[View]\n\n")
		case 23: // Bitable
			builder.WriteString("[Bitable]\n\n")
		case 24: // ChatCard
			builder.WriteString("[Chat Card]\n\n")
		case 25: // Video
			builder.WriteString("[Video]\n\n")
		case 26: // File
			builder.WriteString("[File]\n\n")
		case 27: // Audio
			builder.WriteString("[Audio]\n\n")
		case 28: // Countdown
			builder.WriteString("[Countdown]\n\n")
		case 29: // Mindnote
			builder.WriteString("[Mindmap]\n\n")
		case 30: // Diagram
			builder.WriteString("[Diagram]\n\n")
		case 31: // Chart
			builder.WriteString("[Chart]\n\n")
		case 32: // Group
			builder.WriteString("[Group]\n\n")
		case 33: // TodoList
			builder.WriteString("[Todo List]\n\n")
		case 34: // Sprint
			builder.WriteString("[Sprint]\n\n")
		case 35: // Meeting
			builder.WriteString("[Meeting]\n\n")
		case 36: // Poll
			builder.WriteString("[Poll]\n\n")
		case 37: // Wiki
			builder.WriteString("[Wiki]\n\n")
		case 38: // CodeBlock
			builder.WriteString(formatCodeBlock(block))
		case 39: // Shortcut
			builder.WriteString("[Shortcut]\n\n")
		case 40: // SyncedSource
			builder.WriteString("[Synced Source]\n\n")
		case 41: // BiTableSyncedSource
			builder.WriteString("[Bitable Synced Source]\n\n")
		case 42: // Interface
			builder.WriteString("[Interface]\n\n")
		case 43: // Whiteboard
			builder.WriteString("[Whiteboard]\n\n")
		case 44: // Heading
			builder.WriteString(fmt.Sprintf("## %s\n\n", extractTextFromBlock(block)))
		case 45: // List
			builder.WriteString(fmt.Sprintf("- %s\n", extractTextFromBlock(block)))
		case 46: // QuoteContainer
			builder.WriteString(fmt.Sprintf("> %s\n\n", extractTextFromBlock(block)))
		case 47: // Org
			builder.WriteString("[Org Chart]\n\n")
		case 48: // Collection
			builder.WriteString("[Collection]\n\n")
		case 49: // Sheet
			builder.WriteString("[Sheet]\n\n")
		case 50: // Docx
			builder.WriteString("[Document]\n\n")
		case 51: // Bitable
			builder.WriteString("[Bitable]\n\n")
		case 52: // File
			builder.WriteString("[File]\n\n")
		case 53: // Folder
			builder.WriteString("[Folder]\n\n")
		case 54: // Mindnote
			builder.WriteString("[Mindmap]\n\n")
		case 55: // Docx
			builder.WriteString("[Document]\n\n")
		case 56: // Bitable
			builder.WriteString("[Bitable]\n\n")
		case 57: // File
			builder.WriteString("[File]\n\n")
		case 58: // Folder
			builder.WriteString("[Folder]\n\n")
		case 59: // Link
			builder.WriteString("[Link]\n\n")
		case 60: // User
			builder.WriteString("[User]\n\n")
		case 61: // Group
			builder.WriteString("[Group Chat]\n\n")
		case 62: // Room
			builder.WriteString("[Room]\n\n")
		case 63: // Calendar
			builder.WriteString("[Calendar]\n\n")
		case 64: // Event
			builder.WriteString("[Event]\n\n")
		case 65: // Task
			builder.WriteString("[Task]\n\n")
		case 66: // Email
			builder.WriteString("[Email]\n\n")
		case 67: // Thread
			builder.WriteString("[Thread]\n\n")
		case 68: // Draft
			builder.WriteString("[Draft]\n\n")
		case 69: // Template
			builder.WriteString("[Template]\n\n")
		case 70: // Story
			builder.WriteString("[Story]\n\n")
		case 71: // Wiki
			builder.WriteString("[Wiki]\n\n")
		case 72: // Drive
			builder.WriteString("[Drive]\n\n")
		case 73: // Wallet
			builder.WriteString("[Wallet]\n\n")
		case 74: // Wallet
			builder.WriteString("[Wallet]\n\n")
		case 75: // Invoice
			builder.WriteString("[Invoice]\n\n")
		case 76: // Receipt
			builder.WriteString("[Receipt]\n\n")
		case 77: // Contract
			builder.WriteString("[Contract]\n\n")
		case 78: // Report
			builder.WriteString("[Report]\n\n")
		case 79: // Certificate
			builder.WriteString("[Certificate]\n\n")
		case 80: // Badge
			builder.WriteString("[Badge]\n\n")
		case 81: // Trophy
			builder.WriteString("[Trophy]\n\n")
		case 82: // Medal
			builder.WriteString("[Medal]\n\n")
		case 83: // Award
			builder.WriteString("[Award]\n\n")
		case 84: // Other
			builder.WriteString("[Other]\n\n")
		default:
			// Try to extract text for unknown block types
			if text := extractTextFromBlock(block); text != "" {
				builder.WriteString(fmt.Sprintf("%s\n\n", text))
			}
		}
	}

	return builder.String()
}

// formatPageBlock formats a page block.
func formatPageBlock(block *docxv1.Block) string {
	if block.Page == nil {
		return ""
	}
	var text strings.Builder
	if block.Page.Elements != nil {
		for _, element := range block.Page.Elements {
			if element.TextRun != nil && element.TextRun.Content != nil {
				text.WriteString(*element.TextRun.Content)
			}
		}
	}
	if text.Len() > 0 {
		return fmt.Sprintf("# %s\n\n", text.String())
	}
	return "---\n\n"
}

// formatTextBlock formats a text block with inline styles.
func formatTextBlock(block *docxv1.Block) string {
	if block.Text == nil {
		return ""
	}
	var builder strings.Builder
	if block.Text.Elements != nil {
		for _, element := range block.Text.Elements {
			if element.TextRun != nil && element.TextRun.Content != nil {
				text := *element.TextRun.Content
				// Add markdown styling based on text style
				if element.TextRun.TextElementStyle != nil {
					if element.TextRun.TextElementStyle.Bold != nil && *element.TextRun.TextElementStyle.Bold {
						text = fmt.Sprintf("**%s**", text)
					}
					if element.TextRun.TextElementStyle.Italic != nil && *element.TextRun.TextElementStyle.Italic {
						text = fmt.Sprintf("*%s*", text)
					}
					if element.TextRun.TextElementStyle.Strikethrough != nil && *element.TextRun.TextElementStyle.Strikethrough {
						text = fmt.Sprintf("~~%s~~", text)
					}
					if element.TextRun.TextElementStyle.InlineCode != nil && *element.TextRun.TextElementStyle.InlineCode {
						text = fmt.Sprintf("`%s`", text)
					}
					if element.TextRun.TextElementStyle.Link != nil && element.TextRun.TextElementStyle.Link.Url != nil {
						text = fmt.Sprintf("[%s](%s)", text, *element.TextRun.TextElementStyle.Link.Url)
					}
				}
				builder.WriteString(text)
			} else if element.MentionUser != nil && element.MentionUser.UserId != nil {
				builder.WriteString(fmt.Sprintf("@%s", *element.MentionUser.UserId))
			} else if element.MentionDoc != nil && element.MentionDoc.Title != nil {
				builder.WriteString(fmt.Sprintf("[%s]", *element.MentionDoc.Title))
			}
		}
	}
	builder.WriteString("\n")
	return builder.String()
}

// formatCodeBlock formats a code block.
func formatCodeBlock(block *docxv1.Block) string {
	lang := ""
	var code strings.Builder

	if block.Code != nil {
		if block.Code.Style != nil && block.Code.Style.Language != nil {
			// Language is an int, convert to common language names
			langMap := map[int]string{
				0:  "text",
				1:  "javascript",
				2:  "typescript",
				3:  "java",
				4:  "c",
				5:  "cpp",
				6:  "csharp",
				7:  "python",
				8:  "go",
				9:  "rust",
				10: "php",
				11: "ruby",
				12: "swift",
				13: "kotlin",
				14: "scala",
				15: "dart",
				16: "shell",
				17: "bash",
				18: "sql",
				19: "html",
				20: "css",
				21: "xml",
				22: "json",
				23: "yaml",
				24: "markdown",
			}
			if l, ok := langMap[*block.Code.Style.Language]; ok {
				lang = l
			}
		}
		if block.Code.Elements != nil {
			for _, element := range block.Code.Elements {
				if element.TextRun != nil && element.TextRun.Content != nil {
					code.WriteString(*element.TextRun.Content)
				}
			}
		}
	}

	return fmt.Sprintf("```%s\n%s\n```\n\n", lang, code.String())
}

// formatImageBlock formats an image block.
func formatImageBlock(block *docxv1.Block) string {
	if block.Image == nil {
		return ""
	}
	token := ""
	if block.Image.Token != nil {
		token = *block.Image.Token
	}
	return fmt.Sprintf("![Image](%s)\n\n", token)
}

// DocxWriteTool writes Markdown content to a document using Feishu's native Markdown API.
type DocxWriteTool struct {
	MCP *FeishuMCP
}

func (t *DocxWriteTool) Name() string { return "feishu_docx_write" }

func (t *DocxWriteTool) Description() string {
	return "Write Markdown content to a Feishu document. Supports headings, lists, code blocks, quotes, tables, and more."
}

func (t *DocxWriteTool) Parameters() []llm.ToolParam {
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
	}
}

func (t *DocxWriteTool) Execute(ctx *tools.ToolContext, input string) (*tools.ToolResult, error) {
	var args struct {
		DocumentID string `json:"document_id"`
		Content    string `json:"content"`
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

	convertResp, err := client.Client().Docx.V1.Document.Convert(ctx.Ctx, convertReq,
		larkcore.WithUserAccessToken(client.AccessToken()))
	if err != nil {
		return nil, fmt.Errorf("convert markdown to blocks: %w", err)
	}
	if !convertResp.Success() {
		return nil, NewAPIError(convertResp.Code, convertResp.Msg)
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
	rootBlockIDs := findRootBlockIDs(convertResp.Data.Blocks)

	// Step 4: Insert blocks using Descendant API
	// The Descendant API supports nested structures like tables
	descendantBody := docxv1.NewCreateDocumentBlockDescendantReqBodyBuilder().
		Descendants(convertResp.Data.Blocks).
		ChildrenId(rootBlockIDs).
		Index(0). // Insert at beginning
		Build()

	descendantReq := docxv1.NewCreateDocumentBlockDescendantReqBuilder().
		DocumentId(args.DocumentID).
		BlockId(args.DocumentID). // For root level, block_id equals document_id
		Body(descendantBody).
		Build()

	descendantResp, err := client.Client().Docx.V1.DocumentBlockDescendant.Create(ctx.Ctx, descendantReq,
		larkcore.WithUserAccessToken(client.AccessToken()))
	if err != nil {
		return nil, fmt.Errorf("insert blocks to document: %w", err)
	}
	if !descendantResp.Success() {
		return nil, NewAPIError(descendantResp.Code, descendantResp.Msg)
	}

	summary := fmt.Sprintf("Written %d block(s) to document", len(convertResp.Data.Blocks))
	return tools.NewResult(summary), nil
}

// cleanBlockForDescendant cleans a block for Descendant API
// Keeps: block_id, children (needed for hierarchy)
// Removes: parent_id, merge_info, mention_doc.title
func cleanBlockForDescendant(block *docxv1.Block) {
	if block == nil {
		return
	}

	// KEEP block_id - required for Descendant API hierarchy
	// KEEP children - required for parent-child relationships
	// KEEP block_type - required for API to identify block type

	// Remove parent_id - not needed for Descendant API
	block.ParentId = nil

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

	// Clean mention_doc title fields (read-only)
	cleanTextMentionDoc(block.Page)
	cleanTextMentionDoc(block.Text)
	cleanTextMentionDoc(block.Heading1)
	cleanTextMentionDoc(block.Heading2)
	cleanTextMentionDoc(block.Heading3)
	cleanTextMentionDoc(block.Heading4)
	cleanTextMentionDoc(block.Heading5)
	cleanTextMentionDoc(block.Heading6)
	cleanTextMentionDoc(block.Heading7)
	cleanTextMentionDoc(block.Heading8)
	cleanTextMentionDoc(block.Heading9)
	cleanTextMentionDoc(block.Bullet)
	cleanTextMentionDoc(block.Ordered)
	cleanTextMentionDoc(block.Code)
	cleanTextMentionDoc(block.Quote)
	cleanTextMentionDoc(block.Equation)
	cleanTextMentionDoc(block.Todo)
}

// findRootBlockIDs finds the IDs of root blocks (blocks that are not children of any other block)
func findRootBlockIDs(blocks []*docxv1.Block) []string {
	// Build a set of all block IDs that are children of other blocks
	childIDs := make(map[string]bool)
	for _, block := range blocks {
		for _, childID := range block.Children {
			childIDs[childID] = true
		}
	}

	var rootIDs []string
	for _, block := range blocks {
		if block.BlockId != nil {
			// A block is a root if:
			// 1. It's not a child of any other block
			// 2. It's not a structural block type (table_cell, grid_column)
			if !childIDs[*block.BlockId] && block.BlockType != nil {
				blockType := *block.BlockType
				// Table cells (32) and grid columns (24) are structural blocks, not root blocks
				if blockType != 32 && blockType != 24 {
					rootIDs = append(rootIDs, *block.BlockId)
				}
			}
		}
	}
	return rootIDs
}

// cleanBlocksForInsertion removes read-only fields from blocks before insertion.
// The convert API returns some fields that are read-only and cannot be passed to the create API.
func cleanBlocksForInsertion(blocks []*docxv1.Block) []*docxv1.Block {
	result := make([]*docxv1.Block, 0, len(blocks))
	for _, block := range blocks {
		cleanBlock(block)
		result = append(result, block)
	}
	return result
}

// cleanBlock recursively cleans read-only fields from a block.
// According to Feishu documentation, only merge_info in Table blocks is read-only.
// Other fields like block_type, parent_id, children are needed for the Descendant Create API.
func cleanBlock(block *docxv1.Block) {
	if block == nil {
		return
	}

	// Clean block_id - API auto-generates new IDs during creation
	// The temporary IDs from Convert API are for parent-child reference only
	block.BlockId = nil

	// KEEP block_type - needed for API to identify block type
	// KEEP parent_id - needed for nested structure
	// KEEP children - needed for parent-child relationships (children are block IDs, not nested blocks)

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

	// Clean mention_doc title fields in text elements (read-only)
	cleanTextMentionDoc(block.Page)
	cleanTextMentionDoc(block.Text)
	cleanTextMentionDoc(block.Heading1)
	cleanTextMentionDoc(block.Heading2)
	cleanTextMentionDoc(block.Heading3)
	cleanTextMentionDoc(block.Heading4)
	cleanTextMentionDoc(block.Heading5)
	cleanTextMentionDoc(block.Heading6)
	cleanTextMentionDoc(block.Heading7)
	cleanTextMentionDoc(block.Heading8)
	cleanTextMentionDoc(block.Heading9)
	cleanTextMentionDoc(block.Bullet)
	cleanTextMentionDoc(block.Ordered)
	cleanTextMentionDoc(block.Code)
	cleanTextMentionDoc(block.Quote)
	cleanTextMentionDoc(block.Equation)
	cleanTextMentionDoc(block.Todo)
}

// cleanTextMentionDoc cleans read-only fields from text elements.
// Only removes fields that are documented as read-only.
func cleanTextMentionDoc(text *docxv1.Text) {
	if text == nil || text.Elements == nil {
		return
	}

	for _, elem := range text.Elements {
		// Clean mention_doc title (read-only according to documentation)
		if elem.MentionDoc != nil {
			elem.MentionDoc.Title = nil
		}
	}
}

// extractTextFromBlock extracts plain text from a block.
func extractTextFromBlock(block *docxv1.Block) string {
	var textBlocks []*docxv1.Text

	// Try different text fields
	switch {
	case block.Text != nil:
		textBlocks = []*docxv1.Text{block.Text}
	case block.Heading1 != nil:
		textBlocks = []*docxv1.Text{block.Heading1}
	case block.Heading2 != nil:
		textBlocks = []*docxv1.Text{block.Heading2}
	case block.Heading3 != nil:
		textBlocks = []*docxv1.Text{block.Heading3}
	case block.Heading4 != nil:
		textBlocks = []*docxv1.Text{block.Heading4}
	case block.Heading5 != nil:
		textBlocks = []*docxv1.Text{block.Heading5}
	case block.Heading6 != nil:
		textBlocks = []*docxv1.Text{block.Heading6}
	case block.Heading7 != nil:
		textBlocks = []*docxv1.Text{block.Heading7}
	case block.Heading8 != nil:
		textBlocks = []*docxv1.Text{block.Heading8}
	case block.Heading9 != nil:
		textBlocks = []*docxv1.Text{block.Heading9}
	case block.Bullet != nil:
		textBlocks = []*docxv1.Text{block.Bullet}
	case block.Ordered != nil:
		textBlocks = []*docxv1.Text{block.Ordered}
	case block.Code != nil:
		textBlocks = []*docxv1.Text{block.Code}
	case block.Quote != nil:
		textBlocks = []*docxv1.Text{block.Quote}
	case block.Equation != nil:
		textBlocks = []*docxv1.Text{block.Equation}
	case block.Todo != nil:
		textBlocks = []*docxv1.Text{block.Todo}
	case block.Page != nil:
		textBlocks = []*docxv1.Text{block.Page}
	}

	var parts []string
	for _, text := range textBlocks {
		if text == nil || text.Elements == nil {
			continue
		}
		for _, element := range text.Elements {
			if element.TextRun != nil && element.TextRun.Content != nil {
				parts = append(parts, *element.TextRun.Content)
			}
		}
	}
	return strings.Join(parts, "")
}

// DocxUpdateBlockTool updates a specific block in a document.
type DocxUpdateBlockTool struct {
	MCP *FeishuMCP
}

func (t *DocxUpdateBlockTool) Name() string { return "feishu_docx_update_block" }

func (t *DocxUpdateBlockTool) Description() string {
	return "Update a specific block in a Feishu document. Update types: 'text_elements', 'text_style', 'text_full'."
}

func (t *DocxUpdateBlockTool) Parameters() []llm.ToolParam {
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
			Description: "Block ID to update (get from feishu_docx_list_blocks)",
			Required:    true,
		},
		{
			Name:        "update_type",
			Type:        "string",
			Description: "Update type: 'text_elements' (provide elements array), 'text_style' (provide style object), 'text_full' (provide markdown_content)",
			Required:    true,
		},
		{
			Name:        "elements",
			Type:        "array",
			Description: "Text elements array (for text_elements update type). Each element should have 'content' field.",
			Required:    false,
		},
		{
			Name:        "style",
			Type:        "object",
			Description: "Text style object (for text_style update type). Can include 'bold', 'italic', 'underline', etc.",
			Required:    false,
		},
		{
			Name:        "markdown_content",
			Type:        "string",
			Description: "Markdown content (for text_full update type). Will replace all text content in the block.",
			Required:    false,
		},
	}
}

func (t *DocxUpdateBlockTool) Execute(ctx *tools.ToolContext, input string) (*tools.ToolResult, error) {
	var args struct {
		DocumentID      string                   `json:"document_id"`
		BlockID         string                   `json:"block_id"`
		UpdateType      string                   `json:"update_type"`
		Elements        []map[string]interface{} `json:"elements"`
		Style           map[string]interface{}   `json:"style"`
		MarkdownContent string                   `json:"markdown_content"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}

	client, err := t.MCP.GetClient(ctx.Ctx, ctx.Channel, ctx.ChatID)
	if err != nil {
		return nil, err
	}

	// Build update request based on update_type
	var updateRequest *docxv1.UpdateBlockRequest

	switch args.UpdateType {
	case "text_elements":
		// Convert elements to TextElement array
		elements := make([]*docxv1.TextElement, 0, len(args.Elements))
		for _, elem := range args.Elements {
			content := ""
			if c, ok := elem["content"].(string); ok {
				content = c
			}
			elements = append(elements, &docxv1.TextElement{
				TextRun: &docxv1.TextRun{
					Content: &content,
				},
			})
		}
		updateRequest = &docxv1.UpdateBlockRequest{
			UpdateTextElements: &docxv1.UpdateTextElementsRequest{
				Elements: elements,
			},
		}

	case "text_style":
		// Build text style from input (block-level styles like alignment)
		style := &docxv1.TextStyle{}
		fields := []int{}
		if align, ok := args.Style["align"].(float64); ok {
			style.Align = ptrToInt(int(align))
			fields = append(fields, 1) // align field index
		}
		if done, ok := args.Style["done"].(bool); ok {
			style.Done = &done
			fields = append(fields, 2) // done field index
		}
		updateRequest = &docxv1.UpdateBlockRequest{
			UpdateTextStyle: &docxv1.UpdateTextStyleRequest{
				Style:  style,
				Fields: fields,
			},
		}

	case "text_full":
		// Convert markdown to block and use full text update
		if args.MarkdownContent == "" {
			return nil, fmt.Errorf("markdown_content is required for text_full update type")
		}
		// Convert markdown to blocks
		convertBody := docxv1.NewConvertDocumentReqBodyBuilder().
			ContentType("markdown").
			Content(args.MarkdownContent).
			Build()

		convertReq := docxv1.NewConvertDocumentReqBuilder().
			Body(convertBody).
			Build()

		convertResp, err := client.Client().Docx.V1.Document.Convert(ctx.Ctx, convertReq,
			larkcore.WithUserAccessToken(client.AccessToken()))
		if err != nil {
			return nil, fmt.Errorf("convert markdown to blocks: %w", err)
		}
		if !convertResp.Success() {
			return nil, NewAPIError(convertResp.Code, convertResp.Msg)
		}

		if len(convertResp.Data.Blocks) == 0 {
			return tools.NewResult("No content to update"), nil
		}

		// Use the first block's text content
		firstBlock := convertResp.Data.Blocks[0]
		textContent := extractTextFromBlock(firstBlock)
		elements := []*docxv1.TextElement{
			{
				TextRun: &docxv1.TextRun{
					Content: &textContent,
				},
			},
		}
		updateRequest = &docxv1.UpdateBlockRequest{
			UpdateTextElements: &docxv1.UpdateTextElementsRequest{
				Elements: elements,
			},
		}

	default:
		return nil, fmt.Errorf("invalid update_type: %s (must be 'text_elements', 'text_style', or 'text_full')", args.UpdateType)
	}

	// Execute the update
	req := docxv1.NewPatchDocumentBlockReqBuilder().
		DocumentId(args.DocumentID).
		BlockId(args.BlockID).
		UpdateBlockRequest(updateRequest).
		Build()

	resp, err := client.Client().Docx.V1.DocumentBlock.Patch(ctx.Ctx, req,
		larkcore.WithUserAccessToken(client.AccessToken()))
	if err != nil {
		return nil, fmt.Errorf("update block: %w", err)
	}
	if !resp.Success() {
		return nil, NewAPIError(resp.Code, resp.Msg)
	}

	return tools.NewResult(fmt.Sprintf("✅ Block %s updated successfully", args.BlockID)), nil
}

// ptrToInt returns a pointer to an int
func ptrToInt(i int) *int { return &i }

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

	resp, err := client.Client().Docx.V1.DocumentBlockChildren.BatchDelete(ctx.Ctx, req,
		larkcore.WithUserAccessToken(client.AccessToken()))
	if err != nil {
		return nil, fmt.Errorf("delete blocks: %w", err)
	}
	if !resp.Success() {
		return nil, NewAPIError(resp.Code, resp.Msg)
	}

	count := args.EndIndex - args.StartIndex
	return tools.NewResult(fmt.Sprintf("✅ Deleted %d block(s) from index %d to %d",
		count, args.StartIndex, args.EndIndex)), nil
}
