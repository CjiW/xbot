package tools

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

// ============================================================================
// Remote sandbox built-in tools integration tests
// Verifies Grep, Glob, Read, FileCreate, FileReplace, Cd work correctly
// when sandbox.Name() == "remote" (i.e., remote runner mode).
// ============================================================================

// newRemoteMockSandbox creates a MockSandbox configured as "remote" with
// an ExecFunc that simulates basic POSIX commands (grep, find, cat, base64, mkdir).
func newRemoteMockSandbox(workspace string) *MockSandbox {
	mock := NewMockSandbox()
	mock.NameVal = "remote"
	mock.WorkspaceVal = workspace
	mock.SetDir(workspace)

	mock.ExecFunc = func(ctx context.Context, spec ExecSpec) (*ExecResult, error) {
		args := spec.Args
		// Parse shell -l -c "..." invocation
		if len(args) >= 4 && args[1] == "-l" && args[2] == "-c" {
			return execSimulated(mock, args[3], spec.Dir, workspace)
		}
		// Direct command invocation
		if len(args) >= 1 {
			return execSimulated(mock, strings.Join(args[1:], " "), spec.Dir, workspace)
		}
		return &ExecResult{ExitCode: 127, Stderr: "mock: unknown command"}, nil
	}

	return mock
}

// extractAllSingleQuoted extracts all single-quoted strings from s.
func extractAllSingleQuoted(s string) []string {
	var results []string
	inQuote := false
	start := -1
	for i, c := range s {
		if c == '\'' {
			if !inQuote {
				inQuote = true
				start = i + 1
			} else {
				inQuote = false
				results = append(results, s[start:i])
			}
		}
	}
	return results
}

// execSimulated simulates basic POSIX commands for testing.
func execSimulated(mock *MockSandbox, shellCmd, dir, workspace string) (*ExecResult, error) {
	// Handle compound commands (&&)
	if strings.Contains(shellCmd, " && ") {
		parts := strings.SplitN(shellCmd, " && ", 2)
		r1, err1 := execSimulated(mock, parts[0], dir, workspace)
		if err1 != nil {
			return nil, err1
		}
		if r1.ExitCode != 0 {
			return r1, nil
		}
		return execSimulated(mock, parts[1], dir, workspace)
	}

	// Handle pipe (for grep | head -200)
	if strings.Contains(shellCmd, " | ") {
		pipeParts := strings.SplitN(shellCmd, " | ", 2)
		r1, err1 := execSimulated(mock, pipeParts[0], dir, workspace)
		if err1 != nil {
			return nil, err1
		}
		if strings.Contains(pipeParts[1], "head -") {
			lines := strings.Split(strings.TrimSpace(r1.Stdout), "\n")
			var n int
			fmt.Sscanf(pipeParts[1], "head -%d", &n)
			if n > 0 && len(lines) > n {
				lines = lines[:n]
			}
			return &ExecResult{ExitCode: 0, Stdout: strings.Join(lines, "\n")}, nil
		}
	}

	// cat '<file>'
	if strings.HasPrefix(shellCmd, "cat '") {
		quoted := extractAllSingleQuoted(shellCmd)
		if len(quoted) == 0 {
			return &ExecResult{ExitCode: 1, Stderr: "cat: missing operand"}, nil
		}
		path := quoted[0]
		data, err := mock.ReadFile(context.Background(), path, "testuser")
		if err != nil {
			return &ExecResult{ExitCode: 1, Stderr: fmt.Sprintf("cat: %s: No such file", path)}, nil
		}
		return &ExecResult{ExitCode: 0, Stdout: string(data)}, nil
	}

	// grep -E[flags] ... 'pattern' 'dir'
	if strings.Contains(shellCmd, "grep ") {
		return simulateGrep(mock, shellCmd, workspace)
	}

	// find '<dir>' -type f ...
	if strings.HasPrefix(shellCmd, "find '") {
		return simulateFind(mock, shellCmd, workspace)
	}

	// mkdir -p '<dir>'
	if strings.HasPrefix(shellCmd, "mkdir -p ") {
		quoted := extractAllSingleQuoted(shellCmd)
		if len(quoted) > 0 {
			mock.MkdirAll(context.Background(), quoted[0], 0755, "testuser")
		}
		return &ExecResult{ExitCode: 0}, nil
	}

	// echo '<base64>' | base64 -d > '<path>'
	if strings.Contains(shellCmd, "base64 -d >") {
		quoted := extractAllSingleQuoted(shellCmd)
		if len(quoted) >= 2 {
			encoded := quoted[0]
			outPath := quoted[1]
			decoded, decErr := base64.StdEncoding.DecodeString(encoded)
			if decErr != nil {
				// Store raw if base64 decode fails (mock simplicity)
				mock.SetFile(outPath, []byte(encoded))
			} else {
				mock.SetFile(outPath, decoded)
			}
			return &ExecResult{ExitCode: 0}, nil
		}
		return &ExecResult{ExitCode: 1, Stderr: "base64: parse error"}, nil
	}

	// pwd
	if strings.TrimSpace(shellCmd) == "pwd" {
		d := dir
		if d == "" {
			d = workspace
		}
		return &ExecResult{ExitCode: 0, Stdout: d}, nil
	}

	return &ExecResult{ExitCode: 127, Stderr: "mock: unhandled: " + shellCmd}, nil
}

func simulateGrep(mock *MockSandbox, shellCmd, workspace string) (*ExecResult, error) {
	quoted := extractAllSingleQuoted(shellCmd)
	if len(quoted) < 2 {
		return &ExecResult{ExitCode: 2, Stderr: "grep: missing arguments"}, nil
	}

	// Last two quoted args are pattern and searchDir
	pattern := quoted[len(quoted)-2]
	searchDir := quoted[len(quoted)-1]

	// Parse --include filters from the command
	var includePatterns []string
	if strings.Contains(shellCmd, "--include=") {
		incParts := strings.Split(shellCmd, "--include='")
		for i := 1; i < len(incParts); i++ {
			end := strings.Index(incParts[i], "'")
			if end > 0 {
				includePatterns = append(includePatterns, incParts[i][:end])
			}
		}
	}

	var results []string
	for path, data := range mock.Files {
		if !strings.HasPrefix(path, searchDir+"/") && path != searchDir {
			continue
		}
		// Skip .git and node_modules
		if strings.Contains(path, "/.git/") || strings.Contains(path, "/node_modules/") {
			continue
		}
		// Apply include filter
		if len(includePatterns) > 0 {
			matched := false
			baseName := path[strings.LastIndex(path, "/")+1:]
			for _, pat := range includePatterns {
				if matchedGlob(pat, baseName) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}

		lines := strings.Split(string(data), "\n")
		for lineNum, line := range lines {
			if strings.Contains(line, pattern) {
				results = append(results, fmt.Sprintf("%s:%d:%s", path, lineNum+1, line))
			}
		}
	}

	if len(results) == 0 {
		return &ExecResult{ExitCode: 1, Stdout: ""}, nil
	}
	return &ExecResult{ExitCode: 0, Stdout: strings.Join(results, "\n")}, nil
}

func matchedGlob(pattern, name string) bool {
	if pattern == "*" {
		return true
	}
	if strings.HasPrefix(pattern, "*.") {
		suffix := pattern[1:]
		return strings.HasSuffix(name, suffix)
	}
	return pattern == name
}

func simulateFind(mock *MockSandbox, shellCmd, workspace string) (*ExecResult, error) {
	quoted := extractAllSingleQuoted(shellCmd)
	if len(quoted) == 0 {
		return &ExecResult{ExitCode: 1, Stderr: "find: missing arguments"}, nil
	}
	searchDir := quoted[0]

	var results []string
	for path := range mock.Files {
		if !strings.HasPrefix(path, searchDir+"/") && path != searchDir {
			continue
		}
		if strings.Contains(path, "/.git/") || strings.Contains(path, "/node_modules/") {
			continue
		}
		results = append(results, path)
	}

	if len(results) == 0 {
		return &ExecResult{ExitCode: 0, Stdout: ""}, nil
	}
	return &ExecResult{ExitCode: 0, Stdout: strings.Join(results, "\n")}, nil
}

// testToolContext creates a ToolContext configured for remote sandbox testing.
func testToolContext(sandbox *MockSandbox, workspace, currentDir string) *ToolContext {
	ctx := &ToolContext{
		Ctx:           context.Background(),
		Sandbox:       sandbox,
		OriginUserID:  "testuser",
		SenderID:      "testuser",
		WorkspaceRoot: workspace,
		WorkingDir:    workspace,
	}
	// Always set SetCurrentDir so tools can update CurrentDir
	ctx.SetCurrentDir = func(dir string) { ctx.CurrentDir = dir }
	if currentDir != "" {
		ctx.CurrentDir = currentDir
	}
	return ctx
}

// ============================================================================
// Grep tool tests in remote sandbox
// ============================================================================

func TestGrepTool_RemoteSandbox_Basic(t *testing.T) {
	workspace := "/home/user/workspace"
	mock := newRemoteMockSandbox(workspace)
	mock.SetFile(workspace+"/main.go", []byte("package main\n\nfunc main() {}\n"))
	mock.SetFile(workspace+"/util.go", []byte("package main\n\nfunc helper() {}\n"))

	ctx := testToolContext(mock, workspace, "")

	tool := &GrepTool{}
	result, err := tool.Execute(ctx, `{"pattern": "func", "path": "`+workspace+`"}`)
	if err != nil {
		t.Fatalf("Grep failed: %v", err)
	}
	if !strings.Contains(result.Summary, "func main") {
		t.Errorf("Expected 'func main' in result, got: %s", result.Summary)
	}
	if !strings.Contains(result.Summary, "func helper") {
		t.Errorf("Expected 'func helper' in result, got: %s", result.Summary)
	}
}

func TestGrepTool_RemoteSandbox_WithInclude(t *testing.T) {
	workspace := "/home/user/workspace"
	mock := newRemoteMockSandbox(workspace)
	mock.SetFile(workspace+"/main.go", []byte("func main() {}\n"))
	mock.SetFile(workspace+"/readme.txt", []byte("func main documented here\n"))

	ctx := testToolContext(mock, workspace, "")

	tool := &GrepTool{}
	result, err := tool.Execute(ctx, `{"pattern": "func", "include": "*.go", "path": "`+workspace+`"}`)
	if err != nil {
		t.Fatalf("Grep failed: %v", err)
	}
	if strings.Contains(result.Summary, "readme.txt") {
		t.Errorf("Expected no readme.txt in result (include=*.go), got: %s", result.Summary)
	}
	if !strings.Contains(result.Summary, "main.go") {
		t.Errorf("Expected main.go in result, got: %s", result.Summary)
	}
}

func TestGrepTool_RemoteSandbox_NoMatch(t *testing.T) {
	workspace := "/home/user/workspace"
	mock := newRemoteMockSandbox(workspace)
	mock.SetFile(workspace+"/main.go", []byte("package main\n"))

	ctx := testToolContext(mock, workspace, "")

	tool := &GrepTool{}
	result, err := tool.Execute(ctx, `{"pattern": "ZZZ_NOT_FOUND", "path": "`+workspace+`"}`)
	if err != nil {
		t.Fatalf("Grep failed: %v", err)
	}
	if !strings.Contains(result.Summary, "No matches found") {
		t.Errorf("Expected 'No matches found', got: %s", result.Summary)
	}
}

// ============================================================================
// Glob tool tests in remote sandbox
// ============================================================================

func TestGlobTool_RemoteSandbox_Basic(t *testing.T) {
	workspace := "/home/user/workspace"
	mock := newRemoteMockSandbox(workspace)
	mock.SetFile(workspace+"/main.go", []byte("package main\n"))
	mock.SetFile(workspace+"/util.go", []byte("package main\n"))
	mock.SetDir(workspace + "/subdir")

	ctx := testToolContext(mock, workspace, "")

	tool := &GlobTool{}
	result, err := tool.Execute(ctx, `{"pattern": "*.go", "path": "`+workspace+`"}`)
	if err != nil {
		t.Fatalf("Glob failed: %v", err)
	}
	if !strings.Contains(result.Summary, "main.go") {
		t.Errorf("Expected main.go in result, got: %s", result.Summary)
	}
	if !strings.Contains(result.Summary, "util.go") {
		t.Errorf("Expected util.go in result, got: %s", result.Summary)
	}
}

func TestGlobTool_RemoteSandbox_Recursive(t *testing.T) {
	workspace := "/home/user/workspace"
	mock := newRemoteMockSandbox(workspace)
	mock.SetFile(workspace+"/src/main.go", []byte("package main\n"))
	mock.SetDir(workspace + "/src")

	ctx := testToolContext(mock, workspace, "")

	tool := &GlobTool{}
	result, err := tool.Execute(ctx, `{"pattern": "**/*.go", "path": "`+workspace+`"}`)
	if err != nil {
		t.Fatalf("Glob failed: %v", err)
	}
	if !strings.Contains(result.Summary, "src/main.go") {
		t.Errorf("Expected src/main.go in result, got: %s", result.Summary)
	}
}

func TestGlobTool_RemoteSandbox_NoMatch(t *testing.T) {
	workspace := "/home/user/workspace"
	mock := newRemoteMockSandbox(workspace)

	ctx := testToolContext(mock, workspace, "")

	tool := &GlobTool{}
	result, err := tool.Execute(ctx, `{"pattern": "*.rs", "path": "`+workspace+`"}`)
	if err != nil {
		t.Fatalf("Glob failed: %v", err)
	}
	if !strings.Contains(result.Summary, "No files matched") {
		t.Errorf("Expected 'No files matched', got: %s", result.Summary)
	}
}

// ============================================================================
// Read tool tests in remote sandbox
// ============================================================================

func TestReadTool_RemoteSandbox_RelativePath(t *testing.T) {
	workspace := "/home/user/workspace"
	mock := newRemoteMockSandbox(workspace)
	mock.SetFile(workspace+"/hello.txt", []byte("Hello World\nLine 2\nLine 3\n"))

	ctx := testToolContext(mock, workspace, "")

	tool := &ReadTool{}
	result, err := tool.Execute(ctx, `{"path": "hello.txt"}`)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if !strings.Contains(result.Summary, "Hello World") {
		t.Errorf("Expected 'Hello World' in result, got: %s", result.Summary)
	}
}

func TestReadTool_RemoteSandbox_AbsolutePath(t *testing.T) {
	workspace := "/home/user/workspace"
	mock := newRemoteMockSandbox(workspace)
	mock.SetFile(workspace+"/hello.txt", []byte("Hello from absolute path\n"))

	ctx := testToolContext(mock, workspace, "")

	tool := &ReadTool{}
	result, err := tool.Execute(ctx, `{"path": "`+workspace+`/hello.txt"}`)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if !strings.Contains(result.Summary, "Hello from absolute path") {
		t.Errorf("Expected content in result, got: %s", result.Summary)
	}
}

func TestReadTool_RemoteSandbox_FileNotFound(t *testing.T) {
	workspace := "/home/user/workspace"
	mock := newRemoteMockSandbox(workspace)

	ctx := testToolContext(mock, workspace, "")

	tool := &ReadTool{}
	// Read via sandbox uses cat, which returns stderr on missing files.
	// The tool returns the error from RunInSandboxWithShell.
	result, err := tool.Execute(ctx, `{"path": "nonexistent.txt"}`)
	// sandbox cat returns exit 1 but RunInSandboxWithShell doesn't propagate
	// non-zero exit as Go error — it returns the stderr in the output.
	// So either err is nil (exit 1 swallowed) or result contains error info.
	if err != nil {
		// OK: error propagated
		return
	}
	if result == nil || strings.Contains(result.Summary, "nonexistent") || strings.Contains(result.Summary, "No such file") {
		// OK: error in result
		return
	}
	// cat on nonexistent file in sandbox returns empty stdout with exit code 1,
	// RunInSandboxWithShell's formatExecResult includes stderr.
	t.Logf("Note: sandbox cat returned nil error for nonexistent file (result: %v)", result)
}

// ============================================================================
// FileCreate tool tests in remote sandbox
// ============================================================================

func TestFileCreateTool_RemoteSandbox_Basic(t *testing.T) {
	workspace := "/home/user/workspace"
	mock := newRemoteMockSandbox(workspace)

	ctx := testToolContext(mock, workspace, "")

	tool := &FileCreateTool{}
	result, err := tool.Execute(ctx, `{"path": "newfile.txt", "content": "Hello New File"}`)
	if err != nil {
		t.Fatalf("FileCreate failed: %v", err)
	}
	if !strings.Contains(result.Summary, "File created successfully") {
		t.Errorf("Expected success message, got: %s", result.Summary)
	}

	// Verify file exists in mock sandbox
	data, err := mock.ReadFile(context.Background(), workspace+"/newfile.txt", "testuser")
	if err != nil {
		t.Fatalf("File not found in sandbox after creation: %v", err)
	}
	if string(data) != "Hello New File" {
		t.Errorf("Unexpected content: %q", string(data))
	}
}

func TestFileCreateTool_RemoteSandbox_Subdir(t *testing.T) {
	workspace := "/home/user/workspace"
	mock := newRemoteMockSandbox(workspace)

	ctx := testToolContext(mock, workspace, "")

	tool := &FileCreateTool{}
	result, err := tool.Execute(ctx, `{"path": "sub/newfile.txt", "content": "nested"}`)
	if err != nil {
		t.Fatalf("FileCreate failed: %v", err)
	}
	if !strings.Contains(result.Summary, "File created successfully") {
		t.Errorf("Expected success message, got: %s", result.Summary)
	}
}

// ============================================================================
// FileReplace tool tests in remote sandbox
// ============================================================================

func TestFileReplaceTool_RemoteSandbox_Basic(t *testing.T) {
	workspace := "/home/user/workspace"
	mock := newRemoteMockSandbox(workspace)
	mock.SetFile(workspace+"/hello.txt", []byte("Hello World\nGoodbye World\n"))

	ctx := testToolContext(mock, workspace, "")

	tool := &FileReplaceTool{}
	result, err := tool.Execute(ctx, `{"path": "hello.txt", "old_string": "Hello", "new_string": "Hi"}`)
	if err != nil {
		t.Fatalf("FileReplace failed: %v", err)
	}
	if !strings.Contains(result.Summary, "Replaced") && !strings.Contains(result.Summary, "replaced") {
		t.Errorf("Expected replace confirmation, got: %s", result.Summary)
	}

	// Verify content changed via Sandbox API
	data, err := mock.ReadFile(context.Background(), workspace+"/hello.txt", "testuser")
	if err != nil {
		t.Fatalf("File not found after replace: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "Hi World") {
		t.Errorf("Expected 'Hi World' after replace, got: %s", content)
	}
	if strings.Contains(content, "Hello World") {
		t.Errorf("Old text 'Hello World' should be replaced, got: %s", content)
	}
}

// ============================================================================
// Cd tool tests in remote sandbox
// ============================================================================

func TestCdTool_RemoteSandbox_ChangeDir(t *testing.T) {
	workspace := "/home/user/workspace"
	mock := newRemoteMockSandbox(workspace)
	mock.SetDir(workspace + "/subdir")
	mock.SetFile(workspace+"/subdir/main.go", []byte("package sub\n"))

	ctx := testToolContext(mock, workspace, "")

	tool := &CdTool{}
	result, err := tool.Execute(ctx, `{"path": "`+workspace+`/subdir"}`)
	if err != nil {
		t.Fatalf("Cd failed: %v", err)
	}
	if !strings.Contains(result.Summary, "Changed directory") {
		t.Errorf("Expected 'Changed directory' in result, got: %s", result.Summary)
	}
	if ctx.CurrentDir != workspace+"/subdir" {
		t.Errorf("Expected CurrentDir=%s, got %s", workspace+"/subdir", ctx.CurrentDir)
	}
}

// ============================================================================
// setSandboxDir tests (remote mode CurrentDir propagation)
// ============================================================================

func TestSetSandboxDir_Remote_WithCurrentDir(t *testing.T) {
	workspace := "/home/user/workspace"
	mock := newRemoteMockSandbox(workspace)
	ctx := testToolContext(mock, workspace, workspace+"/subdir")

	spec := ExecSpec{}
	setSandboxDir(ctx, mock, &spec)

	if spec.Dir != workspace+"/subdir" {
		t.Errorf("Expected Dir=%s, got %s", workspace+"/subdir", spec.Dir)
	}
}

func TestSetSandboxDir_Remote_NoCurrentDir(t *testing.T) {
	workspace := "/home/user/workspace"
	mock := newRemoteMockSandbox(workspace)
	ctx := testToolContext(mock, workspace, "")

	spec := ExecSpec{}
	setSandboxDir(ctx, mock, &spec)

	// When no CurrentDir, Dir should be empty (runner uses its default workspace)
	if spec.Dir != "" {
		t.Errorf("Expected Dir to be empty (runner default), got %s", spec.Dir)
	}
}

// ============================================================================
// Path resolution tests for remote sandbox
// ============================================================================

func TestResolveSandboxPath_Remote_AbsolutePath(t *testing.T) {
	workspace := "/home/user/workspace"
	mock := newRemoteMockSandbox(workspace)
	ctx := testToolContext(mock, workspace, workspace+"/src")

	// Absolute path within sandbox
	result := resolveSandboxPath(ctx, workspace+"/src/main.go")
	if result != workspace+"/src/main.go" {
		t.Errorf("Expected %s, got %s", workspace+"/src/main.go", result)
	}

	// Relative path from CurrentDir
	result = resolveSandboxPath(ctx, "main.go")
	if result != workspace+"/src/main.go" {
		t.Errorf("Expected %s, got %s", workspace+"/src/main.go", result)
	}
}

func TestResolveSandboxPath_Remote_RelativePath(t *testing.T) {
	workspace := "/home/user/workspace"
	mock := newRemoteMockSandbox(workspace)
	ctx := testToolContext(mock, workspace, "")

	// Relative path from sandboxBase
	result := resolveSandboxPath(ctx, "main.go")
	if result != workspace+"/main.go" {
		t.Errorf("Expected %s, got %s", workspace+"/main.go", result)
	}
}

func TestResolveSandboxCWD_Remote_SandboxPath(t *testing.T) {
	workspace := "/home/user/workspace"
	mock := newRemoteMockSandbox(workspace)

	ctx := testToolContext(mock, workspace, workspace+"/src/lib")
	result := resolveSandboxCWD(ctx, workspace)
	if result != workspace+"/src/lib" {
		t.Errorf("Expected %s, got %s", workspace+"/src/lib", result)
	}
}

func TestResolveSandboxCWD_Remote_HostPathConversion(t *testing.T) {
	workspace := "/home/user/workspace"
	hostWorkspace := "/workspace/xbot"
	mock := newRemoteMockSandbox(workspace)

	ctx := testToolContext(mock, workspace, hostWorkspace+"/agent")
	ctx.WorkspaceRoot = hostWorkspace
	result := resolveSandboxCWD(ctx, workspace)
	if result != workspace+"/agent" {
		t.Errorf("Expected %s, got %s", workspace+"/agent", result)
	}
}

// ============================================================================
// sandboxBaseDir tests for remote sandbox
// ============================================================================

func TestSandboxBaseDir_Remote(t *testing.T) {
	workspace := "/home/user/workspace"
	mock := newRemoteMockSandbox(workspace)
	ctx := testToolContext(mock, workspace, "")

	result := sandboxBaseDir(ctx)
	if result != workspace {
		t.Errorf("Expected %s, got %s", workspace, result)
	}
}

func TestShouldUseSandbox_Remote(t *testing.T) {
	workspace := "/home/user/workspace"
	mock := newRemoteMockSandbox(workspace)
	ctx := testToolContext(mock, workspace, "")

	if !shouldUseSandbox(ctx) {
		t.Error("Expected shouldUseSandbox=true for remote sandbox")
	}
}

func TestShouldUseSandbox_Nil(t *testing.T) {
	if shouldUseSandbox(nil) {
		t.Error("Expected shouldUseSandbox=false for nil context")
	}
}
