package agent

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"xbot/llm"
)

// ---------------------------------------------------------------------------
// formatErrorForUser
// ---------------------------------------------------------------------------

func TestFormatErrorForUser(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "nil error returns empty",
			err:  nil,
			want: "",
		},
		{
			name: "wrapped ErrLLMGenerate returns LLM-specific message",
			err:  fmt.Errorf("wrapped: %w", ErrLLMGenerate),
			want: fmt.Sprintf("LLM 服务调用失败，请稍后重试或检查配置。\n错误详情: wrapped: %v", ErrLLMGenerate),
		},
		{
			name: "bare ErrLLMGenerate",
			err:  ErrLLMGenerate,
			want: fmt.Sprintf("LLM 服务调用失败，请稍后重试或检查配置。\n错误详情: %v", ErrLLMGenerate),
		},
		{
			name: "generic error returns generic message",
			err:  errors.New("something broke"),
			want: "处理消息时发生错误: something broke",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := formatErrorForUser(tc.err)
			if got != tc.want {
				t.Errorf("formatErrorForUser() = %q, want %q", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// sessionKey
// ---------------------------------------------------------------------------

func TestSessionKey(t *testing.T) {
	tests := []struct {
		name    string
		channel string
		chatID  string
		want    string
	}{
		{name: "normal case", channel: "telegram", chatID: "12345", want: "telegram:12345"},
		{name: "empty channel", channel: "", chatID: "12345", want: ":12345"},
		{name: "empty chatID", channel: "telegram", chatID: "", want: "telegram:"},
		{name: "both empty", channel: "", chatID: "", want: ":"},
		{name: "channel contains colon", channel: "some:channel", chatID: "abc", want: "some:channel:abc"},
		{name: "chatID contains colon", channel: "tg", chatID: "group:thread", want: "tg:group:thread"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := sessionKey(tc.channel, tc.chatID)
			if got != tc.want {
				t.Errorf("sessionKey(%q, %q) = %q, want %q", tc.channel, tc.chatID, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// resolveMemoryProvider
// ---------------------------------------------------------------------------

func TestResolveMemoryProvider(t *testing.T) {
	tests := []struct {
		name string
		cfg  string
		want string
	}{
		{name: "empty defaults to flat", cfg: "", want: "flat"},
		{name: "flat stays flat", cfg: "flat", want: "flat"},
		{name: "letta stays letta", cfg: "letta", want: "letta"},
		{name: "arbitrary value passed through", cfg: "custom", want: "custom"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveMemoryProvider(tc.cfg)
			if got != tc.want {
				t.Errorf("resolveMemoryProvider(%q) = %q, want %q", tc.cfg, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// resolveGlobalSkillsDirs
// ---------------------------------------------------------------------------

func TestResolveGlobalSkillsDirs(t *testing.T) {
	tests := []struct {
		name            string
		legacySkillsDir string
		want            []string
	}{
		{
			name:            "empty returns nil",
			legacySkillsDir: "",
			want:            nil,
		},
		{
			name:            "non-empty returns single-element slice",
			legacySkillsDir: "/path/to/skills",
			want:            []string{"/path/to/skills"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveGlobalSkillsDirs(tc.legacySkillsDir)
			if tc.want == nil {
				if got != nil {
					t.Errorf("resolveGlobalSkillsDirs(%q) = %v, want nil", tc.legacySkillsDir, got)
				}
			} else {
				if len(got) != len(tc.want) {
					t.Fatalf("length mismatch: got %d, want %d", len(got), len(tc.want))
				}
				for i := range got {
					if got[i] != tc.want[i] {
						t.Errorf("got[%d] = %q, want %q", i, got[i], tc.want[i])
					}
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// copyMessages
// ---------------------------------------------------------------------------

func TestCopyMessages(t *testing.T) {
	t.Run("returns different slice with same content", func(t *testing.T) {
		original := []llm.ChatMessage{
			{Role: "user", Content: "hello"},
			{Role: "assistant", Content: "hi"},
		}
		cpy := copyMessages(original)

		// Different backing array
		if &cpy[0] == &original[0] {
			t.Error("copyMessages returned same backing array")
		}
		// Same length and content
		if len(cpy) != len(original) {
			t.Fatalf("len = %d, want %d", len(cpy), len(original))
		}
		for i := range cpy {
			if !reflect.DeepEqual(cpy[i], original[i]) {
				t.Errorf("cpy[%d] = %v, want %v", i, cpy[i], original[i])
			}
		}
	})

	t.Run("empty slice", func(t *testing.T) {
		original := []llm.ChatMessage{}
		cpy := copyMessages(original)
		if len(cpy) != 0 {
			t.Errorf("expected empty slice, got %d elements", len(cpy))
		}
	})

	t.Run("nil input", func(t *testing.T) {
		cpy := copyMessages(nil)
		if len(cpy) != 0 {
			t.Errorf("expected empty slice for nil input, got %d elements", len(cpy))
		}
	})
}

// ---------------------------------------------------------------------------
// assertNoSystemPersist — comprehensive coverage supplement
// (basic test also exists in persist_bridge_test.go)
// ---------------------------------------------------------------------------

func TestAssertNoSystemPersistHelpers(t *testing.T) {
	t.Run("system message returns error", func(t *testing.T) {
		err := assertNoSystemPersist(llm.ChatMessage{Role: "system", Content: "sys prompt"})
		if err == nil {
			t.Error("expected error for system message, got nil")
		}
	})

	t.Run("user message returns nil", func(t *testing.T) {
		err := assertNoSystemPersist(llm.ChatMessage{Role: "user", Content: "hello"})
		if err != nil {
			t.Errorf("expected nil for user message, got %v", err)
		}
	})

	t.Run("assistant message returns nil", func(t *testing.T) {
		err := assertNoSystemPersist(llm.ChatMessage{Role: "assistant", Content: "hi"})
		if err != nil {
			t.Errorf("expected nil for assistant message, got %v", err)
		}
	})
}
