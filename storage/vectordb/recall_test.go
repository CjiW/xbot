package vectordb

import (
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// setupTestDB creates a temporary SQLite database with the event_history schema
// for testing recall functions (no FTS needed — recall is time-range only).
func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	schema := `
CREATE TABLE event_history (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    tenant_id INTEGER NOT NULL,
    entry TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_event_history_tenant_created ON event_history(tenant_id, created_at);
`
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	return db
}

func insertEntry(t *testing.T, db *sql.DB, tenantID int64, entry, createdAt string) {
	t.Helper()
	_, err := db.Exec(
		"INSERT INTO event_history (tenant_id, entry, created_at) VALUES (?, ?, ?)",
		tenantID, entry, createdAt,
	)
	if err != nil {
		t.Fatalf("insert entry: %v", err)
	}
}

// --- RecallTimeRangeFunc tests ---

func TestNewSQLiteRecallTimeRangeFunc_TimeRange(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	fn := NewSQLiteRecallTimeRangeFunc(db)

	insertEntry(t, db, 1, "Discussed Go generics feature", "2026-03-01 10:00:00")
	insertEntry(t, db, 1, "Go interface patterns explained", "2026-03-03 14:00:00")
	insertEntry(t, db, 1, "Rust ownership model deep dive", "2026-03-05 09:00:00")
	insertEntry(t, db, 1, "Go error handling best practices", "2026-03-07 16:00:00")

	// Time range: 2026-03-02 to 2026-03-06
	start, _ := time.Parse("2006-01-02", "2026-03-02")
	end, _ := time.Parse("2006-01-02", "2026-03-06")
	end = end.Add(24*time.Hour - time.Second)

	results, err := fn(1, start, end, 10)
	if err != nil {
		t.Fatalf("recall time range: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results (03-03 and 03-05), got %d", len(results))
	}
}

func TestNewSQLiteRecallTimeRangeFunc_TimeRangeOnly(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	fn := NewSQLiteRecallTimeRangeFunc(db)

	insertEntry(t, db, 1, "Morning standup discussion", "2026-03-01 09:00:00")
	insertEntry(t, db, 1, "Code review feedback", "2026-03-01 14:00:00")
	insertEntry(t, db, 1, "Deployment planning", "2026-03-03 10:00:00")

	// Time range: 2026-03-01 only
	start, _ := time.Parse("2006-01-02", "2026-03-01")
	end, _ := time.Parse("2006-01-02", "2026-03-01")
	end = end.Add(24*time.Hour - time.Second)

	results, err := fn(1, start, end, 10)
	if err != nil {
		t.Fatalf("recall time range only: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results for 2026-03-01, got %d", len(results))
	}
}

func TestNewSQLiteRecallTimeRangeFunc_RecentNoParams(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	fn := NewSQLiteRecallTimeRangeFunc(db)

	insertEntry(t, db, 1, "Entry A", "2026-03-01 10:00:00")
	insertEntry(t, db, 1, "Entry B", "2026-03-02 10:00:00")
	insertEntry(t, db, 1, "Entry C", "2026-03-03 10:00:00")

	// Neither time range — should return recent entries (DESC order)
	results, err := fn(1, time.Time{}, time.Time{}, 2)
	if err != nil {
		t.Fatalf("recall recent: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 recent results, got %d", len(results))
	}
	// Most recent first
	if len(results) > 0 && results[0].Entry != "Entry C" {
		t.Errorf("expected most recent entry first, got: %s", results[0].Entry)
	}
}

func TestNewSQLiteRecallTimeRangeFunc_TenantIsolation(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	fn := NewSQLiteRecallTimeRangeFunc(db)

	insertEntry(t, db, 1, "Tenant 1 secret data", "2026-03-01 10:00:00")
	insertEntry(t, db, 2, "Tenant 2 other data", "2026-03-01 10:00:00")

	results, err := fn(1, time.Time{}, time.Time{}, 10)
	if err != nil {
		t.Fatalf("tenant isolation: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result for tenant 1, got %d", len(results))
	}
	if len(results) > 0 && results[0].Entry != "Tenant 1 secret data" {
		t.Errorf("got wrong tenant entry: %s", results[0].Entry)
	}
}

func TestNewSQLiteRecallTimeRangeFunc_DefaultLimit(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	fn := NewSQLiteRecallTimeRangeFunc(db)

	for i := 0; i < 30; i++ {
		insertEntry(t, db, 1, "Bulk entry for limit test", "2026-03-01 10:00:00")
	}

	// Zero limit should default to 20
	results, err := fn(1, time.Time{}, time.Time{}, 0)
	if err != nil {
		t.Fatalf("default limit: %v", err)
	}
	if len(results) != 20 {
		t.Errorf("expected 20 results with default limit, got %d", len(results))
	}
}

func TestRecallEntry_CreatedAtParsing(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	fn := NewSQLiteRecallTimeRangeFunc(db)

	insertEntry(t, db, 1, "Entry with known timestamp", "2026-03-05 14:30:00")

	results, err := fn(1, time.Time{}, time.Time{}, 10)
	if err != nil {
		t.Fatalf("timestamp parse: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	// modernc.org/sqlite returns timestamps as RFC3339 (e.g. "2026-03-05T14:30:00Z")
	expected := time.Date(2026, 3, 5, 14, 30, 0, 0, time.UTC)
	if !results[0].CreatedAt.Equal(expected) {
		t.Errorf("expected CreatedAt %v, got %v", expected, results[0].CreatedAt)
	}
}
