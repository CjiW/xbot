package channel

import (
	"regexp"
	"strings"

	"github.com/pgavlin/mermaid-ascii/pkg/diagram"
	"github.com/pgavlin/mermaid-ascii/pkg/render"
)

// mermaidBlockRe matches ```mermaid ... ``` code blocks.
var mermaidBlockRe = regexp.MustCompile("(?s)```mermaid\\s*\n(.*?)```")

// renderMermaidBlocks replaces all ```mermaid code blocks in markdown content
// with their ASCII/Unicode art representation.
func renderMermaidBlocks(content string) string {
	return mermaidBlockRe.ReplaceAllStringFunc(content, func(match string) string {
		// Extract the mermaid source code
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
			// Rendering failed, keep original block
			return match
		}

		// Wrap in a plain code block to preserve formatting
		return "```\n" + output + "\n```"
	})
}
