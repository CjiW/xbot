package tools

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"xbot/cron"
	"xbot/llm"
	log "xbot/logger"
	"xbot/storage/sqlite"

	"github.com/google/uuid"
)

// CronTool 定时任务工具（无状态）
type CronTool struct {
	cronSvc *sqlite.CronService
}

// NewCronTool 创建 CronTool 实例
func NewCronTool(cronSvc *sqlite.CronService) *CronTool {
	return &CronTool{
		cronSvc: cronSvc,
	}
}

func (t *CronTool) Name() string { return "Cron" }

func (t *CronTool) Description() string {
	return `Schedule tasks that trigger the agent at specified times. Actions: add, list, remove.
- add: create a job with message + one of (cron_expr, every_seconds, delay_seconds, at). When triggered, the message is sent to the agent as a user message, initiating a full processing loop (LLM reasoning + tool calls + reply).
- list: show all scheduled jobs
- remove: delete a job by job_id`
}

func (t *CronTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "action", Type: "string", Description: "Action: add, list, remove", Required: true},
		{Name: "message", Type: "string", Description: "Prompt sent to the agent when the job triggers. Write it as a user instruction, e.g. 'Check server status and report any issues' or 'Remind me to start the standup meeting'. The agent will process this as a normal user message.", Required: false},
		{Name: "every_seconds", Type: "integer", Description: "Interval in seconds for recurring tasks", Required: false},
		{Name: "delay_seconds", Type: "integer", Description: "Execute once after this many seconds (one-shot delay)", Required: false},
		{Name: "cron_expr", Type: "string", Description: "Cron expression like '0 9 * * *' (5-field, Local timezone)", Required: false},
		{Name: "at", Type: "string", Description: "ISO datetime for one-time execution, e.g. '2026-02-12T10:30:00'", Required: false},
		{Name: "job_id", Type: "string", Description: "Job ID (for remove)", Required: false},
	}
}

type cronParams struct {
	Action       string `json:"action"`
	Message      string `json:"message"`
	EverySeconds int    `json:"every_seconds"`
	DelaySeconds int    `json:"delay_seconds"`
	CronExpr     string `json:"cron_expr"`
	At           string `json:"at"`
	JobID        string `json:"job_id"`
}

func (t *CronTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	p, err := parseToolArgs[cronParams](input)
	if err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	senderID := ""
	if ctx != nil {
		senderID = ctx.SenderID
	}

	switch p.Action {
	case "add":
		return t.addJob(ctx, *p)
	case "list":
		return t.listJobs(senderID)
	case "remove":
		return t.removeJob(p.JobID, senderID)
	default:
		return nil, fmt.Errorf("unknown action: %s (use add, list, remove)", p.Action)
	}
}

func (t *CronTool) addJob(ctx *ToolContext, p cronParams) (*ToolResult, error) {
	if p.Message == "" {
		return nil, fmt.Errorf("message is required for add")
	}

	// Must specify exactly one scheduling method
	hasCron := p.CronExpr != ""
	hasInterval := p.EverySeconds > 0
	hasDelay := p.DelaySeconds > 0
	hasAt := p.At != ""
	count := 0
	if hasCron {
		if err := cron.ValidateCronExpr(p.CronExpr); err != nil {
			return nil, fmt.Errorf("invalid cron expression: %w", err)
		}
		count++
	}
	if hasInterval {
		count++
	}
	if hasDelay {
		count++
	}
	if hasAt {
		// Validate ISO datetime format (e.g., "2026-02-12T10:30:00" or "2026-02-12T10:30:00Z")
		_, parseErr := time.Parse(time.RFC3339, p.At)
		if parseErr != nil {
			// Also try without timezone for common LLM outputs like "2026-02-12T10:30:00"
			_, parseErr2 := time.Parse("2006-01-02T15:04:05", p.At)
			if parseErr2 != nil {
				return nil, fmt.Errorf("invalid 'at' datetime format %q: must be ISO 8601 (e.g. '2026-02-12T10:30:00' or '2026-02-12T10:30:00Z'): %w", p.At, parseErr)
			}
		}
		count++
	}
	if count == 0 {
		return nil, fmt.Errorf("must specify one of: cron_expr, every_seconds, delay_seconds, at")
	}
	if count > 1 {
		return nil, fmt.Errorf("specify only one of: cron_expr, every_seconds, delay_seconds, at")
	}

	now := time.Now()
	job := &sqlite.CronJob{
		ID:           fmt.Sprintf("job_%s", uuid.New().String()[:8]),
		Message:      p.Message,
		CronExpr:     p.CronExpr,
		EverySeconds: p.EverySeconds,
		DelaySeconds: p.DelaySeconds,
		At:           p.At,
		CreatedAt:    now,
	}

	// Set channel and sender info
	if ctx != nil {
		job.Channel = ctx.Channel
		job.ChatID = ctx.ChatID
		job.SenderID = ctx.SenderID
	}

	// Calculate next run and one_shot flag
	job.OneShot = job.At != "" || job.DelaySeconds > 0

	nextRun, err := cron.CalculateNextRun(job, now)
	if err != nil {
		return nil, err
	}

	// Check if one-shot job is in the past
	if job.OneShot && nextRun.Before(now) {
		return nil, fmt.Errorf("datetime %s is in the past", job.At)
	}

	job.NextRun = nextRun

	// Save to database
	if err := t.cronSvc.AddJob(job); err != nil {
		log.WithError(err).Error("Failed to save cron job")
		return nil, fmt.Errorf("failed to save cron job: %w", err)
	}

	schedDesc := t.scheduleDescription(job)
	return NewResult(fmt.Sprintf("Job created: %s\nSchedule: %s\nMessage: %s\nNext run: %s",
		job.ID, schedDesc, job.Message, job.NextRun.Format("2006-01-02 15:04:05 MST"))), nil
}

func (t *CronTool) listJobs(senderID string) (*ToolResult, error) {
	jobs, err := t.cronSvc.ListJobsBySender(senderID)
	if err != nil {
		log.WithError(err).Error("Failed to list cron jobs")
		return nil, fmt.Errorf("failed to list cron jobs: %w", err)
	}

	if len(jobs) == 0 {
		return NewResult("No scheduled jobs."), nil
	}

	// Sort by created time
	sort.Slice(jobs, func(i, j int) bool {
		return jobs[i].CreatedAt.Before(jobs[j].CreatedAt)
	})

	var sb strings.Builder
	fmt.Fprintf(&sb, "Scheduled jobs (%d):\n\n", len(jobs))
	for _, j := range jobs {
		fmt.Fprintf(&sb, "- **%s**\n  Schedule: %s\n  Message: %s\n  Channel: %s\n  Next: %s\n\n",
			j.ID, t.scheduleDescription(j), j.Message, j.Channel,
			j.NextRun.Format("2006-01-02 15:04:05 MST"))
	}
	return NewResult(sb.String()), nil
}

func (t *CronTool) removeJob(jobID string, senderID string) (*ToolResult, error) {
	if jobID == "" {
		return nil, fmt.Errorf("job_id is required for remove")
	}

	// Verify job exists and belongs to sender
	job, err := t.cronSvc.GetJob(jobID)
	if err != nil {
		log.WithError(err).Error("Failed to get cron job")
		return nil, fmt.Errorf("failed to get cron job: %w", err)
	}
	if job == nil {
		return nil, fmt.Errorf("job not found: %s", jobID)
	}
	if job.SenderID != senderID {
		return nil, fmt.Errorf("job not found: %s", jobID)
	}

	if err := t.cronSvc.RemoveJob(jobID); err != nil {
		log.WithError(err).Error("Failed to remove cron job")
		return nil, fmt.Errorf("failed to remove cron job: %w", err)
	}
	return NewResult(fmt.Sprintf("Job removed: %s", jobID)), nil
}

func (t *CronTool) scheduleDescription(job *sqlite.CronJob) string {
	if job.At != "" {
		return fmt.Sprintf("once at %s", job.At)
	}
	if job.DelaySeconds > 0 {
		if job.DelaySeconds >= 3600 {
			return fmt.Sprintf("once after %dh%dm", job.DelaySeconds/3600, (job.DelaySeconds%3600)/60)
		}
		if job.DelaySeconds >= 60 {
			return fmt.Sprintf("once after %dm%ds", job.DelaySeconds/60, job.DelaySeconds%60)
		}
		return fmt.Sprintf("once after %ds", job.DelaySeconds)
	}
	if job.EverySeconds > 0 {
		if job.EverySeconds >= 3600 {
			return fmt.Sprintf("every %dh%dm", job.EverySeconds/3600, (job.EverySeconds%3600)/60)
		}
		if job.EverySeconds >= 60 {
			return fmt.Sprintf("every %dm%ds", job.EverySeconds/60, job.EverySeconds%60)
		}
		return fmt.Sprintf("every %ds", job.EverySeconds)
	}
	if job.CronExpr != "" {
		return fmt.Sprintf("cron(%s)", job.CronExpr)
	}
	return "unknown"
}
