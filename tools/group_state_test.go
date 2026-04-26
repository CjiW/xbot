package tools

import (
	"slices"
	"testing"
)

func TestGroupCreateAndGet(t *testing.T) {
	gm := CreateGroup("test1", []string{"agent:a/r1", "agent:b/r2"})
	if gm.ID != "test1" {
		t.Errorf("expected ID test1, got %s", gm.ID)
	}
	if gm.Name != "group:test1" {
		t.Errorf("expected Name group:test1, got %s", gm.Name)
	}
	if len(gm.Members) != 2 {
		t.Fatalf("expected 2 members, got %d", len(gm.Members))
	}
	if gm.Closed {
		t.Error("new group should not be closed")
	}

	got, ok := GetGroup("group:test1")
	if !ok {
		t.Fatal("GetGroup failed")
	}
	if got.ID != "test1" {
		t.Errorf("retrieved wrong group: %s", got.ID)
	}

	DeleteGroup("group:test1")
	_, ok = GetGroup("group:test1")
	if ok {
		t.Error("group should be deleted")
	}
}

func TestGroupIsMember(t *testing.T) {
	gm := CreateGroup("test2", []string{"agent:a/1", "agent:b/2"})
	defer DeleteGroup("group:test2")

	if !gm.IsMember("agent:a/1") {
		t.Error("agent:a/1 should be a member")
	}
	if gm.IsMember("agent:c/3") {
		t.Error("agent:c/3 should not be a member")
	}
}

func TestGroupClose(t *testing.T) {
	gm := CreateGroup("test3", []string{"agent:a/1"})
	defer DeleteGroup("group:test3")

	if gm.Closed {
		t.Error("group should not be closed initially")
	}
	gm.Close()
	if !gm.Closed {
		t.Error("group should be closed after Close()")
	}
}

func TestGroupDeleteNonexistent(t *testing.T) {
	DeleteGroup("group:nonexistent") // should not panic
}

func TestListGroups(t *testing.T) {
	defer DeleteGroup("group:la1")
	defer DeleteGroup("group:la2")

	CreateGroup("la1", []string{"agent:x/1"})
	CreateGroup("la2", []string{"agent:y/2", "agent:z/3"})

	groups := ListGroups()
	if len(groups) < 2 {
		t.Fatalf("expected at least 2 groups, got %d", len(groups))
	}

	if !slices.ContainsFunc(groups, func(g GroupSummary) bool { return g.ID == "la1" && len(g.Members) == 1 }) {
		t.Error("la1 group not found in listing")
	}
}
