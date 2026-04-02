package tools

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"xbot/llm"
	log "xbot/logger"
)

// TaskStatusTool returns the current status of a background task.
type TaskStatusTool struct{}

func (t *TaskStatusTool) Name() string   { return "task_status" }
func (t *TaskStatusTool) Required() bool { return false }
func (t *TaskStatusTool) Description() string {
	return `Check the status of a background task. Shows task ID, command, status (running/done/error/killed), elapsed time, and a preview of the output.

Parameters (JSON):
  - task_id: string, the task ID to check`
}

func (t *TaskStatusTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "task_id", Type: "string", Description: "The background task ID to check", Required: true},
	}
}

func (t *TaskStatusTool) Execute(toolCtx *ToolContext, input string) (*ToolResult, error) {
	if toolCtx == nil || toolCtx.BgTaskManager == nil {
		return nil, fmt.Errorf("background tasks not supported")
	}

	var params struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	task, err := toolCtx.BgTaskManager.Status(params.TaskID)
	if err != nil {
		return nil, err
	}

	return NewResult(formatTask(task)), nil
}

// TaskKillTool terminates a running background task.
type TaskKillTool struct{}

func (t *TaskKillTool) Name() string   { return "task_kill" }
func (t *TaskKillTool) Required() bool { return false }
func (t *TaskKillTool) Description() string {
	return `Terminate a running background task. All child processes of the task will be killed.

Parameters (JSON):
  - task_id: string, the task ID to kill`
}

func (t *TaskKillTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "task_id", Type: "string", Description: "The background task ID to kill", Required: true},
	}
}

func (t *TaskKillTool) Execute(toolCtx *ToolContext, input string) (*ToolResult, error) {
	if toolCtx == nil || toolCtx.BgTaskManager == nil {
		return nil, fmt.Errorf("background tasks not supported")
	}

	var params struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	if err := toolCtx.BgTaskManager.Kill(params.TaskID); err != nil {
		return NewErrorResult(fmt.Sprintf("Failed to kill task %s: %s", params.TaskID, err.Error())), nil
	}

	log.WithField("task_id", params.TaskID).Info("Background task killed by user")
	return NewResult(fmt.Sprintf("Task %s killed successfully.", params.TaskID)), nil
}

// TaskReadTool reads the full output of a completed (or running) background task.
type TaskReadTool struct{}

func (t *TaskReadTool) Name() string   { return "task_read" }
func (t *TaskReadTool) Required() bool { return false }
func (t *TaskReadTool) Description() string {
	return `Read the full output of a background task. Useful for reviewing the complete output of a completed task.

Parameters (JSON):
  - task_id: string, the task ID to read
  - tail: number (optional), only return the last N characters (default: all)`
}

func (t *TaskReadTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "task_id", Type: "string", Description: "The background task ID to read", Required: true},
		{Name: "tail", Type: "number", Description: "Only return the last N characters of output (default: all)", Required: false},
	}
}

func (t *TaskReadTool) Execute(toolCtx *ToolContext, input string) (*ToolResult, error) {
	if toolCtx == nil || toolCtx.BgTaskManager == nil {
		return nil, fmt.Errorf("background tasks not supported")
	}

	var params struct {
		TaskID string `json:"task_id"`
		Tail   int    `json:"tail"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	task, err := toolCtx.BgTaskManager.Status(params.TaskID)
	if err != nil {
		return nil, err
	}

	output := task.Output
	if params.Tail > 0 && len(output) > params.Tail {
		output = "... (truncated) ...\n" + output[len(output)-params.Tail:]
	}

	if output == "" {
		return NewResult(fmt.Sprintf("Task %s has no output yet.", task.ID)), nil
	}

	return NewResult(fmt.Sprintf("[Task %s output (%s, %d bytes)]\n%s",
		task.ID, task.Status, len(task.Output), output)), nil
}

// formatTask formats a task for display.
func formatTask(task *BackgroundTask) string {
	elapsed := time.Since(task.StartedAt).Round(time.Second)
	if task.FinishedAt != nil {
		elapsed = task.FinishedAt.Sub(task.StartedAt).Round(time.Second)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Task: %s\n", task.ID)
	fmt.Fprintf(&sb, "Command: %s\n", task.Command)
	fmt.Fprintf(&sb, "Status: %s\n", task.Status)
	fmt.Fprintf(&sb, "Elapsed: %s\n", elapsed)

	if task.ExitCode >= 0 {
		fmt.Fprintf(&sb, "Exit Code: %d\n", task.ExitCode)
	}
	if task.Error != "" {
		fmt.Fprintf(&sb, "Error: %s\n", task.Error)
	}

	// Show last 500 chars of output as preview
	preview := task.Output
	if len(preview) > 500 {
		preview = "... " + preview[len(preview)-497:]
	}
	if preview != "" {
		fmt.Fprintf(&sb, "Output Preview:\n%s\n", preview)
	}

	return sb.String()
}

// FormatBgTaskCompletion formats a completed background task notification for injection.
// This is used by the engine to inject the task result into the conversation as a tool message.
func FormatBgTaskCompletion(task *BackgroundTask) string {
	elapsed := task.FinishedAt.Sub(task.StartedAt).Round(time.Second)

	var sb strings.Builder
	fmt.Fprintf(&sb, "[System Notification] Background task %s completed.\n", task.ID)
	fmt.Fprintf(&sb, "Command: %s\n", task.Command)
	fmt.Fprintf(&sb, "Status: %s | Elapsed: %s\n", task.Status, elapsed)

	if task.ExitCode >= 0 {
		fmt.Fprintf(&sb, "Exit Code: %d\n", task.ExitCode)
	}
	if task.Error != "" {
		fmt.Fprintf(&sb, "Error: %s\n", task.Error)
	}

	if task.Output != "" {
		output := task.Output
		// Truncate large output to avoid bloating context
		const maxOutputLen = 2000
		if len(output) > maxOutputLen {
			fmt.Fprintf(&sb, "\nOutput (truncated, %d/%d chars):\n%s\n... [use offload_recall or check task output for full content]", maxOutputLen, len(output), output[:maxOutputLen])
		} else {
			fmt.Fprintf(&sb, "\nOutput:\n%s", output)
		}
	} else {
		sb.WriteString("\n(no output)")
	}

	return sb.String()
}

// ListBgTasks returns a summary of all background tasks for a session.
func ListBgTasks(mgr *BackgroundTaskManager, sessionKey string) string {
	if mgr == nil {
		return "No background task support."
	}

	tasks := mgr.List(sessionKey)
	if len(tasks) == 0 {
		return "No background tasks."
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Background tasks (%d):\n", len(tasks))
	for _, task := range tasks {
		elapsed := time.Since(task.StartedAt).Round(time.Second)
		if task.FinishedAt != nil {
			elapsed = task.FinishedAt.Sub(task.StartedAt).Round(time.Second)
		}
		fmt.Fprintf(&sb, "  %s  %s  %s  %s  (%s)\n",
			task.ID, task.Status, elapsed, task.Command, truncateStr(task.Command, 40))
	}
	return sb.String()
}

func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// ensure TaskStatusTool implements Tool
var _ Tool = (*TaskStatusTool)(nil)
var _ Tool = (*TaskKillTool)(nil)
var _ Tool = (*TaskReadTool)(nil)
