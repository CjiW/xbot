package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"xbot/llm"
)

func TestNewOffloadStore_Defaults(t *testing.T) {
	store := NewOffloadStore(OffloadConfig{})
	if store.config.MaxResultTokens != 2000 {
		t.Errorf("expected MaxResultTokens=2000, got %d", store.config.MaxResultTokens)
	}
	if store.config.MaxResultBytes != 10240 {
		t.Errorf("expected MaxResultBytes=10240, got %d", store.config.MaxResultBytes)
	}
	if store.config.CleanupAgeDays != 7 {
		t.Errorf("expected CleanupAgeDays=7, got %d", store.config.CleanupAgeDays)
	}
	if store.config.StoreDir != "offload_store" {
		t.Errorf("expected default StoreDir, got %s", store.config.StoreDir)
	}
}

func TestMaybeOffload_SmallResult(t *testing.T) {
	dir := t.TempDir()
	store := NewOffloadStore(OffloadConfig{
		StoreDir:        dir,
		MaxResultTokens: 2000,
		MaxResultBytes:  10240,
	})

	smallResult := strings.Repeat("hello", 100) // ~500 bytes, well under threshold
	_, wasOffloaded := store.MaybeOffload("test:session", "Read", `{"path":"file.go"}`, smallResult, "", "")
	if wasOffloaded {
		t.Error("small result should not be offloaded")
	}
}

func TestMaybeOffload_LargeResult(t *testing.T) {
	dir := t.TempDir()
	store := NewOffloadStore(OffloadConfig{
		StoreDir:        dir,
		MaxResultTokens: 100, // very low threshold
		MaxResultBytes:  10240,
	})

	largeResult := strings.Repeat("a", 10000)
	offloaded, wasOffloaded := store.MaybeOffload("test:session", "Read", `{"path":"bigfile.go"}`, largeResult, "", "")
	if !wasOffloaded {
		t.Fatal("large result should be offloaded")
	}
	if offloaded.ID == "" {
		t.Error("offloaded ID should not be empty")
	}
	if !strings.HasPrefix(offloaded.ID, "ol_") {
		t.Error("offloaded ID should start with 'ol_'")
	}
	if offloaded.TokenSize <= 0 {
		t.Error("offloaded TokenSize should be positive")
	}
	if !strings.Contains(offloaded.Summary, "📂") {
		t.Error("summary should contain 📂 marker")
	}
	if !strings.Contains(offloaded.Summary, offloaded.ID) {
		t.Error("summary should contain offload ID")
	}
}

func TestMaybeOffload_EmptyResult(t *testing.T) {
	dir := t.TempDir()
	store := NewOffloadStore(OffloadConfig{StoreDir: dir})

	_, wasOffloaded := store.MaybeOffload("test:session", "Read", `{"path":"file.go"}`, "", "", "")
	if wasOffloaded {
		t.Error("empty result should not be offloaded")
	}
}

func TestRecall(t *testing.T) {
	dir := t.TempDir()
	store := NewOffloadStore(OffloadConfig{
		StoreDir:        dir,
		MaxResultTokens: 100,
		MaxResultBytes:  10240,
	})

	originalContent := "this is the original large content: " + strings.Repeat("x", 5000)
	offloaded, wasOffloaded := store.MaybeOffload("test:session", "Shell", `{"command":"ls -la"}`, originalContent, "", "")
	if !wasOffloaded {
		t.Fatal("should be offloaded")
	}

	recalled, err := store.Recall("test:session", offloaded.ID)
	if err != nil {
		t.Fatalf("Recall failed: %v", err)
	}
	if recalled != originalContent {
		t.Errorf("recalled content mismatch: got %d bytes, want %d bytes", len(recalled), len(originalContent))
	}
}

func TestRecall_NotFound(t *testing.T) {
	dir := t.TempDir()
	store := NewOffloadStore(OffloadConfig{StoreDir: dir})

	_, err := store.Recall("test:session", "ol_nonexistent")
	if err == nil {
		t.Error("expected error for non-existent ID")
	}
}

func TestRecall_WrongSession(t *testing.T) {
	dir := t.TempDir()
	store := NewOffloadStore(OffloadConfig{
		StoreDir:        dir,
		MaxResultTokens: 100,
		MaxResultBytes:  10240,
	})

	originalContent := strings.Repeat("y", 5000)
	offloaded, _ := store.MaybeOffload("session1", "Read", `{"path":"a.go"}`, originalContent, "", "")

	// Try to recall from a different session — should fail.
	// Cross-session search was removed for security: no user should be able to
	// read another user's offload data.
	_, err := store.Recall("session2", offloaded.ID)
	if err == nil {
		t.Error("expected error when recalling from wrong session")
	}
}

func TestCleanSession(t *testing.T) {
	dir := t.TempDir()
	store := NewOffloadStore(OffloadConfig{
		StoreDir:        dir,
		MaxResultTokens: 100,
		MaxResultBytes:  10240,
	})

	offloaded, _ := store.MaybeOffload("test:clean", "Read", `{"path":"file.go"}`, strings.Repeat("z", 5000), "", "")
	sessionDir := store.getSessionDir("test:clean")

	// Verify files exist
	if _, err := os.Stat(filepath.Join(sessionDir, offloaded.ID+".json")); os.IsNotExist(err) {
		t.Fatal("offload file should exist before cleanup")
	}

	store.CleanSession("test:clean")

	// Verify files are removed
	if _, err := os.Stat(sessionDir); !os.IsNotExist(err) {
		t.Error("session directory should be removed after cleanup")
	}

	// Verify memory index is cleared
	_, err := store.Recall("test:clean", offloaded.ID)
	if err == nil {
		t.Error("recall should fail after cleanup")
	}
}

func TestCleanStale(t *testing.T) {
	dir := t.TempDir()
	store := NewOffloadStore(OffloadConfig{
		StoreDir:       dir,
		CleanupAgeDays: 1, // 1 day threshold
	})

	// Create a stale session
	sessionDir := store.getSessionDir("stale:session")
	os.MkdirAll(sessionDir, 0o755)
	os.WriteFile(filepath.Join(sessionDir, "dummy.json"), []byte("{}"), 0o644)

	// Set modification time to 2 days ago
	os.Chtimes(sessionDir, time.Now().AddDate(0, 0, -2), time.Now().AddDate(0, 0, -2))

	store.CleanStale()

	if _, err := os.Stat(sessionDir); !os.IsNotExist(err) {
		t.Error("stale directory should be cleaned")
	}
}

func TestCleanStale_NonExistentDir(t *testing.T) {
	dir := t.TempDir()
	store := NewOffloadStore(OffloadConfig{
		StoreDir:       filepath.Join(dir, "nonexistent"),
		CleanupAgeDays: 7,
	})

	// Should not panic
	store.CleanStale()
}

func TestGenerateRuleSummary_Read(t *testing.T) {
	args := `{"path":"main.go"}`
	content := `package main

import "fmt"

func hello() {
	fmt.Println("hello")
}

func world() {
	fmt.Println("world")
}

// many more lines
` + strings.Repeat("line\n", 50)

	summary := generateRuleSummary("Read", args, content)
	if !strings.Contains(summary, "main.go") {
		t.Error("Read summary should contain file path")
	}
	if !strings.Contains(summary, "hello") || !strings.Contains(summary, "world") {
		t.Error("Read summary should contain function names")
	}
}

func TestGenerateRuleSummary_Grep(t *testing.T) {
	content := `file1.go:10: func foo() {
file1.go:20: func bar() {
file2.go:5: func baz() {
file3.go:15: func qux() {
`
	summary := generateRuleSummary("Grep", "", content)
	if !strings.Contains(summary, "4 matches") {
		t.Error("Grep summary should contain match count")
	}
	if !strings.Contains(summary, "file1.go") {
		t.Error("Grep summary should contain file names")
	}
}

func TestGenerateRuleSummary_Shell(t *testing.T) {
	content := strings.Repeat("output line\n", 20) + "exit code: 0"
	summary := generateRuleSummary("Shell", "", content)
	if !strings.Contains(summary, "exit code: 0") {
		t.Error("Shell summary should contain exit code")
	}
	if !strings.Contains(summary, "omitted") {
		t.Error("Shell summary should indicate omitted lines")
	}
}

func TestGenerateRuleSummary_Glob(t *testing.T) {
	content := strings.Join([]string{"file1.go", "file2.go", "file3.go", "file4.go", "file5.go", "file6.go", "file7.go", "file8.go"}, "\n")
	summary := generateRuleSummary("Glob", "", content)
	if !strings.Contains(summary, "8 files matched") {
		t.Error("Glob summary should contain file count")
	}
}

func TestGenerateRuleSummary_Default(t *testing.T) {
	content := strings.Repeat("default content here. ", 100)
	summary := generateRuleSummary("UnknownTool", "", content)
	if !strings.Contains(summary, "Content") {
		t.Error("default summary should contain 'Content'")
	}
	if !strings.Contains(summary, "tokens") {
		t.Error("default summary should contain token estimate")
	}
}

func TestExtractJSONStringField(t *testing.T) {
	tests := []struct {
		jsonStr string
		field   string
		want    string
	}{
		{`{"path": "/tmp/file.go"}`, "path", "/tmp/file.go"},
		{`{"command": "ls -la", "cwd": "/tmp"}`, "command", "ls -la"},
		{`{"key": "value"}`, "nonexistent", ""},
		{`{}`, "key", ""},
	}

	for _, tt := range tests {
		got := extractJSONStringField(tt.jsonStr, tt.field)
		if got != tt.want {
			t.Errorf("extractJSONStringField(%q, %q) = %q, want %q", tt.jsonStr, tt.field, got, tt.want)
		}
	}
}

func TestExtractFunctionNames(t *testing.T) {
	code := `package main

func hello() string {
	return "hello"
}

func (a *Agent) Run(ctx context.Context) error {
	return nil
}

func world(x int, y int) {
}

// not a function
var foo = 42
`
	names := extractFunctionNames(code)
	found := map[string]bool{}
	for _, n := range names {
		found[n] = true
	}

	for _, expected := range []string{"hello", "Run", "world"} {
		if !found[expected] {
			t.Errorf("expected to find function %q in %v", expected, names)
		}
	}
}

func TestOffloadStore_SessionDirSanitization(t *testing.T) {
	store := NewOffloadStore(OffloadConfig{StoreDir: "/tmp/test"})
	dir := store.getSessionDir("cli:user/../../etc")
	// Verify path traversal characters are sanitized (replaced with _)
	if strings.Contains(dir, "/../") || strings.Contains(dir, "\\..\\") {
		t.Errorf("session directory should not contain path traversal sequences: %s", dir)
	}
	// Verify colon is sanitized
	if strings.Contains(dir, ":") {
		t.Errorf("session directory should not contain colon: %s", dir)
	}
	// Verify it's under the store dir
	if !strings.HasPrefix(dir, "/tmp/test") {
		t.Errorf("session directory should be under store dir: %s", dir)
	}
}

func TestOffloadStore_PersistAndLoadIndex(t *testing.T) {
	dir := t.TempDir()
	store := NewOffloadStore(OffloadConfig{
		StoreDir:        dir,
		MaxResultTokens: 100,
		MaxResultBytes:  10240,
	})

	// Create multiple offloads
	offloaded1, _ := store.MaybeOffload("test:index", "Read", `{"path":"a.go"}`, strings.Repeat("a", 5000), "", "")
	offloaded2, _ := store.MaybeOffload("test:index", "Shell", `{"command":"ls"}`, strings.Repeat("b", 5000), "", "")

	// Verify index file exists and contains both entries
	sessionDir := store.getSessionDir("test:index")
	indexFile := store.indexFilePath(sessionDir)
	data, err := os.ReadFile(indexFile)
	if err != nil {
		t.Fatalf("failed to read index file: %v", err)
	}

	var entries []OffloadedResult
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatalf("failed to unmarshal index: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].ID != offloaded1.ID || entries[1].ID != offloaded2.ID {
		t.Error("index entries should match offloaded results")
	}
}

func TestEstimateTokenSize(t *testing.T) {
	// 100 chars should give roughly 40 tokens
	tokens := estimateTokenSize(strings.Repeat("a", 100), "gpt-4o")
	if tokens <= 0 || tokens > 100 {
		t.Errorf("unexpected token estimate: %d", tokens)
	}
}

func TestSummarizeRead_LongSingleLine(t *testing.T) {
	// 构造一个超长单行（模拟 JSON 压缩输出）
	longLine := strings.Repeat("x", 2000)
	content := "first line\n" + longLine + "\nlast line\n"

	// summarizeRead 接收 (args, content)，文件名从 args JSON 中提取
	summary := summarizeRead(`{"path":"bigfile.json"}`, content)

	// 超长单行应被截断到 500 字符（rune）+ 截断标记
	for _, line := range strings.Split(summary, "\n") {
		runes := []rune(line)
		if len(runes) > 530 { // 500 runes + truncation suffix ~30 chars
			t.Errorf("single line exceeds 530 chars (500+suffix): got %d runes, content: %.80s...", len(runes), line)
		}
	}
	// 应包含截断标记
	if !strings.Contains(summary, "truncated") {
		t.Error("summary should contain truncation marker 'truncated'")
	}
	// 应包含文件名
	if !strings.Contains(summary, "bigfile.json") {
		t.Error("summary should contain file name 'bigfile.json'")
	}
}

func TestSummarizeRead_TotalLimit(t *testing.T) {
	// 构造多行内容，总量超过 3000 字符
	var lines []string
	for i := 0; i < 100; i++ {
		lines = append(lines, strings.Repeat("line content number "+fmt.Sprintf("%04d", i)+" ", 30))
	}
	content := strings.Join(lines, "\n")

	summary := summarizeRead("hugefile.go", content)

	// 总量不应超过 3000 字符 + 尾部附加信息
	runes := []rune(summary)
	if len(runes) > 3200 {
		t.Errorf("summary total exceeds 3200 chars (3000 + margin): got %d", len(runes))
	}
	// 应包含截断指示
	if !strings.Contains(summary, "omitted") {
		t.Error("summary should indicate omitted lines")
	}
}

func TestSummarizeRead_UTF8SafeTruncation(t *testing.T) {
	// 构造含中文的长单行，确保 UTF-8 安全截断
	// "中文内容测试" 6 chars × 100 = 600 runes, 超过 500 会被截断
	longLine := strings.Repeat("中文内容测试", 100) // 600 runes, 1800 bytes
	content := "header\n" + longLine + "\nfooter\n"

	summary := summarizeRead("utf8file.txt", content)

	// 不应出现乱码（无效 UTF-8）
	for i, r := range summary {
		if r == utf8.RuneError {
			t.Errorf("invalid UTF-8 rune at position %d", i)
		}
	}

	// 每行 rune 数不应超过 540（500 runes + truncation suffix ~40 chars）
	for _, line := range strings.Split(summary, "\n") {
		if len([]rune(line)) > 540 {
			t.Errorf("line exceeds 540 runes: %d", len([]rune(line)))
		}
	}
}

// --- Offload staleness detection tests ---

// helper to create a store with very low thresholds and a temp dir
func newTestStore(t *testing.T) (*OffloadStore, string) {
	t.Helper()
	dir := t.TempDir()
	store := NewOffloadStore(OffloadConfig{
		StoreDir:        dir,
		MaxResultTokens: 100,
		MaxResultBytes:  10240,
	})
	return store, dir
}

func TestInvalidateStaleReads_NoChange(t *testing.T) {
	store, dir := newTestStore(t)

	// Create a file and offload its content
	filePath := filepath.Join(dir, "testfile.go")
	content := strings.Repeat("line of content\n", 500)
	os.WriteFile(filePath, []byte(content), 0o644)

	args := fmt.Sprintf(`{"path":"%s"}`, filePath)
	offloaded, ok := store.MaybeOffload("stale:test", "Read", args, content, "", "")
	if !ok {
		t.Fatal("expected offload to succeed")
	}
	if offloaded.ContentHash == "" {
		t.Fatal("ContentHash should be set for Read tool")
	}
	if offloaded.ReadPath != filePath {
		t.Fatalf("ReadPath = %q, want %q", offloaded.ReadPath, filePath)
	}

	// File unchanged → no stale
	staleIDs := store.InvalidateStaleReads("stale:test", dir, "")
	if len(staleIDs) != 0 {
		t.Errorf("expected 0 stale IDs, got %v", staleIDs)
	}
}

func TestInvalidateStaleReads_FileModified(t *testing.T) {
	store, dir := newTestStore(t)

	filePath := filepath.Join(dir, "testfile.go")
	content := strings.Repeat("original content\n", 500)
	os.WriteFile(filePath, []byte(content), 0o644)

	args := fmt.Sprintf(`{"path":"%s"}`, filePath)
	offloaded, ok := store.MaybeOffload("stale:test", "Read", args, content, "", "")
	if !ok {
		t.Fatal("expected offload to succeed")
	}

	// Modify the file
	newContent := strings.Repeat("modified content\n", 500)
	os.WriteFile(filePath, []byte(newContent), 0o644)

	staleIDs := store.InvalidateStaleReads("stale:test", dir, "")
	if len(staleIDs) != 1 {
		t.Fatalf("expected 1 stale ID, got %v", staleIDs)
	}
	if staleIDs[0] != offloaded.ID {
		t.Errorf("stale ID = %q, want %q", staleIDs[0], offloaded.ID)
	}
}

func TestInvalidateStaleReads_FileDeleted(t *testing.T) {
	store, dir := newTestStore(t)

	filePath := filepath.Join(dir, "tempfile.go")
	content := strings.Repeat("temp content\n", 500)
	os.WriteFile(filePath, []byte(content), 0o644)

	args := fmt.Sprintf(`{"path":"%s"}`, filePath)
	offloaded, ok := store.MaybeOffload("stale:test", "Read", args, content, "", "")
	if !ok {
		t.Fatal("expected offload to succeed")
	}

	// Delete the file
	os.Remove(filePath)

	staleIDs := store.InvalidateStaleReads("stale:test", dir, "")
	if len(staleIDs) != 1 {
		t.Fatalf("expected 1 stale ID, got %v", staleIDs)
	}
	if staleIDs[0] != offloaded.ID {
		t.Errorf("stale ID = %q, want %q", staleIDs[0], offloaded.ID)
	}
}

func TestInvalidateStaleReads_NonReadTool(t *testing.T) {
	store, dir := newTestStore(t)

	// Create a Shell offload (no ContentHash/ReadPath)
	shellContent := strings.Repeat("shell output\n", 500)
	offloaded, ok := store.MaybeOffload("stale:test", "Shell", `{}`, shellContent, "", "")
	if !ok {
		t.Fatal("expected offload to succeed")
	}

	// Shell offload should not have ContentHash
	if offloaded.ContentHash != "" {
		t.Error("Shell offload should not have ContentHash")
	}

	// Should not be marked stale
	staleIDs := store.InvalidateStaleReads("stale:test", dir, "")
	if len(staleIDs) != 0 {
		t.Errorf("expected 0 stale IDs for non-Read tool, got %v", staleIDs)
	}
}

func TestPurgeStaleMessages(t *testing.T) {
	store, dir := newTestStore(t)

	// Create a file and offload it
	filePath := filepath.Join(dir, "purgetest.go")
	content := strings.Repeat("purge content\n", 500)
	os.WriteFile(filePath, []byte(content), 0o644)

	args := fmt.Sprintf(`{"path":"%s"}`, filePath)
	offloaded, _ := store.MaybeOffload("stale:test", "Read", args, content, "", "")

	// Modify file to make it stale
	os.WriteFile(filePath, []byte("modified\n"), 0o644)
	store.InvalidateStaleReads("stale:test", dir, "")

	// Build messages with a tool message containing the offload marker
	originalContent := fmt.Sprintf("📂 [offload:%s] Read(...) summary here", offloaded.ID)
	messages := []llm.ChatMessage{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "read the file"},
		{Role: "assistant", Content: "", ToolCalls: []llm.ToolCall{{ID: "tc_1", Name: "Read", Arguments: args}}},
		{Role: "tool", Content: originalContent, ToolCallID: "tc_1", ToolName: "Read"},
	}

	// Purge should replace the tool message content
	purged := store.PurgeStaleMessages("stale:test", messages)

	// Original messages should not be modified
	if messages[3].Content != originalContent {
		t.Error("PurgeStaleMessages should not modify original messages slice")
	}

	// Purged message should have stale marker
	expectedStale := fmt.Sprintf("⚠️ [offload:%s] STALE — 该文件已被修改，此内容已过期。请重新 Read 获取最新内容。", offloaded.ID)
	if purged[3].Content != expectedStale {
		t.Errorf("purged content = %q, want %q", purged[3].Content, expectedStale)
	}

	// Non-tool messages should be unchanged
	if purged[0].Content != "system prompt" {
		t.Error("system message should be unchanged")
	}
	if purged[1].Content != "read the file" {
		t.Error("user message should be unchanged")
	}
}

func TestPurgeStaleMessages_NoStale(t *testing.T) {
	store, _ := newTestStore(t)

	messages := []llm.ChatMessage{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
	}

	purged := store.PurgeStaleMessages("stale:test", messages)

	if len(purged) != len(messages) {
		t.Errorf("expected same length, got %d vs %d", len(purged), len(messages))
	}

	// Should return the same messages (or equivalent)
	if purged[1].Content != "hello" {
		t.Error("messages should be unchanged when no stale offloads")
	}
}

func TestInvalidateStaleReads_AlreadyStale(t *testing.T) {
	store, dir := newTestStore(t)

	filePath := filepath.Join(dir, "already_stale.go")
	content := strings.Repeat("already stale content\n", 500)
	os.WriteFile(filePath, []byte(content), 0o644)

	args := fmt.Sprintf(`{"path":"%s"}`, filePath)
	_, _ = store.MaybeOffload("stale:test", "Read", args, content, "", "")

	// Modify file and invalidate → first time should return the ID
	os.WriteFile(filePath, []byte("changed\n"), 0o644)
	staleIDs := store.InvalidateStaleReads("stale:test", dir, "")
	if len(staleIDs) != 1 {
		t.Fatalf("expected 1 stale ID on first call, got %v", staleIDs)
	}

	// Second call → already stale, should not return again
	staleIDs2 := store.InvalidateStaleReads("stale:test", dir, "")
	if len(staleIDs2) != 0 {
		t.Errorf("expected 0 stale IDs on second call (already stale), got %v", staleIDs2)
	}
}

func TestInvalidateStaleReads_RelativePath(t *testing.T) {
	store, dir := newTestStore(t)

	// Use a relative path
	filePath := filepath.Join(dir, "relfile.go")
	content := strings.Repeat("relative path content\n", 500)
	os.WriteFile(filePath, []byte(content), 0o644)

	// Use relative path in args
	relPath := "relfile.go"
	args := fmt.Sprintf(`{"path":"%s"}`, relPath)
	offloaded, _ := store.MaybeOffload("stale:test", "Read", args, content, dir, "")

	// Modify the file
	os.WriteFile(filePath, []byte("modified relative\n"), 0o644)

	// Invalidate with workspaceRoot = dir should resolve the relative path
	staleIDs := store.InvalidateStaleReads("stale:test", dir, "")
	if len(staleIDs) != 1 {
		t.Fatalf("expected 1 stale ID with relative path, got %v", staleIDs)
	}
	if staleIDs[0] != offloaded.ID {
		t.Errorf("stale ID = %q, want %q", staleIDs[0], offloaded.ID)
	}
}

// ============================================================================
// InvalidateStaleReads sandbox 路径转换回归测试
// LOCKED: 验证 ReadPath 为沙箱格式时正确转换为宿主机路径做 os.ReadFile。
// 这是 stale 误报的根因修复：LLM 传沙箱路径，os.ReadFile 需要宿主机路径。
// DO NOT MODIFY without understanding the sandbox↔host path convention.
// ============================================================================

func TestInvalidateStaleReads_SandboxPathConversion(t *testing.T) {
	store, _ := newTestStore(t)

	// 模拟 sandbox 场景：
	// - workspaceRoot (宿主机) = tmpdir
	// - sandboxWorkDir = /workspace
	// - ReadPath 存的是沙箱路径 /workspace/main.go
	hostDir := t.TempDir()
	hostFile := filepath.Join(hostDir, "main.go")
	content := strings.Repeat("package main\n", 500)
	os.WriteFile(hostFile, []byte(content), 0o644)

	// 用沙箱路径做 ReadPath（模拟 LLM 传入的路径）
	sandboxPath := "/workspace/main.go"
	args := fmt.Sprintf(`{"path":"%s"}`, sandboxPath)
	offloaded, ok := store.MaybeOffload("stale:sandbox", "Read", args, content, hostDir, "/workspace")
	if !ok {
		t.Fatal("expected offload to succeed")
	}
	if offloaded.ReadPath != sandboxPath {
		t.Fatalf("ReadPath = %q, want %q", offloaded.ReadPath, sandboxPath)
	}

	// 文件未改 → 不应 stale（验证沙箱→宿主机路径转换正确）
	staleIDs := store.InvalidateStaleReads("stale:sandbox", hostDir, "/workspace")
	if len(staleIDs) != 0 {
		t.Errorf("expected 0 stale (file unchanged), got %v", staleIDs)
	}

	// 修改文件 → 应检测到 stale
	os.WriteFile(hostFile, []byte("modified\n"), 0o644)
	staleIDs = store.InvalidateStaleReads("stale:sandbox", hostDir, "/workspace")
	if len(staleIDs) != 1 {
		t.Fatalf("expected 1 stale after modification, got %v", staleIDs)
	}
	if staleIDs[0] != offloaded.ID {
		t.Errorf("stale ID = %q, want %q", staleIDs[0], offloaded.ID)
	}
}

func TestInvalidateStaleReads_SandboxPathDeleted(t *testing.T) {
	store, _ := newTestStore(t)

	hostDir := t.TempDir()
	hostFile := filepath.Join(hostDir, "temp.go")
	content := strings.Repeat("temp content\n", 500)
	os.WriteFile(hostFile, []byte(content), 0o644)

	sandboxPath := "/workspace/temp.go"
	args := fmt.Sprintf(`{"path":"%s"}`, sandboxPath)
	offloaded, ok := store.MaybeOffload("stale:sandbox-del", "Read", args, content, hostDir, "/workspace")
	if !ok {
		t.Fatal("expected offload to succeed")
	}

	os.Remove(hostFile)

	staleIDs := store.InvalidateStaleReads("stale:sandbox-del", hostDir, "/workspace")
	if len(staleIDs) != 1 {
		t.Fatalf("expected 1 stale after deletion, got %v", staleIDs)
	}
	if staleIDs[0] != offloaded.ID {
		t.Errorf("stale ID = %q, want %q", staleIDs[0], offloaded.ID)
	}
}

func TestInvalidateStaleReads_SandboxNestedPath(t *testing.T) {
	store, _ := newTestStore(t)

	hostDir := t.TempDir()
	subDir := filepath.Join(hostDir, "agent")
	os.MkdirAll(subDir, 0o755)
	hostFile := filepath.Join(subDir, "engine.go")
	content := strings.Repeat("func Run() {}\n", 500)
	os.WriteFile(hostFile, []byte(content), 0o644)

	// LLM 传入 /workspace/agent/engine.go
	sandboxPath := "/workspace/agent/engine.go"
	args := fmt.Sprintf(`{"path":"%s"}`, sandboxPath)
	_, ok := store.MaybeOffload("stale:nested", "Read", args, content, hostDir, "/workspace")
	if !ok {
		t.Fatal("expected offload to succeed")
	}

	// 未修改 → 不 stale
	staleIDs := store.InvalidateStaleReads("stale:nested", hostDir, "/workspace")
	if len(staleIDs) != 0 {
		t.Errorf("expected 0 stale for nested path, got %v", staleIDs)
	}

	// 修改 → stale
	os.WriteFile(hostFile, []byte("changed\n"), 0o644)
	staleIDs = store.InvalidateStaleReads("stale:nested", hostDir, "/workspace")
	if len(staleIDs) != 1 {
		t.Errorf("expected 1 stale for nested path, got %v", staleIDs)
	}
}

func TestReadArgsHasOffsetOrLimit(t *testing.T) {
	tests := []struct {
		name string
		args string
		want bool
	}{
		{"no offset/limit", `{"path":"file.go"}`, false},
		{"offset zero", `{"path":"file.go","offset":0}`, false},
		{"max_lines zero", `{"path":"file.go","max_lines":0}`, false},
		{"offset > 0", `{"path":"file.go","offset":10}`, true},
		{"max_lines > 0", `{"path":"file.go","max_lines":50}`, true},
		{"both set", `{"path":"file.go","offset":10,"max_lines":50}`, true},
		{"invalid json", `{invalid}`, false},
		{"empty", ``, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := readArgsHasOffsetOrLimit(tt.args)
			if got != tt.want {
				t.Errorf("readArgsHasOffsetOrLimit(%q) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}
