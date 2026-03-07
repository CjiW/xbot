package vectordb

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	chromem "github.com/philippgille/chromem-go"

	log "xbot/logger"
)

// ArchivalEntry represents a single archival memory search result.
type ArchivalEntry struct {
	ID         string
	TenantID   int64
	Content    string
	CreatedAt  time.Time
	Similarity float32
}

// RecallFunc performs FTS5 recall search over conversation history.
// Decoupled from SQLite so the vectordb package has no sqlite dependency.
type RecallFunc func(tenantID int64, query string, limit int) ([]string, error)

// ArchivalService stores long-term archival memory entries in chromem-go,
// a pure-Go embedded vector database with file-based persistence.
type ArchivalService struct {
	db            *chromem.DB
	embeddingFunc chromem.EmbeddingFunc
	recallFn      RecallFunc
}

// NewArchivalService creates an archival service backed by chromem-go.
//
// persistDir: directory for chromem-go file persistence (created if needed).
// embeddingFunc: OpenAI-compatible embedding function (nil disables vector search).
// recallFn: optional FTS5 recall search over conversation history.
func NewArchivalService(persistDir string, embeddingFunc chromem.EmbeddingFunc, recallFn RecallFunc) (*ArchivalService, error) {
	db, err := chromem.NewPersistentDB(persistDir, false)
	if err != nil {
		return nil, fmt.Errorf("create chromem-go DB at %s: %w", persistDir, err)
	}

	log.WithFields(log.Fields{
		"persist_dir":    persistDir,
		"embedding_func": embeddingFunc != nil,
	}).Info("Archival memory (chromem-go) initialized")

	return &ArchivalService{
		db:            db,
		embeddingFunc: embeddingFunc,
		recallFn:      recallFn,
	}, nil
}

// NewEmbeddingFunc creates a chromem-go EmbeddingFunc from OpenAI-compatible API config.
// Returns nil if model is empty.
func NewEmbeddingFunc(baseURL, apiKey, model string) chromem.EmbeddingFunc {
	if model == "" || baseURL == "" {
		return nil
	}
	return chromem.NewEmbeddingFuncOpenAICompat(baseURL, apiKey, model, nil)
}

func (s *ArchivalService) collectionName(tenantID int64) string {
	return fmt.Sprintf("archival_%d", tenantID)
}

func (s *ArchivalService) getOrCreateCollection(tenantID int64) (*chromem.Collection, error) {
	name := s.collectionName(tenantID)
	return s.db.GetOrCreateCollection(name, nil, s.embeddingFunc)
}

// Insert stores a new archival memory entry. Embedding is computed automatically by chromem-go.
func (s *ArchivalService) Insert(ctx context.Context, tenantID int64, content string) (string, error) {
	if s.embeddingFunc == nil {
		return "", fmt.Errorf("archival insert requires embedding configuration (set LLM_EMBEDDING_MODEL)")
	}

	coll, err := s.getOrCreateCollection(tenantID)
	if err != nil {
		return "", fmt.Errorf("get collection: %w", err)
	}

	id := uuid.New().String()
	now := time.Now()

	err = coll.AddDocument(ctx, chromem.Document{
		ID:      id,
		Content: content,
		Metadata: map[string]string{
			"created_at": now.Format(time.RFC3339),
		},
	})
	if err != nil {
		return "", fmt.Errorf("add document: %w", err)
	}

	log.WithFields(log.Fields{
		"tenant_id": tenantID,
		"id":        id,
		"length":    len(content),
	}).Debug("Archival memory inserted (chromem-go)")

	return id, nil
}

// Search performs semantic similarity search over archival entries for a tenant.
func (s *ArchivalService) Search(ctx context.Context, tenantID int64, query string, limit int) ([]ArchivalEntry, error) {
	if s.embeddingFunc == nil {
		return nil, fmt.Errorf("archival search requires embedding configuration (set LLM_EMBEDDING_MODEL)")
	}
	if limit <= 0 {
		limit = 5
	}

	coll, err := s.getOrCreateCollection(tenantID)
	if err != nil {
		return nil, fmt.Errorf("get collection: %w", err)
	}

	count := coll.Count()
	if count == 0 {
		return nil, nil
	}
	if limit > count {
		limit = count
	}

	results, err := coll.Query(ctx, query, limit, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("query archival: %w", err)
	}

	entries := make([]ArchivalEntry, len(results))
	for i, r := range results {
		createdAt, _ := time.Parse(time.RFC3339, r.Metadata["created_at"])
		entries[i] = ArchivalEntry{
			ID:         r.ID,
			TenantID:   tenantID,
			Content:    r.Content,
			CreatedAt:  createdAt,
			Similarity: r.Similarity,
		}
	}
	return entries, nil
}

// SearchText performs FTS5 full-text search over conversation history (recall memory).
// Delegates to the injected RecallFunc (backed by SQLite FTS5).
func (s *ArchivalService) SearchText(tenantID int64, query string, limit int) ([]string, error) {
	if s.recallFn == nil {
		return nil, nil
	}
	return s.recallFn(tenantID, query, limit)
}

// Delete removes an archival memory entry by ID.
func (s *ArchivalService) Delete(ctx context.Context, tenantID int64, entryID string) error {
	coll, err := s.getOrCreateCollection(tenantID)
	if err != nil {
		return fmt.Errorf("get collection: %w", err)
	}
	return coll.Delete(ctx, nil, nil, entryID)
}

// Count returns the number of archival memory entries for a tenant.
func (s *ArchivalService) Count(tenantID int64) (int, error) {
	name := s.collectionName(tenantID)
	coll := s.db.GetCollection(name, s.embeddingFunc)
	if coll == nil {
		return 0, nil
	}
	return coll.Count(), nil
}
