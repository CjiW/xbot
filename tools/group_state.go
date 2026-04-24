package tools

import "sync"

// GroupMembership defines a virtual group chat — it's just a set of agent members.
// There is NO separate message store. Each agent already has its own session
// with full message history. The group only constrains which agents can
// communicate: agents in the same group can SendMessage directly to each other.
//
// This is a deliberate simplification over the old GroupState model which
// duplicated agent session functionality (message store, history formatting).
type GroupMembership struct {
	ID      string   // e.g. "g1"
	Name    string   // e.g. "group:g1"
	Members []string // agent addresses e.g. ["agent:reviewer/r1", "agent:tester/t1"]
	Closed  bool
}

// groupStore holds active group memberships.
var groupStore sync.Map // "group:<id>" -> *GroupMembership

// CreateGroup creates a new group membership and stores it.
func CreateGroup(id string, members []string) *GroupMembership {
	gm := &GroupMembership{
		ID:      id,
		Name:    "group:" + id,
		Members: members,
	}
	groupStore.Store(gm.Name, gm)
	return gm
}

// GetGroup retrieves a group membership by name (e.g., "group:g1").
func GetGroup(name string) (*GroupMembership, bool) {
	v, ok := groupStore.Load(name)
	if !ok {
		return nil, false
	}
	gm, ok := v.(*GroupMembership)
	if !ok {
		return nil, false
	}
	return gm, true
}

// DeleteGroup removes a group from the store.
func DeleteGroup(name string) {
	groupStore.Delete(name)
}

// Close marks the group as closed.
func (g *GroupMembership) Close() {
	g.Closed = true
}

// IsMember checks if an address is in this group.
func (g *GroupMembership) IsMember(addr string) bool {
	for _, m := range g.Members {
		if m == addr {
			return true
		}
	}
	return false
}

// RemoveMember removes an address from the group. Returns true if the member was found.
// If no members remain, the group is deleted from the store.
func RemoveMember(groupName, memberAddr string) bool {
	gm, ok := GetGroup(groupName)
	if !ok {
		return false
	}
	for i, m := range gm.Members {
		if m == memberAddr {
			gm.Members = append(gm.Members[:i], gm.Members[i+1:]...)
			break
		}
	}
	if len(gm.Members) == 0 {
		DeleteGroup(groupName)
	}
	return true
}

// GroupSummary is a lightweight snapshot for CLI listing.
type GroupSummary struct {
	ID      string
	Name    string
	Members []string
	Closed  bool
}

// ListGroups returns info about all active and closed groups.
func ListGroups() []GroupSummary {
	var results []GroupSummary
	groupStore.Range(func(key, value any) bool {
		gm, ok := value.(*GroupMembership)
		if !ok {
			return true
		}
		results = append(results, GroupSummary{
			ID:      gm.ID,
			Name:    gm.Name,
			Members: append([]string{}, gm.Members...),
			Closed:  gm.Closed,
		})
		return true
	})
	return results
}
