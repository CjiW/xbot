package tools

import (
	"crypto/sha256"
	"fmt"
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
	// 先检查 ctx 是否为 nil，避免后续访问 panic
	if ctx == nil {
		return
	}

	// 使用 OriginUserID 作为同步键（基于原始用户隔离）
	syncUserID := ctx.OriginUserID
	if syncUserID == "" {
		syncUserID = ctx.SenderID // fallback：兼容旧数据
	}
	if syncUserID == "" || ctx.WorkspaceRoot == "" {
		return
	}

	globalSkillSyncer.mu.Lock()
	// Evict stale entries (older than 24h) to prevent unbounded map growth
	now := time.Now()
	for k, v := range globalSkillSyncer.synced {
		if now.Sub(v) > 24*time.Hour {
			delete(globalSkillSyncer.synced, k)
		}
	}
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

// fileChecksum computes SHA256 hex digest of a file.
func fileChecksum(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// syncFile copies src → dst only if dst is missing, older than src, or has different content.
// When mtime differs, uses SHA256 checksum to confirm whether content actually changed.
func syncFile(src, dst string) {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return
	}
	dstInfo, err := os.Stat(dst)
	if err == nil {
		// dst exists — check if up-to-date
		if !srcInfo.ModTime().After(dstInfo.ModTime()) {
			return // dst is same age or newer, skip
		}
		// mtime differs; verify content actually changed via checksum
		srcSum := fileChecksum(src)
		if srcSum != "" {
			dstSum := fileChecksum(dst)
			if dstSum != "" && srcSum == dstSum {
				// Content identical despite mtime difference (e.g. touch);
				// preserve dst to avoid unnecessary I/O
				return
			}
		}
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
