package tools

import (
	"strings"
	"testing"
)

// TestParseExportCommand 测试 export 命令解析
func TestParseExportCommand(t *testing.T) {
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
			command:  "export A=1 B=2",
			expected: []string{"A=1", "B=2"},
		},
		{
			name:     "多个 export 在不同行",
			command:  "export PATH=/a\nexport GOPATH=/root/go",
			expected: []string{"PATH=/a", "GOPATH=/root/go"},
		},
		{
			name:     "带双引号的 export",
			command:  `export MY_VAR="hello world"`,
			expected: []string{`MY_VAR="hello world"`},
		},
		{
			name:     "带单引号的 export",
			command:  `export MY_VAR='hello world'`,
			expected: []string{`MY_VAR='hello world'`},
		},
		{
			name:     "没有 export 命令",
			command:  "echo hello",
			expected: nil,
		},
		{
			name:     "复杂值带冒号",
			command:  "export PATH=/usr/local/go/bin:$PATH",
			expected: []string{"PATH=/usr/local/go/bin:$PATH"},
		},
		{
			name:     "混合引号和无引号",
			command:  `export A=1 B="hello world" C=/simple/path`,
			expected: []string{"A=1", `B="hello world"`, "C=/simple/path"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results := parseExportCommand(tt.command)

			if len(results) != len(tt.expected) {
				t.Errorf("expected %d matches, got %d: %v", len(tt.expected), len(results), results)
				return
			}

			for i, exp := range tt.expected {
				if results[i] != exp {
					t.Errorf("match %d: expected %q, got %q", i, exp, results[i])
				}
			}
		})
	}
}

// TestEnvMergeDeduplication 测试环境变量合并去重逻辑
func TestEnvMergeDeduplication(t *testing.T) {
	// 模拟 persistEnvFromCommand 中的去重逻辑
	mergeEnv := func(existing string, newExports []string) []string {
		envMap := make(map[string]string)

		// 解析现有环境变量
		for _, line := range strings.Split(existing, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				envMap[parts[0]] = parts[1]
			}
		}

		// 添加新的环境变量（会覆盖同名变量）
		for _, exp := range newExports {
			parts := strings.SplitN(exp, "=", 2)
			if len(parts) == 2 {
				envMap[parts[0]] = parts[1]
			}
		}

		// 构建结果
		var lines []string
		for k, v := range envMap {
			lines = append(lines, k+"="+v)
		}
		return lines
	}

	tests := []struct {
		name          string
		existing      string
		newExports    []string
		checkKey      string
		checkValue    string
		expectedCount int
	}{
		{
			name:          "首次设置",
			existing:      "",
			newExports:    []string{"PATH=/a"},
			checkKey:      "PATH",
			checkValue:    "/a",
			expectedCount: 1,
		},
		{
			name:          "覆盖同名变量",
			existing:      "PATH=/a",
			newExports:    []string{"PATH=/b"},
			checkKey:      "PATH",
			checkValue:    "/b",
			expectedCount: 1, // 仍然只有一行
		},
		{
			name:          "执行两次相同设置",
			existing:      "PATH=/a",
			newExports:    []string{"PATH=/a"},
			checkKey:      "PATH",
			checkValue:    "/a",
			expectedCount: 1, // 去重后只有一行
		},
		{
			name:          "多个变量设置",
			existing:      "PATH=/a",
			newExports:    []string{"GOPATH=/root/go", "GOROOT=/usr/local/go"},
			expectedCount: 3,
		},
		{
			name:          "带注释的现有文件",
			existing:      "# comment\nPATH=/a",
			newExports:    []string{"GOPATH=/root/go"},
			expectedCount: 2,
		},
		{
			name:          "多行 export A=1 B=2",
			existing:      "",
			newExports:    []string{"A=1", "B=2"},
			expectedCount: 2,
		},
		{
			name:          "部分覆盖",
			existing:      "A=1\nB=2",
			newExports:    []string{"B=3", "C=4"},
			checkKey:      "B",
			checkValue:    "3",
			expectedCount: 3, // A=1, B=3, C=4
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines := mergeEnv(tt.existing, tt.newExports)

			if len(lines) != tt.expectedCount {
				t.Errorf("expected %d lines, got %d: %v", tt.expectedCount, len(lines), lines)
			}

			if tt.checkKey != "" {
				found := false
				for _, line := range lines {
					if strings.HasPrefix(line, tt.checkKey+"=") {
						found = true
						if line != tt.checkKey+"="+tt.checkValue {
							t.Errorf("expected %s=%s, got %s", tt.checkKey, tt.checkValue, line)
						}
						break
					}
				}
				if !found {
					t.Errorf("key %s not found in result", tt.checkKey)
				}
			}
		})
	}
}

// TestEnvPersistIntegration 集成测试：模拟完整的 export 命令处理流程
func TestEnvPersistIntegration(t *testing.T) {
	// 模拟完整的处理流程
	processExportCommand := func(existingEnv string, command string) (string, bool) {
		exports := parseExportCommand(command)
		if len(exports) == 0 {
			return existingEnv, false
		}

		// 合并环境变量
		envMap := make(map[string]string)
		for _, line := range strings.Split(existingEnv, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				envMap[parts[0]] = parts[1]
			}
		}

		for _, exp := range exports {
			parts := strings.SplitN(exp, "=", 2)
			if len(parts) == 2 {
				envMap[parts[0]] = parts[1]
			}
		}

		var lines []string
		lines = append(lines, "# Auto-generated by xbot")
		for k, v := range envMap {
			lines = append(lines, k+"="+v)
		}
		return strings.Join(lines, "\n"), true
	}

	// 测试场景：两次 export PATH
	env := ""
	env, _ = processExportCommand(env, "export PATH=/usr/local/go/bin:$PATH")
	if !strings.Contains(env, "PATH=/usr/local/go/bin:$PATH") {
		t.Errorf("first export failed: %s", env)
	}

	// 再次 export PATH
	env, _ = processExportCommand(env, "export PATH=/new/path")
	if !strings.Contains(env, "PATH=/new/path") {
		t.Errorf("second export failed: %s", env)
	}
	if strings.Contains(env, "/usr/local/go/bin") {
		t.Errorf("old PATH should be replaced, got: %s", env)
	}

	// 检查只有一行 PATH
	pathCount := strings.Count(env, "PATH=")
	if pathCount != 1 {
		t.Errorf("expected 1 PATH entry, got %d: %s", pathCount, env)
	}

	// 测试引号内空格
	env = ""
	env, _ = processExportCommand(env, `export MY_VAR="hello world"`)
	// 检查完整的值是否存在
	if !strings.Contains(env, `MY_VAR="hello world"`) {
		t.Errorf("quoted export failed: %s", env)
	}
	// 检查值是否完整（不应该被截断）
	// 正确的值应该包含 "hello world"，而不是只有 "hello
	if strings.Count(env, "MY_VAR=") != 1 {
		t.Errorf("expected 1 MY_VAR entry, got: %s", env)
	}
}

// TestParseValue 详细测试 parseValue 函数
func TestParseValue(t *testing.T) {
	tests := []struct {
		name           string
		input          string
		expectedValue  string
		expectedRemain string
	}{
		{
			name:           "无引号简单值",
			input:          "/usr/bin",
			expectedValue:  "/usr/bin",
			expectedRemain: "",
		},
		{
			name:           "无引号带变量",
			input:          "$PATH:/usr/local/bin",
			expectedValue:  "$PATH:/usr/local/bin",
			expectedRemain: "",
		},
		{
			name:           "双引号值",
			input:          `"hello world" rest`,
			expectedValue:  `"hello world"`,
			expectedRemain: " rest",
		},
		{
			name:           "单引号值",
			input:          `'hello world' rest`,
			expectedValue:  `'hello world'`,
			expectedRemain: " rest",
		},
		{
			name:           "值后有空格",
			input:          "/usr/bin next",
			expectedValue:  "/usr/bin",
			expectedRemain: " next",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value, remain := parseValue(tt.input)
			if value != tt.expectedValue {
				t.Errorf("value: expected %q, got %q", tt.expectedValue, value)
			}
			if remain != tt.expectedRemain {
				t.Errorf("remain: expected %q, got %q", tt.expectedRemain, remain)
			}
		})
	}
}
