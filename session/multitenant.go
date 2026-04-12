package session

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	log "xbot/logger"

	"xbot/llm"
	"xbot/memory"
	"xbot/memory/flat"
	"xbot/memory/letta"
	"xbot/config"
	"xbot/storage/sqlite"
	"xbot/storage/vectordb"
	"xbot/tools"
)

// MultiTenantOption 配置选项
type MultiTenantOption func(*MultiTenantSession)

// WithMCPTimeout 设置 MCP 不活跃超时时间
func WithMCPTimeout(timeout time.Duration) MultiTenantOption {
	return func(m *MultiTenantSession) {
		m.mcpInactivityTimeout = timeout
	}
}

// WithCleanupInterval 设置清理扫描间隔
func WithCleanupInterval(interval time.Duration) MultiTenantOption {
	return func(m *MultiTenantSession) {
		m.mcpCleanupInterval = interval
	}
}

// WithSessionCacheTimeout 设置会话缓存超时时间
func WithSessionCacheTimeout(timeout time.Duration) MultiTenantOption {
	return func(m *MultiTenantSession) {
		m.sessionCacheTimeout = timeout
	}
}

// WithMemoryProvider 设置记忆提供者 ("flat" 或 "letta")
func WithMemoryProvider(provider string) MultiTenantOption {
	return func(m *MultiTenantSession) {
		m.memoryProvider = provider
	}
}

// WithPersonaIsolation enables per-tenant persona isolation (no fallback to tenantID=0).
func WithPersonaIsolation(enabled bool) MultiTenantOption {
	return func(m *MultiTenantSession) {
		m.coreSvc.SetPersonaIsolation(enabled)
	}
}

// WithArchivalService 设置向量归档服务（Letta 模式下使用）
// 如果不设置，会在 NewMultiTenant 中根据 EmbeddingConfig 自动创建
func WithArchivalService(svc *vectordb.ArchivalService) MultiTenantOption {
	return func(m *MultiTenantSession) {
		m.archivalSvc = svc
	}
}

// EmbeddingConfig 嵌入向量配置（用于自动创建归档服务）
type EmbeddingConfig struct {
	Provider   string // Embedding 提供者: "openai"(默认) 或 "ollama"
	BaseURL    string
	APIKey     string
	Model      string
	MaxTokens  int     // Maximum tokens for embedding model (default 2048)
	LLMClient  llm.LLM // LLM client for content compression (optional)
	LLMModel   string  // Model name for LLM compression (optional)
	TokenModel string  // Model name for token counting (default "gpt-4")
}

// WithEmbeddingConfig 设置嵌入向量配置，NewMultiTenant 将自动创建 chromem-go 归档服务
func WithEmbeddingConfig(cfg EmbeddingConfig) MultiTenantOption {
	return func(m *MultiTenantSession) {
		m.embeddingConfig = &cfg
	}
}

// WithToolIndexService 设置工具索引服务
func WithToolIndexService(svc *vectordb.ToolIndexService) MultiTenantOption {
	return func(m *MultiTenantSession) {
		m.toolIndexSvc = svc
	}
}

// MultiTenantSession manages multiple tenant sessions with SQLite backing
type MultiTenantSession struct {
	db                    *sqlite.DB
	tenantSvc             *sqlite.TenantService
	sessionSvc            *sqlite.SessionService
	memorySvc             *sqlite.MemoryService
	userProfileSvc        *sqlite.UserProfileService
	tokenUsageSvc         *sqlite.UserTokenUsageService
	coreSvc               *sqlite.CoreMemoryService
	archivalSvc           *vectordb.ArchivalService
	toolIndexSvc          *vectordb.ToolIndexService
	recallTimeRangeFn     vectordb.RecallTimeRangeFunc // 时间范围会话历史搜索
	embeddingConfig       *EmbeddingConfig             // for auto-creating archival service
	memoryProvider        string                       // "flat" or "letta"
	mu                    sync.RWMutex
	tenantCache           map[string]*TenantSession // key: "channel:chat_id"
	dbPath                string
	mcpConfigPath         string          // MCP 配置文件路径
	mcpInactivityTimeout  time.Duration   // MCP 不活跃超时配置
	mcpCleanupInterval    time.Duration   // MCP 清理扫描间隔
	sessionCacheTimeout   time.Duration   // 会话缓存超时配置
	cleanupStopCh         chan struct{}   // 清理协程停止信号
	cleanupWg             sync.WaitGroup  // 清理协程等待组
	cleanupStopOnce       sync.Once       // 确保 StopCleanupRoutine 只执行一次
	shutdownCtx           context.Context // cancelled on StopCleanupRoutine; used as parent for background goroutines
	shutdownCancel        context.CancelFunc
	toolIndexFingerprints map[int64]string          // per-tenant catalog fingerprint (guarded by mu)
	toolIndexPrevNames    map[int64]map[string]bool // per-tenant previous tool name set (guarded by mu)
	onSessionEvict        func(sessionKey string)   // 会话被清理时的回调（用于 Registry 清理 sessionActivated/sessionRound）
}

// NewMultiTenant creates a new multi-tenant session manager
func NewMultiTenant(dbPath string, opts ...MultiTenantOption) (*MultiTenantSession, error) {
	db, err := sqlite.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	shutdownCtx, shutdownCancel := context.WithCancel(context.Background())
	m := &MultiTenantSession{
		db:                    db,
		tenantSvc:             sqlite.NewTenantService(db),
		sessionSvc:            sqlite.NewSessionService(db),
		memorySvc:             sqlite.NewMemoryService(db),
		userProfileSvc:        sqlite.NewUserProfileService(db),
		tokenUsageSvc:         sqlite.NewUserTokenUsageService(db),
		coreSvc:               sqlite.NewCoreMemoryService(db),
		memoryProvider:        "flat",
		tenantCache:           make(map[string]*TenantSession),
		toolIndexFingerprints: make(map[int64]string),
		toolIndexPrevNames:    make(map[int64]map[string]bool),
		dbPath:                dbPath,
		mcpConfigPath:         "mcp.json", // 默认在工作目录下
		mcpInactivityTimeout:  30 * time.Minute,
		mcpCleanupInterval:    5 * time.Minute,
		sessionCacheTimeout:   24 * time.Hour,
		cleanupStopCh:         make(chan struct{}),
		shutdownCtx:           shutdownCtx,
		shutdownCancel:        shutdownCancel,
	}

	// 应用配置选项
	for _, opt := range opts {
		opt(m)
	}

	// Build shared embedding limit options (used by both archival and tool index)
	var embOpts []vectordb.EmbeddingLimitOption
	if m.embeddingConfig != nil {
		if m.embeddingConfig.MaxTokens > 0 {
			embOpts = append(embOpts, vectordb.WithMaxTokens(m.embeddingConfig.MaxTokens))
		}
		if m.embeddingConfig.TokenModel != "" {
			embOpts = append(embOpts, vectordb.WithTokenModel(m.embeddingConfig.TokenModel))
		}
		if m.embeddingConfig.LLMClient != nil && m.embeddingConfig.LLMModel != "" {
			compressor := vectordb.LLMContentCompressor(m.embeddingConfig.LLMClient, m.embeddingConfig.LLMModel)
			embOpts = append(embOpts, vectordb.WithCompressor(compressor))
		}
	}

	// Letta 模式：自动创建 chromem-go 归档服务（如果未通过 WithArchivalService 注入）
	if m.memoryProvider == "letta" && m.archivalSvc == nil && m.embeddingConfig != nil {
		archivalDir := filepath.Join(filepath.Dir(dbPath), "archival")
		embFunc := vectordb.NewEmbeddingFunc(m.embeddingConfig.BaseURL, m.embeddingConfig.APIKey, m.embeddingConfig.Model, m.embeddingConfig.Provider, m.embeddingConfig.MaxTokens)

		archSvc, err := vectordb.NewArchivalService(archivalDir, embFunc, embOpts...)
		if err != nil {
			log.WithError(err).Error("Failed to initialize archival memory (chromem-go), archival tools will be unavailable")
		} else {
			m.archivalSvc = archSvc
		}
	}

	// Letta 模式：自动创建工具索引服务（如果未通过 WithToolIndexService 注入）
	if m.memoryProvider == "letta" && m.toolIndexSvc == nil && m.embeddingConfig != nil {
		toolIndexDir := filepath.Join(filepath.Dir(dbPath), "tool_index")
		embFunc := vectordb.NewEmbeddingFunc(m.embeddingConfig.BaseURL, m.embeddingConfig.APIKey, m.embeddingConfig.Model, m.embeddingConfig.Provider, m.embeddingConfig.MaxTokens)

		toolIdxSvc, err := vectordb.NewToolIndexService(toolIndexDir, embFunc, embOpts...)
		if err != nil {
			log.WithError(err).Warn("Tool index DB corrupted, removing and recreating")
			if removeErr := os.RemoveAll(toolIndexDir); removeErr != nil {
				log.WithError(removeErr).Error("Failed to remove corrupted tool index directory")
			} else {
				toolIdxSvc, err = vectordb.NewToolIndexService(toolIndexDir, embFunc, embOpts...)
				if err != nil {
					log.WithError(err).Error("Failed to initialize tool index service after recreation, tool search will be unavailable")
				}
			}
		}
		if toolIdxSvc != nil {
			m.toolIndexSvc = toolIdxSvc
		}
	}

	// Letta 模式：创建时间范围搜索函数
	if m.memoryProvider == "letta" {
		m.recallTimeRangeFn = vectordb.NewSQLiteRecallTimeRangeFunc(db.Conn())
	}

	return m, nil
}

// NewMultiTenantWithOptions 创建带配置选项的会话管理器（向后兼容）
func NewMultiTenantWithOptions(dbPath string, opts ...MultiTenantOption) (*MultiTenantSession, error) {
	return NewMultiTenant(dbPath, opts...)
}

// SetMCPConfigPath 设置 MCP 配置文件路径
func (m *MultiTenantSession) SetMCPConfigPath(path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mcpConfigPath = path
}

// RecordUserTokenUsage records token usage for a user (upsert).
func (m *MultiTenantSession) RecordUserTokenUsage(senderID, model string, inputTokens, outputTokens, cachedTokens int, conversationCount, llmCallCount int) error {
	return m.tokenUsageSvc.RecordUsage(m.db.Conn(), senderID, model, inputTokens, outputTokens, cachedTokens, conversationCount, llmCallCount)
}

// GetUserTokenUsage retrieves cumulative token usage for a user.
func (m *MultiTenantSession) GetUserTokenUsage(senderID string) (*sqlite.UserTokenUsage, error) {
	return m.tokenUsageSvc.GetUsage(senderID)
}

// GetDailyTokenUsage retrieves daily token usage for a user.
func (m *MultiTenantSession) GetDailyTokenUsage(senderID string, days int) ([]sqlite.DailyTokenUsage, error) {
	return m.tokenUsageSvc.GetDailyUsage(senderID, days)
}

// GetDailyTokenUsageSummary retrieves aggregated per-day usage for a user.
func (m *MultiTenantSession) GetDailyTokenUsageSummary(senderID string, days int) ([]sqlite.DailyTokenUsage, error) {
	return m.tokenUsageSvc.GetDailyUsageSummary(senderID, days)
}

// GetAllUserTokenUsage retrieves token usage for all users, sorted by total desc.
func (m *MultiTenantSession) GetAllUserTokenUsage() ([]sqlite.UserTokenUsage, error) {
	return m.tokenUsageSvc.GetAllUsage()
}

// GetOrCreateSession retrieves or creates a tenant session for the given channel and chatID.
// senderID is passed via context (letta.WithUserID) at call time, not here.
func (m *MultiTenantSession) GetOrCreateSession(channel, chatID string) (*TenantSession, error) {
	// Cache key: channel:chat_id (NOT senderID)
	// Per-user human block is handled dynamically via Recall/Memorize with senderID parameter
	key := channel + ":" + chatID

	// Fast path: check cache with read lock
	m.mu.RLock()
	sess, ok := m.tenantCache[key]
	m.mu.RUnlock()

	if ok {
		// 标记会话为活跃
		sess.MarkActive()
		return sess, nil
	}

	// Slow path: acquire write lock and create session
	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check after acquiring write lock
	if sess, ok := m.tenantCache[key]; ok {
		sess.MarkActive()
		return sess, nil
	}

	// Get or create tenant ID
	tenantID, err := m.tenantSvc.GetOrCreateTenantID(channel, chatID)
	if err != nil {
		return nil, fmt.Errorf("get/create tenant: %w", err)
	}

	// 创建会话 MCP 管理器（用户作用域由 ConfigureSessionMCP 在消息处理时注入）
	sessionKey := channel + ":" + chatID
	mcpManager := tools.NewSessionMCPManager(sessionKey, "", m.mcpConfigPath, "", "", m.mcpInactivityTimeout)

	// Letta 模式：创建 LettaMemory（userID 通过 context 传递，不存储在结构体中）
	// 根据配置选择记忆提供者
	var memProvider memory.MemoryProvider
	switch m.memoryProvider {
	case "letta":
		memProvider = letta.New(tenantID, m.coreSvc, m.archivalSvc, m.memorySvc, m.toolIndexSvc)
		// 前向兼容：一次性迁移 user_profiles → core memory blocks
		m.migrateProfileToCoreMemory(tenantID)
	default:
		// Flat memory: file-based storage under ~/.xbot/memory/{tenantID}/
		// Use tenantID (numeric) as directory name for filesystem safety
		flatMemDir := filepath.Join(config.XbotHome(), "memory", fmt.Sprintf("%d", tenantID))
		memProvider = flat.New(tenantID, flatMemDir)
	}
	// Create tenant session
	sess = &TenantSession{
		tenantID:   tenantID,
		channel:    channel,
		chatID:     chatID,
		sessionSvc: m.sessionSvc,
		memorySvc:  m.memorySvc,
		memory:     memProvider,
		mcpManager: mcpManager,
		lastActive: time.Now(),
	}

	m.tenantCache[key] = sess
	return sess, nil
}

// ConfigureSessionMCP 根据当前用户更新会话 MCP 作用域。
// 返回新注册的个人 MCP 工具名列表（用于立即激活），catalog 未变化时返回 nil。
func (m *MultiTenantSession) ConfigureSessionMCP(channel, chatID, senderID, workDir string) ([]string, error) {
	sess, err := m.GetOrCreateSession(channel, chatID)
	if err != nil {
		return nil, err
	}

	mgr := sess.GetMCPManager()
	if mgr == nil {
		return nil, nil
	}

	userConfigPath := tools.UserMCPConfigPath(workDir, senderID)
	workspaceRoot := tools.UserWorkspaceRoot(workDir, senderID)
	mgr.UpdateScope(senderID, userConfigPath, workspaceRoot)

	newTools := m.indexPersonalMCPTools(sess.TenantID(), mgr)
	return newTools, nil
}

// catalogFingerprint computes a stable hash of the MCP catalog tool names.
func catalogFingerprint(catalog []tools.MCPServerCatalogEntry) string {
	var keys []string
	for _, entry := range catalog {
		for _, toolName := range entry.ToolNames {
			keys = append(keys, entry.Name+":"+toolName)
		}
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte{0})
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// indexPersonalMCPTools indexes personal MCP tools for a tenant.
// Returns only truly NEW tool names (added since last catalog snapshot) for immediate activation.
// On first load or when catalog is unchanged, returns nil.
func (m *MultiTenantSession) indexPersonalMCPTools(tenantID int64, mgr *tools.SessionMCPManager) []string {
	if mgr == nil {
		return nil
	}

	catalog := mgr.GetCatalog()
	if len(catalog) == 0 {
		return nil
	}

	// Build tool entries and names
	var entries []memory.ToolIndexEntry
	var toolNames []string
	for _, entry := range catalog {
		for _, toolName := range entry.ToolNames {
			fullName := fmt.Sprintf("mcp_%s_%s", entry.Name, toolName)
			desc := fmt.Sprintf("MCP server: %s. Tool: %s", entry.Name, toolName)
			if entry.Instructions != "" {
				desc = fmt.Sprintf("%s. %s", desc, entry.Instructions)
			}
			entries = append(entries, memory.ToolIndexEntry{
				Name:        fullName,
				ServerName:  entry.Name,
				Source:      "personal",
				Description: desc,
			})
			toolNames = append(toolNames, fullName)
		}
	}

	// Fingerprint check: skip if catalog unchanged
	// Check in-memory first, fall back to disk (survives restart)
	fp := catalogFingerprint(catalog)
	m.mu.RLock()
	prev := m.toolIndexFingerprints[tenantID]
	prevNames := m.toolIndexPrevNames[tenantID]
	m.mu.RUnlock()
	if prev == "" && m.toolIndexSvc != nil {
		prev = m.toolIndexSvc.GetFingerprint(tenantID)
		if prev != "" {
			m.mu.Lock()
			m.toolIndexFingerprints[tenantID] = prev
			m.mu.Unlock()
		}
	}
	if fp == prev {
		return nil
	}

	// Catalog changed — kick off async indexing (if tool index is available)
	if m.toolIndexSvc != nil && len(entries) > 0 {
		entriesCopy := make([]memory.ToolIndexEntry, len(entries))
		copy(entriesCopy, entries)
		fpCopy := fp
		go func() {
			ctx, cancel := context.WithTimeout(m.shutdownCtx, 10*time.Minute)
			defer cancel()
			if err := m.IndexToolsForTenant(ctx, tenantID, entriesCopy); err != nil {
				if m.shutdownCtx.Err() != nil {
					log.Debugf("Index personal MCP tools for tenant %d cancelled by shutdown", tenantID)
				} else {
					log.WithError(err).Warnf("Failed to index personal MCP tools for tenant %d", tenantID)
				}
				return
			}
			log.Infof("Indexed %d personal MCP tools for tenant %d", len(entriesCopy), tenantID)
			m.toolIndexSvc.SetFingerprint(tenantID, fpCopy)
			m.mu.Lock()
			m.toolIndexFingerprints[tenantID] = fpCopy
			m.mu.Unlock()
		}()
	}

	// Update tool name snapshot (fingerprint updated in goroutine after success)
	currentNames := make(map[string]bool, len(toolNames))
	for _, name := range toolNames {
		currentNames[name] = true
	}
	m.mu.Lock()
	m.toolIndexPrevNames[tenantID] = currentNames
	m.mu.Unlock()

	// First load: no previous snapshot → all tools are pre-existing, not "new"
	if prevNames == nil {
		return nil
	}

	// Subsequent change: only return tools not in previous catalog
	var newTools []string
	for _, name := range toolNames {
		if !prevNames[name] {
			newTools = append(newTools, name)
		}
	}
	return newTools
}

// migrateProfileToCoreMemory performs a one-time forward-compatible migration
// of legacy user_profiles data into Letta core memory blocks.
// - __me__ profile → persona block (bot identity, global per-tenant)
// Only writes if the target block is currently empty to avoid overwriting user edits.
func (m *MultiTenantSession) migrateProfileToCoreMemory(tenantID int64) {
	// Check if persona block is already populated (persona is global, use "" for userID)
	persona, _, err := m.coreSvc.GetBlock(tenantID, "persona", "")
	if err != nil {
		log.WithError(err).Warn("Profile migration: failed to read persona block")
		return
	}
	if persona != "" {
		return // Already has content, skip migration
	}

	// Read self profile (__me__)
	_, selfProfile, err := m.userProfileSvc.GetProfile("__me__")
	if err != nil {
		log.WithError(err).Warn("Profile migration: failed to read __me__ profile")
		return
	}
	if selfProfile == "" {
		return // No profile to migrate
	}

	// Write to persona block (global, use "" for userID)
	if err := m.coreSvc.SetBlock(tenantID, "persona", selfProfile, ""); err != nil {
		log.WithError(err).Warn("Profile migration: failed to write persona block")
		return
	}
	log.WithField("tenant_id", tenantID).Info("Migrated __me__ profile to persona core memory block")
}

// RecallTimeRangeFunc returns the time-range recall search function (nil if not in Letta mode).
func (m *MultiTenantSession) RecallTimeRangeFunc() vectordb.RecallTimeRangeFunc {
	return m.recallTimeRangeFn
}

// IndexToolsForTenant indexes MCP tools for a specific tenant.
func (m *MultiTenantSession) IndexToolsForTenant(ctx context.Context, tenantID int64, tools []memory.ToolIndexEntry) error {
	if m.toolIndexSvc == nil {
		return nil // Tool index not available (flat mode or no embedding config)
	}
	// Convert memory.ToolIndexEntry to vectordb.ToolIndexEntry
	entries := make([]vectordb.ToolIndexEntry, len(tools))
	for i, t := range tools {
		entries[i] = vectordb.ToolIndexEntry{
			Name:        t.Name,
			ServerName:  t.ServerName,
			Source:      t.Source,
			Description: t.Description,
		}
	}
	return m.toolIndexSvc.IndexTools(ctx, tenantID, entries)
}

// Close closes the database connection
func (m *MultiTenantSession) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.db != nil {
		return m.db.Close()
	}
	return nil
}

// DBPath returns the database path (useful for migration checks)
func (m *MultiTenantSession) DBPath() string {
	return m.dbPath
}

// DB returns the underlying SQLite database connection
func (m *MultiTenantSession) DB() *sqlite.DB {
	return m.db
}

// CoreMemoryService returns the shared core memory service.
func (m *MultiTenantSession) CoreMemoryService() *sqlite.CoreMemoryService {
	return m.coreSvc
}

// ArchivalService returns the shared archival memory service.
func (m *MultiTenantSession) ArchivalService() *vectordb.ArchivalService {
	return m.archivalSvc
}

// MemoryService returns the shared memory (recall) service.
func (m *MultiTenantSession) MemoryService() *sqlite.MemoryService {
	return m.memorySvc
}

// ToolIndexService returns the shared tool index service.
func (m *MultiTenantSession) ToolIndexService() *vectordb.ToolIndexService {
	return m.toolIndexSvc
}

// RecallTimeRange returns the recall time-range search function.
func (m *MultiTenantSession) RecallTimeRange() vectordb.RecallTimeRangeFunc {
	return m.recallTimeRangeFn
}

// NewLettaMemory creates a new LettaMemory instance with an independent tenantID.
// The service instances (coreSvc, archivalSvc, etc.) are shared — data isolation
// is provided by the tenantID parameter.
func (m *MultiTenantSession) NewLettaMemory(tenantID int64) *letta.LettaMemory {
	return letta.New(tenantID, m.coreSvc, m.archivalSvc, m.memorySvc, m.toolIndexSvc)
}

// GetSessionMCPManager 实现 SessionMCPManagerProvider 接口
func (m *MultiTenantSession) GetSessionMCPManager(sessionKey string) *tools.SessionMCPManager {
	m.mu.RLock()
	sess, ok := m.tenantCache[sessionKey]
	m.mu.RUnlock()

	if ok {
		return sess.GetMCPManager()
	}
	return nil
}

// StartCleanupRoutine 启动后台清理协程
func (m *MultiTenantSession) StartCleanupRoutine() {
	m.cleanupWg.Add(1)
	go func() {
		defer m.cleanupWg.Done()
		ticker := time.NewTicker(m.mcpCleanupInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				m.cleanupInactiveResources()
			case <-m.cleanupStopCh:
				return
			}
		}
	}()
	log.WithFields(log.Fields{
		"mcpCleanupInterval":  m.mcpCleanupInterval,
		"sessionCacheTimeout": m.sessionCacheTimeout,
	}).Info("MCP cleanup routine started")
}

// StopCleanupRoutine 停止清理协程并取消后台任务（可安全重复调用）
func (m *MultiTenantSession) StopCleanupRoutine() {
	m.cleanupStopOnce.Do(func() {
		m.shutdownCancel()
		close(m.cleanupStopCh)
		m.cleanupWg.Wait()
	})
}

// SetOnSessionEvict 设置会话被清理时的回调（用于 Registry 清理 sessionActivated/sessionRound）
func (m *MultiTenantSession) SetOnSessionEvict(cb func(sessionKey string)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onSessionEvict = cb
}

// cleanupInactiveResources 清理不活跃的资源（MCP 连接和会话缓存）
func (m *MultiTenantSession) cleanupInactiveResources() {
	m.mu.Lock()

	now := time.Now()
	var sessionsToDelete []string

	// 收集需要清理的会话（持锁期间只做轻量操作，不执行 I/O）
	for key, sess := range m.tenantCache {
		// 使用 lastActive 字段判断是否超时（避免在锁内调用 CleanupInactiveMCPs 的 I/O）
		lastActive := sess.LastActive()
		if now.Sub(lastActive) > m.sessionCacheTimeout {
			sessionsToDelete = append(sessionsToDelete, key)
		}
	}

	// 收集要关闭的会话并从缓存中移除（持锁期间只做轻量操作）
	type sessionToClose struct {
		key  string
		sess *TenantSession
	}
	var toClose []sessionToClose
	for _, key := range sessionsToDelete {
		if sess, ok := m.tenantCache[key]; ok {
			toClose = append(toClose, sessionToClose{key: key, sess: sess})
			delete(m.tenantCache, key)
		}
	}

	// 获取回调并释放锁
	onEvict := m.onSessionEvict
	m.mu.Unlock()

	// 释放锁后执行所有 I/O 操作（关闭 MCP 连接）和回调
	for _, item := range toClose {
		// CleanupInactiveMCPs 和 Close 都包含 I/O，在锁外执行
		item.sess.CleanupInactiveMCPs()
		item.sess.Close()
		log.WithField("session", item.key).Info("Removed session from cache due to inactivity")
		// 通知 Registry 清理该会话的激活状态
		if onEvict != nil {
			onEvict(item.key)
		}
	}
}

// InvalidateAll 使所有缓存会话的 MCP 连接失效，强制下次使用时重新加载
func (m *MultiTenantSession) InvalidateAll() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for key, sess := range m.tenantCache {
		sess.InvalidateMCP()
		log.WithField("session", key).Debug("Invalidated session MCP")
	}

	log.Info("All session MCP connections invalidated, will reload on next use")
}

// InvalidateSessionMCP 使特定会话的 MCP 连接失效
// 用于 token 刷新等场景，需要重新建立特定 MCP 服务器的连接
func (m *MultiTenantSession) InvalidateSessionMCP(sessionKey string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if sess, ok := m.tenantCache[sessionKey]; ok {
		sess.InvalidateMCP()
		log.WithField("session", sessionKey).Info("Session MCP invalidated")
	}
}

// ClearMemory clears the specified memory type(s) for a tenant identified by (channel, chatID).
// targetType is one of: "session", "core_persona", "core_human", "core_working",
// "core_all", "long_term", "event_history", "archival", "reset_all".
func (m *MultiTenantSession) ClearMemory(ctx context.Context, channel, chatID, targetType, userID string) error {
	tenantID, err := m.tenantSvc.GetOrCreateTenantID(channel, chatID)
	if err != nil {
		return fmt.Errorf("resolve tenant: %w", err)
	}

	var errs []string
	appendErr := func(name string, err error) {
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", name, err))
		}
	}

	switch targetType {
	case "session":
		appendErr("session", m.sessionSvc.Clear(tenantID))
		// Evict cached session so next request loads fresh state
		sessionKey := channel + ":" + chatID
		m.mu.Lock()
		delete(m.tenantCache, sessionKey)
		m.mu.Unlock()
	case "core_persona":
		appendErr("persona", m.coreSvc.ClearBlock(tenantID, "persona", ""))
	case "core_human":
		appendErr("human", m.coreSvc.ClearBlock(tenantID, "human", userID))
	case "core_working":
		appendErr("working_context", m.coreSvc.ClearBlock(tenantID, "working_context", ""))
	case "core_all":
		appendErr("core_all", m.coreSvc.ClearAllBlocks(tenantID, userID))
		// Evict cached session to reset in-memory core memory references
		sessionKey := channel + ":" + chatID
		m.mu.Lock()
		delete(m.tenantCache, sessionKey)
		m.mu.Unlock()
	case "long_term":
		appendErr("long_term", m.memorySvc.ClearLongTerm(ctx, tenantID))
	case "event_history":
		appendErr("event_history", m.memorySvc.ClearHistory(ctx, tenantID))
	case "archival":
		if m.archivalSvc != nil {
			appendErr("archival", m.archivalSvc.ClearAll(ctx, tenantID))
		}
	case "reset_all":
		appendErr("session", m.sessionSvc.Clear(tenantID))
		appendErr("core_all", m.coreSvc.ClearAllBlocks(tenantID, userID))
		appendErr("long_term", m.memorySvc.ClearLongTerm(ctx, tenantID))
		appendErr("event_history", m.memorySvc.ClearHistory(ctx, tenantID))
		appendErr("state", m.memorySvc.ClearState(ctx, tenantID))
		if m.archivalSvc != nil {
			appendErr("archival", m.archivalSvc.ClearAll(ctx, tenantID))
		}
		// Evict session from cache to reset in-memory state
		sessionKey := channel + ":" + chatID
		m.mu.Lock()
		delete(m.tenantCache, sessionKey)
		m.mu.Unlock()
	default:
		return fmt.Errorf("unknown target type: %s", targetType)
	}

	if len(errs) > 0 {
		return fmt.Errorf("部分清空失败: %s", strings.Join(errs, "; "))
	}

	log.WithFields(log.Fields{
		"tenant_id":   tenantID,
		"target_type": targetType,
	}).Info("Memory cleared")
	return nil
}

// GetMemoryStats returns statistics for all memory types of a tenant.
func (m *MultiTenantSession) GetMemoryStats(ctx context.Context, channel, chatID, userID string) map[string]string {
	stats := map[string]string{}

	tenantID, err := m.tenantSvc.GetOrCreateTenantID(channel, chatID)
	if err != nil {
		return stats
	}

	// Session message count
	if count, err := m.sessionSvc.GetMessagesCount(tenantID); err == nil {
		stats["session"] = fmt.Sprintf("%d 条消息", count)
	}

	// Core memory blocks
	if blocks, err := m.coreSvc.GetAllBlocks(tenantID, userID); err == nil {
		for _, name := range []string{"persona", "human", "working_context"} {
			if content, ok := blocks[name]; ok && content != "" {
				stats[name] = fmt.Sprintf("%d chars", len(content))
			}
		}
	}

	// Archival memory count
	if m.archivalSvc != nil {
		if count, err := m.archivalSvc.Count(tenantID); err == nil && count > 0 {
			stats["archival"] = fmt.Sprintf("%d 条", count)
		}
	}

	// Long-term memory
	if content, err := m.memorySvc.ReadLongTerm(ctx, tenantID); err == nil && content != "" {
		stats["long_term"] = "有内容"
	}

	// Event history count
	if count, err := m.memorySvc.GetHistoryCount(ctx, tenantID); err == nil && count > 0 {
		stats["event_history"] = fmt.Sprintf("%d 条", count)
	}

	return stats
}
