package tools

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestNewChatHistoryStore(t *testing.T) {
	t.Run("default max size", func(t *testing.T) {
		s := NewChatHistoryStore(0)
		if s.maxSize != 200 {
			t.Errorf("expected default maxSize 200, got %d", s.maxSize)
		}
	})

	t.Run("custom max size", func(t *testing.T) {
		s := NewChatHistoryStore(50)
		if s.maxSize != 50 {
			t.Errorf("expected maxSize 50, got %d", s.maxSize)
		}
	})

	t.Run("negative max size uses default", func(t *testing.T) {
		s := NewChatHistoryStore(-1)
		if s.maxSize != 200 {
			t.Errorf("expected default maxSize 200 for negative input, got %d", s.maxSize)
		}
	})
}

func TestChatHistoryStore_AddAndGet(t *testing.T) {
	s := NewChatHistoryStore(10)

	// Add messages to a channel
	s.Add("feishu", "chat1", "user1", "hello")
	s.Add("feishu", "chat1", "user2", "hi there")
	s.Add("feishu", "chat1", "user1", "how are you?")

	t.Run("get all messages", func(t *testing.T) {
		msgs := s.Get("feishu", "chat1", 0)
		if len(msgs) != 3 {
			t.Fatalf("expected 3 messages, got %d", len(msgs))
		}
		if msgs[0].Content != "hello" {
			t.Errorf("expected first message 'hello', got %q", msgs[0].Content)
		}
		if msgs[1].Content != "hi there" {
			t.Errorf("expected second message 'hi there', got %q", msgs[1].Content)
		}
		if msgs[2].Content != "how are you?" {
			t.Errorf("expected third message 'how are you?', got %q", msgs[2].Content)
		}
	})

	t.Run("get with limit", func(t *testing.T) {
		msgs := s.Get("feishu", "chat1", 2)
		if len(msgs) != 2 {
			t.Fatalf("expected 2 messages with limit=2, got %d", len(msgs))
		}
		// Should return the most recent 2
		if msgs[0].Content != "hi there" {
			t.Errorf("expected first of limited 'hi there', got %q", msgs[0].Content)
		}
		if msgs[1].Content != "how are you?" {
			t.Errorf("expected second of limited 'how are you?', got %q", msgs[1].Content)
		}
	})

	t.Run("get from non-existent channel returns nil", func(t *testing.T) {
		msgs := s.Get("feishu", "nonexistent", 10)
		if msgs != nil {
			t.Errorf("expected nil for non-existent channel, got %v", msgs)
		}
	})

	t.Run("limit exceeds total returns all", func(t *testing.T) {
		msgs := s.Get("feishu", "chat1", 100)
		if len(msgs) != 3 {
			t.Errorf("expected 3 messages with large limit, got %d", len(msgs))
		}
	})
}

func TestChatHistoryStore_MaxSize(t *testing.T) {
	s := NewChatHistoryStore(5)

	// Add 8 messages
	for i := 0; i < 8; i++ {
		s.Add("feishu", "chat1", "user1", time.Now().Format("msg_"+string(rune('A'+i))))
	}

	msgs := s.Get("feishu", "chat1", 0)
	if len(msgs) != 5 {
		t.Fatalf("expected maxSize 5 messages, got %d", len(msgs))
	}
}

func TestChatHistoryStore_ReturnsCopy(t *testing.T) {
	s := NewChatHistoryStore(10)
	s.Add("feishu", "chat1", "user1", "original")

	msgs := s.Get("feishu", "chat1", 0)
	msgs[0].Content = "modified"

	// Original should not be affected
	msgs2 := s.Get("feishu", "chat1", 0)
	if msgs2[0].Content != "original" {
		t.Errorf("Get() should return a copy; modifying returned slice affected original")
	}
}

func TestChatHistoryStore_MultipleChannels(t *testing.T) {
	s := NewChatHistoryStore(10)

	s.Add("feishu", "chat1", "user1", "feishu message")
	s.Add("onebot", "chat1", "user1", "onebot message")

	feishuMsgs := s.Get("feishu", "chat1", 0)
	onebotMsgs := s.Get("onebot", "chat1", 0)

	if len(feishuMsgs) != 1 || feishuMsgs[0].Content != "feishu message" {
		t.Errorf("feishu channel: expected 1 message 'feishu message', got %d: %v", len(feishuMsgs), feishuMsgs)
	}
	if len(onebotMsgs) != 1 || onebotMsgs[0].Content != "onebot message" {
		t.Errorf("onebot channel: expected 1 message 'onebot message', got %d: %v", len(onebotMsgs), onebotMsgs)
	}
}

func TestChatHistoryStore_ConcurrentAccess(t *testing.T) {
	s := NewChatHistoryStore(100)
	var wg sync.WaitGroup

	// Concurrently add messages
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			s.Add("feishu", "chat1", "user1", "concurrent")
		}(i)
	}

	// Concurrently read messages
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_ = s.Get("feishu", "chat1", 10)
		}(i)
	}

	wg.Wait()

	msgs := s.Get("feishu", "chat1", 0)
	if len(msgs) != 50 {
		t.Errorf("expected 50 messages after concurrent adds, got %d", len(msgs))
	}
}

func TestChatHistoryStore_EvictOldest(t *testing.T) {
	// Fill up to defaultMaxChannels (10000) to trigger eviction.
	// Each iteration creates a unique channel to ensure the history map grows.
	s := NewChatHistoryStore(10)

	for i := 0; i < defaultMaxChannels+100; i++ {
		s.Add("feishu", fmt.Sprintf("chat_evict_%d", i), "user1", "msg")
	}

	// The very last channel added should still exist (it was just created)
	msgs := s.Get("feishu", fmt.Sprintf("chat_evict_%d", defaultMaxChannels+50), 0)
	if len(msgs) == 0 {
		t.Error("expected messages in recently added channel after eviction")
	}

	// Some early channels should have been evicted
	msgsEarly := s.Get("feishu", "chat_evict_0", 0)
	if msgsEarly != nil {
		t.Error("expected earliest channel to be evicted")
	}
}

func TestChatMessage_Fields(t *testing.T) {
	msg := ChatMessage{
		Content:   "test content",
		SenderID:  "user123",
		Timestamp: time.Now(),
	}

	if msg.Content != "test content" {
		t.Errorf("expected Content 'test content', got %q", msg.Content)
	}
	if msg.SenderID != "user123" {
		t.Errorf("expected SenderID 'user123', got %q", msg.SenderID)
	}
	if msg.Timestamp.IsZero() {
		t.Error("expected non-zero Timestamp")
	}
}
