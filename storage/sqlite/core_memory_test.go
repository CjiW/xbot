package sqlite

import (
	"strings"
	"testing"
)

func newTestCoreMemoryDB(t *testing.T) (*DB, *CoreMemoryService, int64) {
	t.Helper()
	dbPath := t.TempDir() + "/test.db"
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	tenantSvc := NewTenantService(db)
	tenantID, err := tenantSvc.GetOrCreateTenantID("test", "chat1")
	if err != nil {
		t.Fatalf("Failed to create tenant: %v", err)
	}

	coreSvc := NewCoreMemoryService(db)
	return db, coreSvc, tenantID
}

// TestCoreMemoryService_TenantIsolation_Persona verifies that persona is globally
// shared (tenantID=0). Writing persona under one tenant must be visible when read
// under a different tenant.
func TestCoreMemoryService_TenantIsolation_Persona(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	tenantSvc := NewTenantService(db)
	tenantID1, err := tenantSvc.GetOrCreateTenantID("test", "chat1")
	if err != nil {
		t.Fatalf("Failed to create tenant1: %v", err)
	}
	tenantID2, err := tenantSvc.GetOrCreateTenantID("test", "chat2")
	if err != nil {
		t.Fatalf("Failed to create tenant2: %v", err)
	}

	coreSvc := NewCoreMemoryService(db)

	if err := coreSvc.InitBlocks(tenantID1, "user1"); err != nil {
		t.Fatalf("InitBlocks: %v", err)
	}

	// Write persona under tenant1
	want := "I am a helpful assistant."
	if err := coreSvc.SetBlock(tenantID1, "persona", want, "user1"); err != nil {
		t.Fatalf("SetBlock persona: %v", err)
	}

	// Read persona under tenant2 — must see the same value (global share via tenantID=0)
	got, _, err := coreSvc.GetBlock(tenantID2, "persona", "user2")
	if err != nil {
		t.Fatalf("GetBlock persona: %v", err)
	}
	if got != want {
		t.Errorf("persona not globally shared: got %q, want %q", got, want)
	}
}

// TestCoreMemoryService_TenantIsolation_Human verifies that human blocks are
// shared per user across tenants (stored at tenantID=0 keyed by userID).
func TestCoreMemoryService_TenantIsolation_Human(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	tenantSvc := NewTenantService(db)
	tenantID1, err := tenantSvc.GetOrCreateTenantID("test", "chat1")
	if err != nil {
		t.Fatalf("Failed to create tenant1: %v", err)
	}
	tenantID2, err := tenantSvc.GetOrCreateTenantID("test", "chat2")
	if err != nil {
		t.Fatalf("Failed to create tenant2: %v", err)
	}

	coreSvc := NewCoreMemoryService(db)

	if err := coreSvc.InitBlocks(tenantID1, "alice"); err != nil {
		t.Fatalf("InitBlocks: %v", err)
	}

	// Write human for alice under tenant1
	want := "Alice prefers Go over Python."
	if err := coreSvc.SetBlock(tenantID1, "human", want, "alice"); err != nil {
		t.Fatalf("SetBlock human: %v", err)
	}

	// Read human for alice under tenant2 — must see the same value (cross-tenant per-user)
	got, _, err := coreSvc.GetBlock(tenantID2, "human", "alice")
	if err != nil {
		t.Fatalf("GetBlock human: %v", err)
	}
	if got != want {
		t.Errorf("human not shared across tenants for same user: got %q, want %q", got, want)
	}
}

// TestCoreMemoryService_TenantIsolation_WorkingContext verifies that
// working_context is isolated per tenant.
func TestCoreMemoryService_TenantIsolation_WorkingContext(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	tenantSvc := NewTenantService(db)
	tenantID1, err := tenantSvc.GetOrCreateTenantID("test", "chat1")
	if err != nil {
		t.Fatalf("Failed to create tenant1: %v", err)
	}
	tenantID2, err := tenantSvc.GetOrCreateTenantID("test", "chat2")
	if err != nil {
		t.Fatalf("Failed to create tenant2: %v", err)
	}

	coreSvc := NewCoreMemoryService(db)

	if err := coreSvc.InitBlocks(tenantID1, "user1"); err != nil {
		t.Fatalf("InitBlocks tenant1: %v", err)
	}
	if err := coreSvc.InitBlocks(tenantID2, "user2"); err != nil {
		t.Fatalf("InitBlocks tenant2: %v", err)
	}

	// Write working_context for tenant1
	wc1 := "Tenant1 working on feature A."
	if err := coreSvc.SetBlock(tenantID1, "working_context", wc1, "user1"); err != nil {
		t.Fatalf("SetBlock working_context tenant1: %v", err)
	}

	// Write working_context for tenant2
	wc2 := "Tenant2 working on feature B."
	if err := coreSvc.SetBlock(tenantID2, "working_context", wc2, "user2"); err != nil {
		t.Fatalf("SetBlock working_context tenant2: %v", err)
	}

	// Each tenant must see only its own working_context
	got1, _, err := coreSvc.GetBlock(tenantID1, "working_context", "user1")
	if err != nil {
		t.Fatalf("GetBlock working_context tenant1: %v", err)
	}
	if got1 != wc1 {
		t.Errorf("tenant1 working_context: got %q, want %q", got1, wc1)
	}

	got2, _, err := coreSvc.GetBlock(tenantID2, "working_context", "user2")
	if err != nil {
		t.Fatalf("GetBlock working_context tenant2: %v", err)
	}
	if got2 != wc2 {
		t.Errorf("tenant2 working_context: got %q, want %q", got2, wc2)
	}

	if got1 == got2 {
		t.Errorf("working_context should differ between tenants but both returned %q", got1)
	}
}

// TestCoreMemoryService_ReadWriteConsistency verifies that content written via
// SetBlock is returned unchanged by both GetBlock and GetAllBlocks.
func TestCoreMemoryService_ReadWriteConsistency(t *testing.T) {
	_, coreSvc, tenantID := newTestCoreMemoryDB(t)

	if err := coreSvc.InitBlocks(tenantID, "bob"); err != nil {
		t.Fatalf("InitBlocks: %v", err)
	}

	cases := []struct {
		block   string
		content string
		userID  string
	}{
		{"persona", "I am xbot.", "bob"},
		{"human", "Bob likes SQLite.", "bob"},
		{"working_context", "Reviewing PR #42.", "bob"},
	}

	for _, tc := range cases {
		if err := coreSvc.SetBlock(tenantID, tc.block, tc.content, tc.userID); err != nil {
			t.Fatalf("SetBlock %s: %v", tc.block, err)
		}
		got, _, err := coreSvc.GetBlock(tenantID, tc.block, tc.userID)
		if err != nil {
			t.Fatalf("GetBlock %s: %v", tc.block, err)
		}
		if got != tc.content {
			t.Errorf("GetBlock %s: got %q, want %q", tc.block, got, tc.content)
		}
	}

	// Verify GetAllBlocks returns consistent values
	all, err := coreSvc.GetAllBlocks(tenantID, "bob")
	if err != nil {
		t.Fatalf("GetAllBlocks: %v", err)
	}
	for _, tc := range cases {
		if all[tc.block] != tc.content {
			t.Errorf("GetAllBlocks[%s]: got %q, want %q", tc.block, all[tc.block], tc.content)
		}
	}
}

// TestCoreMemoryService_DefaultBlocks verifies that InitBlocks creates all three
// default blocks with empty content and the correct character limits.
func TestCoreMemoryService_DefaultBlocks(t *testing.T) {
	_, coreSvc, tenantID := newTestCoreMemoryDB(t)

	if err := coreSvc.InitBlocks(tenantID, "user1"); err != nil {
		t.Fatalf("InitBlocks: %v", err)
	}

	for name, wantLimit := range DefaultBlocks {
		content, charLimit, err := coreSvc.GetBlock(tenantID, name, "user1")
		if err != nil {
			t.Fatalf("GetBlock %s: %v", name, err)
		}
		if content != "" {
			t.Errorf("default block %s: expected empty content, got %q", name, content)
		}
		if charLimit != wantLimit {
			t.Errorf("default block %s: expected char_limit=%d, got %d", name, wantLimit, charLimit)
		}
	}
}

// TestCoreMemoryService_CharLimit verifies that SetBlock rejects content that
// exceeds the block's character limit.
func TestCoreMemoryService_CharLimit(t *testing.T) {
	_, coreSvc, tenantID := newTestCoreMemoryDB(t)

	if err := coreSvc.InitBlocks(tenantID, "user1"); err != nil {
		t.Fatalf("InitBlocks: %v", err)
	}

	// persona has a char_limit of 2000; a 2001-character string must be rejected
	overLimit := strings.Repeat("x", DefaultBlocks["persona"]+1)
	err := coreSvc.SetBlock(tenantID, "persona", overLimit, "user1")
	if err == nil {
		t.Errorf("SetBlock persona: expected error for over-limit content, got nil")
	}

	// Exactly at the limit must succeed
	atLimit := strings.Repeat("y", DefaultBlocks["persona"])
	if err := coreSvc.SetBlock(tenantID, "persona", atLimit, "user1"); err != nil {
		t.Errorf("SetBlock persona at limit: unexpected error: %v", err)
	}
}

// TestCoreMemoryService_DifferentUsersHaveDifferentHuman verifies that two
// distinct users each have their own independent human block.
func TestCoreMemoryService_DifferentUsersHaveDifferentHuman(t *testing.T) {
	_, coreSvc, tenantID := newTestCoreMemoryDB(t)

	if err := coreSvc.InitBlocks(tenantID, "alice"); err != nil {
		t.Fatalf("InitBlocks alice: %v", err)
	}
	if err := coreSvc.InitBlocks(tenantID, "bob"); err != nil {
		t.Fatalf("InitBlocks bob: %v", err)
	}

	aliceInfo := "Alice is a software engineer."
	bobInfo := "Bob is a product manager."

	if err := coreSvc.SetBlock(tenantID, "human", aliceInfo, "alice"); err != nil {
		t.Fatalf("SetBlock human alice: %v", err)
	}
	if err := coreSvc.SetBlock(tenantID, "human", bobInfo, "bob"); err != nil {
		t.Fatalf("SetBlock human bob: %v", err)
	}

	gotAlice, _, err := coreSvc.GetBlock(tenantID, "human", "alice")
	if err != nil {
		t.Fatalf("GetBlock human alice: %v", err)
	}
	gotBob, _, err := coreSvc.GetBlock(tenantID, "human", "bob")
	if err != nil {
		t.Fatalf("GetBlock human bob: %v", err)
	}

	if gotAlice != aliceInfo {
		t.Errorf("human alice: got %q, want %q", gotAlice, aliceInfo)
	}
	if gotBob != bobInfo {
		t.Errorf("human bob: got %q, want %q", gotBob, bobInfo)
	}
	if gotAlice == gotBob {
		t.Errorf("human blocks should differ between users but both returned %q", gotAlice)
	}
}

// TestCoreMemoryService_MigrationKeepsLongest verifies that when legacy data
// exists for multiple tenants, the migration keeps the longest content.
func TestCoreMemoryService_MigrationKeepsLongest(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	tenantSvc := NewTenantService(db)
	tenantID1, err := tenantSvc.GetOrCreateTenantID("test", "chat1")
	if err != nil {
		t.Fatalf("Failed to create tenant1: %v", err)
	}
	tenantID2, err := tenantSvc.GetOrCreateTenantID("test", "chat2")
	if err != nil {
		t.Fatalf("Failed to create tenant2: %v", err)
	}

	// Insert legacy persona blocks directly (simulating pre-migration state)
	shorter := "short persona"
	longer := "this is a much longer persona content that should survive migration"
	conn := db.Conn()
	for _, row := range []struct {
		tid     int64
		content string
	}{
		{tenantID1, shorter},
		{tenantID2, longer},
	} {
		_, err := conn.Exec(`
			INSERT OR REPLACE INTO core_memory_blocks (tenant_id, block_name, user_id, content, char_limit)
			VALUES (?, 'persona', '', ?, 2000)
		`, row.tid, row.content)
		if err != nil {
			t.Fatalf("insert legacy persona for tenant %d: %v", row.tid, err)
		}
	}

	// Insert legacy human blocks for user "carol" under two tenants
	shortHuman := "carol short"
	longHuman := "carol has a much longer human description stored in tenant2"
	for _, row := range []struct {
		tid     int64
		content string
	}{
		{tenantID1, shortHuman},
		{tenantID2, longHuman},
	} {
		_, err := conn.Exec(`
			INSERT OR REPLACE INTO core_memory_blocks (tenant_id, block_name, user_id, content, char_limit)
			VALUES (?, 'human', 'carol', ?, 2000)
		`, row.tid, row.content)
		if err != nil {
			t.Fatalf("insert legacy human for tenant %d: %v", row.tid, err)
		}
	}

	// Trigger migration by calling InitBlocks on a new CoreMemoryService instance
	coreSvc := NewCoreMemoryService(db)
	if err := coreSvc.InitBlocks(tenantID1, "carol"); err != nil {
		t.Fatalf("InitBlocks (trigger migration): %v", err)
	}

	// After migration, persona at tenantID=0 must hold the longest content
	gotPersona, _, err := coreSvc.GetBlock(0, "persona", "")
	if err != nil {
		t.Fatalf("GetBlock persona after migration: %v", err)
	}
	if gotPersona != longer {
		t.Errorf("migration persona: got %q, want longest %q", gotPersona, longer)
	}

	// After migration, human for carol at tenantID=0 must hold the longest content
	gotHuman, _, err := coreSvc.GetBlock(0, "human", "carol")
	if err != nil {
		t.Fatalf("GetBlock human after migration: %v", err)
	}
	if gotHuman != longHuman {
		t.Errorf("migration human: got %q, want longest %q", gotHuman, longHuman)
	}

	// Legacy rows at non-zero tenants must have been cleaned up
	var count int
	err = conn.QueryRow(
		"SELECT COUNT(*) FROM core_memory_blocks WHERE block_name IN ('persona','human') AND tenant_id != 0",
	).Scan(&count)
	if err != nil {
		t.Fatalf("count legacy rows: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 legacy persona/human rows after migration, got %d", count)
	}
}
