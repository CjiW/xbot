package tools

import (
	"reflect"
	"testing"
)

func TestParseExportStatements(t *testing.T) {
	tests := []struct {
		name     string
		command  string
		expected []string
	}{
		{
			name:     "单个 export",
			command:  "export PATH=/usr/local/go/bin:$PATH",
			expected: []string{"PATH=/usr/local/go/bin:$PATH"},
		},
		{
			name:     "多个 export 在同一行",
			command:  "export A=1 B=2 C=3",
			expected: []string{"A=1", "B=2", "C=3"},
		},
		{
			name:     "多个 export 在不同行",
			command:  "export A=1\nexport B=2",
			expected: []string{"A=1", "B=2"},
		},
		{
			name:     "带双引号的 export",
			command:  `export MY_VAR="hello world"`,
			expected: []string{"MY_VAR=hello world"},
		},
		{
			name:     "带单引号的 export",
			command:  `export MY_VAR='hello world'`,
			expected: []string{"MY_VAR=hello world"},
		},
		{
			name:     "带引号和特殊字符",
			command:  `export PATH="/usr/local/go/bin:$PATH"`,
			expected: []string{"PATH=/usr/local/go/bin:$PATH"},
		},
		{
			name:     "混合引号和无引号",
			command:  `export A=1 B="hello world" C=/path/to/bin`,
			expected: []string{"A=1", "B=hello world", "C=/path/to/bin"},
		},
		{
			name:     "没有 export 命令",
			command:  "echo hello",
			expected: nil,
		},
		{
			name:     "export 后面没有变量",
			command:  "export",
			expected: nil,
		},
		{
			name:     "export 后面是已存在的变量名（无赋值）",
			command:  "export PATH",
			expected: nil,
		},
		{
			name:     "带分号分隔",
			command:  "export A=1; echo done",
			expected: []string{"A=1"},
		},
		{
			name:     "带转义字符（双引号）",
			command:  `export MSG="hello\"world"`,
			expected: []string{`MSG=hello"world`},
		},
		{
			name:     "单引号内反斜杠不转义",
			command:  `export MSG='hello\"world'`,
			expected: []string{`MSG=hello\"world`},
		},
		{
			name:     "GOPATH 设置",
			command:  "export GOPATH=/root/go",
			expected: []string{"GOPATH=/root/go"},
		},
		{
			name:     "复杂 PATH 设置",
			command:  "export PATH=/usr/local/go/bin:/root/go/bin:$PATH",
			expected: []string{"PATH=/usr/local/go/bin:/root/go/bin:$PATH"},
		},
		// P2: 边界测试
		{
			name:     "空值",
			command:  "export A=",
			expected: []string{"A="},
		},
		{
			name:     "值含等号",
			command:  "export A=foo=bar",
			expected: []string{"A=foo=bar"},
		},
		{
			name:     "未闭合双引号 - 不应添加变量",
			command:  `export A="hello world`,
			expected: nil,
		},
		{
			name:     "未闭合单引号 - 不应添加变量",
			command:  `export A='hello world`,
			expected: nil,
		},
		{
			name:     "非法变量名（数字开头）- 不应添加变量",
			command:  "export 1ABC=val",
			expected: nil,
		},
		{
			name:     "单引号内反斜杠保持原样",
			command:  `export A='hello\nworld'`,
			expected: []string{`A=hello\nworld`},
		},
		{
			name:     "双引号内转义反斜杠",
			command:  `export A="hello\\nworld"`,
			expected: []string{`A=hello\nworld`},
		},
		{
			name:     "未闭合引号后无有效变量",
			command:  `export A=valid B="unclosed`,
			expected: []string{"A=valid"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseExportStatements(tt.command)
			if !reflect.DeepEqual(result, tt.expected) {
				t.Errorf("parseExportStatements() = %v, want %v", result, tt.expected)
			}
		})
	}
}
