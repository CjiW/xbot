package bus

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestMailboxSendReceive(t *testing.T) {
	addr := Address{Scheme: SchemeAgent, Domain: "main", ID: "reviewer"}
	mb := NewMailbox(addr)

	msg := InboundMessage{
		From:    Address{Scheme: SchemeAgent, Domain: "main"},
		To:      addr,
		Content: "hello reviewer",
	}

	// Send should succeed
	if err := mb.Send(msg); err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	// Receive should return the message
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	received, err := mb.Receive(ctx)
	if err != nil {
		t.Fatalf("Receive failed: %v", err)
	}
	if received.Content != "hello reviewer" {
		t.Errorf("Content = %q, want %q", received.Content, "hello reviewer")
	}
}

func TestMailboxReplyWaitReply(t *testing.T) {
	addr := Address{Scheme: SchemeAgent, Domain: "main", ID: "reviewer"}
	mb := NewMailbox(addr)

	reply := OutboundMessage{Content: "done"}
	if err := mb.Reply(reply); err != nil {
		t.Fatalf("Reply failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	got, err := mb.WaitReply(ctx)
	if err != nil {
		t.Fatalf("WaitReply failed: %v", err)
	}
	if got.Content != "done" {
		t.Errorf("Content = %q, want %q", got.Content, "done")
	}
}

func TestMailboxClose(t *testing.T) {
	addr := Address{Scheme: SchemeAgent, Domain: "main", ID: "reviewer"}
	mb := NewMailbox(addr)

	if mb.IsClosed() {
		t.Error("mailbox should not be closed initially")
	}
	mb.Close()
	if !mb.IsClosed() {
		t.Error("mailbox should be closed after Close()")
	}

	// Send to closed mailbox should fail
	err := mb.Send(InboundMessage{})
	if err == nil {
		t.Error("Send to closed mailbox should fail")
	}
}

func TestMailboxReceiveCancelled(t *testing.T) {
	addr := Address{Scheme: SchemeAgent, Domain: "main", ID: "reviewer"}
	mb := NewMailbox(addr)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := mb.Receive(ctx)
	if err == nil {
		t.Error("Receive on cancelled context should fail")
	}
}

func TestPostOfficeRegisterLookup(t *testing.T) {
	po := NewPostOffice()
	addr := Address{Scheme: SchemeAgent, Domain: "main", ID: "reviewer"}
	mb := NewMailbox(addr)

	// Register
	if err := po.Register(mb); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Lookup
	found, ok := po.Lookup(addr)
	if !ok {
		t.Error("Lookup should find registered mailbox")
	}
	if found.Owner.String() != addr.String() {
		t.Errorf("Owner = %q, want %q", found.Owner, addr)
	}

	// Duplicate registration should fail
	if err := po.Register(mb); err == nil {
		t.Error("duplicate registration should fail")
	}

	// Unregister
	po.Unregister(addr)
	_, ok = po.Lookup(addr)
	if ok {
		t.Error("Lookup should not find unregistered mailbox")
	}
}

func TestPostOfficeDeliver(t *testing.T) {
	po := NewPostOffice()
	addr := Address{Scheme: SchemeAgent, Domain: "main", ID: "reviewer"}
	mb := NewMailbox(addr)
	po.Register(mb)

	msg := InboundMessage{
		From:    Address{Scheme: SchemeAgent, Domain: "main"},
		To:      addr,
		Content: "task for reviewer",
	}

	if err := po.Deliver(msg); err != nil {
		t.Fatalf("Deliver failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	received, err := mb.Receive(ctx)
	if err != nil {
		t.Fatalf("Receive failed: %v", err)
	}
	if received.Content != "task for reviewer" {
		t.Errorf("Content = %q, want %q", received.Content, "task for reviewer")
	}

	// Deliver to non-existent address should fail
	err = po.Deliver(InboundMessage{
		From:    Address{Scheme: SchemeAgent, Domain: "main"},
		To:      Address{Scheme: SchemeAgent, Domain: "nonexistent"},
		Content: "nobody home",
	})
	if err == nil {
		t.Error("Deliver to non-existent address should fail")
	}
}

func TestPostOfficeListMailboxes(t *testing.T) {
	po := NewPostOffice()

	addrs := []Address{
		{Scheme: SchemeAgent, Domain: "main", ID: "a"},
		{Scheme: SchemeAgent, Domain: "main", ID: "b"},
	}
	for _, a := range addrs {
		po.Register(NewMailbox(a))
	}

	list := po.ListMailboxes()
	if len(list) != 2 {
		t.Errorf("ListMailboxes returned %d, want 2", len(list))
	}
}

func TestGroupBroadcast(t *testing.T) {
	po := NewPostOffice()

	// Register 3 members
	addrs := []Address{
		{Scheme: SchemeAgent, Domain: "main", ID: "a"},
		{Scheme: SchemeAgent, Domain: "main", ID: "b"},
		{Scheme: SchemeAgent, Domain: "main", ID: "c"},
	}
	for _, a := range addrs {
		po.Register(NewMailbox(a))
	}

	// Create group
	members := map[string]*Mailbox{}
	for _, a := range addrs {
		mb, _ := po.Lookup(a)
		members[a.String()] = mb
	}
	coordAddr := Address{Scheme: SchemeAgent, Domain: "main"}
	group := NewGroup("test-group", members, coordAddr, 5)

	// Broadcast from A → B and C should receive
	err := group.Broadcast(addrs[0], "hello from A")
	if err != nil {
		t.Fatalf("Broadcast failed: %v", err)
	}

	// B and C should each have 1 message
	ctx := context.Background()
	for i := 1; i < 3; i++ {
		mb, _ := po.Lookup(addrs[i])
		msg, err := mb.Receive(ctx)
		if err != nil {
			t.Fatalf("Member %d Receive failed: %v", i, err)
		}
		if msg.Content != "[Group test-group] a: hello from A" {
			t.Errorf("Member %d got %q, want group formatted message", i, msg.Content)
		}
	}

	// A should NOT receive (it's the sender)
	mbA, _ := po.Lookup(addrs[0])
	if mbA.InboxLen() != 0 {
		t.Error("Sender should not receive their own broadcast")
	}
}

func TestGroupMaxRounds(t *testing.T) {
	po := NewPostOffice()

	addrs := []Address{
		{Scheme: SchemeAgent, Domain: "main", ID: "a"},
		{Scheme: SchemeAgent, Domain: "main", ID: "b"},
	}
	for _, a := range addrs {
		po.Register(NewMailbox(a))
	}

	members := map[string]*Mailbox{}
	for _, a := range addrs {
		mb, _ := po.Lookup(a)
		members[a.String()] = mb
	}
	coordAddr := Address{Scheme: SchemeAgent, Domain: "main"}
	group := NewGroup("limited", members, coordAddr, 2)

	// Round 1: A speaks
	if err := group.Broadcast(addrs[0], "round 1"); err != nil {
		t.Fatalf("Broadcast round 1 failed: %v", err)
	}
	// Drain B's inbox
	mbB, _ := po.Lookup(addrs[1])
	mbB.Receive(context.Background())

	// Round 2: B speaks
	if err := group.Broadcast(addrs[1], "round 2"); err != nil {
		t.Fatalf("Broadcast round 2 failed: %v", err)
	}
	// Drain A's inbox
	mbA, _ := po.Lookup(addrs[0])
	mbA.Receive(context.Background())

	// Round 3 should fail (max rounds = 2)
	err := group.Broadcast(addrs[0], "round 3")
	if err == nil {
		t.Error("Broadcast beyond max rounds should fail")
	}
}

func TestGroupPerRoundLimit(t *testing.T) {
	po := NewPostOffice()

	addrs := []Address{
		{Scheme: SchemeAgent, Domain: "main", ID: "a"},
		{Scheme: SchemeAgent, Domain: "main", ID: "b"},
	}
	for _, a := range addrs {
		po.Register(NewMailbox(a))
	}

	members := map[string]*Mailbox{}
	for _, a := range addrs {
		mb, _ := po.Lookup(a)
		members[a.String()] = mb
	}
	coordAddr := Address{Scheme: SchemeAgent, Domain: "main"}
	group := NewGroup("perround", members, coordAddr, 10)

	// First speak should succeed
	if err := group.Broadcast(addrs[0], "first"); err != nil {
		t.Fatalf("First broadcast failed: %v", err)
	}
	// Drain B's inbox
	mbB, _ := po.Lookup(addrs[1])
	mbB.Receive(context.Background())

	// Second speak from same member in same round should fail
	err := group.Broadcast(addrs[0], "second")
	if err == nil {
		t.Error("Same member speaking twice in same round should fail")
	}
}

func TestGroupClose(t *testing.T) {
	po := NewPostOffice()

	addr := Address{Scheme: SchemeAgent, Domain: "main", ID: "a"}
	po.Register(NewMailbox(addr))

	members := map[string]*Mailbox{}
	mb, _ := po.Lookup(addr)
	members[addr.String()] = mb

	coordAddr := Address{Scheme: SchemeAgent, Domain: "main"}
	group := NewGroup("close-test", members, coordAddr, 5)

	if group.IsClosed() {
		t.Error("group should not be closed initially")
	}
	group.Close("test done")
	if !group.IsClosed() {
		t.Error("group should be closed after Close()")
	}

	// Broadcast to closed group should fail
	err := group.Broadcast(addr, "should fail")
	if err == nil {
		t.Error("Broadcast to closed group should fail")
	}

	// Double close should be idempotent
	group.Close("double close")
}

func TestMailboxConcurrent(t *testing.T) {
	addr := Address{Scheme: SchemeAgent, Domain: "main", ID: "worker"}
	// Use a small buffer mailbox to test concurrent send/receive
	mb := NewMailbox(addr)

	var received atomic.Int32
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Consumer goroutine — drains inbox continuously
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			_, err := mb.Receive(ctx)
			if err != nil {
				return
			}
			received.Add(1)
		}
	}()

	// Send messages — use TryReceive-friendly pace
	// Send 50 messages (inbox buffer is 64, so no drops)
	for i := 0; i < 50; i++ {
		mb.Send(InboundMessage{
			From:    Address{Scheme: SchemeAgent, Domain: "main"},
			To:      addr,
			Content: "msg",
		})
	}

	// Wait for consumer to finish processing
	time.Sleep(100 * time.Millisecond)
	cancel()

	<-done
	if got := received.Load(); got != 50 {
		t.Errorf("received %d messages, want 50", got)
	}
}
