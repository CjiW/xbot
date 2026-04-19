package agent

import (
	"fmt"

	"xbot/bus"
	"xbot/tools"
)

// postOfficeAdapter wraps bus.PostOffice to implement tools.PostOfficeProvider.
// Placed in the agent package to avoid circular dependency between bus and tools.
type postOfficeAdapter struct {
	po *bus.PostOffice
}

var _ tools.PostOfficeProvider = (*postOfficeAdapter)(nil)

func newPostOfficeAdapter(po *bus.PostOffice) *postOfficeAdapter {
	return &postOfficeAdapter{po: po}
}

func (a *postOfficeAdapter) LookupMailbox(addr string) (tools.PostOfficeMailbox, bool) {
	parsed, _ := bus.ParseAddress(addr)
	mb, ok := a.po.Lookup(parsed)
	if !ok {
		return nil, false
	}
	return &mailboxAdapter{mb: mb}, true
}

func (a *postOfficeAdapter) ListMailboxAddresses() []string {
	addrs := a.po.ListMailboxes()
	result := make([]string, len(addrs))
	for i, addr := range addrs {
		result[i] = addr.String()
	}
	return result
}

func (a *postOfficeAdapter) LookupGroup(id string) (tools.PostOfficeGroup, bool) {
	g, ok := a.po.LookupGroup(id)
	if !ok {
		return nil, false
	}
	return &groupAdapter{g: g}, true
}

func (a *postOfficeAdapter) RegisterGroup(def tools.PostOfficeGroupDef) error {
	members := make(map[string]*bus.Mailbox)
	for _, addr := range def.MemberAddresses {
		parsed, _ := bus.ParseAddress(addr)
		mb, ok := a.po.Lookup(parsed)
		if !ok {
			return fmt.Errorf("member mailbox not found: %s", addr)
		}
		members[addr] = mb
	}
	coordAddr, _ := bus.ParseAddress(def.CoordinatorAddr)
	g := bus.NewGroup(def.ID, members, coordAddr, def.MaxRounds)
	return a.po.RegisterGroup(g)
}

func (a *postOfficeAdapter) UnregisterGroup(id string) {
	a.po.UnregisterGroup(id)
}

// mailboxAdapter wraps bus.Mailbox to implement tools.PostOfficeMailbox.
type mailboxAdapter struct {
	mb *bus.Mailbox
}

var _ tools.PostOfficeMailbox = (*mailboxAdapter)(nil)

func (m *mailboxAdapter) OwnerAddress() string { return m.mb.Owner.String() }
func (m *mailboxAdapter) InboxLen() int         { return m.mb.InboxLen() }
func (m *mailboxAdapter) IsClosed() bool        { return m.mb.IsClosed() }

// groupAdapter wraps bus.Group to implement tools.PostOfficeGroup.
type groupAdapter struct {
	g *bus.Group
}

var _ tools.PostOfficeGroup = (*groupAdapter)(nil)

func (g *groupAdapter) GroupID() string         { return g.g.ID }
func (g *groupAdapter) MemberAddresses() []string {
	members := g.g.Members()
	result := make([]string, len(members))
	for i, m := range members {
		result[i] = m.String()
	}
	return result
}
func (g *groupAdapter) CurrentRound() int       { return g.g.Round() }
func (g *groupAdapter) MaxRounds() int           { return g.g.MaxRounds }
func (g *groupAdapter) IsClosed() bool           { return g.g.IsClosed() }
func (g *groupAdapter) Broadcast(fromAddr string, content string) error {
	from, _ := bus.ParseAddress(fromAddr)
	return g.g.Broadcast(from, content)
}
func (g *groupAdapter) Close(reason string) { g.g.Close(reason) }
