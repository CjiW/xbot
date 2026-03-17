package agent

import "testing"

func TestDeriveSubAgentTenantID_Deterministic(t *testing.T) {
	// Same inputs should always produce the same tenantID
	id1 := deriveSubAgentTenantID(42, "main", "code-reviewer")
	id2 := deriveSubAgentTenantID(42, "main", "code-reviewer")
	if id1 != id2 {
		t.Errorf("expected deterministic output: %d != %d", id1, id2)
	}
}

func TestDeriveSubAgentTenantID_Negative(t *testing.T) {
	// SubAgent tenantIDs must be negative to distinguish from normal IM tenantIDs
	cases := []struct {
		parentTenantID int64
		parentAgentID  string
		roleName       string
	}{
		{1, "main", "code-reviewer"},
		{42, "main", "researcher"},
		{0, "main", "default"},
		{999, "main/nested", "sub"},
	}
	for _, c := range cases {
		id := deriveSubAgentTenantID(c.parentTenantID, c.parentAgentID, c.roleName)
		if id >= 0 {
			t.Errorf("tenantID should be negative for (%d, %q, %q): got %d", c.parentTenantID, c.parentAgentID, c.roleName, id)
		}
	}
}

func TestDeriveSubAgentTenantID_Uniqueness(t *testing.T) {
	// Different inputs should produce different tenantIDs
	ids := map[int64]bool{}
	type testCase struct {
		parentTenantID int64
		parentAgentID  string
		roleName       string
	}
	cases := []testCase{
		{1, "main", "code-reviewer"},
		{1, "main", "researcher"},
		{1, "main/sub", "code-reviewer"},
		{2, "main", "code-reviewer"},
		{42, "main", "planner"},
	}
	for _, c := range cases {
		id := deriveSubAgentTenantID(c.parentTenantID, c.parentAgentID, c.roleName)
		if ids[id] {
			t.Errorf("collision detected for tenantID %d", id)
		}
		ids[id] = true
	}
}

func TestSubAgentHumanBlockSenderID(t *testing.T) {
	cases := []struct {
		parentAgentID string
		expected      string
	}{
		{"main", "agent:main"},
		{"main/code-reviewer", "agent:main/code-reviewer"},
		{"", "agent:"},
	}
	for _, c := range cases {
		got := subAgentHumanBlockSenderID(c.parentAgentID)
		if got != c.expected {
			t.Errorf("subAgentHumanBlockSenderID(%q) = %q, want %q", c.parentAgentID, got, c.expected)
		}
	}
}
