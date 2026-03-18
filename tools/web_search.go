package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
	"xbot/llm"
)

// WebSearchTool 网络搜索工具（基于 Tavily API）
type WebSearchTool struct {
	apiKey     string
	httpClient *http.Client
}

// NewWebSearchTool 创建网络搜索工具
func NewWebSearchTool() *WebSearchTool {
	return &WebSearchTool{
		apiKey: os.Getenv("TAVILY_API_KEY"),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (t *WebSearchTool) Name() string {
	return "WebSearch"
}

func (t *WebSearchTool) Description() string {
	return `Search the web for real-time information using Tavily API.
Use this tool when you need up-to-date information that might not be in your training data.
Parameters (JSON):
  - query: string, the search query (required)
  - search_depth: string, "basic" or "advanced" (optional, default: "basic")
  - max_results: number, maximum number of results to return (optional, default: 5, max: 10)
  - include_answer: boolean, whether to include an AI-generated answer (optional, default: true)
Example: {"query": "latest news about AI", "max_results": 5}`
}

func (t *WebSearchTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "query", Type: "string", Description: "The search query to look up on the web", Required: true},
		{Name: "search_depth", Type: "string", Description: "Search depth: 'basic' or 'advanced'", Required: false},
		{Name: "max_results", Type: "number", Description: "Maximum number of results (1-10)", Required: false},
		{Name: "include_answer", Type: "boolean", Description: "Include AI-generated answer summary", Required: false},
	}
}

// TavilySearchRequest Tavily 搜索请求
type TavilySearchRequest struct {
	Query         string `json:"query"`
	SearchDepth   string `json:"search_depth,omitempty"`
	MaxResults    int    `json:"max_results,omitempty"`
	IncludeAnswer bool   `json:"include_answer,omitempty"`
}

// TavilySearchResult Tavily 搜索结果
type TavilySearchResult struct {
	Title   string  `json:"title"`
	URL     string  `json:"url"`
	Content string  `json:"content"`
	Score   float64 `json:"score"`
}

// TavilySearchResponse Tavily 搜索响应
type TavilySearchResponse struct {
	Query   string               `json:"query"`
	Answer  string               `json:"answer,omitempty"`
	Results []TavilySearchResult `json:"results"`
}

func (t *WebSearchTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	// 检查 API Key
	if t.apiKey == "" {
		return nil, fmt.Errorf("TAVILY_API_KEY environment variable is not set")
	}

	// 解析输入参数
	params, err := ParseInput[struct {
		Query         string `json:"query"`
		SearchDepth   string `json:"search_depth"`
		MaxResults    int    `json:"max_results"`
		IncludeAnswer *bool  `json:"include_answer"`
	}](input)
	if err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	if params.Query == "" {
		return nil, fmt.Errorf("query is required")
	}

	// 设置默认值
	searchDepth := "basic"
	if params.SearchDepth == "advanced" {
		searchDepth = "advanced"
	}

	maxResults := 5
	if params.MaxResults > 0 && params.MaxResults <= 10 {
		maxResults = params.MaxResults
	}

	includeAnswer := true
	if params.IncludeAnswer != nil {
		includeAnswer = *params.IncludeAnswer
	}

	// 构建请求
	reqBody := TavilySearchRequest{
		Query:         params.Query,
		SearchDepth:   searchDepth,
		MaxResults:    maxResults,
		IncludeAnswer: includeAnswer,
	}

	reqJSON, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// 发送请求（支持 context 取消）
	req, err := http.NewRequestWithContext(ctx.Ctx, "POST", "https://api.tavily.com/search", bytes.NewBuffer(reqJSON))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+t.apiKey)

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	// 读取响应
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tavily API error (status %d): %s", resp.StatusCode, string(body))
	}

	// 解析响应
	var searchResp TavilySearchResponse
	if err := json.Unmarshal(body, &searchResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	// 格式化输出
	return NewResult(formatSearchResults(&searchResp)), nil
}

// formatSearchResults 格式化搜索结果
func formatSearchResults(resp *TavilySearchResponse) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "# Web Search Results for: %s\n\n", resp.Query)

	// 如果有 AI 生成的答案，先显示
	if resp.Answer != "" {
		sb.WriteString("## Summary\n")
		sb.WriteString(resp.Answer)
		sb.WriteString("\n\n")
	}

	// 显示搜索结果
	if len(resp.Results) > 0 {
		sb.WriteString("## Sources\n\n")
		for i, result := range resp.Results {
			fmt.Fprintf(&sb, "### %d. %s\n", i+1, result.Title)
			fmt.Fprintf(&sb, "**URL:** %s\n\n", result.URL)
			if result.Content != "" {
				sb.WriteString(result.Content)
				sb.WriteString("\n\n")
			}
		}
	} else {
		sb.WriteString("No results found.\n")
	}

	return sb.String()
}
