package channel

import (
	"encoding/json"
	"testing"
)

// ---------------------------------------------------------------------------
// Message segment parsing tests
// ---------------------------------------------------------------------------

func TestNapCatParseMessageSegments_TextOnly(t *testing.T) {
	ch := NewNapCatChannel(NapCatConfig{}, nil)
	segments := `[{"type":"text","data":{"text":"hello world"}}]`
	content, media, _ := ch.parseMessageSegments(json.RawMessage(segments), 0)
	if content != "hello world" {
		t.Errorf("expected 'hello world', got %q", content)
	}
	if len(media) != 0 {
		t.Errorf("expected no media, got %v", media)
	}
}

func TestNapCatParseMessageSegments_Image(t *testing.T) {
	ch := NewNapCatChannel(NapCatConfig{}, nil)
	segments := `[{"type":"image","data":{"file":"abc.jpg","url":"https://example.com/abc.jpg"}}]`
	content, media, _ := ch.parseMessageSegments(json.RawMessage(segments), 0)
	if content != "" {
		t.Errorf("expected empty content, got %q", content)
	}
	if len(media) != 1 || media[0] != "https://example.com/abc.jpg" {
		t.Errorf("expected 1 media URL, got %v", media)
	}
}

func TestNapCatParseMessageSegments_ImageFallbackToFile(t *testing.T) {
	ch := NewNapCatChannel(NapCatConfig{}, nil)
	segments := `[{"type":"image","data":{"file":"file:///tmp/abc.jpg","url":""}}]`
	content, media, _ := ch.parseMessageSegments(json.RawMessage(segments), 0)
	if content != "" {
		t.Errorf("expected empty content, got %q", content)
	}
	if len(media) != 1 || media[0] != "file:///tmp/abc.jpg" {
		t.Errorf("expected file URL fallback, got %v", media)
	}
}

func TestNapCatParseMessageSegments_AtBot(t *testing.T) {
	ch := NewNapCatChannel(NapCatConfig{}, nil)
	// selfID = 123456, @bot should be filtered and mentionedBot=true
	segments := `[{"type":"at","data":{"qq":"123456"}},{"type":"text","data":{"text":" hello"}}]`
	content, media, mentionedBot := ch.parseMessageSegments(json.RawMessage(segments), 123456)
	if content != "hello" {
		t.Errorf("expected 'hello' (at-bot filtered), got %q", content)
	}
	if len(media) != 0 {
		t.Errorf("expected no media, got %v", media)
	}
	if !mentionedBot {
		t.Error("expected mentionedBot=true when @bot")
	}
}

func TestNapCatParseMessageSegments_AtOther(t *testing.T) {
	ch := NewNapCatChannel(NapCatConfig{}, nil)
	segments := `[{"type":"at","data":{"qq":"999999"}},{"type":"text","data":{"text":" hello"}}]`
	content, media, mentionedBot := ch.parseMessageSegments(json.RawMessage(segments), 123456)
	if content != "@999999 hello" {
		t.Errorf("expected '@999999 hello', got %q", content)
	}
	if len(media) != 0 {
		t.Errorf("expected no media, got %v", media)
	}
	if mentionedBot {
		t.Error("expected mentionedBot=false when @other")
	}
}

func TestNapCatParseMessageSegments_AtAll(t *testing.T) {
	ch := NewNapCatChannel(NapCatConfig{}, nil)
	segments := `[{"type":"at","data":{"qq":"all"}},{"type":"text","data":{"text":" hello"}}]`
	content, media, mentionedBot := ch.parseMessageSegments(json.RawMessage(segments), 123456)
	if content != "hello" {
		t.Errorf("expected 'hello' (at-all filtered), got %q", content)
	}
	if len(media) != 0 {
		t.Errorf("expected no media, got %v", media)
	}
	if !mentionedBot {
		t.Error("expected mentionedBot=true when @all")
	}
}

func TestNapCatParseMessageSegments_Reply(t *testing.T) {
	ch := NewNapCatChannel(NapCatConfig{}, nil)
	segments := `[{"type":"reply","data":{"id":"12345"}},{"type":"text","data":{"text":"reply content"}}]`
	content, media, _ := ch.parseMessageSegments(json.RawMessage(segments), 0)
	if content != "reply content" {
		t.Errorf("expected 'reply content', got %q", content)
	}
	if len(media) != 0 {
		t.Errorf("expected no media, got %v", media)
	}
}

func TestNapCatParseMessageSegments_Mixed(t *testing.T) {
	ch := NewNapCatChannel(NapCatConfig{}, nil)
	segments := `[
		{"type":"reply","data":{"id":"111"}},
		{"type":"at","data":{"qq":"123456"}},
		{"type":"text","data":{"text":"看看这个图"}},
		{"type":"image","data":{"file":"abc.jpg","url":"https://example.com/img.jpg"}},
		{"type":"face","data":{"id":"178"}}
	]`
	content, media, _ := ch.parseMessageSegments(json.RawMessage(segments), 123456)
	if content != "看看这个图" {
		t.Errorf("expected '看看这个图', got %q", content)
	}
	if len(media) != 1 || media[0] != "https://example.com/img.jpg" {
		t.Errorf("expected 1 media URL, got %v", media)
	}
}

func TestNapCatParseMessageSegments_StringFormat(t *testing.T) {
	ch := NewNapCatChannel(NapCatConfig{}, nil)
	// messagePostFormat=string 时，message 是字符串
	content, media, _ := ch.parseMessageSegments(json.RawMessage(`"hello string format"`), 0)
	if content != "hello string format" {
		t.Errorf("expected 'hello string format', got %q", content)
	}
	if len(media) != 0 {
		t.Errorf("expected no media, got %v", media)
	}
}

func TestNapCatParseMessageSegments_Empty(t *testing.T) {
	ch := NewNapCatChannel(NapCatConfig{}, nil)
	content, media, _ := ch.parseMessageSegments(nil, 0)
	if content != "" {
		t.Errorf("expected empty content, got %q", content)
	}
	if len(media) != 0 {
		t.Errorf("expected no media, got %v", media)
	}
}

func TestNapCatParseMessageSegments_Record(t *testing.T) {
	ch := NewNapCatChannel(NapCatConfig{}, nil)
	segments := `[{"type":"record","data":{"file":"voice.amr","url":"https://example.com/voice.amr"}}]`
	content, media, _ := ch.parseMessageSegments(json.RawMessage(segments), 0)
	if content != "" {
		t.Errorf("expected empty content, got %q", content)
	}
	if len(media) != 1 || media[0] != "https://example.com/voice.amr" {
		t.Errorf("expected voice URL, got %v", media)
	}
}

func TestNapCatParseMessageSegments_Video(t *testing.T) {
	ch := NewNapCatChannel(NapCatConfig{}, nil)
	segments := `[{"type":"video","data":{"file":"video.mp4","url":"https://example.com/video.mp4"}}]`
	content, media, _ := ch.parseMessageSegments(json.RawMessage(segments), 0)
	if content != "" {
		t.Errorf("expected empty content, got %q", content)
	}
	if len(media) != 1 || media[0] != "https://example.com/video.mp4" {
		t.Errorf("expected video URL, got %v", media)
	}
}

// ---------------------------------------------------------------------------
// Deduplication tests
// ---------------------------------------------------------------------------

func TestNapCatIsDuplicate(t *testing.T) {
	ch := NewNapCatChannel(NapCatConfig{}, nil)
	ch.maxProcessed = 3

	// First time: not duplicate
	if ch.isDuplicate("msg1") {
		t.Error("msg1 should not be duplicate on first call")
	}

	// Second time: duplicate
	if !ch.isDuplicate("msg1") {
		t.Error("msg1 should be duplicate on second call")
	}

	// Fill up to max
	ch.isDuplicate("msg2")
	ch.isDuplicate("msg3")

	// msg1 should be evicted now (maxProcessed=3, we have msg1,msg2,msg3)
	// Actually msg1 is still there since we have exactly 3
	if !ch.isDuplicate("msg1") {
		t.Error("msg1 should still be in cache (exactly at max)")
	}

	// Add msg4, which should evict msg1 (now we have msg2,msg3,msg1 -> after msg4: msg3,msg1,msg4)
	// Wait, the order is: msg1, msg2, msg3, msg1(dup-skip), msg4
	// processedOrder after msg1,msg2,msg3 = [msg1,msg2,msg3], len=3
	// msg1 is dup, no change
	// msg4: add -> [msg1,msg2,msg3,msg4], len=4 > 3, evict msg1 -> [msg2,msg3,msg4]
	ch.isDuplicate("msg4")

	// msg1 should be evicted
	if ch.isDuplicate("msg1") {
		t.Error("msg1 should have been evicted")
	}
}

func TestNapCatIsDuplicate_Eviction(t *testing.T) {
	ch := NewNapCatChannel(NapCatConfig{}, nil)
	ch.maxProcessed = 2

	ch.isDuplicate("a")
	ch.isDuplicate("b")
	// cache: [a, b]

	ch.isDuplicate("c")
	// cache should be [b, c] (a evicted)

	// "a" was evicted, so isDuplicate returns false (not seen)
	if ch.isDuplicate("a") {
		t.Error("'a' should have been evicted and not be duplicate")
	}
	// Note: calling isDuplicate("a") above re-added "a" to cache, evicting "b"
	// cache is now: [c, a]

	// "b" was evicted when "a" was re-added
	if ch.isDuplicate("b") {
		t.Error("'b' should have been evicted")
	}
	// cache is now: [a, b]

	// "c" was evicted
	if ch.isDuplicate("c") {
		t.Error("'c' should have been evicted")
	}
}

// ---------------------------------------------------------------------------
// Allowlist tests
// ---------------------------------------------------------------------------

func TestNapCatIsAllowed_EmptyList(t *testing.T) {
	ch := NewNapCatChannel(NapCatConfig{AllowFrom: nil}, nil)
	if !ch.isAllowed(ch.config.AllowFrom, "12345") {
		t.Error("empty allowlist should allow everyone")
	}
}

func TestNapCatIsAllowed_InList(t *testing.T) {
	ch := NewNapCatChannel(NapCatConfig{AllowFrom: []string{"111", "222", "333"}}, nil)
	if !ch.isAllowed(ch.config.AllowFrom, "222") {
		t.Error("222 should be allowed")
	}
}

func TestNapCatIsAllowed_NotInList(t *testing.T) {
	ch := NewNapCatChannel(NapCatConfig{AllowFrom: []string{"111", "222"}}, nil)
	if ch.isAllowed(ch.config.AllowFrom, "999") {
		t.Error("999 should not be allowed")
	}
}

// ---------------------------------------------------------------------------
// Outbound message building tests
// ---------------------------------------------------------------------------

func TestNapCatBuildOutboundMessage_TextOnly(t *testing.T) {
	ch := NewNapCatChannel(NapCatConfig{}, nil)
	result := ch.buildOutboundMessage("hello", nil)
	s, ok := result.(string)
	if !ok {
		t.Fatalf("expected string, got %T", result)
	}
	if s != "hello" {
		t.Errorf("expected 'hello', got %q", s)
	}
}

func TestNapCatBuildOutboundMessage_WithMedia(t *testing.T) {
	ch := NewNapCatChannel(NapCatConfig{}, nil)
	result := ch.buildOutboundMessage("看图", []string{"https://example.com/img.jpg"})
	segments, ok := result.([]map[string]any)
	if !ok {
		t.Fatalf("expected []map[string]any, got %T", result)
	}
	if len(segments) != 2 {
		t.Fatalf("expected 2 segments, got %d", len(segments))
	}

	// First segment: text
	if segments[0]["type"] != "text" {
		t.Errorf("expected text segment, got %v", segments[0]["type"])
	}

	// Second segment: image
	if segments[1]["type"] != "image" {
		t.Errorf("expected image segment, got %v", segments[1]["type"])
	}
}

func TestNapCatBuildOutboundMessage_MediaOnly(t *testing.T) {
	ch := NewNapCatChannel(NapCatConfig{}, nil)
	result := ch.buildOutboundMessage("", []string{"https://example.com/img.jpg"})
	segments, ok := result.([]map[string]any)
	if !ok {
		t.Fatalf("expected []map[string]any, got %T", result)
	}
	if len(segments) != 1 {
		t.Fatalf("expected 1 segment (image only), got %d", len(segments))
	}
	if segments[0]["type"] != "image" {
		t.Errorf("expected image segment, got %v", segments[0]["type"])
	}
}

// ---------------------------------------------------------------------------
// Truncate helper test
// ---------------------------------------------------------------------------

func TestTruncate(t *testing.T) {
	if truncate("hello", 10) != "hello" {
		t.Error("short string should not be truncated")
	}
	if truncate("hello world", 5) != "hello..." {
		t.Errorf("expected 'hello...', got %q", truncate("hello world", 5))
	}
}

// ---------------------------------------------------------------------------
// Event handling tests
// ---------------------------------------------------------------------------

func TestNapCatHandleEvent_Heartbeat(t *testing.T) {
	ch := NewNapCatChannel(NapCatConfig{}, nil)
	data := `{"post_type":"meta_event","meta_event_type":"heartbeat","interval":30000,"self_id":123456}`
	err := ch.handleEvent([]byte(data))
	if err != nil {
		t.Errorf("heartbeat should not error: %v", err)
	}
	if ch.selfID.Load() != 123456 {
		t.Errorf("expected selfID=123456, got %d", ch.selfID.Load())
	}
}

func TestNapCatHandleEvent_Lifecycle(t *testing.T) {
	ch := NewNapCatChannel(NapCatConfig{}, nil)
	data := `{"post_type":"meta_event","meta_event_type":"lifecycle","sub_type":"connect","self_id":789}`
	err := ch.handleEvent([]byte(data))
	if err != nil {
		t.Errorf("lifecycle should not error: %v", err)
	}
}

func TestNapCatHandleEvent_APIResponse(t *testing.T) {
	ch := NewNapCatChannel(NapCatConfig{}, nil)

	// Register a pending request
	respCh := make(chan json.RawMessage, 1)
	ch.pendingMu.Lock()
	ch.pending["test-echo-123"] = respCh
	ch.pendingMu.Unlock()

	data := `{"status":"ok","retcode":0,"data":{"message_id":999},"echo":"test-echo-123"}`
	err := ch.handleEvent([]byte(data))
	if err != nil {
		t.Errorf("api response should not error: %v", err)
	}

	// Check response was delivered
	select {
	case resp := <-respCh:
		var apiResp obAPIResponse
		if err := json.Unmarshal(resp, &apiResp); err != nil {
			t.Fatalf("failed to parse response: %v", err)
		}
		if apiResp.RetCode != 0 {
			t.Errorf("expected retcode=0, got %d", apiResp.RetCode)
		}
	default:
		t.Error("expected response to be delivered to pending channel")
	}
}
