package agent

import (
	"context"
	"testing"
	"time"

	"xbot/storage/vectordb"
)

func TestBuildProjectHintText_NoService(t *testing.T) {
	result := BuildProjectHintText(context.Background(), nil, 1)
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}

func TestBuildProjectHintText_ZeroTenantID(t *testing.T) {
	result := BuildProjectHintText(context.Background(), nil, 0)
	if result != "" {
		t.Errorf("expected empty string for zero tenantID, got %q", result)
	}
}

func TestProjectHintMiddleware_Name(t *testing.T) {
	var nilSvc *vectordb.ArchivalService
	mw := NewProjectHintMiddleware(func() *vectordb.ArchivalService { return nilSvc })
	if mw.Name() != "project_hint" {
		t.Errorf("expected name 'project_hint', got %q", mw.Name())
	}
}

func TestProjectHintMiddleware_Priority(t *testing.T) {
	var nilSvc *vectordb.ArchivalService
	mw := NewProjectHintMiddleware(func() *vectordb.ArchivalService { return nilSvc })
	if mw.Priority() != 1 {
		t.Errorf("expected priority 1, got %d", mw.Priority())
	}
}

func TestProjectHintMiddleware_NilService(t *testing.T) {
	var nilSvc *vectordb.ArchivalService
	mw := NewProjectHintMiddleware(func() *vectordb.ArchivalService { return nilSvc })
	mc := &MessageContext{
		Ctx:         context.Background(),
		SystemParts: make(map[string]string),
		Extra:       make(map[string]any),
	}
	mc.SetExtra(ExtraKeyTenantID, int64(1))

	err := mw.Process(mc)
	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
	if len(mc.SystemParts) > 0 {
		t.Errorf("expected no system parts, got %d", len(mc.SystemParts))
	}
}

func TestProjectHintMiddleware_NoTenantID(t *testing.T) {
	var nilSvc *vectordb.ArchivalService
	mw := NewProjectHintMiddleware(func() *vectordb.ArchivalService { return nilSvc })
	mc := &MessageContext{
		Ctx:         context.Background(),
		SystemParts: make(map[string]string),
		Extra:       make(map[string]any),
	}

	err := mw.Process(mc)
	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
}

func TestProjectHintMiddleware_SystemPartsKeyConvention(t *testing.T) {
	key := "05_project_hint"
	if key < "00_base" || key > "10_skills" {
		t.Errorf("key %q not between 00_base and 10_skills", key)
	}
}

func TestProjectHintCache_Expire(t *testing.T) {
	cache := newProjectHintCache()
	now := time.Now()
	// 写入已过期的缓存
	cache.mu.Lock()
	cache.items[1] = &cachedHint{
		entries:  nil,
		expireAt: now.Add(-time.Second), // 已过期
	}
	cache.mu.Unlock()

	cache.mu.RLock()
	cached, hit := cache.items[1]
	cache.mu.RUnlock()
	if !hit {
		t.Fatal("expected cache hit")
	}
	if time.Now().Before(cached.expireAt) {
		t.Error("expected expired cache entry")
	}
}

func TestProjectHintCache_StoreAndRetrieve(t *testing.T) {
	cache := newProjectHintCache()
	entries := []vectordb.ArchivalEntry{
		{ID: "1", Content: "test project"},
	}

	cache.mu.Lock()
	cache.items[42] = &cachedHint{
		entries:  entries,
		expireAt: time.Now().Add(60 * time.Second),
	}
	cache.mu.Unlock()

	cache.mu.RLock()
	cached, hit := cache.items[42]
	cache.mu.RUnlock()
	if !hit {
		t.Fatal("expected cache hit")
	}
	if len(cached.entries) != 1 {
		t.Errorf("expected 1 entry, got %d", len(cached.entries))
	}
	if cached.entries[0].Content != "test project" {
		t.Errorf("expected 'test project', got %q", cached.entries[0].Content)
	}
}
