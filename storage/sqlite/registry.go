package sqlite

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// SharedEntry represents a shared skill/agent in the registry.
type SharedEntry struct {
	ID          int64  `json:"id"`
	Type        string `json:"type"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Author      string `json:"author"`
	Tags        string `json:"tags"`
	SourcePath  string `json:"source_path"`
	Sharing     string `json:"sharing"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

// SharedSkillRegistry manages the shared_registry table.
type SharedSkillRegistry struct {
	db *DB
}

// NewSharedSkillRegistry creates a new SharedSkillRegistry.
func NewSharedSkillRegistry(db *DB) *SharedSkillRegistry {
	return &SharedSkillRegistry{db: db}
}

// ListShared lists public shared entries, optionally filtered by type.
// limit <= 0 means no limit.
func (r *SharedSkillRegistry) ListShared(entryType string, limit, offset int) ([]SharedEntry, error) {
	query := "SELECT id, type, name, description, author, tags, source_path, sharing, created_at, updated_at FROM shared_registry WHERE sharing = 'public'"
	args := []any{}

	if entryType != "" {
		query += " AND type = ?"
		args = append(args, entryType)
	}

	query += " ORDER BY updated_at DESC"

	if limit > 0 {
		query += " LIMIT ? OFFSET ?"
		args = append(args, limit, offset)
	}

	rows, err := r.db.Conn().Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list shared: %w", err)
	}
	defer rows.Close()

	return scanSharedEntries(rows)
}

// SearchShared searches public shared entries by query string (matches name, description, tags).
func (r *SharedSkillRegistry) SearchShared(query string, entryType string, limit int) ([]SharedEntry, error) {
	like := "%" + strings.ToLower(query) + "%"
	sqlQuery := `SELECT id, type, name, description, author, tags, source_path, sharing, created_at, updated_at
		FROM shared_registry
		WHERE sharing = 'public' AND (LOWER(name) LIKE ? OR LOWER(description) LIKE ? OR LOWER(tags) LIKE ?)`
	args := []any{like, like, like}

	if entryType != "" {
		sqlQuery += " AND type = ?"
		args = append(args, entryType)
	}

	sqlQuery += " ORDER BY updated_at DESC"

	if limit > 0 {
		sqlQuery += " LIMIT ?"
		args = append(args, limit)
	}

	rows, err := r.db.Conn().Query(sqlQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("search shared: %w", err)
	}
	defer rows.Close()

	return scanSharedEntries(rows)
}

// Publish inserts or updates a shared entry (marks as shared).
func (r *SharedSkillRegistry) Publish(entry *SharedEntry) error {
	now := time.Now().UnixMilli()

	// Check if entry already exists (by type + name + author)
	var existingID int64
	err := r.db.Conn().QueryRow(
		"SELECT id FROM shared_registry WHERE type = ? AND name = ? AND author = ?",
		entry.Type, entry.Name, entry.Author,
	).Scan(&existingID)

	if err == sql.ErrNoRows {
		// Insert new entry
		entry.CreatedAt = now
		entry.UpdatedAt = now
		result, err := r.db.Conn().Exec(
			`INSERT INTO shared_registry (type, name, description, author, tags, source_path, sharing, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			entry.Type, entry.Name, entry.Description, entry.Author, entry.Tags,
			entry.SourcePath, entry.Sharing, entry.CreatedAt, entry.UpdatedAt,
		)
		if err != nil {
			return fmt.Errorf("publish insert: %w", err)
		}
		id, _ := result.LastInsertId()
		entry.ID = id
		return nil
	}
	if err != nil {
		return fmt.Errorf("publish check: %w", err)
	}

	// Update existing entry
	_, err = r.db.Conn().Exec(
		`UPDATE shared_registry SET description = ?, tags = ?, source_path = ?, sharing = ?, updated_at = ?
		 WHERE id = ?`,
		entry.Description, entry.Tags, entry.SourcePath, entry.Sharing, now, existingID,
	)
	if err != nil {
		return fmt.Errorf("publish update: %w", err)
	}

	entry.ID = existingID
	entry.UpdatedAt = now
	return nil
}

// Unpublish removes a shared entry (sets sharing to 'private').
func (r *SharedSkillRegistry) Unpublish(id int64, author string) error {
	result, err := r.db.Conn().Exec(
		"UPDATE shared_registry SET sharing = 'private', updated_at = ? WHERE id = ? AND author = ?",
		time.Now().UnixMilli(), id, author,
	)
	if err != nil {
		return fmt.Errorf("unpublish: %w", err)
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("entry not found or not owned by author")
	}
	return nil
}

// GetByID returns a shared entry by ID.
func (r *SharedSkillRegistry) GetByID(id int64) (*SharedEntry, error) {
	row := r.db.Conn().QueryRow(
		"SELECT id, type, name, description, author, tags, source_path, sharing, created_at, updated_at FROM shared_registry WHERE id = ?",
		id,
	)
	entry := &SharedEntry{}
	err := row.Scan(&entry.ID, &entry.Type, &entry.Name, &entry.Description,
		&entry.Author, &entry.Tags, &entry.SourcePath, &entry.Sharing,
		&entry.CreatedAt, &entry.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get shared by id: %w", err)
	}
	return entry, nil
}

// GetByTypeAndName finds a shared entry by type and name.
func (r *SharedSkillRegistry) GetByTypeAndName(entryType, name string) (*SharedEntry, error) {
	row := r.db.Conn().QueryRow(
		"SELECT id, type, name, description, author, tags, source_path, sharing, created_at, updated_at FROM shared_registry WHERE type = ? AND name = ?",
		entryType, name,
	)
	entry := &SharedEntry{}
	err := row.Scan(&entry.ID, &entry.Type, &entry.Name, &entry.Description,
		&entry.Author, &entry.Tags, &entry.SourcePath, &entry.Sharing,
		&entry.CreatedAt, &entry.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get shared by type and name: %w", err)
	}
	return entry, nil
}

// ListByAuthor lists all entries by an author (any sharing status).
func (r *SharedSkillRegistry) ListByAuthor(author string) ([]SharedEntry, error) {
	rows, err := r.db.Conn().Query(
		"SELECT id, type, name, description, author, tags, source_path, sharing, created_at, updated_at FROM shared_registry WHERE author = ? ORDER BY updated_at DESC",
		author,
	)
	if err != nil {
		return nil, fmt.Errorf("list by author: %w", err)
	}
	defer rows.Close()
	return scanSharedEntries(rows)
}

func scanSharedEntries(rows *sql.Rows) ([]SharedEntry, error) {
	var entries []SharedEntry
	for rows.Next() {
		var e SharedEntry
		if err := rows.Scan(&e.ID, &e.Type, &e.Name, &e.Description,
			&e.Author, &e.Tags, &e.SourcePath, &e.Sharing,
			&e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}
