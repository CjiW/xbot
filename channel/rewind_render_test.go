package channel

import (
	"fmt"
	"strings"
	"testing"
)

func TestRewindResultBlockAlignment(t *testing.T) {
	styles := buildStyles(80)

	restored := []string{
		"/home/user/src/xbot/agent/backend.go",
		"/home/user/src/xbot/agent/backend_remote.go",
		"/home/user/src/xbot/agent/backend_config.go",
	}
	deleted := []string{
		"/home/user/src/xbot/docs/new_file.md",
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

	t.Logf("Rendered output:\n%s", sb.String())

	lines := strings.Split(sb.String(), "\n")
	for i, line := range lines {
		if line == "" {
			continue
		}
		visible := stripAnsiForTest(line)
		// File path lines should start with exactly 4 spaces
		if strings.Contains(visible, "/") && !strings.HasPrefix(visible, "    ") {
			t.Errorf("line %d: wrong indent (expected 4 spaces): visible_width=%d  %q", i, len(visible), visible)
		}
	}
}

func stripAnsiForTest(s string) string {
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
