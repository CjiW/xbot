package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	_, wasOffloaded := store.MaybeOffload("test:session", "Read", `{"path":"file.go"}`, smallResult)
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
	offloaded, wasOffloaded := store.MaybeOffload("test:session", "Read", `{"path":"bigfile.go"}`, largeResult)
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

	_, wasOffloaded := store.MaybeOffload("test:session", "Read", `{"path":"file.go"}`, "")
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
	offloaded, wasOffloaded := store.MaybeOffload("test:session", "Shell", `{"command":"ls -la"}`, originalContent)
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
	offloaded, _ := store.MaybeOffload("session1", "Read", `{"path":"a.go"}`, originalContent)

	// Try to recall from a different session
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

	offloaded, _ := store.MaybeOffload("test:clean", "Read", `{"path":"file.go"}`, strings.Repeat("z", 5000))
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
	offloaded1, _ := store.MaybeOffload("test:index", "Read", `{"path":"a.go"}`, strings.Repeat("a", 5000))
	offloaded2, _ := store.MaybeOffload("test:index", "Shell", `{"command":"ls"}`, strings.Repeat("b", 5000))

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
