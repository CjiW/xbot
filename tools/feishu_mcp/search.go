package feishu_mcp

import (
	"encoding/json"
	"fmt"

	"xbot/llm"
	"xbot/tools"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	docxv1 "github.com/larksuite/oapi-sdk-go/v3/service/docx/v1"
	wikiv2 "github.com/larksuite/oapi-sdk-go/v3/service/wiki/v2"
)

// WikiListSpacesTool lists all Wiki spaces the user has access to.
type WikiListSpacesTool struct {
	MCP *FeishuMCP
}

func (t *WikiListSpacesTool) Name() string { return "feishu_wiki_list_spaces" }

func (t *WikiListSpacesTool) Description() string {
	return "List all Wiki knowledge spaces you have access to."
}

func (t *WikiListSpacesTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{}
}

func (t *WikiListSpacesTool) Execute(ctx *tools.ToolContext, input string) (*tools.ToolResult, error) {
	client, err := t.MCP.GetClient(ctx.Ctx, ctx.Channel, ctx.ChatID)
	if err != nil {
		return nil, err
	}

	req := wikiv2.NewListSpaceReqBuilder().
		Build()

	resp, err := client.Client().Wiki.V2.Space.List(ctx.Ctx, req,
		larkcore.WithUserAccessToken(client.AccessToken()))
	if err != nil {
		return nil, fmt.Errorf("list wiki spaces: %w", err)
	}
	if !resp.Success() {
		return nil, NewAPIError(resp.Code, resp.Msg)
	}

	if len(resp.Data.Items) == 0 {
		return tools.NewResultWithTips("No Wiki spaces found", "You may need to create a Wiki space first or check your permissions."), nil
	}

	var result []map[string]any
	for _, item := range resp.Data.Items {
		space := map[string]any{
			"space_id":    item.SpaceId,
			"name":        item.Name,
			"description": item.Description,
			"space_type":  item.SpaceType,
			"visibility":  item.Visibility,
		}
		result = append(result, space)
	}

	summary, _ := json.MarshalIndent(result, "", "  ")
	return tools.NewResultWithTips(
		fmt.Sprintf("Found %d Wiki space(s):\n%s", len(result), summary),
		"Use feishu_wiki_list_nodes with a space_id to list pages in a Wiki space.",
	), nil
}

// WikiListNodesTool lists nodes within a Wiki space.
type WikiListNodesTool struct {
	MCP *FeishuMCP
}

func (t *WikiListNodesTool) Name() string { return "feishu_wiki_list_nodes" }

func (t *WikiListNodesTool) Description() string {
	return "List nodes (pages) within a Wiki space."
}

func (t *WikiListNodesTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{
			Name:        "space_id",
			Type:        "string",
			Description: "Wiki space ID from feishu_wiki_list_spaces",
			Required:    true,
		},
		{
			Name:        "parent_node_token",
			Type:        "string",
			Description: "Parent node token to list children (optional, lists all nodes if not specified)",
			Required:    false,
		},
		{
			Name:        "page_token",
			Type:        "string",
			Description: "Page token for pagination (optional)",
			Required:    false,
		},
	}
}

func (t *WikiListNodesTool) Execute(ctx *tools.ToolContext, input string) (*tools.ToolResult, error) {
	var args struct {
		SpaceID         string `json:"space_id"`
		ParentNodeToken string `json:"parent_node_token"`
		PageToken       string `json:"page_token"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}

	client, err := t.MCP.GetClient(ctx.Ctx, ctx.Channel, ctx.ChatID)
	if err != nil {
		return nil, err
	}

	reqBuilder := wikiv2.NewListSpaceNodeReqBuilder().
		SpaceId(args.SpaceID)

	if args.ParentNodeToken != "" {
		reqBuilder.ParentNodeToken(args.ParentNodeToken)
	}

	if args.PageToken != "" {
		reqBuilder.PageToken(args.PageToken)
	}

	req := reqBuilder.Build()

	resp, err := client.Client().Wiki.V2.SpaceNode.List(ctx.Ctx, req,
		larkcore.WithUserAccessToken(client.AccessToken()))
	if err != nil {
		return nil, fmt.Errorf("list wiki nodes: %w", err)
	}
	if !resp.Success() {
		return nil, NewAPIError(resp.Code, resp.Msg)
	}

	if len(resp.Data.Items) == 0 {
		return tools.NewResultWithTips("No nodes found in this Wiki space", "The space may be empty or you may not have permission to view it."), nil
	}

	var result []map[string]any
	for _, item := range resp.Data.Items {
		nodeToken := ""
		if item.NodeToken != nil {
			nodeToken = *item.NodeToken
		}
		objType := ""
		if item.ObjType != nil {
			objType = *item.ObjType
		}
		title := ""
		if item.Title != nil {
			title = *item.Title
		}
		parentToken := ""
		if item.ParentNodeToken != nil {
			parentToken = *item.ParentNodeToken
		}
		hasChild := false
		if item.HasChild != nil {
			hasChild = *item.HasChild
		}

		node := map[string]any{
			"node_token":   nodeToken,
			"parent_token": parentToken,
			"obj_type":     objType,
			"title":        title,
			"has_child":    hasChild,
			"url":          client.BuildURL(nodeToken, objType),
		}
		result = append(result, node)
	}

	summary := fmt.Sprintf("Found %d node(s)", len(result))
	detail, _ := json.MarshalIndent(result, "", "  ")
	return tools.NewResultWithDetail(summary, string(detail)), nil
}

// WikiGetNodeTool gets node details and content.
type WikiGetNodeTool struct {
	MCP *FeishuMCP
}

func (t *WikiGetNodeTool) Name() string { return "feishu_wiki_get_node" }

func (t *WikiGetNodeTool) Description() string {
	return "Get Wiki node details and content. Supports Wiki node tokens (wikcnXXXXX) and document tokens (doxcnXXXXX, bascXXXXX)."
}

func (t *WikiGetNodeTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{
			Name:        "token",
			Type:        "string",
			Description: "Node token or document token (e.g., wikcnXXXXX, doxcnXXXXX, bascXXXXX)",
			Required:    true,
		},
		{
			Name:        "obj_type",
			Type:        "string",
			Description: "Object type: docx, wiki, bitable, etc. (auto-detected if not provided)",
			Required:    false,
		},
	}
}

func (t *WikiGetNodeTool) Execute(ctx *tools.ToolContext, input string) (*tools.ToolResult, error) {
	var args struct {
		Token   string `json:"token"`
		ObjType string `json:"obj_type"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}

	client, err := t.MCP.GetClient(ctx.Ctx, ctx.Channel, ctx.ChatID)
	if err != nil {
		return nil, err
	}

	// Auto-detect obj_type from token prefix if not provided
	objType := args.ObjType
	if objType == "" {
		switch {
		case len(args.Token) >= 5 && args.Token[:5] == "wikcn":
			objType = "wiki"
		case len(args.Token) >= 5 && args.Token[:5] == "doxcn":
			objType = "docx"
		case len(args.Token) >= 4 && args.Token[:4] == "basc":
			objType = "bitable"
		case len(args.Token) >= 5 && args.Token[:5] == "shtcn":
			objType = "sheet"
		case len(args.Token) >= 5 && args.Token[:5] == "ndtbn":
			objType = "mindnote"
		case len(args.Token) >= 5 && args.Token[:5] == "pptcn":
			objType = "slides"
		case len(args.Token) >= 5 && args.Token[:5] == "filcn":
			objType = "file"
		default:
			// Default to wiki for unknown tokens
			objType = "wiki"
		}
	}

	// First try Wiki API (for documents in Wiki spaces)
	req := wikiv2.NewGetNodeSpaceReqBuilder().
		Token(args.Token).
		ObjType(objType).
		Build()

	resp, err := client.Client().Wiki.V2.Space.GetNode(ctx.Ctx, req,
		larkcore.WithUserAccessToken(client.AccessToken()))

	// If Wiki API fails and it's a docx document, try the Docx API directly
	if err != nil || !resp.Success() {
		if objType == "docx" || (objType == "" && len(args.Token) >= 5 && args.Token[:4] == "doxc") {
			return t.getDocxDocument(ctx, client, args.Token)
		}
		if err != nil {
			return nil, fmt.Errorf("get wiki node: %w", err)
		}
		return nil, NewAPIError(resp.Code, resp.Msg)
	}

	if resp.Data.Node == nil {
		// Try fallback to docx API for docx tokens
		if objType == "docx" {
			return t.getDocxDocument(ctx, client, args.Token)
		}
		return tools.NewResultWithTips("Node not found - the document may not be in a Wiki space yet", "Try using feishu_docx_get_content if you have a document token (doxcnXXX)."), nil
	}

	node := resp.Data.Node
	var nodeToken, objToken, title string // Declare new variables
	if node.NodeToken != nil {
		nodeToken = *node.NodeToken
	}
	objType = "" // Reuse existing variable
	if node.ObjType != nil {
		objType = *node.ObjType
	}
	if node.ObjToken != nil {
		objToken = *node.ObjToken
	}
	if node.Title != nil {
		title = *node.Title
	}

	result := map[string]any{
		"space_id":        node.SpaceId,
		"node_token":      nodeToken,
		"parent_token":    node.ParentNodeToken,
		"obj_type":        objType,
		"obj_token":       objToken,
		"title":           title,
		"node_type":       node.NodeType,
		"has_child":       node.HasChild,
		"obj_create_time": node.ObjCreateTime,
		"obj_edit_time":   node.ObjEditTime,
		"url":             client.BuildURL(nodeToken, objType),
	}

	summary, _ := json.MarshalIndent(result, "", "  ")
	return tools.NewResult(fmt.Sprintf("Node info:\n\n📄 **%s**\n🔗 URL: %s\n\nDetails:\n%s",
		title, client.BuildURL(nodeToken, objType), summary)), nil
}

// getDocxDocument gets document info directly from Docx API for documents not in Wiki spaces.
func (t *WikiGetNodeTool) getDocxDocument(ctx *tools.ToolContext, client *Client, documentID string) (*tools.ToolResult, error) {
	req := docxv1.NewGetDocumentReqBuilder().
		DocumentId(documentID).
		Build()

	resp, err := client.Client().Docx.V1.Document.Get(ctx.Ctx, req,
		larkcore.WithUserAccessToken(client.AccessToken()))
	if err != nil {
		return nil, fmt.Errorf("get document: %w", err)
	}
	if !resp.Success() {
		return nil, NewAPIError(resp.Code, resp.Msg)
	}

	doc := resp.Data.Document
	docID := ""
	if doc.DocumentId != nil {
		docID = *doc.DocumentId
	}
	title := ""
	if doc.Title != nil {
		title = *doc.Title
	}

	return tools.NewResultWithTips(
		fmt.Sprintf("📄 Document: **%s**\n\n🔗 URL: %s\n\nNote: This document is not yet in a Wiki space.", title, client.BuildURL(docID, "docx")),
		"Use feishu_docx_get_content to read the document content, or feishu_wiki_create_node to add it to a Wiki space.",
	), nil
}

// WikiMoveNodeTool moves a Wiki node to another parent node.
type WikiMoveNodeTool struct {
	MCP *FeishuMCP
}

func (t *WikiMoveNodeTool) Name() string { return "feishu_wiki_move_node" }

func (t *WikiMoveNodeTool) Description() string {
	return "Move a Wiki node to another parent node within the same or different Wiki space."
}

func (t *WikiMoveNodeTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{
			Name:        "space_id",
			Type:        "string",
			Description: "Source Wiki space ID",
			Required:    true,
		},
		{
			Name:        "node_token",
			Type:        "string",
			Description: "Node token to move (e.g., wikcnXXXXX)",
			Required:    true,
		},
		{
			Name:        "target_parent_token",
			Type:        "string",
			Description: "Target parent node token where the node will be moved to",
			Required:    true,
		},
		{
			Name:        "target_space_id",
			Type:        "string",
			Description: "Target space ID (optional, defaults to same space if not specified)",
			Required:    false,
		},
	}
}

func (t *WikiMoveNodeTool) Execute(ctx *tools.ToolContext, input string) (*tools.ToolResult, error) {
	var args struct {
		SpaceID           string `json:"space_id"`
		NodeToken         string `json:"node_token"`
		TargetParentToken string `json:"target_parent_token"`
		TargetSpaceID     string `json:"target_space_id"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}

	client, err := t.MCP.GetClient(ctx.Ctx, ctx.Channel, ctx.ChatID)
	if err != nil {
		return nil, err
	}

	// Build the move request body
	bodyBuilder := wikiv2.NewMoveSpaceNodeReqBodyBuilder().
		TargetParentToken(args.TargetParentToken)

	// If target_space_id is specified, add it to the request
	if args.TargetSpaceID != "" {
		bodyBuilder.TargetSpaceId(args.TargetSpaceID)
	}

	body := bodyBuilder.Build()

	req := wikiv2.NewMoveSpaceNodeReqBuilder().
		SpaceId(args.SpaceID).
		NodeToken(args.NodeToken).
		Body(body).
		Build()

	resp, err := client.Client().Wiki.V2.SpaceNode.Move(ctx.Ctx, req,
		larkcore.WithUserAccessToken(client.AccessToken()))
	if err != nil {
		return nil, fmt.Errorf("move wiki node: %w", err)
	}
	if !resp.Success() {
		return nil, NewAPIError(resp.Code, resp.Msg)
	}

	return tools.NewResult(fmt.Sprintf("✅ Node %s moved successfully to parent %s",
		args.NodeToken, args.TargetParentToken)), nil
}

// WikiCreateNodeTool creates a new node in a Wiki space, optionally with a new document.
type WikiCreateNodeTool struct {
	MCP *FeishuMCP
}

func (t *WikiCreateNodeTool) Name() string { return "feishu_wiki_create_node" }

func (t *WikiCreateNodeTool) Description() string {
	return "Create a new node in a Wiki knowledge space. Can create a new document or add an existing document to the wiki."
}

func (t *WikiCreateNodeTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{
			Name:        "space_id",
			Type:        "string",
			Description: "Wiki space ID from feishu_wiki_list_spaces",
			Required:    true,
		},
		{
			Name:        "title",
			Type:        "string",
			Description: "Node title (document title)",
			Required:    true,
		},
		{
			Name:        "parent_node_token",
			Type:        "string",
			Description: "Parent node token (optional, defaults to root level)",
			Required:    false,
		},
		{
			Name:        "obj_token",
			Type:        "string",
			Description: "Existing document token to add to wiki (optional, e.g., 'doxcnXXXXX'). If not provided, a new document will be created.",
			Required:    false,
		},
		{
			Name:        "obj_type",
			Type:        "string",
			Description: "Document type: docx (default), bitable, sheet, mindnote, file, slides. Only used when obj_token is provided.",
			Required:    false,
		},
	}
}

func (t *WikiCreateNodeTool) Execute(ctx *tools.ToolContext, input string) (*tools.ToolResult, error) {
	var args struct {
		SpaceID         string `json:"space_id"`
		Title           string `json:"title"`
		ParentNodeToken string `json:"parent_node_token"`
		ObjToken        string `json:"obj_token"`
		ObjType         string `json:"obj_type"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}

	client, err := t.MCP.GetClient(ctx.Ctx, ctx.Channel, ctx.ChatID)
	if err != nil {
		return nil, err
	}

	// Build the node
	nodeBuilder := wikiv2.NewNodeBuilder().
		Title(args.Title)

	// If obj_token is provided, add existing document to wiki
	if args.ObjToken != "" {
		objType := args.ObjType
		if objType == "" {
			objType = "docx" // default to docx
		}
		nodeBuilder.ObjToken(args.ObjToken).ObjType(objType)
	}

	// Set parent node if specified
	if args.ParentNodeToken != "" {
		nodeBuilder.ParentNodeToken(args.ParentNodeToken)
	}

	// Create the node in wiki
	req := wikiv2.NewCreateSpaceNodeReqBuilder().
		SpaceId(args.SpaceID).
		Node(nodeBuilder.Build()).
		Build()

	resp, err := client.Client().Wiki.V2.SpaceNode.Create(ctx.Ctx, req,
		larkcore.WithUserAccessToken(client.AccessToken()))
	if err != nil {
		return nil, fmt.Errorf("create wiki node: %w", err)
	}
	if !resp.Success() {
		return nil, NewAPIError(resp.Code, resp.Msg)
	}

	node := resp.Data.Node
	nodeToken := ""
	objToken := ""
	title := args.Title
	if node != nil {
		if node.NodeToken != nil {
			nodeToken = *node.NodeToken
		}
		if node.ObjToken != nil {
			objToken = *node.ObjToken
		}
		if node.Title != nil {
			title = *node.Title
		}
	}

	// Build result
	wikiURL := client.BuildURL(nodeToken, "wiki")
	result := map[string]any{
		"node_token": nodeToken,
		"obj_token":  objToken,
		"title":      title,
		"space_id":   args.SpaceID,
		"url":        wikiURL,
	}

	var summary string
	if args.ObjToken != "" {
		summary = fmt.Sprintf("✅ Added document to Wiki: **%s**\n\n🔗 URL: %s", title, wikiURL)
	} else {
		summary = fmt.Sprintf("✅ Created new Wiki page: **%s**\n\n🔗 URL: %s\n📄 Document ID: %s", title, wikiURL, objToken)
	}

	detail, _ := json.MarshalIndent(result, "", "  ")
	return tools.NewResultWithDetail(summary, string(detail)), nil
}
