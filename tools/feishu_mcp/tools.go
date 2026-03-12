package feishu_mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"xbot/llm"
	"xbot/tools"
)

// SearchWikiTool searches wiki nodes via Feishu open API /wiki/v2/nodes/search.
// This endpoint is currently not available in the Go SDK, so it is called directly with HTTP.
type SearchWikiTool struct {
	FeishuToolBase
	MCP *FeishuMCP
}

func (t *SearchWikiTool) Name() string { return "feishu_search_wiki" }

func (t *SearchWikiTool) Description() string {
	return "Search Wiki documents by query using Feishu's native wiki search endpoint."
}

func (t *SearchWikiTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{
			Name:        "query",
			Type:        "string",
			Description: "Search keyword (max 50 chars).",
			Required:    true,
		},
		{
			Name:        "space_id",
			Type:        "string",
			Description: "Wiki space ID (numeric string like '7123456789012345678', from feishu_wiki_list_spaces). Optional, searches all spaces if omitted.",
			Required:    false,
		},
		{
			Name:        "node_id",
			Type:        "string",
			Description: "Wiki node ID to scope search to this node and descendants. Requires space_id when provided.",
			Required:    false,
		},
		{
			Name:        "limit",
			Type:        "string",
			Description: "Maximum number of results to return (default: 50, max: 200)",
			Required:    false,
		},
	}
}

func (t *SearchWikiTool) Execute(ctx *tools.ToolContext, input string) (*tools.ToolResult, error) {
	var args struct {
		Query   string `json:"query"`
		SpaceID string `json:"space_id"`
		NodeID  string `json:"node_id"`
		Limit   string `json:"limit"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}
	if args.Query == "" {
		return nil, fmt.Errorf("query is required")
	}
	if args.NodeID != "" && args.SpaceID == "" {
		return nil, fmt.Errorf("space_id is required when node_id is provided")
	}

	client, err := t.MCP.GetClient(ctx.Ctx, ctx.Channel, ctx.ChatID)
	if err != nil {
		return nil, err
	}

	maxResults := 50
	if args.Limit != "" {
		parsedLimit, parseErr := strconv.Atoi(args.Limit)
		if parseErr != nil {
			return nil, fmt.Errorf("invalid limit: %w", parseErr)
		}
		maxResults = parsedLimit
	}
	if maxResults <= 0 {
		maxResults = 50
	}
	if maxResults > 200 {
		maxResults = 200
	}

	var results []map[string]any
	pageToken := ""
	hasMore := true
	for hasMore && len(results) < maxResults {
		pageSize := maxResults - len(results)
		if pageSize > 50 {
			pageSize = 50
		}

		resp, err := t.searchWiki(ctx, client.AccessToken(), args.Query, args.SpaceID, args.NodeID, pageToken, pageSize)
		if err != nil {
			return nil, err
		}

		for _, item := range resp.Data.Items {
			if len(results) >= maxResults {
				break
			}
			results = append(results, map[string]any{
				"title":     item.Title,
				"space_id":  item.SpaceID,
				"node_id":   item.NodeID,
				"obj_type":  item.ObjType,
				"obj_token": item.ObjToken,
				"url":       item.URL,
			})
		}

		hasMore = resp.Data.HasMore
		pageToken = resp.Data.PageToken
		if !hasMore || pageToken == "" {
			break
		}
	}

	if len(results) == 0 {
		return tools.NewResultWithTips("No matching results found", "Try different search keywords, or narrow with space_id/node_id to search within a specific Wiki scope."), nil
	}

	summary := fmt.Sprintf("Found %d result(s)", len(results))
	detail, _ := json.MarshalIndent(results, "", "  ")
	return tools.NewResultWithDetail(summary, string(detail)), nil
}

type wikiSearchResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		Items []struct {
			NodeID   string `json:"node_id"`
			SpaceID  string `json:"space_id"`
			ObjType  int    `json:"obj_type"`
			ObjToken string `json:"obj_token"`
			Title    string `json:"title"`
			URL      string `json:"url"`
		} `json:"items"`
		PageToken string `json:"page_token"`
		HasMore   bool   `json:"has_more"`
	} `json:"data"`
}

func (t *SearchWikiTool) searchWiki(ctx *tools.ToolContext, accessToken, query, spaceID, nodeID, pageToken string, pageSize int) (*wikiSearchResponse, error) {
	reqBody := map[string]any{
		"query": query,
	}
	if spaceID != "" {
		reqBody["space_id"] = spaceID
	}
	if nodeID != "" {
		reqBody["node_id"] = nodeID
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal wiki search request: %w", err)
	}

	u, err := url.Parse("https://open.feishu.cn/open-apis/wiki/v2/nodes/search")
	if err != nil {
		return nil, fmt.Errorf("build wiki search URL: %w", err)
	}
	q := u.Query()
	q.Set("page_size", strconv.Itoa(pageSize))
	if pageToken != "" {
		q.Set("page_token", pageToken)
	}
	u.RawQuery = q.Encode()

	httpReq, err := http.NewRequestWithContext(ctx.Ctx, http.MethodPost, u.String(), bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("build wiki search request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+accessToken)
	httpReq.Header.Set("Content-Type", "application/json; charset=utf-8")

	httpResp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("call wiki search API: %w", err)
	}
	defer httpResp.Body.Close()

	respBytes, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read wiki search response: %w", err)
	}

	var resp wikiSearchResponse
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return nil, fmt.Errorf("decode wiki search response: %w", err)
	}

	if resp.Code != 0 {
		return nil, NewAPIErrorWithDetails(resp.Code, resp.Msg, string(respBytes))
	}

	return &resp, nil
}
