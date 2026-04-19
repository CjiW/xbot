package channel

import (
	"fmt"
	"sync"
	"sync/atomic"

	"xbot/bus"
	log "xbot/logger"
)

// GroupChannel is a Channel that broadcasts messages to all members.
// Members are identified by their channel name (registered in Dispatcher).
// The coordinator controls the group lifecycle and can terminate it.
//
// Anti-storm mechanisms:
//   - MaxRounds limits total conversation rounds
//   - Close() can be called by coordinator at any time
//
// Thread safety: All public methods are mutex-guarded. Send() uses
// non-blocking sends to members to prevent re-entrant deadlock
// (where a member agent tries to send back to the group while processing
// a group message, causing mutual blocking).
type GroupChannel struct {
	id         string
	name       string            // "group:roundtable"
	members    map[string]string // member address → channel name in Dispatcher
	coordAddr  string            // coordinator address
	maxRounds  int
	roundCount atomic.Int32
	closed     atomic.Bool
	dispatcher *Dispatcher
	mu         sync.Mutex
}

// NewGroupChannel creates a new group chat channel.
func NewGroupChannel(id, coordAddr string, members map[string]string, maxRounds int, dispatcher *Dispatcher) *GroupChannel {
	return &GroupChannel{
		id:         id,
		name:       "group:" + id,
		members:    members,
		coordAddr:  coordAddr,
		maxRounds:  maxRounds,
		dispatcher: dispatcher,
	}
}

// Name returns the channel name (e.g., "group:roundtable").
func (g *GroupChannel) Name() string { return g.name }

// Start is a no-op for groups.
func (g *GroupChannel) Start() error { return nil }

// Stop closes the group.
func (g *GroupChannel) Stop() { g.Close("stopped") }

// Send broadcasts the message to all members via Dispatcher.
// Uses fire-and-forget semantics: errors are logged but don't block.
func (g *GroupChannel) Send(msg bus.OutboundMessage) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.closed.Load() {
		return "", fmt.Errorf("group %s is closed", g.name)
	}

	formatted := fmt.Sprintf("[Group %s] %s", g.id, msg.Content)
	sent := 0
	for memberAddr, channelName := range g.members {
		outMsg := bus.OutboundMessage{
			Channel: channelName,
			ChatID:  memberAddr,
			Content: formatted,
		}
		if _, err := g.dispatcher.SendDirect(outMsg); err != nil {
			log.WithError(err).WithField("member", memberAddr).Warn("Failed to broadcast to member")
		} else {
			sent++
		}
	}

	if sent == 0 {
		return "", fmt.Errorf("group %s: failed to deliver to any member", g.name)
	}

	// Count round only after successful send to at least one member
	round := int(g.roundCount.Add(1))
	if g.maxRounds > 0 && round >= g.maxRounds {
		g.closeLocked(fmt.Sprintf("max rounds (%d) reached", g.maxRounds))
		return fmt.Sprintf("broadcast to %d members (final round %d/%d)", sent, round, g.maxRounds), nil
	}

	return fmt.Sprintf("broadcast to %d members (round %d)", sent, round), nil
}

// Close terminates the group and notifies members.
func (g *GroupChannel) Close(reason string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closeLocked(reason)
}

// closeLocked is the internal close implementation. Caller must hold g.mu.
func (g *GroupChannel) closeLocked(reason string) {
	if g.closed.Swap(true) {
		return
	}
	if g.dispatcher == nil {
		return
	}
	closeMsg := fmt.Sprintf("[Group %s CLOSED] %s", g.id, reason)
	for memberAddr, channelName := range g.members {
		outMsg := bus.OutboundMessage{
			Channel: channelName,
			ChatID:  memberAddr,
			Content: closeMsg,
		}
		_, _ = g.dispatcher.SendDirect(outMsg)
	}
	g.dispatcher.Unregister(g.name)
}

// IsClosed reports whether the group is closed.
func (g *GroupChannel) IsClosed() bool { return g.closed.Load() }

// Round returns the current round number.
func (g *GroupChannel) Round() int { return int(g.roundCount.Load()) }
