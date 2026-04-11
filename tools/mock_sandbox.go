package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// MockSandbox is an in-memory Sandbox implementation for testing.
type MockSandbox struct {
	Files        map[string][]byte // path → content
	Dirs         map[string]bool   // path → exists
	ExecFunc     func(ctx context.Context, spec ExecSpec) (*ExecResult, error)
	NameVal      string
	WorkspaceVal string

	mu sync.RWMutex
}

// NewMockSandbox creates a new MockSandbox with empty state.
func NewMockSandbox() *MockSandbox {
	return &MockSandbox{
		Files:   make(map[string][]byte),
		Dirs:    make(map[string]bool),
		NameVal: "mock",
	}
}

// parentDirs returns all ancestor directories of path (excluding path itself).
// Uses the fact that filepath.Dir returns the same string when it reaches a root
// (e.g. "/" on Unix, "C:\" on Windows), providing a natural termination condition.
func parentDirs(path string) []string {
	var dirs []string
	for p, prev := filepath.Dir(path), path; p != prev; p, prev = filepath.Dir(p), p {
		dirs = append(dirs, p)
	}
	return dirs
}

// SetDir marks a path as a directory (create intermediate dirs automatically).
func (m *MockSandbox) SetDir(path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Dirs[path] = true
	for _, p := range parentDirs(path) {
		m.Dirs[p] = true
	}
}

func (m *MockSandbox) SetFile(path string, content []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Files[path] = content
	dir := filepath.Dir(path)
	if dir != "." {
		m.Dirs[dir] = true
		for _, p := range parentDirs(path) {
			m.Dirs[p] = true
		}
	}
}

func (m *MockSandbox) Name() string { return m.NameVal }

func (m *MockSandbox) Workspace(userID string) string      { return m.WorkspaceVal }
func (m *MockSandbox) Close() error                        { return nil }
func (m *MockSandbox) CloseForUser(userID string) error    { return nil }
func (m *MockSandbox) IsExporting(userID string) bool      { return false }
func (m *MockSandbox) ExportAndImport(userID string) error { return nil }

func (m *MockSandbox) GetShell(userID string, workspace string) (string, error) {
	return "/bin/bash", nil
}

func (m *MockSandbox) Exec(ctx context.Context, spec ExecSpec) (*ExecResult, error) {
	if m.ExecFunc != nil {
		return m.ExecFunc(ctx, spec)
	}
	return &ExecResult{ExitCode: 127, Stderr: "mock: no exec function set"}, nil
}

func (m *MockSandbox) ReadFile(ctx context.Context, path string, userID string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	data, ok := m.Files[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	result := make([]byte, len(data))
	copy(result, data)
	return result, nil
}

func (m *MockSandbox) WriteFile(ctx context.Context, path string, data []byte, perm os.FileMode, userID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]byte, len(data))
	copy(result, data)
	m.Files[path] = result
	return nil
}

func (m *MockSandbox) Stat(ctx context.Context, path string, userID string) (*SandboxFileInfo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Check if it's a directory
	if m.Dirs[path] {
		return &SandboxFileInfo{
			Name:  filepath.Base(path),
			Mode:  os.ModeDir | 0o755,
			IsDir: true,
		}, nil
	}

	// Check if it's a file
	data, ok := m.Files[path]
	if !ok {
		return nil, os.ErrNotExist
	}

	return &SandboxFileInfo{
		Name: filepath.Base(path),
		Size: int64(len(data)),
		Mode: 0o644,
	}, nil
}

func (m *MockSandbox) ReadDir(ctx context.Context, path string, userID string) ([]DirEntry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if !m.Dirs[path] {
		return nil, os.ErrNotExist
	}

	prefix := path
	if prefix != "/" {
		prefix += "/"
	}

	var entries []DirEntry

	// Collect directories under this path
	for dirPath := range m.Dirs {
		if dirPath == path {
			continue // skip self
		}
		if hasDirPrefix(dirPath, prefix) {
			// Get the immediate child name
			rest := dirPath[len(prefix):]
			if idx := strings.Index(rest, "/"); idx >= 0 {
				name := rest[:idx]
				if !containsEntry(entries, name, true) {
					entries = append(entries, DirEntry{Name: name, IsDir: true})
				}
			} else {
				if !containsEntry(entries, rest, true) {
					entries = append(entries, DirEntry{Name: rest, IsDir: true})
				}
			}
		}
	}

	// Collect files under this path
	for filePath := range m.Files {
		if hasDirPrefix(filePath, prefix) {
			rest := filePath[len(prefix):]
			if idx := strings.Index(rest, "/"); idx >= 0 {
				name := rest[:idx]
				// This is a file in a subdirectory, add subdirectory if not already
				if !containsEntry(entries, name, true) {
					entries = append(entries, DirEntry{Name: name, IsDir: true})
				}
			} else {
				if !containsEntry(entries, rest, false) {
					entries = append(entries, DirEntry{Name: rest, IsDir: false, Size: int64(len(m.Files[filePath]))})
				}
			}
		}
	}

	if entries == nil {
		entries = []DirEntry{}
	}
	return entries, nil
}

func hasDirPrefix(path, prefix string) bool {
	return strings.HasPrefix(path, prefix)
}

func containsEntry(entries []DirEntry, name string, isDir bool) bool {
	for _, e := range entries {
		if e.Name == name && e.IsDir == isDir {
			return true
		}
	}
	return false
}

func (m *MockSandbox) MkdirAll(ctx context.Context, path string, perm os.FileMode, userID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Dirs[path] = true
	for _, p := range parentDirs(path) {
		m.Dirs[p] = true
	}
	return nil
}

func (m *MockSandbox) Remove(ctx context.Context, path string, userID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.Files[path]; ok {
		delete(m.Files, path)
		return nil
	}
	if _, ok := m.Dirs[path]; ok {
		// Check if empty
		prefix := path
		if prefix != "/" {
			prefix += "/"
		}
		for p := range m.Files {
			if strings.HasPrefix(p, prefix) {
				return os.ErrNotExist // not empty, Remove can't remove non-empty dir
			}
		}
		for p := range m.Dirs {
			if p != path && strings.HasPrefix(p, prefix) {
				return os.ErrNotExist
			}
		}
		delete(m.Dirs, path)
		return nil
	}
	return os.ErrNotExist
}

func (m *MockSandbox) RemoveAll(ctx context.Context, path string, userID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	prefix := path
	if prefix != "/" {
		prefix += "/"
	}

	// Remove all files under path
	for p := range m.Files {
		if p == path || strings.HasPrefix(p, prefix) {
			delete(m.Files, p)
		}
	}

	// Remove all dirs under path
	for p := range m.Dirs {
		if p == path || strings.HasPrefix(p, prefix) {
			delete(m.Dirs, p)
		}
	}

	return nil
}

func (m *MockSandbox) DownloadFile(ctx context.Context, url, outputPath, userID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Files[outputPath] = []byte("mock-download")
	m.Dirs[filepath.Dir(outputPath)] = true
	return nil
}

// Wrap is a legacy method for compatibility.
func (m *MockSandbox) Wrap(command string, args []string, env []string, workspace string, userID string) (string, []string, error) {
	return command, args, nil
}
