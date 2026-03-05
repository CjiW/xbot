//go:build ignore

package feishu_mcp

import (
	"context"
	"encoding/json"
	"testing"

	"xbot/oauth"
	"xbot/oauth/providers"
	"xbot/tools"
)

// TestDocxWrite tests the docx write functionality
func TestDocxWrite(t *testing.T) {
	// This test requires valid OAuth credentials
	// Run with: go test -run TestDocxWrite ./tools/feishu_mcp/

	// Create a mock MCP for testing

	sotrage, err := oauth.NewSQLiteStorage("/home/user/src/xbot/.xbot/oauth_tokens.db")
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	mcp := &FeishuMCP{
		oauth: oauth.NewManager(sotrage),
	}

	mcp.oauth.RegisterProvider(providers.NewFeishuProvider("", "", "http://localhost:8080/callback"))

	tool := &DocxWriteTool{MCP: mcp}

	// Test parameters
	params := map[string]interface{}{
		"document_id": "O3QudiygaozHAKxRN4scEtjZnRg",
		"content": `# Test Heading

This is a paragraph.

- Item 1
  - 111
  - 222
    - 3333
- Item 2



1. Ordered 1
2. Ordered 2

| Table Header 1 | Table Header 2 |
| --- | --- |
| Cell 1 | $let a = 1$ |
| Cell 3 | Cell 4 |
## dassdad
> Quote
### adsdad
` + "```" + `
graph TD
    A[用户申请权限] --> B[审批流程]
    B --> C{时间评估}
    C -->|短期| D[设置过期时间]
    C -->|长期| E[定期审查]
    D --> F[自动过期提醒]
    E --> G[季度权限审计]
` + "```",
	}

	input, _ := json.Marshal(params)

	result, err := tool.Execute(&tools.ToolContext{
		Ctx:     context.Background(),
		Channel: "feishu",
		ChatID:  "",
	}, string(input))

	if err != nil {
		t.Fatalf("Error (expected without real credentials): %v", err)
	}

	t.Logf("Result: %v", result)
}
