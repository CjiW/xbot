package tools

import "strings"

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
			if len(command) > 0 {
				quote := byte(0)
				if command[0] == '"' || command[0] == '\'' {
					quote = command[0]
					command = command[1:]
				}

				for len(command) > 0 {
					c := command[0]
					if quote != 0 {
						// 引号模式
						if c == '\\' && len(command) > 1 {
							// 转义字符
							command = command[1:]
							if len(command) > 0 {
								value.WriteByte(command[0])
								command = command[1:]
							}
							continue
						}
						if c == quote {
							// 引号结束
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

			exports = append(exports, varName+"="+value.String())
		}
	}

	return exports
}