package feishu_mcp

import (
	"encoding/json"
	"fmt"

	"xbot/llm"
	"xbot/tools"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	wikiv2 "github.com/larksuite/oapi-sdk-go/v3/service/wiki/v2"
)

// SearchWikiTool searches across Wiki spaces by listing nodes.
// Note: This is a basic implementation that lists and filters nodes.
// For full-text search, the search v2 API requires setting up data sources first.
type SearchWikiTool struct {
	MCP *FeishuMCP
}

func (t *SearchWikiTool) Name() string { return "feishu_search_wiki" }

func (t *SearchWikiTool) Description() string {
	return "Search across Wiki spaces for documents matching a query. " +
		"STEP 1: First call oauth_authorize with provider='feishu' if not already authorized. " +
		"STEP 2: Then call this tool with search query. Note: This searches titles by listing nodes."
}

func (t *SearchWikiTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{
			Name:        "query",
			Type:        "string",
			Description: "Search query text (matches against node titles)",
			Required:    true,
		},
		{
			Name:        "space_id",
			Type:        "string",
			Description: "Specific Wiki space ID to search (optional, searches all spaces if not provided)",
			Required:    false,
		},
		{
			Name:        "limit",
			Type:        "string",
			Description: "Maximum number of results to return (default: 50)",
			Required:    false,
		},
	}
}

func (t *SearchWikiTool) Execute(ctx *tools.ToolContext, input string) (*tools.ToolResult, error) {
	var args struct {
		Query   string `json:"query"`
		SpaceID string `json:"space_id"`
		Limit   string `json:"limit"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}

	client, err := t.MCP.GetClient(ctx.Ctx, ctx.Channel, ctx.ChatID)
	if err != nil {
		return nil, err
	}

	// First, list all wiki spaces if no specific space_id provided
	var spaceIDs []string
	if args.SpaceID != "" {
		spaceIDs = []string{args.SpaceID}
	} else {
		spacesReq := wikiv2.NewListSpaceReqBuilder().Build()
		spacesResp, err := client.Client().Wiki.V2.Space.List(ctx.Ctx, spacesReq,
			larkcore.WithUserAccessToken(client.AccessToken()))
		if err != nil {
			return nil, fmt.Errorf("list wiki spaces: %w", err)
		}
		if !spacesResp.Success() {
			return nil, NewAPIError(spacesResp.Code, spacesResp.Msg)
		}
		for _, space := range spacesResp.Data.Items {
			if space.SpaceId != nil {
				spaceIDs = append(spaceIDs, *space.SpaceId)
			}
		}
	}

	// Search through nodes in each space
	var results []map[string]any
	maxResults := 50
	if args.Limit != "" {
		fmt.Sscanf(args.Limit, "%d", &maxResults)
	}

	for _, spaceID := range spaceIDs {
		if len(results) >= maxResults {
			break
		}

		nodesReq := wikiv2.NewListSpaceNodeReqBuilder().
			SpaceId(spaceID).
			Build()

		nodesResp, err := client.Client().Wiki.V2.SpaceNode.List(ctx.Ctx, nodesReq,
			larkcore.WithUserAccessToken(client.AccessToken()))
		if err != nil {
			continue // Skip spaces we can't access
		}
		if !nodesResp.Success() {
			continue
		}

		for _, node := range nodesResp.Data.Items {
			if len(results) >= maxResults {
				break
			}
			// Simple title matching (case-insensitive)
			if node.Title != nil {
				title := *node.Title
				if containsIgnoreCase(title, args.Query) {
					result := map[string]any{
						"title":        node.Title,
						"space_id":     spaceID,
						"node_token":   node.NodeToken,
						"obj_type":     node.ObjType,
						"obj_token":    node.ObjToken,
						"parent_token": node.ParentNodeToken,
					}
					results = append(results, result)
				}
			}
		}
	}

	if len(results) == 0 {
		return tools.NewResult("No matching results found"), nil
	}

	summary := fmt.Sprintf("Found %d result(s)", len(results))
	detail, _ := json.MarshalIndent(results, "", "  ")
	return tools.NewResultWithDetail(summary, string(detail)), nil
}

// containsIgnoreCase checks if a string contains a substring (case-insensitive).
func containsIgnoreCase(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr ||
		len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && findIgnoreCase(s, substr)))
}

func findIgnoreCase(s, substr string) bool {
	s = toLower(s)
	substr = toLower(substr)
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func toLower(s string) string {
	result := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			result[i] = c + 32
		} else {
			result[i] = c
		}
	}
	return string(result)
}
