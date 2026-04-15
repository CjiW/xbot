package cron

import (
	"fmt"
	"strings"
	"sync"
	"time"

	log "xbot/logger"
	"xbot/storage/sqlite"
)

// InjectFunc is the function type for injecting messages into the agent
type InjectFunc func(channel, chatID, senderID, content string)

// Scheduler manages the cron job scheduling loop
type Scheduler struct {
	cronSvc    *sqlite.CronService
	injectFunc InjectFunc
	stopCh     chan struct{}
	once       sync.Once // 共用 once 保证 Start 和 StartDelayed 互斥：只有第一个调用者会启动调度器
	running    bool
	mu         sync.Mutex
}

// NewScheduler creates a new Scheduler
func NewScheduler(cronSvc *sqlite.CronService) *Scheduler {
	return &Scheduler{
		cronSvc: cronSvc,
		stopCh:  make(chan struct{}),
	}
}

// SetInjectFunc sets the message injection function
func (s *Scheduler) SetInjectFunc(fn InjectFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.injectFunc = fn
}

// Start starts the scheduler loop
func (s *Scheduler) Start() {
	s.once.Do(func() {
		s.mu.Lock()
		s.running = true
		s.mu.Unlock()
		go s.runLoop()
		log.Info("Cron scheduler started")
	})
}

// StartDelayed starts the scheduler after a delay, first cleaning up expired jobs
// This is useful when the scheduler needs to wait for other initialization (like tool indexing)
func (s *Scheduler) StartDelayed(delay time.Duration) {
	s.once.Do(func() {
		s.mu.Lock()
		s.running = true
		s.mu.Unlock()

		go func() {
			// Wait for the delay
			log.WithField("delay", delay).Info("Cron scheduler waiting before start")
			time.Sleep(delay)

			// Clean up expired jobs before first tick
			s.cleanupExpiredJobs()

			go s.runLoop()
			log.Info("Cron scheduler started after delay")
		}()
	})
}

// cleanupExpiredJobs removes or updates expired jobs on startup
func (s *Scheduler) cleanupExpiredJobs() {
	now := time.Now()
	jobs, err := s.cronSvc.ListAllJobs()
	if err != nil {
		log.WithError(err).Error("Failed to list cron jobs during cleanup")
		return
	}

	cleaned := 0
	for _, job := range jobs {
		if job.OneShot && job.NextRun.Before(now) {
			// Remove expired one-shot jobs
			if err := s.cronSvc.RemoveJob(job.ID); err != nil {
				log.WithError(err).WithField("job_id", job.ID).Warn("Failed to remove expired one-shot job")
			} else {
				log.WithFields(log.Fields{
					"job_id":   job.ID,
					"next_run": job.NextRun,
				}).Info("Removed expired one-shot cron job on startup")
				cleaned++
			}
		} else if !job.OneShot && job.NextRun.Before(now) {
			// For recurring jobs with expired next_run, recalculate next run
			var nextRun time.Time
			var err error

			if job.EverySeconds > 0 {
				// Simple interval: calculate next run from now
				nextRun = now.Add(time.Duration(job.EverySeconds) * time.Second)
			} else if job.CronExpr != "" {
				// Cron expression: calculate next run from now
				nextRun, err = nextCronTime(job.CronExpr, now)
				if err != nil {
					log.WithError(err).WithField("job_id", job.ID).Warn("Failed to calculate next cron time, removing job")
					s.cronSvc.RemoveJob(job.ID)
					cleaned++
					continue
				}
			} else {
				continue
			}

			if err := s.cronSvc.UpdateNextRun(job.ID, nextRun); err != nil {
				log.WithError(err).WithField("job_id", job.ID).Warn("Failed to update expired recurring job")
			} else {
				log.WithFields(log.Fields{
					"job_id":   job.ID,
					"old_next": job.NextRun,
					"new_next": nextRun,
				}).Info("Updated expired recurring cron job on startup")
				cleaned++
			}
		}
	}

	if cleaned > 0 {
		log.WithField("count", cleaned).Info("Cleaned up expired cron jobs on startup")
	}
}

// Stop stops the scheduler
func (s *Scheduler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		select {
		case <-s.stopCh:
			// already closed
		default:
			close(s.stopCh)
		}
		s.running = false
	}
}

// runLoop is the main scheduling loop
func (s *Scheduler) runLoop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			log.Info("Cron scheduler stopped")
			return
		case now := <-ticker.C:
			s.checkAndFire(now)
		}
	}
}

// checkAndFire checks for due jobs and fires them
func (s *Scheduler) checkAndFire(now time.Time) {
	s.mu.Lock()
	injectFunc := s.injectFunc
	s.mu.Unlock()

	if injectFunc == nil {
		return
	}

	jobs, err := s.cronSvc.ListAllJobs()
	if err != nil {
		log.WithError(err).Error("Failed to list cron jobs")
		return
	}

	for _, job := range jobs {
		// Skip jobs not yet due
		if now.Before(job.NextRun) {
			continue
		}

		// Skip one-shot jobs that were already fired (LastTrigger is set).
		// One-shot jobs with NextRun in the past and no LastTrigger were never
		// executed (e.g. missed during downtime) — they should fall through to
		// the firing logic below, not be silently removed.
		if job.OneShot && job.LastTrigger != nil {
			continue
		}

		// Prevent double-firing: check if this job was already triggered very recently
		// This handles edge cases where the job runs again within the same second
		if job.LastTrigger != nil && now.Sub(*job.LastTrigger) < time.Second {
			log.WithFields(log.Fields{
				"job_id":       job.ID,
				"last_trigger": job.LastTrigger,
			}).Warn("Cron job triggered too recently, skipping")
			continue
		}

		// Fire the job
		log.WithFields(log.Fields{
			"job_id":  job.ID,
			"channel": job.Channel,
			"chat_id": job.ChatID,
		}).Info("Cron job fired")

		injectFunc(job.Channel, job.ChatID, job.SenderID, job.Message)

		// Record trigger time for deduplication
		if err := s.cronSvc.UpdateLastTrigger(job.ID, now); err != nil {
			log.WithError(err).WithField("job_id", job.ID).Warn("Failed to update last trigger time")
		}

		// Handle job after firing
		if job.OneShot {
			// Remove one-shot jobs after firing
			if err := s.cronSvc.RemoveJob(job.ID); err != nil {
				log.WithError(err).WithField("job_id", job.ID).Error("Failed to remove one-shot job")
			}
		} else if job.EverySeconds > 0 {
			// Update next run for interval jobs
			nextRun := now.Add(time.Duration(job.EverySeconds) * time.Second)
			if err := s.cronSvc.UpdateNextRun(job.ID, nextRun); err != nil {
				log.WithError(err).WithField("job_id", job.ID).Error("Failed to update interval job")
			}
		} else if job.CronExpr != "" {
			// Calculate next run for cron expression jobs
			next, err := nextCronTime(job.CronExpr, now)
			if err != nil {
				log.WithError(err).WithField("job_id", job.ID).Error("Failed to calculate next cron time, removing job")
				s.cronSvc.RemoveJob(job.ID)
			} else {
				if err := s.cronSvc.UpdateNextRun(job.ID, next); err != nil {
					log.WithError(err).WithField("job_id", job.ID).Error("Failed to update cron job")
				}
			}
		}
	}
}

// CalculateNextRun calculates the next run time for a job
func CalculateNextRun(job *sqlite.CronJob, now time.Time) (time.Time, error) {
	if job.At != "" {
		t, err := time.ParseInLocation("2006-01-02T15:04:05", job.At, time.Local)
		if err != nil {
			t, err = time.Parse(time.RFC3339, job.At)
			if err != nil {
				return time.Time{}, fmt.Errorf("invalid datetime %q: use ISO format like 2026-02-12T10:30:00", job.At)
			}
		}
		return t, nil
	}
	if job.DelaySeconds > 0 {
		return now.Add(time.Duration(job.DelaySeconds) * time.Second), nil
	}
	if job.EverySeconds > 0 {
		return now.Add(time.Duration(job.EverySeconds) * time.Second), nil
	}
	if job.CronExpr != "" {
		return nextCronTime(job.CronExpr, now)
	}
	return time.Time{}, fmt.Errorf("no schedule specified")
}

// ===== Simple cron expression parser (5 fields: min hour dom mon dow) =====

// ValidateCronExpr pre-validates a cron expression format
func ValidateCronExpr(expr string) error {
	_, err := nextCronTime(expr, time.Now())
	return err
}

// nextCronTime calculates the next trigger time for a cron expression after now
func nextCronTime(expr string, now time.Time) (time.Time, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return time.Time{}, fmt.Errorf("cron expression must have exactly 5 fields (min hour dom mon dow), got %d", len(fields))
	}

	minuteSet, err := parseCronField(fields[0], 0, 59)
	if err != nil {
		return time.Time{}, fmt.Errorf("minute field: %w", err)
	}
	hourSet, err := parseCronField(fields[1], 0, 23)
	if err != nil {
		return time.Time{}, fmt.Errorf("hour field: %w", err)
	}
	domSet, err := parseCronField(fields[2], 1, 31)
	if err != nil {
		return time.Time{}, fmt.Errorf("day-of-month field: %w", err)
	}
	monSet, err := parseCronField(fields[3], 1, 12)
	if err != nil {
		return time.Time{}, fmt.Errorf("month field: %w", err)
	}
	dowSet, err := parseCronField(fields[4], 0, 6)
	if err != nil {
		return time.Time{}, fmt.Errorf("day-of-week field: %w", err)
	}

	// Start searching from now+1 minute, max 4 years
	t := now.Truncate(time.Minute).Add(time.Minute)
	limit := t.Add(4 * 365 * 24 * time.Hour)

	for t.Before(limit) {
		if !monSet[int(t.Month())] {
			// Skip to next month
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, t.Location())
			continue
		}
		if !domSet[t.Day()] || !dowSet[int(t.Weekday())] {
			t = t.AddDate(0, 0, 1)
			t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
			continue
		}
		if !hourSet[t.Hour()] {
			t = t.Add(time.Hour)
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, t.Location())
			continue
		}
		if !minuteSet[t.Minute()] {
			t = t.Add(time.Minute)
			continue
		}
		return t, nil
	}
	return time.Time{}, fmt.Errorf("no next run found within 4 years")
}

// parseCronField parses a single cron field and returns a set of allowed values
func parseCronField(field string, min, max int) (map[int]bool, error) {
	result := make(map[int]bool)
	parts := strings.Split(field, ",")
	for _, part := range parts {
		if err := parseCronPart(part, min, max, result); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func parseCronPart(part string, min, max int, result map[int]bool) error {
	// Handle step: */n or range/n
	step := 1
	if idx := strings.Index(part, "/"); idx >= 0 {
		s := 0
		if _, err := fmt.Sscanf(part[idx+1:], "%d", &s); err != nil || s <= 0 {
			return fmt.Errorf("invalid step in %q", part)
		}
		step = s
		part = part[:idx]
	}

	if part == "*" {
		for i := min; i <= max; i += step {
			result[i] = true
		}
		return nil
	}

	// Range: a-b
	if idx := strings.Index(part, "-"); idx >= 0 {
		var a, b int
		if _, err := fmt.Sscanf(part[:idx], "%d", &a); err != nil {
			return fmt.Errorf("invalid range start in %q", part)
		}
		if _, err := fmt.Sscanf(part[idx+1:], "%d", &b); err != nil {
			return fmt.Errorf("invalid range end in %q", part)
		}
		if a < min || b > max || a > b {
			return fmt.Errorf("range %d-%d out of bounds [%d,%d]", a, b, min, max)
		}
		for i := a; i <= b; i += step {
			result[i] = true
		}
		return nil
	}

	// Single value
	var v int
	if _, err := fmt.Sscanf(part, "%d", &v); err != nil {
		return fmt.Errorf("invalid value %q", part)
	}
	if v < min || v > max {
		return fmt.Errorf("value %d out of bounds [%d,%d]", v, min, max)
	}
	if step > 1 {
		for i := v; i <= max; i += step {
			result[i] = true
		}
	} else {
		result[v] = true
	}
	return nil
}
