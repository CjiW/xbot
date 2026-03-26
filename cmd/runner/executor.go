package main

import (
	"context"
	"os"
	"time"
)

// Executor 抽象 runner 的操作后端（native 或 docker）。
type Executor interface {
	// 命令执行
	Exec(ctx context.Context, spec ExecSpec) (*ExecResult, error)

	// 文件操作
	ReadFile(path string) ([]byte, error)
	WriteFile(path string, data []byte, perm os.FileMode) error
	Stat(path string) (FileInfo, error)
	ReadDir(path string) ([]DirEntry, error)
	MkdirAll(path string, perm os.FileMode) error
	Remove(path string) error
	RemoveAll(path string) error

	// 生命周期
	Close() error
}

// ExecSpec 是命令执行参数。
type ExecSpec struct {
	Command string
	Args    []string
	Shell   bool
	Dir     string
	Env     []string
	Stdin   string
	Timeout time.Duration
}

// ExecResult 是命令执行结果。
type ExecResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
	TimedOut bool
}

// FileInfo 是文件元信息（对标服务端 SandboxFileInfo）。
type FileInfo struct {
	Name    string
	Size    int64
	Mode    os.FileMode
	ModTime time.Time
	IsDir   bool
}

// DirEntry 是目录条目。
type DirEntry struct {
	Name  string
	IsDir bool
	Size  int64
}
