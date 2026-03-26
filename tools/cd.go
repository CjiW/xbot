package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"xbot/llm"

	log "xbot/logger"
)

// CdTool changes the agent's working directory (persisted across tool calls).
type CdTool struct{}

func (t *CdTool) Name() string {
	return "Cd"
}

func (t *CdTool) Description() string {
	return `Change the current working directory. The new directory persists across subsequent tool calls (Shell, Read, Glob, Grep, etc.).
Parameters (JSON):
  - path: string, the directory to change to (relative or absolute)
Example: {"path": "src/components"}`
}

func (t *CdTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "path", Type: "string", Description: "The directory to change to", Required: true},
	}
}

func (t *CdTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	params, err := parseToolArgs[struct {
		Path string `json:"path"`
	}](input)
	if err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	if params.Path == "" {
		return nil, fmt.Errorf("path is required")
	}

	// V4: use Sandbox API for directory operations (works for docker + remote)
	if shouldUseSandbox(ctx) {
		return t.executeWithSandboxAPI(ctx, params.Path)
	}
	// Legacy: docker sandbox mode using shell commands
	if ctx != nil && ctx.SandboxEnabled && ctx.WorkspaceRoot != "" {
		return t.executeInSandbox(ctx, params.Path)
	}

	return t.executeLocal(ctx, params.Path)
}

// projectMarker defines a file that indicates a project type.
type projectMarker struct {
	file string
	typ  string
	desc string
}

// knownMarkers lists project marker files and their types.
var knownMarkers = []projectMarker{
	// Go
	{file: "go.mod", typ: "Go", desc: "Go module"},
	{file: "go.sum", typ: "Go", desc: "Go module dependencies"},
	// Node.js / TypeScript
	{file: "package.json", typ: "Node.js", desc: "Node.js / JavaScript project"},
	{file: "tsconfig.json", typ: "TypeScript", desc: "TypeScript project"},
	{file: "pnpm-lock.yaml", typ: "Node.js", desc: "pnpm lock file"},
	{file: "yarn.lock", typ: "Node.js", desc: "yarn lock file"},
	{file: "package-lock.json", typ: "Node.js", desc: "npm lock file"},
	// Rust
	{file: "Cargo.toml", typ: "Rust", desc: "Rust project"},
	// Python
	{file: "pyproject.toml", typ: "Python", desc: "Python project (PEP 517)"},
	{file: "setup.py", typ: "Python", desc: "Python project (setuptools)"},
	{file: "requirements.txt", typ: "Python", desc: "Python dependencies"},
	{file: "Pipfile", typ: "Python", desc: "Python project (Pipenv)"},
	// Java / JVM
	{file: "pom.xml", typ: "Java/Maven", desc: "Maven project"},
	{file: "build.gradle", typ: "Java/Gradle", desc: "Gradle project"},
	{file: "build.gradle.kts", typ: "Java/Gradle", desc: "Gradle project (Kotlin DSL)"},
	// Ruby
	{file: "Gemfile", typ: "Ruby", desc: "Ruby project (Bundler)"},
	// PHP
	{file: "composer.json", typ: "PHP", desc: "PHP project (Composer)"},
	// C/C++
	{file: "CMakeLists.txt", typ: "C/C++", desc: "CMake project"},
	{file: "Makefile", typ: "C/C++", desc: "Make project"},
	// Version control
	{file: ".gitignore", typ: "Git", desc: "Git repository"},
	{file: ".git/HEAD", typ: "Git", desc: "Git repository"},
	// Docker
	{file: "Dockerfile", typ: "Docker", desc: "Docker image"},
	{file: "docker-compose.yml", typ: "Docker", desc: "Docker Compose"},
	{file: "docker-compose.yaml", typ: "Docker", desc: "Docker Compose"},
	// CI/CD
	{file: ".github/workflows", typ: "CI/CD", desc: "GitHub Actions"},
	{file: ".gitlab-ci.yml", typ: "CI/CD", desc: "GitLab CI"},
	{file: "Jenkinsfile", typ: "CI/CD", desc: "Jenkins"},
	// Misc project markers
	{file: "README.md", typ: "", desc: "Project README"},
	{file: "LICENSE", typ: "", desc: "License file"},
	{file: ".editorconfig", typ: "", desc: "Editor config"},
	{file: ".env", typ: "", desc: "Environment file"},
	{file: ".env.example", typ: "", desc: "Environment example"},
	{file: ".env.local", typ: "", desc: "Local environment"},
	// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
	{file: ".xbot/", typ: "xbot", desc: "xbot project config"},
}

// detectProjectContext detects project type and key files in the given directory.
// Returns a formatted string with project info, or empty string if no project detected.
func detectProjectContext(dir string) string {
	var foundMarkers []projectMarker
	var projectTypes []string
	seen := make(map[string]bool)

	for _, m := range knownMarkers {
		_, err := os.Stat(filepath.Join(dir, m.file))
		if err == nil {
			foundMarkers = append(foundMarkers, m)
			if m.typ != "" && !seen[m.typ] {
				projectTypes = append(projectTypes, m.typ)
				seen[m.typ] = true
			}
		}
	}

	if len(foundMarkers) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("📁 Project context:\n")

	if len(projectTypes) > 0 {
		fmt.Fprintf(&sb, "   Type: %s\n", strings.Join(projectTypes, " / "))
	}

	sb.WriteString("   Files: ")
	var files []string
	for _, m := range foundMarkers {
		files = append(files, m.file)
	}
	sort.Strings(files)
	seenFiles := make(map[string]bool)
	var uniqueFiles []string
	for _, f := range files {
		if !seenFiles[f] {
			seenFiles[f] = true
			uniqueFiles = append(uniqueFiles, f)
		}
	}
	if len(uniqueFiles) > 0 {
		showCount := len(uniqueFiles)
		if showCount > 12 {
			showCount = 12
		}
		sb.WriteString(strings.Join(uniqueFiles[:showCount], ", "))
		if len(uniqueFiles) > 12 {
			fmt.Fprintf(&sb, " +%d more", len(uniqueFiles)-12)
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// sandboxMarkerChecks generates shell check commands for project markers in sandbox mode.
// It mirrors knownMarkers but skips ".xbot/" (a directory in sandbox).
func sandboxMarkerChecks() []string {
	var checks []string
	for _, m := range knownMarkers {
		// Skip .xbot/ — in sandbox it's a directory, not a file marker
		// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
		if m.file == ".xbot/" {
			continue
		}
		checks = append(checks, m.file+":"+m.typ)
	}
	return checks
}

// detectProjectContextInSandbox detects project context by running commands inside the sandbox.
func detectProjectContextInSandbox(ctx *ToolContext, dir string) string {
	markerFiles := sandboxMarkerChecks()

	// Build shell check commands: use test -f for non-directory markers, test -d for directory markers, test -e for .git/HEAD
	var checks []string
	for _, m := range markerFiles {
		parts := strings.SplitN(m, ":", 2)
		fileName := parts[0]
		if fileName == ".git/HEAD" {
			checks = append(checks, fmt.Sprintf("test -f '%s' && echo '%s'", fileName, m))
		} else if strings.Contains(fileName, "/") {
			// Directory markers (e.g., .github/workflows)
			checks = append(checks, fmt.Sprintf("test -d '%s' && echo '%s'", fileName, m))
		} else {
			checks = append(checks, fmt.Sprintf("test -e '%s' && echo '%s'", fileName, m))
		}
	}
	// Also check for .git directory
	checks = append(checks, "test -d .git && echo '.git:Git'")

	cmd := fmt.Sprintf("cd '%s' && (%s)", strings.ReplaceAll(dir, "'", "'\\''"),
		strings.Join(checks, "; "))
	output, err := RunInSandboxWithShell(ctx, cmd)
	if err != nil {
		return ""
	}

	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) == 0 || (len(lines) == 1 && lines[0] == "") {
		return ""
	}

	var foundFiles []string
	var projectTypes []string
	seen := make(map[string]bool)

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		foundFiles = append(foundFiles, parts[0])
		if parts[1] != "" && !seen[parts[1]] {
			projectTypes = append(projectTypes, parts[1])
			seen[parts[1]] = true
		}
	}

	if len(foundFiles) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("📁 Project context:\n")

	if len(projectTypes) > 0 {
		fmt.Fprintf(&sb, "   Type: %s\n", strings.Join(projectTypes, " / "))
	}

	sb.WriteString("   Files: ")
	showCount := len(foundFiles)
	if showCount > 12 {
		showCount = 12
	}
	sb.WriteString(strings.Join(foundFiles[:showCount], ", "))
	if len(foundFiles) > 12 {
		fmt.Fprintf(&sb, " +%d more", len(foundFiles)-12)
	}
	sb.WriteString("\n")

	return sb.String()
}

// buildDirectoryTree builds a compact directory tree (max 30 entries).
func buildDirectoryTree(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}

	type dirEntry struct {
		name  string
		isDir bool
		size  int64
	}

	var items []dirEntry
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") && !isKnownDotFile(name) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		items = append(items, dirEntry{name: name, isDir: e.IsDir(), size: info.Size()})
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].isDir != items[j].isDir {
			return items[i].isDir
		}
		return items[i].name < items[j].name
	})

	maxEntries := 30
	if len(items) > maxEntries {
		items = items[:maxEntries]
	}

	var sb strings.Builder
	sb.WriteString("📂 Directory structure:\n")

	for _, item := range items {
		var prefix, suffix string
		if item.isDir {
			prefix = "   📁 "
			suffix = "/"
		} else {
			prefix = "   📄 "
			if item.size > 0 {
				suffix = fmt.Sprintf(" (%s)", formatSize(item.size))
			}
		}
		fmt.Fprintf(&sb, "%s%s%s\n", prefix, item.name, suffix)
	}

	return sb.String()
}

// buildDirectoryTreeInSandbox builds a directory tree by running commands inside the sandbox.
func buildDirectoryTreeInSandbox(ctx *ToolContext, dir string) string {
	cmd := fmt.Sprintf("cd '%s' && ls -1a --group-directories-first 2>/dev/null || ls -1a", strings.ReplaceAll(dir, "'", "'\\''"))
	output, err := RunInSandboxWithShell(ctx, cmd)
	if err != nil {
		return ""
	}

	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) == 0 {
		return ""
	}

	var items []string
	seen := make(map[string]bool)
	for _, name := range lines {
		name = strings.TrimSpace(name)
		if name == "" || name == "." || name == ".." {
			continue
		}
		if strings.HasPrefix(name, ".") && !isKnownDotFile(name) {
			continue
		}
		if !seen[name] {
			seen[name] = true
			items = append(items, name)
		}
		if len(items) >= 30 {
			break
		}
	}

	if len(items) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("📂 Directory structure:\n")
	for _, name := range items {
		fmt.Fprintf(&sb, "   %s\n", name)
	}

	return sb.String()
}

// isKnownDotFile returns true for dot files that should be shown in the tree.
// buildDirectoryTreeSandboxAPI builds a directory tree using Sandbox.ReadDir API (V4).
// Used when shouldUseSandbox(ctx) is true — avoids shell commands, works for remote sandbox.
func buildDirectoryTreeSandboxAPI(ctx *ToolContext, dir string) string {
	entries, err := ctx.Sandbox.ReadDir(ctx.Ctx, dir, ctx.OriginUserID)
	if err != nil {
		return ""
	}

	type dirEntry struct {
		name  string
		isDir bool
		size  int64
	}

	var items []dirEntry
	for _, e := range entries {
		name := e.Name
		if strings.HasPrefix(name, ".") && !isKnownDotFile(name) {
			continue
		}
		items = append(items, dirEntry{name: name, isDir: e.IsDir, size: e.Size})
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].isDir != items[j].isDir {
			return items[i].isDir
		}
		return items[i].name < items[j].name
	})

	maxEntries := 30
	if len(items) > maxEntries {
		items = items[:maxEntries]
	}

	var sb strings.Builder
	sb.WriteString("📂 Directory structure:\n")

	for _, item := range items {
		var prefix, suffix string
		if item.isDir {
			prefix = "   📁 "
			suffix = "/"
		} else {
			prefix = "   📄 "
			if item.size > 0 {
				suffix = fmt.Sprintf(" (%s)", formatSize(item.size))
			}
		}
		fmt.Fprintf(&sb, "%s%s%s\n", prefix, item.name, suffix)
	}

	return sb.String()
}

func isKnownDotFile(name string) bool {
	known := map[string]bool{
		// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
		".xbot":          true,
		".git":           true,
		".github":        true,
		".gitlab-ci.yml": true,
		".gitignore":     true,
		".editorconfig":  true,
		".env":           true,
		".env.example":   true,
		".env.local":     true,
	}
	return known[name]
}

// formatSize formats file size in a human-readable way.
func formatSize(bytes int64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
	)
	switch {
	case bytes >= GB:
		return fmt.Sprintf("%.1f GB", float64(bytes)/float64(GB))
	case bytes >= MB:
		return fmt.Sprintf("%.1f MB", float64(bytes)/float64(MB))
	case bytes >= KB:
		return fmt.Sprintf("%.1f KB", float64(bytes)/float64(KB))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

func (t *CdTool) executeLocal(ctx *ToolContext, dir string) (*ToolResult, error) {
	// Resolve relative paths against CurrentDir, then WorkspaceRoot
	target := dir
	if !filepath.IsAbs(target) {
		base := ""
		if ctx != nil && ctx.CurrentDir != "" {
			base = ctx.CurrentDir
		} else if ctx != nil && ctx.WorkspaceRoot != "" {
			base = ctx.WorkspaceRoot
		} else if ctx != nil {
			base = ctx.WorkingDir
		}
		if base != "" {
			target = filepath.Join(base, target)
		}
	}

	target = filepath.Clean(target)

	info, err := os.Stat(target)
	if err != nil {
		return nil, fmt.Errorf("directory not found: %s", dir)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("not a directory: %s", dir)
	}

	// Validate the target is within allowed roots
	if ctx != nil && (ctx.WorkspaceRoot != "" || ctx.WorkingDir != "") {
		root := ctx.WorkspaceRoot
		if root == "" {
			root = ctx.WorkingDir
		}
		realTarget, err := filepath.EvalSymlinks(target)
		if err == nil {
			target = realTarget
		}
		realRoot, _ := filepath.EvalSymlinks(root)
		if realRoot == "" {
			realRoot = root
		}

		allowed := false
		if isWithinRoot(target, realRoot) {
			allowed = true
		}
		if !allowed && ctx.ReadOnlyRoots != nil {
			for _, ro := range ctx.SandboxReadOnlyRoots {
				if ro == "" {
					continue
				}
				realRO, err := filepath.EvalSymlinks(ro)
				if err != nil {
					realRO = ro
				}
				if isWithinRoot(target, realRO) {
					allowed = true
					break
				}
			}
		}
		if !allowed {
			return nil, fmt.Errorf("directory is outside allowed workspace: %s", dir)
		}
	}

	if ctx != nil && ctx.SetCurrentDir != nil {
		ctx.CurrentDir = target
		ctx.SetCurrentDir(target)
	}

	log.WithField("dir", target).Debug("Working directory changed")

	var sb strings.Builder
	fmt.Fprintf(&sb, "Changed directory to %s\n\n", target)
	projectCtx := detectProjectContext(target)
	sb.WriteString(projectCtx)
	sb.WriteString(buildDirectoryTree(target))

	if projectCtx != "" {
		fmt.Fprintf(&sb, "\n💡 提示：如果这是你经常工作的项目，可以用 archival_memory_insert 将项目信息存入知识库（包含路径 %s 和项目特征），下次对话可直接查询。\n", target)
	}

	return NewResult(sb.String()), nil
}

func (t *CdTool) executeInSandbox(ctx *ToolContext, dir string) (*ToolResult, error) {
	// Resolve relative paths against CurrentDir in sandbox.
	// CurrentDir in sandbox mode stores sandbox-internal paths (e.g. /workspace/src).
	target := dir
	if !filepath.IsAbs(target) {
		base := ""
		if ctx.CurrentDir != "" {
			// CurrentDir in sandbox mode stores sandbox paths (e.g. /workspace/src)
			base = ctx.CurrentDir
		} else {
			base = ctx.SandboxWorkDir
		}
		if base != "" {
			target = filepath.Join(base, target)
		}
	}
	target = filepath.Clean(target)

	// Defense-in-depth: verify target is within sandbox workspace or allowed read-only roots.
	// In docker mode the container provides isolation, but this check prevents escapes
	// if the sandbox implementation has bugs or if mode is misconfigured.
	sandboxBase := sandboxBaseDir(ctx)
	if sandboxBase != "" && !isWithinRoot(target, sandboxBase) {
		allowed := false
		for _, ro := range ctx.SandboxReadOnlyRoots {
			if ro == "" {
				continue
			}
			if isWithinRoot(target, ro) {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, fmt.Errorf("directory is outside allowed workspace: %s", dir)
		}
	}

	// Verify directory exists inside the sandbox
	cmd := fmt.Sprintf("test -d '%s' && echo ok", strings.ReplaceAll(target, "'", "'\\''"))
	output, err := RunInSandboxWithShell(ctx, cmd)
	if err != nil || strings.TrimSpace(output) != "ok" {
		return nil, fmt.Errorf("directory not found in sandbox: %s", dir)
	}

	if ctx.SetCurrentDir != nil {
		ctx.CurrentDir = target
		ctx.SetCurrentDir(target)
	}

	log.WithField("dir", target).Debug("Working directory changed")

	projectCtx := detectProjectContextInSandbox(ctx, target)
	dirTree := buildDirectoryTreeInSandbox(ctx, target)

	var sb strings.Builder
	fmt.Fprintf(&sb, "Changed directory to %s\n\n", target)
	sb.WriteString(projectCtx)
	sb.WriteString(dirTree)

	if projectCtx != "" {
		fmt.Fprintf(&sb, "\n💡 提示：如果这是你经常工作的项目，可以用 archival_memory_insert 将项目信息存入知识库（包含路径 %s 和项目特征），下次对话可直接查询。\n", target)
	}

	return NewResult(sb.String()), nil
}

// executeWithSandboxAPI changes directory using Sandbox API (V4 approach).
func (t *CdTool) executeWithSandboxAPI(ctx *ToolContext, dir string) (*ToolResult, error) {
	target := dir
	if !filepath.IsAbs(target) {
		base := ""
		if ctx.CurrentDir != "" {
			base = ctx.CurrentDir
		} else {
			base = ctx.SandboxWorkDir
		}
		if base != "" {
			target = filepath.Join(base, target)
		}
	}
	target = filepath.Clean(target)

	// Verify directory exists in sandbox
	userID := ctx.OriginUserID
	if userID == "" {
		userID = ctx.SenderID
	}
	if _, err := ctx.Sandbox.Stat(ctx.Ctx, target, userID); err != nil {
		return nil, fmt.Errorf("directory not found: %s", dir)
	}

	// Validate target is within sandbox workspace or allowed roots
	sandboxBase := ctx.SandboxWorkDir
	if sandboxBase != "" && !isWithinRoot(target, sandboxBase) {
		allowed := false
		for _, ro := range ctx.SandboxReadOnlyRoots {
			if ro == "" {
				continue
			}
			if isWithinRoot(target, ro) {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, fmt.Errorf("directory is outside allowed workspace: %s", dir)
		}
	}

	if ctx.SetCurrentDir != nil {
		ctx.CurrentDir = target
		ctx.SetCurrentDir(target)
	}

	log.WithField("dir", target).Debug("Working directory changed (Sandbox API)")

	var sb strings.Builder
	fmt.Fprintf(&sb, "Changed directory to %s\n\n", target)

	// Project detection using Sandbox
	projectCtx := detectProjectContextInSandbox(ctx, target)
	sb.WriteString(projectCtx)
	sb.WriteString(buildDirectoryTreeSandboxAPI(ctx, target))

	if projectCtx != "" {
		fmt.Fprintf(&sb, "\n💡 提示：如果这是你经常工作的项目，可以用 archival_memory_insert 将项目信息存入知识库（包含路径 %s 和项目特征），下次对话可直接查询。\n", target)
	}

	return NewResult(sb.String()), nil
}
