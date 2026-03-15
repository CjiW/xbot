package tools

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/commonmark"
	"github.com/go-shiori/go-readability"
	"github.com/tiktoken-go/tokenizer"
	"xbot/llm"
)

// FetchTool 网页获取工具
type FetchTool struct {
	httpClient *http.Client
	converter  *converter.Converter
	tokenizer  tokenizer.Codec
}

// NewFetchTool 创建 FetchTool
func NewFetchTool() *FetchTool {
	// 创建 converter 和 tokenizer（复用）
	conv := converter.NewConverter(
		converter.WithPlugins(commonmark.NewCommonmarkPlugin()),
	)
	enc, _ := tokenizer.Get(tokenizer.Cl100kBase)

	return &FetchTool{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				// 限制最多 5 次重定向
				if len(via) >= 5 {
					return fmt.Errorf("too many redirects")
				}
				return nil
			},
		},
		converter: conv,
		tokenizer: enc,
	}
}

func (t *FetchTool) Name() string {
	return "Fetch"
}

func (t *FetchTool) Description() string {
	return `Fetch a webpage and convert it to LLM-friendly Markdown format.
Use this tool when you need to extract content from a URL.
Parameters (JSON):
  - url: string, the URL to fetch (required)
  - max_tokens: number, maximum output tokens (optional, default: 4096, max: 30000)
Example: {"url": "https://example.com", "max_tokens": 5000}`
}

func (t *FetchTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "url", Type: "string", Description: "The URL to fetch", Required: true},
		{Name: "max_tokens", Type: "number", Description: "Maximum output tokens (default: 4096, max: 30000)", Required: false},
	}
}

// fetchParams Fetch 工具参数
type fetchParams struct {
	URL       string `json:"url"`
	MaxTokens int    `json:"max_tokens"`
}

func (t *FetchTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	// 解析参数
	var params fetchParams
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	if params.URL == "" {
		return nil, fmt.Errorf("url is required")
	}

	// 验证 URL
	if err := validateURL(params.URL); err != nil {
		return nil, err
	}

	// 设置默认 max_tokens
	if params.MaxTokens <= 0 {
		params.MaxTokens = 4096
	}
	if params.MaxTokens > 30000 {
		params.MaxTokens = 30000
	}

	// 发起 HTTP 请求
	resp, err := t.fetchURL(ctx, params.URL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// 检查 Content-Type
	contentType := resp.Header.Get("Content-Type")

	// 读取响应（限制最大 10MB）
	reader := io.LimitedReader{R: resp.Body, N: 10 * 1024 * 1024}
	htmlContent, err := io.ReadAll(&reader)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	// text/plain（如 GitHub raw 文件）直接返回原文
	if strings.Contains(contentType, "text/plain") {
		content := string(htmlContent)
		content, _ = t.truncateByTokens(content, params.MaxTokens)
		return NewResult(content), nil
	}

	// 支持 text/html 和 application/xhtml+xml
	isHTML := strings.Contains(contentType, "text/html") ||
		strings.Contains(contentType, "application/xhtml+xml")
	if !isHTML {
		return nil, fmt.Errorf("unsupported content type: %s", contentType)
	}

	// 使用 go-readability 提取正文
	parsedURL, err := url.Parse(params.URL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse URL: %w", err)
	}
	article, err := readability.FromReader(strings.NewReader(string(htmlContent)), parsedURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse webpage: %w", err)
	}

	// 构建 Markdown 内容
	content := t.formatAsMarkdown(&article, params.URL)

	// Token 截断
	content, _ = t.truncateByTokens(content, params.MaxTokens)

	// 构建输出
	return NewResult(content), nil
}

// fetchURL 获取 URL 内容
func (t *FetchTool) fetchURL(ctx *ToolContext, targetURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx.Ctx, "GET", targetURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// 设置合理的 User-Agent
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; xbot/1.0; +https://github.com/CjiW/xbot)")

	// 不发送 Authorization header
	req.Header.Del("Authorization")

	return t.httpClient.Do(req)
}

// validateURL 验证 URL 安全性
func validateURL(rawURL string) error {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	// 协议检查
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return fmt.Errorf("unsupported protocol: %s (only http and https are allowed)", parsedURL.Scheme)
	}

	host := parsedURL.Hostname()

	// 域名检查：localhost
	if host == "localhost" || host == "localhost.localdomain" {
		return fmt.Errorf("localhost is not allowed")
	}

	// 内网 IP 检查
	if isPrivateIP(host) {
		return fmt.Errorf("private/internal IP addresses are not allowed: %s", host)
	}

	return nil
}

// isPrivateIP 检查是否为内网 IP
func isPrivateIP(host string) bool {
	// 先尝试解析为 IP
	ip := net.ParseIP(host)
	if ip == nil {
		// 如果不是 IP，可能是域名，暂时允许（可通过 DNS 解析进一步检查）
		return false
	}

	// 转换为 IPv4 4字节表示
	ipv4 := ip.To4()
	if ipv4 == nil {
		// 不是 IPv4 地址，可能是 IPv6
		// 对于 IPv6，暂不阻止
		return false
	}

	// 127.0.0.0/8 (loopback)
	if ipv4.IsLoopback() {
		return true
	}

	// 10.0.0.0/8
	if ipv4[0] == 10 {
		return true
	}

	// 172.16.0.0/12
	if ipv4[0] == 172 && ipv4[1] >= 16 && ipv4[1] <= 31 {
		return true
	}

	// 192.168.0.0/16
	if ipv4[0] == 192 && ipv4[1] == 168 {
		return true
	}

	// 169.254.0.0/16 (link-local)
	if ipv4[0] == 169 && ipv4[1] == 254 {
		return true
	}

	// 0.0.0.0/8
	if ipv4[0] == 0 {
		return true
	}

	return false
}

// formatAsMarkdown 将文章格式化为 Markdown
func (t *FetchTool) formatAsMarkdown(article *readability.Article, pageURL string) string {
	var sb strings.Builder

	// 标题
	title := strings.TrimSpace(article.Title)
	if title != "" {
		sb.WriteString("# ")
		sb.WriteString(title)
		sb.WriteString("\n\n")
	}

	// URL
	sb.WriteString("**URL:** ")
	sb.WriteString(pageURL)
	sb.WriteString("\n\n")
	sb.WriteString("---\n\n")

	// 正文 - 将 HTML 转换为 Markdown 格式
	markdownContent := convertHTMLToMarkdown(t.converter, article.Content, pageURL, article.TextContent)
	sb.WriteString(markdownContent)

	return sb.String()
}

// convertHTMLToMarkdown 将 HTML 内容转换为 Markdown 格式
// 注意：converter 应该在 FetchTool 初始化时创建并复用
func convertHTMLToMarkdown(conv *converter.Converter, htmlContent, baseURL string, fallbackText string) string {
	// 如果没有 HTML 内容，使用回退文本
	if htmlContent == "" {
		return fallbackText
	}

	// 转换 HTML 到 Markdown
	markdown, err := conv.ConvertString(htmlContent)
	if err != nil {
		// 如果转换失败，回退到纯文本
		return fallbackText
	}

	return strings.TrimSpace(markdown)
}

// truncateByTokens 按 token 数量截断内容，返回实际 token 数
func (t *FetchTool) truncateByTokens(content string, maxTokens int) (string, int) {
	// 使用结构体中的 tokenizer（已在初始化时创建）
	if t.tokenizer == nil {
		// 如果 tokenizer 未初始化，不截断
		return content, countTokensRoughly(content)
	}

	ids, _, err := t.tokenizer.Encode(content)
	if err != nil {
		// 如果失败，不截断
		return content, countTokensRoughly(content)
	}

	actualTokens := len(ids)

	// 如果不超过限制，直接返回
	if actualTokens <= maxTokens {
		return content, actualTokens
	}

	// 截断到 maxTokens
	truncated := ids[:maxTokens]
	truncatedContent, err := t.tokenizer.Decode(truncated)
	if err != nil {
		// 截断失败，不截断
		return content, actualTokens
	}

	// 添加截断提示
	var sb strings.Builder
	sb.WriteString(truncatedContent)
	fmt.Fprintf(&sb, "\n\n---\n\n*⚠️ 内容已截断（已截取 %d / %d tokens）*", maxTokens, actualTokens)

	return sb.String(), maxTokens
}

// countTokensRoughly 粗略估算 token 数（字符/4 是常见估算）
func countTokensRoughly(content string) int {
	return len(content) / 4
}
