package agent

import (
	"cmp"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"xbot/llm"
)

// ActiveFile 最近 N 轮活跃文件记录
type ActiveFile struct {
	Path         string   // 文件路径
	LastSeenIter int      // 最后出现的轮次（0=最近一轮）
	Functions    []string // 涉及的函数签名（从 tool result 中提取）
}

// containsSemanticMatch 语义模糊匹配：归一化子串 + 关键词重叠度。
func containsSemanticMatch(text, target string) bool {
	if text == "" || target == "" {
		return false
	}

	// 精确子串匹配（不区分大小写）
	if strings.Contains(strings.ToLower(text), strings.ToLower(target)) {
		return true
	}

	// 反向检查：target 包含 text 的关键部分
	if len(target) > 50 && len(target) > len(text) {
		if strings.Contains(strings.ToLower(target), strings.ToLower(text)) {
			return true
		}
	}

	// 关键词重叠度：target 分词后至少 60% 出现在 text 中
	targetWords := splitToWords(target)
	if len(targetWords) == 0 {
		return false
	}
	matchedWords := 0
	textLower := strings.ToLower(text)
	for _, w := range targetWords {
		if strings.Contains(textLower, strings.ToLower(w)) {
			matchedWords++
		}
	}
	return float64(matchedWords)/float64(len(targetWords)) >= 0.6
}

// ----------------------------------------------------------------
// 辅助函数
// ----------------------------------------------------------------

// extractFilePaths 正则提取文件路径。
// 匹配：/absolute/path, ./relative/path, ../parent/path, 文件名.ext（至少含一个 / 或 .ext）
var filePathRe = regexp.MustCompile(`(?:[A-Za-z]:[\\\/]|[./~][\w./~-]*|/\S+?\.\w{1,10})(?:\s|[),;:}"'\n]|$)`)

var funcSigRe = regexp.MustCompile(`func\s+(\w+)`)

func extractFilePaths(text string) []string {
	matches := filePathRe.FindAllString(text, -1)
	var result []string
	seen := make(map[string]bool)
	for _, m := range matches {
		// 去掉尾部非路径字符
		m = strings.TrimRight(m, " \t\n\r,);:}\"'")
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		result = append(result, m)
	}
	return result
}

// extractFunctionSignatures 从文本中提取 Go 函数签名（func FuncName 模式）。
func extractFunctionSignatures(text string) []string {
	matches := funcSigRe.FindAllStringSubmatch(text, -1)
	seen := make(map[string]bool)
	var result []string
	for _, m := range matches {
		if len(m) > 1 {
			sig := "func " + m[1]
			if !seen[sig] {
				seen[sig] = true
				result = append(result, sig)
			}
		}
	}
	return result
}

// stopWords 停用词集合。
var stopWords = map[string]bool{
	"the": true, "a": true, "an": true, "is": true, "are": true,
	"was": true, "were": true, "be": true, "been": true, "being": true,
	"have": true, "has": true, "had": true, "do": true, "does": true,
	"did": true, "will": true, "would": true, "shall": true, "should": true,
	"may": true, "might": true, "must": true, "can": true, "could": true,
	"to": true, "of": true, "in": true, "for": true, "on": true,
	"at": true, "by": true, "with": true, "from": true, "as": true,
	"into": true, "about": true, "like": true, "through": true, "after": true,
	"over": true, "between": true, "out": true, "against": true, "during": true,
	"without": true, "before": true, "under": true, "around": true, "among": true,
	"and": true, "but": true, "or": true, "nor": true, "not": true,
	"so": true, "yet": true, "both": true, "either": true, "neither": true,
	"each": true, "every": true, "all": true, "any": true, "few": true,
	"more": true, "most": true, "other": true, "some": true, "such": true,
	"no": true, "only": true, "own": true, "same": true, "than": true,
	"too": true, "very": true, "just": true, "because": true, "if": true,
	"when": true, "where": true, "how": true, "what": true, "which": true,
	"who": true, "whom": true, "this": true, "that": true, "these": true,
	"those": true, "then": true, "there": true, "here": true, "also": true,
	"it": true, "its": true, "i": true, "me": true, "my": true,
	"we": true, "us": true, "our": true, "you": true, "your": true,
	"he": true, "him": true, "his": true, "she": true, "her": true,
	"they": true, "them": true, "their": true, "up": true, "down": true,
}

// splitToWords 文本分词（去停用词）。
func splitToWords(text string) []string {
	fields := strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
	})
	var result []string
	for _, f := range fields {
		lower := strings.ToLower(f)
		if !stopWords[lower] && len(f) > 1 {
			result = append(result, f)
		}
	}
	return result
}

// Internal helpers

// ExtractActiveFiles 从最近 N 轮 tool call 中提取活跃文件。
// 一轮 = 一组 assistant(tool_calls) + 对应的 tool result 消息。
// 扫描 messages 尾部，按工具类型提取文件路径：
//   - Read/Edit/Write → Arguments JSON 中的 "path" 或 "file_path"
//   - Glob → Arguments JSON 中的 "pattern"
//   - Grep → Arguments JSON 中的 "path"
//   - Shell → 从 tool result Content 中正则提取文件路径
//   - SubAgent → 不提取
//
// 同时从 tool result Content 中提取函数签名（func \w+ 模式）。
// 去重后按 LastSeenIter 降序排列（最近的排在前面）。
func ExtractActiveFiles(messages []llm.ChatMessage, lastN int) []ActiveFile {
	if len(messages) == 0 || lastN <= 0 {
		return nil
	}

	// 从尾部向前找 tool 组
	type toolRound struct {
		iter  int
		paths []string
		funcs []string
	}
	var rounds []toolRound

	// 识别 tool 组：assistant(tool_calls) + tool results
	// 从尾部向前扫描
	var currentPaths []string
	var currentFuncs []string

	roundCount := 0
	i := len(messages) - 1

	for i >= 0 && roundCount < lastN {
		msg := messages[i]

		if msg.Role == "tool" {
			// 从 tool result 中提取路径（Shell 特殊处理）
			if msg.ToolName == "Shell" {
				// 从 Shell 输出中正则提取文件路径
				currentPaths = append(currentPaths, extractFilePaths(msg.Content)...)
			}
			// 从 tool result 中提取函数签名
			funcSigs := extractFunctionSignatures(msg.Content)
			currentFuncs = append(currentFuncs, funcSigs...)
		} else if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			// 从 tool call Arguments JSON 中提取路径
			for _, tc := range msg.ToolCalls {
				paths := extractPathsFromToolArgs(tc.Name, tc.Arguments)
				currentPaths = append(currentPaths, paths...)
			}

			// 这是一个新轮的开始
			if len(currentPaths) > 0 || len(currentFuncs) > 0 {
				rounds = append(rounds, toolRound{
					iter:  roundCount,
					paths: currentPaths,
					funcs: currentFuncs,
				})
				roundCount++
				currentPaths = nil
				currentFuncs = nil
			} else {
				// 即使没有提取到路径也计为一轮（避免漏算）
				roundCount++
			}
		} else if roundCount > 0 {
			// 遇到非 tool/assistant 消息且已经开始收集，停止
			break
		}
		i--
	}

	// 反转 rounds 使最近的在前面
	for l, r := 0, len(rounds)-1; l < r; l, r = l+1, r-1 {
		rounds[l], rounds[r] = rounds[r], rounds[l]
	}

	// 合并去重
	pathInfo := make(map[string]*ActiveFile) // path -> ActiveFile
	for _, rd := range rounds {
		seen := make(map[string]bool)
		for _, p := range rd.paths {
			if p == "" || seen[p] {
				continue
			}
			seen[p] = true
			if existing, ok := pathInfo[p]; ok {
				// 更新为更近的轮次
				if rd.iter < existing.LastSeenIter {
					existing.LastSeenIter = rd.iter
				}
			} else {
				pathInfo[p] = &ActiveFile{
					Path:         p,
					LastSeenIter: rd.iter,
				}
			}
		}
		// 函数签名归入对应文件（按最近路径）
		for _, f := range rd.funcs {
			if f == "" {
				continue
			}
			// 找到该轮中最相关的路径
			if len(rd.paths) > 0 {
				p := rd.paths[0]
				if af, ok := pathInfo[p]; ok {
					af.Functions = append(af.Functions, f)
				}
			}
		}
	}

	// 去重函数签名
	result := make([]ActiveFile, 0, len(pathInfo))
	for _, af := range pathInfo {
		af.Functions = dedupStrings(af.Functions)
		result = append(result, *af)
	}

	// 按 LastSeenIter 降序排列（最近的在前）
	slices.SortFunc(result, func(a, b ActiveFile) int {
		return cmp.Compare(a.LastSeenIter, b.LastSeenIter)
	})

	return result
}

// extractPathsFromToolArgs 从工具调用的 Arguments JSON 中提取文件路径。
func extractPathsFromToolArgs(toolName, argsJSON string) []string {
	if argsJSON == "" {
		return nil
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return nil
	}

	var paths []string
	switch toolName {
	case "Read", "FileCreate", "FileReplace", "Write":
		if p, ok := args["path"].(string); ok && p != "" {
			paths = append(paths, p)
		}
		if p, ok := args["file_path"].(string); ok && p != "" {
			paths = append(paths, p)
		}
	case "Glob":
		if p, ok := args["pattern"].(string); ok && p != "" {
			paths = append(paths, p)
		}
	case "Grep":
		if p, ok := args["path"].(string); ok && p != "" {
			paths = append(paths, p)
		}
	}
	return paths
}

// dedupStrings 去重字符串切片，保持顺序
func dedupStrings(ss []string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			result = append(result, s)
		}
	}
	return result
}
