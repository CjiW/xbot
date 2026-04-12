package storage

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"xbot/config"
	"xbot/llm"
	log "xbot/logger"
	"xbot/storage/sqlite"
)

const (
	// CLITenantChannel is the channel name for CLI mode legacy data
	CLITenantChannel = "cli"
	// CLITenantChatID is the chat ID for CLI mode legacy data
	CLITenantChatID = "direct"
)

// MigrateFromFileStorage migrates data from the old file-based storage to SQLite
//
// It reads from:
// - workDir/session.jsonl - session messages (will be migrated to "cli:direct" tenant)
// - workDir/MEMORY.md - long-term memory
// - workDir/HISTORY.md - event history
//
// And writes to the SQLite database at dbPath.
func MigrateFromFileStorage(workDir, dbPath string) error {
	log.Info("Starting migration from file-based storage to SQLite")

	// Open SQLite database
	db, err := sqlite.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	tenantSvc := sqlite.NewTenantService(db)
	sessionSvc := sqlite.NewSessionService(db)
	memorySvc := sqlite.NewMemoryService(db)

	// Get or create the CLI tenant
	tenantID, err := tenantSvc.GetOrCreateTenantID(CLITenantChannel, CLITenantChatID)
	if err != nil {
		return fmt.Errorf("get/create CLI tenant: %w", err)
	}

	log.WithField("tenant_id", tenantID).Info("CLI tenant ready for migration")

	// Migrate session messages
	// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
	sessionPath := filepath.Join(workDir, ".xbot", "session.jsonl")
	if err := migrateSessionMessages(sessionPath, tenantID, sessionSvc); err != nil {
		return fmt.Errorf("migrate session messages: %w", err)
	}

	// Migrate memory files
	memoryDir := workDir
	memoryPath := filepath.Join(memoryDir, "MEMORY.md")
	historyPath := filepath.Join(memoryDir, "HISTORY.md")

	if err := migrateMemoryFiles(memoryPath, historyPath, tenantID, memorySvc); err != nil {
		return fmt.Errorf("migrate memory files: %w", err)
	}

	log.Info("Migration completed successfully")

	// Rename old files to mark migration as complete
	backupTime := time.Now().Format("20060102-150405")
	renameWithBackup := func(path string) error {
		if _, err := os.Stat(path); err == nil {
			backupPath := path + ".migrated-" + backupTime
			if err := os.Rename(path, backupPath); err != nil {
				return fmt.Errorf("rename %s: %w", path, err)
			}
			log.WithField("backup", backupPath).Info("Backed up original file")
		}
		return nil
	}

	_ = renameWithBackup(sessionPath)
	_ = renameWithBackup(memoryPath)
	_ = renameWithBackup(historyPath)

	return nil
}

// migrateSessionMessages migrates session messages from JSONL to SQLite
func migrateSessionMessages(sessionPath string, tenantID int64, sessionSvc *sqlite.SessionService) error {
	f, err := os.Open(sessionPath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Info("No session.jsonl file found, skipping session migration")
			return nil
		}
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	count := 0
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var msg llm.ChatMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			log.WithError(err).Warn("Skipping corrupt session line")
			continue
		}
		if err := sessionSvc.AddMessage(tenantID, msg); err != nil {
			log.WithError(err).WithField("line", count).Warn("Failed to migrate session message")
			continue
		}
		count++
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan session file: %w", err)
	}

	log.WithField("messages", count).Info("Session messages migrated")
	return nil
}

// migrateMemoryFiles migrates MEMORY.md and HISTORY.md to SQLite
func migrateMemoryFiles(memoryPath, historyPath string, tenantID int64, memorySvc *sqlite.MemoryService) error {
	// Migrate long-term memory
	if _, err := os.Stat(memoryPath); err == nil {
		content, err := os.ReadFile(memoryPath)
		if err != nil {
			return fmt.Errorf("read MEMORY.md: %w", err)
		}
		if len(content) > 0 {
			if err := memorySvc.WriteLongTerm(context.Background(), tenantID, string(content)); err != nil {
				return fmt.Errorf("write long-term memory: %w", err)
			}
			log.Info("Long-term memory migrated")
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("check MEMORY.md: %w", err)
	} else {
		log.Info("No MEMORY.md file found, skipping memory migration")
	}

	// Migrate event history
	if _, err := os.Stat(historyPath); err == nil {
		f, err := os.Open(historyPath)
		if err != nil {
			return fmt.Errorf("open HISTORY.md: %w", err)
		}
		defer f.Close()

		scanner := bufio.NewScanner(f)
		count := 0
		for scanner.Scan() {
			entry := scanner.Text()
			if entry == "" {
				continue
			}
			if err := memorySvc.AppendHistory(context.Background(), tenantID, entry); err != nil {
				log.WithError(err).Warn("Failed to migrate history entry")
				continue
			}
			count++
		}

		if err := scanner.Err(); err != nil {
			return fmt.Errorf("scan HISTORY.md: %w", err)
		}

		log.WithField("entries", count).Info("Event history migrated")
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("check HISTORY.md: %w", err)
	} else {
		log.Info("No HISTORY.md file found, skipping history migration")
	}

	return nil
}

// ShouldMigrate checks if migration is needed
func ShouldMigrate(workDir, dbPath string) bool {
	// Check if database exists
	if _, err := os.Stat(dbPath); err == nil {
		// Database exists, no migration needed
		return false
	}

	// Check if any legacy files exist
	// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
	sessionPath := filepath.Join(workDir, ".xbot", "session.jsonl")
	memoryPath := filepath.Join(workDir, "MEMORY.md")
	historyPath := filepath.Join(workDir, "HISTORY.md")

	hasSession := false
	hasMemory := false
	hasHistory := false

	if _, err := os.Stat(sessionPath); err == nil {
		hasSession = true
	}
	if _, err := os.Stat(memoryPath); err == nil {
		hasMemory = true
	}
	if _, err := os.Stat(historyPath); err == nil {
		hasHistory = true
	}

	return hasSession || hasMemory || hasHistory
}

// MigrateIfNeeded runs migration if legacy data is detected
func MigrateIfNeeded(ctx context.Context, workDir, dbPath string) error {
	if !ShouldMigrate(workDir, dbPath) {
		return nil
	}

	log.Info("Legacy storage detected, starting migration...")
	log.WithField("db_path", dbPath).Info("Migrating to SQLite database")

	return MigrateFromFileStorage(workDir, dbPath)
}

const (
	migrateMemoryFileName  = "MEMORY.md"
	migrateHistoryFileName = "HISTORY.md"
)

// MigrateMemoryToFiles migrates flat memory data from SQLite tables to MD files.
// Called once at startup if long_term_memory or event_history tables have data
// but the target MD files don't exist yet.
func MigrateMemoryToFiles(dbPath string) {
	db, err := sqlite.Open(dbPath)
	if err != nil {
		log.WithError(err).Warn("Failed to open database for memory file migration")
		return
	}
	defer db.Close()

	memorySvc := sqlite.NewMemoryService(db)
	ctx := context.Background()

	// Get all tenant IDs that have memory data
	tenantIDs := getDistinctTenantIDs(ctx, db)
	if len(tenantIDs) == 0 {
		return
	}

	home := config.XbotHome()

	for _, tenantID := range tenantIDs {
		// Check if MEMORY.md already exists (directory name = tenantID)
		tenantDir := filepath.Join(home, "memory", fmt.Sprintf("%d", tenantID))
		memoryFile := filepath.Join(tenantDir, migrateMemoryFileName)
		historyFile := filepath.Join(tenantDir, migrateHistoryFileName)

		if _, err := os.Stat(memoryFile); err == nil {
			continue // Already migrated
		}

		os.MkdirAll(tenantDir, 0o755)

		// Migrate long_term_memory → MEMORY.md
		content, err := memorySvc.ReadLongTerm(ctx, tenantID)
		if err == nil && content != "" {
			if err := os.WriteFile(memoryFile, []byte(content), 0o644); err != nil {
				log.WithError(err).WithField("tenant_id", tenantID).Error("Failed to migrate long_term_memory to file")
			} else {
				log.WithField("tenant_id", tenantID).Info("Migrated long_term_memory to MEMORY.md")
			}
		}

		// Migrate event_history → HISTORY.md
		entries, err := memorySvc.GetHistoryEntries(ctx, tenantID, 1000)
		if err == nil && len(entries) > 0 {
			var sb strings.Builder
			for _, entry := range entries {
				sb.WriteString(entry)
				sb.WriteString("\n")
			}
			if err := os.WriteFile(historyFile, []byte(sb.String()), 0o644); err != nil {
				log.WithError(err).WithField("tenant_id", tenantID).Error("Failed to migrate event_history to file")
			} else {
				log.WithField("tenant_id", tenantID).Infof("Migrated %d event_history entries to HISTORY.md", len(entries))
			}
		}
	}
}

// getDistinctTenantIDs returns all distinct tenant IDs from long_term_memory and event_history.
func getDistinctTenantIDs(ctx context.Context, db *sqlite.DB) []int64 {
	conn := db.Conn()
	rows, err := conn.QueryContext(ctx, "SELECT DISTINCT tenant_id FROM long_term_memory UNION SELECT DISTINCT tenant_id FROM event_history")
	if err != nil {
		return nil
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}
