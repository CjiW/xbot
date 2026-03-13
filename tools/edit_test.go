package tools

import (
	"strings"
	"testing"
)

// TestSandboxReplaceMultiline 测试多行替换逻辑
func TestSandboxReplaceMultiline(t *testing.T) {
	content := "line1\nline2\nline3"
	oldStr := "line1\nline2"
	newStr := "replaced"

	// 模拟 sandboxReplace 中的逻辑
	if !strings.Contains(content, oldStr) {
		t.Fatal("应该找到多行文本")
	}

	result := strings.Replace(content, oldStr, newStr, 1)

	expected := "replaced\nline3"
	if result != expected {
		t.Errorf("替换失败: got %q, want %q", result, expected)
	}

	t.Logf("✅ 多行替换成功: %q", result)
}

// TestSandboxReplaceWithSlash 测试包含 / 的内容
func TestSandboxReplaceWithSlash(t *testing.T) {
	content := "path/to/file\nanother/line"
	oldStr := "path/to/file"
	newStr := "replaced"

	// 模拟 sandboxReplace 中的逻辑
	result := strings.Replace(content, oldStr, newStr, 1)

	expected := "replaced\nanother/line"
	if result != expected {
		t.Errorf("替换失败: got %q, want %q", result, expected)
	}

	t.Logf("✅ 包含 / 的替换成功: %q", result)
}

// TestSandboxReplaceWithSpecialChars 测试特殊字符
func TestSandboxReplaceWithSpecialChars(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		oldStr   string
		newStr   string
		expected string
	}{
		{
			name:     "ampersand",
			content:  "foo & bar",
			oldStr:   "foo & bar",
			newStr:   "replaced",
			expected: "replaced",
		},
		{
			name:     "backslash",
			content:  `path\to\file`,
			oldStr:   `path\to\file`,
			newStr:   "replaced",
			expected: "replaced",
		},
		{
			name:     "dollar",
			content:  "$100 dollars",
			oldStr:   "$100 dollars",
			newStr:   "replaced",
			expected: "replaced",
		},
		{
			name:     "multiline_with_special",
			content:  "line1/foo\nline2&bar",
			oldStr:   "line1/foo\nline2&bar",
			newStr:   "replaced",
			expected: "replaced",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := strings.Replace(tt.content, tt.oldStr, tt.newStr, 1)
			if result != tt.expected {
				t.Errorf("替换失败: got %q, want %q", result, tt.expected)
			}
		})
	}
}

// TestSandboxReplaceAll 测试 replace_all
func TestSandboxReplaceAll(t *testing.T) {
	content := "foo\nbar\nfoo\nbaz"
	oldStr := "foo"
	newStr := "replaced"

	result := strings.ReplaceAll(content, oldStr, newStr)

	expected := "replaced\nbar\nreplaced\nbaz"
	if result != expected {
		t.Errorf("替换失败: got %q, want %q", result, expected)
	}

	t.Logf("✅ 全部替换成功")
}
