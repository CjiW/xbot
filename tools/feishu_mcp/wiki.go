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
	return "List all Wiki knowledge spaces you have access to. " +
		"STEP 1: First call oauth_authorize with provider='feishu' if not already authorized. " +
		"STEP 2: Then call this tool to list all available Wiki spaces. " +
		"IMPORTANT: When presenting results to users, always convert space_id to a clickable URL format: " +
		"\"https://xxx.feishu.cn/wiki/{space_id}\" (tell users to replace xxx with their Feishu domain)."
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
		return tools.NewResult("No Wiki spaces found"), nil
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
	return tools.NewResult(fmt.Sprintf("Found %d Wiki space(s):\n%s", len(result), summary)), nil
}

// WikiListNodesTool lists nodes within a Wiki space.
type WikiListNodesTool struct {
	MCP *FeishuMCP
}

func (t *WikiListNodesTool) Name() string { return "feishu_wiki_list_nodes" }

func (t *WikiListNodesTool) Description() string {
	return "List nodes (pages) within a Wiki space. " +
		"STEP 1: First call oauth_authorize with provider='feishu' if not already authorized. " +
		"STEP 2: Then call this tool with space_id from feishu_wiki_list_spaces. " +
		"IMPORTANT: Always present node_token to users as a URL: \"https://xxx.feishu.cn/wiki/{node_token}\" " +
		"(replace xxx with user's Feishu domain)."
}

func (t *WikiListNodesTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{
			Name:        "space_id",
			Type:        "string",
			Description: "Wiki space ID",
			Required:    true,
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
		SpaceID   string `json:"space_id"`
		PageToken string `json:"page_token"`
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
		return tools.NewResult("No nodes found in this Wiki space"), nil
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
			"url":          BuildFeishuURL(nodeToken, objType),
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
	return "Get Wiki node details and content. Supports Wiki node tokens (wikcnXXXXX) and document tokens (doxcnXXXXX, bascXXXXX). " +
		"Note: For newly created documents not yet in a Wiki space, this will return basic document info. " +
		"STEP 1: First call oauth_authorize with provider='feishu' if not already authorized. " +
		"STEP 2: Then call this tool with the token from the URL or node list. " +
		"CRITICAL: Always present the URL to users, not just the token. Format: \"https://xxx.feishu.cn/{type}/{token}\" " +
		"where type=wiki for wikcnXXX, type=docx for doxcnXXX, type=base for bascXXX."
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
		case len(args.Token) >= 6 && args.Token[:5] == "wikcn":
			objType = "wiki"
		case len(args.Token) >= 6 && args.Token[:5] == "doxcn":
			objType = "docx"
		case len(args.Token) >= 6 && args.Token[:4] == "basc":
			objType = "bitable"
		default:
			// Try docx as default for unknown tokens
			objType = "docx"
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
		return tools.NewResult("Node not found - the document may not be in a Wiki space yet"), nil
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
		"url":             BuildFeishuURL(nodeToken, objType),
	}

	summary, _ := json.MarshalIndent(result, "", "  ")
	return tools.NewResult(fmt.Sprintf("Node info:\n\n📄 **%s**\n🔗 URL: %s\n\nDetails:\n%s",
		title, BuildFeishuURL(nodeToken, objType), summary)), nil
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

	return tools.NewResult(fmt.Sprintf("📄 Document created: **%s**\n\n🔗 URL: https://xxx.feishu.cn/docx/%s\n\nNote: Replace 'xxx' with your Feishu domain. This document is not yet in a Wiki space.",
		title, docID)), nil
}
