package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

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

	log.Printf("Registration sent  user=%s  workspace=%s", userID, workspace)
	return conn, nil
}

// runReadLoop reads messages from the server and dispatches to handlers.
func runReadLoop(conn *websocket.Conn, done chan struct{}, writeMu *sync.Mutex) {
	defer close(done)
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
		writeMu.Lock()
		err = conn.WriteMessage(websocket.TextMessage, data)
		writeMu.Unlock()
		if err != nil {
			log.Printf("WebSocket write error: %v", err)
			return
		}
	}
}

// runHeartbeat sends periodic ping messages to keep the connection alive.
func runHeartbeat(conn *websocket.Conn, stop chan struct{}, writeMu *sync.Mutex) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			writeMu.Lock()
			conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
			writeMu.Unlock()
		case <-stop:
			return
		}
	}
}
