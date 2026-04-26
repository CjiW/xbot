package agent

import (
	"cmp"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"xbot/llm"
	log "xbot/logger"
)

// MaskedObservation 存储一条被遮蔽的 tool result 的完整信息。
type MaskedObservation struct {
	ID         string    `json:"id"`
	ToolName   string    `json:"tool_name"`
	Arguments  string    `json:"arguments"`
	Content    string    `json:"content"` // 完整的原始 tool result
	MaskedAt   time.Time `json:"masked_at"`
	MessageIdx int       `json:"message_idx"` // 在 messages slice 中的原始位置
}

const (
	defaultMaxEntries = 200       // 默认最大条数
	defaultMaxChars   = 2_000_000 // 默认最大存储字符数（~2MB）
)

// ObservationMaskStore 管理 observation masking 的存储和召回。
// 零成本压缩策略：遮蔽旧 tool result，不发给 LLM，但完整保留可通过工具召回。
// 双重容量限制：maxSize（条数）+ maxChars（总字符数），任一超限则淘汰最旧条目。
//
// 磁盘持久化：每个 mask entry 存为 {storeDir}/{id}.json，重启后可恢复。
// Recall 时优先内存查找，miss 则读磁盘。CleanOldEntries 时同步删除磁盘文件。
type ObservationMaskStore struct {
	mu         sync.RWMutex
	entries    []MaskedObservation // 按 mask 顺序存储
	maxSize    int                 // 最大存储条数
	maxChars   int                 // 最大存储总字符数
	totalChars int                 // 当前总字符数
	baseDir    string              // 磁盘存储基目录（baseDir/{tenantID}）
	storeDir   string              // 当前租户的磁盘存储目录（空 = 纯内存模式）
	tenantID   int64               // 当前租户 ID
	loaded     bool                // 是否已从磁盘加载
}

// NewObservationMaskStore 创建 ObservationMaskStore。
// storeDir 非空时启用磁盘持久化。
func NewObservationMaskStore(maxSize int, storeDir ...string) *ObservationMaskStore {
	if maxSize <= 0 {
		maxSize = defaultMaxEntries
	}
	s := &ObservationMaskStore{
		maxSize:  maxSize,
		maxChars: defaultMaxChars,
	}
	if len(storeDir) > 0 && storeDir[0] != "" {
		s.storeDir = storeDir[0]
	}
	return s
}

// SetStoreDir 设置固定磁盘存储目录（兼容旧调用/测试）。
func (s *ObservationMaskStore) SetStoreDir(dir string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.baseDir = ""
	s.storeDir = dir
	s.tenantID = 0
	s.entries = nil
	s.totalChars = 0
	s.loaded = false
}

// SetBaseDir 设置按租户分片的基目录：baseDir/{tenantID}/。
func (s *ObservationMaskStore) SetBaseDir(dir string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.baseDir = dir
	if s.tenantID != 0 {
		s.storeDir = filepath.Join(dir, fmt.Sprintf("%d", s.tenantID))
	} else {
		s.storeDir = ""
	}
	s.entries = nil
	s.totalChars = 0
	s.loaded = false
}

// SetTenantID 切换当前租户目录到 {baseDir}/{tenantID}/。
func (s *ObservationMaskStore) SetTenantID(tenantID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.baseDir == "" {
		s.tenantID = tenantID
		return
	}
	if s.tenantID == tenantID && s.loaded {
		return
	}
	s.tenantID = tenantID
	if tenantID == 0 {
		s.storeDir = ""
	} else {
		s.storeDir = filepath.Join(s.baseDir, fmt.Sprintf("%d", tenantID))
	}
	s.entries = nil
	s.totalChars = 0
	s.loaded = false
}

// ensureLoaded 首次访问时从磁盘目录加载所有 mask entries。
func (s *ObservationMaskStore) ensureLoaded() {
	if s.loaded || s.storeDir == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loaded {
		return
	}
	s.loaded = true

	dir := s.storeDir
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		log.WithError(err).Warn("ObservationMaskStore: failed to list store directory")
		return
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			log.WithError(err).WithField("file", entry.Name()).Warn("ObservationMaskStore: failed to read entry file")
			continue
		}
		var obs MaskedObservation
		if err := json.Unmarshal(data, &obs); err != nil {
			log.WithError(err).WithField("file", entry.Name()).Warn("ObservationMaskStore: failed to unmarshal entry")
			continue
		}
		s.entries = append(s.entries, obs)
		s.totalChars += len([]rune(obs.Content))
	}

	// 按时间排序（保证淘汰顺序正确）
	slices.SortFunc(s.entries, func(a, b MaskedObservation) int {
		return a.MaskedAt.Compare(b.MaskedAt)
	})

	if len(s.entries) > 0 {
		log.WithField("count", len(s.entries)).Info("ObservationMaskStore: loaded entries from disk")
	}
}

// persistEntry 将单个 entry 写入磁盘。
func (s *ObservationMaskStore) persistEntry(entry MaskedObservation) {
	if s.storeDir == "" {
		return
	}
	if err := os.MkdirAll(s.storeDir, 0o755); err != nil {
		log.WithError(err).Warn("ObservationMaskStore: failed to create store directory")
		return
	}
	data, err := json.Marshal(entry)
	if err != nil {
		log.WithError(err).Warn("ObservationMaskStore: failed to marshal entry")
		return
	}
	fp := filepath.Join(s.storeDir, entry.ID+".json")
	if err := os.WriteFile(fp, data, 0o644); err != nil {
		log.WithError(err).Warn("ObservationMaskStore: failed to persist entry")
	}
}

// deleteEntryFile 删除磁盘上的 entry 文件。
func (s *ObservationMaskStore) deleteEntryFile(id string) {
	if s.storeDir == "" {
		return
	}
	fp := filepath.Join(s.storeDir, id+".json")
	os.Remove(fp)
}

// generateMaskID 生成 mask ID: "mk_" + 8位随机 hex。
func generateMaskID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		// Fallback to timestamp-based ID if crypto/rand fails (should never happen)
		log.WithError(err).Warn("crypto/rand.Read failed in generateMaskID, using fallback")
		now := time.Now().UnixNano()
		return fmt.Sprintf("mk_%08x", now&0xffffffff)
	}
	return "mk_" + hex.EncodeToString(b)
}

// Mask 遮蔽一条 tool result，存储完整内容并返回占位符文本。
// 占位符格式: 📂 [masked:mk_xxxx] ToolName(args_preview) — N chars — 结果已遮蔽，使用 recall_masked 可查看完整内容
func (s *ObservationMaskStore) Mask(toolName, arguments, content string, messageIdx int) (MaskedObservation, string) {
	s.ensureLoaded()
	id := generateMaskID()

	entry := MaskedObservation{
		ID:         id,
		ToolName:   toolName,
		Arguments:  arguments,
		Content:    content,
		MaskedAt:   time.Now(),
		MessageIdx: messageIdx,
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// 双重容量限制：超条数或超字符数时，淘汰最旧条目
	contentLen := len([]rune(content))
	evictedCount := 0
	for len(s.entries) >= s.maxSize || (s.totalChars+contentLen > s.maxChars && len(s.entries) > 0) {
		evicted := s.entries[0]
		s.totalChars -= len([]rune(evicted.Content))
		s.entries = s.entries[1:]
		evictedCount++
		// 异步删除磁盘文件（不需要等，淘汰是低频操作）
		go s.deleteEntryFile(evicted.ID)
	}
	// 重新分配 slice，释放被淘汰条目占用的底层数组内存
	if evictedCount > 0 {
		newEntries := make([]MaskedObservation, len(s.entries))
		copy(newEntries, s.entries)
		s.entries = newEntries
	}
	s.entries = append(s.entries, entry)
	s.totalChars += contentLen

	// 持久化到磁盘
	s.persistEntry(entry)

	// 生成占位符
	argsPreview := arguments
	if len([]rune(argsPreview)) > 80 {
		argsPreview = string([]rune(argsPreview)[:80]) + "..."
	}
	charCount := len([]rune(content))
	placeholder := fmt.Sprintf("📂 [masked:%s] %s(%s) — %d chars — 结果已遮蔽，使用 recall_masked 可查看完整内容", id, toolName, argsPreview, charCount)

	return entry, placeholder
}

// Recall 按 ID 召回已遮蔽的完整 tool result。
// 优先内存查找，miss 则从磁盘加载。
func (s *ObservationMaskStore) Recall(id string) (MaskedObservation, error) {
	s.ensureLoaded()

	s.mu.RLock()
	for _, e := range s.entries {
		if e.ID == id {
			s.mu.RUnlock()
			return e, nil
		}
	}
	s.mu.RUnlock()

	// 内存未找到，尝试从磁盘读取
	if s.storeDir != "" {
		fp := filepath.Join(s.storeDir, id+".json")
		data, err := os.ReadFile(fp)
		if err == nil {
			var obs MaskedObservation
			if jsonErr := json.Unmarshal(data, &obs); jsonErr == nil {
				// 恢复到内存
				s.mu.Lock()
				s.entries = append(s.entries, obs)
				s.totalChars += len([]rune(obs.Content))
				s.mu.Unlock()
				return obs, nil
			}
		}
	}

	return MaskedObservation{}, fmt.Errorf("masked observation %s not found", id)
}

// List 列出所有已遮蔽的 observation（按 mask 时间倒序）。
func (s *ObservationMaskStore) List() []MaskedObservation {
	s.ensureLoaded()

	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]MaskedObservation, len(s.entries))
	copy(result, s.entries)
	return result
}

// Size 返回当前存储的 observation 数量。
func (s *ObservationMaskStore) Size() int {
	s.ensureLoaded()

	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.entries)
}

// Clear 清空所有已遮蔽的 observation（内存 + 磁盘）。
func (s *ObservationMaskStore) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = nil
	s.totalChars = 0
	// 删除磁盘文件
	if s.storeDir != "" {
		os.RemoveAll(s.storeDir)
		os.MkdirAll(s.storeDir, 0o755)
	}
}

// CleanOldEntries 删除 MaskedAt 在 cutoff 之前的记录。
// 用于压缩后清理：压缩点之前的 masked observation 已被摘要替代，不再需要召回。
func (s *ObservationMaskStore) CleanOldEntries(cutoff time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	var kept []MaskedObservation
	removedCount := 0
	for _, e := range s.entries {
		if e.MaskedAt.Before(cutoff) {
			s.totalChars -= len([]rune(e.Content))
			removedCount++
			// 删除磁盘文件
			go s.deleteEntryFile(e.ID)
		} else {
			kept = append(kept, e)
		}
	}
	s.entries = kept
	if removedCount > 0 {
		log.WithFields(log.Fields{
			"removed": removedCount,
			"kept":    len(kept),
			"cutoff":  cutoff.Format(time.RFC3339),
		}).Info("ObservationMaskStore: cleaned old entries after compression")
	}
	return removedCount
}

// CleanStale 清理超过指定天数的残留 mask 数据（磁盘文件）。
// 用于定期清理。
func (s *ObservationMaskStore) CleanStale(maxAgeDays int) {
	if maxAgeDays <= 0 {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -maxAgeDays)

	s.mu.RLock()
	baseDir := s.baseDir
	storeDir := s.storeDir
	s.mu.RUnlock()

	var dirs []string
	if baseDir != "" {
		entries, err := os.ReadDir(baseDir)
		if err != nil {
			if os.IsNotExist(err) {
				return
			}
			log.WithError(err).Warn("ObservationMaskStore: failed to list base directory for stale cleanup")
			return
		}
		for _, entry := range entries {
			if entry.IsDir() {
				dirs = append(dirs, filepath.Join(baseDir, entry.Name()))
			}
		}
	} else if storeDir != "" {
		dirs = append(dirs, storeDir)
	} else {
		return
	}

	removed := 0
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			log.WithError(err).WithField("dir", dir).Warn("ObservationMaskStore: failed to list store directory for stale cleanup")
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				continue
			}
			if info.ModTime().Before(cutoff) {
				os.Remove(filepath.Join(dir, entry.Name()))
				removed++
			}
		}
	}
	if removed > 0 {
		log.WithField("removed", removed).Info("ObservationMaskStore: cleaned stale entries from disk")
	}
}

// --- tools.MaskedRecallStore 接口实现 ---
// 这些方法让 ObservationMaskStore 满足 tools 包的 MaskedRecallStore 接口。
// 不需要导入 tools 包（Go 鸭子类型），只需方法签名匹配。

// RecallMasked 按 ID 召回已遮蔽的内容。
func (s *ObservationMaskStore) RecallMasked(id string) (string, string, error) {
	obs, err := s.Recall(id)
	if err != nil {
		return "", "", err
	}
	argsPreview := obs.Arguments
	if len([]rune(argsPreview)) > 80 {
		argsPreview = string([]rune(argsPreview)[:80]) + "..."
	}
	return fmt.Sprintf("%s(%s)", obs.ToolName, argsPreview), obs.Content, nil
}

// ListMasked 列出所有已遮蔽的 observation（摘要信息）。
func (s *ObservationMaskStore) ListMasked() []map[string]any {
	entries := s.List()
	result := make([]map[string]any, len(entries))
	for i, e := range entries {
		argsPreview := e.Arguments
		if len([]rune(argsPreview)) > 60 {
			argsPreview = string([]rune(argsPreview)[:60]) + "..."
		}
		result[i] = map[string]any{
			"id":           e.ID,
			"tool_name":    e.ToolName,
			"args_preview": argsPreview,
			"char_count":   len([]rune(e.Content)),
		}
	}
	return result
}

// calculateKeepGroups 根据 token 用量动态计算保留的 tool group 数量。
// 上下文越充裕，保留越多；上下文紧张时才减少。
func calculateKeepGroups(totalTokens, maxTokens int) int {
	ratio := float64(totalTokens) / float64(maxTokens)
	switch {
	case ratio <= 0.70:
		return 12
	case ratio <= 0.80:
		return 8
	case ratio <= 0.90:
		return 5
	default:
		return 3
	}
}

// MaskedEntry 记录一条被 mask 的消息的位置和新内容，用于持久化回 Session。
type MaskedEntry struct {
	MessageIndex int    // 在 messages slice 中的位置
	Content      string // 替换后的 content（占位符或空字符串）
}

// MaskOldToolResults 遮蔽 messages 中较旧的 tool result，返回修改后的 messages slice。
//
// 策略：
//   - 保留最近的 keepGroups 个完整 tool group
//   - 活跃文件相关的 tool group 不遮蔽（即使超过 keepGroups）
//   - 短内容（<300 chars）不遮蔽
//   - 连续纯工具组（assistant 无思考文本）折叠为一对消息
//   - 按 token 收益排序遮蔽（内容最长的优先）
//   - assistant 消息的思考内容保留（不 strip think blocks）
//
// 返回：修改后的 messages（新 slice），实际遮蔽数量，被修改的消息条目（用于持久化）。
func MaskOldToolResults(messages []llm.ChatMessage, store *ObservationMaskStore, keepGroups int) ([]llm.ChatMessage, int, []MaskedEntry) {
	if keepGroups <= 0 {
		keepGroups = 3
	}

	type toolGroup struct{ start, end int }

	var groups []toolGroup
	for i := range messages {
		if messages[i].Role == "assistant" && len(messages[i].ToolCalls) > 0 {
			g := toolGroup{start: i, end: i}
			for j := i + 1; j < len(messages) && messages[j].Role == "tool"; j++ {
				g.end = j
			}
			groups = append(groups, g)
		}
	}

	maskCount := len(groups) - keepGroups
	if maskCount <= 0 {
		return messages, 0, nil
	}

	// 提取活跃文件（最近 3 轮工具调用涉及的文件路径）
	activeFiles := ExtractActiveFiles(messages, 3)
	activePaths := make(map[string]bool)
	for _, af := range activeFiles {
		activePaths[af.Path] = true
	}

	// 收集可 mask 的候选组，排除活跃文件组
	type maskCandidate struct {
		groupIdx int
		grp      toolGroup
		chars    int // group 中所有 tool result 的总字符数
	}
	var candidates []maskCandidate

	for g := range maskCount {
		grp := groups[g]

		// 检查是否涉及活跃文件
		if isGroupActiveFile(messages, grp, activePaths) {
			continue
		}

		// 计算该 group 中可 mask 的 tool result 总字符数
		chars := 0
		allShort := true
		for j := grp.start; j <= grp.end; j++ {
			if messages[j].Role == "tool" {
				content := messages[j].Content
				// 跳过已遮蔽的
				if content == "" || content == "null" || strings.HasPrefix(content, "📂 [masked:") {
					continue
				}
				runeLen := len([]rune(content))
				if runeLen >= 300 {
					allShort = false
					chars += runeLen
				}
			}
		}
		// 所有 tool result 都太短，不 mask
		if allShort {
			continue
		}

		candidates = append(candidates, maskCandidate{groupIdx: g, grp: grp, chars: chars})
	}

	if len(candidates) == 0 {
		return messages, 0, nil
	}

	// 按 token 收益排序：字符数最多的优先 mask
	slices.SortFunc(candidates, func(a, b maskCandidate) int {
		return cmp.Compare(b.chars, a.chars) // descending
	})

	result := make([]llm.ChatMessage, len(messages))
	copy(result, messages)

	maskedTotal := 0
	var maskedEntries []MaskedEntry

	for _, cand := range candidates {
		grp := cand.grp

		// 判断该组是否为"纯工具组"（assistant 无思考文本，只有 tool_calls）
		assistantMsg := messages[grp.start]
		isPureToolGroup := strings.TrimSpace(llm.StripThinkBlocks(assistantMsg.Content)) == ""

		if isPureToolGroup {
			// 连续纯工具组折叠：收集该组的所有 tool result，折叠为一对消息
			n, entries := foldPureToolGroup(result, grp, store)
			maskedTotal += n
			maskedEntries = append(maskedEntries, entries...)
		} else {
			// 有思考内容的 assistant 组：独立 mask tool results，保留 assistant 完整内容
			for j := grp.start; j <= grp.end; j++ {
				msg := result[j]
				if msg.Role == "tool" {
					content := msg.Content
					if content != "" && content != "null" && !strings.HasPrefix(content, "📂 [masked:") {
						runeLen := len([]rune(content))
						if runeLen < 300 {
							continue // 短内容不 mask
						}
						_, placeholder := store.Mask(msg.ToolName, msg.ToolArguments, msg.Content, j)
						msg.Content = placeholder
						maskedTotal++
						maskedEntries = append(maskedEntries, MaskedEntry{MessageIndex: j, Content: placeholder})
					}
				}
				// assistant 消息：保留完整内容（不 strip think blocks）
				result[j] = msg
			}
		}
	}

	log.WithFields(map[string]any{
		"masked_count":  maskedTotal,
		"kept_groups":   keepGroups,
		"total_groups":  len(groups),
		"candidates":    len(candidates),
		"active_groups": maskCount - len(candidates),
	}).Info("Observation masking: masked old tool results")

	return result, maskedTotal, maskedEntries
}

// isGroupActiveFile 检查 tool group 是否涉及活跃文件。
func isGroupActiveFile(messages []llm.ChatMessage, grp struct{ start, end int }, activePaths map[string]bool) bool {
	for j := grp.start; j <= grp.end; j++ {
		msg := messages[j]
		if msg.Role == "assistant" {
			for _, tc := range msg.ToolCalls {
				paths := extractPathsFromToolArgs(tc.Name, tc.Arguments)
				for _, p := range paths {
					if activePaths[p] {
						return true
					}
				}
			}
		}
	}
	return false
}

// foldPureToolGroup 将一个纯工具组折叠为一对 assistant+tool 消息。
// 所有 tool result 存入 MaskStore，assistant 和第一条 tool 被替换为折叠摘要。
// 返回实际 mask 的 tool result 数量和被修改的消息条目。
func foldPureToolGroup(result []llm.ChatMessage, grp struct{ start, end int }, store *ObservationMaskStore) (int, []MaskedEntry) {
	// 收集所有 tool call 名称和参数
	var callSummaries []string
	maskedCount := 0
	var batchIDs []string
	var entries []MaskedEntry

	for j := grp.start; j <= grp.end; j++ {
		msg := result[j]
		if msg.Role == "assistant" {
			for _, tc := range msg.ToolCalls {
				argsPreview := tc.Arguments
				if len([]rune(argsPreview)) > 60 {
					argsPreview = string([]rune(argsPreview)[:60]) + "..."
				}
				callSummaries = append(callSummaries, fmt.Sprintf("%s(%s)", tc.Name, argsPreview))
			}
		} else if msg.Role == "tool" {
			content := msg.Content
			if content == "" || content == "null" || strings.HasPrefix(content, "📂 [masked:") {
				continue
			}
			// 短内容不 mask
			if len([]rune(content)) < 300 {
				continue
			}
			entry, _ := store.Mask(msg.ToolName, msg.ToolArguments, msg.Content, j)
			batchIDs = append(batchIDs, entry.ID)
			maskedCount++
		}
	}

	if maskedCount == 0 {
		return 0, nil
	}

	// 折叠 assistant：保留 ToolCalls 以维持 tool_use/tool_result 配对，只替换 Content
	summary := fmt.Sprintf("📂 [batch: %d tool calls folded] %s", maskedCount, strings.Join(callSummaries, ", "))
	assistantMsg := result[grp.start]
	assistantMsg.Content = summary
	result[grp.start] = assistantMsg
	entries = append(entries, MaskedEntry{MessageIndex: grp.start, Content: summary})

	// 折叠 tool results：替换 Content 为占位符，保留 ToolCallID 以维持配对
	batchPlaceholder := fmt.Sprintf("📂 [batch-masked: %d results] IDs: %s — recall_masked <id> to view", maskedCount, strings.Join(batchIDs, ", "))
	firstTool := true
	for j := grp.start + 1; j <= grp.end; j++ {
		msg := result[j]
		if msg.Role == "tool" {
			content := msg.Content
			if content == "" || content == "null" || strings.HasPrefix(content, "📂 [masked:") {
				continue
			}
			if len([]rune(content)) < 300 {
				continue
			}
			if firstTool {
				msg.Content = batchPlaceholder
				result[j] = msg
				entries = append(entries, MaskedEntry{MessageIndex: j, Content: batchPlaceholder})
				firstTool = false
			} else {
				msg.Content = "" // 清空后续 tool result
				result[j] = msg
				entries = append(entries, MaskedEntry{MessageIndex: j, Content: ""})
			}
		}
	}

	return maskedCount, entries
}
