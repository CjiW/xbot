package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditTool_Replace(t *testing.T) {
	// 创建临时文件
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")

	content := "Hello World\nHello Go\nHello Rust"
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &EditTool{}

	// 测试单次替换
	result, err := tool.Execute(nil, `{"mode": "replace", "path": "`+testFile+`", "old_string": "Hello", "new_string": "Hi"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Summary, "1 of 3") {
		t.Errorf("expected warning about multiple occurrences, got: %s", result.Summary)
	}

	// 验证只替换了第一个
	data, _ := os.ReadFile(testFile)
	if string(data) != "Hi World\nHello Go\nHello Rust" {
		t.Errorf("unexpected content: %s", string(data))
	}

	// 测试全部替换
	result, err = tool.Execute(nil, `{"mode": "replace", "path": "`+testFile+`", "old_string": "Hello", "new_string": "Hi", "replace_all": true}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, _ = os.ReadFile(testFile)
	if string(data) != "Hi World\nHi Go\nHi Rust" {
		t.Errorf("unexpected content after replace_all: %s", string(data))
	}
}

func TestEditTool_LineEdit(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")

	content := "line1\nline2\nline3"
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &EditTool{}

	// 测试 insert_after
	_, err := tool.Execute(nil, `{"mode": "line", "path": "`+testFile+`", "line_number": 2, "action": "insert_after", "content": "new_line"}`)
	if err != nil {
		t.Fatalf("insert_after error: %v", err)
	}

	data, _ := os.ReadFile(testFile)
	expected := "line1\nline2\nnew_line\nline3"
	if string(data) != expected {
		t.Errorf("insert_after failed, got: %s, expected: %s", string(data), expected)
	}

	// 测试 delete
	_, err = tool.Execute(nil, `{"mode": "line", "path": "`+testFile+`", "line_number": 3, "action": "delete"}`)
	if err != nil {
		t.Fatalf("delete error: %v", err)
	}

	data, _ = os.ReadFile(testFile)
	expected = "line1\nline2\nline3"
	if string(data) != expected {
		t.Errorf("delete failed, got: %s, expected: %s", string(data), expected)
	}

	// 测试 replace
	_, err = tool.Execute(nil, `{"mode": "line", "path": "`+testFile+`", "line_number": 2, "action": "replace", "content": "replaced_line2"}`)
	if err != nil {
		t.Fatalf("replace error: %v", err)
	}

	data, _ = os.ReadFile(testFile)
	expected = "line1\nreplaced_line2\nline3"
	if string(data) != expected {
		t.Errorf("replace failed, got: %s, expected: %s", string(data), expected)
	}
}

func TestEditTool_Regex(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.go")

	content := `func foo() {}
func bar() {}
func baz() {}`
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &EditTool{}

	// 测试正则替换
	_, err := tool.Execute(nil, `{"mode": "regex", "path": "`+testFile+`", "pattern": "func (\\w+)", "replacement": "function $1", "replace_all": true}`)
	if err != nil {
		t.Fatalf("regex error: %v", err)
	}

	data, _ := os.ReadFile(testFile)
	expected := `function foo() {}
function bar() {}
function baz() {}`
	if string(data) != expected {
		t.Errorf("regex failed, got: %s", string(data))
	}
}

func TestEditTool_Insert(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")

	content := "line1\nline2"
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &EditTool{}

	// 测试 insert at start
	_, err := tool.Execute(nil, `{"mode": "insert", "path": "`+testFile+`", "position": "start", "content": "header\n"}`)
	if err != nil {
		t.Fatalf("insert start error: %v", err)
	}

	data, _ := os.ReadFile(testFile)
	expected := "header\nline1\nline2"
	if string(data) != expected {
		t.Errorf("insert start failed, got: %s", string(data))
	}

	// 测试 insert at end
	_, err = tool.Execute(nil, `{"mode": "insert", "path": "`+testFile+`", "position": "end", "content": "footer"}`)
	if err != nil {
		t.Fatalf("insert end error: %v", err)
	}

	data, _ = os.ReadFile(testFile)
	expected = "header\nline1\nline2\nfooter"
	if string(data) != expected {
		t.Errorf("insert end failed, got: %s", string(data))
	}
}

func TestEditTool_Create(t *testing.T) {
	tmpDir := t.TempDir()

	tool := &EditTool{}

	// 测试创建文件
	testFile := filepath.Join(tmpDir, "newfile.txt")
	result, err := tool.Execute(nil, `{"mode": "create", "path": "`+testFile+`", "content": "Hello, World!"}`)
	if err != nil {
		t.Fatalf("create error: %v", err)
	}
	if !strings.Contains(result.Summary, "File created successfully") {
		t.Errorf("expected success message, got: %s", result.Summary)
	}

	data, _ := os.ReadFile(testFile)
	if string(data) != "Hello, World!" {
		t.Errorf("unexpected content: %s", string(data))
	}

	// 测试创建带子目录的文件
	testFile2 := filepath.Join(tmpDir, "sub", "dir", "file.txt")
	_, err = tool.Execute(nil, `{"mode": "create", "path": "`+testFile2+`", "content": "nested file"}`)
	if err != nil {
		t.Fatalf("create nested error: %v", err)
	}

	data, _ = os.ReadFile(testFile2)
	if string(data) != "nested file" {
		t.Errorf("unexpected nested content: %s", string(data))
	}
}

func TestEditTool_Errors(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")

	if err := os.WriteFile(testFile, []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &EditTool{}

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "missing path",
			input: `{"mode": "replace"}`,
			want:  "path is required",
		},
		{
			name:  "missing mode",
			input: `{"path": "` + testFile + `"}`,
			want:  "mode is required",
		},
		{
			name:  "unknown mode",
			input: `{"path": "` + testFile + `", "mode": "unknown"}`,
			want:  "unknown mode",
		},
		{
			name:  "text not found",
			input: `{"path": "` + testFile + `", "mode": "replace", "old_string": "notexist", "new_string": "new"}`,
			want:  "text not found",
		},
		{
			name:  "invalid regex",
			input: `{"path": "` + testFile + `", "mode": "regex", "pattern": "[invalid", "replacement": "new"}`,
			want:  "invalid regex",
		},
		{
			name:  "line number exceeds",
			input: `{"path": "` + testFile + `", "mode": "line", "line_number": 100, "action": "delete"}`,
			want:  "exceeds total lines",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tool.Execute(nil, tt.input)
			if err == nil {
				t.Error("expected error, got nil")
				return
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error should contain %q, got: %v", tt.want, err)
			}
		})
	}
}
