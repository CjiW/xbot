//go:build windows

package cmdbuilder

// defaultShell is the shell binary used for shell-mode command execution on Windows.
const defaultShell = "powershell.exe"

// defaultShellFlag is the flag used to pass a command string to the shell.
// powershell.exe -Command "command"
const defaultShellFlag = "-Command"
