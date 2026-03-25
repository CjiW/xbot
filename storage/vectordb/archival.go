package vectordb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	chromem "github.com/philippgille/chromem-go"

	"xbot/llm"
	log "xbot/logger"
	"xbot/memory"
)

// ContentCompressor compresses content that exceeds token limits.
// Returns compressed content or error. Used when embedding content exceeds model token limit.
type ContentCompressor func(ctx context.Context, content string, maxTokens int) (string, error)

// ContentCompressorFunc is a type alias for content compression functions.
type ContentCompressorFunc func(ctx context.Context, content string, maxTokens int) (string, error)

// llmContentCompressor is a package-level LLM-based content compressor.
// It is initialized on startup if an LLM client is available.
var llmContentCompressor ContentCompressorFunc

// SetLLMContentCompressor sets the package-level LLM-based content compressor.
// This should be called during application startup if an LLM client is available,
// so that ensureContentFits can prefer semantic compression over naive truncation.
func SetLLMContentCompressor(c ContentCompressorFunc) {
	llmContentCompressor = c
}

// DefaultContentCompressor is a no-op compressor that just truncates.
// Used when no LLM is available for compression.
func DefaultContentCompressor(ctx context.Context, content string, maxTokens int) (string, error) {
	// Rough truncation: ~4 chars per token
	maxChars := maxTokens * 4
	if len([]rune(content)) <= maxChars {
		return content, nil
	}
	return string([]rune(content)[:maxChars]), nil
}

// LLMContentCompressor creates a compressor using LLM to summarize content.
// The compressor preserves key information while fitting within token limits.
// Callers (ensureContentFits) already verify tokens exceed the limit via tiktoken,
// so this function skips redundant estimation and always compresses.
func LLMContentCompressor(llmClient llm.LLM, model string) ContentCompressor {
	return func(ctx context.Context, content string, maxTokens int) (string, error) {
		prompt := fmt.Sprintf(`Summarize the following content for semantic search embedding. 
Keep ALL important information (names, dates, facts, decisions, technical details).
Target length: under %d tokens.

Content:
%s

Output the summarized content directly, no explanations.`, maxTokens, content)

		resp, err := llmClient.Generate(ctx, model, []llm.ChatMessage{
			llm.NewSystemMessage("You are a content compressor. Summarize content for embedding while preserving all important information."),
			llm.NewUserMessage(prompt),
		}, nil, "")
		if err != nil {
			return "", fmt.Errorf("LLM compression failed: %w", err)
		}

		compressed := llm.StripThinkBlocks(resp.Content)
		log.WithFields(log.Fields{
			"original_len":   len(content),
			"compressed_len": len(compressed),
			"target_tokens":  maxTokens,
		}).Info("Content compressed for embedding")

		return compressed, nil
	}
}

// embeddingLimitConfig holds shared configuration for token limit enforcement
// used by both ArchivalService and ToolIndexService.
type embeddingLimitConfig struct {
	compressor ContentCompressor
	maxTokens  int
	tokenModel string
}

func defaultEmbeddingLimitConfig() embeddingLimitConfig {
	return embeddingLimitConfig{
		compressor: DefaultContentCompressor,
		maxTokens:  2048,
		tokenModel: "gpt-4",
	}
}

// ensureContentFits checks token count and compresses content if it exceeds the limit.
// Uses accurate token counting via tiktoken, and the configured compressor if needed.
func ensureContentFits(ctx context.Context, cfg embeddingLimitConfig, content string, contextHint string) (string, error) {
	tokenCount, err := llm.CountTokens(content, cfg.tokenModel)
	if err != nil {
		log.WithError(err).Warn("Failed to count tokens, using rough estimate")
		tokenCount = len(content) / 4
	}

	if tokenCount <= cfg.maxTokens {
		return content, nil
	}

	log.WithFields(log.Fields{
		"context":      contextHint,
		"original_len": len(content),
		"token_count":  tokenCount,
		"max_tokens":   cfg.maxTokens,
	}).Warn("Content exceeds embedding model token limit, compressing")

	// 优先使用 LLM Content Compressor 做语义压缩
	if llmContentCompressor != nil {
		compressed, err := llmContentCompressor(ctx, content, cfg.maxTokens)
		if err == nil {
			return compressed, nil
		}
		// LLM compressor 失败则 fallback 到暴力截断（尽量保留一些内容）
		log.Warnf("LLM content compression failed, falling back to default: %v", err)
	}

	compressed, err := cfg.compressor(ctx, content, cfg.maxTokens)
	if err != nil {
		return "", fmt.Errorf("compress content: %w", err)
	}

	return compressed, nil
}

// EmbeddingLimitOption configures embedding token limit behavior for both ArchivalService and ToolIndexService.
type EmbeddingLimitOption func(*embeddingLimitConfig)

// WithCompressor sets the content compressor.
func WithCompressor(compressor ContentCompressor) EmbeddingLimitOption {
	return func(c *embeddingLimitConfig) {
		c.compressor = compressor
	}
}

// WithMaxTokens sets the maximum tokens for the embedding model.
func WithMaxTokens(maxTokens int) EmbeddingLimitOption {
	return func(c *embeddingLimitConfig) {
		c.maxTokens = maxTokens
	}
}

// WithTokenModel sets the model name for token counting.
func WithTokenModel(model string) EmbeddingLimitOption {
	return func(c *embeddingLimitConfig) {
		c.tokenModel = model
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
	embeddingLimitConfig
}

// NewArchivalService creates an archival service backed by chromem-go.
//
// persistDir: directory for chromem-go file persistence (created if needed).
// embeddingFunc: OpenAI-compatible embedding function (nil disables vector search).
// options: optional configuration (compressor, maxTokens, tokenModel).
func NewArchivalService(persistDir string, embeddingFunc chromem.EmbeddingFunc, options ...EmbeddingLimitOption) (*ArchivalService, error) {
	db, err := chromem.NewPersistentDB(persistDir, false)
	if err != nil {
		return nil, fmt.Errorf("create chromem-go DB at %s: %w", persistDir, err)
	}

	cfg := defaultEmbeddingLimitConfig()
	for _, opt := range options {
		opt(&cfg)
	}

	s := &ArchivalService{
		db:                   db,
		embeddingFunc:        embeddingFunc,
		embeddingLimitConfig: cfg,
	}

	log.WithFields(log.Fields{
		"persist_dir":    persistDir,
		"embedding_func": embeddingFunc != nil,
		"max_tokens":     s.maxTokens,
		"compressor":     s.compressor != nil,
	}).Info("Archival memory (chromem-go) initialized")

	return s, nil
}

// NewEmbeddingFunc creates a chromem-go EmbeddingFunc.
// When provider is "ollama" (or auto-detected from URL port :11434 for backward compat),
// uses the native /api/embed endpoint with options.num_ctx.
// Otherwise uses the OpenAI-compatible endpoint.
// Returns nil if model is empty.
func NewEmbeddingFunc(baseURL, apiKey, model, provider string, maxTokens int) chromem.EmbeddingFunc {
	if model == "" || baseURL == "" {
		return nil
	}
	useOllama := provider == "ollama" || (provider == "" && isOllamaURL(baseURL))
	if maxTokens > 0 && useOllama {
		ollamaBase := toOllamaBaseURL(baseURL)
		log.WithFields(log.Fields{
			"base_url": ollamaBase,
			"model":    model,
			"num_ctx":  maxTokens,
		}).Info("Using Ollama native embedding API with num_ctx")
		return newOllamaEmbedFunc(ollamaBase, model, maxTokens)
	}
	return chromem.NewEmbeddingFuncOpenAICompat(baseURL, apiKey, model, nil)
}

func isOllamaURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	// Check default Ollama port
	if u.Port() == "11434" {
		return true
	}
	// Check default Ollama host without explicit port (e.g. http://localhost)
	if u.Host == "localhost" || u.Host == "127.0.0.1" || u.Host == "::1" {
		return u.Port() == "" || u.Port() == "11434"
	}
	return false
}

func toOllamaBaseURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	u.Path = ""
	u.RawQuery = ""
	return u.String()
}

// newOllamaEmbedFunc returns an EmbeddingFunc using Ollama's native /api/embed
// with explicit num_ctx so the model isn't loaded with an oversized context.
func newOllamaEmbedFunc(baseURL, model string, numCtx int) chromem.EmbeddingFunc {
	client := &http.Client{}

	var checkedNormalized bool
	// checkedNormalized 是闭包变量，在每个 embedding 调用后被读取。
	// sync.Once.Do 保证首次写入 happens-before 后续所有读取，因此无需额外同步。
	checkNormalized := sync.Once{}

	return func(ctx context.Context, text string) ([]float32, error) {
		reqBody, err := json.Marshal(map[string]any{
			"model": model,
			"input": text,
			"options": map[string]any{
				"num_ctx": numCtx,
			},
		})
		if err != nil {
			return nil, fmt.Errorf("couldn't marshal request body: %w", err)
		}

		req, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/api/embed", bytes.NewBuffer(reqBody))
		if err != nil {
			return nil, fmt.Errorf("couldn't create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("couldn't send request: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			return nil, fmt.Errorf("embedding API error %s: %s", resp.Status, string(body))
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("couldn't read response body: %w", err)
		}

		var result struct {
			Embeddings [][]float32 `json:"embeddings"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, fmt.Errorf("couldn't unmarshal response body: %w", err)
		}
		if len(result.Embeddings) == 0 || len(result.Embeddings[0]) == 0 {
			return nil, errors.New("no embeddings found in the response")
		}

		v := result.Embeddings[0]
		checkNormalized.Do(func() {
			checkedNormalized = isNormalized(v)
		})
		if !checkedNormalized {
			v = normalizeVector(v)
		}
		return v, nil
	}
}

func isNormalized(v []float32) bool {
	var sqSum float64
	for _, val := range v {
		sqSum += float64(val) * float64(val)
	}
	return math.Abs(sqSum-1) < 1e-3
}

func normalizeVector(v []float32) []float32 {
	var sqSum float64
	for _, val := range v {
		sqSum += float64(val) * float64(val)
	}
	norm := math.Sqrt(sqSum)
	if norm == 0 {
		return v
	}
	out := make([]float32, len(v))
	for i, val := range v {
		out[i] = float32(float64(val) / norm)
	}
	return out
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

	content, err = ensureContentFits(ctx, s.embeddingLimitConfig, content, "archival")
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

	query, err = ensureContentFits(ctx, s.embeddingLimitConfig, query, "archival-search")
	if err != nil {
		return nil, fmt.Errorf("fit query: %w", err)
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

// SearchByDocumentContains searches archival entries where document content
// contains the specified substring, using chromem-go's whereDocument filter.
// This is useful for finding entries with specific markers like [PROJECT_CARD].
//
// Parameters:
//   - ctx: context for cancellation
//   - tenantID: the tenant whose archival entries to search
//   - contains: the substring to match against document content (passed as both
//     the query text and the $contains filter to leverage chromem-go's hybrid matching)
//   - limit: maximum number of results to return (<= 0 defaults to 3)
func (s *ArchivalService) SearchByDocumentContains(ctx context.Context, tenantID int64, contains string, limit int) ([]ArchivalEntry, error) {
	if s.embeddingFunc == nil {
		return nil, nil // no embedding = no archival service, skip silently
	}
	if limit <= 0 {
		limit = 3
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

	contains, err = ensureContentFits(ctx, s.embeddingLimitConfig, contains, "archival-contains-search")
	if err != nil {
		return nil, fmt.Errorf("fit query: %w", err)
	}

	// chromem-go whereDocument: $contains does substring matching on document content
	results, err := coll.Query(ctx, contains, limit, nil, map[string]string{
		"$contains": contains,
	})
	if err != nil {
		return nil, fmt.Errorf("query by document contains: %w", err)
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
	persistDir    string
	fpMu          sync.Mutex
	embeddingLimitConfig

	// fingerprints cache: in-memory copy with debounced writes
	fpCache      map[string]string // in-memory fingerprint data
	fpDirty      bool              // true if cache has unsaved changes
	fpLoadOnce   sync.Once         // ensure fingerprints are loaded once
	fpFlushTimer *time.Timer       // debounced flush timer
}

// NewToolIndexService creates a tool index service.
func NewToolIndexService(persistDir string, embeddingFunc chromem.EmbeddingFunc, options ...EmbeddingLimitOption) (*ToolIndexService, error) {
	db, err := chromem.NewPersistentDB(persistDir, false)
	if err != nil {
		return nil, fmt.Errorf("create chromem-go DB at %s: %w", persistDir, err)
	}

	cfg := defaultEmbeddingLimitConfig()
	for _, opt := range options {
		opt(&cfg)
	}

	return &ToolIndexService{
		db:                   db,
		embeddingFunc:        embeddingFunc,
		persistDir:           persistDir,
		embeddingLimitConfig: cfg,
	}, nil
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
	content, err = ensureContentFits(ctx, s.embeddingLimitConfig, content, toolID)
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
	query, err = ensureContentFits(ctx, s.embeddingLimitConfig, query, "tool-search")
	if err != nil {
		return nil, fmt.Errorf("fit query: %w", err)
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
		content, err = ensureContentFits(ctx, s.embeddingLimitConfig, content, toolID)
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

func (s *ToolIndexService) fingerprintPath() string {
	return filepath.Join(s.persistDir, "fingerprints.json")
}

func (s *ToolIndexService) loadFingerprints() map[string]string {
	data, err := os.ReadFile(s.fingerprintPath())
	if err != nil {
		return nil
	}
	var fps map[string]string
	if err := json.Unmarshal(data, &fps); err != nil {
		return nil
	}
	return fps
}

func (s *ToolIndexService) saveFingerprints(fps map[string]string) error {
	out, err := json.Marshal(fps)
	if err != nil {
		return err
	}
	return os.WriteFile(s.fingerprintPath(), out, 0644)
}

// GetFingerprint returns the persisted catalog fingerprint for a tenant.
func (s *ToolIndexService) GetFingerprint(tenantID int64) string {
	s.fpMu.Lock()
	defer s.fpMu.Unlock()
	s.fpLoadOnce.Do(s.initFPCache)
	return s.fpCache[fmt.Sprintf("%d", tenantID)]
}

// SetFingerprint persists the catalog fingerprint for a tenant.
func (s *ToolIndexService) SetFingerprint(tenantID int64, fp string) {
	s.fpMu.Lock()
	defer s.fpMu.Unlock()
	s.fpLoadOnce.Do(s.initFPCache)
	s.fpCache[fmt.Sprintf("%d", tenantID)] = fp
	s.fpDirty = true
	s.scheduleFPFlush()
}

// DeleteFingerprint removes the persisted catalog fingerprint for a tenant.
func (s *ToolIndexService) DeleteFingerprint(tenantID int64) {
	s.fpMu.Lock()
	defer s.fpMu.Unlock()
	s.fpLoadOnce.Do(s.initFPCache)
	delete(s.fpCache, fmt.Sprintf("%d", tenantID))
	s.fpDirty = true
	s.scheduleFPFlush()
}

// initFPCache lazily loads fingerprints from disk on first access.
func (s *ToolIndexService) initFPCache() {
	s.fpCache = s.loadFingerprints()
	if s.fpCache == nil {
		s.fpCache = make(map[string]string)
	}
	s.fpDirty = false
}

// scheduleFPFlush debounces fingerprint writes to disk.
// Writes are delayed by 1 second; if another write comes in, the timer resets.
func (s *ToolIndexService) scheduleFPFlush() {
	if s.fpFlushTimer != nil {
		s.fpFlushTimer.Stop()
	}
	s.fpFlushTimer = time.AfterFunc(1*time.Second, func() {
		s.flushFingerprints()
	})
}

// flushFingerprints writes the in-memory cache to disk.
func (s *ToolIndexService) flushFingerprints() {
	s.fpMu.Lock()
	if !s.fpDirty {
		s.fpMu.Unlock()
		return
	}
	fps := make(map[string]string, len(s.fpCache))
	for k, v := range s.fpCache {
		fps[k] = v
	}
	s.fpDirty = false
	s.fpMu.Unlock()

	if err := s.saveFingerprints(fps); err != nil {
		log.WithError(err).Warn("Failed to persist tool index fingerprints")
	}
}
