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
func connectToServer(serverURL, userID, authToken string) (*websocket.Conn, error) {
	u, err := url.Parse(serverURL)
	if err != nil {
		return nil, fmt.Errorf("parse server URL: %w", err)
	}

	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("dial server: %w", err)
	}

	// Send registration wrapped in RunnerMessage envelope.
	// Server expects RunnerMessage{Type: "register", Body: <RegisterRequest JSON>}.
	regBody, _ := json.Marshal(RegisterRequest{
		UserID:    userID,
		AuthToken: authToken,
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

	return conn, nil
}

// sendRegistration sends an updated registration message (with HTTP address).
func sendRegistration(conn *websocket.Conn, userID, authToken, httpAddr string) error {
	regBody, _ := json.Marshal(RegisterRequest{
		UserID:    userID,
		HTTPAddr:  httpAddr,
		AuthToken: authToken,
	})
	msg, _ := json.Marshal(RunnerMessage{
		Type:   "register",
		UserID: userID,
		Body:   regBody,
	})
	return conn.WriteMessage(websocket.TextMessage, msg)
}

// runReadLoop reads messages from the server and dispatches to handlers.
func runReadLoop(conn *websocket.Conn, workspace string, done chan struct{}, writeMu *sync.Mutex) {
	defer close(done)
	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			log.Printf("Read error: %v", err)
			return
		}

		var msg RunnerMessage
		if err := json.Unmarshal(message, &msg); err != nil {
			log.Printf("Invalid message: %v", err)
			continue
		}

		resp := handleRequest(msg, workspace)
		data, _ := json.Marshal(resp)
		writeMu.Lock()
		err = conn.WriteMessage(websocket.TextMessage, data)
		writeMu.Unlock()
		if err != nil {
			log.Printf("Write error: %v", err)
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
