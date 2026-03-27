package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"time"

	"github.com/gorilla/websocket"
)

const (
	pingPeriod = 30 * time.Second
	pongWait   = 60 * time.Second
	writeWait  = 10 * time.Second
)

// writeMsg is a message to be sent by the single writer goroutine.
type writeMsg struct {
	data []byte
	err  chan error // non-nil for control messages that need error reporting
}

// connectToServer establishes WebSocket connection and sends registration.
func connectToServer(serverURL, userID, authToken, workspace string) (*websocket.Conn, error) {
	u, err := url.Parse(serverURL)
	if err != nil {
		return nil, fmt.Errorf("parse server URL: %w", err)
	}

	log.Printf("Dialing server %s ...", u.String())
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("dial server: %w", err)
	}

	// Set up pong handler — resets read deadline on each pong from server.
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})
	conn.SetReadDeadline(time.Now().Add(pongWait))

	regBody, _ := json.Marshal(RegisterRequest{
		UserID:    userID,
		AuthToken: authToken,
		Workspace: workspace,
	})
	regMsg, _ := json.Marshal(RunnerMessage{
		Type:   "register",
		UserID: userID,
		Body:   regBody,
	})
	if err := conn.WriteMessage(websocket.TextMessage, regMsg); err != nil {
		conn.Close()
		return nil, fmt.Errorf("send registration: %w", err)
	}

	// Wait for server acknowledgment or rejection
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("waiting for registration response: %w", err)
	}
	var resp RunnerMessage
	if err := json.Unmarshal(raw, &resp); err != nil {
		conn.Close()
		return nil, fmt.Errorf("invalid registration response: %w", err)
	}
	if resp.Type == "error" {
		var e ErrorResponse
		json.Unmarshal(resp.Body, &e)
		conn.Close()
		return nil, fmt.Errorf("registration rejected: %s", e.Message)
	}

	// Reset read deadline to pongWait for normal operation
	conn.SetReadDeadline(time.Now().Add(pongWait))

	log.Printf("Registration sent  user=%s  workspace=%s", userID, workspace)
	return conn, nil
}

// writePump is the single goroutine that writes to the WebSocket connection.
// All writes (responses, heartbeats) go through writeCh to avoid concurrent writes.
func writePump(conn *websocket.Conn, writeCh <-chan writeMsg, stop <-chan struct{}, done chan<- struct{}) {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		conn.Close()
		close(done)
	}()

	for {
		select {
		case msg := <-writeCh:
			if msg.err != nil {
				// control message (ping) — use WriteControl
				err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeWait))
				msg.err <- err
			} else {
				err := conn.WriteMessage(websocket.TextMessage, msg.data)
				if err != nil {
					log.Printf("WebSocket write error: %v", err)
					return
				}
			}
		case <-ticker.C:
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeWait)); err != nil {
				log.Printf("Ping failed: %v", err)
				return
			}
		case <-stop:
			return
		}
	}
}

// runReadLoop reads messages from the server and dispatches to handlers.
// Responses are sent via writeCh to the single writer goroutine.
func runReadLoop(conn *websocket.Conn, writeCh chan<- writeMsg, writeDone <-chan struct{}) {
	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Printf("WebSocket read error: %v", err)
			} else {
				log.Printf("WebSocket closed: %v", err)
			}
			return
		}

		var msg RunnerMessage
		if err := json.Unmarshal(message, &msg); err != nil {
			log.Printf("Invalid message from server: %v", err)
			continue
		}

		if verboseLog {
			log.Printf("→ %s [id=%s]", msg.Type, msg.ID)
		}

		resp := handleRequest(msg)
		data, _ := json.Marshal(resp)
		select {
		case writeCh <- writeMsg{data: data}:
		case <-writeDone:
			return
		}
	}
}
