package channel

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
)

const (
	debugDir        = ".xbot/debug"
	debugSockName   = "ctl.sock"
	debugUIFile     = "ui_capture.log"
	debugCaptureMax = 200 // max lines to keep in capture log (ring buffer)
)

// parseKeyInput parses a human-readable key string into a tea.KeyPressMsg.
// Supports: plain chars (a, A, 1), special keys (enter, tab, esc, up, down, etc.),
// and modifier combos (ctrl+c, ctrl+z, alt+enter, shift+tab).
func parseKeyInput(input string) tea.KeyPressMsg {
	input = strings.TrimSpace(input)
	if input == "" {
		return tea.KeyPressMsg{}
	}

	var mod tea.KeyMod
	// Parse modifiers (left to right)
	for {
		if strings.HasPrefix(input, "ctrl+") {
			mod |= tea.ModCtrl
			input = input[5:]
		} else if strings.HasPrefix(input, "alt+") {
			mod |= tea.ModAlt
			input = input[4:]
		} else if strings.HasPrefix(input, "shift+") {
			mod |= tea.ModShift
			input = input[6:]
		} else {
			break
		}
	}

	lower := strings.ToLower(input)

	// Special keys
	switch lower {
	case "enter", "return":
		return tea.KeyPressMsg{Code: tea.KeyEnter, Mod: mod}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: mod}
	case "esc", "escape":
		return tea.KeyPressMsg{Code: tea.KeyEsc, Mod: mod}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp, Mod: mod}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown, Mod: mod}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft, Mod: mod}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight, Mod: mod}
	case "home":
		return tea.KeyPressMsg{Code: tea.KeyHome, Mod: mod}
	case "end":
		return tea.KeyPressMsg{Code: tea.KeyEnd, Mod: mod}
	case "pgup", "pageup":
		return tea.KeyPressMsg{Code: tea.KeyPgUp, Mod: mod}
	case "pgdown", "pagedown":
		return tea.KeyPressMsg{Code: tea.KeyPgDown, Mod: mod}
	case "backspace", "bs":
		return tea.KeyPressMsg{Code: tea.KeyBackspace, Mod: mod}
	case "delete", "del":
		return tea.KeyPressMsg{Code: tea.KeyDelete, Mod: mod}
	case "insert", "ins":
		return tea.KeyPressMsg{Code: tea.KeyInsert, Mod: mod}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Mod: mod}
	case "f1":
		return tea.KeyPressMsg{Code: tea.KeyF1, Mod: mod}
	case "f2":
		return tea.KeyPressMsg{Code: tea.KeyF2, Mod: mod}
	case "f3":
		return tea.KeyPressMsg{Code: tea.KeyF3, Mod: mod}
	case "f4":
		return tea.KeyPressMsg{Code: tea.KeyF4, Mod: mod}
	case "f5":
		return tea.KeyPressMsg{Code: tea.KeyF5, Mod: mod}
	case "f6":
		return tea.KeyPressMsg{Code: tea.KeyF6, Mod: mod}
	case "f7":
		return tea.KeyPressMsg{Code: tea.KeyF7, Mod: mod}
	case "f8":
		return tea.KeyPressMsg{Code: tea.KeyF8, Mod: mod}
	case "f9":
		return tea.KeyPressMsg{Code: tea.KeyF9, Mod: mod}
	case "f10":
		return tea.KeyPressMsg{Code: tea.KeyF10, Mod: mod}
	case "f11":
		return tea.KeyPressMsg{Code: tea.KeyF11, Mod: mod}
	case "f12":
		return tea.KeyPressMsg{Code: tea.KeyF12, Mod: mod}
	}

	// Single printable character
	runes := []rune(input)
	if len(runes) == 1 {
		return tea.KeyPressMsg{Code: runes[0], Text: input, Mod: mod}
	}

	// Fallback: treat as text
	return tea.KeyPressMsg{Code: runes[0], Text: input, Mod: mod}
}

// debugCaptureUI dumps the current TUI view to the capture log file.
func (m *cliModel) debugCaptureUI() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	dir := filepath.Join(home, debugDir)
	os.MkdirAll(dir, 0700)

	view := m.View().Content
	if view == "" {
		return
	}

	path := filepath.Join(dir, debugUIFile)

	// Ring buffer: keep last N captures separated by timestamps
	lines := strings.Split(view, "\n")

	// Read existing content to append
	var existing []string
	if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
		existing = strings.Split(string(data), "\n")
	}

	// Trim to keep size bounded
	header := fmt.Sprintf("--- %s ---", time.Now().Format("15:04:05"))
	newLines := []string{"", header}
	newLines = append(newLines, lines...)
	combined := append(existing, newLines...)

	// Keep last debugCaptureMax lines
	if len(combined) > debugCaptureMax {
		combined = combined[len(combined)-debugCaptureMax:]
	}

	_ = os.WriteFile(path, []byte(strings.Join(combined, "\n")), 0600)
}

// debugSockListener manages the Unix socket for key injection.
type debugSockListener struct {
	listener net.Listener
	done     chan struct{}
	wg       sync.WaitGroup
}

// startDebugSock creates and starts listening on the debug Unix socket.
// Each accepted connection reads lines, parses them as key inputs, and
// injects them into the tea program via sendFn.
func startDebugSock(sockPath string, sendFn func(tea.Msg)) (*debugSockListener, error) {
	// Remove stale socket
	os.Remove(sockPath)

	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("debug socket: %w", err)
	}

	dl := &debugSockListener{
		listener: listener,
		done:     make(chan struct{}),
	}

	dl.wg.Add(1)
	go dl.acceptLoop(sendFn)

	return dl, nil
}

func (dl *debugSockListener) acceptLoop(sendFn func(tea.Msg)) {
	defer dl.wg.Done()
	for {
		conn, err := dl.listener.Accept()
		if err != nil {
			select {
			case <-dl.done:
				return
			default:
				continue
			}
		}
		dl.wg.Add(1)
		go dl.handleConn(conn, sendFn)
	}
}

func (dl *debugSockListener) handleConn(conn net.Conn, sendFn func(tea.Msg)) {
	defer dl.wg.Done()
	defer conn.Close()

	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		if strings.EqualFold(line, "quit") || strings.EqualFold(line, "exit") {
			return
		}
		key := parseKeyInput(line)
		if key.Code != 0 || key.Text != "" {
			sendFn(key)
		}
	}
}

func (dl *debugSockListener) Stop() {
	close(dl.done)
	dl.listener.Close()
	dl.wg.Wait()
}

// debugSockPath returns the Unix socket path for the debug control interface.
func debugSockPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, debugDir)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return filepath.Join(dir, debugSockName), nil
}
