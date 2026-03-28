package tools

import (
	"context"
	"fmt"
	"os"
)

// DeniedSandbox implements Sandbox by rejecting ALL operations with permission errors.
// It is used as a security fallback for users who should not have any sandbox access
// (e.g. web-registered users when WEB_USER_SERVER_RUNNER=false).
//
// Unlike NoneSandbox (which executes directly on the host), DeniedSandbox guarantees
// zero access to the host filesystem or command execution.
type DeniedSandbox struct{}

func (s *DeniedSandbox) Name() string              { return "denied" }
func (s *DeniedSandbox) Workspace(_ string) string { return "" }

func (s *DeniedSandbox) Close() error                        { return nil }
func (s *DeniedSandbox) CloseForUser(userID string) error    { return nil }
func (s *DeniedSandbox) IsExporting(userID string) bool      { return false }
func (s *DeniedSandbox) ExportAndImport(userID string) error { return nil }

func (s *DeniedSandbox) GetShell(userID string, workspace string) (string, error) {
	return "", fmt.Errorf("sandbox access denied for user %q: no runner available", userID)
}

func (s *DeniedSandbox) Exec(ctx context.Context, spec ExecSpec) (*ExecResult, error) {
	return nil, fmt.Errorf("sandbox access denied for user %q: no runner available", spec.UserID)
}

func (s *DeniedSandbox) ReadFile(ctx context.Context, path string, userID string) ([]byte, error) {
	return nil, fmt.Errorf("sandbox access denied for user %q: no runner available", userID)
}

func (s *DeniedSandbox) WriteFile(ctx context.Context, path string, data []byte, perm os.FileMode, userID string) error {
	return fmt.Errorf("sandbox access denied for user %q: no runner available", userID)
}

func (s *DeniedSandbox) Stat(ctx context.Context, path string, userID string) (*SandboxFileInfo, error) {
	return nil, fmt.Errorf("sandbox access denied for user %q: no runner available", userID)
}

func (s *DeniedSandbox) ReadDir(ctx context.Context, path string, userID string) ([]DirEntry, error) {
	return nil, fmt.Errorf("sandbox access denied for user %q: no runner available", userID)
}

func (s *DeniedSandbox) MkdirAll(ctx context.Context, path string, perm os.FileMode, userID string) error {
	return fmt.Errorf("sandbox access denied for user %q: no runner available", userID)
}

func (s *DeniedSandbox) Remove(ctx context.Context, path string, userID string) error {
	return fmt.Errorf("sandbox access denied for user %q: no runner available", userID)
}

func (s *DeniedSandbox) RemoveAll(ctx context.Context, path string, userID string) error {
	return fmt.Errorf("sandbox access denied for user %q: no runner available", userID)
}
