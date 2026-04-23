package tools

import "strings"

// shellUnquoteValue 反转 shellEscapeValue 的编码，将 shell 单引号转义还原为原始值。
// shellEscapeValue 将值编码为 '...' 形式：
//   - 内嵌单引号用 '\” 表示（关闭引号 → 转义单引号 → 重新开引号）
//   - 换行用 '\n' 表示（关闭引号 → 字面 \n → 重新开引号），这是有损转换
//
// 本函数做逆向处理：去除外层单引号，将 '\” 还原为 '，将 '\n' 还原为 \n（两字符）。
func shellUnquoteValue(v string) string {
	v = strings.TrimSpace(v)
	if len(v) < 2 || v[0] != '\'' || v[len(v)-1] != '\'' {
		return v
	}
	// 去除外层单引号
	inner := v[1 : len(v)-1]
	// 将 '\'' 还原为 '（shell 单引号内嵌转义）
	inner = strings.ReplaceAll(inner, "'\\''", "'")
	// 将 '\n' 还原为 \n（shellEscapeValue 对换行的有损编码）
	inner = strings.ReplaceAll(inner, "'\\n'", "\\n")
	return inner
}

// parseEnvFileLines 解析 .xbot_env 文件内容为 envMap。
// 正确处理 "export " 前缀（包括多次堆积），避免因前缀导致的 key 不匹配。
// 对单引号包裹的值执行 unquote，防止反复读写导致引号指数级膨胀。
func parseEnvFileLines(content string) map[string]string {
	envMap := make(map[string]string)
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Strip "export " prefix(es) if present (file stores "export KEY=VALUE")
		// Handle corrupted lines with stacked prefixes like "export export PATH=..."
		// 循环剥离所有 "export " 前缀（处理 "export export KEY=VAL" 等异常情况）
		for strings.HasPrefix(line, "export ") {
			line = strings.TrimPrefix(line, "export ")
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = shellUnquoteValue(parts[1])
		}
	}
	return envMap
}

// parseExportStatements 解析 export 语句，提取所有 KEY=VALUE 对
// 支持引号内的空格、单引号、双引号、$变量引用等
func parseExportStatements(command string) []string {
	var exports []string

	// 找到所有 export 语句
	for {
		idx := strings.Index(command, "export")
		if idx == -1 {
			break
		}
		// 检查 export 是否是独立的单词
		if idx > 0 {
			prev := command[idx-1]
			if prev != ' ' && prev != '\t' && prev != '\n' && prev != ';' && prev != '|' && prev != '&' {
				command = command[idx+6:]
				continue
			}
		}
		// export 后面必须是空白字符或结尾
		afterIdx := idx + 6
		if afterIdx < len(command) {
			after := command[afterIdx]
			if after != ' ' && after != '\t' && after != '\n' {
				command = command[afterIdx:]
				continue
			}
		}

		// 跳过 export 关键字和后面的空白
		rest := strings.TrimLeft(command[afterIdx:], " \t\n")
		command = rest

		// 解析 export 后面的变量赋值
		for len(command) > 0 {
			// 跳过前导空白
			command = strings.TrimLeft(command, " \t")
			if len(command) == 0 {
				break
			}

			// 检查是否遇到语句结束符
			if command[0] == ';' || command[0] == '|' || command[0] == '&' || command[0] == '#' || command[0] == '\n' {
				break
			}

			// 解析变量名
			varNameEnd := 0
			for varNameEnd < len(command) {
				c := command[varNameEnd]
				if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' {
					varNameEnd++
				} else {
					break
				}
			}

			// 检查是否有有效的变量名和等号
			if varNameEnd == 0 {
				// 没有有效的变量名，跳过
				break
			}
			// P0: 变量名不能以数字开头
			firstChar := command[0]
			if firstChar >= '0' && firstChar <= '9' {
				// 非法变量名，跳过
				break
			}
			if varNameEnd >= len(command) {
				// 变量名后面没有内容（无等号），跳过
				break
			}
			if command[varNameEnd] != '=' {
				// 变量名后面不是等号，跳过
				break
			}

			varName := command[:varNameEnd]
			command = command[varNameEnd+1:] // 跳过 '='

			// 解析值
			var value strings.Builder
			quoteClosed := true // P0: 追踪引号是否闭合
			if len(command) > 0 {
				quote := byte(0)
				if command[0] == '"' || command[0] == '\'' {
					quote = command[0]
					quoteClosed = false
					command = command[1:]
				}

				for len(command) > 0 {
					c := command[0]
					if quote != 0 {
						// 引号模式
						if c == '\\' && len(command) > 1 && quote == '"' {
							// P0: 转义字符只在双引号模式下处理
							command = command[1:]
							if len(command) > 0 {
								value.WriteByte(command[0])
								command = command[1:]
							}
							continue
						}
						if c == quote {
							// 引号结束
							quoteClosed = true
							command = command[1:]
							break
						}
						value.WriteByte(c)
						command = command[1:]
					} else {
						// 非引号模式
						if c == ' ' || c == '\t' || c == '\n' || c == ';' || c == '|' || c == '&' {
							break
						}
						value.WriteByte(c)
						command = command[1:]
					}
				}
			}

			// P0: 只有引号闭合（或没有引号）才添加变量
			if quoteClosed {
				exports = append(exports, varName+"="+value.String())
			}
		}
	}

	return exports
}
