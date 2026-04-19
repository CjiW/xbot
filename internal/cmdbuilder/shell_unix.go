//go:build !windows

package cmdbuilder

import "strings"

// shellEscape wraps a path in single quotes, escaping any embedded single quotes.
func shellEscape(s string) string {
	// Replace ' with '\'' (end quote, escaped quote, start quote)
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
