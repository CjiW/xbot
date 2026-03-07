package vectordb

import (
	"database/sql"
	"fmt"
	"time"
)

// NewSQLiteRecallFunc creates a RecallFunc that performs FTS5 search over
// the event_history table in SQLite. Falls back to LIKE if FTS fails.
func NewSQLiteRecallFunc(db *sql.DB) RecallFunc {
	return func(tenantID int64, query string, limit int) ([]string, error) {
		if limit <= 0 {
			limit = 10
		}

		// Try FTS5 first
		rows, err := db.Query(`
			SELECT eh.entry FROM event_history eh
			JOIN event_history_fts fts ON eh.id = fts.rowid
            WHERE eh.tenant_id = ? AND fts MATCH ?  
            ORDER BY bm25(fts)
			LIMIT ?
		`, tenantID, query, limit)
		if err != nil {
			// Fallback to LIKE search
			return recallFallback(db, tenantID, query, limit)
		}
		defer rows.Close()

		var entries []string
		for rows.Next() {
			var entry string
			if err := rows.Scan(&entry); err != nil {
				return nil, fmt.Errorf("scan fts result: %w", err)
			}
			entries = append(entries, entry)
		}
		return entries, rows.Err()
	}
}

func recallFallback(db *sql.DB, tenantID int64, query string, limit int) ([]string, error) {
	rows, err := db.Query(`
		SELECT entry FROM event_history
		WHERE tenant_id = ? AND entry LIKE ?
		ORDER BY id DESC
		LIMIT ?
	`, tenantID, "%"+query+"%", limit)
	if err != nil {
		return nil, fmt.Errorf("recall fallback: %w", err)
	}
	defer rows.Close()

	var entries []string
	for rows.Next() {
		var entry string
		if err := rows.Scan(&entry); err != nil {
			return nil, fmt.Errorf("scan recall fallback: %w", err)
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

// RecallTimeRangeFunc searches event_history within a time range, optionally
// combined with a text/FTS query. If query is empty, returns all entries in range.
type RecallTimeRangeFunc func(tenantID int64, query string, start, end time.Time, limit int) ([]RecallEntry, error)

// RecallEntry represents a single recall search result with timestamp.
type RecallEntry struct {
	Entry     string
	CreatedAt time.Time
}

// NewSQLiteRecallTimeRangeFunc creates a RecallTimeRangeFunc backed by SQLite.
// Uses FTS5 when a query is provided, with LIKE fallback.
// Uses the idx_event_history_tenant_created index for efficient time-range filtering.
func NewSQLiteRecallTimeRangeFunc(db *sql.DB) RecallTimeRangeFunc {
	return func(tenantID int64, query string, start, end time.Time, limit int) ([]RecallEntry, error) {
		if limit <= 0 {
			limit = 20
		}

		hasQuery := query != ""
		hasTimeRange := !start.IsZero() || !end.IsZero()

		// Set default time range bounds
		if start.IsZero() {
			start = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
		}
		if end.IsZero() {
			end = time.Now().UTC().Add(24 * time.Hour)
		}

		start = start.UTC()
		end = end.UTC()

		startStr := start.Format("2006-01-02 15:04:05")
		endStr := end.Format("2006-01-02 15:04:05")

		if hasQuery {
			// Try FTS5 + time range
			rows, err := db.Query(`
				SELECT eh.entry, eh.created_at FROM event_history eh
				JOIN event_history_fts fts ON eh.id = fts.rowid
				WHERE eh.tenant_id = ?
				  AND eh.created_at >= ? AND eh.created_at <= ?
				  AND event_history_fts MATCH ?
				ORDER BY eh.created_at DESC
				LIMIT ?
			`, tenantID, startStr, endStr, query, limit)
			if err != nil {
				// FTS failed, fallback to LIKE + time range
				return recallTimeRangeFallback(db, tenantID, query, startStr, endStr, limit)
			}
			defer rows.Close()
			return scanRecallEntries(rows)
		}

		if hasTimeRange {
			// Time range only, no text search
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

		// Neither query nor time range — return recent entries
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

func recallTimeRangeFallback(db *sql.DB, tenantID int64, query, startStr, endStr string, limit int) ([]RecallEntry, error) {
	rows, err := db.Query(`
		SELECT entry, created_at FROM event_history
		WHERE tenant_id = ? AND created_at >= ? AND created_at <= ? AND entry LIKE ?
		ORDER BY created_at DESC
		LIMIT ?
	`, tenantID, startStr, endStr, "%"+query+"%", limit)
	if err != nil {
		return nil, fmt.Errorf("recall time range fallback: %w", err)
	}
	defer rows.Close()
	return scanRecallEntries(rows)
}

// parseTimestamp parses a timestamp string, handling both RFC3339 (from modernc.org/sqlite)
// and plain "2006-01-02 15:04:05" formats.
func parseTimestamp(s string) time.Time {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	if t, err := time.Parse("2006-01-02T15:04:05Z", s); err == nil {
		return t
	}
	if t, err := time.Parse("2006-01-02 15:04:05", s); err == nil {
		return t
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
