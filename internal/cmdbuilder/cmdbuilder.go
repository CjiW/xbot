// Package cmdbuilder provides shared command construction logic
// for both server-side NoneSandbox and runner-side NativeExecutor.
// It centralizes exec.Cmd creation with optional OS user switching via sudo.
package cmdbuilder

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// Config controls command construction behavior.
type Config struct {
	// RunAsUser is the OS username to execute the command as.
	// When set, the command is wrapped with: sudo -n -H -u <user> --
	// Requires NOPASSWD sudoers entry for the target user.
	// Empty string means execute as the current process user (no wrapping).
	RunAsUser string
}

// Build creates an *exec.Cmd with optional OS user switching.
//
// When cfg.RunAsUser is set (Unix only), the command is wrapped with sudo:
//
//	sudo -n -H -u <user> -- <shell> <flag> "<command>"
//
// When cfg.RunAsUser is empty, the command is constructed directly:
//
//	<shell> <flag> "<command>"
//
// On Windows, RunAsUser is not supported and returns an error.
// The default shell is /bin/sh on Unix and powershell.exe on Windows.
//
// Parameters:
//   - ctx: context for cancellation (nil → no context)
//   - shell: true → use shell command; false → use args directly
//   - command: the command string (used when shell=true)
//   - args: the arg list (used when shell=false, must be non-empty)
//   - dir: working directory
//   - env: additional environment variables (appended to os.Environ())
//   - cfg: configuration including RunAsUser
func Build(ctx context.Context, shell bool, command string, args []string,
	dir string, env []string, cfg Config) (*exec.Cmd, error) {

	var cmd *exec.Cmd

	if cfg.RunAsUser != "" {
		// OS user switching via sudo is not supported on Windows.
		if runtime.GOOS == "windows" {
			return nil, fmt.Errorf("run_as (user switching) is not supported on Windows")
		}
		// Wrap with sudo -n -H -u <user> --
		if shell {
			sudoArgs := []string{"-n", "-H", "-u", cfg.RunAsUser, "--", defaultShell, defaultShellFlag, command}
			if ctx != nil {
				cmd = exec.CommandContext(ctx, "sudo", sudoArgs...)
			} else {
				cmd = exec.Command("sudo", sudoArgs...)
			}
		} else {
			if len(args) == 0 {
				return nil, fmt.Errorf("non-shell exec requires args to be set")
			}
			sudoArgs := append([]string{"-n", "-H", "-u", cfg.RunAsUser, "--"}, args...)
			if ctx != nil {
				cmd = exec.CommandContext(ctx, "sudo", sudoArgs...)
			} else {
				cmd = exec.Command("sudo", sudoArgs...)
			}
		}
	} else {
		// No user switching — direct execution
		if shell {
			if ctx != nil {
				cmd = exec.CommandContext(ctx, defaultShell, defaultShellFlag, command)
			} else {
				cmd = exec.Command(defaultShell, defaultShellFlag, command)
			}
		} else {
			if len(args) == 0 {
				return nil, fmt.Errorf("non-shell exec requires args to be set")
			}
			if ctx != nil {
				cmd = exec.CommandContext(ctx, args[0], args[1:]...)
			} else {
				cmd = exec.Command(args[0], args[1:]...)
			}
		}
	}

	if dir != "" {
		cmd.Dir = dir
	}
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}

	return cmd, nil
}

// WriteFileAsUser writes data to a file as the specified OS user via sudo.
// If runAsUser is empty, falls back to os.WriteFile directly.
func WriteFileAsUser(runAsUser, path string, data []byte, perm os.FileMode) error {
	if runAsUser == "" {
		return os.WriteFile(path, data, perm)
	}

	// Use sudo to write as the target user:
	// sudo -n -H -u <user> -- <shell> <flag> "cat > '<escaped_path>' && chmod <perm> '<escaped_path>'"
	escaped := shellEscape(path)
	permStr := fmt.Sprintf("%o", perm)
	shellCmd := fmt.Sprintf("cat > %s && chmod %s %s", escaped, permStr, escaped)

	cmd := exec.Command("sudo", "-n", "-H", "-u", runAsUser, "--", defaultShell, defaultShellFlag, shellCmd)
	cmd.Stdin = bytes.NewReader(data)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("write file as user %q failed: %w: %s", runAsUser, err, stderr.String())
	}
	return nil
}

// ReadFileAsUser reads a file as the specified OS user via sudo.
// If runAsUser is empty, falls back to os.ReadFile directly.
func ReadFileAsUser(runAsUser, path string) ([]byte, error) {
	if runAsUser == "" {
		return os.ReadFile(path)
	}

	// Pass the raw path as an argv element. Do NOT shell-escape here because
	// exec.Command does not invoke a shell; quoting would become part of the filename.
	cmd := exec.Command("sudo", "-n", "-H", "-u", runAsUser, "--", "cat", path)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("read file as user %q failed: %w: %s", runAsUser, err, stderr.String())
	}
	return stdout.Bytes(), nil
}

// MkdirAllAsUser creates directories as the specified OS user via sudo.
// If runAsUser is empty, falls back to os.MkdirAll directly.
func MkdirAllAsUser(runAsUser, path string, perm os.FileMode) error {
	if runAsUser == "" {
		return os.MkdirAll(path, perm)
	}

	escaped := shellEscape(path)
	permStr := fmt.Sprintf("%o", perm)
	shellCmd := fmt.Sprintf("mkdir -p %s && chmod %s %s", escaped, permStr, escaped)

	cmd := exec.Command("sudo", "-n", "-H", "-u", runAsUser, "--", defaultShell, defaultShellFlag, shellCmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("mkdir as user %q failed: %w: %s", runAsUser, err, stderr.String())
	}
	return nil
}

// shellEscape wraps a path in single quotes, escaping any embedded single quotes.
func shellEscape(s string) string {
	// Replace ' with '\'' (end quote, escaped quote, start quote)
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// CheckSudoers tests whether sudo -n -u <user> -- true works (NOPASSWD configured).
// Returns nil if sudoers is properly configured for the given user.
func CheckSudoers(user string) error {
	cmd := exec.Command("sudo", "-n", "-H", "-u", user, "--", "true")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("sudoers not configured for user %q: %s", user, strings.TrimSpace(string(output)))
	}
	return nil
}

// GenerateSudoersScript generates a bash script that sets up /etc/sudoers.d/xbot.
// The script validates syntax with visudo -c before installing.
func GenerateSudoersScript(defaultUser, privilegedUser string) string {
	currentUser := os.Getenv("USER")
	if currentUser == "" {
		currentUser = "$(whoami)"
	}

	var entries []string
	if defaultUser != "" {
		entries = append(entries, fmt.Sprintf("%s ALL=(%s) NOPASSWD: ALL", currentUser, defaultUser))
	}
	if privilegedUser != "" {
		entries = append(entries, fmt.Sprintf("%s ALL=(%s) NOPASSWD: ALL", currentUser, privilegedUser))
	}

	entriesStr := ""
	for _, e := range entries {
		entriesStr += e + "\n"
	}

	return fmt.Sprintf(`#!/bin/bash
# xbot permission control setup script
# Run with: sudo bash %s

set -e

SUDOERS_FILE="/etc/sudoers.d/xbot"

echo "Setting up sudoers for xbot permission control..."
echo ""

cat > /tmp/xbot-sudoers.$$ << 'EOF'
%s
EOF

# Validate syntax before installing
if visudo -c -f /tmp/xbot-sudoers.$$ >/dev/null 2>&1; then
    install -m 0440 /tmp/xbot-sudoers.$$ "$SUDOERS_FILE"
    rm -f /tmp/xbot-sudoers.$$
    echo "✓ sudoers configured at $SUDOERS_FILE"
else
    cat /tmp/xbot-sudoers.$$
    rm -f /tmp/xbot-sudoers.$$
    echo "✗ sudoers syntax check failed. Please fix manually."
    exit 1
fi
`, "<generated-script>", entriesStr)
}
