package tools

import (
	"regexp"
	"strings"
	"testing"
)

// TestExportPattern 提取 export 语句的正则表达式测试
func TestExportPattern(t *testing.T) {
	// 使用与 shell.go 相同的正则表达式
	exportPattern := regexp.MustCompile(`export\s+((?:[A-Za-z_][A-Za-z0-9_]*=\S+\s*)+)`)
	kvPattern := regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*=\S+)`)

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
			name:     "带引号的 export",
			command:  `export MY_VAR="hello"`,
			expected: []string{`MY_VAR="hello"`},
		},
		{
			name:     "没有 export 命令",
			command:  "echo hello",
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matches := exportPattern.FindAllStringSubmatch(tt.command, -1)
			var results []string
			for _, match := range matches {
				if len(match) > 1 {
					kvMatches := kvPattern.FindAllString(match[1], -1)
					results = append(results, kvMatches...)
				}
			}

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

func TestDetectCdTip(t *testing.T) {
	tests := []struct {
		command string
		hasTip  bool
	}{
		// Should detect
		{"cd /tmp", true},
		{"cd src && ls", true},
		{"cd src/components", true},
		{"cd ..", true},
		{"cd ~", true},
		{"ls && cd subdir", true},
		{"echo hi; cd foo", true},
		{"false || cd bar", true},

		// Should NOT detect
		{"ls -la", false},
		{"echo cd is cool", false},
		{"mkdir -p foo", false},
		{"echo 'cd /tmp'", false}, // inside string — regex is simple, may match; acceptable tradeoff
		{"abcd /tmp", false},      // "abcd" is not "cd"
		{"git checkout develop", false},
		{"grep cd file.txt", false},
	}

	for _, tt := range tests {
		tip := detectCdTip(tt.command)
		got := tip != ""
		if got != tt.hasTip {
			t.Errorf("detectCdTip(%q) = %v, want hasTip=%v", tt.command, got, tt.hasTip)
		}
	}
}

// TestEnvPersistIntegration 集成测试：模拟完整的 export 命令处理流程
func TestEnvPersistIntegration(t *testing.T) {
	exportPattern := regexp.MustCompile(`export\s+((?:[A-Za-z_][A-Za-z0-9_]*=\S+\s*)+)`)
	kvPattern := regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*=\S+)`)

	// 模拟完整的处理流程
	processExportCommand := func(existingEnv string, command string) (string, bool) {
		if !strings.Contains(command, "export") {
			return existingEnv, false
		}

		matches := exportPattern.FindAllStringSubmatch(command, -1)
		if len(matches) == 0 {
			return existingEnv, false
		}

		var exports []string
		for _, match := range matches {
			if len(match) > 1 {
				kvMatches := kvPattern.FindAllString(match[1], -1)
				exports = append(exports, kvMatches...)
			}
		}

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
}
