package channel

import (
	"regexp"
	"strings"

	"github.com/mattn/go-runewidth"
	"github.com/pgavlin/mermaid-ascii/pkg/diagram"
	"github.com/pgavlin/mermaid-ascii/pkg/render"
)

// mermaidBlockRe matches ```mermaid ... ``` code blocks.
var mermaidBlockRe = regexp.MustCompile("(?s)```mermaid\\s*\n(.*?)```")

// renderMermaidBlocks replaces all ```mermaid code blocks in markdown content
// with their ASCII/Unicode art representation. Each output line is truncated to
// maxW display columns to prevent glamour from wrapping them.
func renderMermaidBlocks(content string, maxW int) string {
	return mermaidBlockRe.ReplaceAllStringFunc(content, func(match string) string {
		sub := mermaidBlockRe.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		src := strings.TrimSpace(sub[1])
		if src == "" {
			return match
		}

		output, err := render.Render(src, diagram.DefaultConfig())
		if err != nil {
			return match
		}

		// Truncate each line to maxW columns so glamour won't wrap.
		// mermaid-ascii output is plain text (no ANSI), so we can use
		// runewidth directly.
		if maxW > 0 {
			lines := strings.Split(output, "\n")
			for i, line := range lines {
				line = strings.TrimRight(line, " \t")
				if runewidth.StringWidth(line) > maxW {
					lines[i] = truncateStringWidth(line, maxW)
				} else {
					lines[i] = line
				}
			}
			output = strings.Join(lines, "\n")
		}

		return "```\n" + output + "\n```"
	})
}

// truncateStringWidth truncates a plain-text string (no ANSI) to maxW display
// columns, handling wide runes (CJK, box-drawing) correctly.
func truncateStringWidth(s string, maxW int) string {
	var buf strings.Builder
	w := 0
	for _, r := range s {
		rw := runewidth.RuneWidth(r)
		if w+rw > maxW {
			break
		}
		buf.WriteRune(r)
		w += rw
	}
	return buf.String()
}
