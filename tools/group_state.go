package tools

import (
	"fmt"
	"sync"
	"time"
)

// GroupState manages a meeting-style group chat.
// The moderator controls who speaks via @mentions.
// All members can see the full discussion history, but only @mentioned agents respond.
type GroupState struct {
	ID        string
	Moderator string   // creator address (e.g., "main")
	Members   []string // all member addresses (e.g., ["agent:reviewer/r1", "agent:tester/t1"])
	MaxRounds int
	Round     int
	Closed    bool
	Messages  []GroupMessage
	mu        sync.Mutex
}

// GroupMessage is a single message in the group discussion.
type GroupMessage struct {
	Sender    string    // who sent this message
	Content   string    // message content
	Timestamp time.Time // when it was sent
	IsSystem  bool      // true for system messages (join, close, etc.)
}

// groupStore holds active group chats.
var groupStore sync.Map // "group:<id>" -> *GroupState

// CreateGroupState creates a new group and stores it.
func CreateGroupState(id, moderator string, members []string, maxRounds int) *GroupState {
	gs := &GroupState{
		ID:        id,
		Moderator: moderator,
		Members:   members,
		MaxRounds: maxRounds,
		Messages: []GroupMessage{
			{
				Sender:    "system",
				Content:   fmt.Sprintf("Group created. Moderator: %s, Members: %v", moderator, members),
				Timestamp: time.Now(),
				IsSystem:  true,
			},
		},
	}
	groupStore.Store("group:"+id, gs)
	return gs
}

// GetGroupState retrieves a group by name (e.g., "group:g1").
func GetGroupState(name string) (*GroupState, bool) {
	v, ok := groupStore.Load(name)
	if !ok {
		return nil, false
	}
	gs, ok := v.(*GroupState)
	if !ok {
		return nil, false
	}
	return gs, true
}

// DeleteGroupState removes a group from the store.
func DeleteGroupState(name string) {
	groupStore.Delete(name)
}

// AddMessage adds a message to the group history. Returns the round number.
func (g *GroupState) AddMessage(sender, content string, isSystem bool) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	msg := GroupMessage{
		Sender:    sender,
		Content:   content,
		Timestamp: time.Now(),
		IsSystem:  isSystem,
	}
	g.Messages = append(g.Messages, msg)
	return len(g.Messages)
}

// GetHistory returns the formatted discussion history for agent context.
func (g *GroupState) GetHistory() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	result := fmt.Sprintf("=== Group Discussion: %s ===\n", g.ID)
	result += fmt.Sprintf("Members: %v\n\n", g.Members)
	for _, msg := range g.Messages {
		if msg.IsSystem {
			result += fmt.Sprintf("[System] %s\n", msg.Content)
		} else {
			result += fmt.Sprintf("[%s]: %s\n", msg.Sender, msg.Content)
		}
	}
	result += "\n=== End of History ==="
	return result
}

// Close marks the group as closed.
func (g *GroupState) Close(reason string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.Closed = true
	g.Messages = append(g.Messages, GroupMessage{
		Sender:    "system",
		Content:   fmt.Sprintf("Group closed: %s", reason),
		Timestamp: time.Now(),
		IsSystem:  true,
	})
}

// IsMember checks if an address is a group member.
func (g *GroupState) IsMember(addr string) bool {
	for _, m := range g.Members {
		if m == addr {
			return true
		}
	}
	return false
}
