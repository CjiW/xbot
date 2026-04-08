package sqlite

import (
	"path/filepath"
	"sync"
	"testing"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open test DB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestDailyTokenUsage_ConcurrentUpsert(t *testing.T) {
	db := openTestDB(t)
	svc := NewUserTokenUsageService(db)
	conn := db.Conn()

	// Create tables
	if err := svc.createTable(conn); err != nil {
		t.Fatalf("createTable: %v", err)
	}
	if err := svc.createDailyTable(conn); err != nil {
		t.Fatalf("createDailyTable: %v", err)
	}
	if err := svc.addCachedTokensColumn(conn); err != nil {
		t.Fatalf("addCachedTokensColumn: %v", err)
	}

	// Concurrent upserts from 10 goroutines, 50 each
	const goroutines = 10
	const perGoroutine = 50
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				err := svc.RecordUsage(conn, "user-concurrent", "gpt-4", 100, 50, 30, 1, 1)
				if err != nil {
					t.Errorf("RecordUsage error: %v", err)
				}
			}
		}()
	}
	wg.Wait()

	// Verify cumulative totals
	usage, err := svc.GetUsage("user-concurrent")
	if err != nil {
		t.Fatalf("GetUsage: %v", err)
	}
	expectedTotal := int64(goroutines * perGoroutine)
	if usage.InputTokens != expectedTotal*100 {
		t.Errorf("expected input=%d, got %d", expectedTotal*100, usage.InputTokens)
	}
	if usage.OutputTokens != expectedTotal*50 {
		t.Errorf("expected output=%d, got %d", expectedTotal*50, usage.OutputTokens)
	}
	if usage.CachedTokens != expectedTotal*30 {
		t.Errorf("expected cached=%d, got %d", expectedTotal*30, usage.CachedTokens)
	}
	if usage.ConversationCount != expectedTotal {
		t.Errorf("expected conversations=%d, got %d", expectedTotal, usage.ConversationCount)
	}

	// Verify daily totals
	daily, err := svc.GetDailyUsage("user-concurrent", 1)
	if err != nil {
		t.Fatalf("GetDailyUsage: %v", err)
	}
	if len(daily) != 1 {
		t.Fatalf("expected 1 daily record, got %d", len(daily))
	}
	if daily[0].InputTokens != expectedTotal*100 {
		t.Errorf("daily input=%d, want %d", daily[0].InputTokens, expectedTotal*100)
	}
	if daily[0].CachedTokens != expectedTotal*30 {
		t.Errorf("daily cached=%d, want %d", daily[0].CachedTokens, expectedTotal*30)
	}

	// Verify cache rate
	cacheRate := float64(usage.CachedTokens) / float64(usage.InputTokens) * 100
	if cacheRate < 29.9 || cacheRate > 30.1 {
		t.Errorf("expected ~30%% cache rate, got %.1f%%", cacheRate)
	}
}

func TestDailyTokenUsage_MultiModel(t *testing.T) {
	db := openTestDB(t)
	svc := NewUserTokenUsageService(db)
	conn := db.Conn()

	if err := svc.createTable(conn); err != nil {
		t.Fatalf("createTable: %v", err)
	}
	if err := svc.createDailyTable(conn); err != nil {
		t.Fatalf("createDailyTable: %v", err)
	}
	if err := svc.addCachedTokensColumn(conn); err != nil {
		t.Fatalf("addCachedTokensColumn: %v", err)
	}

	// Record usage for two models
	svc.RecordUsage(conn, "user-multi", "gpt-4", 1000, 500, 200, 1, 2)
	svc.RecordUsage(conn, "user-multi", "claude-4", 2000, 800, 600, 1, 3)
	svc.RecordUsage(conn, "user-multi", "gpt-4", 500, 200, 100, 1, 1)

	// Daily should have 2 records (one per model)
	daily, err := svc.GetDailyUsage("user-multi", 1)
	if err != nil {
		t.Fatalf("GetDailyUsage: %v", err)
	}
	if len(daily) != 2 {
		t.Fatalf("expected 2 daily records (2 models), got %d", len(daily))
	}

	// Summary should aggregate across models
	summary, err := svc.GetDailyUsageSummary("user-multi", 1)
	if err != nil {
		t.Fatalf("GetDailyUsageSummary: %v", err)
	}
	if len(summary) != 1 {
		t.Fatalf("expected 1 summary record, got %d", len(summary))
	}
	if summary[0].InputTokens != 3500 {
		t.Errorf("summary input=%d, want 3500", summary[0].InputTokens)
	}
	if summary[0].CachedTokens != 900 {
		t.Errorf("summary cached=%d, want 900", summary[0].CachedTokens)
	}

	// Cumulative should be sum of all
	usage, err := svc.GetUsage("user-multi")
	if err != nil {
		t.Fatalf("GetUsage: %v", err)
	}
	if usage.TotalTokens != 5000 {
		t.Errorf("cumulative total=%d, want 5000", usage.TotalTokens)
	}
}
