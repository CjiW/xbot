package sqlite

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	log "xbot/logger"
)

// CronJob represents a scheduled cron job
type CronJob struct {
	ID           string     `json:"id"`
	Message      string     `json:"message"`
	Channel      string     `json:"channel"`
	ChatID       string     `json:"chat_id"`
	SenderID     string     `json:"sender_id,omitempty"`
	CronExpr     string     `json:"cron_expr,omitempty"`
	EverySeconds int        `json:"every_seconds,omitempty"`
	DelaySeconds int        `json:"delay_seconds,omitempty"`
	At           string     `json:"at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	NextRun      time.Time  `json:"next_run"`
	LastTrigger  *time.Time `json:"last_trigger,omitempty"` // 上次触发时间，用于防重复
	OneShot      bool       `json:"one_shot"`
}

// CronService provides SQLite storage for cron jobs
type CronService struct {
	db *DB
}

// NewCronService creates a new CronService
func NewCronService(db *DB) *CronService {
	return &CronService{db: db}
}

// AddJob inserts a new cron job
func (s *CronService) AddJob(job *CronJob) error {
	conn := s.db.Conn()
	var lastTriggerStr *string
	if job.LastTrigger != nil {
		s := job.LastTrigger.Format(time.RFC3339)
		lastTriggerStr = &s
	}
	_, err := conn.Exec(`
		INSERT INTO cron_jobs (id, message, channel, chat_id, sender_id, cron_expr, every_seconds, delay_seconds, at, created_at, next_run, last_trigger, one_shot)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, job.ID, job.Message, job.Channel, job.ChatID, job.SenderID, job.CronExpr, job.EverySeconds, job.DelaySeconds, job.At, job.CreatedAt.Format(time.RFC3339), job.NextRun.Format(time.RFC3339), lastTriggerStr, job.OneShot)
	if err != nil {
		return fmt.Errorf("insert cron job: %w", err)
	}
	return nil
}

// RemoveJob deletes a cron job by ID
func (s *CronService) RemoveJob(id string) error {
	conn := s.db.Conn()
	_, err := conn.Exec(`DELETE FROM cron_jobs WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete cron job: %w", err)
	}
	return nil
}

// GetJob retrieves a cron job by ID
func (s *CronService) GetJob(id string) (*CronJob, error) {
	conn := s.db.Conn()
	row := conn.QueryRow(`
		SELECT id, message, channel, chat_id, sender_id, cron_expr, every_seconds, delay_seconds, at, created_at, next_run, last_trigger, one_shot
		FROM cron_jobs WHERE id = ?
	`, id)

	job := &CronJob{}
	var createdAt, nextRun string
	var lastTriggerStr *string
	err := row.Scan(&job.ID, &job.Message, &job.Channel, &job.ChatID, &job.SenderID, &job.CronExpr,
		&job.EverySeconds, &job.DelaySeconds, &job.At, &createdAt, &nextRun, &lastTriggerStr, &job.OneShot)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan cron job: %w", err)
	}

	var parseErr error
	job.CreatedAt, parseErr = time.Parse(time.RFC3339, createdAt)
	if parseErr != nil {
		return nil, fmt.Errorf("parse created_at %q: %w", createdAt, parseErr)
	}
	job.NextRun, parseErr = time.Parse(time.RFC3339, nextRun)
	if parseErr != nil {
		return nil, fmt.Errorf("parse next_run %q: %w", nextRun, parseErr)
	}
	if lastTriggerStr != nil {
		t, err := time.Parse(time.RFC3339, *lastTriggerStr)
		if err != nil {
			return nil, fmt.Errorf("parse last_trigger %q: %w", *lastTriggerStr, err)
		}
		job.LastTrigger = &t
	}
	return job, nil
}

// ListJobsBySender lists all cron jobs for a specific sender
func (s *CronService) ListJobsBySender(senderID string) ([]*CronJob, error) {
	conn := s.db.Conn()
	rows, err := conn.Query(`
		SELECT id, message, channel, chat_id, sender_id, cron_expr, every_seconds, delay_seconds, at, created_at, next_run, last_trigger, one_shot
		FROM cron_jobs WHERE sender_id = ? ORDER BY created_at
	`, senderID)
	if err != nil {
		return nil, fmt.Errorf("query cron jobs: %w", err)
	}
	defer rows.Close()

	var jobs []*CronJob
	for rows.Next() {
		job := &CronJob{}
		var createdAt, nextRun string
		var lastTriggerStr *string
		if err := rows.Scan(&job.ID, &job.Message, &job.Channel, &job.ChatID, &job.SenderID, &job.CronExpr,
			&job.EverySeconds, &job.DelaySeconds, &job.At, &createdAt, &nextRun, &lastTriggerStr, &job.OneShot); err != nil {
			return nil, fmt.Errorf("scan cron job row: %w", err)
		}
		var parseErr error
		job.CreatedAt, parseErr = time.Parse(time.RFC3339, createdAt)
		if parseErr != nil {
			return nil, fmt.Errorf("parse created_at %q for job %s: %w", createdAt, job.ID, parseErr)
		}
		job.NextRun, parseErr = time.Parse(time.RFC3339, nextRun)
		if parseErr != nil {
			return nil, fmt.Errorf("parse next_run %q for job %s: %w", nextRun, job.ID, parseErr)
		}
		if lastTriggerStr != nil {
			t, err := time.Parse(time.RFC3339, *lastTriggerStr)
			if err != nil {
				return nil, fmt.Errorf("parse last_trigger %q for job %s: %w", *lastTriggerStr, job.ID, err)
			}
			job.LastTrigger = &t
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sender cron jobs: %w", err)
	}
	return jobs, nil
}

// ListAllJobs lists all cron jobs (for scheduler)
func (s *CronService) ListAllJobs() ([]*CronJob, error) {
	conn := s.db.Conn()
	rows, err := conn.Query(`
		SELECT id, message, channel, chat_id, sender_id, cron_expr, every_seconds, delay_seconds, at, created_at, next_run, last_trigger, one_shot
		FROM cron_jobs ORDER BY next_run
	`)
	if err != nil {
		return nil, fmt.Errorf("query all cron jobs: %w", err)
	}
	defer rows.Close()

	var jobs []*CronJob
	for rows.Next() {
		job := &CronJob{}
		var createdAt, nextRun string
		var lastTriggerStr *string
		if err := rows.Scan(&job.ID, &job.Message, &job.Channel, &job.ChatID, &job.SenderID, &job.CronExpr,
			&job.EverySeconds, &job.DelaySeconds, &job.At, &createdAt, &nextRun, &lastTriggerStr, &job.OneShot); err != nil {
			return nil, fmt.Errorf("scan cron job row: %w", err)
		}
		var parseErr error
		job.CreatedAt, parseErr = time.Parse(time.RFC3339, createdAt)
		if parseErr != nil {
			return nil, fmt.Errorf("parse created_at %q for job %s: %w", createdAt, job.ID, parseErr)
		}
		job.NextRun, parseErr = time.Parse(time.RFC3339, nextRun)
		if parseErr != nil {
			return nil, fmt.Errorf("parse next_run %q for job %s: %w", nextRun, job.ID, parseErr)
		}
		if lastTriggerStr != nil {
			t, err := time.Parse(time.RFC3339, *lastTriggerStr)
			if err != nil {
				return nil, fmt.Errorf("parse last_trigger %q for job %s: %w", *lastTriggerStr, job.ID, err)
			}
			job.LastTrigger = &t
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate all cron jobs: %w", err)
	}
	return jobs, nil
}

// UpdateNextRun updates the next_run time for a job
func (s *CronService) UpdateNextRun(id string, nextRun time.Time) error {
	conn := s.db.Conn()
	_, err := conn.Exec(`UPDATE cron_jobs SET next_run = ? WHERE id = ?`, nextRun.Format(time.RFC3339), id)
	if err != nil {
		return fmt.Errorf("update cron job next_run: %w", err)
	}
	return nil
}

// UpdateLastTrigger updates the last_trigger time for a job
func (s *CronService) UpdateLastTrigger(id string, lastTrigger time.Time) error {
	conn := s.db.Conn()
	_, err := conn.Exec(`UPDATE cron_jobs SET last_trigger = ? WHERE id = ?`, lastTrigger.Format(time.RFC3339), id)
	if err != nil {
		return fmt.Errorf("update cron job last_trigger: %w", err)
	}
	return nil
}

// MigrateFromJSON migrates cron jobs from the old JSON file format
func (s *CronService) MigrateFromJSON(dataDir string) error {
	// Check for existing jobs in database
	existing, err := s.ListAllJobs()
	if err != nil {
		return fmt.Errorf("check existing jobs: %w", err)
	}
	if len(existing) > 0 {
		log.Info("Cron jobs already in database, skipping JSON migration")
		return nil
	}

	// Try new path first, then old path
	newPath := filepath.Join(dataDir, ".xbot", "cron.json")
	oldPath := filepath.Join(dataDir, "cron.json")

	var jsonPath string
	if _, err := os.Stat(newPath); err == nil {
		jsonPath = newPath
	} else if _, err := os.Stat(oldPath); err == nil {
		jsonPath = oldPath
	} else {
		log.Info("No cron.json file found, skipping migration")
		return nil
	}

	data, err := os.ReadFile(jsonPath)
	if err != nil {
		return fmt.Errorf("read cron.json: %w", err)
	}

	// Old format: map[string]*cronJob (with runtime fields)
	var oldJobs map[string]struct {
		ID           string `json:"id"`
		Message      string `json:"message"`
		Channel      string `json:"channel"`
		ChatID       string `json:"chat_id"`
		SenderID     string `json:"sender_id,omitempty"`
		CronExpr     string `json:"cron_expr,omitempty"`
		EverySeconds int    `json:"every_seconds,omitempty"`
		DelaySeconds int    `json:"delay_seconds,omitempty"`
		At           string `json:"at,omitempty"`
		CreatedAt    string `json:"created_at"`
	}
	if err := json.Unmarshal(data, &oldJobs); err != nil {
		return fmt.Errorf("parse cron.json: %w", err)
	}

	now := time.Now()
	migrated := 0
	for _, old := range oldJobs {
		job := &CronJob{
			ID:           old.ID,
			Message:      old.Message,
			Channel:      old.Channel,
			ChatID:       old.ChatID,
			SenderID:     old.SenderID,
			CronExpr:     old.CronExpr,
			EverySeconds: old.EverySeconds,
			DelaySeconds: old.DelaySeconds,
			At:           old.At,
		}

		// Parse created_at
		if old.CreatedAt != "" {
			job.CreatedAt, _ = time.Parse(time.RFC3339, old.CreatedAt)
		}
		if job.CreatedAt.IsZero() {
			job.CreatedAt = now
		}

		// Calculate next_run and one_shot
		job.OneShot = job.At != "" || job.DelaySeconds > 0
		if job.At != "" {
			t, err := time.ParseInLocation("2006-01-02T15:04:05", job.At, time.Local)
			if err != nil {
				t, _ = time.Parse(time.RFC3339, job.At)
			}
			job.NextRun = t
		} else if job.DelaySeconds > 0 {
			job.NextRun = job.CreatedAt.Add(time.Duration(job.DelaySeconds) * time.Second)
		} else if job.EverySeconds > 0 {
			job.NextRun = now.Add(time.Duration(job.EverySeconds) * time.Second)
		} else if job.CronExpr != "" {
			// Will be calculated by scheduler
			job.NextRun = now
		}

		// Skip expired one-shot jobs
		if job.OneShot && job.NextRun.Before(now) {
			log.WithField("job_id", job.ID).Info("Skipping expired one-shot job during migration")
			continue
		}

		if err := s.AddJob(job); err != nil {
			log.WithError(err).WithField("job_id", job.ID).Warn("Failed to migrate cron job")
			continue
		}
		migrated++
	}

	// Backup old file
	if migrated > 0 {
		backupPath := jsonPath + ".migrated-" + now.Format("20060102-150405")
		if err := os.Rename(jsonPath, backupPath); err != nil {
			log.WithError(err).Warn("Failed to backup cron.json after migration")
		} else {
			log.WithField("backup", backupPath).Info("Backed up cron.json after migration")
		}
	}

	log.WithField("count", migrated).Info("Migrated cron jobs from JSON to SQLite")
	return nil
}
