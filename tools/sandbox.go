package tools

import (
	"context"
	"os"
	"time"
)

// MaxSandboxFileSize is the maximum file size for ReadFile/WriteFile (500MB).
const MaxSandboxFileSize = 500 * 1024 * 1024

// SandboxCtx returns a context with a 30-second timeout for sandbox I/O operations.
// The returned cancel function should be deferred to avoid resource leaks.
// This is used for single sandbox calls where no caller context is available.
func SandboxCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

// ExecSpec defines the parameters for a sandbox command execution.
type ExecSpec struct {
	Command   string        // executable or shell command
	Args      []string      // arguments (ignored when Shell=true)
	Shell     bool          // use shell for execution (sh -c)
	Dir       string        // working directory (absolute path in sandbox)
	Env       []string      // environment variables
	Stdin     string        // stdin input
	Timeout   time.Duration // execution timeout
	Workspace string        // workspace root (for sandbox setup)
	UserID    string        // user identity (for sandbox routing)

	// KeepAlive indicates that on timeout, the process should NOT be killed.
	// Instead, the caller takes ownership of the process via ExecResult.Process.
	// Currently only supported by NoneSandbox.
	KeepAlive bool
}

// ExecResult holds the result of a sandbox command execution.
type ExecResult struct {
	Stdout        string        // standard output
	Stderr        string        // standard error
	ExitCode      int           // exit code (-1 if timed out)
	TimedOut      bool          // whether execution timed out
	Process       *os.Process   // live process when KeepAlive=true and TimedOut=true (caller owns lifecycle)
	ExitCodeCh    chan int      // optional: receives real exit code from cmd.Wait goroutine after timeout
	OngoingOutput func() string // optional: reads accumulated output from capture goroutines (KeepAlive timeout only)
}

// SandboxFileInfo is the sandbox equivalent of os.FileInfo.
// Does NOT include Sys() — cross-process metadata is meaningless.
type SandboxFileInfo struct {
	Name    string      // base name
	Size    int64       // length in bytes
	Mode    os.FileMode // file mode bits
	ModTime time.Time   // modification time
	IsDir   bool        // is directory
}

// DirEntry represents a directory entry from ReadDir.
type DirEntry struct {
	Name  string
	IsDir bool
	Size  int64
}

// Sandbox defines the unified interface for all sandbox modes (none/docker/remote).
// All file path parameters must be absolute paths in sandbox format.
// Path conversion (sandbox↔host) is an internal concern of each implementation.
type Sandbox interface {
	// === Command Execution ===
	Exec(ctx context.Context, spec ExecSpec) (*ExecResult, error)

	// === File I/O ===
	// ReadFile reads the entire file at path. Path must be absolute.
	// Returns os.ErrNotExist if file does not exist.
	ReadFile(ctx context.Context, path string, userID string) ([]byte, error)

	// WriteFile writes data to path. Path must be absolute.
	// Does NOT auto-create parent directories — call MkdirAll first.
	WriteFile(ctx context.Context, path string, data []byte, perm os.FileMode, userID string) error

	// Stat returns file info. Path must be absolute.
	// Returns os.ErrNotExist if file does not exist.
	Stat(ctx context.Context, path string, userID string) (*SandboxFileInfo, error)

	// ReadDir lists directory entries. Path must be absolute.
	ReadDir(ctx context.Context, path string, userID string) ([]DirEntry, error)

	// MkdirAll creates directory tree. Path must be absolute.
	MkdirAll(ctx context.Context, path string, perm os.FileMode, userID string) error

	// Remove removes a file. Path must be absolute.
	Remove(ctx context.Context, path string, userID string) error

	// RemoveAll removes a directory tree. Path must be absolute.
	RemoveAll(ctx context.Context, path string, userID string) error

	// DownloadFile downloads a file from the given URL and saves it to outputPath.
	// For RemoteSandbox, the runner downloads directly (avoids server as proxy).
	// For DockerSandbox, the container downloads directly.
	// Path must be absolute.
	DownloadFile(ctx context.Context, url, outputPath string, userID string) error

	// === Shell Configuration ===
	// GetShell returns the preferred shell command for the user/workspace.
	GetShell(userID string, workspace string) (string, error)

	// === Lifecycle ===
	Name() string
	Workspace(userID string) string
	Close() error
	CloseForUser(userID string) error

	// === Export/Import (docker-specific) ===
	IsExporting(userID string) bool
	ExportAndImport(userID string) error
}

// WalkSandboxDir recursively walks a sandbox directory, equivalent to filepath.WalkDir.
// fn is called for each file (directories are traversed but not passed to fn).
func WalkSandboxDir(ctx context.Context, sb Sandbox, root, userID string, fn func(relPath string, entry DirEntry) error) error {
	return walkSandboxDir(ctx, sb, root, "", userID, fn)
}

func walkSandboxDir(ctx context.Context, sb Sandbox, dir, relBase, userID string, fn func(string, DirEntry) error) error {
	entries, err := sb.ReadDir(ctx, dir, userID)
	if err != nil {
		return err
	}
	for _, e := range entries {
		var relPath string
		if relBase == "" {
			relPath = e.Name
		} else {
			relPath = relBase + "/" + e.Name
		}
		if e.IsDir {
			if err := walkSandboxDir(ctx, sb, dir+"/"+e.Name, relPath, userID, fn); err != nil {
				return err
			}
		} else {
			if err := fn(relPath, e); err != nil {
				return err
			}
		}
	}
	return nil
}

// SandboxSyncer is an optional interface that Sandbox implementations can implement
// to support on-demand sync of global skills/agents (e.g., remote sandbox).
type SandboxSyncer interface {
	// EnsureSynced checks if global skills/agents have been synced to the runner
	// for the given user, and triggers sync if not. Safe to call repeatedly.
	EnsureSynced(ctx context.Context, userID string)
}

// SandboxResolver is an optional interface that a multi-sandbox implementation
// (e.g., SandboxRouter) can implement to resolve per-user sandbox instances.
// buildToolContext uses this to inject the user-specific sandbox into ToolContext.Sandbox,
// so that downstream code (shell, sandbox_exec, etc.) sees the correct Name(), Workspace(), etc.
type SandboxResolver interface {
	// SandboxForUser returns the user-specific Sandbox instance.
	// Falls back to the default sandbox if userID is empty or unknown.
	SandboxForUser(userID string) Sandbox
}

// SandboxExporter is an optional interface for docker-specific export/import operations.
// Not all sandbox modes support export/import (e.g., remote, none return no-op).
type SandboxExporter interface {
	IsExporting(userID string) bool
	ExportAndImport(userID string) error
}
