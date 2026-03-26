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
}

// ExecResult holds the result of a sandbox command execution.
type ExecResult struct {
	Stdout   string // standard output
	Stderr   string // standard error
	ExitCode int    // exit code (-1 if timed out)
	TimedOut bool   // whether execution timed out
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

	// === Shell Configuration ===
	// GetShell returns the preferred shell command for the user/workspace.
	GetShell(userID string, workspace string) (string, error)

	// === Lifecycle ===
	Name() string
	Close() error
	CloseForUser(userID string) error

	// === Export/Import (docker-specific) ===
	IsExporting(userID string) bool
	ExportAndImport(userID string) error

	// === Command Wrapping (used by ShellTool, MCP, sandbox_exec) ===
	// Wrap 将命令包装到沙箱执行，返回可直接用于 exec.Command 的 command 与 args
	// env 参数指定要传递到沙箱的环境变量（格式：KEY=VALUE）
	// TODO: migrate all Wrap call sites to use Exec, then remove this method
	Wrap(command string, args []string, env []string, workspace string, userID string) (string, []string, error)
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
