package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"
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

	// Send registration
	reg := RegisterRequest{
		UserID:    userID,
		AuthToken: authToken,
		// HTTPAddr will be set after HTTP server starts
	}
	regData, _ := json.Marshal(reg)
	if err := conn.WriteMessage(websocket.TextMessage, regData); err != nil {
		conn.Close()
		return nil, fmt.Errorf("send registration: %w", err)
	}

	return conn, nil
}

// sendRegistration sends an updated registration message (with HTTP address).
func sendRegistration(conn *websocket.Conn, userID, authToken, httpAddr string) error {
	reg := RegisterRequest{
		UserID:    userID,
		HTTPAddr:  httpAddr,
		AuthToken: authToken,
	}
	data, _ := json.Marshal(reg)
	return conn.WriteMessage(websocket.TextMessage, data)
}

// runReadLoop reads messages from the server and dispatches to handlers.
func runReadLoop(conn *websocket.Conn, workspace string, done chan struct{}) {
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
		if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
			log.Printf("Write error: %v", err)
			return
		}
	}
}

// runHeartbeat sends periodic ping messages to keep the connection alive.
func runHeartbeat(conn *websocket.Conn, stop chan struct{}) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
		case <-stop:
			return
		}
	}
}
