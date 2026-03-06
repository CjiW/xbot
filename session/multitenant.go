package session

import (
	"fmt"
	"sync"
	"time"

	log "xbot/logger"
	"xbot/memory/flat"
	"xbot/storage/sqlite"
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

// MultiTenantSession manages multiple tenant sessions with SQLite backing
type MultiTenantSession struct {
	db                   *sqlite.DB
	tenantSvc            *sqlite.TenantService
	sessionSvc           *sqlite.SessionService
	memorySvc            *sqlite.MemoryService
	userProfileSvc       *sqlite.UserProfileService
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

	// 创建会话 MCP 管理器
	sessionKey := channel + ":" + chatID
	mcpManager := tools.NewSessionMCPManager(sessionKey, m.mcpConfigPath, m.mcpInactivityTimeout)

	// Create tenant session
	sess = &TenantSession{
		tenantID:   tenantID,
		channel:    channel,
		chatID:     chatID,
		sessionSvc: m.sessionSvc,
		memorySvc:  m.memorySvc,
		memory:     flat.New(tenantID, m.memorySvc),
		mcpManager: mcpManager,
		lastActive: time.Now(),
	}

	m.tenantCache[key] = sess
	return sess, nil
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

// GetUserProfile retrieves the user profile for a sender (cross-session)
func (m *MultiTenantSession) GetUserProfile(senderID string) (name, profile string, err error) {
	return m.userProfileSvc.GetProfile(senderID)
}

// SaveUserProfile saves or updates the user profile for a sender (cross-session)
func (m *MultiTenantSession) SaveUserProfile(senderID, name, profile string) error {
	return m.userProfileSvc.SaveProfile(senderID, name, profile)
}

// DBPath returns the database path (useful for migration checks)
func (m *MultiTenantSession) DBPath() string {
	return m.dbPath
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
