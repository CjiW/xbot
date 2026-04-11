//go:build !windows

package channel

import (
	"os"
	"os/signal"
	"syscall"
)

// setupCtrlZSuspend sets up a SIGTSTP (Ctrl+Z) handler that restores the terminal
// and exits immediately. Unix-only — Windows doesn't have SIGTSTP.
func setupCtrlZSuspend(c *CLIChannel, origStdout, origStderr *os.File) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTSTP)
	go func() {
		<-sigCh
		// 恢复终端并直接退出，不依赖 bubbletea 的 Quit 流程
		_ = c.program.ReleaseTerminal()
		os.Stdout = origStdout
		os.Stderr = origStderr
		os.Exit(0)
	}()
}
