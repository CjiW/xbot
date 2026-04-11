//go:build !windows

package cmdbuilder

// defaultShell is the shell binary used for shell-mode command execution.
const defaultShell = "/bin/sh"

// defaultShellFlag is the flag used to pass a command string to the shell.
// /bin/sh -c "command"
const defaultShellFlag = "-c"
