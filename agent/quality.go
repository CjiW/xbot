package agent

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"xbot/llm"
)

// KeyInfoFingerprint 对话关键信息指纹，用于压缩质量校验。
// 当前在 compressMessagesWithFingerprint 中引导 LLM 保留关键信息。
// 未来可用于压缩后自动验证（对比压缩前后的指纹一致性）。
type KeyInfoFingerprint struct {
	FilePaths   []string
	Identifiers []string
	Errors      []string
	Decisions   []string
	ActiveFiles []ActiveFile // 最近 N 轮的活跃文件
}

// ActiveFile 最近 N 轮活跃文件记录
type ActiveFile struct {
	Path         string   // 文件路径
	LastSeenIter int      // 最后出现的轮次（0=最近一轮）
	Functions    []string // 涉及的函数签名（从 tool result 中提取）
}

// ----------------------------------------------------------------
// 核心函数
// ----------------------------------------------------------------

// ExtractFingerprint 从消息列表提取关键信息指纹。
func ExtractFingerprint(messages []llm.ChatMessage) KeyInfoFingerprint {
	var fp KeyInfoFingerprint
	seen := make(map[string]bool)

	addUnique := func(slice *[]string, s string) {
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		*slice = append(*slice, s)
	}

	for _, msg := range messages {
		text := msg.Content

		for _, p := range extractFilePaths(text) {
			addUnique(&fp.FilePaths, p)
		}
		for _, id := range extractCodeIdentifiers(text) {
			addUnique(&fp.Identifiers, id)
		}
		// BUG FIX: 只从 tool 消息中提取 error。
		// 之前从所有消息（user/assistant/tool）中提取，导致 "error handling"、
		// "error message format" 等元讨论也被当作 error，膨胀 Errors 数量，
		// 拉低 retention_rate（从 400+ 噪音行降至实际错误行数）。
		if msg.Role == "tool" {
			for _, e := range extractErrorMessages(text) {
				addUnique(&fp.Errors, e)
			}
		}
		for _, d := range extractDecisions(text) {
			addUnique(&fp.Decisions, d)
		}
	}

	// 提取活跃文件
	fp.ActiveFiles = ExtractActiveFiles(messages, 3)

	// 限制收集规模：避免长对话累积过多项
	// FilePaths: 取最近出现的（从后往前保留最后 50 个唯一路径）
	if len(fp.FilePaths) > 50 {
		fp.FilePaths = fp.FilePaths[len(fp.FilePaths)-50:]
	}
	// Identifiers: 上限 100
	if len(fp.Identifiers) > 100 {
		fp.Identifiers = fp.Identifiers[len(fp.Identifiers)-100:]
	}
	// Errors: 上限 20
	if len(fp.Errors) > 20 {
		fp.Errors = fp.Errors[len(fp.Errors)-20:]
	}
	// Decisions: 上限 20
	if len(fp.Decisions) > 20 {
		fp.Decisions = fp.Decisions[len(fp.Decisions)-20:]
	}

	return fp
}

// ValidateCompression 校验压缩后信息保留率。
// 返回保留率 (0-1) 和丢失项列表。
func ValidateCompression(original []llm.ChatMessage, compressed []llm.ChatMessage, fp KeyInfoFingerprint) (float64, []string) {
	// 从压缩结果中提取指纹
	compressedFp := ExtractFingerprint(compressed)
	compressedText := joinMessages(compressed)

	totalItems := len(fp.FilePaths) + len(fp.Identifiers) + len(fp.Errors) + len(fp.Decisions)
	if totalItems == 0 {
		return 1.0, nil
	}

	lostItems := make(map[string]bool)
	matchCount := 0

	// 检查文件路径
	for _, fp_path := range fp.FilePaths {
		if matchInSlice(fp_path, compressedFp.FilePaths) || containsSemanticMatch(compressedText, fp_path) {
			matchCount++
		} else {
			lostItems["file:"+fp_path] = true
		}
	}

	// 检查标识符
	for _, id := range fp.Identifiers {
		if matchInSlice(id, compressedFp.Identifiers) || containsSemanticMatch(compressedText, id) {
			matchCount++
		} else {
			lostItems["identifier:"+id] = true
		}
	}

	// 检查错误信息
	for _, e := range fp.Errors {
		if matchInSlice(e, compressedFp.Errors) || containsSemanticMatch(compressedText, e) {
			matchCount++
		} else {
			lostItems["error:"+e] = true
		}
	}

	// 检查决策记录
	for _, d := range fp.Decisions {
		if matchInSlice(d, compressedFp.Decisions) || containsSemanticMatch(compressedText, d) {
			matchCount++
		} else {
			lostItems["decision:"+d] = true
		}
	}

	retentionRate := float64(matchCount) / float64(totalItems)

	lost := make([]string, 0, len(lostItems))
	for k := range lostItems {
		lost = append(lost, k)
	}
	return retentionRate, lost
}

// EvaluateQuality 综合质量评分 (0-1)。
// 考量：压缩比、信息保留率、关键信息密度。
// 评分公式：
//
//	ratioScore:     0-0.3（压缩比）
//	retentionScore: 0-0.5（信息保留率，核心指标）
//	densityScore:   0-0.2（压缩后文本中匹配到的指纹项数量评分）
func EvaluateQuality(originalTokens, compressedTokens int, fp KeyInfoFingerprint, compressed string) float64 {
	if originalTokens == 0 {
		return 1.0
	}

	// 1. 压缩比评分 (0-0.3)：压缩到 50% 以内得满分，越接近 100% 得分越低
	compressionRatio := float64(compressedTokens) / float64(originalTokens)
	var ratioScore float64
	switch {
	case compressionRatio <= 0.3:
		ratioScore = 0.3
	case compressionRatio <= 0.5:
		ratioScore = 0.25
	case compressionRatio <= 0.7:
		ratioScore = 0.2
	case compressionRatio < 1.0:
		ratioScore = 0.1
	default:
		ratioScore = 0.0
	}

	// 2. 信息保留率评分 (0-0.5)：核心指标，权重最高
	retainedCount := 0
	totalKeyItems := len(fp.FilePaths) + len(fp.Identifiers) + len(fp.Errors) + len(fp.Decisions)
	compressedLower := strings.ToLower(compressed)
	for _, p := range fp.FilePaths {
		if strings.Contains(compressedLower, strings.ToLower(p)) {
			retainedCount++
		}
	}
	for _, id := range fp.Identifiers {
		if strings.Contains(compressedLower, strings.ToLower(id)) {
			retainedCount++
		}
	}
	for _, e := range fp.Errors {
		if containsSemanticMatch(compressed, e) {
			retainedCount++
		}
	}
	for _, d := range fp.Decisions {
		if containsSemanticMatch(compressed, d) {
			retainedCount++
		}
	}
	var retentionScore float64
	if totalKeyItems > 0 {
		retention := float64(retainedCount) / float64(totalKeyItems)
		retentionScore = 0.5 * retention
	} else {
		retentionScore = 0.5
	}

	// 3. 关键信息密度评分 (0-0.2)：评估压缩后文本中匹配到的指纹项数量
	// retainedCount 即压缩后文本中出现的指纹项数，而非指纹总数
	var densityScore float64
	switch {
	case retainedCount >= 10:
		densityScore = 0.2
	case retainedCount >= 5:
		densityScore = 0.15
	case retainedCount >= 1:
		densityScore = 0.1
	default:
		densityScore = 0.0
	}

	return clamp01(ratioScore + retentionScore + densityScore)
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
var filePathRe = regexp.MustCompile(`(?:[A-Za-z]:[\\/]|[./~][\w./~-]*|/\S+?\.\w{1,10})(?:\s|[),;:}"'\n]|$)`)

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

// extractCodeIdentifiers 提取驼峰/下划线标识符。
var identifierRe = regexp.MustCompile(`\b[a-zA-Z_][a-zA-Z0-9_]{2,}\b`)

func extractCodeIdentifiers(text string) []string {
	matches := identifierRe.FindAllString(text, -1)
	seen := make(map[string]bool)
	var result []string
	for _, m := range matches {
		// 过滤常见英文单词（长度 > 3 且含大写字母或下划线，或像 snake_case）
		if isCommonWord(m) {
			continue
		}
		if seen[m] {
			continue
		}
		seen[m] = true
		result = append(result, m)
	}
	return result
}

// codeKeywords 高频代码词汇，用于过滤代码标识符中的常见关键字（区分于通用停用词）。
var codeKeywords = map[string]bool{
	"get": true, "set": true, "new": true, "err": true, "ctx": true,
	"val": true, "msg": true, "req": true, "res": true, "ret": true,
	"tmp": true, "buf": true, "opt": true, "ref": true, "src": true,
	"dst": true, "num": true, "idx": true, "len": true, "key": true,
}

func isCommonWord(word string) bool {
	if len(word) <= 3 {
		return true
	}
	return stopWords[strings.ToLower(word)] || codeKeywords[strings.ToLower(word)]
}

// isErrorContext 检测错误上下文。
// 优先匹配具体的错误模式（高精度），再匹配泛化错误关键词（会有些噪音）。
// 过滤元讨论模式（"error handling"、"error message" 等非错误文本）。
var errorIndicators = []string{
	"error", "failed", "failure", "panic", "fatal",
	"exception", "timeout", "refused", "denied",
	"cannot", "undefined", "nil pointer",
}

// errorMetaPatterns 元讨论模式：这些行的 "error" 不是实际错误，而是对错误的概念性讨论。
var errorMetaPatterns = []string{
	"error handling", "error message", "error recovery", "error format",
	"error output", "error type", "error checking", "error code",
	"error case", "error scenario", "error pattern", "error log",
	"if error", "if err", "return error", "no error", "the error",
	"any error", "this error", "that error", "an error",
}

func isErrorContext(text string) bool {
	lower := strings.ToLower(text)
	// 排除元讨论模式
	for _, meta := range errorMetaPatterns {
		if strings.Contains(lower, meta) {
			return false
		}
	}
	for _, indicator := range errorIndicators {
		if strings.Contains(lower, indicator) {
			return true
		}
	}
	return false
}

// extractErrorMessages 提取错误描述。
// 从含有错误关键词的行中提取信息。
func extractErrorMessages(text string) []string {
	var errors []string
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if isErrorContext(trimmed) {
			// 截取最多 200 字符
			runes := []rune(trimmed)
			if len(runes) > 200 {
				trimmed = string(runes[:200]) + "..."
			}
			errors = append(errors, trimmed)
		}
	}
	return errors
}

// extractDecisions 提取决策记录。
// 识别 "decided to", "chose to", "will use", "going to use", "@decision:" 等模式。
var decisionPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)decided to (.+?)[\.\n]`),
	regexp.MustCompile(`(?i)chose to (.+?)[\.\n]`),
	regexp.MustCompile(`(?i)will use (.+?)[\.\n]`),
	regexp.MustCompile(`(?i)going to use (.+?)[\.\n]`),
	regexp.MustCompile(`@decision:\s*(.+)`),
	regexp.MustCompile(`(?i)agreed to (.+?)[\.\n]`),
	regexp.MustCompile(`(?i)plan to (.+?)[\.\n]`),
}

func extractDecisions(text string) []string {
	var decisions []string
	seen := make(map[string]bool)
	for _, re := range decisionPatterns {
		matches := re.FindAllStringSubmatch(text, -1)
		for _, m := range matches {
			if len(m) >= 2 {
				d := strings.TrimSpace(m[1])
				if d != "" && !seen[d] {
					seen[d] = true
					decisions = append(decisions, d)
				}
			}
		}
	}
	return decisions
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
	// 按非字母数字字符分割
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

// ----------------------------------------------------------------
// 内部辅助
// ----------------------------------------------------------------

// ValidateMarkers checks compressed output for required structured markers.
// BUG FIX: 对文件路径使用 containsSemanticMatch（不区分大小写 + 关键词重叠），
// 而非 strings.Contains 精确子串匹配。压缩后 LLM 可能改变路径大小写或格式。
func ValidateMarkers(compressed string, fp KeyInfoFingerprint) []string {
	var missing []string
	for _, path := range fp.FilePaths {
		if !containsSemanticMatch(compressed, path) {
			missing = append(missing, "file:"+path)
		}
	}
	for _, err := range fp.Errors {
		if !containsSemanticMatch(compressed, err) {
			missing = append(missing, "error:"+err)
		}
	}
	for _, d := range fp.Decisions {
		if !containsSemanticMatch(compressed, d) {
			missing = append(missing, "decision:"+d)
		}
	}
	return missing
}

// Internal helpers

func joinMessages(messages []llm.ChatMessage) string {
	var b strings.Builder
	for _, msg := range messages {
		b.WriteString(msg.Content)
		b.WriteString("\n")
	}
	return b.String()
}

func matchInSlice(target string, slice []string) bool {
	for _, s := range slice {
		if s == target {
			return true
		}
	}
	return false
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

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
	sort.Slice(result, func(i, j int) bool {
		return result[i].LastSeenIter < result[j].LastSeenIter
	})

	return result
}

// extractPathsFromToolArgs 从工具调用的 Arguments JSON 中提取文件路径。
func extractPathsFromToolArgs(toolName, argsJSON string) []string {
	if argsJSON == "" {
		return nil
	}
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return nil
	}

	var paths []string
	switch toolName {
	case "Read", "Edit", "Write":
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
