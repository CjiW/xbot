package llm

import (
	"context"
	"fmt"
	"strings"
)

// orderedToolCalls converts the map-based tool call accumulation into an
// ordered slice, sorted by stream index.
func orderedToolCalls(toolCalls map[int]*ToolCallDelta) []ToolCall {
	if len(toolCalls) == 0 {
		return nil
	}
	maxIdx := -1
	for idx := range toolCalls {
		if idx > maxIdx {
			maxIdx = idx
		}
	}
	result := make([]ToolCall, 0, len(toolCalls))
	for i := 0; i <= maxIdx; i++ {
		tc, ok := toolCalls[i]
		if !ok {
			continue
		}
		result = append(result, ToolCall{
			ID:        tc.ID,
			Name:      tc.Name,
			Arguments: tc.Arguments,
		})
	}
	return result
}

// safeCallback invokes f with a panic recovery guard and context cancellation check.
func safeCallback(ctx context.Context, f func(string), s string) {
	if f == nil {
		return
	}
	func() {
		defer func() { recover() }()
		select {
		case <-ctx.Done():
			return
		default:
		}
		f(s)
	}()
}

// CollectStream collects all events from a stream channel and assembles them into a single LLMResponse.
// It handles content, reasoning content, tool calls (accumulating deltas by index), usage, and finish reason.
// Returns an error if the stream emits an EventError or if ctx is cancelled during collection.
func CollectStream(ctx context.Context, eventCh <-chan StreamEvent) (*LLMResponse, error) {
	return CollectStreamWithCallback(ctx, eventCh, nil, nil)
}

// CollectStreamWithCallback is like CollectStream but calls onContent with the
// accumulated text content after each EventContent delta. Optionally calls
// onReasoning with accumulated reasoning content after each EventReasoningContent
// delta (for real-time thinking/reasoning display). EventError handling
// is identical to CollectStream (returns partial content).
func CollectStreamWithCallback(ctx context.Context, eventCh <-chan StreamEvent, onContent func(content string), onReasoning func(content string)) (*LLMResponse, error) {
	var resp LLMResponse
	var content strings.Builder
	var reasoningContent strings.Builder
	toolCalls := make(map[int]*ToolCallDelta) // index → accumulated delta

	for ev := range eventCh {
		// Check context cancellation between events
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		switch ev.Type {
		case EventContent:
			content.WriteString(ev.Content)
			safeCallback(ctx, onContent, content.String())
		case EventReasoningContent:
			reasoningContent.WriteString(ev.ReasoningContent)
			safeCallback(ctx, onReasoning, reasoningContent.String())
		case EventToolCall:
			if ev.ToolCall == nil {
				continue
			}
			idx := ev.ToolCall.Index
			tc := toolCalls[idx]
			if tc == nil {
				tc = &ToolCallDelta{Index: idx}
				toolCalls[idx] = tc
			}
			if ev.ToolCall.ID != "" {
				tc.ID = ev.ToolCall.ID
			}
			if ev.ToolCall.Name != "" {
				tc.Name = ev.ToolCall.Name
			}
			tc.Arguments += ev.ToolCall.Arguments
		case EventUsage:
			if ev.Usage != nil {
				resp.Usage = *ev.Usage
			}
		case EventDone:
			if ev.FinishReason != "" {
				resp.FinishReason = ev.FinishReason
			}
		case EventError:
			if ev.Error != "" {
				// Return partial content accumulated so far instead of nil,
				// so the engine can display what was received before the stream broke.
				resp.Content = content.String()
				resp.ReasoningContent = reasoningContent.String()
				resp.ToolCalls = orderedToolCalls(toolCalls)
				return &resp, fmt.Errorf("stream error: %s", ev.Error)
			}
		}
	}

	resp.Content = content.String()
	resp.ReasoningContent = reasoningContent.String()
	resp.ToolCalls = orderedToolCalls(toolCalls)

	// Infer finish_reason from actual response data.
	// Some providers send "stop" instead of "tool_calls" even when tool_calls are present.
	if resp.FinishReason == "" && len(resp.ToolCalls) > 0 {
		resp.FinishReason = FinishReasonToolCalls
	}

	return &resp, nil
}
