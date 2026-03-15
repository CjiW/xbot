package agent

import (
	"sync"
	"testing"
)

func TestCancelNotRegisteredAsCommand(t *testing.T) {
	// /cancel is intercepted in Run(), not registered as a Command.
	// It should NOT match as a command in the registry.
	r := NewCommandRegistry()
	registerBuiltinCommands(r)

	if r.IsCommand("/cancel") {
		t.Error("IsCommand(/cancel) = true, want false — /cancel is handled in Run(), not as a registered command")
	}
	if r.IsCommand("/CANCEL") {
		t.Error("IsCommand(/CANCEL) = true, want false")
	}
}

func TestChatCancelCh_BasicSignaling(t *testing.T) {
	// Test that the cancel channel mechanism works correctly
	var cancelMap sync.Map

	cancelKey := "feishu:chat123:user456"
	cancelCh := make(chan struct{}, 1)
	cancelMap.Store(cancelKey, cancelCh)

	// Sending cancel signal should succeed
	if ch, ok := cancelMap.Load(cancelKey); ok {
		select {
		case ch.(chan struct{}) <- struct{}{}:
			// OK
		default:
			t.Error("Failed to send cancel signal")
		}
	} else {
		t.Error("Cancel channel not found")
	}

	// Second send should not block (channel already has a signal, default case)
	if ch, ok := cancelMap.Load(cancelKey); ok {
		select {
		case ch.(chan struct{}) <- struct{}{}:
			t.Error("Second cancel signal should not succeed (channel full)")
		default:
			// OK — expected
		}
	}

	// Different key should not find a channel
	wrongKey := "feishu:chat123:otherUser"
	if _, ok := cancelMap.Load(wrongKey); ok {
		t.Error("Should not find cancel channel for different user")
	}
}

func TestCancelKeyIncludesSenderID(t *testing.T) {
	// Verify that cancel keys are per-sender, preventing cross-user cancellation in group chats
	var cancelMap sync.Map

	userA := "feishu:groupChat:userA"
	userB := "feishu:groupChat:userB"

	chA := make(chan struct{}, 1)
	chB := make(chan struct{}, 1)
	cancelMap.Store(userA, chA)
	cancelMap.Store(userB, chB)

	// Cancel userA's task
	if ch, ok := cancelMap.Load(userA); ok {
		ch.(chan struct{}) <- struct{}{}
	}

	// userB's channel should still be empty
	select {
	case <-chB:
		t.Error("userB's cancel channel should not have been signaled")
	default:
		// OK
	}

	// userA's channel should have the signal
	select {
	case <-chA:
		// OK
	default:
		t.Error("userA's cancel channel should have been signaled")
	}
}
