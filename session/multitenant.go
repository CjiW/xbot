package session

import (
	"fmt"
	"sync"

	"xbot/storage/sqlite"
)

// MultiTenantSession manages multiple tenant sessions with SQLite backing
type MultiTenantSession struct {
	db           *sqlite.DB
	tenantSvc    *sqlite.TenantService
	sessionSvc   *sqlite.SessionService
	memorySvc    *sqlite.MemoryService
	mu           sync.RWMutex
	tenantCache  map[string]*TenantSession // key: "channel:chat_id"
	dbPath       string
}

// NewMultiTenant creates a new multi-tenant session manager
func NewMultiTenant(dbPath string) (*MultiTenantSession, error) {
	db, err := sqlite.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	m := &MultiTenantSession{
		db:          db,
		tenantSvc:   sqlite.NewTenantService(db),
		sessionSvc:  sqlite.NewSessionService(db),
		memorySvc:   sqlite.NewMemoryService(db),
		tenantCache: make(map[string]*TenantSession),
		dbPath:      dbPath,
	}

	return m, nil
}

// GetOrCreateSession retrieves or creates a tenant session for the given channel and chatID
func (m *MultiTenantSession) GetOrCreateSession(channel, chatID string) (*TenantSession, error) {
	key := channel + ":" + chatID

	// Fast path: check cache with read lock
	m.mu.RLock()
	sess, ok := m.tenantCache[key]
	m.mu.RUnlock()

	if ok {
		return sess, nil
	}

	// Slow path: acquire write lock and create session
	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check after acquiring write lock
	if sess, ok := m.tenantCache[key]; ok {
		return sess, nil
	}

	// Get or create tenant ID
	tenantID, err := m.tenantSvc.GetOrCreateTenantID(channel, chatID)
	if err != nil {
		return nil, fmt.Errorf("get/create tenant: %w", err)
	}

	// Create tenant session
	sess = &TenantSession{
		tenantID:   tenantID,
		channel:    channel,
		chatID:     chatID,
		sessionSvc: m.sessionSvc,
		memorySvc:  m.memorySvc,
		memory: &TenantMemory{
			tenantID:  tenantID,
			memorySvc: m.memorySvc,
		},
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

// DBPath returns the database path (useful for migration checks)
func (m *MultiTenantSession) DBPath() string {
	return m.dbPath
}
