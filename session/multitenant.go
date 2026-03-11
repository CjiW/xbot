package session

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	log "xbot/logger"
	"xbot/memory"
	"xbot/memory/flat"
	"xbot/memory/letta"
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

// WithArchivalService 设置向量归档服务（Letta 模式下使用）
// 如果不设置，会在 NewMultiTenant 中根据 EmbeddingConfig 自动创建
func WithArchivalService(svc *vectordb.ArchivalService) MultiTenantOption {
	return func(m *MultiTenantSession) {
		m.archivalSvc = svc
	}
}

// EmbeddingConfig 嵌入向量配置（用于自动创建归档服务）
type EmbeddingConfig struct {
	BaseURL string
	APIKey  string
	Model   string
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
	db                   *sqlite.DB
	tenantSvc            *sqlite.TenantService
	sessionSvc           *sqlite.SessionService
	memorySvc            *sqlite.MemoryService
	userProfileSvc       *sqlite.UserProfileService
	coreSvc              *sqlite.CoreMemoryService
	archivalSvc          *vectordb.ArchivalService
	toolIndexSvc         *vectordb.ToolIndexService
	recallTimeRangeFn    vectordb.RecallTimeRangeFunc // 时间范围会话历史搜索
	embeddingConfig      *EmbeddingConfig             // for auto-creating archival service
	memoryProvider       string                       // "flat" or "letta"
	mu                   sync.RWMutex
	tenantCache          map[string]*TenantSession // key: "channel:chat_id"
	dbPath               string
	mcpConfigPath        string         // MCP 配置文件路径
	mcpInactivityTimeout time.Duration  // MCP 不活跃超时配置
	mcpCleanupInterval   time.Duration  // MCP 清理扫描间隔
	sessionCacheTimeout  time.Duration  // 会话缓存超时配置
	cleanupStopCh        chan struct{}  // 清理协程停止信号
	cleanupWg            sync.WaitGroup // 清理协程等待组
	cleanupStopOnce      sync.Once      // 确保 StopCleanupRoutine 只执行一次
}

// NewMultiTenant creates a new multi-tenant session manager
func NewMultiTenant(dbPath string, opts ...MultiTenantOption) (*MultiTenantSession, error) {
	db, err := sqlite.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	m := &MultiTenantSession{
		db:                   db,
		tenantSvc:            sqlite.NewTenantService(db),
		sessionSvc:           sqlite.NewSessionService(db),
		memorySvc:            sqlite.NewMemoryService(db),
		userProfileSvc:       sqlite.NewUserProfileService(db),
		coreSvc:              sqlite.NewCoreMemoryService(db),
		memoryProvider:       "flat",
		tenantCache:          make(map[string]*TenantSession),
		dbPath:               dbPath,
		mcpConfigPath:        "mcp.json", // 默认在工作目录下
		mcpInactivityTimeout: 30 * time.Minute,
		mcpCleanupInterval:   5 * time.Minute,
		sessionCacheTimeout:  24 * time.Hour,
		cleanupStopCh:        make(chan struct{}),
	}

	// 应用配置选项
	for _, opt := range opts {
		opt(m)
	}

	// Letta 模式：自动创建 chromem-go 归档服务（如果未通过 WithArchivalService 注入）
	if m.memoryProvider == "letta" && m.archivalSvc == nil && m.embeddingConfig != nil {
		archivalDir := filepath.Join(filepath.Dir(dbPath), "archival")
		embFunc := vectordb.NewEmbeddingFunc(m.embeddingConfig.BaseURL, m.embeddingConfig.APIKey, m.embeddingConfig.Model)
		archSvc, err := vectordb.NewArchivalService(archivalDir, embFunc)
		if err != nil {
			log.WithError(err).Error("Failed to initialize archival memory (chromem-go), archival tools will be unavailable")
		} else {
			m.archivalSvc = archSvc
		}
	}

	// Letta 模式：自动创建工具索引服务（如果未通过 WithToolIndexService 注入）
	if m.memoryProvider == "letta" && m.toolIndexSvc == nil && m.embeddingConfig != nil {
		toolIndexDir := filepath.Join(filepath.Dir(dbPath), "tool_index")
		embFunc := vectordb.NewEmbeddingFunc(m.embeddingConfig.BaseURL, m.embeddingConfig.APIKey, m.embeddingConfig.Model)
		toolIdxSvc, err := vectordb.NewToolIndexService(toolIndexDir, embFunc)
		if err != nil {
			log.WithError(err).Error("Failed to initialize tool index service, tool search will be unavailable")
		} else {
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

// GetOrCreateSession retrieves or creates a tenant session for the given channel and chatID
func (m *MultiTenantSession) GetOrCreateSession(channel, chatID string) (*TenantSession, error) {
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
	mcpManager := tools.NewSessionMCPManager(sessionKey, m.mcpConfigPath, "", "", m.mcpInactivityTimeout)
	// 根据配置选择记忆提供者
	var memProvider memory.MemoryProvider
	switch m.memoryProvider {
	case "letta":
		memProvider = letta.New(tenantID, m.coreSvc, m.archivalSvc, m.memorySvc, m.toolIndexSvc)
		// 前向兼容：一次性迁移 user_profiles → core memory blocks
		m.migrateProfileToCoreMemory(tenantID)
	default:
		memProvider = flat.New(tenantID, m.memorySvc, m.toolIndexSvc)
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
func (m *MultiTenantSession) ConfigureSessionMCP(channel, chatID, senderID, workDir string) error {
	sess, err := m.GetOrCreateSession(channel, chatID)
	if err != nil {
		return err
	}

	mgr := sess.GetMCPManager()
	if mgr == nil {
		return nil
	}

	userConfigPath := tools.UserMCPConfigPath(workDir, senderID)
	workspaceRoot := tools.UserWorkspaceRoot(workDir, senderID)
	mgr.UpdateScope(userConfigPath, workspaceRoot)
	return nil
}

// migrateProfileToCoreMemory performs a one-time forward-compatible migration
// of legacy user_profiles data into Letta core memory blocks.
// - __me__ profile → persona block (bot identity)
// - Other profiles are not migrated to the human block (it's per-tenant, not per-user).
// Only writes if the target block is currently empty to avoid overwriting user edits.
func (m *MultiTenantSession) migrateProfileToCoreMemory(tenantID int64) {
	// Check if persona block is already populated
	persona, _, err := m.coreSvc.GetBlock(tenantID, "persona")
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

	if err := m.coreSvc.SetBlock(tenantID, "persona", selfProfile); err != nil {
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

// StopCleanupRoutine 停止清理协程（可安全重复调用）
func (m *MultiTenantSession) StopCleanupRoutine() {
	m.cleanupStopOnce.Do(func() {
		close(m.cleanupStopCh)
		m.cleanupWg.Wait()
	})
}

// cleanupInactiveResources 清理不活跃的资源（MCP 连接和会话缓存）
func (m *MultiTenantSession) cleanupInactiveResources() {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	var sessionsToDelete []string

	// 清理每个会话的不活跃 MCP 连接
	for key, sess := range m.tenantCache {
		lastActive := sess.CleanupInactiveMCPs()
		if now.Sub(lastActive) > m.sessionCacheTimeout {
			sessionsToDelete = append(sessionsToDelete, key)
		}
	}

	// 删除超时的会话
	for _, key := range sessionsToDelete {
		if sess, ok := m.tenantCache[key]; ok {
			sess.Close() // 关闭 MCP 连接
			delete(m.tenantCache, key)
			log.WithField("session", key).Info("Removed session from cache due to inactivity")
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
