package tools

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckpointStore_WriteAndRead(t *testing.T) {
	dir := t.TempDir()
	store, err := NewCheckpointStore(dir)
	if err != nil {
		t.Fatalf("NewCheckpointStore: %v", err)
	}
	defer store.Close()

	// Write two snapshots
	snap1 := FileSnapshot{
		TurnIdx:    1,
		ToolName:   "FileReplace",
		FilePath:   "/tmp/foo.go",
		Existed:    true,
		ContentB64: "c2FtcGxl", // "sample" in base64
	}
	snap2 := FileSnapshot{
		TurnIdx:  2,
		ToolName: "FileCreate",
		FilePath: "/tmp/new.go",
		Existed:  false,
	}

	if err := store.Write(snap1); err != nil {
		t.Fatalf("Write snap1: %v", err)
	}
	if err := store.Write(snap2); err != nil {
		t.Fatalf("Write snap2: %v", err)
	}

	// Read back
	snaps, err := store.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(snaps) != 2 {
		t.Fatalf("expected 2 snapshots, got %d", len(snaps))
	}
	if snaps[0].FilePath != "/tmp/foo.go" || snaps[0].TurnIdx != 1 {
		t.Errorf("snap1 mismatch: %+v", snaps[0])
	}
	if snaps[1].FilePath != "/tmp/new.go" || snaps[1].TurnIdx != 2 {
		t.Errorf("snap2 mismatch: %+v", snaps[1])
	}
}

func TestCheckpointStore_Rewind(t *testing.T) {
	dir := t.TempDir()
	store, err := NewCheckpointStore(dir)
	if err != nil {
		t.Fatalf("NewCheckpointStore: %v", err)
	}
	defer store.Close()

	// Create test files
	testDir := t.TempDir()
	fooPath := filepath.Join(testDir, "foo.go")
	barPath := filepath.Join(testDir, "bar.go")
	newPath := filepath.Join(testDir, "new.go")

	if err := os.WriteFile(fooPath, []byte("original foo"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(barPath, []byte("original bar"), 0644); err != nil {
		t.Fatal(err)
	}

	// Turn 1: modify foo.go
	store.Write(FileSnapshot{
		TurnIdx: 1, ToolName: "FileReplace", FilePath: fooPath,
		Existed: true, ContentB64: encodeB64("original foo"),
	})
	// Turn 2: modify bar.go, create new.go
	store.Write(FileSnapshot{
		TurnIdx: 2, ToolName: "FileReplace", FilePath: barPath,
		Existed: true, ContentB64: encodeB64("original bar"),
	})
	store.Write(FileSnapshot{
		TurnIdx: 2, ToolName: "FileCreate", FilePath: newPath,
		Existed: false,
	})

	// Simulate agent edits: modify files
	os.WriteFile(fooPath, []byte("modified foo turn 1"), 0644)
	os.WriteFile(barPath, []byte("modified bar turn 2"), 0644)
	os.WriteFile(newPath, []byte("new file content"), 0644)

	// Rewind to before turn 2
	result := store.Rewind(2)

	if len(result.Restored) != 1 {
		t.Errorf("expected 1 restored, got %d", len(result.Restored))
	}
	if len(result.CreatedDel) != 1 {
		t.Errorf("expected 1 created del, got %d", len(result.CreatedDel))
	}
	if len(result.Errors) != 0 {
		t.Errorf("expected 0 errors, got %d: %v", len(result.Errors), result.Errors)
	}

	// Verify bar.go was restored
	content, err := os.ReadFile(barPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "original bar" {
		t.Errorf("bar.go content = %q, want %q", string(content), "original bar")
	}

	// Verify new.go was deleted
	if _, err := os.Stat(newPath); !os.IsNotExist(err) {
		t.Error("new.go should have been deleted")
	}

	// Verify foo.go was NOT restored (it was modified in turn 1, rewind to turn 2)
	content, err = os.ReadFile(fooPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "modified foo turn 1" {
		t.Errorf("foo.go should not be restored: got %q", string(content))
	}
}

func TestCheckpointStore_RewindAll(t *testing.T) {
	dir := t.TempDir()
	store, err := NewCheckpointStore(dir)
	if err != nil {
		t.Fatalf("NewCheckpointStore: %v", err)
	}
	defer store.Close()

	testDir := t.TempDir()
	fooPath := filepath.Join(testDir, "foo.go")
	os.WriteFile(fooPath, []byte("original"), 0644)

	// Turn 1: modify foo.go
	store.Write(FileSnapshot{
		TurnIdx: 1, ToolName: "FileReplace", FilePath: fooPath,
		Existed: true, ContentB64: encodeB64("original"),
	})

	os.WriteFile(fooPath, []byte("modified"), 0644)

	// Rewind everything
	result := store.Rewind(1)
	if len(result.Restored) != 1 {
		t.Errorf("expected 1 restored, got %d", len(result.Restored))
	}

	content, _ := os.ReadFile(fooPath)
	if string(content) != "original" {
		t.Errorf("foo.go = %q, want %q", string(content), "original")
	}
}

func TestCheckpointStore_CountChanges(t *testing.T) {
	dir := t.TempDir()
	store, err := NewCheckpointStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	store.Write(FileSnapshot{TurnIdx: 1, FilePath: "/a.go"})
	store.Write(FileSnapshot{TurnIdx: 2, FilePath: "/b.go"})
	store.Write(FileSnapshot{TurnIdx: 2, FilePath: "/c.go"})
	store.Write(FileSnapshot{TurnIdx: 3, FilePath: "/d.go"})

	if n := store.CountChanges(2); n != 3 {
		t.Errorf("CountChanges(2) = %d, want 3 (b, c, d)", n)
	}
	if n := store.CountChanges(3); n != 1 {
		t.Errorf("CountChanges(3) = %d, want 1 (d)", n)
	}
	if n := store.CountChanges(4); n != 0 {
		t.Errorf("CountChanges(4) = %d, want 0", n)
	}
}

func TestCheckpointStore_Cleanup(t *testing.T) {
	dir := t.TempDir()
	store, err := NewCheckpointStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	store.Write(FileSnapshot{TurnIdx: 1, FilePath: "/a.go"})
	store.Close()

	store2, err := NewCheckpointStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	snaps, _ := store2.ReadAll()
	if len(snaps) != 1 {
		t.Errorf("expected 1 snapshot after reopen, got %d", len(snaps))
	}
	store2.Cleanup()

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("cleanup should remove directory")
	}
}

// encodeB64 is a test helper for base64 encoding file content.
func encodeB64(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

func TestCheckpointHook_PrePost(t *testing.T) {
	dir := t.TempDir()
	store, err := NewCheckpointStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	hook := NewCheckpointHook(store)
	hook.SetTurnIdx(1)

	testDir := t.TempDir()
	testFile := filepath.Join(testDir, "test.go")
	os.WriteFile(testFile, []byte("before"), 0644)

	// Pre: snapshot before edit
	args := `{"path": "` + testFile + `", "old_string": "before", "new_string": "after"}`
	if err := hook.PreToolUse(context.TODO(), "FileReplace", args); err != nil {
		t.Fatalf("PreToolUse: %v", err)
	}

	// Simulate the edit
	os.WriteFile(testFile, []byte("after"), 0644)

	// Post: confirm success
	hook.PostToolUse(context.TODO(), "FileReplace", args, nil, nil, 0)

	// Verify snapshot was recorded
	snaps, _ := store.ReadAll()
	if len(snaps) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(snaps))
	}
	if snaps[0].FilePath != testFile {
		t.Errorf("path = %q, want %q", snaps[0].FilePath, testFile)
	}
	if !snaps[0].Existed {
		t.Error("file should have existed")
	}
}

func TestCheckpointHook_PostError(t *testing.T) {
	dir := t.TempDir()
	store, err := NewCheckpointStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	hook := NewCheckpointHook(store)
	hook.SetTurnIdx(1)

	testDir := t.TempDir()
	testFile := filepath.Join(testDir, "test.go")
	os.WriteFile(testFile, []byte("before"), 0644)

	args := `{"path": "` + testFile + `", "old_string": "x", "new_string": "y"}`
	hook.PreToolUse(context.TODO(), "FileReplace", args)

	// Post with error — should discard snapshot
	hook.PostToolUse(context.TODO(), "FileReplace", args, nil, os.ErrNotExist, 0)

	snaps, _ := store.ReadAll()
	if len(snaps) != 0 {
		t.Errorf("expected 0 snapshots on error, got %d", len(snaps))
	}
}
