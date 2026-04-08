package llm

import (
	"context"
	"fmt"
	"strings"
)

// CollectStream collects all events from a stream channel and assembles them into a single LLMResponse.
// It handles content, reasoning content, tool calls (accumulating deltas by index), usage, and finish reason.
// Returns an error if the stream emits an EventError or if ctx is cancelled during collection.
func CollectStream(ctx context.Context, eventCh <-chan StreamEvent) (*LLMResponse, error) {
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
		case EventReasoningContent:
			reasoningContent.WriteString(ev.ReasoningContent)
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
				return nil, fmt.Errorf("stream error: %s", ev.Error)
			}
		}
	}

	resp.Content = content.String()
	resp.ReasoningContent = reasoningContent.String()

	// Convert map to ordered slice by index
	if len(toolCalls) > 0 {
		maxIdx := -1
		for idx := range toolCalls {
			if idx > maxIdx {
				maxIdx = idx
			}
		}
		resp.ToolCalls = make([]ToolCall, 0, len(toolCalls))
		for i := 0; i <= maxIdx; i++ {
			tc, ok := toolCalls[i]
			if !ok {
				continue
			}
			resp.ToolCalls = append(resp.ToolCalls, ToolCall{
				ID:        tc.ID,
				Name:      tc.Name,
				Arguments: tc.Arguments,
			})
		}
	}

	// Infer finish_reason from actual response data.
	// Some providers send "stop" instead of "tool_calls" even when tool_calls are present.
	if resp.FinishReason == "" && len(resp.ToolCalls) > 0 {
		resp.FinishReason = FinishReasonToolCalls
	}

	return &resp, nil
}
