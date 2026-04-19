package tools

import (
	"strings"
	"testing"
)

func TestGroupStateCreateAndGet(t *testing.T) {
	gs := CreateGroupState("test1", "moderator", []string{"agent:a/r1", "agent:b/r2"}, 5)
	if gs.ID != "test1" {
		t.Errorf("expected ID test1, got %s", gs.ID)
	}
	if gs.Moderator != "moderator" {
		t.Errorf("expected moderator, got %s", gs.Moderator)
	}
	if len(gs.Members) != 2 {
		t.Fatalf("expected 2 members, got %d", len(gs.Members))
	}
	if gs.MaxRounds != 5 {
		t.Errorf("expected maxRounds 5, got %d", gs.MaxRounds)
	}
	if gs.Closed {
		t.Error("new group should not be closed")
	}

	// Retrieve
	got, ok := GetGroupState("group:test1")
	if !ok {
		t.Fatal("GetGroupState failed")
	}
	if got.ID != "test1" {
		t.Errorf("retrieved wrong group: %s", got.ID)
	}

	// Delete
	DeleteGroupState("group:test1")
	_, ok = GetGroupState("group:test1")
	if ok {
		t.Error("group should be deleted")
	}
}

func TestGroupStateAddMessage(t *testing.T) {
	gs := CreateGroupState("test2", "mod", []string{"agent:x/y"}, 10)
	defer DeleteGroupState("group:test2")

	// Initial message is the system creation message
	if len(gs.Messages) != 1 {
		t.Fatalf("expected 1 initial message, got %d", len(gs.Messages))
	}

	n := gs.AddMessage("mod", "Hello everyone", false)
	if n != 2 {
		t.Errorf("expected 2 messages after add, got %d", n)
	}

	n = gs.AddMessage("agent:x/y", "Hi!", false)
	if n != 3 {
		t.Errorf("expected 3 messages after add, got %d", n)
	}
}

func TestGroupStateGetHistory(t *testing.T) {
	gs := CreateGroupState("test3", "mod", []string{"agent:a/1", "agent:b/2"}, 10)
	defer DeleteGroupState("group:test3")

	gs.AddMessage("mod", "Let's discuss the design.", false)
	gs.AddMessage("agent:a/1", "I think we should use option A.", false)

	history := gs.GetHistory()
	if history == "" {
		t.Fatal("history should not be empty")
	}
	if !strings.Contains(history, "Let's discuss") {
		t.Error("history should contain moderator message")
	}
	if !strings.Contains(history, "option A") {
		t.Error("history should contain agent message")
	}
	if !strings.Contains(history, "System") {
		t.Error("history should contain system message")
	}
}

func TestGroupStateIsMember(t *testing.T) {
	gs := CreateGroupState("test4", "mod", []string{"agent:a/1", "agent:b/2"}, 5)
	defer DeleteGroupState("group:test4")

	if !gs.IsMember("agent:a/1") {
		t.Error("agent:a/1 should be a member")
	}
	if !gs.IsMember("agent:b/2") {
		t.Error("agent:b/2 should be a member")
	}
	if gs.IsMember("agent:c/3") {
		t.Error("agent:c/3 should not be a member")
	}
}

func TestGroupStateClose(t *testing.T) {
	gs := CreateGroupState("test5", "mod", []string{"agent:a/1"}, 5)
	defer DeleteGroupState("group:test5")

	if gs.Closed {
		t.Error("group should not be closed initially")
	}

	gs.Close("test done")
	if !gs.Closed {
		t.Error("group should be closed after Close()")
	}

	// Last message should be the close reason
	lastMsg := gs.Messages[len(gs.Messages)-1]
	if !lastMsg.IsSystem {
		t.Error("close message should be system message")
	}
	if !strings.Contains(lastMsg.Content, "test done") {
		t.Errorf("close message should contain reason, got: %s", lastMsg.Content)
	}
}

func TestGroupStateDeleteNonexistent(t *testing.T) {
	// Should not panic
	DeleteGroupState("group:nonexistent")
}
