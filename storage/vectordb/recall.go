package vectordb

import (
	"database/sql"
	"fmt"
	"time"
)

// RecallTimeRangeFunc retrieves conversation history entries within a time range.
// No text search — semantic search should use ArchivalService.Search (vector) instead.
// Returns entries ordered by created_at DESC.
type RecallTimeRangeFunc func(tenantID int64, start, end time.Time, limit int) ([]RecallEntry, error)

// RecallEntry represents a single recall search result with timestamp.
type RecallEntry struct {
	Entry     string
	CreatedAt time.Time
}

// NewSQLiteRecallTimeRangeFunc creates a RecallTimeRangeFunc backed by SQLite.
// Retrieves event_history rows filtered by tenant and time range.
// Uses the idx_event_history_tenant_created index for efficient filtering.
func NewSQLiteRecallTimeRangeFunc(db *sql.DB) RecallTimeRangeFunc {
	return func(tenantID int64, start, end time.Time, limit int) ([]RecallEntry, error) {
		if limit <= 0 {
			limit = 20
		}

		hasTimeRange := !start.IsZero() || !end.IsZero()

		// Set default time range bounds
		if start.IsZero() {
			start = time.Date(2000, 1, 1, 0, 0, 0, 0, time.Local)
		}
		if end.IsZero() {
			end = time.Now().Add(24 * time.Hour)
		}

		startStr := start.Format("2006-01-02 15:04:05")
		endStr := end.Format("2006-01-02 15:04:05")

		if hasTimeRange {
			rows, err := db.Query(`
				SELECT entry, created_at FROM event_history
				WHERE tenant_id = ? AND created_at >= ? AND created_at <= ?
				ORDER BY created_at DESC
				LIMIT ?
			`, tenantID, startStr, endStr, limit)
			if err != nil {
				return nil, fmt.Errorf("recall time range: %w", err)
			}
			defer rows.Close()
			return scanRecallEntries(rows)
		}

		// No time range — return recent entries
		rows, err := db.Query(`
			SELECT entry, created_at FROM event_history
			WHERE tenant_id = ?
			ORDER BY created_at DESC
			LIMIT ?
		`, tenantID, limit)
		if err != nil {
			return nil, fmt.Errorf("recall recent: %w", err)
		}
		defer rows.Close()
		return scanRecallEntries(rows)
	}
}

// parseTimestamp parses a timestamp string, handling both RFC3339 (from modernc.org/sqlite)
// and plain "2006-01-02 15:04:05" formats.
func parseTimestamp(s string) time.Time {
	if t, err := time.ParseInLocation("2006-01-02 15:04:05", s, time.Local); err == nil {
		return t
	}
	if t, err := time.ParseInLocation("2006-01-02T15:04:05Z", s, time.Local); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.Local()
	}

	return time.Time{}
}

func scanRecallEntries(rows *sql.Rows) ([]RecallEntry, error) {
	var entries []RecallEntry
	for rows.Next() {
		var e RecallEntry
		var createdAt string
		if err := rows.Scan(&e.Entry, &createdAt); err != nil {
			return nil, fmt.Errorf("scan recall entry: %w", err)
		}
		e.CreatedAt = parseTimestamp(createdAt)
		entries = append(entries, e)
	}
	return entries, rows.Err()
}
