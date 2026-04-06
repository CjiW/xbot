package channel

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// WSChannelBase provides shared WebSocket infrastructure for QQ and NapCat channels.
type WSChannelBase struct {
	conn             *websocket.Conn
	connMu           sync.Mutex
	stopCh           chan struct{}
	processedIDs     map[string]struct{}
	processedOrder   []string
	processedMu      sync.Mutex
	maxProcessed     int
	disconnectTimes  []time.Time
	disconnectMu     sync.Mutex
	maxDisconnectAge time.Duration
	maxDisconnects   int
}

// NewWSChannelBase creates a WSChannelBase with the given configuration.
func NewWSChannelBase(maxProcessed int, maxDisconnectAge time.Duration, maxDisconnects int) WSChannelBase {
	return WSChannelBase{
		processedIDs:     make(map[string]struct{}),
		maxProcessed:     maxProcessed,
		maxDisconnectAge: maxDisconnectAge,
		maxDisconnects:   maxDisconnects,
	}
}

// ---------------------------------------------------------------------------
// Deduplication
// ---------------------------------------------------------------------------

// isDuplicate checks if a message ID has been seen before.
func (b *WSChannelBase) isDuplicate(messageID string) bool {
	b.processedMu.Lock()
	defer b.processedMu.Unlock()

	if _, exists := b.processedIDs[messageID]; exists {
		return true
	}

	b.processedIDs[messageID] = struct{}{}
	b.processedOrder = append(b.processedOrder, messageID)

	// Evict oldest entries when over capacity
	for len(b.processedOrder) > b.maxProcessed {
		oldest := b.processedOrder[0]
		b.processedOrder = b.processedOrder[1:]
		delete(b.processedIDs, oldest)
	}
	return false
}

// ---------------------------------------------------------------------------
// Access control
// ---------------------------------------------------------------------------

// isAllowed checks if a sender ID is in the allow list.
// An empty allow list means everyone is allowed.
func (b *WSChannelBase) isAllowed(allowList []string, senderID string) bool {
	if len(allowList) == 0 {
		return true
	}
	for _, allowed := range allowList {
		if allowed == senderID {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Wait / stop
// ---------------------------------------------------------------------------

// sleepOrStop waits for the given duration or until stopCh is closed.
// Returns true if the wait completed, false if interrupted.
func (b *WSChannelBase) sleepOrStop(d time.Duration) bool {
	select {
	case <-time.After(d):
		return true
	case <-b.stopCh:
		return false
	}
}

// ---------------------------------------------------------------------------
// WebSocket send / close
// ---------------------------------------------------------------------------

// wsSend sends a JSON payload over the WebSocket connection.
func (b *WSChannelBase) wsSend(payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal ws payload: %w", err)
	}

	b.connMu.Lock()
	defer b.connMu.Unlock()

	if b.conn == nil {
		return fmt.Errorf("no ws connection")
	}
	return b.conn.WriteMessage(websocket.TextMessage, data)
}

// closeConn closes the WebSocket connection if open.
func (b *WSChannelBase) closeConn() {
	b.connMu.Lock()
	defer b.connMu.Unlock()

	if b.conn != nil {
		b.conn.Close()
		b.conn = nil
	}
}

// ---------------------------------------------------------------------------
// Quick disconnect detection
// ---------------------------------------------------------------------------

// recordDisconnect records a disconnect event for quick-disconnect detection.
func (b *WSChannelBase) recordDisconnect(_ time.Time) {
	b.disconnectMu.Lock()
	defer b.disconnectMu.Unlock()

	b.disconnectTimes = append(b.disconnectTimes, time.Now())

	// Keep only recent entries
	if len(b.disconnectTimes) > b.maxDisconnects*2 {
		b.disconnectTimes = b.disconnectTimes[len(b.disconnectTimes)-b.maxDisconnects*2:]
	}
}

// isQuickDisconnectLoop detects rapid disconnect loops.
func (b *WSChannelBase) isQuickDisconnectLoop() bool {
	b.disconnectMu.Lock()
	defer b.disconnectMu.Unlock()

	n := len(b.disconnectTimes)
	if n < b.maxDisconnects {
		return false
	}

	// Check if the last N disconnects all happened within maxDisconnectAge of each other
	recent := b.disconnectTimes[n-b.maxDisconnects:]
	for i := 1; i < len(recent); i++ {
		if recent[i].Sub(recent[i-1]) > b.maxDisconnectAge {
			return false
		}
	}

	// Reset after detection to avoid repeated triggers
	b.disconnectTimes = make([]time.Time, 0, 10)
	return true
}
