//go:build windows

package cmdbuilder

import "strings"

// defaultShell is the shell binary used for shell-mode command execution on Windows.
const defaultShell = "powershell.exe"

// defaultShellFlag is the flag used to pass a command string to the shell.
// powershell.exe -Command "command"
const defaultShellFlag = "-Command"

// shellEscape escapes a string for use in PowerShell commands.
// PowerShell uses backtick ` as the escape character, not single quotes.
// We wrap in single quotes which are literal in PowerShell (no variable expansion),
// and escape any embedded single quotes with double quotes.
func shellEscape(s string) string {
	// In PowerShell single-quoted strings:
	// - Single quotes don't need escaping except to end the string
	// - We use '' (two single quotes) to escape a single quote inside single quotes
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
