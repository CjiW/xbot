package bus

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
)

// Mailbox is a named message inbox for an Agent instance.
// Every Agent (main or SubAgent) can have a Mailbox registered with the PostOffice,
// allowing other Agents to send messages directly to it.
//
// Usage:
//   - Oneshot SubAgent: create Mailbox → Register → Send task → WaitReply → Unregister
//   - Interactive SubAgent: Register Mailbox → others Deliver to it → Unload when done
//   - Main Agent: typically does NOT register a Mailbox (receives IM messages via Bus.Inbound)
type Mailbox struct {
	// Owner is the address of the Agent that owns this mailbox.
	Owner Address

	inbox chan InboundMessage  // buffered incoming messages
	reply chan OutboundMessage // buffered reply messages
	closed atomic.Bool
}

// NewMailbox creates a new Mailbox for the given owner address.
func NewMailbox(owner Address) *Mailbox {
	return &Mailbox{
		Owner: owner,
		inbox: make(chan InboundMessage, 64),
		reply: make(chan OutboundMessage, 16),
	}
}

// Send delivers a message to this Mailbox's inbox.
// Non-blocking: returns error if inbox is full.
func (m *Mailbox) Send(msg InboundMessage) error {
	if m.closed.Load() {
		return fmt.Errorf("mailbox %s is closed", m.Owner)
	}
	select {
	case m.inbox <- msg:
		return nil
	default:
		return fmt.Errorf("mailbox %s inbox full", m.Owner)
	}
}

// Receive blocks until a message arrives or context is cancelled.
func (m *Mailbox) Receive(ctx context.Context) (InboundMessage, error) {
	select {
	case msg, ok := <-m.inbox:
		if !ok {
			return InboundMessage{}, fmt.Errorf("mailbox %s closed", m.Owner)
		}
		return msg, nil
	case <-ctx.Done():
		return InboundMessage{}, ctx.Err()
	}
}

// TryReceive attempts to receive without blocking.
// Returns the message and true if available, zero value and false otherwise.
func (m *Mailbox) TryReceive() (InboundMessage, bool) {
	select {
	case msg, ok := <-m.inbox:
		return msg, ok
	default:
		return InboundMessage{}, false
	}
}

// Reply sends a completion reply from this Mailbox's agent.
func (m *Mailbox) Reply(msg OutboundMessage) error {
	if m.closed.Load() {
		return fmt.Errorf("mailbox %s is closed", m.Owner)
	}
	select {
	case m.reply <- msg:
		return nil
	default:
		return fmt.Errorf("mailbox %s reply channel full", m.Owner)
	}
}

// WaitReply blocks until a reply arrives or context is cancelled.
func (m *Mailbox) WaitReply(ctx context.Context) (OutboundMessage, error) {
	select {
	case msg, ok := <-m.reply:
		if !ok {
			return OutboundMessage{}, fmt.Errorf("mailbox %s reply closed", m.Owner)
		}
		return msg, nil
	case <-ctx.Done():
		return OutboundMessage{}, ctx.Err()
	}
}

// Close marks the mailbox as closed and drains channels.
func (m *Mailbox) Close() {
	m.closed.Store(true)
	close(m.inbox)
	close(m.reply)
}

// IsClosed reports whether the mailbox has been closed.
func (m *Mailbox) IsClosed() bool {
	return m.closed.Load()
}

// InboxLen returns the number of pending messages in the inbox.
func (m *Mailbox) InboxLen() int {
	return len(m.inbox)
}

// PostOffice manages Agent-to-Agent message routing.
// It is independent of the IM MessageBus — PostOffice routes between Agents,
// while MessageBus routes between IM channels and the main Agent.
//
// Initialize in main.go:
//
//	postOffice := bus.NewPostOffice()
//	agent.SetPostOffice(postOffice)
type PostOffice struct {
	mailboxes map[string]*Mailbox // key: Address.String()
	groups    map[string]*Group   // key: group ID
	mu        sync.RWMutex
}

// NewPostOffice creates a new PostOffice.
func NewPostOffice() *PostOffice {
	return &PostOffice{
		mailboxes: make(map[string]*Mailbox),
		groups:    make(map[string]*Group),
	}
}

// Register adds a Mailbox to the routing table.
// Returns error if the address is already registered.
func (po *PostOffice) Register(mb *Mailbox) error {
	key := mb.Owner.String()
	po.mu.Lock()
	defer po.mu.Unlock()
	if _, exists := po.mailboxes[key]; exists {
		return fmt.Errorf("mailbox already registered: %s", key)
	}
	po.mailboxes[key] = mb
	return nil
}

// Unregister removes a Mailbox from the routing table and closes it.
func (po *PostOffice) Unregister(addr Address) {
	key := addr.String()
	po.mu.Lock()
	mb, ok := po.mailboxes[key]
	if ok {
		delete(po.mailboxes, key)
	}
	po.mu.Unlock()
	if ok && !mb.IsClosed() {
		mb.Close()
	}
}

// Deliver sends a message to the target Agent's Mailbox.
// The target is determined by msg.To address.
func (po *PostOffice) Deliver(msg InboundMessage) error {
	po.mu.RLock()
	mb, ok := po.mailboxes[msg.To.String()]
	po.mu.RUnlock()
	if !ok {
		return fmt.Errorf("agent not found: %s", msg.To)
	}
	return mb.Send(msg)
}

// Lookup checks if an Agent is registered.
func (po *PostOffice) Lookup(addr Address) (*Mailbox, bool) {
	po.mu.RLock()
	mb, ok := po.mailboxes[addr.String()]
	po.mu.RUnlock()
	return mb, ok
}

// ListMailboxes returns all registered mailbox addresses.
func (po *PostOffice) ListMailboxes() []Address {
	po.mu.RLock()
	defer po.mu.RUnlock()
	addrs := make([]Address, 0, len(po.mailboxes))
	for _, mb := range po.mailboxes {
		addrs = append(addrs, mb.Owner)
	}
	return addrs
}

// RegisterGroup adds a Group to the routing table.
func (po *PostOffice) RegisterGroup(g *Group) error {
	po.mu.Lock()
	defer po.mu.Unlock()
	if _, exists := po.groups[g.ID]; exists {
		return fmt.Errorf("group already registered: %s", g.ID)
	}
	po.groups[g.ID] = g
	return nil
}

// UnregisterGroup removes a Group from the routing table.
func (po *PostOffice) UnregisterGroup(id string) {
	po.mu.Lock()
	delete(po.groups, id)
	po.mu.Unlock()
}

// LookupGroup finds a group by ID.
func (po *PostOffice) LookupGroup(id string) (*Group, bool) {
	po.mu.RLock()
	g, ok := po.groups[id]
	po.mu.RUnlock()
	return g, ok
}

// ListGroups returns all registered group IDs.
func (po *PostOffice) ListGroups() []string {
	po.mu.RLock()
	defer po.mu.RUnlock()
	ids := make([]string, 0, len(po.groups))
	for id := range po.groups {
		ids = append(ids, id)
	}
	return ids
}
