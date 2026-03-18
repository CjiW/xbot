package channel

import (
	"encoding/json"
	"fmt"
	"strconv"
	"testing"
)

// ===========================================================================
// CQ 码解析测试
// ===========================================================================

func TestOneBotParseCQImages_SingleImage(t *testing.T) {
	msg := `[CQ:image,file=abc.jpg,url=https://example.com/img.jpg]`
	urls := parseCQImages(msg)
	if len(urls) != 1 {
		t.Fatalf("expected 1 url, got %d", len(urls))
	}
	if urls[0] != "https://example.com/img.jpg" {
		t.Errorf("expected https://example.com/img.jpg, got %s", urls[0])
	}
}

func TestOneBotParseCQImages_MultipleImages(t *testing.T) {
	msg := `hello [CQ:image,file=a.jpg,url=https://a.com/1.jpg] world [CQ:image,file=b.jpg,url=https://b.com/2.jpg]`
	urls := parseCQImages(msg)
	if len(urls) != 2 {
		t.Fatalf("expected 2 urls, got %d", len(urls))
	}
	if urls[0] != "https://a.com/1.jpg" {
		t.Errorf("expected https://a.com/1.jpg, got %s", urls[0])
	}
	if urls[1] != "https://b.com/2.jpg" {
		t.Errorf("expected https://b.com/2.jpg, got %s", urls[1])
	}
}

func TestOneBotParseCQImages_NoImages(t *testing.T) {
	msg := "hello world, no images here"
	urls := parseCQImages(msg)
	if len(urls) != 0 {
		t.Errorf("expected 0 urls, got %d", len(urls))
	}
}

func TestOneBotParseCQImages_EmptyMessage(t *testing.T) {
	urls := parseCQImages("")
	if len(urls) != 0 {
		t.Errorf("expected 0 urls, got %d", len(urls))
	}
}

func TestOneBotParseCQImages_ImageWithExtraParams(t *testing.T) {
	// url 不在最后，后面还有其他参数
	msg := `[CQ:image,url=https://example.com/img.jpg,file=abc.jpg,subType=0]`
	urls := parseCQImages(msg)
	if len(urls) != 1 {
		t.Fatalf("expected 1 url, got %d", len(urls))
	}
	if urls[0] != "https://example.com/img.jpg" {
		t.Errorf("expected https://example.com/img.jpg, got %s", urls[0])
	}
}

func TestOneBotParseCQImages_OnlyCQCodeNoImage(t *testing.T) {
	msg := `[CQ:at,qq=12345] [CQ:face,id=178]`
	urls := parseCQImages(msg)
	if len(urls) != 0 {
		t.Errorf("expected 0 urls, got %d", len(urls))
	}
}

func TestOneBotParseCQImages_MixedCQCodes(t *testing.T) {
	msg := `[CQ:at,qq=12345] hello [CQ:image,file=a.jpg,url=https://img.com/a.jpg] [CQ:face,id=178]`
	urls := parseCQImages(msg)
	if len(urls) != 1 {
		t.Fatalf("expected 1 url, got %d", len(urls))
	}
	if urls[0] != "https://img.com/a.jpg" {
		t.Errorf("expected https://img.com/a.jpg, got %s", urls[0])
	}
}

// ===========================================================================
// stripCQCodes 测试
// ===========================================================================

func TestOneBotStripCQCodes_RemoveAll(t *testing.T) {
	msg := `[CQ:at,qq=12345] hello [CQ:image,file=a.jpg,url=https://img.com/a.jpg] world [CQ:face,id=178]`
	result := stripCQCodes(msg)
	expected := " hello  world "
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestOneBotStripCQCodes_NoCode(t *testing.T) {
	msg := "hello world"
	result := stripCQCodes(msg)
	if result != msg {
		t.Errorf("expected %q, got %q", msg, result)
	}
}

func TestOneBotStripCQCodes_EmptyMessage(t *testing.T) {
	result := stripCQCodes("")
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}

func TestOneBotStripCQCodes_OnlyCQCodes(t *testing.T) {
	msg := `[CQ:at,qq=12345][CQ:face,id=178]`
	result := stripCQCodes(msg)
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}

func TestOneBotStripCQCodes_NestedBrackets(t *testing.T) {
	// CQ 码不应该有嵌套方括号，但确保正则不会贪婪匹配
	msg := `[CQ:at,qq=123] text [CQ:face,id=1]`
	result := stripCQCodes(msg)
	expected := " text "
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

// ===========================================================================
// markdownToOneBotMessage 测试
// ===========================================================================

func TestOneBotMarkdownToMessage_PlainText(t *testing.T) {
	content := "Hello, this is plain text."
	result := markdownToOneBotMessage(content)
	if result != content {
		t.Errorf("expected %q, got %q", content, result)
	}
}

func TestOneBotMarkdownToMessage_Bold(t *testing.T) {
	content := "This is **bold** text."
	result := markdownToOneBotMessage(content)
	expected := "This is bold text."
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestOneBotMarkdownToMessage_Italic(t *testing.T) {
	content := "This is *italic* text."
	result := markdownToOneBotMessage(content)
	expected := "This is italic text."
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestOneBotMarkdownToMessage_InlineCode(t *testing.T) {
	content := "Use `go test` to run."
	result := markdownToOneBotMessage(content)
	expected := "Use go test to run."
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestOneBotMarkdownToMessage_Heading(t *testing.T) {
	content := "## Section Title\nSome text."
	result := markdownToOneBotMessage(content)
	expected := "Section Title\nSome text."
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestOneBotMarkdownToMessage_HTTPImage(t *testing.T) {
	// HTTP URL 图片应保留为 Markdown 链接格式（不转 CQ 码）
	content := "Look: ![photo](https://example.com/photo.jpg)"
	result := markdownToOneBotMessage(content)
	// markdownToOneBotMessage 对 HTTP URL 图片不转 CQ 码，保留原样
	// 然后链接正则会把 ![photo](url) 转为 "photo (url)" 格式
	// 但实际上 onebotMdImgRe 匹配后返回原 match（因为是 HTTP），
	// 然后后面的链接正则 [text](url) 会匹配 ![photo](url) 吗？
	// 不会，因为 ![photo](url) 前面有 !，链接正则是 \[...\]\(...\)
	// 实际上 ![photo](url) 也匹配 \[...\]\(...\)，因为 ! 在 [ 前面
	// 让我们看看实际行为
	// onebotMdImgRe 匹配 ![photo](url)，发现是 HTTP，返回原 match
	// 然后链接正则 \[([^\]]+)\]\(([^)]+)\) 会匹配 [photo](url) 部分
	// 所以结果是 "Look: !photo (https://example.com/photo.jpg)"
	expected := "Look: !photo (https://example.com/photo.jpg)"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestOneBotMarkdownToMessage_Link(t *testing.T) {
	content := "Visit [Google](https://google.com) for search."
	result := markdownToOneBotMessage(content)
	expected := "Visit Google (https://google.com) for search."
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestOneBotMarkdownToMessage_LocalImageNonExistent(t *testing.T) {
	// 本地文件不存在时，保留原始 Markdown 格式
	content := "![img](/nonexistent/path/image.png)"
	result := markdownToOneBotMessage(content)
	// 文件不存在，保留原 match，然后链接正则转换
	expected := "!img (/nonexistent/path/image.png)"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

// ===========================================================================
// 事件解析测试
// ===========================================================================

func TestOneBotEventParse_PrivateMessage(t *testing.T) {
	data := `{
		"post_type": "message",
		"message_type": "private",
		"sub_type": "friend",
		"message_id": 12345,
		"user_id": 10001,
		"message": "hello bot",
		"raw_message": "hello bot",
		"self_id": 99999,
		"sender": {
			"user_id": 10001,
			"nickname": "TestUser"
		}
	}`

	var evt onebotEvent
	err := json.Unmarshal([]byte(data), &evt)
	if err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if evt.PostType != "message" {
		t.Errorf("expected post_type=message, got %s", evt.PostType)
	}
	if evt.MessageType != "private" {
		t.Errorf("expected message_type=private, got %s", evt.MessageType)
	}
	if evt.UserID != 10001 {
		t.Errorf("expected user_id=10001, got %d", evt.UserID)
	}
	if evt.MessageID != 12345 {
		t.Errorf("expected message_id=12345, got %d", evt.MessageID)
	}
	if evt.Message != "hello bot" {
		t.Errorf("expected message='hello bot', got %s", evt.Message)
	}
	if evt.Sender.Nickname != "TestUser" {
		t.Errorf("expected sender.nickname=TestUser, got %s", evt.Sender.Nickname)
	}
	if evt.SelfID != 99999 {
		t.Errorf("expected self_id=99999, got %d", evt.SelfID)
	}
}

func TestOneBotEventParse_GroupMessage(t *testing.T) {
	data := `{
		"post_type": "message",
		"message_type": "group",
		"sub_type": "normal",
		"message_id": 67890,
		"user_id": 20002,
		"group_id": 300003,
		"message": "[CQ:at,qq=99999] hello",
		"raw_message": "[CQ:at,qq=99999] hello",
		"self_id": 99999,
		"sender": {
			"user_id": 20002,
			"nickname": "GroupUser"
		}
	}`

	var evt onebotEvent
	err := json.Unmarshal([]byte(data), &evt)
	if err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if evt.MessageType != "group" {
		t.Errorf("expected message_type=group, got %s", evt.MessageType)
	}
	if evt.GroupID != 300003 {
		t.Errorf("expected group_id=300003, got %d", evt.GroupID)
	}
	if evt.UserID != 20002 {
		t.Errorf("expected user_id=20002, got %d", evt.UserID)
	}
	if evt.Sender.Nickname != "GroupUser" {
		t.Errorf("expected sender.nickname=GroupUser, got %s", evt.Sender.Nickname)
	}
}

func TestOneBotEventParse_MetaEvent(t *testing.T) {
	data := `{
		"post_type": "meta_event",
		"meta_event_type": "lifecycle",
		"sub_type": "connect",
		"self_id": 88888
	}`

	var raw map[string]json.RawMessage
	err := json.Unmarshal([]byte(data), &raw)
	if err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	var postType string
	if pt, ok := raw["post_type"]; ok {
		json.Unmarshal(pt, &postType)
	}
	if postType != "meta_event" {
		t.Errorf("expected post_type=meta_event, got %s", postType)
	}

	var selfID int64
	if sid, ok := raw["self_id"]; ok {
		json.Unmarshal(sid, &selfID)
	}
	if selfID != 88888 {
		t.Errorf("expected self_id=88888, got %d", selfID)
	}
}

// ===========================================================================
// AllowFrom 过滤测试
// ===========================================================================

func TestOneBotAllowFrom_EmptyAllowsAll(t *testing.T) {
	cfg := OneBotConfig{
		AllowFrom: []string{},
	}
	// 空列表应允许所有用户
	allowed := isOneBotAllowed(cfg.AllowFrom, "12345")
	if !allowed {
		t.Error("empty AllowFrom should allow all users")
	}
}

func TestOneBotAllowFrom_NilAllowsAll(t *testing.T) {
	cfg := OneBotConfig{
		AllowFrom: nil,
	}
	allowed := isOneBotAllowed(cfg.AllowFrom, "12345")
	if !allowed {
		t.Error("nil AllowFrom should allow all users")
	}
}

func TestOneBotAllowFrom_AllowedUser(t *testing.T) {
	cfg := OneBotConfig{
		AllowFrom: []string{"10001", "10002", "10003"},
	}
	allowed := isOneBotAllowed(cfg.AllowFrom, "10002")
	if !allowed {
		t.Error("user 10002 should be allowed")
	}
}

func TestOneBotAllowFrom_DeniedUser(t *testing.T) {
	cfg := OneBotConfig{
		AllowFrom: []string{"10001", "10002", "10003"},
	}
	allowed := isOneBotAllowed(cfg.AllowFrom, "99999")
	if allowed {
		t.Error("user 99999 should be denied")
	}
}

// isOneBotAllowed 模拟 handleOneBotMessage 中的 AllowFrom 检查逻辑
func isOneBotAllowed(allowFrom []string, userID string) bool {
	if len(allowFrom) == 0 {
		return true
	}
	for _, a := range allowFrom {
		if a == userID {
			return true
		}
	}
	return false
}

// ===========================================================================
// ChatID 映射测试
// ===========================================================================

func TestOneBotChatID_PrivateMessage(t *testing.T) {
	evt := onebotEvent{
		MessageType: "private",
		UserID:      10001,
	}
	chatID := onebotChatID(evt)
	expected := "private_10001"
	if chatID != expected {
		t.Errorf("expected %q, got %q", expected, chatID)
	}
}

func TestOneBotChatID_GroupMessage(t *testing.T) {
	evt := onebotEvent{
		MessageType: "group",
		UserID:      10001,
		GroupID:     300003,
	}
	chatID := onebotChatID(evt)
	expected := "group_300003"
	if chatID != expected {
		t.Errorf("expected %q, got %q", expected, chatID)
	}
}

func TestOneBotChatID_OtherType(t *testing.T) {
	evt := onebotEvent{
		MessageType: "guild",
		UserID:      10001,
	}
	chatID := onebotChatID(evt)
	expected := "other_10001"
	if chatID != expected {
		t.Errorf("expected %q, got %q", expected, chatID)
	}
}

// onebotChatID 模拟 handleOneBotMessage 中的 chatID 生成逻辑
func onebotChatID(evt onebotEvent) string {
	userIDStr := strconv.FormatInt(evt.UserID, 10)
	if evt.MessageType == "private" {
		return "private_" + userIDStr
	} else if evt.MessageType == "group" {
		return "group_" + strconv.FormatInt(evt.GroupID, 10)
	}
	return "other_" + userIDStr
}

// 确保 fmt 被使用（用于其他测试辅助）
var _ = fmt.Sprintf

// ===========================================================================
// extractNumericID 测试
// ===========================================================================

func TestOneBotExtractNumericID_Group(t *testing.T) {
	result := extractNumericID("group_123456")
	if result != "123456" {
		t.Errorf("expected '123456', got %q", result)
	}
}

func TestOneBotExtractNumericID_Private(t *testing.T) {
	result := extractNumericID("private_789")
	if result != "789" {
		t.Errorf("expected '789', got %q", result)
	}
}

func TestOneBotExtractNumericID_Other(t *testing.T) {
	result := extractNumericID("other_555")
	if result != "555" {
		t.Errorf("expected '555', got %q", result)
	}
}

func TestOneBotExtractNumericID_NoPrefix(t *testing.T) {
	result := extractNumericID("123456")
	if result != "123456" {
		t.Errorf("expected '123456', got %q", result)
	}
}

func TestOneBotExtractNumericID_MultipleUnderscores(t *testing.T) {
	// SplitN with 2 means only split on first underscore
	result := extractNumericID("group_123_456")
	if result != "123_456" {
		t.Errorf("expected '123_456', got %q", result)
	}
}

// ===========================================================================
// CQ 码正则边界测试
// ===========================================================================

func TestOneBotCQImageRegex_URLAtEnd(t *testing.T) {
	msg := `[CQ:image,file=abc.jpg,url=https://example.com/img.jpg]`
	matches := cqImageRe.FindAllStringSubmatch(msg, -1)
	if len(matches) != 1 || matches[0][1] != "https://example.com/img.jpg" {
		t.Errorf("unexpected match: %v", matches)
	}
}

func TestOneBotCQImageRegex_URLInMiddle(t *testing.T) {
	msg := `[CQ:image,url=https://example.com/img.jpg,file=abc.jpg]`
	matches := cqImageRe.FindAllStringSubmatch(msg, -1)
	if len(matches) != 1 || matches[0][1] != "https://example.com/img.jpg" {
		t.Errorf("unexpected match: %v", matches)
	}
}

func TestOneBotCQImageRegex_NoURL(t *testing.T) {
	msg := `[CQ:image,file=abc.jpg]`
	matches := cqImageRe.FindAllStringSubmatch(msg, -1)
	if len(matches) != 0 {
		t.Errorf("expected no match, got %v", matches)
	}
}

func TestOneBotCQCodeRegex_MatchesAll(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{`[CQ:at,qq=12345]`, []string{`[CQ:at,qq=12345]`}},
		{`[CQ:face,id=178]`, []string{`[CQ:face,id=178]`}},
		{`[CQ:image,file=a.jpg,url=https://x.com/a.jpg]`, []string{`[CQ:image,file=a.jpg,url=https://x.com/a.jpg]`}},
		{`text [CQ:at,qq=1] more [CQ:face,id=2]`, []string{`[CQ:at,qq=1]`, `[CQ:face,id=2]`}},
		{`no cq codes`, nil},
	}

	for _, tt := range tests {
		matches := cqCodeRe.FindAllString(tt.input, -1)
		if len(matches) != len(tt.want) {
			t.Errorf("input %q: expected %d matches, got %d: %v", tt.input, len(tt.want), len(matches), matches)
			continue
		}
		for i, m := range matches {
			if m != tt.want[i] {
				t.Errorf("input %q: match[%d] expected %q, got %q", tt.input, i, tt.want[i], m)
			}
		}
	}
}

// ===========================================================================
// Markdown 图片正则测试
// ===========================================================================

func TestOneBotMdImgRegex(t *testing.T) {
	tests := []struct {
		input string
		want  []string // captured paths
	}{
		{"![alt](path.jpg)", []string{"path.jpg"}},
		{"![](path.png)", []string{"path.png"}},
		{"text ![img](a.jpg) more ![img2](b.png)", []string{"a.jpg", "b.png"}},
		{"no image here", nil},
		{"![photo](https://example.com/photo.jpg)", []string{"https://example.com/photo.jpg"}},
	}

	for _, tt := range tests {
		matches := onebotMdImgRe.FindAllStringSubmatch(tt.input, -1)
		if len(matches) != len(tt.want) {
			t.Errorf("input %q: expected %d matches, got %d", tt.input, len(tt.want), len(matches))
			continue
		}
		for i, m := range matches {
			if len(m) < 2 || m[1] != tt.want[i] {
				t.Errorf("input %q: match[%d] expected path %q, got %v", tt.input, i, tt.want[i], m)
			}
		}
	}
}

// ===========================================================================
// OneBotConfig 结构测试
// ===========================================================================

func TestOneBotConfig_Fields(t *testing.T) {
	cfg := OneBotConfig{
		WSUrl:     "ws://127.0.0.1:8080",
		HTTPUrl:   "http://127.0.0.1:8080",
		Token:     "test_token",
		AllowFrom: []string{"10001", "10002"},
	}

	if cfg.WSUrl != "ws://127.0.0.1:8080" {
		t.Errorf("unexpected WSUrl: %s", cfg.WSUrl)
	}
	if cfg.HTTPUrl != "http://127.0.0.1:8080" {
		t.Errorf("unexpected HTTPUrl: %s", cfg.HTTPUrl)
	}
	if cfg.Token != "test_token" {
		t.Errorf("unexpected Token: %s", cfg.Token)
	}
	if len(cfg.AllowFrom) != 2 {
		t.Errorf("expected 2 AllowFrom entries, got %d", len(cfg.AllowFrom))
	}
}

// ===========================================================================
// NewOneBotChannel 测试
// ===========================================================================

func TestOneBotNewChannel(t *testing.T) {
	cfg := OneBotConfig{
		WSUrl:   "ws://127.0.0.1:8080",
		HTTPUrl: "http://127.0.0.1:8080",
	}
	ch := NewOneBotChannel(cfg, nil)
	if ch == nil {
		t.Fatal("NewOneBotChannel returned nil")
	}
	if ch.Name() != "onebot" {
		t.Errorf("expected name 'onebot', got %q", ch.Name())
	}
	if ch.httpCli == nil {
		t.Error("httpCli should not be nil")
	}
	if ch.done == nil {
		t.Error("done channel should not be nil")
	}
}

// ===========================================================================
// Stop 幂等性测试
// ===========================================================================

func TestOneBotStop_Idempotent(t *testing.T) {
	cfg := OneBotConfig{
		WSUrl:   "ws://127.0.0.1:8080",
		HTTPUrl: "http://127.0.0.1:8080",
	}
	ch := NewOneBotChannel(cfg, nil)

	// 多次调用 Stop 不应 panic
	ch.Stop()
	ch.Stop()
	ch.Stop()
}

// ===========================================================================
// chatType 映射测试
// ===========================================================================

func TestOneBotChatType_Private(t *testing.T) {
	evt := onebotEvent{MessageType: "private"}
	chatType := onebotChatType(evt)
	if chatType != "p2p" {
		t.Errorf("expected 'p2p', got %q", chatType)
	}
}

func TestOneBotChatType_Group(t *testing.T) {
	evt := onebotEvent{MessageType: "group"}
	chatType := onebotChatType(evt)
	if chatType != "group" {
		t.Errorf("expected 'group', got %q", chatType)
	}
}

func TestOneBotChatType_Other(t *testing.T) {
	evt := onebotEvent{MessageType: "guild"}
	chatType := onebotChatType(evt)
	if chatType != "guild" {
		t.Errorf("expected 'guild', got %q", chatType)
	}
}

// onebotChatType 模拟 handleOneBotMessage 中的 chatType 生成逻辑
func onebotChatType(evt onebotEvent) string {
	if evt.MessageType == "private" {
		return "p2p"
	} else if evt.MessageType == "group" {
		return "group"
	}
	return evt.MessageType
}

// ===========================================================================
// 重连延迟策略测试
// ===========================================================================

func TestOneBotReconnectDelays(t *testing.T) {
	if len(onebotReconnectDelays) == 0 {
		t.Fatal("reconnect delays should not be empty")
	}
	// 验证延迟递增
	for i := 1; i < len(onebotReconnectDelays); i++ {
		if onebotReconnectDelays[i] < onebotReconnectDelays[i-1] {
			t.Errorf("delay[%d]=%v should be >= delay[%d]=%v",
				i, onebotReconnectDelays[i], i-1, onebotReconnectDelays[i-1])
		}
	}
}

func TestOneBotMaxReconnectAttempts(t *testing.T) {
	if onebotMaxReconnectAttempts <= 0 {
		t.Error("max reconnect attempts should be positive")
	}
}

// ===========================================================================
// 综合场景测试
// ===========================================================================

func TestOneBotParseCQImages_URLWithQueryParams(t *testing.T) {
	msg := `[CQ:image,file=abc.jpg,url=https://example.com/img.jpg?token=abc&size=large]`
	urls := parseCQImages(msg)
	// 正则 url=([^\],]+) 会匹配到 ] 或 , 之前的所有字符
	if len(urls) != 1 {
		t.Fatalf("expected 1 url, got %d", len(urls))
	}
	// URL 中的 & 不是 , 或 ]，所以应该被完整捕获
	expected := "https://example.com/img.jpg?token=abc&size=large"
	if urls[0] != expected {
		t.Errorf("expected %q, got %q", expected, urls[0])
	}
}

func TestOneBotStripCQCodes_PreservesNormalBrackets(t *testing.T) {
	msg := "array[0] = [1, 2, 3]"
	result := stripCQCodes(msg)
	// 普通方括号不应被去除（不匹配 [CQ: 开头）
	if result != msg {
		t.Errorf("expected %q, got %q", msg, result)
	}
}

func TestOneBotMarkdownToMessage_ComplexMarkdown(t *testing.T) {
	content := "## Title\n\nThis is **bold** and *italic* with `code`.\n\nVisit [link](https://example.com)."
	result := markdownToOneBotMessage(content)
	expected := "Title\n\nThis is bold and italic with code.\n\nVisit link (https://example.com)."
	if result != expected {
		t.Errorf("expected:\n%s\ngot:\n%s", expected, result)
	}
}
