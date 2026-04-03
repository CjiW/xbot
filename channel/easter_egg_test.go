package channel

import (
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// 彩蛋 #1: Konami Code 测试
// ---------------------------------------------------------------------------

func TestCheckKonami_FullSequence(t *testing.T) {
	m := &cliModel{}
	// 按下完整的 Konami Code 序列
	keys := []string{"up", "up", "down", "down", "left", "right", "left", "right", "b", "a"}
	for i, key := range keys {
		triggered := m.checkKonami(key)
		if i < len(keys)-1 && triggered {
			t.Errorf("Konami triggered prematurely at key %d (%s)", i, key)
		}
		if i == len(keys)-1 && !triggered {
			t.Error("Konami Code should trigger on the last key (A)")
		}
	}
}

func TestCheckKonami_PartialSequence(t *testing.T) {
	m := &cliModel{}
	// 不完整的序列不应触发
	keys := []string{"up", "up", "down"}
	for _, key := range keys {
		if m.checkKonami(key) {
			t.Error("Partial Konami sequence should not trigger")
		}
	}
}

func TestCheckKonami_ResetAfterTrigger(t *testing.T) {
	m := &cliModel{}
	// 第一次完整序列触发
	triggered := false
	keys := []string{"up", "up", "down", "down", "left", "right", "left", "right", "b", "a"}
	for _, key := range keys {
		if m.checkKonami(key) {
			triggered = true
		}
	}
	if !triggered {
		t.Fatal("First Konami Code should trigger")
	}

	// 第二次按下 A 不应该触发（缓冲区已重置）
	if m.checkKonami("a") {
		t.Error("Buffer should be reset after trigger")
	}
}

func TestCheckKonami_BufferOverflow(t *testing.T) {
	m := &cliModel{}
	// 按下超过序列长度的无关按键
	for i := 0; i < 20; i++ {
		m.checkKonami("up")
	}
	// 然后按下正确序列的最后几个键
	if m.checkKonami("down") {
		t.Error("Buffer overflow should not cause false trigger")
	}
}

// ---------------------------------------------------------------------------
// 彩蛋 #3: The Answer is 42 测试
// ---------------------------------------------------------------------------

func TestIsAnswer42(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"the answer to life, the universe and everything", true},
		{"The Answer to Life, the Universe and Everything", true},
		{"the answer to the ultimate question", true},
		{"ultimate question of life", true},
		{"生命、宇宙及一切的答案", true},
		{"关于生命宇宙以及一切的", true},
		{"what is 42", false},
		{"hello world", false},
		{"the answer is 42", false}, // 这个不匹配
		{"", false},
	}
	for _, tt := range tests {
		got := isAnswer42(tt.input)
		if got != tt.want {
			t.Errorf("isAnswer42(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// 彩蛋 #4: 节日 Splash 测试
// ---------------------------------------------------------------------------

func TestHolidaySplash(t *testing.T) {
	// 测试返回值类型（不测试具体日期，因为日期在变）
	result := holidaySplash()
	// 可能为空（非特殊日期），也可能是字符串
	if result != "" && len(result) < 5 {
		t.Errorf("holidaySplash returned suspiciously short string: %q", result)
	}
}

func TestIsLeapYear(t *testing.T) {
	tests := []struct {
		year int
		want bool
	}{
		{2000, true},
		{2024, true},
		{2025, false},
		{1900, false},
		{2023, false},
	}
	for _, tt := range tests {
		got := isLeapYear(tt.year)
		if got != tt.want {
			t.Errorf("isLeapYear(%d) = %v, want %v", tt.year, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// 彩蛋 #5: /sudo 测试
// ---------------------------------------------------------------------------

func TestRandomSudoMessage(t *testing.T) {
	msg := randomSudoMessage()
	if msg == "" {
		t.Error("randomSudoMessage should return a non-empty string")
	}
	if !strings.Contains(msg, "🚫") {
		t.Errorf("randomSudoMessage should contain 🚫 icon, got: %s", msg)
	}
}

// ---------------------------------------------------------------------------
// 彩蛋 #6: /fortune 测试
// ---------------------------------------------------------------------------

func TestRandomFortune(t *testing.T) {
	text, lucky := randomFortune()
	if text == "" {
		t.Error("randomFortune should return non-empty text")
	}
	if lucky < 0 || lucky > 100 {
		t.Errorf("lucky number %d out of expected range [0, 100]", lucky)
	}
}

// ---------------------------------------------------------------------------
// 彩蛋 #7: 三连 /version 测试
// ---------------------------------------------------------------------------

func TestCheckVersionOCD_ThreeHits(t *testing.T) {
	m := &cliModel{}
	now := time.Now()
	m.versionHitTimes = []time.Time{
		now.Add(-5 * time.Second),
		now.Add(-3 * time.Second),
		now,
	}

	// Should NOT trigger because we haven't processed /version yet
	// checkVersionOCD should set easterEgg
	m.checkVersionOCD()

	if m.easterEgg != easterEggVersion {
		t.Errorf("checkVersionOCD should set easterEgg to version, got %q", m.easterEgg)
	}
	if m.versionHitTimes != nil {
		t.Error("versionHitTimes should be reset after trigger")
	}
}

func TestCheckVersionOCD_SlowHits(t *testing.T) {
	m := &cliModel{}
	now := time.Now()
	m.versionHitTimes = []time.Time{
		now.Add(-20 * time.Second),
		now.Add(-15 * time.Second),
		now,
	}

	m.checkVersionOCD()

	if m.easterEgg != easterEggNone {
		t.Errorf("checkVersionOCD should not trigger with slow hits, got %q", m.easterEgg)
	}
}

// ---------------------------------------------------------------------------
// 彩蛋 #8: /zen 测试
// ---------------------------------------------------------------------------

func TestRandomZen(t *testing.T) {
	haiku, message := randomZen()
	if haiku == "" {
		t.Error("randomZen should return non-empty haiku")
	}
	if message == "" {
		t.Error("randomZen should return non-empty message")
	}
	// 俳句应该有多行
	if !strings.Contains(haiku, "\n") {
		t.Errorf("haiku should be multi-line, got: %s", haiku)
	}
}

// ---------------------------------------------------------------------------
// 彩蛋激活/消失测试
// ---------------------------------------------------------------------------

func TestActivateEasterEgg(t *testing.T) {
	m := &cliModel{}
	if m.easterEgg != easterEggNone {
		t.Errorf("easterEgg should start as none, got %q", m.easterEgg)
	}

	cmd := m.activateEasterEgg(easterEggKonami, 0)
	if m.easterEgg != easterEggKonami {
		t.Error("activateEasterEgg should set the mode")
	}
	if cmd != nil {
		t.Error("activateEasterEgg with 0 duration should return nil cmd")
	}

	// 带 duration 的激活
	cmd = m.activateEasterEgg(easterEggMatrix, 5*time.Second)
	if cmd == nil {
		t.Error("activateEasterEgg with duration should return a tea.Cmd")
	}
}

func TestRenderEasterEggOverlay_Empty(t *testing.T) {
	m := &cliModel{width: 80, height: 24}
	result := m.renderEasterEggOverlay()
	if result != "" {
		t.Errorf("renderEasterEggOverlay should return empty string when no easter egg, got: %q", result)
	}
}

func TestRenderEasterEggOverlay_Konami(t *testing.T) {
	m := &cliModel{width: 80, height: 24}
	m.easterEgg = easterEggKonami
	result := m.renderEasterEggOverlay()
	if result == "" {
		t.Error("renderEasterEggOverlay should return content for Konami mode")
	}
	if !strings.Contains(result, "KONAMI") {
		t.Error("Konami overlay should contain 'KONAMI'")
	}
}

func TestRenderEasterEggOverlay_Answer42(t *testing.T) {
	m := &cliModel{width: 80, height: 24}
	m.easterEgg = easterEggAnswer42
	result := m.renderEasterEggOverlay()
	if result == "" {
		t.Error("renderEasterEggOverlay should return content for Answer42 mode")
	}
	if !strings.Contains(result, "42") {
		t.Error("Answer42 overlay should contain '42'")
	}
}

func TestRenderEasterEggOverlay_Version(t *testing.T) {
	m := &cliModel{width: 80, height: 24}
	m.easterEgg = easterEggVersion
	m.easterEggCustom = "test version content"
	result := m.renderEasterEggOverlay()
	if result == "" {
		t.Error("renderEasterEggOverlay should return content for Version mode")
	}
}

// ---------------------------------------------------------------------------
// 彩蛋命令路由测试
// ---------------------------------------------------------------------------

func TestHandleEasterEggCommand_Matrix(t *testing.T) {
	m := &cliModel{}
	handled := m.handleEasterEggCommand("/matrix")
	if !handled {
		t.Error("/matrix should be handled by easter egg system")
	}
	if m.easterEgg != easterEggMatrix {
		t.Error("/matrix should activate matrix easter egg")
	}
}

func TestHandleEasterEggCommand_Sudo(t *testing.T) {
	m := &cliModel{
		messages: make([]cliMessage, 0, 10),
	}
	handled := m.handleEasterEggCommand("/sudo")
	if !handled {
		t.Error("/sudo should be handled by easter egg system")
	}
	// sudo 添加 system 消息
	if len(m.messages) != 1 {
		t.Errorf("/sudo should add 1 system message, got %d", len(m.messages))
	}
}

func TestHandleEasterEggCommand_Fortune(t *testing.T) {
	m := &cliModel{
		messages: make([]cliMessage, 0, 10),
	}
	handled := m.handleEasterEggCommand("/fortune")
	if !handled {
		t.Error("/fortune should be handled by easter egg system")
	}
	if len(m.messages) != 1 {
		t.Errorf("/fortune should add 1 system message, got %d", len(m.messages))
	}
}

func TestHandleEasterEggCommand_Zen(t *testing.T) {
	m := &cliModel{
		messages: make([]cliMessage, 0, 10),
	}
	handled := m.handleEasterEggCommand("/zen")
	if !handled {
		t.Error("/zen should be handled by easter egg system")
	}
	if len(m.messages) != 1 {
		t.Errorf("/zen should add 1 system message, got %d", len(m.messages))
	}
}

func TestHandleEasterEggCommand_Version(t *testing.T) {
	m := &cliModel{}
	// /version 不被彩蛋系统独占，返回 false（让正常路由处理）
	handled := m.handleEasterEggCommand("/version")
	if handled {
		t.Error("/version should not be exclusively handled by easter egg system (returns false)")
	}
}

func TestHandleEasterEggCommand_Unknown(t *testing.T) {
	m := &cliModel{}
	handled := m.handleEasterEggCommand("/unknown")
	if handled {
		t.Error("Unknown commands should not be handled by easter egg system")
	}
}

// ---------------------------------------------------------------------------
// centerOverlay 测试
// ---------------------------------------------------------------------------

func TestCenterOverlay(t *testing.T) {
	input := "hello"
	result := centerOverlay(input, 80, 24)
	if !strings.Contains(result, "hello") {
		t.Error("centerOverlay should contain the input string")
	}
	// 应该有前导空格
	if result == "hello" {
		t.Error("centerOverlay should pad the input")
	}
}

func TestCenterOverlay_Narrow(t *testing.T) {
	input := strings.Repeat("x", 100)
	result := centerOverlay(input, 50, 10)
	if !strings.Contains(result, "x") {
		t.Error("centerOverlay should still contain input even when wider than terminal")
	}
}
