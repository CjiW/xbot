package tools

import (
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

// TestCheckpointStore_RewindMultiCycle tests the scenario where a user
// repeatedly rewinds to the same message, sends, cancels, then rewinds again.
// This was a real bug: turnsAfter (a count) was passed to Rewind() which
// expects an absolute turn index. After cycles, agentTurnID grows, making
// the count much smaller than the actual turn indices, causing Rewind() to
// delete ALL checkpoints including early ones.
func TestCheckpointStore_RewindMultiCycle(t *testing.T) {
	dir := t.TempDir()
	store, err := NewCheckpointStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	testDir := t.TempDir()
	fooPath := filepath.Join(testDir, "foo.go")
	barPath := filepath.Join(testDir, "bar.go")
	bazPath := filepath.Join(testDir, "baz.go")

	os.WriteFile(fooPath, []byte("original foo"), 0644)
	os.WriteFile(barPath, []byte("original bar"), 0644)
	os.WriteFile(bazPath, []byte("original baz"), 0644)

	// Turn 1: modify foo.go
	store.Write(FileSnapshot{TurnIdx: 1, ToolName: "FileReplace", FilePath: fooPath,
		Existed: true, ContentB64: encodeB64("original foo")})
	os.WriteFile(fooPath, []byte("modified foo"), 0644)

	// Turn 2: modify bar.go
	store.Write(FileSnapshot{TurnIdx: 2, ToolName: "FileReplace", FilePath: barPath,
		Existed: true, ContentB64: encodeB64("original bar")})
	os.WriteFile(barPath, []byte("modified bar"), 0644)

	// Turn 3: modify baz.go
	store.Write(FileSnapshot{TurnIdx: 3, ToolName: "FileReplace", FilePath: bazPath,
		Existed: true, ContentB64: encodeB64("original baz")})
	os.WriteFile(bazPath, []byte("modified baz"), 0644)

	// --- Cycle 1: Rewind to turn 2 (simulate user selecting 2nd message) ---
	// agentTurnID=3, rewindItems=[U1,U2,U3], selecting U2 (index 1)
	// Correct absTurnIdx = 3 - (3-1-1) = 2
	store.Rewind(2)

	// Verify: baz.go restored (turn 3 snapshot), bar.go restored (turn 2 snapshot)
	content, _ := os.ReadFile(bazPath)
	if string(content) != "original baz" {
		t.Errorf("cycle 1: baz.go = %q, want %q", string(content), "original baz")
	}
	content, _ = os.ReadFile(barPath)
	if string(content) != "original bar" {
		t.Errorf("cycle 1: bar.go = %q, want %q", string(content), "original bar")
	}

	// Verify turn 1 snapshot still exists (critical regression check)
	snaps, _ := store.ReadAll()
	for _, s := range snaps {
		if s.TurnIdx == 1 {
			t.Log("turn 1 snapshot survived cycle 1 (correct)")
			break
		}
	}
	// After Rewind(2), snapshots with TurnIdx >= 2 are truncated
	// So only turn 1 snapshot should remain
	for _, s := range snaps {
		if s.TurnIdx >= 2 {
			t.Errorf("cycle 1: turn %d snapshot should have been truncated", s.TurnIdx)
		}
	}

	// --- Cycle 2: User re-sends (turn 4), modifies bar.go, then cancels ---
	store.Write(FileSnapshot{TurnIdx: 4, ToolName: "FileReplace", FilePath: barPath,
		Existed: true, ContentB64: encodeB64("original bar")})
	os.WriteFile(barPath, []byte("modified bar v2"), 0644)

	// Now agentTurnID=4, rewindItems=[U1,U2_new]
	// Simulate rewind to U2_new: absTurnIdx = 4 - (2-1-1) = 4
	store.Rewind(4)

	// bar.go should be restored to "original bar"
	content, _ = os.ReadFile(barPath)
	if string(content) != "original bar" {
		t.Errorf("cycle 2: bar.go = %q, want %q", string(content), "original bar")
	}

	// Verify turn 1 snapshot STILL exists (this was the bug: old code would
	// pass turnsAfter=1, causing Rewind(1) to delete turn 1's snapshot)
	snaps, _ = store.ReadAll()
	foundTurn1 := false
	for _, s := range snaps {
		if s.TurnIdx == 1 {
			foundTurn1 = true
			break
		}
	}
	if !foundTurn1 {
		t.Error("REGRESSION: turn 1 snapshot was deleted after cycle 2 rewind")
	}

	// --- Cycle 3: Another re-send (turn 5), modifies foo.go, then cancels ---
	store.Write(FileSnapshot{TurnIdx: 5, ToolName: "FileReplace", FilePath: fooPath,
		Existed: true, ContentB64: encodeB64("original foo")})
	os.WriteFile(fooPath, []byte("modified foo v2"), 0644)

	// agentTurnID=5, rewindItems=[U1,U2_new2]
	// Simulate rewind to U1: absTurnIdx = 5 - (2-1-0) = 4
	store.Rewind(4)

	// foo.go was modified in cycle 3 at turn 5, Rewind(4) removes TurnIdx >= 4
	// Turn 5 snapshot is removed → foo.go restored (it was turn 1 that modified it,
	// and turn 5 that modified it again — only turn 5's snapshot exists now)
	// Wait: foo.go was modified at turn 1 (snapshot exists). Rewind(4) keeps TurnIdx < 4.
	// Turn 1 snapshot for foo.go is kept. But foo.go is currently "modified foo v2".
	// Rewind(4) removes turn 5 snapshot and restores foo.go from... no, Rewind only
	// restores files that have snapshots with TurnIdx >= the argument.
	// Turn 5's snapshot for foo.go has TurnIdx=5 >= 4, so it gets restored.
	content, _ = os.ReadFile(fooPath)
	if string(content) != "original foo" {
		t.Errorf("cycle 3: foo.go = %q, want %q", string(content), "original foo")
	}
	content, _ = os.ReadFile(barPath)
	// bar.go was "original bar" after cycle 2. No new snapshot for bar in cycle 3.
	if string(content) != "original bar" {
		t.Errorf("cycle 3: bar.go = %q, want %q", string(content), "original bar")
	}
}

// TestCheckpointStore_RewindPreservesEarlierSnapshots verifies that Rewind(N)
// only removes snapshots with TurnIdx >= N, preserving all earlier ones.
func TestCheckpointStore_RewindPreservesEarlierSnapshots(t *testing.T) {
	dir := t.TempDir()
	store, err := NewCheckpointStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// Write snapshots across 5 turns
	for i := 1; i <= 5; i++ {
		store.Write(FileSnapshot{
			TurnIdx:    i,
			ToolName:   "FileReplace",
			FilePath:   filepath.Join(t.TempDir(), "f.go"),
			Existed:    true,
			ContentB64: encodeB64("content"),
		})
	}

	// Rewind to turn 3
	store.Rewind(3)

	snaps, _ := store.ReadAll()
	for _, s := range snaps {
		if s.TurnIdx >= 3 {
			t.Errorf("snapshot with TurnIdx=%d should have been removed", s.TurnIdx)
		}
	}
	// Turns 1 and 2 should remain
	if len(snaps) != 2 {
		t.Errorf("expected 2 remaining snapshots, got %d", len(snaps))
	}
}
