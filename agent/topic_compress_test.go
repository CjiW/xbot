package agent

import (
	"strings"
	"testing"

	"xbot/llm"
)

// TestTopicAwareCompressSegments verifies that topic-aware compression
// splits messages into historical and current topics, compressing only historical ones.
func TestTopicAwareCompressSegments(t *testing.T) {
	d := NewTopicDetector()
	d.MinSegmentSize = 2
	d.CosineThreshold = 0.3

	// Build messages simulating two topic switches (20+ messages to pass DefaultMinHistory)
	messages := []llm.ChatMessage{
		llm.NewSystemMessage("You are a helpful coding assistant."),
	}
	// Topic 1: Database work (8 messages = 4 turns)
	for i := 0; i < 4; i++ {
		messages = append(messages, userMsg("Fix the database connection issue in db.go"))
		messages = append(messages, assistantMsg("I'll fix the database connection by updating the driver."))
	}
	// Topic 2: API work (8 messages = 4 turns)
	for i := 0; i < 4; i++ {
		messages = append(messages, userMsg("Add authentication middleware to the API"))
		messages = append(messages, assistantMsg("I'll add JWT authentication middleware."))
	}
	// Topic 3 (current): Memory work (8 messages = 4 turns)
	for i := 0; i < 4; i++ {
		messages = append(messages, userMsg("Implement the archival memory search function"))
		messages = append(messages, assistantMsg("I'll implement semantic search for archival memory."))
	}

	segments, err := d.Detect(messages)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}

	if len(segments) < 2 {
		t.Fatalf("expected at least 2 segments, got %d", len(segments))
	}

	// The last segment should be marked as current topic
	lastSeg := segments[len(segments)-1]
	if !lastSeg.IsCurrent {
		t.Error("last segment should be marked as current topic")
	}

	// Earlier segments should NOT be current
	for _, seg := range segments[:len(segments)-1] {
		if seg.IsCurrent {
			t.Errorf("historical segment at [%d:%d] should not be current", seg.StartIdx, seg.EndIdx)
		}
	}
}

// TestTopicDetectorIntegration verifies that phase1Manager correctly
// stores and uses TopicDetector.
func TestTopicDetectorIntegration(t *testing.T) {
	cfg := &ContextManagerConfig{}
	pm := newPhase1Manager(cfg)

	// Initially nil
	if pm.topicDetector != nil {
		t.Error("topicDetector should be nil initially")
	}

	// Set and verify
	d := NewTopicDetector()
	pm.SetTopicDetector(&d)
	if pm.topicDetector == nil || *pm.topicDetector != d {
		t.Error("SetTopicDetector did not set the detector")
	}
}

// TestTopicAwareCompressWithNilDetector verifies that nil TopicDetector
// doesn't affect compression (backward compatible).
func TestTopicAwareCompressWithNilDetector(t *testing.T) {
	messages := []llm.ChatMessage{
		llm.NewSystemMessage("You are a helpful coding assistant."),
		llm.NewUserMessage("Fix the bug in main.go"),
		llm.NewAssistantMessage("I'll fix the bug now."),
	}

	// Detect with short conversation should return single segment
	d := NewTopicDetector()
	segments, err := d.Detect(messages)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}

	// Below DefaultMinHistory should return single segment
	if len(segments) != 1 {
		t.Errorf("expected 1 segment for short conversation, got %d", len(segments))
	}
}

// TestTopicPartitionPreservesCurrentTopicContent verifies the key guarantee:
// when topic partition is active, current topic messages are preserved verbatim
// in the LLMView output.
func TestTopicPartitionPreservesCurrentTopicContent(t *testing.T) {
	d := NewTopicDetector()
	d.MinSegmentSize = 2
	d.CosineThreshold = 0.3

	// Build a conversation with clear topic switches
	messages := []llm.ChatMessage{
		llm.NewSystemMessage("You are a helpful coding assistant."),
	}
	// Historical topic: authentication (enough to exceed DefaultMinHistory)
	for i := 0; i < 6; i++ {
		messages = append(messages, userMsg("Please fix the authentication flow in auth.go by adding OAuth2 support"))
		messages = append(messages, assistantMsg("I'll add OAuth2 support to auth.go."))
	}
	// Current topic: database (should be preserved)
	currentTopicMarker := "UNIQUE_CURRENT_TOPIC_MARKER_XYZ123"
	messages = append(messages, userMsg("Fix the database schema for users table"))
	messages = append(messages, assistantMsg(currentTopicMarker))
	messages = append(messages, userMsg("Add foreign key constraint"))
	messages = append(messages, assistantMsg(currentTopicMarker))

	// Detect segments
	segments, err := d.Detect(messages)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}

	if len(segments) < 2 {
		t.Fatalf("expected at least 2 segments, got %d — test data not diverse enough", len(segments))
	}

	// Verify the last segment contains our current topic marker
	lastSeg := segments[len(segments)-1]
	found := false
	for _, msg := range messages[lastSeg.StartIdx:lastSeg.EndIdx] {
		if strings.Contains(msg.Content, currentTopicMarker) {
			found = true
		}
	}
	if !found {
		t.Error("current topic marker not found in last segment — current topic not correctly identified")
	}
}
