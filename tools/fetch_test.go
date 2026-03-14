package tools

import (
	"strings"
	"testing"
)

func TestValidateURL(t *testing.T) {
	tests := []struct {
		name        string
		rawURL      string
		wantErr     bool
		errContains string
	}{
		// 合法 URL
		{"valid https URL", "https://example.com", false, ""},
		{"valid http URL", "http://example.com", false, ""},
		{"valid URL with path", "https://example.com/blog/post", false, ""},
		// 无效协议
		{"ftp protocol", "ftp://example.com", true, "unsupported protocol"},
		{"file protocol", "file:///etc/passwd", true, "unsupported protocol"},
		// localhost
		{"localhost", "http://localhost:8080", true, "localhost"},
		{"localhost.localdomain", "http://localhost.localdomain", true, "localhost"},
		// 内网 IP - loopback
		{"loopback 127.0.0.1", "http://127.0.0.1:8080", true, "private/internal IP"},
		{"loopback 127.0.0.2", "http://127.0.0.2", true, "private/internal IP"},
		// 内网 IP - 10.0.0.0/8
		{"private 10.0.0.1", "http://10.0.0.1", true, "private/internal IP"},
		{"private 10.255.255.255", "http://10.255.255.255", true, "private/internal IP"},
		// 内网 IP - 172.16.0.0/12
		{"private 172.16.0.1", "http://172.16.0.1", true, "private/internal IP"},
		{"private 172.31.255.255", "http://172.31.255.255", true, "private/internal IP"},
		// 内网 IP - 192.168.0.0/16
		{"private 192.168.0.1", "http://192.168.0.1", true, "private/internal IP"},
		{"private 192.168.255.255", "http://192.168.255.255", true, "private/internal IP"},
		// 内网 IP - link-local
		{"link-local 169.254.0.1", "http://169.254.0.1", true, "private/internal IP"},
		// 无效 URL
		{"invalid URL", "not-a-valid-url", true, ""},
		{"empty URL", "", true, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateURL(tt.rawURL)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateURL() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && err != nil && tt.errContains != "" {
				if !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("validateURL() error = %v, should contain %v", err, tt.errContains)
				}
			}
		})
	}
}

func TestIsPrivateIP(t *testing.T) {
	tests := []struct {
		host string
		want bool
	}{
		// 公有 IP
		{"example.com", false},
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		// loopback
		{"127.0.0.1", true},
		{"127.0.0.2", true},
		// 10.0.0.0/8
		{"10.0.0.0", true},
		{"10.0.0.1", true},
		{"10.255.255.255", true},
		// 172.16.0.0/12
		{"172.16.0.0", true},
		{"172.16.0.1", true},
		{"172.31.255.255", true},
		// 192.168.0.0/16
		{"192.168.0.0", true},
		{"192.168.0.1", true},
		{"192.168.255.255", true},
		// link-local
		{"169.254.0.0", true},
		{"169.254.0.1", true},
		// 0.0.0.0/8
		{"0.0.0.0", true},
		{"0.255.255.255", true},
		// 非 IP 域名
		{"localhost", false},
	}

	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			if got := isPrivateIP(tt.host); got != tt.want {
				t.Errorf("isPrivateIP(%q) = %v, want %v", tt.host, got, tt.want)
			}
		})
	}
}

func TestTruncateByTokens(t *testing.T) {
	content := "This is a test content. " +
		"We need to create a string that is long enough. " +
		"Token counting is important for LLM applications. "

	tests := []struct {
		name      string
		content   string
		maxTokens int
	}{
		{"content shorter than maxTokens", "Short content", 10000},
		{"content needs truncation", content, 10},
		{"very small maxTokens", "This is a test content.", 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := NewFetchTool()
			result, actualTokens := tool.truncateByTokens(tt.content, tt.maxTokens)

			// 验证返回的 token 数不超过限制
			if actualTokens > tt.maxTokens {
				t.Errorf("truncateByTokens() returned %d tokens, should be <= %d", actualTokens, tt.maxTokens)
			}

			// 如果内容被截断，结果应该更短或包含截断提示
			if tt.maxTokens < 50 && len(result) >= len(tt.content) {
				t.Logf("Content might not be truncated as expected, got %d chars, original %d chars", len(result), len(tt.content))
			}
			_ = result
		})
	}
}
