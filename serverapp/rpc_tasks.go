package serverapp

import (
	"fmt"
	"runtime/debug"
	"strconv"
	"time"

	"xbot/channel"
	log "xbot/logger"
	"xbot/storage/sqlite"
)

// --- Parameter types ---

type sessionKeyParams struct {
	SessionKey string `json:"session_key"`
}

type taskIDParams struct {
	TaskID string `json:"task_id"`
}

type setChannelConfigParams struct {
	Channel string            `json:"channel"`
	Values  map[string]string `json:"values"`
}

// bgTaskJSON is the JSON shape for background task listing.
type bgTaskJSON struct {
	ID         string `json:"id"`
	Command    string `json:"command"`
	Status     string `json:"status"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at,omitempty"`
	Output     string `json:"output"`
	ExitCode   int    `json:"exit_code"`
	Error      string `json:"error,omitempty"`
}

// --- Handlers ---

func (h *rpcContext) getBgTaskCount(p sessionKeyParams) (int, error) {
	if !isAdmin(h.authSenderID) && p.SessionKey != "" {
		if owner := sessionKeyOwner(p.SessionKey); owner != "" && owner != h.bizID {
			return 0, fmt.Errorf("access denied")
		}
	}
	if h.backend.BgTaskManager() == nil {
		return 0, nil
	}
	return len(h.backend.BgTaskManager().ListRunning(p.SessionKey)), nil
}

func (h *rpcContext) listBgTasks(p sessionKeyParams) (interface{}, error) {
	if !isAdmin(h.authSenderID) && p.SessionKey != "" {
		if owner := sessionKeyOwner(p.SessionKey); owner != "" && owner != h.bizID {
			return nil, fmt.Errorf("access denied")
		}
	}
	if h.backend.BgTaskManager() == nil {
		return []struct{}{}, nil
	}
	tasks := h.backend.BgTaskManager().ListAllForSession(p.SessionKey)
	result := make([]bgTaskJSON, len(tasks))
	for i, t := range tasks {
		result[i] = bgTaskJSON{
			ID:        t.ID,
			Command:   t.Command,
			Status:    string(t.Status),
			StartedAt: t.StartedAt.Format(time.RFC3339),
			ExitCode:  t.ExitCode,
			Output:    t.Output,
			Error:     t.Error,
		}
		if t.FinishedAt != nil {
			result[i].FinishedAt = t.FinishedAt.Format(time.RFC3339)
		}
	}
	return result, nil
}

func (h *rpcContext) killBgTask(p taskIDParams) error {
	if h.backend.BgTaskManager() == nil {
		return fmt.Errorf("background tasks not available")
	}
	if !isAdmin(h.authSenderID) {
		task, err := h.backend.BgTaskManager().Status(p.TaskID)
		if err != nil {
			return fmt.Errorf("access denied: task not found")
		}
		if owner := sessionKeyOwner(task.SessionKey()); owner != "" && owner != h.bizID {
			return fmt.Errorf("access denied")
		}
	}
	return h.backend.BgTaskManager().Kill(p.TaskID)
}

func (h *rpcContext) cleanupCompletedBgTasks(p sessionKeyParams) (bool, error) {
	if !isAdmin(h.authSenderID) && p.SessionKey != "" {
		if owner := sessionKeyOwner(p.SessionKey); owner != "" && owner != h.bizID {
			return false, fmt.Errorf("access denied")
		}
	}
	if h.backend.BgTaskManager() != nil {
		h.backend.BgTaskManager().RemoveCompletedTasks(p.SessionKey)
	}
	return true, nil
}

func (h *rpcContext) listTenants() (interface{}, error) {
	if h.backend.MultiSession() == nil {
		return []struct{}{}, nil
	}
	db := h.backend.MultiSession().DB()
	if db == nil {
		return []struct{}{}, nil
	}
	tenantSvc := sqlite.NewTenantService(db)
	tenants, err := tenantSvc.ListTenants()
	if err != nil {
		return nil, err
	}
	if !isAdmin(h.authSenderID) {
		var userTenants []sqlite.TenantInfo
		for _, t := range tenants {
			if t.ChatID == h.bizID {
				userTenants = append(userTenants, t)
			}
		}
		tenants = userTenants
	}
	// Filter out agent tenants
	var filtered []sqlite.TenantInfo
	for _, t := range tenants {
		if t.Channel == "agent" {
			continue
		}
		filtered = append(filtered, t)
	}

	type tenantJSON struct {
		ID           int64  `json:"id"`
		Channel      string `json:"channel"`
		ChatID       string `json:"chat_id"`
		CreatedAt    string `json:"created_at"`
		LastActiveAt string `json:"last_active_at"`
	}
	result := make([]tenantJSON, len(filtered))
	for i, t := range filtered {
		result[i] = tenantJSON{
			ID:           t.ID,
			Channel:      t.Channel,
			ChatID:       t.ChatID,
			CreatedAt:    t.CreatedAt.Format(time.RFC3339),
			LastActiveAt: t.LastActiveAt.Format(time.RFC3339),
		}
	}
	return result, nil
}

func (h *rpcContext) resetTokenState() error {
	h.backend.ResetTokenState()
	return nil
}

func (h *rpcContext) setChannelConfig(p setChannelConfigParams) (interface{}, error) {
	if err := h.backend.SetChannelConfig(p.Channel, p.Values); err != nil {
		return nil, err
	}
	// Dynamic channel start/stop
	if enabledVal, ok := p.Values["enabled"]; ok {
		if h.disp == nil || h.msgBus == nil {
			return nil, nil
		}
		enabled, _ := strconv.ParseBool(enabledVal)
		_, alreadyRunning := h.disp.GetChannel(p.Channel)
		if enabled && !alreadyRunning {
			if ch := createChannelInstance(p.Channel, h.cfg, h.msgBus); ch != nil {
				h.disp.Register(ch)
				go func(n string, c channel.Channel) {
					defer func() {
						if r := recover(); r != nil {
							log.WithFields(log.Fields{"channel": n, "panic": r}).Error("Dynamic channel start panicked\n" + string(debug.Stack()))
						}
					}()
					log.WithField("channel", n).Info("Dynamically starting channel...")
					if err := c.Start(); err != nil {
						log.WithError(err).WithField("channel", n).Error("Dynamic channel failed")
					}
				}(ch.Name(), ch)
			}
		} else if !enabled && alreadyRunning {
			h.disp.Unregister(p.Channel)
		}
	}
	return nil, nil
}
