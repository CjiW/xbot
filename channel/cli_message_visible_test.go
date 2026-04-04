// cli_message_visible_test.go — Unit tests for visibleTurnIndices / visibleMsgGroupIndices
// Covers: Ctrl+K delete grouping by conversation turns

package channel

import (
	"reflect"
	"testing"
)

// mkMsg is a helper to create a cliMessage with only the role field set.
func mkMsg(role string) cliMessage {
	return cliMessage{role: role}
}

// rolesOf extracts role strings from a message slice for debugging.
func rolesOf(msgs []cliMessage) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.role
	}
	return out
}

// ---------------------------------------------------------------------------
// visibleTurnIndices — turn-based delete grouping
// ---------------------------------------------------------------------------

func TestVisibleTurnIndices_SimpleConversation(t *testing.T) {
	// 2 turns: user-assistant, user
	// turns: [0, 2] — 按"1"删最后 1 轮 → cutIdx=2, 保留 [user, assistant]
	msgs := []cliMessage{
		mkMsg("user"),      // 0 — turn 0
		mkMsg("assistant"), // 1
		mkMsg("user"),      // 2 — turn 1
	}
	got := visibleTurnIndices(msgs)
	want := []int{0, 2}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestVisibleTurnIndices_WithToolSummary(t *testing.T) {
	// 2 turns with tool_summaries attached to their turns
	// turns: [0, 4] — 按"1"删最后 1 轮 → cutIdx=4, 保留 [user, assistant, tool_summary, assistant]
	msgs := []cliMessage{
		mkMsg("user"),         // 0 — turn 0
		mkMsg("assistant"),    // 1
		mkMsg("tool_summary"), // 2
		mkMsg("assistant"),    // 3
		mkMsg("user"),         // 4 — turn 1
	}
	got := visibleTurnIndices(msgs)
	want := []int{0, 4}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestVisibleTurnIndices_NoOrphanOnDelete(t *testing.T) {
	// Critical: deleting a turn must not leave orphaned tool_summaries
	// 按"1"删最后 1 轮 → cutIdx = turn[1] = 4
	// remaining: [user, assistant, tool_summary, assistant] — no orphaned tool_summary at tail
	msgs := []cliMessage{
		mkMsg("user"),         // 0
		mkMsg("assistant"),    // 1
		mkMsg("tool_summary"), // 2
		mkMsg("assistant"),    // 3
		mkMsg("user"),         // 4
		mkMsg("assistant"),    // 5
	}
	turns := visibleTurnIndices(msgs)
	confirmDelete := 1
	cutIdx := turns[len(turns)-confirmDelete]
	remaining := msgs[:cutIdx]

	// No trailing orphaned tool_summary
	if len(remaining) > 0 && remaining[len(remaining)-1].role == "tool_summary" {
		t.Errorf("trailing orphan tool_summary after delete (cutIdx=%d, remaining: %v)", cutIdx, rolesOf(remaining))
	}
	// Verify we kept exactly the first turn (indices 0-3)
	if len(remaining) != 4 {
		t.Errorf("expected 4 remaining messages, got %d (roles: %v)", len(remaining), rolesOf(remaining))
	}
}

func TestVisibleTurnIndices_MultipleToolSummaries(t *testing.T) {
	// Multiple tool_summaries within a turn — all belong to that turn
	// 按"1"删最后 1 轮 → cutIdx=5, 保留前 5 条（turn 0 全部内容）
	msgs := []cliMessage{
		mkMsg("user"),         // 0 — turn 0
		mkMsg("assistant"),    // 1
		mkMsg("tool_summary"), // 2
		mkMsg("assistant"),    // 3
		mkMsg("tool_summary"), // 4
		mkMsg("user"),         // 5 — turn 1
	}
	got := visibleTurnIndices(msgs)
	want := []int{0, 5}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestVisibleTurnIndices_DeleteMultipleTurns(t *testing.T) {
	// 3 turns, delete last 2 → keep first turn only
	msgs := []cliMessage{
		mkMsg("user"),      // 0 — turn 0
		mkMsg("assistant"), // 1
		mkMsg("user"),      // 2 — turn 1
		mkMsg("assistant"), // 3
		mkMsg("user"),      // 4 — turn 2
		mkMsg("assistant"), // 5
	}
	turns := visibleTurnIndices(msgs)
	// 按"2"删最后 2 轮 → cutIdx = turns[3-2] = turns[1] = 2
	confirmDelete := 2
	cutIdx := turns[len(turns)-confirmDelete]
	remaining := msgs[:cutIdx]
	if len(remaining) != 2 {
		t.Errorf("expected 2 remaining, got %d (roles: %v)", len(remaining), rolesOf(remaining))
	}
}

func TestVisibleTurnIndices_LeadingToolSummary(t *testing.T) {
	// Edge: tool_summary before first user — not a turn boundary
	msgs := []cliMessage{
		mkMsg("tool_summary"), // 0
		mkMsg("user"),         // 1 — turn 0
		mkMsg("assistant"),    // 2
	}
	got := visibleTurnIndices(msgs)
	want := []int{1}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestVisibleTurnIndices_Empty(t *testing.T) {
	got := visibleTurnIndices(nil)
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
	got = visibleTurnIndices([]cliMessage{})
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

func TestVisibleTurnIndices_NoUserMessages(t *testing.T) {
	// Edge: only non-user messages — fallback to index 0
	msgs := []cliMessage{
		mkMsg("assistant"),
		mkMsg("tool_summary"),
	}
	got := visibleTurnIndices(msgs)
	want := []int{0}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestVisibleTurnIndices_OnlyToolSummaries(t *testing.T) {
	// Edge: only tool_summaries — fallback to index 0
	msgs := []cliMessage{
		mkMsg("tool_summary"),
		mkMsg("tool_summary"),
	}
	got := visibleTurnIndices(msgs)
	want := []int{0}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestVisibleTurnIndices_SystemMessages(t *testing.T) {
	// System messages are not turn boundaries
	msgs := []cliMessage{
		mkMsg("system"),       // 0
		mkMsg("user"),         // 1 — turn 0
		mkMsg("assistant"),    // 2
		mkMsg("tool_summary"), // 3
		mkMsg("user"),         // 4 — turn 1
	}
	got := visibleTurnIndices(msgs)
	want := []int{1, 4}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// TestVisibleTurnIndices_ComprehensiveNoOrphans tests that deleting any number
// of turns from the end never leaves orphaned tool_summaries.
// "Orphan" = tool_summary with no preceding assistant in the remaining slice.
func TestVisibleTurnIndices_ComprehensiveNoOrphans(t *testing.T) {
	msgs := []cliMessage{
		mkMsg("user"),         // 0 — turn 0
		mkMsg("assistant"),    // 1
		mkMsg("tool_summary"), // 2
		mkMsg("assistant"),    // 3
		mkMsg("tool_summary"), // 4
		mkMsg("user"),         // 5 — turn 1
		mkMsg("assistant"),    // 6
		mkMsg("user"),         // 7 — turn 2
	}
	turns := visibleTurnIndices(msgs)
	for del := 1; del <= len(turns); del++ {
		cutIdx := turns[len(turns)-del]
		remaining := msgs[:cutIdx]
		// Every tool_summary must have a preceding assistant (or user) in the remaining slice
		for i, msg := range remaining {
			if msg.role == "tool_summary" {
				hasPreceding := false
				for j := 0; j < i; j++ {
					if remaining[j].role == "assistant" || remaining[j].role == "user" {
						hasPreceding = true
						break
					}
				}
				if !hasPreceding {
					t.Errorf("confirmDelete=%d: orphaned tool_summary at index %d (remaining: %v)",
						del, i, rolesOf(remaining))
				}
			}
		}
	}
}

// visibleMsgGroupIndices backward compatibility
func TestVisibleMsgGroupIndices_IsAlias(t *testing.T) {
	msgs := []cliMessage{
		mkMsg("user"),
		mkMsg("assistant"),
		mkMsg("user"),
	}
	turns := visibleTurnIndices(msgs)
	groups := visibleMsgGroupIndices(msgs)
	if !reflect.DeepEqual(turns, groups) {
		t.Errorf("visibleMsgGroupIndices should alias visibleTurnIndices: got %v, want %v", groups, turns)
	}
}
