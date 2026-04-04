// cli_message_visible_test.go — Unit tests for visibleMsgGroupIndices
// Covers: Ctrl+K delete grouping, tool_summary orphan prevention

package channel

import (
	"reflect"
	"testing"
)

// mkMsg is a helper to create a cliMessage with only the role field set.
func mkMsg(role string) cliMessage {
	return cliMessage{role: role}
}

// ---------------------------------------------------------------------------
// visibleMsgGroupIndices — core delete-grouping logic
// ---------------------------------------------------------------------------

func TestVisibleMsgGroupIndices_NoToolSummary(t *testing.T) {
	// Simple case: no tool_summary messages at all
	msgs := []cliMessage{
		mkMsg("user"),
		mkMsg("assistant"),
		mkMsg("user"),
	}
	got := visibleMsgGroupIndices(msgs)
	want := []int{0, 1, 2}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestVisibleMsgGroupIndices_ToolSummaryBeforeAssistant(t *testing.T) {
	// Bug scenario: tool_summary(2) belongs to assistant(3).
	// Deleting groups should include the tool_summary in the same group.
	// messages: [user(0), assistant(1), tool_summary(2), assistant(3), user(4)]
	// assistant(3) scans back to tool_summary(2) → group start = 2
	// groups: [0, 1, 2, 4]
	msgs := []cliMessage{
		mkMsg("user"),         // 0
		mkMsg("assistant"),    // 1
		mkMsg("tool_summary"), // 2 — belongs to assistant(3)
		mkMsg("assistant"),    // 3
		mkMsg("user"),         // 4
	}
	got := visibleMsgGroupIndices(msgs)
	want := []int{0, 1, 2, 4}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestVisibleMsgGroupIndices_MultipleToolSummaries(t *testing.T) {
	// messages: [user(0), assistant(1), tool_summary(2), assistant(3), tool_summary(4), user(5)]
	// assistant(1): no tool_summary before → start=1
	// assistant(3): scans back to tool_summary(2) → start=2
	// user(5): scans back to tool_summary(4) → start=4
	// groups: [0, 1, 2, 4, 5]
	msgs := []cliMessage{
		mkMsg("user"),         // 0
		mkMsg("assistant"),    // 1
		mkMsg("tool_summary"), // 2 — belongs to assistant(3)
		mkMsg("assistant"),    // 3
		mkMsg("tool_summary"), // 4 — between assistant(3) and user(5)
		mkMsg("user"),         // 5
	}
	got := visibleMsgGroupIndices(msgs)
	want := []int{0, 1, 2, 4}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestVisibleMsgGroupIndices_OrphanPrevention(t *testing.T) {
	// The critical bug scenario:
	// messages: [user(0), assistant(1), tool_summary(2), assistant(3), user(4)]
	// confirmDelete=2 means delete last 2 groups → keep first 2 groups
	// With OLD code: groups=[0,1,3,4], cutIdx=groups[2]=3, result=[user,assistant,tool_summary]
	//   → tool_summary(2) is orphaned (its assistant(3) was deleted)
	// With NEW code: groups=[0,1,2,4], cutIdx=groups[2]=2, result=[user,assistant]
	//   → tool_summary correctly removed with its group
	msgs := []cliMessage{
		mkMsg("user"),         // 0
		mkMsg("assistant"),    // 1
		mkMsg("tool_summary"), // 2 — belongs to assistant(3)
		mkMsg("assistant"),    // 3
		mkMsg("user"),         // 4
	}
	groups := visibleMsgGroupIndices(msgs)
	confirmDelete := 2
	cutIdx := groups[len(groups)-confirmDelete]

	remaining := msgs[:cutIdx]
	// After delete, no message should have a tool_summary at the end
	// (orphaned from its assistant)
	for i, msg := range remaining {
		if msg.role == "tool_summary" {
			// Check if the next non-tool_summary message is also in remaining
			hasFollowingNonTool := false
			for j := i + 1; j < len(remaining); j++ {
				if remaining[j].role != "tool_summary" {
					hasFollowingNonTool = true
					break
				}
			}
			if !hasFollowingNonTool {
				t.Errorf("orphaned tool_summary at index %d after delete (cutIdx=%d, groups=%v)", i, cutIdx, groups)
			}
		}
	}
	// Verify no orphaned tool_summary at the tail
	if len(remaining) > 0 && remaining[len(remaining)-1].role == "tool_summary" {
		t.Errorf("tool_summary at tail is orphaned after delete (remaining roles: %v)", rolesOf(remaining))
	}
}

func TestVisibleMsgGroupIndices_LeadingToolSummary(t *testing.T) {
	// Edge case: tool_summary at the very start (unlikely but should not panic)
	// tool_summary(0) is not covered by any following group (user(1) doesn't scan back past index 0
	// because messages[0] IS tool_summary) — wait, it DOES scan back.
	// user(1) scans back: messages[0].role == "tool_summary" → startIdx=0
	// groups: [0, 1, 2]
	msgs := []cliMessage{
		mkMsg("tool_summary"), // 0
		mkMsg("user"),         // 1
		mkMsg("assistant"),    // 2
	}
	// user(1) scans back: messages[0]="tool_summary" → startIdx=0, covers [0,1]
	// assistant(2) scans back: messages[1]="user" ≠ tool_summary → startIdx=2
	// groups: [0, 2]
	// But tool_summary(0) is covered, so no extra group needed.
	// However, the original test expected [0, 1, 2]. Let me reconsider...
	// Actually with the current fix: covered tracks which indices are claimed.
	// user(1): startIdx=0 (scan back), covers 0,1 → groups=[0]
	// assistant(2): startIdx=2 (no tool_summary before), covers 2 → groups=[0,2]
	// No uncovered tool_summaries → final groups = [0, 2]
	// This is correct: deleting from group[0] removes the leading tool_summary too.
	// But the user might expect 3 groups for 3 visible items...
	// The key is: cutIdx correctness matters, not group count.
	want := []int{0, 2}
	got := visibleMsgGroupIndices(msgs)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestVisibleMsgGroupIndices_ConsecutiveToolSummaries(t *testing.T) {
	// Edge case: multiple consecutive tool_summaries before an assistant
	// messages: [user(0), assistant(1), tool_summary(2), tool_summary(3), assistant(4), user(5)]
	// assistant(1): no tool_summary before → start=1, covers [1]
	// assistant(4): scans back to tool_summary(2) → start=2, covers [2,3,4]
	// user(5): no tool_summary before → start=5, covers [5]
	// groups: [0, 1, 2, 5]
	msgs := []cliMessage{
		mkMsg("user"),         // 0
		mkMsg("assistant"),    // 1
		mkMsg("tool_summary"), // 2
		mkMsg("tool_summary"), // 3
		mkMsg("assistant"),    // 4
		mkMsg("user"),         // 5
	}
	got := visibleMsgGroupIndices(msgs)
	want := []int{0, 1, 2, 5}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestVisibleMsgGroupIndices_SystemMessage(t *testing.T) {
	// System messages are visible groups too
	// messages: [system(0), user(1), assistant(2), tool_summary(3), user(4)]
	// system(0): start=0, covers [0]
	// user(1): start=1, covers [1]
	// assistant(2): start=2, covers [2]
	// user(4): scans back to tool_summary(3) → start=3, covers [3,4]
	// groups: [0, 1, 2, 3]
	msgs := []cliMessage{
		mkMsg("system"),       // 0
		mkMsg("user"),         // 1
		mkMsg("assistant"),    // 2
		mkMsg("tool_summary"), // 3
		mkMsg("user"),         // 4
	}
	got := visibleMsgGroupIndices(msgs)
	want := []int{0, 1, 2, 3}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestVisibleMsgGroupIndices_Empty(t *testing.T) {
	got := visibleMsgGroupIndices(nil)
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
	got = visibleMsgGroupIndices([]cliMessage{})
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

func TestVisibleMsgGroupIndices_SingleToolSummary(t *testing.T) {
	// Only a tool_summary — not covered by any group, becomes its own group
	msgs := []cliMessage{mkMsg("tool_summary")}
	got := visibleMsgGroupIndices(msgs)
	want := []int{0}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestVisibleMsgGroupIndices_ToolSummaryOnlyChain(t *testing.T) {
	// Only tool_summaries — none covered by any non-tool_summary group
	msgs := []cliMessage{
		mkMsg("tool_summary"),
		mkMsg("tool_summary"),
		mkMsg("tool_summary"),
	}
	got := visibleMsgGroupIndices(msgs)
	want := []int{0, 1, 2}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestVisibleMsgGroupIndices_DeleteKeepsNoOrphans(t *testing.T) {
	// Comprehensive: simulate delete with confirmDelete from 1 to len(groups)
	// and verify no orphans after truncation.
	msgs := []cliMessage{
		mkMsg("user"),         // 0
		mkMsg("assistant"),    // 1
		mkMsg("tool_summary"), // 2
		mkMsg("assistant"),    // 3
		mkMsg("tool_summary"), // 4
		mkMsg("user"),         // 5
	}
	groups := visibleMsgGroupIndices(msgs)
	for del := 1; del <= len(groups); del++ {
		cutIdx := groups[len(groups)-del]
		remaining := msgs[:cutIdx]
		// No trailing orphaned tool_summary
		if len(remaining) > 0 && remaining[len(remaining)-1].role == "tool_summary" {
			t.Errorf("confirmDelete=%d: trailing orphan tool_summary (cutIdx=%d, remaining roles: %v)",
				del, cutIdx, rolesOf(remaining))
		}
		// No orphaned tool_summary in the middle either
		for i, msg := range remaining {
			if msg.role == "tool_summary" {
				hasFollowing := false
				for j := i + 1; j < len(remaining); j++ {
					if remaining[j].role != "tool_summary" {
						hasFollowing = true
						break
					}
				}
				if !hasFollowing {
					t.Errorf("confirmDelete=%d: orphaned tool_summary at index %d (remaining: %v)",
						del, i, rolesOf(remaining))
				}
			}
		}
	}
}

// rolesOf extracts role strings from a message slice for debugging.
func rolesOf(msgs []cliMessage) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.role
	}
	return out
}
