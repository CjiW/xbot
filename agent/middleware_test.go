package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"xbot/llm"
	"xbot/memory"
	"xbot/memory/letta"
)

// --- Test helpers ---

// mockMiddleware 用于测试的简单中间件
type mockMiddleware struct {
	name     string
	priority int
	process  func(mc *MessageContext) error
}

func (m *mockMiddleware) Name() string  { return m.name }
func (m *mockMiddleware) Priority() int { return m.priority }
func (m *mockMiddleware) Process(mc *MessageContext) error {
	if m.process != nil {
		return m.process(mc)
	}
	return nil
}

// mockMemoryProvider 用于测试的记忆提供者
type mockMemoryProvider struct {
	recallResult string
	recallErr    error
}

func (m *mockMemoryProvider) Recall(_ context.Context, _ string) (string, error) {
	return m.recallResult, m.recallErr
}

func (m *mockMemoryProvider) Memorize(_ context.Context, _ memory.MemorizeInput) (memory.MemorizeResult, error) {
	return memory.MemorizeResult{}, nil
}

func (m *mockMemoryProvider) Close() error { return nil }

// --- MessageContext tests ---

func TestMessageContext_BuildSystemPrompt(t *testing.T) {
	tests := []struct {
		name     string
		parts    map[string]string
		expected string
	}{
		{
			name:     "empty parts",
			parts:    map[string]string{},
			expected: "",
		},
		{
			name: "single part",
			parts: map[string]string{
				"00_base": "You are xbot.",
			},
			expected: "You are xbot.",
		},
		{
			name: "multiple parts sorted by key",
			parts: map[string]string{
				"20_memory": "# Memory\nSome memory",
				"00_base":   "You are xbot.",
				"10_skills": "# Skills\nSome skills",
			},
			expected: "You are xbot.\n# Skills\nSome skills\n# Memory\nSome memory",
		},
		{
			name: "empty parts are skipped",
			parts: map[string]string{
				"00_base":   "You are xbot.",
				"10_skills": "",
				"20_memory": "# Memory\nSome memory",
			},
			expected: "You are xbot.\n# Memory\nSome memory",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mc := &MessageContext{SystemParts: tt.parts}
			result := mc.BuildSystemPrompt()
			if result != tt.expected {
				t.Errorf("BuildSystemPrompt() =\n%q\nwant:\n%q", result, tt.expected)
			}
		})
	}
}

func TestMessageContext_Assemble(t *testing.T) {
	mc := &MessageContext{
		SystemParts: map[string]string{
			"00_base": "You are xbot.",
		},
		History: []llm.ChatMessage{
			llm.NewUserMessage("hello"),
			llm.NewAssistantMessage("hi there"),
		},
		UserMessage: "what's up?",
	}

	messages := mc.Assemble()

	if len(messages) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(messages))
	}
	if messages[0].Role != "system" {
		t.Errorf("first message should be system, got %s", messages[0].Role)
	}
	if messages[0].Content != "You are xbot." {
		t.Errorf("system content = %q, want %q", messages[0].Content, "You are xbot.")
	}
	if messages[1].Role != "user" || messages[1].Content != "hello" {
		t.Errorf("history[0] mismatch: %+v", messages[1])
	}
	if messages[2].Role != "assistant" || messages[2].Content != "hi there" {
		t.Errorf("history[1] mismatch: %+v", messages[2])
	}
	if messages[3].Role != "user" || messages[3].Content != "what's up?" {
		t.Errorf("user message mismatch: %+v", messages[3])
	}

	// Messages field should also be set
	if len(mc.Messages) != 4 {
		t.Errorf("mc.Messages should be set after Assemble, got %d", len(mc.Messages))
	}
}

func TestMessageContext_Extra(t *testing.T) {
	mc := &MessageContext{}

	// GetExtra on nil map
	_, ok := mc.GetExtra("key")
	if ok {
		t.Error("GetExtra should return false for nil map")
	}

	// SetExtra initializes map
	mc.SetExtra("key", "value")
	v, ok := mc.GetExtra("key")
	if !ok || v != "value" {
		t.Errorf("GetExtra after SetExtra: got %v, %v", v, ok)
	}
}

func TestMessageContext_GetExtraString(t *testing.T) {
	mc := &MessageContext{}

	// Missing key
	s, ok := mc.GetExtraString("missing")
	if ok || s != "" {
		t.Error("GetExtraString should return empty for missing key")
	}

	// Non-string value
	mc.SetExtra("number", 42)
	s, ok = mc.GetExtraString("number")
	if ok || s != "" {
		t.Error("GetExtraString should return false for non-string value")
	}

	// String value
	mc.SetExtra("name", "xbot")
	s, ok = mc.GetExtraString("name")
	if !ok || s != "xbot" {
		t.Errorf("GetExtraString: got %q, %v", s, ok)
	}
}

// --- Pipeline tests ---

func TestMessagePipeline_PriorityOrdering(t *testing.T) {
	var order []string

	mw1 := &mockMiddleware{
		name:     "third",
		priority: 300,
		process: func(mc *MessageContext) error {
			order = append(order, "third")
			return nil
		},
	}
	mw2 := &mockMiddleware{
		name:     "first",
		priority: 100,
		process: func(mc *MessageContext) error {
			order = append(order, "first")
			return nil
		},
	}
	mw3 := &mockMiddleware{
		name:     "second",
		priority: 200,
		process: func(mc *MessageContext) error {
			order = append(order, "second")
			return nil
		},
	}

	pipeline := NewMessagePipeline(mw1, mw2, mw3)
	mc := &MessageContext{
		SystemParts: make(map[string]string),
		UserContent: "test",
		UserMessage: "test",
	}

	pipeline.Run(mc)

	expected := []string{"first", "second", "third"}
	if len(order) != len(expected) {
		t.Fatalf("expected %d calls, got %d", len(expected), len(order))
	}
	for i, v := range expected {
		if order[i] != v {
			t.Errorf("order[%d] = %q, want %q", i, order[i], v)
		}
	}
}

func TestMessagePipeline_StableSort(t *testing.T) {
	var order []string

	// Same priority — should maintain insertion order
	mw1 := &mockMiddleware{
		name:     "a",
		priority: 100,
		process: func(mc *MessageContext) error {
			order = append(order, "a")
			return nil
		},
	}
	mw2 := &mockMiddleware{
		name:     "b",
		priority: 100,
		process: func(mc *MessageContext) error {
			order = append(order, "b")
			return nil
		},
	}
	mw3 := &mockMiddleware{
		name:     "c",
		priority: 100,
		process: func(mc *MessageContext) error {
			order = append(order, "c")
			return nil
		},
	}

	pipeline := NewMessagePipeline(mw1, mw2, mw3)
	mc := &MessageContext{
		SystemParts: make(map[string]string),
		UserContent: "test",
		UserMessage: "test",
	}

	pipeline.Run(mc)

	expected := []string{"a", "b", "c"}
	for i, v := range expected {
		if order[i] != v {
			t.Errorf("stable sort broken: order[%d] = %q, want %q", i, order[i], v)
		}
	}
}

func TestMessagePipeline_ErrorContinues(t *testing.T) {
	var order []string

	mw1 := &mockMiddleware{
		name:     "ok1",
		priority: 100,
		process: func(mc *MessageContext) error {
			order = append(order, "ok1")
			return nil
		},
	}
	mw2 := &mockMiddleware{
		name:     "fail",
		priority: 200,
		process: func(mc *MessageContext) error {
			order = append(order, "fail")
			return fmt.Errorf("something went wrong")
		},
	}
	mw3 := &mockMiddleware{
		name:     "ok2",
		priority: 300,
		process: func(mc *MessageContext) error {
			order = append(order, "ok2")
			return nil
		},
	}

	pipeline := NewMessagePipeline(mw1, mw2, mw3)
	mc := &MessageContext{
		SystemParts: make(map[string]string),
		UserContent: "test",
		UserMessage: "test",
	}

	messages := pipeline.Run(mc)

	// All three should have run despite error
	if len(order) != 3 {
		t.Fatalf("expected 3 calls (error should not stop pipeline), got %d: %v", len(order), order)
	}

	// Messages should still be assembled
	if len(messages) < 1 {
		t.Error("messages should be assembled even with middleware errors")
	}
}

func TestMessagePipeline_Use(t *testing.T) {
	pipeline := NewMessagePipeline()

	mw1 := &mockMiddleware{name: "a", priority: 200}
	mw2 := &mockMiddleware{name: "b", priority: 100}

	pipeline.Use(mw1)
	pipeline.Use(mw2)

	mws := pipeline.Middlewares()
	if len(mws) != 2 {
		t.Fatalf("expected 2 middlewares, got %d", len(mws))
	}
	// Should be sorted by priority
	if mws[0].Name() != "b" || mws[1].Name() != "a" {
		t.Errorf("middlewares not sorted: [%s, %s]", mws[0].Name(), mws[1].Name())
	}
}

func TestMessagePipeline_Remove(t *testing.T) {
	mw1 := &mockMiddleware{name: "a", priority: 100}
	mw2 := &mockMiddleware{name: "b", priority: 200}
	mw3 := &mockMiddleware{name: "c", priority: 300}

	pipeline := NewMessagePipeline(mw1, mw2, mw3)

	// Remove existing
	if n := pipeline.Remove("b"); n != 1 {
		t.Errorf("Remove should return 1 for single existing middleware, got %d", n)
	}
	mws := pipeline.Middlewares()
	if len(mws) != 2 {
		t.Fatalf("expected 2 middlewares after remove, got %d", len(mws))
	}
	if mws[0].Name() != "a" || mws[1].Name() != "c" {
		t.Errorf("wrong middlewares after remove: [%s, %s]", mws[0].Name(), mws[1].Name())
	}

	// Remove non-existing
	if n := pipeline.Remove("nonexistent"); n != 0 {
		t.Errorf("Remove should return 0 for non-existing middleware, got %d", n)
	}
}

func TestMessagePipeline_RemoveDuplicates(t *testing.T) {
	// Simulate duplicate names (e.g., Use() called twice with same name)
	mw1 := &mockMiddleware{name: "dup", priority: 100}
	mw2 := &mockMiddleware{name: "dup", priority: 200}
	mw3 := &mockMiddleware{name: "keep", priority: 150}
	mw4 := &mockMiddleware{name: "dup", priority: 300}

	pipeline := NewMessagePipeline(mw1, mw2, mw3, mw4)

	// Should have 4 middlewares
	if len(pipeline.Middlewares()) != 4 {
		t.Fatalf("expected 4 middlewares, got %d", len(pipeline.Middlewares()))
	}

	// Remove all "dup" — should remove 3
	if n := pipeline.Remove("dup"); n != 3 {
		t.Errorf("Remove should return 3 for 3 duplicates, got %d", n)
	}

	mws := pipeline.Middlewares()
	if len(mws) != 1 {
		t.Fatalf("expected 1 middleware after removing duplicates, got %d", len(mws))
	}
	if mws[0].Name() != "keep" {
		t.Errorf("remaining middleware should be 'keep', got %q", mws[0].Name())
	}

	// Remove again — should return 0
	if n := pipeline.Remove("dup"); n != 0 {
		t.Errorf("Remove should return 0 after all removed, got %d", n)
	}
}

func TestMessagePipeline_EmptyPipeline(t *testing.T) {
	pipeline := NewMessagePipeline()
	mc := &MessageContext{
		SystemParts: make(map[string]string),
		UserContent: "hello",
		UserMessage: "hello",
	}

	messages := pipeline.Run(mc)

	// Should still produce system + user messages
	if len(messages) != 2 {
		t.Fatalf("expected 2 messages (system + user), got %d", len(messages))
	}
}

// --- Builtin middleware tests ---

func TestSkillsCatalogMiddleware(t *testing.T) {
	mc := &MessageContext{
		SystemParts: make(map[string]string),
		Extra:       make(map[string]any),
	}

	// Non-empty catalog via Extra
	mc.SetExtra(ExtraKeySkillsCatalog, "# Skills\n- deploy\n- github")
	mw := NewSkillsCatalogMiddleware()
	_ = mw.Process(mc)
	if mc.SystemParts["10_skills"] == "" {
		t.Error("skills catalog should be set")
	}

	// Empty catalog
	mc2 := &MessageContext{
		SystemParts: make(map[string]string),
		Extra:       make(map[string]any),
	}
	_ = mw.Process(mc2)
	if _, ok := mc2.SystemParts["10_skills"]; ok {
		t.Error("empty catalog should not set key")
	}
}

func TestAgentsCatalogMiddleware(t *testing.T) {
	mc := &MessageContext{
		SystemParts: make(map[string]string),
		Extra:       make(map[string]any),
	}

	mc.SetExtra(ExtraKeyAgentsCatalog, "# Agents\n- code-reviewer")
	mw := NewAgentsCatalogMiddleware()
	_ = mw.Process(mc)
	if mc.SystemParts["15_agents"] == "" {
		t.Error("agents catalog should be set")
	}
}

func TestMemoryMiddleware(t *testing.T) {
	t.Run("nil provider", func(t *testing.T) {
		mc := &MessageContext{
			SystemParts: make(map[string]string),
			Extra:       make(map[string]any),
		}
		mw := NewMemoryMiddleware()
		err := mw.Process(mc)
		if err != nil {
			t.Errorf("nil provider should not error: %v", err)
		}
		if _, ok := mc.SystemParts["20_memory"]; ok {
			t.Error("nil provider should not set memory")
		}
	})

	t.Run("successful recall", func(t *testing.T) {
		mc := &MessageContext{
			Ctx:         context.Background(),
			SystemParts: make(map[string]string),
			UserContent: "hello",
			Extra:       make(map[string]any),
		}
		mc.SetExtra(ExtraKeyMemoryProvider, &mockMemoryProvider{
			recallResult: "## Core Memory\nSome facts",
		})
		mw := NewMemoryMiddleware()
		err := mw.Process(mc)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		mem := mc.SystemParts["20_memory"]
		if !strings.Contains(mem, "Some facts") {
			t.Errorf("memory should contain recall result, got: %q", mem)
		}
	})

	t.Run("recall error", func(t *testing.T) {
		mc := &MessageContext{
			Ctx:         context.Background(),
			SystemParts: make(map[string]string),
			UserContent: "hello",
			Extra:       make(map[string]any),
		}
		mc.SetExtra(ExtraKeyMemoryProvider, &mockMemoryProvider{
			recallErr: fmt.Errorf("db connection failed"),
		})
		mw := NewMemoryMiddleware()
		err := mw.Process(mc)
		if err == nil {
			t.Error("expected error from failed recall")
		}
	})

	t.Run("empty recall", func(t *testing.T) {
		mc := &MessageContext{
			Ctx:         context.Background(),
			SystemParts: make(map[string]string),
			UserContent: "hello",
			Extra:       make(map[string]any),
		}
		mc.SetExtra(ExtraKeyMemoryProvider, &mockMemoryProvider{recallResult: ""})
		mw := NewMemoryMiddleware()
		err := mw.Process(mc)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if _, ok := mc.SystemParts["20_memory"]; ok {
			t.Error("empty recall should not set memory key")
		}
	})
}

func TestSenderInfoMiddleware(t *testing.T) {
	t.Run("with sender name", func(t *testing.T) {
		mc := &MessageContext{
			SystemParts: make(map[string]string),
			SenderName:  "Alice",
		}
		mw := NewSenderInfoMiddleware()
		_ = mw.Process(mc)
		if !strings.Contains(mc.SystemParts["30_sender"], "Alice") {
			t.Errorf("sender info should contain name, got: %q", mc.SystemParts["30_sender"])
		}
	})

	t.Run("without sender name", func(t *testing.T) {
		mc := &MessageContext{
			SystemParts: make(map[string]string),
		}
		mw := NewSenderInfoMiddleware()
		_ = mw.Process(mc)
		if _, ok := mc.SystemParts["30_sender"]; ok {
			t.Error("empty sender should not set key")
		}
	})
}

func TestUserMessageMiddleware(t *testing.T) {
	t.Run("with sender name", func(t *testing.T) {
		mc := &MessageContext{
			UserContent: "hello world",
			SenderName:  "Bob",
		}
		mw := NewUserMessageMiddleware("flat")
		_ = mw.Process(mc)

		if !strings.Contains(mc.UserMessage, "Bob") {
			t.Error("user message should contain sender name")
		}
		if !strings.Contains(mc.UserMessage, "hello world") {
			t.Error("user message should contain original content")
		}
		if !strings.Contains(mc.UserMessage, "Skill") {
			t.Error("user message should contain system guidance")
		}
	})

	t.Run("without sender name", func(t *testing.T) {
		mc := &MessageContext{
			UserContent: "hello world",
		}
		mw := NewUserMessageMiddleware("flat")
		_ = mw.Process(mc)

		if strings.Contains(mc.UserMessage, "[]") {
			t.Error("user message should not have empty sender brackets")
		}
		if !strings.Contains(mc.UserMessage, "hello world") {
			t.Error("user message should contain original content")
		}
	})
}

func TestCronSystemPromptMiddleware(t *testing.T) {
	mc := &MessageContext{
		SystemParts: make(map[string]string),
		UserContent: "check server status",
	}
	mw := NewCronSystemPromptMiddleware("/work")
	_ = mw.Process(mc)

	base := mc.SystemParts["00_base"]
	if !strings.Contains(base, "/work") {
		t.Error("cron prompt should contain work dir")
	}
	if !strings.Contains(base, "scheduled") {
		t.Error("cron prompt should mention scheduled task")
	}
	if mc.UserMessage != "check server status" {
		t.Errorf("cron should use raw user content, got: %q", mc.UserMessage)
	}
}

// --- Integration test: full pipeline output structure ---

func TestPipeline_FullIntegration(t *testing.T) {
	// Build a pipeline with all standard middlewares
	loader := NewPromptLoader("") // uses default template

	pipeline := NewMessagePipeline(
		NewSystemPromptMiddleware(loader, "flat"),
		NewSkillsCatalogMiddleware(),
		NewAgentsCatalogMiddleware(),
		NewMemoryMiddleware(),
		NewSenderInfoMiddleware(),
		NewUserMessageMiddleware("flat"),
	)

	mc := &MessageContext{
		Ctx:         context.Background(),
		SystemParts: make(map[string]string),
		UserContent: "hello",
		History: []llm.ChatMessage{
			llm.NewUserMessage("previous message"),
			llm.NewAssistantMessage("previous response"),
		},
		Channel:    "feishu",
		WorkDir:    "/work",
		SenderName: "TestUser",
		Extra:      make(map[string]any),
	}
	mc.SetExtra(ExtraKeySkillsCatalog, "# Available Skills\n- deploy")
	mc.SetExtra(ExtraKeyAgentsCatalog, "# Available Agents\n- code-reviewer")
	mc.SetExtra(ExtraKeyMemoryProvider, &mockMemoryProvider{recallResult: "## Persona\nI am xbot"})

	messages := pipeline.Run(mc)

	// Verify structure: system + 2 history + user = 4 messages
	if len(messages) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(messages))
	}

	// System message should contain all parts in order
	sys := messages[0].Content
	if !strings.Contains(sys, "xbot") {
		t.Error("system should contain base prompt")
	}
	if !strings.Contains(sys, "deploy") {
		t.Error("system should contain skills catalog")
	}
	if !strings.Contains(sys, "code-reviewer") {
		t.Error("system should contain agents catalog")
	}
	if !strings.Contains(sys, "I am xbot") {
		t.Error("system should contain memory")
	}
	if !strings.Contains(sys, "TestUser") {
		t.Error("system should contain sender info")
	}

	// Verify ordering: base < skills < agents < memory < sender
	baseIdx := strings.Index(sys, "xbot")
	skillsIdx := strings.Index(sys, "deploy")
	memIdx := strings.Index(sys, "I am xbot")
	senderIdx := strings.Index(sys, "TestUser")

	if baseIdx > skillsIdx {
		t.Error("base should come before skills")
	}
	if skillsIdx > memIdx {
		t.Error("skills should come before memory")
	}
	if memIdx > senderIdx {
		t.Error("memory should come before sender")
	}

	// User message should have timestamp and guidance
	userMsg := messages[3].Content
	if !strings.Contains(userMsg, "hello") {
		t.Error("user message should contain original content")
	}
	if !strings.Contains(userMsg, "Skill") {
		t.Error("user message should contain system guidance")
	}
}

// --- Test Cron pipeline ---

func TestCronPipeline(t *testing.T) {
	pipeline := NewMessagePipeline(
		NewCronSystemPromptMiddleware("/work"),
	)

	mc := NewCronMessageContext("remind me to standup")

	messages := pipeline.Run(mc)

	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages))
	}
	if messages[0].Role != "system" {
		t.Error("first should be system")
	}
	if messages[1].Content != "remind me to standup" {
		t.Errorf("user message should be raw content, got: %q", messages[1].Content)
	}
}

// --- Test NewMessageContext / NewCronMessageContext ---

func TestNewMessageContext(t *testing.T) {
	ctx := context.Background()
	mc := NewMessageContext(ctx, "hello", nil, "feishu", "/work", "Alice", "user1", "chat1")

	if mc.Ctx != ctx {
		t.Error("Ctx should be set")
	}
	if mc.UserContent != "hello" {
		t.Error("UserContent should be set")
	}
	if mc.Channel != "feishu" {
		t.Error("Channel should be set")
	}
	if mc.WorkDir != "/work" {
		t.Error("WorkDir should be set")
	}
	if mc.SenderName != "Alice" {
		t.Error("SenderName should be set")
	}
	if mc.SenderID != "user1" {
		t.Error("SenderID should be set")
	}
	if mc.ChatID != "chat1" {
		t.Error("ChatID should be set")
	}
	if mc.SystemParts == nil {
		t.Error("SystemParts should be initialized")
	}
	if mc.Extra == nil {
		t.Error("Extra should be initialized")
	}
}

func TestNewCronMessageContext(t *testing.T) {
	mc := NewCronMessageContext("check status")

	if mc.UserContent != "check status" {
		t.Error("UserContent should be set")
	}
	if mc.SystemParts == nil {
		t.Error("SystemParts should be initialized")
	}
	if mc.Extra == nil {
		t.Error("Extra should be initialized")
	}
}

// --- Test Pipeline dynamic Use/Remove ---

func TestPipeline_DynamicUseRemove(t *testing.T) {
	loader := NewPromptLoader("")
	pipeline := NewMessagePipeline(
		NewSystemPromptMiddleware(loader, "flat"),
		NewSkillsCatalogMiddleware(),
		NewUserMessageMiddleware("flat"),
	)

	// Add a custom middleware
	custom := &mockMiddleware{
		name:     "custom_injector",
		priority: 150,
		process: func(mc *MessageContext) error {
			mc.SystemParts["16_custom"] = "# Custom\nInjected by custom middleware"
			return nil
		},
	}
	pipeline.Use(custom)

	mc := &MessageContext{
		SystemParts: make(map[string]string),
		UserContent: "test",
		Extra:       make(map[string]any),
	}
	mc.SetExtra(ExtraKeySkillsCatalog, "# Skills\n- deploy")

	messages := pipeline.Run(mc)
	sys := messages[0].Content
	if !strings.Contains(sys, "Injected by custom middleware") {
		t.Error("custom middleware should inject content")
	}

	// Remove the custom middleware
	pipeline.Remove("custom_injector")

	mc2 := &MessageContext{
		SystemParts: make(map[string]string),
		UserContent: "test2",
		Extra:       make(map[string]any),
	}
	messages2 := pipeline.Run(mc2)
	sys2 := messages2[0].Content
	if strings.Contains(sys2, "Injected by custom middleware") {
		t.Error("custom middleware should be removed")
	}
}

// --- Test full pipeline with all standard middlewares ---

func TestFullPipeline_AllMiddlewares(t *testing.T) {
	loader := NewPromptLoader("")
	mem := &mockMemoryProvider{recallResult: "## Persona\nI am xbot"}

	pipeline := NewMessagePipeline(
		NewSystemPromptMiddleware(loader, "flat"),
		NewSkillsCatalogMiddleware(),
		NewAgentsCatalogMiddleware(),
		NewMemoryMiddleware(),
		NewSenderInfoMiddleware(),
		NewUserMessageMiddleware("flat"),
	)

	mc := NewMessageContext(context.Background(), "hello", []llm.ChatMessage{llm.NewUserMessage("prev")}, "feishu", "/work", "TestUser", "", "")
	mc.SetExtra(ExtraKeySkillsCatalog, "# Skills\n- deploy")
	mc.SetExtra(ExtraKeyAgentsCatalog, "# Agents\n- reviewer")
	mc.SetExtra(ExtraKeyMemoryProvider, mem)

	messages := pipeline.Run(mc)

	if len(messages) != 3 { // system + 1 history + user
		t.Fatalf("expected 3 messages, got %d", len(messages))
	}
	sys := messages[0].Content
	if !strings.Contains(sys, "deploy") {
		t.Error("should contain skills")
	}
	if !strings.Contains(sys, "reviewer") {
		t.Error("should contain agents")
	}
	if !strings.Contains(sys, "I am xbot") {
		t.Error("should contain memory")
	}
}

// --- Concurrency safety tests ---

func TestMessagePipeline_ConcurrentRun(t *testing.T) {
	// Verify that multiple goroutines can call Run() concurrently without data races.
	// Run with: go test -race ./agent/...
	mw1 := &mockMiddleware{
		name:     "base",
		priority: 0,
		process: func(mc *MessageContext) error {
			mc.SystemParts["00_base"] = "You are xbot."
			return nil
		},
	}
	mw2 := &mockMiddleware{
		name:     "skills",
		priority: 100,
		process: func(mc *MessageContext) error {
			catalog, _ := mc.GetExtraString(ExtraKeySkillsCatalog)
			if catalog != "" {
				mc.SystemParts["10_skills"] = catalog
			}
			return nil
		},
	}
	mw3 := &mockMiddleware{
		name:     "user_msg",
		priority: 200,
		process: func(mc *MessageContext) error {
			mc.UserMessage = mc.UserContent
			return nil
		},
	}

	pipeline := NewMessagePipeline(mw1, mw2, mw3)

	const goroutines = 50
	done := make(chan bool, goroutines)

	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer func() { done <- true }()
			mc := &MessageContext{
				SystemParts: make(map[string]string),
				UserContent: fmt.Sprintf("message from goroutine %d", id),
				Extra:       make(map[string]any),
			}
			mc.SetExtra(ExtraKeySkillsCatalog, "# Skills\n- deploy")

			messages := pipeline.Run(mc)
			if len(messages) < 2 {
				t.Errorf("goroutine %d: expected at least 2 messages, got %d", id, len(messages))
			}
			if messages[0].Role != "system" {
				t.Errorf("goroutine %d: first message should be system", id)
			}
		}(i)
	}

	for i := 0; i < goroutines; i++ {
		<-done
	}
}

func TestMessagePipeline_ConcurrentRunAndUse(t *testing.T) {
	// Verify that Run() and Use() can be called concurrently without data races.
	mw1 := &mockMiddleware{
		name:     "base",
		priority: 0,
		process: func(mc *MessageContext) error {
			mc.SystemParts["00_base"] = "You are xbot."
			mc.UserMessage = mc.UserContent
			return nil
		},
	}

	pipeline := NewMessagePipeline(mw1)

	const goroutines = 50
	done := make(chan bool, goroutines*2)

	// Half goroutines do Run()
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer func() { done <- true }()
			mc := &MessageContext{
				SystemParts: make(map[string]string),
				UserContent: fmt.Sprintf("msg %d", id),
				Extra:       make(map[string]any),
			}
			messages := pipeline.Run(mc)
			if len(messages) < 2 {
				t.Errorf("goroutine %d: expected at least 2 messages", id)
			}
		}(i)
	}

	// Other half goroutines do Use() and Remove()
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer func() { done <- true }()
			name := fmt.Sprintf("dynamic_%d", id)
			mw := &mockMiddleware{
				name:     name,
				priority: 150,
				process: func(mc *MessageContext) error {
					mc.SystemParts["15_dynamic"] = "dynamic content"
					return nil
				},
			}
			pipeline.Use(mw)
			pipeline.Remove(name)
		}(i)
	}

	for i := 0; i < goroutines*2; i++ {
		<-done
	}
}

// --- Regression test: verify userID propagates through buildPrompt → pipeline → middleware ---
// This test ensures that letta.WithUserID(ctx, senderID) is correctly passed to
// NewMessageContext and available in MessageContext.Ctx for per-user human block.
// See PR #112: https://github.com/CjiW/xbot/pull/112

func TestUserIDPropagationThroughPipeline(t *testing.T) {
	// Create context with userID (simulating processMessage's ctx = letta.WithUserID(ctx, msg.SenderID))
	originalCtx := context.Background()
	testUserID := "test-user-123"
	ctxWithUserID := letta.WithUserID(originalCtx, testUserID)

	// Create MessageContext with ctx that has userID (simulating buildPrompt)
	mc := NewMessageContext(
		ctxWithUserID, // This should propagate userID through pipeline
		"hello",
		nil,
		"feishu",
		"/workspace",
		"TestUser",
		testUserID, // senderID
		"chat123",
	)

	// Verify userID is in the context (the fix from PR #112)
	if mc.Ctx == nil {
		t.Fatal("MessageContext.Ctx should not be nil")
	}

	capturedUserID := letta.GetUserID(mc.Ctx)
	if capturedUserID != testUserID {
		t.Errorf("MessageContext.Ctx should contain userID %q, got %q", testUserID, capturedUserID)
	}
}
