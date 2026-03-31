// xbot CLI Channel unit tests
// Tests for CLIChannel and cliModel functionality

package channel

import (
	"strings"
	"testing"
	"time"

	"xbot/bus"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ---------------------------------------------------------------------------
// CLIChannel Basic Tests
// ---------------------------------------------------------------------------

func TestCLIChannelName(t *testing.T) {
	msgBus := bus.NewMessageBus()
	ch := NewCLIChannel(CLIChannelConfig{}, msgBus)

	if got := ch.Name(); got != "cli" {
		t.Errorf("CLIChannel.Name() = %q, want %q", got, "cli")
	}
}

func TestCLIChannelStartStop(t *testing.T) {
	// Skip in CI/headless environment - requires TTY
	if testing.Short() {
		t.Skip("Skipping in short mode - requires TTY")
	}

	msgBus := bus.NewMessageBus()
	ch := NewCLIChannel(CLIChannelConfig{}, msgBus)

	// Start in goroutine since it blocks
	startErr := make(chan error, 1)
	go func() {
		startErr <- ch.Start()
	}()

	// Give it time to initialize
	time.Sleep(100 * time.Millisecond)

	// Stop should terminate the program
	ch.Stop()

	select {
	case err := <-startErr:
		// Start may return error in headless env, that's OK
		_ = err
	case <-time.After(2 * time.Second):
		t.Error("Start() did not return after Stop() within timeout")
	}
}

// ---------------------------------------------------------------------------
// CLIChannel Send Tests
// ---------------------------------------------------------------------------

func TestCLIChannelSend(t *testing.T) {
	msgBus := bus.NewMessageBus()
	ch := NewCLIChannel(CLIChannelConfig{}, msgBus)

	// Send without starting should still work (messages buffered)
	msg := bus.OutboundMessage{
		Channel:   "cli",
		ChatID:    "cli_user",
		Content:   "Hello, CLI!",
		IsPartial: false,
	}

	msgID, err := ch.Send(msg)
	if err != nil {
		t.Errorf("Send() returned error: %v", err)
	}
	if msgID == "" {
		t.Error("Send() returned empty message ID")
	}
}

func TestCLIChannelSendPartial(t *testing.T) {
	msgBus := bus.NewMessageBus()
	ch := NewCLIChannel(CLIChannelConfig{}, msgBus)

	// Send partial (streaming) message
	msg := bus.OutboundMessage{
		Channel:   "cli",
		ChatID:    "cli_user",
		Content:   "Thinking...",
		IsPartial: true,
	}

	msgID, err := ch.Send(msg)
	if err != nil {
		t.Errorf("Send() partial returned error: %v", err)
	}
	if msgID == "" {
		t.Error("Send() partial returned empty message ID")
	}
}

func TestCLIChannelSendComplete(t *testing.T) {
	msgBus := bus.NewMessageBus()
	ch := NewCLIChannel(CLIChannelConfig{}, msgBus)

	// Send complete message
	msg := bus.OutboundMessage{
		Channel:   "cli",
		ChatID:    "cli_user",
		Content:   "Final response",
		IsPartial: false,
	}

	msgID, err := ch.Send(msg)
	if err != nil {
		t.Errorf("Send() complete returned error: %v", err)
	}
	if msgID == "" {
		t.Error("Send() complete returned empty message ID")
	}
}

func TestCLIChannelSendBufferOverflow(t *testing.T) {
	msgBus := bus.NewMessageBus()
	ch := NewCLIChannel(CLIChannelConfig{}, msgBus)

	// Send more messages than buffer size to test non-blocking behavior
	for i := 0; i < cliMsgBufSize+10; i++ {
		msg := bus.OutboundMessage{
			Content: "message",
		}
		_, err := ch.Send(msg)
		if err != nil {
			t.Errorf("Send() at iteration %d returned error: %v", i, err)
		}
	}
	// Should not block or panic
}

func TestCLIChannelSendProgress(t *testing.T) {
	msgBus := bus.NewMessageBus()
	ch := NewCLIChannel(CLIChannelConfig{}, msgBus)

	// SendProgress with nil payload should not panic
	ch.SendProgress("test_chat", nil)

	// SendProgress without program should not panic
	payload := &CLIProgressPayload{
		Phase:     "thinking",
		Iteration: 1,
	}
	ch.SendProgress("test_chat", payload)
	// Should not panic
}

// ---------------------------------------------------------------------------
// CLIChannel Edge Cases
// ---------------------------------------------------------------------------

func TestCLIChannelSendEmptyMessage(t *testing.T) {
	msgBus := bus.NewMessageBus()
	ch := NewCLIChannel(CLIChannelConfig{}, msgBus)

	msg := bus.OutboundMessage{
		Channel:   "cli",
		ChatID:    "cli_user",
		Content:   "", // empty content
		IsPartial: false,
	}

	msgID, err := ch.Send(msg)
	if err != nil {
		t.Errorf("Send() empty message returned error: %v", err)
	}
	if msgID == "" {
		t.Error("Send() empty message returned empty ID")
	}
}

func TestCLIChannelSendLongMessage(t *testing.T) {
	msgBus := bus.NewMessageBus()
	ch := NewCLIChannel(CLIChannelConfig{}, msgBus)

	// Create a very long message
	longContent := strings.Repeat("This is a long message. ", 1000)

	msg := bus.OutboundMessage{
		Channel:   "cli",
		ChatID:    "cli_user",
		Content:   longContent,
		IsPartial: false,
	}

	msgID, err := ch.Send(msg)
	if err != nil {
		t.Errorf("Send() long message returned error: %v", err)
	}
	if msgID == "" {
		t.Error("Send() long message returned empty ID")
	}
}

func TestCLIChannelSendWithMetadata(t *testing.T) {
	msgBus := bus.NewMessageBus()
	ch := NewCLIChannel(CLIChannelConfig{}, msgBus)

	msg := bus.OutboundMessage{
		Channel:   "cli",
		ChatID:    "cli_user",
		Content:   "Message with metadata",
		Metadata:  map[string]string{"key": "value"},
		IsPartial: false,
	}

	msgID, err := ch.Send(msg)
	if err != nil {
		t.Errorf("Send() with metadata returned error: %v", err)
	}
	if msgID == "" {
		t.Error("Send() with metadata returned empty ID")
	}
}

func TestCLIChannelSendWithMedia(t *testing.T) {
	msgBus := bus.NewMessageBus()
	ch := NewCLIChannel(CLIChannelConfig{}, msgBus)

	msg := bus.OutboundMessage{
		Channel:   "cli",
		ChatID:    "cli_user",
		Content:   "Message with media",
		Media:     []string{"/path/to/file1.txt", "/path/to/file2.png"},
		IsPartial: false,
	}

	msgID, err := ch.Send(msg)
	if err != nil {
		t.Errorf("Send() with media returned error: %v", err)
	}
	if msgID == "" {
		t.Error("Send() with media returned empty ID")
	}
}

// ---------------------------------------------------------------------------
// cliModel Tests
// ---------------------------------------------------------------------------

func TestCLIModelInit(t *testing.T) {
	model := newCLIModel()

	cmd := model.Init()
	if cmd == nil {
		t.Error("Init() returned nil command")
	}
}

func TestCLIModelSetMsgBus(t *testing.T) {
	model := newCLIModel()
	msgBus := bus.NewMessageBus()

	model.SetMsgBus(msgBus)

	if model.msgBus != msgBus {
		t.Error("SetMsgBus() did not set msgBus correctly")
	}
}

func TestCLIModelHandleResize(t *testing.T) {
	model := newCLIModel()

	// Test resize
	model.handleResize(120, 40)

	if model.width != 120 {
		t.Errorf("handleResize() width = %d, want 120", model.width)
	}
	if model.height != 40 {
		t.Errorf("handleResize() height = %d, want 40", model.height)
	}
	if !model.ready {
		t.Error("handleResize() should set ready to true")
	}
}

func TestCLIModelHandleResizeMinimum(t *testing.T) {
	model := newCLIModel()

	// Test very small resize
	model.handleResize(10, 10)

	if model.width != 10 {
		t.Errorf("handleResize() width = %d, want 10", model.width)
	}
	// Should not panic
}

func TestCLIModelHandleResizeWithProgress(t *testing.T) {
	model := newCLIModel()
	model.progress = &CLIProgressPayload{
		Phase: "tool_exec",
		ActiveTools: []CLIToolProgress{
			{Name: "test", Label: "Testing"},
		},
	}

	model.handleResize(80, 30)

	if model.viewport.Height <= 0 {
		t.Error("viewport height should be positive")
	}
}

func TestCLIModelCalculateProgressHeight(t *testing.T) {
	model := newCLIModel()

	// No progress
	if h := model.calculateProgressHeight(); h != 0 {
		t.Errorf("calculateProgressHeight() with no progress = %d, want 0", h)
	}

	// With progress
	model.progress = &CLIProgressPayload{
		Phase: "tool_exec",
		ActiveTools: []CLIToolProgress{
			{Name: "read", Label: "Reading file"},
			{Name: "grep", Label: "Searching"},
		},
		SubAgents: []CLISubAgent{
			{Role: "reviewer", Status: "running"},
		},
		Thinking: "Analyzing code...",
	}

	height := model.calculateProgressHeight()
	// Base(1) + ActiveTools(2) + SubAgents(1) + Thinking(1) = 5
	if height < 3 {
		t.Errorf("calculateProgressHeight() = %d, want at least 3", height)
	}
}

func TestCLIModelCalculateProgressHeightOnlyActiveTools(t *testing.T) {
	model := newCLIModel()
	model.progress = &CLIProgressPayload{
		Phase: "tool_exec",
		ActiveTools: []CLIToolProgress{
			{Name: "read", Label: "Reading file"},
		},
	}

	height := model.calculateProgressHeight()
	if height < 2 {
		t.Errorf("calculateProgressHeight() with active tools = %d, want at least 2", height)
	}
}

func TestCLIModelCalculateProgressHeightOnlySubAgents(t *testing.T) {
	model := newCLIModel()
	model.progress = &CLIProgressPayload{
		Phase: "tool_exec",
		SubAgents: []CLISubAgent{
			{Role: "reviewer", Status: "done"},
		},
	}

	height := model.calculateProgressHeight()
	if height < 2 {
		t.Errorf("calculateProgressHeight() with subagents = %d, want at least 2", height)
	}
}

func TestCLIModelViewNotReady(t *testing.T) {
	model := newCLIModel()
	model.ready = false

	view := model.View()
	if !strings.Contains(view, "初始化") {
		t.Errorf("View() when not ready should show initializing message, got: %q", view)
	}
}

func TestCLIModelViewReady(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)

	view := model.View()
	// Should contain title and UI elements
	if view == "" {
		t.Error("View() returned empty string")
	}
}

func TestCLIModelViewWithTyping(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.typing = true

	view := model.View()
	if view == "" {
		t.Error("View() returned empty string")
	}
}

func TestCLIModelViewWithProgress(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.progress = &CLIProgressPayload{
		Phase:     "thinking",
		Iteration: 1,
	}

	view := model.View()
	if view == "" {
		t.Error("View() returned empty string")
	}
}

func TestCLIModelViewWithMessages(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.messages = []cliMessage{
		{role: "user", content: "Hello", timestamp: time.Now()},
		{role: "assistant", content: "Hi there!", timestamp: time.Now()},
	}

	view := model.View()
	if view == "" {
		t.Error("View() returned empty string")
	}
}

// ---------------------------------------------------------------------------
// cliModel Handle Agent Message Tests
// ---------------------------------------------------------------------------

func TestCLIModelHandleAgentMessage(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)

	// Test complete message
	msg := bus.OutboundMessage{
		Content:   "Hello from agent",
		IsPartial: false,
	}

	model.handleAgentMessage(msg)

	if len(model.messages) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(model.messages))
	}
	if model.messages[0].role != "assistant" {
		t.Errorf("Message role = %q, want 'assistant'", model.messages[0].role)
	}
	if model.messages[0].content != "Hello from agent" {
		t.Errorf("Message content = %q, want 'Hello from agent'", model.messages[0].content)
	}
	if model.typing {
		t.Error("typing should be false after complete message")
	}
	if !model.inputReady {
		t.Error("inputReady should be true after complete message")
	}
}

func TestCLIModelHandleAgentMessagePartial(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)

	// First partial message
	msg1 := bus.OutboundMessage{
		Content:   "Thinking...",
		IsPartial: true,
	}
	model.handleAgentMessage(msg1)

	if len(model.messages) != 1 {
		t.Fatalf("Expected 1 message after first partial, got %d", len(model.messages))
	}
	if !model.messages[0].isPartial {
		t.Error("Message should be partial")
	}

	// Second partial (update)
	msg2 := bus.OutboundMessage{
		Content:   "Still thinking...",
		IsPartial: true,
	}
	model.handleAgentMessage(msg2)

	// Should update same message
	if len(model.messages) != 1 {
		t.Errorf("Expected 1 message after second partial, got %d", len(model.messages))
	}

	// Complete message
	msg3 := bus.OutboundMessage{
		Content:   "Final answer",
		IsPartial: false,
	}
	model.handleAgentMessage(msg3)

	if model.messages[0].isPartial {
		t.Error("Message should not be partial after complete")
	}
	if model.typing {
		t.Error("typing should be false after complete")
	}
	if model.streamingMsgIdx != -1 {
		t.Error("streamingMsgIdx should be -1 after complete")
	}
}

func TestCLIModelHandleAgentMessageMultiplePartials(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)

	// Multiple partial updates
	for i := 0; i < 5; i++ {
		msg := bus.OutboundMessage{
			Content:   "Partial content " + string(rune('A'+i)),
			IsPartial: true,
		}
		model.handleAgentMessage(msg)
	}

	if len(model.messages) != 1 {
		t.Errorf("Expected 1 message after multiple partials, got %d", len(model.messages))
	}

	// Complete
	model.handleAgentMessage(bus.OutboundMessage{
		Content:   "Final",
		IsPartial: false,
	})

	if model.streamingMsgIdx != -1 {
		t.Error("streamingMsgIdx should be -1 after complete")
	}
}

func TestCLIModelHandleAgentMessageWithFeishuCard(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)

	// Test Feishu card conversion
	cardContent := `__FEISHU_CARD__:id:{"header":{"title":{"content":"Card Title"}},"elements":[]}`
	msg := bus.OutboundMessage{
		Content:   cardContent,
		IsPartial: false,
	}

	model.handleAgentMessage(msg)

	if len(model.messages) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(model.messages))
	}
	// Content should be converted (contain "Card Title")
	if !strings.Contains(model.messages[0].content, "Card Title") {
		t.Errorf("Feishu card not converted, content: %q", model.messages[0].content)
	}
}

func TestCLIModelHandleAgentMessageFeishuCardWithElements(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)

	cardContent := `__FEISHU_CARD__:id:{"header":{"title":{"content":"Test"}},"elements":[{"tag":"markdown","content":"**bold** text"},{"tag":"div","text":"plain"}]}`
	msg := bus.OutboundMessage{
		Content:   cardContent,
		IsPartial: false,
	}

	model.handleAgentMessage(msg)

	if len(model.messages) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(model.messages))
	}
}

func TestCLIModelHandleAgentMessageEmptyContent(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)

	msg := bus.OutboundMessage{
		Content:   "",
		IsPartial: false,
	}

	model.handleAgentMessage(msg)

	if len(model.messages) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(model.messages))
	}
	if model.messages[0].content != "" {
		t.Errorf("Message content should be empty, got: %q", model.messages[0].content)
	}
}

func TestCLIModelHandleAgentMessageMarkdownContent(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)

	markdownContent := "# Header\n\n**Bold** and *italic* text\n\n- List item 1\n- List item 2"
	msg := bus.OutboundMessage{
		Content:   markdownContent,
		IsPartial: false,
	}

	model.handleAgentMessage(msg)

	if len(model.messages) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(model.messages))
	}
}

// ---------------------------------------------------------------------------
// cliModel Update Tests
// ---------------------------------------------------------------------------

func TestCLIModelUpdateQuitOnCtrlC(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)

	// Simulate Ctrl+C
	keyMsg := tea.KeyMsg{Type: tea.KeyCtrlC}
	_, cmd := model.Update(keyMsg)

	// Should return quit command
	if cmd == nil {
		t.Error("Update(CtrlC) should return quit command")
	}
}

func TestCLIModelUpdateQuitOnEsc(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)

	// Simulate Escape
	keyMsg := tea.KeyMsg{Type: tea.KeyEsc}
	_, cmd := model.Update(keyMsg)

	// Should return quit command
	if cmd == nil {
		t.Error("Update(Esc) should return quit command")
	}
}

func TestCLIModelUpdateProgressMsg(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)

	// Send progress message
	progMsg := cliProgressMsg{
		payload: &CLIProgressPayload{
			Phase:     "thinking",
			Iteration: 1,
		},
	}

	_, _ = model.Update(progMsg)

	if model.progress == nil {
		t.Error("Progress should be set after cliProgressMsg")
	}
	if model.progress.Phase != "thinking" {
		t.Errorf("Progress phase = %q, want 'thinking'", model.progress.Phase)
	}
}

func TestCLIModelUpdateProgressDone(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)

	// Set initial progress
	model.progress = &CLIProgressPayload{Phase: "thinking"}

	// Send done progress
	progMsg := cliProgressMsg{
		payload: &CLIProgressPayload{
			Phase: "done",
		},
	}

	_, _ = model.Update(progMsg)

	// Progress should be cleared when phase is "done"
	if model.progress != nil {
		t.Error("Progress should be nil after done phase")
	}
}

func TestCLIModelUpdateProgressNilPayload(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)

	progMsg := cliProgressMsg{payload: nil}
	_, _ = model.Update(progMsg)

	// Should not panic
}

func TestCLIModelUpdateWindowSizeMsg(t *testing.T) {
	model := newCLIModel()

	// Simulate window resize
	sizeMsg := tea.WindowSizeMsg{Width: 100, Height: 30}
	_, _ = model.Update(sizeMsg)

	if model.width != 100 || model.height != 30 {
		t.Errorf("Window size not updated: width=%d, height=%d", model.width, model.height)
	}
}

func TestCLIModelUpdateTickMsg(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)

	tickMsg := cliTickMsg{}
	_, cmd := model.Update(tickMsg)

	// Should return another tick command
	if cmd == nil {
		t.Error("Update(tickMsg) should return a command")
	}
}

func TestCLIModelUpdateOutboundMsg(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)

	outMsg := cliOutboundMsg{
		msg: bus.OutboundMessage{
			Content:   "Test message",
			IsPartial: false,
		},
	}

	_, _ = model.Update(outMsg)

	if len(model.messages) != 1 {
		t.Errorf("Expected 1 message, got %d", len(model.messages))
	}
}

func TestCLIModelUpdateEnterKeyWithContent(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.inputReady = true
	model.msgBus = bus.NewMessageBus()

	// Set textarea content
	model.textarea.SetValue("Hello world")

	// Simulate Enter key
	keyMsg := tea.KeyMsg{Type: tea.KeyEnter}
	_, _ = model.Update(keyMsg)

	// Message should be added
	if len(model.messages) != 1 {
		t.Errorf("Expected 1 message after Enter, got %d", len(model.messages))
	}
}

func TestCLIModelUpdateEnterKeyEmptyContent(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.inputReady = true

	// Empty textarea
	model.textarea.SetValue("   ")

	// Simulate Enter key
	keyMsg := tea.KeyMsg{Type: tea.KeyEnter}
	_, _ = model.Update(keyMsg)

	// No message should be added
	if len(model.messages) != 0 {
		t.Errorf("Expected 0 messages for empty input, got %d", len(model.messages))
	}
}

func TestCLIModelUpdateEnterKeyInputNotReady(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.inputReady = false

	// Set textarea content
	model.textarea.SetValue("Hello world")

	// Simulate Enter key
	keyMsg := tea.KeyMsg{Type: tea.KeyEnter}
	_, _ = model.Update(keyMsg)

	// No message should be added (input not ready)
	if len(model.messages) != 0 {
		t.Errorf("Expected 0 messages when input not ready, got %d", len(model.messages))
	}
}

// ---------------------------------------------------------------------------
// Progress Rendering Tests
// ---------------------------------------------------------------------------

func TestCLIModelRenderProgressStatus(t *testing.T) {
	model := newCLIModel()

	tests := []struct {
		phase    string
		expected string
	}{
		{"thinking", "正在思考"},
		{"tool_exec", "执行工具"},
		{"compressing", "压缩上下文"},
		{"retrying", "重试中"},
		{"done", "完成"},
		{"unknown", "处理中"},
	}

	progressStyle := lipgloss.NewStyle()
	toolStyle := lipgloss.NewStyle()

	for _, tt := range tests {
		t.Run(tt.phase, func(t *testing.T) {
			model.progress = &CLIProgressPayload{Phase: tt.phase}
			result := model.renderProgressStatus(progressStyle, toolStyle)
			if !strings.Contains(result, tt.expected) {
				t.Errorf("renderProgressStatus(%s) should contain %q, got %q",
					tt.phase, tt.expected, result)
			}
		})
	}
}

func TestCLIModelRenderProgressStatusNil(t *testing.T) {
	model := newCLIModel()
	model.progress = nil

	progressStyle := lipgloss.NewStyle()
	toolStyle := lipgloss.NewStyle()

	result := model.renderProgressStatus(progressStyle, toolStyle)
	if !strings.Contains(result, "正在思考") {
		t.Errorf("renderProgressStatus with nil progress should show thinking, got: %q", result)
	}
}

func TestCLIModelRenderProgressStatusWithIteration(t *testing.T) {
	model := newCLIModel()
	model.progress = &CLIProgressPayload{
		Phase:     "thinking",
		Iteration: 5,
	}

	progressStyle := lipgloss.NewStyle()
	toolStyle := lipgloss.NewStyle()

	result := model.renderProgressStatus(progressStyle, toolStyle)

	if !strings.Contains(result, "迭代 5") {
		t.Errorf("renderProgressStatus should show iteration, got: %q", result)
	}
}

func TestCLIModelRenderProgressStatusWithActiveTools(t *testing.T) {
	model := newCLIModel()
	model.progress = &CLIProgressPayload{
		Phase: "tool_exec",
		ActiveTools: []CLIToolProgress{
			{Name: "read", Label: "Reading file", Elapsed: 100},
		},
	}

	progressStyle := lipgloss.NewStyle()
	toolStyle := lipgloss.NewStyle()

	result := model.renderProgressStatus(progressStyle, toolStyle)

	if !strings.Contains(result, "Reading file") {
		t.Errorf("renderProgressStatus should show tool label, got: %q", result)
	}
	if !strings.Contains(result, "100ms") {
		t.Errorf("renderProgressStatus should show elapsed time, got: %q", result)
	}
}

func TestCLIModelRenderProgressStatusToolWithoutLabel(t *testing.T) {
	model := newCLIModel()
	model.progress = &CLIProgressPayload{
		Phase: "tool_exec",
		ActiveTools: []CLIToolProgress{
			{Name: "read", Label: "", Elapsed: 0},
		},
	}

	progressStyle := lipgloss.NewStyle()
	toolStyle := lipgloss.NewStyle()

	result := model.renderProgressStatus(progressStyle, toolStyle)

	// Should use name when label is empty
	if !strings.Contains(result, "read") {
		t.Errorf("renderProgressStatus should show tool name when label empty, got: %q", result)
	}
}

func TestCLIModelRenderProgressStatusWithSubAgents(t *testing.T) {
	model := newCLIModel()
	model.progress = &CLIProgressPayload{
		Phase: "tool_exec",
		SubAgents: []CLISubAgent{
			{Role: "code-reviewer", Status: "running", Desc: "Reviewing code"},
		},
	}

	progressStyle := lipgloss.NewStyle()
	toolStyle := lipgloss.NewStyle()

	result := model.renderProgressStatus(progressStyle, toolStyle)

	if !strings.Contains(result, "code-reviewer") {
		t.Errorf("renderProgressStatus should show subagent role, got: %q", result)
	}
}

func TestCLIModelRenderProgressStatusSubAgentStatuses(t *testing.T) {
	model := newCLIModel()

	progressStyle := lipgloss.NewStyle()
	toolStyle := lipgloss.NewStyle()

	tests := []struct {
		status   string
		expected string
	}{
		{"running", "🔄"},
		{"done", "✅"},
		{"error", "❌"},
	}

	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			model.progress = &CLIProgressPayload{
				Phase: "tool_exec",
				SubAgents: []CLISubAgent{
					{Role: "test", Status: tt.status},
				},
			}
			result := model.renderProgressStatus(progressStyle, toolStyle)
			if !strings.Contains(result, tt.expected) {
				t.Errorf("renderProgressStatus with status %s should contain %s", tt.status, tt.expected)
			}
		})
	}
}

func TestCLIModelRenderProgressStatusSubAgentWithDesc(t *testing.T) {
	model := newCLIModel()
	model.progress = &CLIProgressPayload{
		Phase: "tool_exec",
		SubAgents: []CLISubAgent{
			{Role: "reviewer", Status: "running", Desc: "Checking code quality"},
		},
	}

	progressStyle := lipgloss.NewStyle()
	toolStyle := lipgloss.NewStyle()

	result := model.renderProgressStatus(progressStyle, toolStyle)

	if !strings.Contains(result, "Checking code quality") {
		t.Errorf("renderProgressStatus should show subagent desc, got: %q", result)
	}
}

func TestCLIModelRenderProgressStatusSubAgentChildren(t *testing.T) {
	model := newCLIModel()
	model.progress = &CLIProgressPayload{
		Phase: "tool_exec",
		SubAgents: []CLISubAgent{
			{
				Role:     "reviewer",
				Status:   "running",
				Children: []CLISubAgent{{Role: "child", Status: "done"}},
			},
		},
	}

	progressStyle := lipgloss.NewStyle()
	toolStyle := lipgloss.NewStyle()

	// Should not panic with children
	_ = model.renderProgressStatus(progressStyle, toolStyle)
}

// ---------------------------------------------------------------------------
// cliModel UpdateViewportContent Tests
// ---------------------------------------------------------------------------

func TestCLIModelUpdateViewportContent(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.messages = []cliMessage{
		{role: "user", content: "Hello", timestamp: time.Now()},
		{role: "assistant", content: "Hi there!", timestamp: time.Now(), isPartial: false},
	}

	model.updateViewportContent()

	// Viewport should have content
	if model.viewport.View() == "" {
		t.Error("updateViewportContent should set viewport content")
	}
}

func TestCLIModelUpdateViewportContentPartialMessage(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.messages = []cliMessage{
		{role: "assistant", content: "Streaming...", timestamp: time.Now(), isPartial: true},
	}

	model.updateViewportContent()

	// Should contain streaming indicator
	content := model.viewport.View()
	if content == "" {
		t.Error("updateViewportContent should set viewport content")
	}
}

func TestCLIModelUpdateViewportContentWithMarkdown(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.messages = []cliMessage{
		{role: "assistant", content: "# Header\n\n**bold**", timestamp: time.Now()},
	}

	model.updateViewportContent()

	// Should render markdown without error
	if model.viewport.View() == "" {
		t.Error("updateViewportContent should set viewport content")
	}
}

func TestCLIModelUpdateViewportContentUserMessage(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.messages = []cliMessage{
		{role: "user", content: "User message", timestamp: time.Now()},
	}

	model.updateViewportContent()

	content := model.viewport.View()
	if !strings.Contains(content, "You") {
		t.Error("User message should contain 'You' label")
	}
}

func TestCLIModelUpdateViewportContentAssistantMessage(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.messages = []cliMessage{
		{role: "assistant", content: "Assistant message", timestamp: time.Now()},
	}

	model.updateViewportContent()

	content := model.viewport.View()
	if !strings.Contains(content, "Assistant") {
		t.Error("Assistant message should contain 'Assistant' label")
	}
}

// ---------------------------------------------------------------------------
// cliModel SendMessage Tests
// ---------------------------------------------------------------------------

func TestCLIModelSendMessage(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	msgBus := bus.NewMessageBus()
	model.msgBus = msgBus

	// Start goroutine to receive message
	received := make(chan bus.InboundMessage, 1)
	go func() {
		msg := <-msgBus.Inbound
		received <- msg
	}()

	model.sendMessage("Hello agent")

	select {
	case msg := <-received:
		if msg.Content != "Hello agent" {
			t.Errorf("Received content = %q, want 'Hello agent'", msg.Content)
		}
		if msg.Channel != "cli" {
			t.Errorf("Received channel = %q, want 'cli'", msg.Channel)
		}
		if !model.typing {
			t.Error("typing should be true after sending message")
		}
		if model.inputReady {
			t.Error("inputReady should be false while waiting for response")
		}
	case <-time.After(time.Second):
		t.Error("Message not received within timeout")
	}
}

func TestCLIModelSendMessageNoMsgBus(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	// msgBus is nil

	model.sendMessage("Hello agent")

	// Should not panic, message added to history
	if len(model.messages) != 1 {
		t.Errorf("Expected 1 message in history, got %d", len(model.messages))
	}
}

func TestCLIModelSendMessageEmpty(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.msgBus = bus.NewMessageBus()

	model.sendMessage("")

	// Message should still be added (empty is valid)
	if len(model.messages) != 1 {
		t.Errorf("Expected 1 message, got %d", len(model.messages))
	}
}

// ---------------------------------------------------------------------------
// Helper Function Tests
// ---------------------------------------------------------------------------

func TestTickCmd(t *testing.T) {
	cmd := tickCmd()
	if cmd == nil {
		t.Error("tickCmd() returned nil")
	}
}

// ---------------------------------------------------------------------------
// cliMessage Tests
// ---------------------------------------------------------------------------

func TestCLIMessageFields(t *testing.T) {
	now := time.Now()
	msg := cliMessage{
		role:      "user",
		content:   "Test content",
		timestamp: now,
		isPartial: false,
	}

	if msg.role != "user" {
		t.Errorf("role = %q, want 'user'", msg.role)
	}
	if msg.content != "Test content" {
		t.Errorf("content = %q, want 'Test content'", msg.content)
	}
	if !msg.timestamp.Equal(now) {
		t.Error("timestamp not set correctly")
	}
	if msg.isPartial {
		t.Error("isPartial should be false")
	}
}

// ---------------------------------------------------------------------------
// CLIProgressPayload Tests
// ---------------------------------------------------------------------------

func TestCLIProgressPayloadFields(t *testing.T) {
	payload := CLIProgressPayload{
		Phase:     "thinking",
		Iteration: 3,
		ActiveTools: []CLIToolProgress{
			{Name: "read", Label: "Reading", Status: "running", Elapsed: 100},
		},
		CompletedTools: []CLIToolProgress{
			{Name: "glob", Label: "Globbing", Status: "done", Elapsed: 50},
		},
		Thinking: "Analyzing...",
		SubAgents: []CLISubAgent{
			{Role: "reviewer", Status: "running", Desc: "Code review"},
		},
	}

	if payload.Phase != "thinking" {
		t.Errorf("Phase = %q, want 'thinking'", payload.Phase)
	}
	if len(payload.ActiveTools) != 1 {
		t.Errorf("ActiveTools count = %d, want 1", len(payload.ActiveTools))
	}
	if len(payload.CompletedTools) != 1 {
		t.Errorf("CompletedTools count = %d, want 1", len(payload.CompletedTools))
	}
}

func TestCLIToolProgressFields(t *testing.T) {
	tool := CLIToolProgress{
		Name:    "read",
		Label:   "Reading file",
		Status:  "running",
		Elapsed: 150,
	}

	if tool.Name != "read" {
		t.Errorf("Name = %q, want 'read'", tool.Name)
	}
	if tool.Elapsed != 150 {
		t.Errorf("Elapsed = %d, want 150", tool.Elapsed)
	}
}

func TestCLISubAgentFields(t *testing.T) {
	subAgent := CLISubAgent{
		Role:     "code-reviewer",
		Status:   "done",
		Desc:     "Completed review",
		Children: []CLISubAgent{},
	}

	if subAgent.Role != "code-reviewer" {
		t.Errorf("Role = %q, want 'code-reviewer'", subAgent.Role)
	}
	if subAgent.Status != "done" {
		t.Errorf("Status = %q, want 'done'", subAgent.Status)
	}
}

// ---------------------------------------------------------------------------
// Interface Compliance Test
// ---------------------------------------------------------------------------

func TestCLIChannelImplementsChannelInterface(t *testing.T) {
	msgBus := bus.NewMessageBus()
	ch := NewCLIChannel(CLIChannelConfig{}, msgBus)

	// This will fail to compile if CLIChannel doesn't implement Channel
	var _ Channel = ch
}

// ---------------------------------------------------------------------------
// CLIChannelConfig Tests
// ---------------------------------------------------------------------------

func TestCLIChannelConfigEmpty(t *testing.T) {
	cfg := CLIChannelConfig{}
	ch := NewCLIChannel(cfg, bus.NewMessageBus())

	if ch == nil {
		t.Error("NewCLIChannel with empty config should not return nil")
	}
}
