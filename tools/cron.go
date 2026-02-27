package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"xbot/llm"
	log "xbot/logger"
)

// CronTool 定时任务工具
type CronTool struct {
	mu   sync.Mutex
	jobs map[string]*cronJob

	stopCh chan struct{}
	once   sync.Once

	// 持久化路径（运行时由第一次 Execute 设置）
	persistPath string
}

type cronJob struct {
	ID           string `json:"id"`
	Message      string `json:"message"`
	Channel      string `json:"channel"`
	ChatID       string `json:"chat_id"`
	CronExpr     string `json:"cron_expr,omitempty"`
	EverySeconds int    `json:"every_seconds,omitempty"`
	DelaySeconds int    `json:"delay_seconds,omitempty"` // one-shot delay
	At           string `json:"at,omitempty"`            // ISO datetime for one-time
	CreatedAt    string `json:"created_at"`

	// 运行时字段（不持久化）
	nextRun  time.Time      `json:"-"`
	location *time.Location `json:"-"`
	oneShot  bool           `json:"-"`
	fired    bool           `json:"-"`
}

// NewCronTool 创建 CronTool 实例
func NewCronTool() *CronTool {
	ct := &CronTool{
		jobs:   make(map[string]*cronJob),
		stopCh: make(chan struct{}),
	}
	return ct
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
	var p cronParams
	if err := json.Unmarshal([]byte(input), &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	// 初始化持久化路径（首次调用时）
	t.mu.Lock()
	if t.persistPath == "" && ctx != nil && ctx.DataDir != "" {
		t.persistPath = filepath.Join(ctx.DataDir, "cron.json")
		t.loadJobs()
	}
	t.mu.Unlock()

	// 确保 ticker 在运行
	if ctx != nil && ctx.InjectInbound != nil {
		t.ensureTicker(ctx.InjectInbound)
	}

	switch p.Action {
	case "add":
		return t.addJob(ctx, p)
	case "list":
		return t.listJobs()
	case "remove":
		return t.removeJob(p.JobID)
	default:
		return nil, fmt.Errorf("unknown action: %s (use add, list, remove)", p.Action)
	}
}

func (t *CronTool) addJob(ctx *ToolContext, p cronParams) (*ToolResult, error) {
	if p.Message == "" {
		return nil, fmt.Errorf("message is required for add")
	}

	// 必须指定调度方式
	hasCron := p.CronExpr != ""
	hasInterval := p.EverySeconds > 0
	hasDelay := p.DelaySeconds > 0
	hasAt := p.At != ""
	count := 0
	if hasCron {
		count++
	}
	if hasInterval {
		count++
	}
	if hasDelay {
		count++
	}
	if hasAt {
		count++
	}
	if count == 0 {
		return nil, fmt.Errorf("must specify one of: cron_expr, every_seconds, delay_seconds, at")
	}
	if count > 1 {
		return nil, fmt.Errorf("specify only one of: cron_expr, every_seconds, delay_seconds, at")
	}

	now := time.Now()
	job := &cronJob{
		ID:           fmt.Sprintf("job_%s", uuid.New().String()[:8]),
		Message:      p.Message,
		CronExpr:     p.CronExpr,
		EverySeconds: p.EverySeconds,
		DelaySeconds: p.DelaySeconds,
		At:           p.At,
		CreatedAt:    now.Format(time.RFC3339),
	}

	// 设置渠道信息
	if ctx != nil {
		job.Channel = ctx.Channel
		job.ChatID = ctx.ChatID
	}

	// 初始化运行时状态
	if err := t.initJobRuntime(job); err != nil {
		return nil, err
	}

	t.mu.Lock()
	t.jobs[job.ID] = job
	t.saveJobs()
	t.mu.Unlock()

	schedDesc := t.scheduleDescription(job)
	return NewResult(fmt.Sprintf("Job created: %s\nSchedule: %s\nMessage: %s\nNext run: %s",
		job.ID, schedDesc, job.Message, job.nextRun.Format("2006-01-02 15:04:05 MST"))), nil
}

func (t *CronTool) listJobs() (*ToolResult, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if len(t.jobs) == 0 {
		return NewResult("No scheduled jobs."), nil
	}

	// 按创建时间排序
	jobs := make([]*cronJob, 0, len(t.jobs))
	for _, j := range t.jobs {
		jobs = append(jobs, j)
	}
	sort.Slice(jobs, func(i, j int) bool {
		return jobs[i].CreatedAt < jobs[j].CreatedAt
	})

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Scheduled jobs (%d):\n\n", len(jobs)))
	for _, j := range jobs {
		sb.WriteString(fmt.Sprintf("- **%s**\n  Schedule: %s\n  Message: %s\n  Channel: %s\n  Next: %s\n\n",
			j.ID, t.scheduleDescription(j), j.Message, j.Channel,
			j.nextRun.Format("2006-01-02 15:04:05 MST")))
	}
	return NewResult(sb.String()), nil
}

func (t *CronTool) removeJob(jobID string) (*ToolResult, error) {
	if jobID == "" {
		return nil, fmt.Errorf("job_id is required for remove")
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	if _, ok := t.jobs[jobID]; !ok {
		return nil, fmt.Errorf("job not found: %s", jobID)
	}
	delete(t.jobs, jobID)
	t.saveJobs()
	return NewResult(fmt.Sprintf("Job removed: %s", jobID)), nil
}

// initJobRuntime 初始化 job 的运行时状态（nextRun、location 等）
func (t *CronTool) initJobRuntime(job *cronJob) error {
	now := time.Now()

	// 统一使用本地时区
	job.location = time.Local

	if job.At != "" {
		// 一次性定时
		at, err := time.ParseInLocation("2006-01-02T15:04:05", job.At, job.location)
		if err != nil {
			// 尝试带时区的格式
			at, err = time.Parse(time.RFC3339, job.At)
			if err != nil {
				return fmt.Errorf("invalid datetime %q: use ISO format like 2026-02-12T10:30:00", job.At)
			}
		}
		if at.Before(now) {
			return fmt.Errorf("datetime %s is in the past", job.At)
		}
		job.nextRun = at
		job.oneShot = true
	} else if job.DelaySeconds > 0 {
		job.nextRun = now.Add(time.Duration(job.DelaySeconds) * time.Second)
		job.oneShot = true
	} else if job.EverySeconds > 0 {
		job.nextRun = now.Add(time.Duration(job.EverySeconds) * time.Second)
	} else if job.CronExpr != "" {
		next, err := nextCronTime(job.CronExpr, now.In(job.location))
		if err != nil {
			return fmt.Errorf("invalid cron expression %q: %w", job.CronExpr, err)
		}
		job.nextRun = next
	}
	return nil
}

// ensureTicker 确保后台 ticker 在运行
func (t *CronTool) ensureTicker(injectFunc func(string, string, string)) {
	t.once.Do(func() {
		go t.tickerLoop(injectFunc)
	})
}

// tickerLoop 每秒检查一次是否有 job 到期
func (t *CronTool) tickerLoop(injectFunc func(string, string, string)) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-t.stopCh:
			return
		case now := <-ticker.C:
			t.checkAndFire(now, injectFunc)
		}
	}
}

func (t *CronTool) checkAndFire(now time.Time, injectFunc func(string, string, string)) {
	t.mu.Lock()
	defer t.mu.Unlock()

	var toRemove []string

	for id, job := range t.jobs {
		if now.Before(job.nextRun) {
			continue
		}

		// 触发
		log.WithFields(log.Fields{
			"job_id":  id,
			"channel": job.Channel,
		}).Info("Cron job fired")

		// 将消息注入 Agent 入站队列，触发完整处理循环
		// 直接使用用户定义的任务描述（不加前缀，因为 cron 消息已通过 SenderID 识别）
		content := fmt.Sprintf("%s", job.Message)
		if injectFunc != nil && job.Channel != "" && job.ChatID != "" {
			injectFunc(job.Channel, job.ChatID, content)
		}

		if job.oneShot {
			job.fired = true
			toRemove = append(toRemove, id)
		} else if job.EverySeconds > 0 {
			job.nextRun = now.Add(time.Duration(job.EverySeconds) * time.Second)
		} else if job.CronExpr != "" {
			next, err := nextCronTime(job.CronExpr, now.In(job.location))
			if err != nil {
				log.WithError(err).Errorf("Failed to calculate next cron time for %s, removing", id)
				toRemove = append(toRemove, id)
			} else {
				job.nextRun = next
			}
		}
	}

	if len(toRemove) > 0 {
		for _, id := range toRemove {
			delete(t.jobs, id)
		}
		t.saveJobs()
	}
}

func (t *CronTool) scheduleDescription(job *cronJob) string {
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

// saveJobs 持久化 jobs 到文件（调用方须持有 mu 锁）
func (t *CronTool) saveJobs() {
	if t.persistPath == "" {
		return
	}
	data, err := json.MarshalIndent(t.jobs, "", "  ")
	if err != nil {
		log.WithError(err).Error("Failed to marshal cron jobs")
		return
	}
	if err := os.MkdirAll(filepath.Dir(t.persistPath), 0o755); err != nil {
		log.WithError(err).Error("Failed to create cron data directory")
		return
	}
	if err := os.WriteFile(t.persistPath, data, 0o644); err != nil {
		log.WithError(err).Error("Failed to save cron jobs")
	}
}

// loadJobs 从文件加载 jobs（调用方须持有 mu 锁）
func (t *CronTool) loadJobs() {
	if t.persistPath == "" {
		return
	}
	data, err := os.ReadFile(t.persistPath)
	if err != nil {
		if !os.IsNotExist(err) {
			log.WithError(err).Warn("Failed to load cron jobs")
		}
		return
	}
	var jobs map[string]*cronJob
	if err := json.Unmarshal(data, &jobs); err != nil {
		log.WithError(err).Warn("Failed to parse cron jobs file")
		return
	}
	now := time.Now()
	for id, job := range jobs {
		if err := t.initJobRuntime(job); err != nil {
			log.WithError(err).Warnf("Skipping invalid cron job %s on load", id)
			continue
		}
		// 跳过已过期的一次性任务
		if job.oneShot && job.nextRun.Before(now) {
			log.Infof("Skipping expired one-shot cron job %s", id)
			continue
		}
		t.jobs[id] = job
	}
	log.Infof("Loaded %d cron jobs from %s", len(t.jobs), t.persistPath)
}

// Stop 停止 ticker
func (t *CronTool) Stop() {
	select {
	case <-t.stopCh:
	default:
		close(t.stopCh)
	}
}

// ===== 简易 cron 表达式解析器（5 字段：分 时 日 月 周） =====

// nextCronTime 计算给定 cron 表达式在 now 之后的下一个触发时间
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

	// 从 now+1分钟 开始搜索，最多搜索 4 年
	t := now.Truncate(time.Minute).Add(time.Minute)
	limit := t.Add(4 * 365 * 24 * time.Hour)

	for t.Before(limit) {
		if !monSet[int(t.Month())] {
			// 跳到下个月
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

// parseCronField 解析单个 cron 字段，返回允许值集合
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
	// 处理 step: */n 或 range/n
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

	// 范围: a-b
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

	// 单个值
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
