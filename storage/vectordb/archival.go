package vectordb

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"
	chromem "github.com/philippgille/chromem-go"

	log "xbot/logger"
	"xbot/llm"
	"xbot/memory"
)

// ContentCompressor compresses content that exceeds token limits.
// Returns compressed content or error. Used when embedding content exceeds model token limit.
type ContentCompressor func(ctx context.Context, content string, maxTokens int) (string, error)

// DefaultContentCompressor is a no-op compressor that just truncates.
// Used when no LLM is available for compression.
func DefaultContentCompressor(ctx context.Context, content string, maxTokens int) (string, error) {
	// Rough truncation: ~4 chars per token
	maxChars := maxTokens * 4
	if len(content) <= maxChars {
		return content, nil
	}
	return content[:maxChars], nil
}

// LLMContentCompressor creates a compressor using LLM to summarize content.
// The compressor preserves key information while fitting within token limits.
func LLMContentCompressor(llmClient llm.LLM, model string) ContentCompressor {
	return func(ctx context.Context, content string, maxTokens int) (string, error) {
		// Rough estimate of current tokens
		currentTokens := len(content) / 4
		if currentTokens <= maxTokens {
			return content, nil
		}

		prompt := fmt.Sprintf(`Summarize the following content for semantic search embedding. 
Keep ALL important information (names, dates, facts, decisions, technical details).
Target length: under %d tokens (currently ~%d tokens).

Content:
%s

Output the summarized content directly, no explanations.`, maxTokens, currentTokens, content)

		resp, err := llmClient.Generate(ctx, model, []llm.ChatMessage{
			llm.NewSystemMessage("You are a content compressor. Summarize content for embedding while preserving all important information."),
			llm.NewUserMessage(prompt),
		}, nil)
		if err != nil {
			return "", fmt.Errorf("LLM compression failed: %w", err)
		}

		compressed := llm.StripThinkBlocks(resp.Content)
		log.WithFields(log.Fields{
			"original_len":    len(content),
			"compressed_len":  len(compressed),
			"original_tokens": currentTokens,
			"target_tokens":   maxTokens,
		}).Info("Content compressed for embedding")

		return compressed, nil
	}
}

// ArchivalEntry represents a single archival memory search result.
type ArchivalEntry struct {
	ID         string
	TenantID   int64
	Content    string
	CreatedAt  time.Time
	Similarity float32
}

// ArchivalService stores long-term archival memory entries in chromem-go,
// a pure-Go embedded vector database with file-based persistence.
type ArchivalService struct {
	db            *chromem.DB
	embeddingFunc chromem.EmbeddingFunc
	compressor    ContentCompressor // Compresses content exceeding token limits
	maxTokens     int               // Maximum tokens for embedding model (default 2048)
	tokenModel    string            // Model name for token counting
}

// ArchivalServiceOption configures the archival service.
type ArchivalServiceOption func(*ArchivalService)

// WithCompressor sets the content compressor for the archival service.
func WithCompressor(compressor ContentCompressor) ArchivalServiceOption {
	return func(s *ArchivalService) {
		s.compressor = compressor
	}
}

// WithMaxTokens sets the maximum tokens for the embedding model.
func WithMaxTokens(maxTokens int) ArchivalServiceOption {
	return func(s *ArchivalService) {
		s.maxTokens = maxTokens
	}
}

// WithTokenModel sets the model name for token counting.
func WithTokenModel(model string) ArchivalServiceOption {
	return func(s *ArchivalService) {
		s.tokenModel = model
	}
}

// NewArchivalService creates an archival service backed by chromem-go.
//
// persistDir: directory for chromem-go file persistence (created if needed).
// embeddingFunc: OpenAI-compatible embedding function (nil disables vector search).
// options: optional configuration (compressor, maxTokens, tokenModel).
func NewArchivalService(persistDir string, embeddingFunc chromem.EmbeddingFunc, options ...ArchivalServiceOption) (*ArchivalService, error) {
	db, err := chromem.NewPersistentDB(persistDir, false)
	if err != nil {
		return nil, fmt.Errorf("create chromem-go DB at %s: %w", persistDir, err)
	}

	s := &ArchivalService{
		db:            db,
		embeddingFunc: embeddingFunc,
		compressor:    DefaultContentCompressor,
		maxTokens:     2048, // Default for most embedding models
		tokenModel:    "gpt-4",
	}

	for _, opt := range options {
		opt(s)
	}

	log.WithFields(log.Fields{
		"persist_dir":    persistDir,
		"embedding_func": embeddingFunc != nil,
		"max_tokens":     s.maxTokens,
		"compressor":     s.compressor != nil,
	}).Info("Archival memory (chromem-go) initialized")

	return s, nil
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
// If ts is non-zero it is recorded as the information timestamp (e.g. conversation time);
// otherwise the current wall-clock time is used.
// If content exceeds embedding model token limit, it is compressed using the configured compressor.
func (s *ArchivalService) Insert(ctx context.Context, tenantID int64, content string, ts time.Time) (string, error) {
	if s.embeddingFunc == nil {
		return "", fmt.Errorf("archival insert requires embedding configuration (set LLM_EMBEDDING_MODEL)")
	}

	coll, err := s.getOrCreateCollection(tenantID)
	if err != nil {
		return "", fmt.Errorf("get collection: %w", err)
	}

	// Check token count and compress if needed
	content, err = s.ensureContentFits(ctx, content, "archival")
	if err != nil {
		return "", fmt.Errorf("ensure content fits: %w", err)
	}

	id := uuid.New().String()
	if ts.IsZero() {
		ts = time.Now()
	}

	err = coll.AddDocument(ctx, chromem.Document{
		ID:      id,
		Content: content,
		Metadata: map[string]string{
			"created_at": ts.Format(time.RFC3339),
		},
	})
	if err != nil {
		return "", fmt.Errorf("add document: %w", err)
	}

	log.WithFields(log.Fields{
		"tenant_id":  tenantID,
		"id":         id,
		"length":     len(content),
		"created_at": ts.Format(time.RFC3339),
	}).Debug("Archival memory inserted (chromem-go)")

	return id, nil
}

// ensureContentFits checks token count and compresses content if it exceeds the limit.
// Uses accurate token counting via tiktoken, and LLM compression if configured.
func (s *ArchivalService) ensureContentFits(ctx context.Context, content string, contextHint string) (string, error) {
	// Count tokens accurately
	tokenCount, err := llm.CountTokens(content, s.tokenModel)
	if err != nil {
		log.WithError(err).Warn("Failed to count tokens, using rough estimate")
		tokenCount = len(content) / 4 // Fallback to rough estimate
	}

	if tokenCount <= s.maxTokens {
		return content, nil
	}

	log.WithFields(log.Fields{
		"context":       contextHint,
		"original_len":  len(content),
		"token_count":   tokenCount,
		"max_tokens":    s.maxTokens,
	}).Warn("Content exceeds embedding model token limit, compressing")

	// Compress using configured compressor (LLM or default truncation)
	compressed, err := s.compressor(ctx, content, s.maxTokens)
	if err != nil {
		return "", fmt.Errorf("compress content: %w", err)
	}

	return compressed, nil
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

// ToolIndexService provides tool indexing using a separate collection.
type ToolIndexService struct {
	db            *chromem.DB
	embeddingFunc chromem.EmbeddingFunc
	compressor    ContentCompressor // Compresses content exceeding token limits
	maxTokens     int               // Maximum tokens for embedding model (default 2048)
	tokenModel    string            // Model name for token counting
}

// ToolIndexServiceOption configures the tool index service.
type ToolIndexServiceOption func(*ToolIndexService)

// WithToolCompressor sets the content compressor for the tool index service.
func WithToolCompressor(compressor ContentCompressor) ToolIndexServiceOption {
	return func(s *ToolIndexService) {
		s.compressor = compressor
	}
}

// WithToolMaxTokens sets the maximum tokens for the embedding model.
func WithToolMaxTokens(maxTokens int) ToolIndexServiceOption {
	return func(s *ToolIndexService) {
		s.maxTokens = maxTokens
	}
}

// WithToolTokenModel sets the model name for token counting.
func WithToolTokenModel(model string) ToolIndexServiceOption {
	return func(s *ToolIndexService) {
		s.tokenModel = model
	}
}

// NewToolIndexService creates a tool index service.
func NewToolIndexService(persistDir string, embeddingFunc chromem.EmbeddingFunc, options ...ToolIndexServiceOption) (*ToolIndexService, error) {
	db, err := chromem.NewPersistentDB(persistDir, false)
	if err != nil {
		return nil, fmt.Errorf("create chromem-go DB at %s: %w", persistDir, err)
	}

	s := &ToolIndexService{
		db:            db,
		embeddingFunc: embeddingFunc,
		compressor:    DefaultContentCompressor,
		maxTokens:     2048,
		tokenModel:    "gpt-4",
	}

	for _, opt := range options {
		opt(s)
	}

	return s, nil
}

func (s *ToolIndexService) collectionName(tenantID int64) string {
	return fmt.Sprintf("tools_%d", tenantID)
}

func (s *ToolIndexService) getOrCreateCollection(tenantID int64) (*chromem.Collection, error) {
	name := s.collectionName(tenantID)
	return s.db.GetOrCreateCollection(name, nil, s.embeddingFunc)
}

// InsertTool indexes a tool with its embedding.
func (s *ToolIndexService) InsertTool(ctx context.Context, tenantID int64, toolID, content string) error {
	if s.embeddingFunc == nil {
		return fmt.Errorf("tool index requires embedding configuration")
	}
	coll, err := s.getOrCreateCollection(tenantID)
	if err != nil {
		return fmt.Errorf("get collection: %w", err)
	}
	// Check token count and compress if needed
	content, err = s.ensureContentFits(ctx, content, toolID)
	if err != nil {
		return fmt.Errorf("ensure content fits: %w", err)
	}
	err = coll.AddDocument(ctx, chromem.Document{
		ID:      toolID,
		Content: content,
	})
	if err != nil {
		return fmt.Errorf("add document: %w", err)
	}
	return nil
}

// SearchTools searches for tools by semantic similarity.
// Returns ID, Content, Similarity, and Metadata for each result.
func (s *ToolIndexService) SearchTools(ctx context.Context, tenantID int64, query string, limit int) ([]struct {
	ID         string
	Content    string
	Similarity float32
	Metadata   map[string]string
}, error) {
	if s.embeddingFunc == nil {
		return nil, fmt.Errorf("tool search requires embedding configuration")
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
		return nil, fmt.Errorf("query tools: %w", err)
	}
	entries := make([]struct {
		ID         string
		Content    string
		Similarity float32
		Metadata   map[string]string
	}, len(results))
	for i, r := range results {
		entries[i] = struct {
			ID         string
			Content    string
			Similarity float32
			Metadata   map[string]string
		}{
			ID:         r.ID,
			Content:    r.Content,
			Similarity: r.Similarity,
			Metadata:   r.Metadata,
		}
	}
	return entries, nil
}

// DeleteTool removes a tool from the index.
func (s *ToolIndexService) DeleteTool(ctx context.Context, tenantID int64, toolID string) error {
	coll, err := s.getOrCreateCollection(tenantID)
	if err != nil {
		return fmt.Errorf("get collection: %w", err)
	}
	return coll.Delete(ctx, nil, nil, toolID)
}

// ClearTools removes all tools from the index for a tenant.
func (s *ToolIndexService) ClearTools(ctx context.Context, tenantID int64) error {
	name := s.collectionName(tenantID)
	coll := s.db.GetCollection(name, s.embeddingFunc)
	if coll == nil {
		return nil
	}
	// Drop the entire collection
	if err := s.db.DeleteCollection(name); err != nil {
		return fmt.Errorf("drop collection %s: %w", name, err)
	}
	return nil
}

// Use memory.ToolIndexEntry instead of duplicating the definition here.
// This alias is kept for backward compatibility with existing code.
type ToolIndexEntry = memory.ToolIndexEntry

// ensureContentFits checks token count and compresses content if it exceeds the limit.
// Uses accurate token counting via tiktoken, and LLM compression if configured.
func (s *ToolIndexService) ensureContentFits(ctx context.Context, content string, contextHint string) (string, error) {
	// Count tokens accurately
	tokenCount, err := llm.CountTokens(content, s.tokenModel)
	if err != nil {
		log.WithError(err).Warn("Failed to count tokens, using rough estimate")
		tokenCount = len(content) / 4 // Fallback to rough estimate
	}

	if tokenCount <= s.maxTokens {
		return content, nil
	}

	log.WithFields(log.Fields{
		"context":       contextHint,
		"original_len":  len(content),
		"token_count":   tokenCount,
		"max_tokens":    s.maxTokens,
	}).Warn("Content exceeds embedding model token limit, compressing")

	// Compress using configured compressor (LLM or default truncation)
	compressed, err := s.compressor(ctx, content, s.maxTokens)
	if err != nil {
		return "", fmt.Errorf("compress content: %w", err)
	}

	return compressed, nil
}

// IndexTools indexes multiple tools at once using batch concurrent embedding.
// Channels are stored in Metadata (not Content) to avoid affecting embedding similarity.
// If content exceeds embedding model token limit, it is compressed using the configured compressor.
func (s *ToolIndexService) IndexTools(ctx context.Context, tenantID int64, tools []ToolIndexEntry) error {
	if s.embeddingFunc == nil {
		return fmt.Errorf("tool index requires embedding configuration")
	}
	if err := s.ClearTools(ctx, tenantID); err != nil {
		return fmt.Errorf("clear tools: %w", err)
	}
	if len(tools) == 0 {
		return nil
	}
	coll, err := s.getOrCreateCollection(tenantID)
	if err != nil {
		return fmt.Errorf("get collection: %w", err)
	}
	docs := make([]chromem.Document, len(tools))
	for i, tool := range tools {
		toolID := fmt.Sprintf("%s_%s", tool.ServerName, tool.Name)
		// Content is pure semantic content for embedding (no channel info)
		content := fmt.Sprintf("Tool: %s\nServer: %s\nSource: %s\nDescription: %s",
			tool.Name, tool.ServerName, tool.Source, tool.Description)
		// Check token count and compress if needed
		content, err = s.ensureContentFits(ctx, content, toolID)
		if err != nil {
			return fmt.Errorf("ensure content fits for %s: %w", toolID, err)
		}
		// Metadata stores structured data (channels) for filtering
		metadata := map[string]string{
			"server_name": tool.ServerName,
			"source":      tool.Source,
		}
		if len(tool.Channels) > 0 {
			metadata["channels"] = strings.Join(tool.Channels, ",")
		}
		docs[i] = chromem.Document{
			ID:       toolID,
			Content:  content,
			Metadata: metadata,
		}
	}
	concurrency := runtime.NumCPU()
	if concurrency < 1 {
		concurrency = 1
	}
	if err := coll.AddDocuments(ctx, docs, concurrency); err != nil {
		return fmt.Errorf("add documents: %w", err)
	}
	return nil
}
