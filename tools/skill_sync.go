package tools

import (
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	log "xbot/logger"
)

// skillSyncer manages lazy sync of global skills/agents into user workspaces.
// Each user (by senderID) is synced periodically (every 5 minutes) to pick up
// changes to global skills/agents directories.
type skillSyncer struct {
	mu     sync.Mutex
	synced map[string]time.Time // senderID → last sync time
}

var globalSkillSyncer = &skillSyncer{synced: make(map[string]time.Time)}

// EnsureSynced lazily copies global skills and agents into the user's workspace volume.
// Safe to call repeatedly; actual I/O only happens once per user every 5 minutes.
func EnsureSynced(ctx *ToolContext) {
	// 使用 OriginUserID 作为同步键（基于原始用户隔离）
	syncUserID := ctx.OriginUserID
	if syncUserID == "" {
		syncUserID = ctx.SenderID // fallback：兼容旧数据
	}
	if ctx == nil || syncUserID == "" || ctx.WorkspaceRoot == "" {
		return
	}

	globalSkillSyncer.mu.Lock()
	if last, ok := globalSkillSyncer.synced[syncUserID]; ok && time.Since(last) < 5*time.Minute {
		globalSkillSyncer.mu.Unlock()
		return
	}
	globalSkillSyncer.synced[syncUserID] = time.Now()
	globalSkillSyncer.mu.Unlock()

	syncSkillsAndAgents(ctx)
}

func syncSkillsAndAgents(ctx *ToolContext) {
	targetSkillsDir := filepath.Join(ctx.WorkspaceRoot, ".skills")
	targetAgentsDir := filepath.Join(ctx.WorkspaceRoot, ".agents")

	// Sync global skill directories
	for _, srcDir := range ctx.SkillsDirs {
		syncDir(srcDir, targetSkillsDir)
	}

	// Sync global agents directory
	if ctx.AgentsDir != "" {
		syncFlatDir(ctx.AgentsDir, targetAgentsDir)
	}
}

// syncDir copies skill subdirectories (each skill is a dir with SKILL.md etc.)
func syncDir(srcRoot, dstRoot string) {
	entries, err := os.ReadDir(srcRoot)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		srcSkill := filepath.Join(srcRoot, e.Name())
		dstSkill := filepath.Join(dstRoot, e.Name())
		syncTree(srcSkill, dstSkill)
	}
}

// syncFlatDir copies files (not recursing into subdirs) — for agents/*.md
func syncFlatDir(srcDir, dstDir string) {
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		syncFile(filepath.Join(srcDir, e.Name()), filepath.Join(dstDir, e.Name()))
	}
}

// syncTree recursively copies srcDir → dstDir, skipping files that are up-to-date.
func syncTree(srcDir, dstDir string) {
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		srcPath := filepath.Join(srcDir, e.Name())
		dstPath := filepath.Join(dstDir, e.Name())
		if e.IsDir() {
			syncTree(srcPath, dstPath)
		} else {
			syncFile(srcPath, dstPath)
		}
	}
}

// syncFile copies src → dst only if dst is missing or older than src.
func syncFile(src, dst string) {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return
	}
	dstInfo, err := os.Stat(dst)
	if err == nil && !srcInfo.ModTime().After(dstInfo.ModTime()) {
		return // dst exists and is up-to-date
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		log.WithError(err).Warnf("skill sync: mkdir %s", filepath.Dir(dst))
		return
	}

	srcF, err := os.Open(src)
	if err != nil {
		return
	}
	defer srcF.Close()

	dstF, err := os.Create(dst)
	if err != nil {
		log.WithError(err).Warnf("skill sync: create %s", dst)
		return
	}
	defer dstF.Close()

	if _, err := io.Copy(dstF, srcF); err != nil {
		log.WithError(err).Warnf("skill sync: copy %s → %s", src, dst)
		return
	}

	// Preserve source modtime so future checks skip unchanged files
	_ = os.Chtimes(dst, srcInfo.ModTime(), srcInfo.ModTime())
}
