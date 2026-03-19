package agent

import (
	"regexp"
	"strings"
	"unicode"

	"xbot/llm"
)

// KeyInfoFingerprint 对话关键信息指纹，用于压缩质量校验。
type KeyInfoFingerprint struct {
	FilePaths   []string
	Identifiers []string
	Errors      []string
	Decisions   []string
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
		for _, e := range extractErrorMessages(text) {
			addUnique(&fp.Errors, e)
		}
		for _, d := range extractDecisions(text) {
			addUnique(&fp.Decisions, d)
		}
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
// 考量：信息保留率、压缩比、结构化标记覆盖。
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

	// 2. 结构化标记评分 (0-0.2)：标记越多，说明保留的上下文越结构化
	markerCount := countStructuredMarkers(compressed)
	var markerScore float64
	switch {
	case markerCount >= 5:
		markerScore = 0.2
	case markerCount >= 3:
		markerScore = 0.15
	case markerCount >= 1:
		markerScore = 0.1
	default:
		markerScore = 0.0
	}

	// 3. 关键信息密度评分 (0-0.2)：指纹项总数
	totalKeyItems := len(fp.FilePaths) + len(fp.Identifiers) + len(fp.Errors) + len(fp.Decisions)
	var densityScore float64
	switch {
	case totalKeyItems >= 10:
		densityScore = 0.2
	case totalKeyItems >= 5:
		densityScore = 0.15
	case totalKeyItems >= 1:
		densityScore = 0.1
	default:
		densityScore = 0.0
	}

	// 4. 信息保留率评分 (0-0.3)：需要压缩前后对比，这里基于指纹在压缩结果中的匹配度
	retainedCount := 0
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
		retentionScore = 0.3 * retention
	} else {
		retentionScore = 0.3
	}

	return clamp01(ratioScore + markerScore + densityScore + retentionScore)
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
var errorIndicators = []string{
	"error", "failed", "failure", "panic", "fatal",
	"exception", "timeout", "refused", "denied",
	"cannot", "undefined", "nil pointer",
}

func isErrorContext(text string) bool {
	lower := strings.ToLower(text)
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

// countStructuredMarkers 统计 @file:, @func: 等结构化标记数量。
var structuredMarkerRe = regexp.MustCompile(`@(?:file|func|type|error|decision|todo|config):\S`)

func countStructuredMarkers(text string) int {
	return len(structuredMarkerRe.FindAllString(text, -1))
}

// ----------------------------------------------------------------
// 内部辅助
// ----------------------------------------------------------------

// ValidateMarkers checks compressed output for required structured markers.
func ValidateMarkers(compressed string, fp KeyInfoFingerprint) []string {
	var missing []string
	for _, path := range fp.FilePaths {
		if !strings.Contains(compressed, path) {
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
