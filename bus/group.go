package bus

import (
	"fmt"
	"sync"
	"sync/atomic"
)

// Group represents a group chat among multiple Agents.
// Messages from any member are broadcast to all other members.
// A Coordinator agent controls the group lifecycle and convergence.
//
// Anti-storm mechanisms:
//   - MaxRounds limits total conversation rounds
//   - Each member can speak at most once per round
//   - Coordinator can terminate the group at any time
type Group struct {
	// ID is the unique group identifier.
	ID string
	// Addr is the group's address: agent://group/{ID}
	Addr Address
	// Coordinator is the agent that created and controls the group.
	Coordinator Address
	// MaxRounds is the maximum number of conversation rounds (0 = unlimited, but NOT recommended).
	MaxRounds int

	members    map[string]*Mailbox // key: Address.String() → member mailbox
	history    []GroupMessage
	roundCount atomic.Int32
	speakCount map[string]int32 // per-round speak count per member
	mu         sync.Mutex
	closed     atomic.Bool
}

// GroupMessage is a single message in a group conversation.
type GroupMessage struct {
	From    Address
	Content string
	Round   int
}

// NewGroup creates a new group chat.
// members is a map of member address → their Mailbox.
// coordinator is the Address of the coordinating agent.
// maxRounds limits conversation rounds (recommended: 10).
func NewGroup(id string, members map[string]*Mailbox, coordinator Address, maxRounds int) *Group {
	g := &Group{
		ID:          id,
		Addr:        Address{Scheme: SchemeAgent, Domain: "group", ID: id},
		Coordinator: coordinator,
		MaxRounds:   maxRounds,
		members:     make(map[string]*Mailbox),
		history:     make([]GroupMessage, 0),
		speakCount:  make(map[string]int32),
	}
	for addr, mb := range members {
		g.members[addr] = mb
	}
	return g
}

// Members returns the addresses of all group members.
func (g *Group) Members() []Address {
	g.mu.Lock()
	defer g.mu.Unlock()
	addrs := make([]Address, 0, len(g.members))
	for s, mb := range g.members {
		_ = s
		addrs = append(addrs, mb.Owner)
	}
	return addrs
}

// History returns a copy of the group's message history.
func (g *Group) History() []GroupMessage {
	g.mu.Lock()
	defer g.mu.Unlock()
	cp := make([]GroupMessage, len(g.history))
	copy(cp, g.history)
	return cp
}

// Round returns the current round number (1-based).
func (g *Group) Round() int {
	return int(g.roundCount.Load())
}

// Broadcast sends a message from one member to all other members.
// Returns error if:
//   - the group is closed
//   - max rounds exceeded
//   - the sender has already spoken this round (per-round limit)
//   - the sender is not a member
func (g *Group) Broadcast(from Address, content string) error {
	if g.closed.Load() {
		return fmt.Errorf("group %s is closed", g.ID)
	}

	g.mu.Lock()

	// Check membership
	fromKey := from.String()
	if _, ok := g.members[fromKey]; !ok {
		g.mu.Unlock()
		return fmt.Errorf("sender %s is not a member of group %s", from, g.ID)
	}

	round := int(g.roundCount.Load()) + 1

	// Check max rounds
	if g.MaxRounds > 0 && round > g.MaxRounds {
		g.mu.Unlock()
		return fmt.Errorf("group %s exceeded max rounds (%d)", g.ID, g.MaxRounds)
	}

	// Per-round speak limit: each member can speak at most once per round
	// speakCount resets when a new round starts (detected by all members having spoken or coordinator speak)
	if g.speakCount == nil {
		g.speakCount = make(map[string]int32)
	}
	if g.speakCount[fromKey] > 0 {
		g.mu.Unlock()
		return fmt.Errorf("member %s already spoke this round in group %s", from.ID, g.ID)
	}

	// Advance round if this is the first speaker of a new round
	// (round advances when any member speaks after a reset)
	if round > int(g.roundCount.Load()) {
		g.roundCount.Store(int32(round))
		// Reset speak counts for the new round
		for k := range g.speakCount {
			g.speakCount[k] = 0
		}
	}
	g.speakCount[fromKey]++

	// Record in history
	msg := GroupMessage{
		From:    from,
		Content: content,
		Round:   round,
	}
	g.history = append(g.history, msg)

	// Collect targets (all members except sender)
	targets := make([]*Mailbox, 0, len(g.members)-1)
	for key, mb := range g.members {
		if key != fromKey {
			targets = append(targets, mb)
		}
	}
	g.mu.Unlock()

	// Deliver to all other members (non-blocking)
	formattedContent := fmt.Sprintf("[Group %s] %s: %s", g.ID, from.ID, content)
	var firstErr error
	for _, mb := range targets {
		if err := mb.Send(InboundMessage{
			From:    from,
			To:      mb.Owner,
			Content: formattedContent,
			Channel: SchemeAgent,
			Metadata: map[string]string{
				"group_id":     g.ID,
				"group_round":  fmt.Sprintf("%d", round),
				"group_action": "broadcast",
			},
		}); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// CoordinatorBroadcast sends a message from the coordinator to all members.
// Unlike Broadcast, this does NOT count toward per-round speak limits
// and does NOT advance the round counter. Use for coordination messages
// like "next round topic" or "summarize so far".
func (g *Group) CoordinatorBroadcast(content string) error {
	if g.closed.Load() {
		return fmt.Errorf("group %s is closed", g.ID)
	}

	g.mu.Lock()
	round := int(g.roundCount.Load())
	// Reset speak counts for next round
	for k := range g.speakCount {
		g.speakCount[k] = 0
	}

	// Record in history
	g.history = append(g.history, GroupMessage{
		From:    g.Coordinator,
		Content: content,
		Round:   round,
	})

	targets := make([]*Mailbox, 0, len(g.members))
	for key, mb := range g.members {
		_ = key
		targets = append(targets, mb)
	}
	g.mu.Unlock()

	formattedContent := fmt.Sprintf("[Group %s] Coordinator: %s", g.ID, content)
	var firstErr error
	for _, mb := range targets {
		if err := mb.Send(InboundMessage{
			From:    g.Coordinator,
			To:      mb.Owner,
			Content: formattedContent,
			Channel: SchemeAgent,
			Metadata: map[string]string{
				"group_id":     g.ID,
				"group_round":  fmt.Sprintf("%d", round),
				"group_action": "coordinator_broadcast",
			},
		}); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Close terminates the group and notifies all members.
func (g *Group) Close(reason string) {
	if g.closed.Swap(true) {
		return // already closed
	}

	g.mu.Lock()
	targets := make([]*Mailbox, 0, len(g.members))
	for key, mb := range g.members {
		_ = key
		targets = append(targets, mb)
	}
	g.mu.Unlock()

	closeMsg := fmt.Sprintf("[Group %s CLOSED] Reason: %s", g.ID, reason)
	for _, mb := range targets {
		_ = mb.Send(InboundMessage{
			From:    g.Coordinator,
			To:      mb.Owner,
			Content: closeMsg,
			Channel: SchemeAgent,
			Metadata: map[string]string{
				"group_id":     g.ID,
				"group_action": "close",
			},
		})
	}
}

// IsClosed reports whether the group has been closed.
func (g *Group) IsClosed() bool {
	return g.closed.Load()
}
