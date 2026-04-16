package channel

import (
	"fmt"
	"strings"
	"testing"
)

func TestRewindResultBlockAlignment(t *testing.T) {
	// Simulate the renderRewindResultBlock function with real styles
	styles := buildStyles(80)

	restored := []string{
		"/home/user/src/xbot/agent/backend.go",
		"/home/user/src/xbot/agent/backend_remote.go",
		"/home/user/src/xbot/agent/backend_config.go",
		"/home/user/src/xbot/channel/cli_message.go",
		"/home/user/src/xbot/channel/cli_panel.go",
		"/home/user/src/xbot/main.go",
		"/home/user/src/xbot/cmd/xbot-cli/main.go",
	}
	deleted := []string{
		"/home/user/src/xbot/docs/new_file.md",
	}
	errors := []string{
		"/home/user/src/xbot/some/big_file.bin: write: permission denied",
	}

	var sb strings.Builder
	sb.WriteString("\n")
	sb.WriteString(styles.ProgressDone.Bold(true).Render("  Rewind complete"))
	sb.WriteString("\n")

	if len(restored) > 0 {
		fmt.Fprintf(&sb, "  Files restored: %d\n", len(restored))
		for _, f := range restored {
			sb.WriteString(styles.TextMutedSt.Render(fmt.Sprintf("    %s", f)))
			sb.WriteString("\n")
		}
	}
	if len(deleted) > 0 {
		fmt.Fprintf(&sb, "  Files deleted: %d\n", len(deleted))
		for _, f := range deleted {
			sb.WriteString(styles.TextMutedSt.Render(fmt.Sprintf("    %s", f)))
			sb.WriteString("\n")
		}
	}
	if len(errors) > 0 {
		for _, e := range errors {
			sb.WriteString(styles.ProgressError.Render(fmt.Sprintf("  Error: %s", e)))
			sb.WriteString("\n")
		}
	}

	// Print raw output for visual inspection
	t.Logf("Raw output:\n%s", sb.String())

	// Check that all file path lines have consistent indent (4 spaces)
	lines := strings.Split(sb.String(), "\n")
	for i, line := range lines {
		if line == "" {
			continue
		}
		visible := stripAnsi(line)
		// File lines should start with exactly 4 spaces
		if strings.HasPrefix(visible, "   ") && !strings.HasPrefix(visible, "    ") {
			// Wrong indent (3 spaces)
			t.Errorf("line %d: wrong indent: %q", i, visible)
		}
		// "Files restored" and "Files deleted" should start with 2 spaces
		if strings.HasPrefix(visible, " Files") {
			t.Logf("  line %d: section header OK: %q", i, visible)
		}
		// Check that no file line has padding beyond the indent
		if strings.Contains(visible, "/") && strings.HasPrefix(visible, "                    ") {
			t.Errorf("line %d: excessive padding before path: visible_width=%d  %q", i, len(visible), visible)
		}
	}
}

func stripAnsi(s string) string {
	var result strings.Builder
	inEscape := false
	for _, r := range s {
		if r == '\x1b' {
			inEscape = true
			continue
		}
		if inEscape {
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
				inEscape = false
			}
			continue
		}
		result.WriteRune(r)
	}
	return result.String()
}
