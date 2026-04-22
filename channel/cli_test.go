// xbot CLI Channel unit tests
// Tests for CLIChannel and cliModel functionality

package channel

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"xbot/bus"
	"xbot/llm"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// isTerminal returns true if stdout is connected to a terminal.
// bubbletea requires a real TTY; tests that call ch.Start() must skip in non-TTY.
func isTerminal() bool {
	fi, err := os.Stdout.Stat()
	return err == nil && (fi.Mode()&os.ModeCharDevice) != 0
}

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
	// Skip if not a real terminal — bubbletea.Start() blocks without TTY
	if !isTerminal() {
		t.Skip("Skipping - requires TTY")
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

	if model.viewport.Height() <= 0 {
		t.Error("viewport height should be positive")
	}
}

func TestCLIModelViewNotReady(t *testing.T) {
	model := newCLIModel()
	model.ready = false

	view := model.View()
	viewStr := view.Content
	// §14 splash 画面优先于 "初始化中..." 展示
	if !strings.Contains(viewStr, "xbot") && !strings.Contains(viewStr, "初始化") {
		t.Errorf("View() when not ready should show splash or initializing message, got: %q", viewStr)
	}
}

func TestCLIModelViewReady(t *testing.T) {
	model := newCLIModel()
	model.splashDone = true
	model.handleResize(80, 24)

	view := model.View()
	if view.Content == "" {
		t.Error("View() returned empty string")
	}
}

func TestCLIModelViewWithTyping(t *testing.T) {
	model := newCLIModel()
	model.splashDone = true
	model.handleResize(80, 24)
	model.typing = true

	view := model.View()
	if view.Content == "" {
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
	if view.Content == "" {
		t.Error("View() returned empty string")
	}
}

func TestCLIModelViewWithMessages(t *testing.T) {
	model := newCLIModel()
	model.splashDone = true
	model.handleResize(80, 24)
	model.messages = []cliMessage{
		{role: "user", content: "Hello", timestamp: time.Now()},
		{role: "assistant", content: "Hi there!", timestamp: time.Now()},
	}

	view := model.View()
	if view.Content == "" {
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

	// Simulate active progress state
	model.progress = &CLIProgressPayload{Phase: "thinking"}
	model.typing = true

	msg := bus.OutboundMessage{
		Content:   "",
		IsPartial: false,
	}

	model.handleAgentMessage(msg)

	// Empty content with no tools/waiting: should clear progress, not add message
	if len(model.messages) != 0 {
		t.Fatalf("Expected 0 messages, got %d", len(model.messages))
	}
	if model.progress != nil {
		t.Error("Expected progress to be cleared")
	}
	if model.typing {
		t.Error("Expected typing to be cleared")
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

func TestCLIModelUpdateCtrlCClearsInput(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.textarea.SetValue("some text")

	keyMsg := tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	_, cmd := model.Update(keyMsg)

	// When not typing, Ctrl+C clears input (no quit)
	if cmd != nil {
		t.Error("Update(CtrlC) when not typing should return nil cmd")
	}
	if model.textarea.Value() != "" {
		t.Errorf("textarea should be empty after CtrlC, got %q", model.textarea.Value())
	}
}

func TestCLIModelUpdateEscClearsInput(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.textarea.SetValue("some text")

	keyMsg := tea.KeyPressMsg{Code: tea.KeyEsc}
	_, cmd := model.Update(keyMsg)

	// When not typing, Esc clears input (no quit)
	if cmd != nil {
		t.Error("Update(Esc) when not typing should return nil cmd")
	}
	if model.textarea.Value() != "" {
		t.Errorf("textarea should be empty after Esc, got %q", model.textarea.Value())
	}
}

func TestCLIModelUpdateCtrlCWhileTyping(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.typing = true
	model.msgBus = bus.NewMessageBus()

	// Drain the inbound channel in background
	go func() { <-model.msgBus.Inbound }()

	keyMsg := tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	_, _ = model.Update(keyMsg)

	// Should add cancel system message
	hasCancel := false
	for _, msg := range model.messages {
		if msg.role == "system" && (strings.Contains(msg.content, "取消") || strings.Contains(msg.content, "Cancel")) {
			hasCancel = true
		}
	}
	if !hasCancel {
		t.Error("CtrlC while typing should add cancel message")
	}
}

func TestCLIModelUpdateProgressMsg(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.typing = true // simulate active agent turn

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

func TestCLIModelStaleProgressIgnored(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)

	// Scenario 1: After Ctrl+C (turn ended), stale progress is ignored
	model.typing = false
	model.progress = nil

	progMsg := cliProgressMsg{
		payload: &CLIProgressPayload{
			Phase:     "thinking",
			Iteration: 1,
			ChatID:    "cli:/test",
		},
	}
	model.chatID = "/test"
	model.channelName = "cli"
	_, _ = model.Update(progMsg)

	if model.progress != nil {
		t.Error("Progress after cancel should be ignored when typing=false")
	}

	// Scenario 2: Progress for a different session is ignored
	model2 := newCLIModel()
	model2.handleResize(80, 24)
	model2.typing = true
	model2.chatID = "/other"
	model2.channelName = "cli"

	_, _ = model2.Update(cliProgressMsg{
		payload: &CLIProgressPayload{
			Phase:     "thinking",
			Iteration: 1,
			ChatID:    "cli:/different",
		},
	})

	if model2.progress != nil {
		t.Error("Progress for a different session should be ignored")
	}

	// Scenario 3: restoreSession with typing=true allows progress through
	model3 := newCLIModel()
	model3.handleResize(80, 24)
	model3.chatID = "/test"
	model3.channelName = "cli"
	model3.savedSessions = map[string]*sessionState{
		"cli:/test": {
			typing:          true,
			progress:        &CLIProgressPayload{Phase: "thinking", Iteration: 0},
			streamingMsgIdx: -1,
		},
	}
	model3.restoreSession()

	if !model3.typing {
		t.Fatal("restoreSession should restore typing=true")
	}
	// Direct call — bypasses full Update() which needs viewport/messages setup
	model3.handleProgressMsg(cliProgressMsg{
		payload: &CLIProgressPayload{
			Phase:     "tool_exec",
			Iteration: 1,
			ChatID:    "cli:/test",
		},
	})

	if model3.progress == nil {
		t.Error("Progress should be accepted after restoreSession with typing=true")
	}
	if model3.progress.Iteration != 1 {
		t.Errorf("Progress iteration = %d, want 1", model3.progress.Iteration)
	}
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

	// Tick without typing/progress should NOT schedule another tick
	tickMsg := cliTickMsg{}
	_, cmd := model.Update(tickMsg)
	// cmd may be non-nil due to spinner/viewport/textarea sub-updates, but
	// the tick itself should not re-schedule. We just verify no panic.
	_ = cmd

	// Tick with typing active should schedule another tick
	model.typing = true
	_, cmd2 := model.Update(tickMsg)
	if cmd2 == nil {
		t.Error("Update(tickMsg) with typing=true should return a command")
	}
}

func TestTickChainSelfHealingViaIdleTick(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)

	// Simulate broken tick chain: typing=true but only idle tick arrives
	model.typing = true
	model.fastTickActive = false

	// idleTickMsg with typing=true should re-arm fast tick chain
	_, cmd := model.Update(idleTickMsg{})
	if cmd == nil {
		t.Fatal("idleTickMsg with typing=true should return tickCmd to self-heal")
	}
}

func TestTickChainSelfHealingViaProgressMsg(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)

	// Progress events should NOT emit tickCmd — that would create duplicate chains.
	// Self-healing is handled by idleTickMsg (3s safety net).
	model.typing = true
	model.fastTickActive = false

	_, cmd := model.Update(cliProgressMsg{payload: &CLIProgressPayload{
		Iteration: 1,
		Phase:     "thinking",
	}})
	// cmd may contain viewport/textarea sub-commands but should NOT contain tickCmd
	// (we can't easily inspect tea.Cmd contents, but at minimum verify no panic)
	_ = cmd
}

func TestStartAgentTurnDoesNotDuplicateChain(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)

	// When chain is already running (fastTickActive=true), startAgentTurn should NOT inject
	model.fastTickActive = true
	model.startAgentTurn()
	if len(model.pendingCmds) > 0 {
		t.Error("startAgentTurn should not inject tickCmd when chain was already running")
	}

	// startAgentTurn no longer manages tickCmd directly.
	// The wasTyping guard at the end of Update() handles idle→typing transitions.
	// Simulate this via cliProcessingMsg (realistic remote-mode scenario):
	// server sends SetProcessing(true) → cliProcessingMsg → startAgentTurn.
	model.fastTickActive = false
	model.pendingCmds = nil
	model.typing = false
	_, cmd := model.Update(cliProcessingMsg{processing: true})
	if cmd == nil {
		t.Fatal("Update should return cmd (tickCmd) after idle→typing transition via wasTyping guard")
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
	keyMsg := tea.KeyPressMsg{Code: tea.KeyEnter}
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
	keyMsg := tea.KeyPressMsg{Code: tea.KeyEnter}
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
	keyMsg := tea.KeyPressMsg{Code: tea.KeyEnter}
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
	model.locale = GetLocale("en")

	tests := []struct {
		phase    string
		expected string
	}{
		{"thinking", "Thinking"},
		{"tool_exec", "#0"},
		{"compressing", "compressing"},
		{"retrying", "retrying"},
		{"done", "#0"},
		{"unknown", "#0"},
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
	model.locale = GetLocale("en")
	model.progress = nil

	progressStyle := lipgloss.NewStyle()
	toolStyle := lipgloss.NewStyle()

	result := model.renderProgressStatus(progressStyle, toolStyle)
	if !strings.Contains(result, "Thinking") {
		t.Errorf("renderProgressStatus with nil progress should show a thinking verb, got: %q", result)
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

	if !strings.Contains(result, "#5") {
		t.Errorf("renderProgressStatus should show iteration, got: %q", result)
	}
}

func TestCLIModelRenderProgressStatusWithActiveTools(t *testing.T) {
	model := newCLIModel()
	model.progress = &CLIProgressPayload{
		Phase:       "tool_exec",
		Iteration:   1,
		ActiveTools: []CLIToolProgress{{Name: "read", Label: "Reading file", Elapsed: 100}},
	}

	progressStyle := lipgloss.NewStyle()
	toolStyle := lipgloss.NewStyle()

	result := model.renderProgressStatus(progressStyle, toolStyle)

	// Active tool name is NOT shown in status bar (rendered in progress block instead)
	// Verify it shows iteration and doesn't crash
	if !strings.Contains(result, "#1") {
		t.Errorf("renderProgressStatus should show iteration with active tools, got: %q", result)
	}
}

func TestCLIModelRenderProgressStatusToolWithoutLabel(t *testing.T) {
	model := newCLIModel()
	model.progress = &CLIProgressPayload{
		Phase:       "tool_exec",
		Iteration:   1,
		ActiveTools: []CLIToolProgress{{Name: "read", Label: "", Elapsed: 0}},
	}

	progressStyle := lipgloss.NewStyle()
	toolStyle := lipgloss.NewStyle()

	result := model.renderProgressStatus(progressStyle, toolStyle)

	// Active tool name is NOT shown in status bar (rendered in progress block instead)
	if !strings.Contains(result, "#1") {
		t.Errorf("renderProgressStatus should show iteration without crash, got: %q", result)
	}
}

func TestCLIModelRenderProgressStatusWithElapsed(t *testing.T) {
	model := newCLIModel()
	model.progress = &CLIProgressPayload{Phase: "thinking"}
	model.typingStartTime = time.Now().Add(-5 * time.Second)

	progressStyle := lipgloss.NewStyle()
	toolStyle := lipgloss.NewStyle()

	result := model.renderProgressStatus(progressStyle, toolStyle)
	if !strings.Contains(result, "s") {
		t.Errorf("renderProgressStatus should show elapsed time, got: %q", result)
	}
}

// ---------------------------------------------------------------------------
// Progress Block (viewport) Rendering Tests
// ---------------------------------------------------------------------------

func TestCLIModelRenderProgressBlockEmpty(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.typing = false
	model.progress = nil

	result := model.renderProgressBlock()
	if result != "" {
		t.Errorf("renderProgressBlock should be empty when not typing, got: %q", result)
	}
}

func TestCLIModelRenderProgressBlockThinking(t *testing.T) {
	model := newCLIModel()
	model.locale = GetLocale("en")
	model.handleResize(80, 24)
	model.typing = true
	model.typingStartTime = time.Now()

	result := model.renderProgressBlock()
	if !strings.Contains(result, "Thinking") {
		t.Errorf("renderProgressBlock should show a thinking verb, got: %q", result)
	}
	if !strings.Contains(result, "Progress") {
		t.Errorf("renderProgressBlock should show Progress header, got: %q", result)
	}
}

func TestCLIModelRenderProgressBlockWithTools(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.typing = true
	model.typingStartTime = time.Now()
	model.progress = &CLIProgressPayload{
		Phase:     "tool_exec",
		Iteration: 1,
		ActiveTools: []CLIToolProgress{
			{Name: "read_file", Label: "Reading config.go", Status: "running", Elapsed: 1200},
		},
		CompletedTools: []CLIToolProgress{
			{Name: "grep", Label: "Searching imports", Status: "done", Elapsed: 300, Iteration: 1},
		},
	}

	result := model.renderProgressBlock()
	if !strings.Contains(result, "Searching imports") {
		t.Errorf("renderProgressBlock should show completed tool, got: %q", result)
	}
	if !strings.Contains(result, "Reading config.go") {
		t.Errorf("renderProgressBlock should show active tool, got: %q", result)
	}
	if !strings.Contains(result, "#1") {
		t.Errorf("renderProgressBlock should show iteration number, got: %q", result)
	}
}

func TestCLIModelRenderProgressBlockWithIterationHistory(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.typing = true
	model.typingStartTime = time.Now()
	model.iterationHistory = []cliIterationSnapshot{
		{
			Iteration: 0,
			Thinking:  "Analyzing requirements",
			Tools: []CLIToolProgress{
				{Name: "read", Label: "Reading file", Status: "done", Elapsed: 500},
			},
		},
	}
	model.progress = &CLIProgressPayload{
		Phase:     "thinking",
		Iteration: 1,
	}

	result := model.renderProgressBlock()
	if !strings.Contains(result, "#0") {
		t.Errorf("renderProgressBlock should show completed iteration #0, got: %q", result)
	}
	if !strings.Contains(result, "#1") {
		t.Errorf("renderProgressBlock should show current iteration #1, got: %q", result)
	}
	if !strings.Contains(result, "Reading file") {
		t.Errorf("renderProgressBlock should show historical tool, got: %q", result)
	}
}

func TestCLIModelRenderProgressBlockSubAgents(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.typing = true
	model.typingStartTime = time.Now()
	model.progress = &CLIProgressPayload{
		Phase:     "tool_exec",
		Iteration: 0,
		SubAgents: []CLISubAgent{
			{Role: "code-reviewer", Status: "running", Desc: "Reviewing code"},
			{Role: "test-runner", Status: "done", Desc: "Tests passed"},
			{Role: "explore", Status: "error", Desc: "429 rate limited"},
		},
	}

	result := model.renderProgressBlock()
	if !strings.Contains(result, "code-reviewer") {
		t.Errorf("renderProgressBlock should show subagent role, got: %q", result)
	}
	if !strings.Contains(result, "Reviewing code") {
		t.Errorf("renderProgressBlock should show subagent desc, got: %q", result)
	}
	// Done sub-agents should be hidden from progress panel
	if strings.Contains(result, "test-runner") {
		t.Errorf("renderProgressBlock should not show completed subagent, got: %q", result)
	}
	// Error sub-agents should also be hidden from progress panel
	if strings.Contains(result, "explore") {
		t.Errorf("renderProgressBlock should not show errored subagent, got: %q", result)
	}
}

func TestCLIModelRenderProgressBlockSubAgentChildren(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.typing = true
	model.typingStartTime = time.Now()
	model.progress = &CLIProgressPayload{
		Phase:     "tool_exec",
		Iteration: 0,
		SubAgents: []CLISubAgent{
			{
				Role:   "reviewer",
				Status: "running",
				Children: []CLISubAgent{
					{Role: "child", Status: "done"},
				},
			},
		},
	}

	result := model.renderProgressBlock()
	// Done child sub-agents should be hidden from progress panel
	if strings.Contains(result, "child") {
		t.Errorf("renderProgressBlock should not show completed child subagent, got: %q", result)
	}
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
// formatElapsed Tests
// ---------------------------------------------------------------------------

func TestFormatElapsed(t *testing.T) {
	tests := []struct {
		ms       int64
		expected string
	}{
		{0, "0ms"},
		{50, "50ms"},
		{999, "999ms"},
		{1000, "1.0s"},
		{1500, "1.5s"},
		{12300, "12.3s"},
		{59999, "60.0s"},
		{60000, "1m0s"},
		{90000, "1m30s"},
		{125000, "2m5s"},
	}
	for _, tt := range tests {
		got := formatElapsed(tt.ms)
		if got != tt.expected {
			t.Errorf("formatElapsed(%d) = %q, want %q", tt.ms, got, tt.expected)
		}
	}
}

// ---------------------------------------------------------------------------
// Iteration History Accumulation Tests
// ---------------------------------------------------------------------------

func TestCLIModelIterationAccumulation(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.typing = true
	model.typingStartTime = time.Now()

	// Iteration 0: thinking
	prog0 := cliProgressMsg{payload: &CLIProgressPayload{
		Phase:     "thinking",
		Iteration: 0,
	}}
	model.Update(prog0)
	if len(model.iterationHistory) != 0 {
		t.Errorf("Expected 0 history entries, got %d", len(model.iterationHistory))
	}

	// Iteration 0: tool_exec with completed tools
	prog0b := cliProgressMsg{payload: &CLIProgressPayload{
		Phase:     "tool_exec",
		Iteration: 0,
		CompletedTools: []CLIToolProgress{
			{Name: "read", Label: "Reading", Status: "done", Elapsed: 100},
		},
	}}
	model.Update(prog0b)

	// Iteration 1: thinking — should snapshot iteration 0
	prog1 := cliProgressMsg{payload: &CLIProgressPayload{
		Phase:     "thinking",
		Iteration: 1,
	}}
	model.Update(prog1)
	if len(model.iterationHistory) != 1 {
		t.Fatalf("Expected 1 history entry after iteration change, got %d", len(model.iterationHistory))
	}
	if model.iterationHistory[0].Iteration != 0 {
		t.Errorf("History[0].Iteration = %d, want 0", model.iterationHistory[0].Iteration)
	}
	if len(model.iterationHistory[0].Tools) != 1 {
		t.Errorf("History[0].Tools count = %d, want 1", len(model.iterationHistory[0].Tools))
	}
}

func TestCLIModelCollectAllTools(t *testing.T) {
	model := newCLIModel()
	model.iterationHistory = []cliIterationSnapshot{
		{Iteration: 0, Tools: []CLIToolProgress{{Name: "a"}, {Name: "b"}}},
		{Iteration: 1, Tools: []CLIToolProgress{{Name: "c"}}},
	}
	all := model.collectAllTools()
	if len(all) != 3 {
		t.Errorf("collectAllTools() = %d tools, want 3", len(all))
	}
}

func TestCLIModelResetProgressState(t *testing.T) {
	model := newCLIModel()
	model.iterationHistory = []cliIterationSnapshot{{Iteration: 0}}
	model.lastSeenIteration = 5
	model.typingStartTime = time.Now().Add(-10 * time.Second)

	model.resetProgressState()

	if model.iterationHistory != nil {
		t.Error("iterationHistory should be nil after reset")
	}
	if model.lastSeenIteration != 0 {
		t.Errorf("lastSeenIteration = %d, want 0", model.lastSeenIteration)
	}
	if model.typingStartTime.IsZero() {
		t.Error("typingStartTime should be set after reset")
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

// ---------------------------------------------------------------------------
// ConvertMessagesToHistory tests
// ---------------------------------------------------------------------------

func makeDetail(iterations []iterSnapshot) string {
	b, _ := json.Marshal(iterations)
	return string(b)
}

func TestConvert_NormalCompletedTurn(t *testing.T) {
	// A normal completed turn: user → assistant(tool_calls) → tool → assistant(Detail + content)
	detail := makeDetail([]iterSnapshot{
		{Iteration: 1, Thinking: "think1", Tools: []iterToolSnap{{Name: "Shell", Label: "Shell", Status: "done", ElapsedMS: 500}}},
		{Iteration: 2, Thinking: "think2", Tools: []iterToolSnap{{Name: "Read", Label: "Read file", Status: "done", ElapsedMS: 200}}},
	})
	msgs := []llm.ChatMessage{
		{Role: "user", Content: "hello"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c1", Name: "Shell", Arguments: "{}"}}},
		{Role: "tool", ToolCallID: "c1", ToolName: "Shell", ToolArguments: "{}"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c2", Name: "Read", Arguments: "{}"}}},
		{Role: "tool", ToolCallID: "c2", ToolName: "Read", ToolArguments: "{}"},
		{Role: "assistant", Content: "done!", Detail: detail},
	}
	history := ConvertMessagesToHistory(msgs)

	// Should be: user, tool_summary(from Detail), assistant
	if len(history) != 3 {
		t.Fatalf("expected 3 messages, got %d: %+v", len(history), history)
	}
	assertRole(t, history[0], "user")
	assertRole(t, history[1], "tool_summary")
	assertRole(t, history[2], "assistant")

	if history[1].Iterations == nil || len(history[1].Iterations) != 2 {
		t.Fatalf("expected 2 iterations in tool_summary, got %d", len(history[1].Iterations))
	}
	// Should come from Detail (has elapsed data), not from pending (elapsed=0)
	if history[1].Iterations[0].Tools[0].Elapsed != 500 {
		t.Errorf("expected elapsed=500 from Detail, got %d", history[1].Iterations[0].Tools[0].Elapsed)
	}
}

func TestConvert_CancelledTurn(t *testing.T) {
	// Cancelled turn: user → assistant(tool_calls) → tool → assistant(tool_calls) → tool (no final assistant)
	msgs := []llm.ChatMessage{
		{Role: "user", Content: "hello"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c1", Name: "Shell", Arguments: "{}"}}},
		{Role: "tool", ToolCallID: "c1", ToolName: "Shell", ToolArguments: "{}"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c2", Name: "Read", Arguments: "{}"}}},
		{Role: "tool", ToolCallID: "c2", ToolName: "Read", ToolArguments: "{}"},
	}
	history := ConvertMessagesToHistory(msgs)

	// Should be: user, tool_summary (accumulated from both iterations)
	if len(history) != 2 {
		t.Fatalf("expected 2 messages, got %d: %+v", len(history), history)
	}
	assertRole(t, history[0], "user")
	assertRole(t, history[1], "tool_summary")

	if len(history[1].Iterations) != 2 {
		t.Fatalf("expected 2 iterations, got %d", len(history[1].Iterations))
	}
	if history[1].Iterations[0].Tools[0].Name != "Shell" {
		t.Errorf("expected Shell, got %s", history[1].Iterations[0].Tools[0].Name)
	}
	if history[1].Iterations[1].Tools[0].Name != "Read" {
		t.Errorf("expected Read, got %s", history[1].Iterations[1].Tools[0].Name)
	}
}

func TestConvert_MultipleTurns(t *testing.T) {
	// Turn 1: completed normally. Turn 2: cancelled.
	detail := makeDetail([]iterSnapshot{
		{Iteration: 1, Tools: []iterToolSnap{{Name: "Shell", Status: "done", ElapsedMS: 100}}},
	})
	msgs := []llm.ChatMessage{
		{Role: "user", Content: "turn1"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c1", Name: "Shell", Arguments: "{}"}}},
		{Role: "tool", ToolCallID: "c1", ToolName: "Shell", ToolArguments: "{}"},
		{Role: "assistant", Content: "done1", Detail: detail},
		{Role: "user", Content: "turn2"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c2", Name: "Grep", Arguments: "{}"}}},
		{Role: "tool", ToolCallID: "c2", ToolName: "Grep", ToolArguments: "{}"},
	}
	history := ConvertMessagesToHistory(msgs)

	// Expected: user, tool_summary(Detail, 1 iter), assistant, user, tool_summary(pending, 1 iter)
	if len(history) != 5 {
		t.Fatalf("expected 5 messages, got %d: %+v", len(history), history)
	}
	assertRole(t, history[0], "user")         // turn1 user
	assertRole(t, history[1], "tool_summary") // turn1 completed
	assertRole(t, history[2], "assistant")    // turn1 reply
	assertRole(t, history[3], "user")         // turn2 user
	assertRole(t, history[4], "tool_summary") // turn2 cancelled

	// Turn 1 tool_summary should have elapsed=100 from Detail
	if history[1].Iterations[0].Tools[0].Elapsed != 100 {
		t.Errorf("turn1 expected elapsed=100, got %d", history[1].Iterations[0].Tools[0].Elapsed)
	}
	// Turn 2 tool_summary should have elapsed=0 from pending
	if history[4].Iterations[0].Tools[0].Elapsed != 0 {
		t.Errorf("turn2 expected elapsed=0, got %d", history[4].Iterations[0].Tools[0].Elapsed)
	}
}

func TestConvert_NoToolCalls(t *testing.T) {
	// Simple conversation without tool calls
	msgs := []llm.ChatMessage{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi!"},
	}
	history := ConvertMessagesToHistory(msgs)
	if len(history) != 2 {
		t.Fatalf("expected 2, got %d", len(history))
	}
	assertRole(t, history[0], "user")
	assertRole(t, history[1], "assistant")
}

func assertRole(t *testing.T, msg HistoryMessage, want string) {
	t.Helper()
	if msg.Role != want {
		t.Errorf("expected role=%q, got %q", want, msg.Role)
	}
}
