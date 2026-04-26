package tools

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/go-shiori/go-readability"
	"github.com/tiktoken-go/tokenizer"
	"xbot/llm"
)

// FetchTool 网页获取工具
type FetchTool struct {
	// Note: httpClient removed — each request now creates a fresh transport with DNS rebinding protection.
	tokenizer tokenizer.Codec
}

// NewFetchTool 创建 FetchTool
func NewFetchTool() *FetchTool {
	// 创建 tokenizer（复用）
	enc, err := tokenizer.Get(tokenizer.Cl100kBase)
	if err != nil {
		slog.Warn("Failed to initialize tokenizer, token counting will use rough estimation", "error", err)
	}

	return &FetchTool{
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
	params, err := parseToolArgs[fetchParams](input)
	if err != nil {
		return nil, err
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
		// 格式化为与 HTML 一致的输出（带 URL 和分隔线）
		content = t.formatAsPlainText(content, params.URL)
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
// 使用自定义 dialer 防止 DNS rebinding：在 TCP 连接建立时验证目标 IP 不是内网地址。
func (t *FetchTool) fetchURL(ctx *ToolContext, targetURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx.Ctx, "GET", targetURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// 设置合理的 User-Agent
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; xbot/1.0; +https://github.com/ai-pivot/xbot)")

	// 不发送 Authorization header
	req.Header.Del("Authorization")

	// 创建自定义 Transport，在拨号时验证 IP 不是内网地址（防止 DNS rebinding TOCTOU）
	safeTransport := &http.Transport{
		DialContext: func(dialCtx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, fmt.Errorf("invalid address %q: %w", addr, err)
			}
			// 解析 IP，验证不是内网地址
			ips, err := net.DefaultResolver.LookupIPAddr(dialCtx, host)
			if err != nil {
				return nil, fmt.Errorf("DNS lookup failed for %q: %w", host, err)
			}
			if len(ips) == 0 {
				return nil, fmt.Errorf("no IP addresses found for %q", host)
			}
			// 选取第一个 IPv4 或 IPv6 地址
			chosenIP := ips[0].IP
			for _, ipAddr := range ips {
				if v4 := ipAddr.IP.To4(); v4 != nil {
					chosenIP = v4
					break
				}
			}
			if isPrivateIPRaw(chosenIP) {
				return nil, fmt.Errorf("DNS rebinding protection: %q resolves to private IP %s", host, chosenIP)
			}
			dialer := &net.Dialer{}
			return dialer.DialContext(dialCtx, network, net.JoinHostPort(chosenIP.String(), port))
		},
	}

	client := &http.Client{
		Timeout:   30 * time.Second,
		Transport: safeTransport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			// 重定向时也验证目标不是内网
			host := req.URL.Hostname()
			ips, err := net.DefaultResolver.LookupIPAddr(req.Context(), host)
			if err != nil {
				return fmt.Errorf("redirect DNS lookup failed for %s: %w", host, err)
			}
			for _, ipAddr := range ips {
				if isPrivateIPRaw(ipAddr.IP) {
					return fmt.Errorf("redirect to private IP %s blocked (DNS rebinding protection)", ipAddr.IP)
				}
			}
			return nil
		},
	}

	return client.Do(req)
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

	// S-02: DNS 解析验证 — 防止域名解析到内网 IP（DNS rebinding attack）
	ips, err := net.LookupIP(host)
	if err == nil {
		for _, ip := range ips {
			if isPrivateIPRaw(ip) {
				return fmt.Errorf("hostname %s resolves to private/internal IP %s (DNS rebinding protection)", host, ip)
			}
		}
	}

	return nil
}

// isPrivateIP 检查主机名是否为内网 IP（仅对字面 IP 地址检查，不含 DNS 解析）
// S-03: 重构支持 IPv6 私有地址检测，内部委托 isPrivateIPRaw
func isPrivateIP(host string) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		// 不是字面 IP 地址（可能是域名），DNS 解析检查在 validateURL 中单独处理
		return false
	}
	return isPrivateIPRaw(ip)
}

// isPrivateIPRaw 检查 IP 地址（IPv4 或 IPv6）是否为私有/内网地址
// S-03: 新增 IPv6 私有地址检查（loopback、ULA、link-local、IPv4-mapped）
func isPrivateIPRaw(ip net.IP) bool {
	// IPv4-mapped IPv6 addresses (::ffff:x.x.x.x)
	if ip4 := ip.To4(); ip4 != nil {
		return isPrivateIPv4(ip4)
	}

	// 原生 IPv6 检查
	if ip.IsLoopback() { // ::1
		return true
	}

	// fc00::/7 — Unique Local Addresses (ULA/私有)
	if len(ip) >= 1 && (ip[0]&0xfe) == 0xfc {
		return true
	}

	// fe80::/10 — Link-Local Addresses
	if len(ip) >= 2 && ip[0] == 0xfe && (ip[1]&0xc0) == 0x80 {
		return true
	}

	return false
}

// isPrivateIPv4 检查 IPv4 地址是否为私有/内网地址
func isPrivateIPv4(ipv4 net.IP) bool {
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
	markdownContent := convertHTMLToMarkdown(article.Content, pageURL, article.TextContent)
	sb.WriteString(markdownContent)

	return sb.String()
}

// formatAsPlainText 将纯文本格式化为 Markdown（与 HTML 分支格式一致）
func (t *FetchTool) formatAsPlainText(content, pageURL string) string {
	var sb strings.Builder

	// URL
	sb.WriteString("**URL:** ")
	sb.WriteString(pageURL)
	sb.WriteString("\n\n")
	sb.WriteString("---\n\n")

	// 正文
	sb.WriteString(content)

	return sb.String()
}

// convertHTMLToMarkdown 将 HTML 内容转换为 Markdown 格式
func convertHTMLToMarkdown(htmlContent, pageURL string, fallbackText string) string {
	// 如果没有 HTML 内容，使用回退文本
	if htmlContent == "" {
		return fallbackText
	}

	// 解析 pageURL 获取域名，用于处理相对链接
	u, err := url.Parse(pageURL)
	if err != nil {
		return fallbackText
	}

	// 使用顶层函数转换（支持 WithDomain）
	markdown, err := htmltomarkdown.ConvertString(
		htmlContent,
		converter.WithDomain(u.Hostname()),
	)
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
