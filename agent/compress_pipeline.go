package agent

import (
	"context"
	"time"

	"xbot/llm"
	log "xbot/logger"
)

// CompressPipelineParams holds the inputs for a compression pipeline execution.
type CompressPipelineParams struct {
	// CM is the context manager that performs the compression.
	CM ContextManager
	// Messages are the current conversation messages.
	Messages []llm.ChatMessage
	// LLMClient is the LLM client for compression calls.
	LLMClient llm.LLM
	// Model is the model name for token counting.
	Model string
	// UseManual selects ManualCompress (true) or Compress (false).
	UseManual bool
	// TokenTracker receives the ResetAfterCompress call.
	TokenTracker *TokenTracker
	// Persistence receives the RewriteAfterCompress call.
	Persistence *PersistenceBridge
	// OffloadStore is cleaned after compression (nil = skip).
	OffloadStore *OffloadStore
	// OffloadSessionKey is the key for offload store cleaning.
	OffloadSessionKey string
	// MaskStore is cleaned after compression (nil = skip).
	MaskStore *ObservationMaskStore
	// AccumulateUsage is called with the compress result to add to local metrics.
	AccumulateUsage func(*CompressResult)
	// SyncMessages syncs the ContextEditor reference when messages change.
	SyncMessages func([]llm.ChatMessage) []llm.ChatMessage
}

// CompressPipelineResult holds the outputs of a compression pipeline execution.
type CompressPipelineResult struct {
	// NewMessages is the compressed message slice (after SyncMessages).
	NewMessages []llm.ChatMessage
	// NewTokenCount is the estimated token count of the compressed messages.
	NewTokenCount int64
	// CompressOutput is the raw result from ContextManager.Compress/ManualCompress.
	CompressOutput *CompressResult
}

// ApplyCompress executes the common compress→persist→cleanup pipeline.
// It:
//  1. Calls CM.Compress or CM.ManualCompress
//  2. Accumulates usage metrics
//  3. Updates messages via SyncMessages
//  4. Estimates new token count and resets TokenTracker
//  5. Persists via Persistence.RewriteAfterCompress
//  6. Cleans OffloadStore and MaskStore entries
//
// Returns the pipeline result or the compression error.
// Persistence failures are logged but do not cause an error return.
func ApplyCompress(ctx context.Context, params CompressPipelineParams) (*CompressPipelineResult, error) {
	var result *CompressResult
	var err error

	if params.UseManual {
		result, err = params.CM.ManualCompress(ctx, params.Messages, params.LLMClient, params.Model)
	} else {
		result, err = params.CM.Compress(ctx, params.Messages, params.LLMClient, params.Model)
	}
	if err != nil {
		return nil, err
	}

	if params.AccumulateUsage != nil {
		params.AccumulateUsage(result)
	}

	newMessages := result.LLMView
	if params.SyncMessages != nil {
		newMessages = params.SyncMessages(result.LLMView)
	}

	newTokenCount, _ := llm.CountMessagesTokens(result.LLMView, params.Model)

	if params.TokenTracker != nil {
		params.TokenTracker.ResetAfterCompress(int64(newTokenCount), len(newMessages))
	}

	if params.Persistence != nil {
		if ok, _ := params.Persistence.RewriteAfterCompress(result.SessionView, len(newMessages)); !ok {
			log.Ctx(ctx).Warn("Compression persistence failed, session may be inconsistent")
		}
	}

	// Clean offload and mask entries that were compressed away.
	compressCutoff := time.Now()
	if params.OffloadStore != nil {
		params.OffloadStore.CleanOldEntries(params.OffloadSessionKey, compressCutoff)
	}
	if params.MaskStore != nil {
		params.MaskStore.CleanOldEntries(compressCutoff)
	}

	return &CompressPipelineResult{
		NewMessages:    newMessages,
		NewTokenCount:  int64(newTokenCount),
		CompressOutput: result,
	}, nil
}
