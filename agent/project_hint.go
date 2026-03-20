package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	log "xbot/logger"
	"xbot/storage/vectordb"
)

const projectCardMarker = "PROJECT_CARD"
const maxProjectCards = 3

const projectHintCacheTTL = 60 * time.Second

// cachedHint caches project hint search results for a tenant.
type cachedHint struct {
	entries  []vectordb.ArchivalEntry
	expireAt time.Time
}

// projectHintCache is an in-memory cache keyed by tenantID.
type projectHintCache struct {
	mu    sync.RWMutex
	items map[int64]*cachedHint
}

func newProjectHintCache() *projectHintCache {
	return &projectHintCache{items: make(map[int64]*cachedHint)}
}

// ProjectHintMiddleware automatically injects known project knowledge cards
// from archival memory into the system prompt. This gives the LLM immediate
// awareness of user projects without requiring manual archival_memory_search.
//
// Priority=1: runs after SystemPromptMiddleware(0), before ChannelPromptMiddleware(5)
// and MemoryInjectMiddleware(100).
type ProjectHintMiddleware struct {
	// archivalSvcGetter is a function that returns the archival service.
	// Using a getter avoids holding a direct reference that could be nil during init.
	archivalSvcGetter func() *vectordb.ArchivalService
	cache             *projectHintCache
}

// NewProjectHintMiddleware creates a project hint middleware.
// The getter function is called lazily on each Process() to handle
// the case where the archival service may not be available at construction time.
func NewProjectHintMiddleware(getter func() *vectordb.ArchivalService) *ProjectHintMiddleware {
	return &ProjectHintMiddleware{archivalSvcGetter: getter, cache: newProjectHintCache()}
}

func (m *ProjectHintMiddleware) Name() string { return "project_hint" }

// Priority=1: after SystemPromptMiddleware(0), before ChannelPromptMiddleware(5)
func (m *ProjectHintMiddleware) Priority() int { return 1 }

// Process searches for [PROJECT_CARD] entries in archival memory and injects
// them into the system prompt as a "Known Projects" section.
// Results are cached per tenantID for 60 seconds to avoid repeated embedding API calls.
func (m *ProjectHintMiddleware) Process(mc *MessageContext) error {
	archivalSvc := m.archivalSvcGetter()
	if archivalSvc == nil {
		return nil
	}

	// Get TenantID from Extra
	tenantID, ok := GetExtraTyped[int64](mc, ExtraKeyTenantID)
	if !ok || tenantID == 0 {
		return nil
	}

	// Check cache
	now := time.Now()
	m.cache.mu.RLock()
	cached, hit := m.cache.items[tenantID]
	m.cache.mu.RUnlock()
	if hit && now.Before(cached.expireAt) {
		if len(cached.entries) == 0 {
			return nil
		}
		m.injectProjectHint(mc, tenantID, cached.entries)
		return nil
	}

	// Cache miss — search for project knowledge cards
	entries, err := archivalSvc.SearchByDocumentContains(mc.Ctx, tenantID, projectCardMarker, maxProjectCards)
	if err != nil {
		// Log but don't fail — project hints are best-effort
		log.WithError(err).WithField("tenant_id", tenantID).Debug("ProjectHintMiddleware: search failed, skipping")
		return nil
	}

	// Store in cache (even empty results to avoid repeated queries)
	m.cache.mu.Lock()
	m.cache.items[tenantID] = &cachedHint{
		entries:  entries,
		expireAt: now.Add(projectHintCacheTTL),
	}
	m.cache.mu.Unlock()

	if len(entries) == 0 {
		return nil
	}

	m.injectProjectHint(mc, tenantID, entries)
	return nil
}

// injectProjectHint builds and injects project hint text into the system prompt.
func (m *ProjectHintMiddleware) injectProjectHint(mc *MessageContext, tenantID int64, entries []vectordb.ArchivalEntry) {
	var sb strings.Builder
	sb.WriteString("\n## 已知项目\n\n")
	sb.WriteString("以下是你在之前对话中了解到的用户项目信息。你可以直接参考，无需重新探索。\n\n")
	for i, entry := range entries {
		fmt.Fprintf(&sb, "### 项目 %d\n%s\n\n", i+1, entry.Content)
	}

	// Inject into SystemParts with a stable key (sorted between "00_base" and "10_skills")
	mc.SystemParts["05_project_hint"] = sb.String()

	log.WithFields(log.Fields{
		"tenant_id": tenantID,
		"count":     len(entries),
	}).Debug("ProjectHintMiddleware: injected project knowledge cards")
}

// BuildProjectHintText is a helper function that searches for project knowledge cards
// and returns the formatted text for injection. Used by SubAgent code which doesn't
// go through the pipeline.
func BuildProjectHintText(ctx context.Context, archivalSvc *vectordb.ArchivalService, tenantID int64) string {
	if archivalSvc == nil || tenantID == 0 {
		return ""
	}

	entries, err := archivalSvc.SearchByDocumentContains(ctx, tenantID, projectCardMarker, maxProjectCards)
	if err != nil || len(entries) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("\n## 用户项目背景\n\n")
	sb.WriteString("以下是你所属的 Agent 之前了解到的用户项目信息，供你参考：\n\n")
	for i, entry := range entries {
		fmt.Fprintf(&sb, "### 项目 %d\n%s\n\n", i+1, entry.Content)
	}

	return sb.String()
}
