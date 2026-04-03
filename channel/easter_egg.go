package channel

import (
	"fmt"
	"math/rand"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"xbot/version"
)

// ---------------------------------------------------------------------------
// 彩蛋状态常量
// ---------------------------------------------------------------------------

// easterEggMode 表示当前激活的彩蛋类型（"" = 无彩蛋）
type easterEggMode string

const (
	easterEggNone     easterEggMode = ""
	easterEggKonami   easterEggMode = "konami"
	easterEggMatrix   easterEggMode = "matrix"
	easterEggAnswer42 easterEggMode = "answer42"
	easterEggVersion  easterEggMode = "version"
)

// ---------------------------------------------------------------------------
// 彩蛋内部消息类型
// ---------------------------------------------------------------------------

// easterEggDoneMsg 彩蛋自动消失消息
type easterEggDoneMsg struct{}

// easterEggMatrixTickMsg Matrix 代码雨动画 tick
type easterEggMatrixTickMsg struct {
	rain []string // 当前帧的代码雨行
}

// ---------------------------------------------------------------------------
// Konami Code (↑↑↓↓←→←→BA)
// ---------------------------------------------------------------------------

// konamiSequence 完整的科乐美指令序列
var konamiSequence = []string{"up", "up", "down", "down", "left", "right", "left", "right", "b", "a"}

// konamiASCII — Konami Code 触发后的 ASCII art 庆祝画面
var konamiASCII = `
   ╔═══════════════════════════════════════╗
   ║                                       ║
   ║    ★  KONAMI CODE ACTIVATED!  ★      ║
   ║                                       ║
   ║      ↑ ↑ ↓ ↓ ← → ← → B A            ║
   ║                                       ║
   ║   ┌─────────────────────────────┐      ║
   ║   │  +30 Lives                  │      ║
   ║   │  (Well, not really, but     │      ║
   ║   │   you found the secret!)    │      ║
   ║   └─────────────────────────────┘      ║
   ║                                       ║
   ║         🎮 ✨ 🏆 ✨ 🎮               ║
   ║                                       ║
   ╚═══════════════════════════════════════╝
`

// checkKonami 检查按键是否匹配 Konami Code 序列。
// 返回 true 表示完整序列已匹配，应触发彩蛋。
func (m *cliModel) checkKonami(keyName string) bool {
	if m.konamiBuffer == nil {
		m.konamiBuffer = make([]string, 0, len(konamiSequence))
	}
	m.konamiBuffer = append(m.konamiBuffer, keyName)

	// 保持缓冲区不超过序列长度
	if len(m.konamiBuffer) > len(konamiSequence) {
		m.konamiBuffer = m.konamiBuffer[len(m.konamiBuffer)-len(konamiSequence):]
	}

	// 检查尾部是否匹配完整序列
	if len(m.konamiBuffer) >= len(konamiSequence) {
		offset := len(m.konamiBuffer) - len(konamiSequence)
		match := true
		for i := 0; i < len(konamiSequence); i++ {
			if m.konamiBuffer[offset+i] != konamiSequence[i] {
				match = false
				break
			}
		}
		if match {
			m.konamiBuffer = nil // 重置缓冲区
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// 彩蛋 #2: /matrix — 黑客帝国代码雨
// ---------------------------------------------------------------------------

// matrixChars 半角片假名 + 数字符号（黑客帝国风格）
var matrixChars = []rune{
	'ｱ', 'ｲ', 'ｳ', 'ｴ', 'ｵ', 'ｶ', 'ｷ', 'ｸ', 'ｹ', 'ｺ',
	'ｻ', 'ｼ', 'ｽ', 'ｾ', 'ｿ', 'ﾀ', 'ﾁ', 'ﾂ', 'ﾃ', 'ﾄ',
	'ﾅ', 'ﾆ', 'ﾇ', 'ﾈ', 'ﾉ', 'ﾊ', 'ﾋ', 'ﾌ', 'ﾍ', 'ﾎ',
	'ﾏ', 'ﾐ', 'ﾑ', 'ﾒ', 'ﾓ', 'ﾔ', 'ﾕ', 'ﾖ', 'ﾗ', 'ﾘ',
	'ﾙ', 'ﾚ', 'ﾛ', 'ﾜ', 'ﾝ', '0', '1', '2', '3', '4',
	'5', '6', '7', '8', '9', ':', '.', '*', '+', '-', '=',
}

// matrixRain 初始化代码雨列状态（每列一个下落位置）
func (m *cliModel) matrixRain() []string {
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	cols := m.width
	if cols < 10 {
		cols = 10
	}
	rows := m.height
	if rows < 5 {
		rows = 5
	}

	// 每列独立的下落位置（0 = 顶部）
	drops := make([]int, cols)
	for i := range drops {
		drops[i] = rng.Intn(rows)
	}

	lines := make([]string, rows)
	for r := 0; r < rows; r++ {
		var buf strings.Builder
		for c := 0; c < cols; c++ {
			if drops[c] > 0 {
				ch := matrixChars[rng.Intn(len(matrixChars))]
				buf.WriteRune(ch)
				drops[c]--
			} else {
				buf.WriteRune(' ')
			}
		}
		lines[r] = buf.String()
	}
	return lines
}

// matrixTickCmd 生成 Matrix 代码雨动画的 tick 命令
func matrixTickCmd(m *cliModel) tea.Cmd {
	return func() tea.Msg {
		return easterEggMatrixTickMsg{rain: m.matrixRain()}
	}
}

// ---------------------------------------------------------------------------
// 彩蛋 #3: The Answer is 42
// ---------------------------------------------------------------------------

// answer42Art Deep Thought 回答 "42" 的 ASCII art
var answer42Art = `
 ┌──────────────────────────────────────────────────────────┐
 │                                                          │
 │              ╔═══════════════════════╗                   │
 │              ║                       ║                   │
 │              ║    D E E P   T H O U G H T    ║                   │
 │              ║                       ║                   │
 │              ║                       ║                   │
 │              ║      The Answer to    ║                   │
 │              ║   the Ultimate Question║                   │
 │              ║      of Life, the     ║                   │
 │              ║   Universe, and       ║                   │
 │              ║     Everything...     ║                   │
 │              ║                       ║                   │
 │              ║                       ║                   │
 │              ║          42           ║                   │
 │              ║                       ║                   │
 │              ╚═══════════════════════╝                   │
 │                                                          │
 │    "Though I don't think," added Deep Thought,           │
 │    "that you're going to like it."                       │
 │                                                          │
 └──────────────────────────────────────────────────────────┘
`

// isAnswer42 检测用户输入是否触发 "The Answer is 42" 彩蛋
func isAnswer42(content string) bool {
	lower := strings.ToLower(content)
	// 匹配 "the answer to life..." 的各种变体
	patterns := []string{
		"the answer to life",
		"the answer to the ultimate question",
		"ultimate question of life",
		"生命、宇宙及一切的答案",
		"关于生命宇宙以及一切的",
	}
	for _, p := range patterns {
		if strings.Contains(lower, strings.ToLower(p)) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// 彩蛋 #4: 节日 Splash 描述
// ---------------------------------------------------------------------------

// holidaySplash 节日特供 splash 描述文字
// 返回空字符串表示无特殊节日
func holidaySplash() string {
	now := time.Now()
	month, day := int(now.Month()), now.Day()

	// 元旦
	if month == 1 && day == 1 {
		return "🎆 新年快乐！Happy New Year!"
	}
	// 情人节
	if month == 2 && day == 14 {
		return "💕 Valentine's Day — May your code compile on the first try"
	}
	// π Day
	if month == 3 && day == 14 {
		return "π 3.14159265358979... Happy π Day!"
	}
	// 愚人节
	if month == 4 && day == 1 {
		return "🤡 今天所有 bug 都是 feature — Happy April Fools'!"
	}
	// 程序员节 (256天 = 第256天，闰年9月13日，平年9月12日)
	if month == 9 {
		isLeap := isLeapYear(now.Year())
		pDay := 12
		if isLeap {
			pDay = 13
		}
		if day == pDay {
			return "🖥️ Happy Programmers' Day (2^8 = 256)"
		}
	}
	// 万圣节
	if month == 10 && day == 31 {
		return "🎃 Boo! 运行时错误潜伏在每个 commit 里..."
	}
	// 圣诞节
	if month == 12 && day == 25 {
		return "🎄 Merry Christmas! 愿所有 PR 都能顺利 merge"
	}
	return ""
}

// isLeapYear 判断是否为闰年
func isLeapYear(year int) bool {
	return year%4 == 0 && (year%100 != 0 || year%400 == 0)
}

// ---------------------------------------------------------------------------
// 彩蛋 #5: /sudo — 权限拒绝
// ---------------------------------------------------------------------------

// sudoMessages 随机权限拒绝消息
var sudoMessages = []string{
	"🚫 root is not in the sudoers file. This incident will be reported.",
	"🚫 Nice try. Permission denied. Try /help instead.",
	"🚫 I'm sorry Dave, I'm afraid I can't do that.",
	"🚫 ACCESS DENIED. Please contact your system administrator (you).",
	"🚫 You shall not pass! — Gandalf",
	"🚫 Segmentation fault (core dumped). Just kidding.",
	"🚫 Error: Insufficient karma. Try contributing to open source first.",
	"🚫 403 Forbidden: Even the Matrix can't grant you sudo access here.",
	"🚫 sudo: a terminal error has occurred. Try rebooting the universe.",
	"🚫 Warning: Running with sudo may cause spontaneous combustion.",
}

// randomSudoMessage 返回一条随机的 sudo 拒绝消息
func randomSudoMessage() string {
	return sudoMessages[rand.Intn(len(sudoMessages))]
}

// ---------------------------------------------------------------------------
// 彩蛋 #6: /fortune — 程序员签语饼
// ---------------------------------------------------------------------------

// fortuneMessages 程序员签语饼内容 + 幸运数字
var fortuneMessages = []struct {
	text  string
	lucky int
}{
	{"A well-written test is worth a thousand bug reports.", 7},
	{"Your code will compile on the first try today. Probably.", 42},
	{"Great debugging session awaits you. Coffee helps.", 13},
	{"Trust your types. Let the compiler be your guide.", 21},
	{"A chance encounter with a semicolon will change your life.", 88},
	{"The bug you seek is not where you think it is.", 64},
	{"Someone will refactor your legacy code. Rejoice.", 3},
	{"An unexpected git bisect will reveal the truth.", 27},
	{"Your pull request will be approved without comments.", 99},
	{"The answer lies in the logs. Always check the logs.", 1},
	{"Today is a good day to delete dead code.", 55},
	{"A clever one-liner will impress your reviewer.", 16},
	{"Embrace the merge conflict. Growth comes from resolution.", 33},
	{"The stack trace is long, but the fix is one line.", 73},
	{"Do not fear the legacy code. It was once modern too.", 48},
	{"Your CI pipeline will be green today. All tests pass.", 100},
	{"A rubber duck will reveal what hours of debugging could not.", 9},
	{"The best code is no code. The second best is someone else's.", 0},
	{"A dependency update will break everything. Pin your versions.", 66},
	{"Your log messages will be poetic and informative.", 37},
}

// randomFortune 返回一条随机签语饼消息和幸运数字
func randomFortune() (string, int) {
	f := fortuneMessages[rand.Intn(len(fortuneMessages))]
	return f.text, f.lucky
}

// ---------------------------------------------------------------------------
// 彩蛋 #7: 三连 /version — 版本强迫症成就
// ---------------------------------------------------------------------------

// versionAchievementArt 版本强迫症成就画面
var versionAchievementArt = `
 ┌────────────────────────────────────────────┐
 │                                            │
 │        🏆 ACHIEVEMENT UNLOCKED! 🏆        │
 │                                            │
 │       「 版 本 强 迫 症 」                 │
 │                                            │
 │    You checked the version 3 times         │
 │    in under 10 seconds.                    │
 │                                            │
 │    Yes, it's still %s         │
 │                                            │
 │         +100 OCD points                     │
 │                                            │
 └────────────────────────────────────────────┘
`

// ---------------------------------------------------------------------------
// 彩蛋 #8: /zen — 禅意时刻
// ---------------------------------------------------------------------------

// zenHaiku 俳句 + 哲理消息
var zenHaiku = []struct {
	haiku   string
	message string
}{
	{"代码如流水，\nBug 在暗处藏身，\n测试光照之。", "The best error message is the one that never shows up."},
	{"键盘声如雨，\n屏幕映照凌晨光，\n一杯咖啡凉。", "Before debugging, take a walk. The answer often comes when you stop looking."},
	{"功能堆如山，\n简洁最难求，\n少即是多矣。", "Perfection is achieved not when there is nothing more to add, but when there is nothing left to take away."},
	{"函数短如诗，\n命名清晰见其义，\n重构日日新。", "Code is like humor. When you have to explain it, it's bad."},
	{"Git 提交清晰，\n回溯如行平坦路，\n未来我感谢。", "Commit early, commit often. Your future self will thank you."},
	{"终端黑如夜，\n光标闪烁如星辰，\n代码即宇宙。", "In the beginning there was nothing, which exploded. Then someone wrote `git init`."},
	{"编译零警告，\n测试全绿心自安，\n部署一瞬间。", "The feeling of all tests passing is the programmer's greatest natural high."},
	{"空格或 Tab，\n争论千年无定论，\n用 prettier 罢。", "The strongest of all warriors are these two — time and patience."},
}

// randomZen 返回一条随机俳句和哲理消息
func randomZen() (string, string) {
	z := zenHaiku[rand.Intn(len(zenHaiku))]
	return z.haiku, z.message
}

// ---------------------------------------------------------------------------
// 彩蛋激活入口 — 集中管理
// ---------------------------------------------------------------------------

// activateEasterEgg 激活指定彩蛋，启动定时消失计时器。
// duration: 彩蛋显示持续时间（0 表示不会自动消失）
func (m *cliModel) activateEasterEgg(mode easterEggMode, duration time.Duration) tea.Cmd {
	m.easterEgg = mode
	m.easterEggTimer = 0

	if duration > 0 {
		return tea.Tick(duration, func(time.Time) tea.Msg {
			return easterEggDoneMsg{}
		})
	}
	return nil
}

// handleEasterEggCommand 处理隐藏的彩蛋斜杠命令。
// 返回 true 表示命令已被彩蛋系统处理，不需要继续路由。
func (m *cliModel) handleEasterEggCommand(cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return false
	}
	command := strings.ToLower(parts[0])

	switch command {
	case "/matrix":
		// 彩蛋 #2: 黑客帝国代码雨
		m.activateEasterEgg(easterEggMatrix, 8*time.Second)
		return true

	case "/sudo":
		// 彩蛋 #5: 权限拒绝
		m.appendSystem(randomSudoMessage())
		m.updateViewportContent()
		return true

	case "/fortune":
		// 彩蛋 #6: 程序员签语饼
		text, lucky := randomFortune()
		m.appendSystem(fmt.Sprintf("🍪 Fortune Cookie\n\n%s\n\nLucky number: %d", text, lucky))
		m.updateViewportContent()
		return true

	case "/zen":
		// 彩蛋 #8: 禅意时刻
		haiku, message := randomZen()
		zenText := fmt.Sprintf("🕉️ Zen Mode\n\n%s\n\n— %s", haiku, message)
		m.appendSystem(zenText)
		m.updateViewportContent()
		return true

	case "/version":
		// 彩蛋 #7: 三连 /version 检测
		m.versionHitTimes = append(m.versionHitTimes, time.Now())
		// 只保留最近 3 次
		if len(m.versionHitTimes) > 3 {
			m.versionHitTimes = m.versionHitTimes[len(m.versionHitTimes)-3:]
		}
		if len(m.versionHitTimes) == 3 {
			elapsed := m.versionHitTimes[2].Sub(m.versionHitTimes[0])
			if elapsed <= 10*time.Second {
				// 触发版本强迫症成就
				return false // 让正常 /version 路由也执行（显示版本号）
			}
		}
		return false

	default:
		return false
	}
}

// checkVersionOCD 检测是否触发三连 /version 彩蛋。
// 在 /version 正常处理后调用。
func (m *cliModel) checkVersionOCD() tea.Cmd {
	if len(m.versionHitTimes) == 3 {
		elapsed := m.versionHitTimes[2].Sub(m.versionHitTimes[0])
		if elapsed <= 10*time.Second {
			// 触发成就！
			m.versionHitTimes = nil // 重置
			art := fmt.Sprintf(versionAchievementArt, version.Version)
			m.activateEasterEgg(easterEggVersion, 5*time.Second)
			m.easterEggCustom = art
			return nil
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// 彩蛋渲染
// ---------------------------------------------------------------------------

// renderEasterEggOverlay 渲染彩蛋覆盖层。
// 如果没有激活的彩蛋，返回空字符串。
func (m *cliModel) renderEasterEggOverlay() string {
	switch m.easterEgg {
	case easterEggKonami:
		return m.renderKonamiOverlay()
	case easterEggMatrix:
		return m.renderMatrixOverlay()
	case easterEggAnswer42:
		return m.renderAnswer42Overlay()
	case easterEggVersion:
		return m.renderVersionOverlay()
	default:
		return ""
	}
}

// renderKonamiOverlay 渲染 Konami Code 庆祝画面
func (m *cliModel) renderKonamiOverlay() string {
	green := lipgloss.NewStyle().Foreground(lipgloss.Color("#00FF00")).Bold(true)
	content := green.Render(konamiASCII)
	return centerOverlay(content, m.width, m.height)
}

// renderMatrixOverlay 渲染 Matrix 代码雨画面
func (m *cliModel) renderMatrixOverlay() string {
	green := lipgloss.NewStyle().Foreground(lipgloss.Color("#00FF00"))
	brightGreen := lipgloss.NewStyle().Foreground(lipgloss.Color("#33FF33")).Bold(true)

	if len(m.matrixRainLines) == 0 {
		return ""
	}

	var sb strings.Builder
	for i, line := range m.matrixRainLines {
		// 最后一行使用高亮绿色（"头部"效果）
		if i == len(m.matrixRainLines)-1 {
			sb.WriteString(brightGreen.Render(line))
		} else {
			// 根据行号产生渐变亮度效果
			style := green
			if i < len(m.matrixRainLines)/3 {
				style = green.Faint(true)
			}
			sb.WriteString(style.Render(line))
		}
		if i < len(m.matrixRainLines)-1 {
			sb.WriteString("\n")
		}
	}

	// 底部添加 "Wake up, Neo..."
	wakeMsg := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#FFFFFF")).
		Bold(true).
		Render("\n    Wake up, Neo...")

	return centerOverlay(sb.String()+wakeMsg, m.width, m.height)
}

// renderAnswer42Overlay 渲染 "The Answer is 42" 画面
func (m *cliModel) renderAnswer42Overlay() string {
	yellow := lipgloss.NewStyle().Foreground(lipgloss.Color("#FFD700")).Bold(true)
	content := yellow.Render(answer42Art)
	return centerOverlay(content, m.width, m.height)
}

// renderVersionOverlay 渲染版本强迫症成就画面
func (m *cliModel) renderVersionOverlay() string {
	gold := lipgloss.NewStyle().Foreground(lipgloss.Color("#FFD700")).Bold(true)
	content := gold.Render(m.easterEggCustom)
	return centerOverlay(content, m.width, m.height)
}

// centerOverlay 将内容居中到指定宽高的终端中
func centerOverlay(content string, termW, termH int) string {
	lines := strings.Split(content, "\n")
	maxW := 0
	for _, line := range lines {
		w := lipgloss.Width(line)
		if w > maxW {
			maxW = w
		}
	}

	padLeft := (termW - maxW) / 2
	if padLeft < 0 {
		padLeft = 0
	}
	padTop := (termH - len(lines)) / 2
	if padTop < 1 {
		padTop = 1
	}

	var sb strings.Builder
	for i := 0; i < padTop; i++ {
		sb.WriteString("\n")
	}
	for _, line := range lines {
		sb.WriteString(strings.Repeat(" ", padLeft))
		sb.WriteString(line)
		sb.WriteString("\n")
	}
	return sb.String()
}

// ---------------------------------------------------------------------------
// 彩蛋 #4: 节日 Splash 描述文字
// ---------------------------------------------------------------------------

// getHolidaySplashDesc 获取节日版 splash 描述文字（如果今天是特殊日期）
func getHolidaySplashDesc() string {
	if desc := holidaySplash(); desc != "" {
		return desc
	}
	return ""
}
