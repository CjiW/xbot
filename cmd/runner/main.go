package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/gorilla/websocket"
)

var (
	flagServer      = flag.String("server", "", "WebSocket server URL (required)")
	flagToken       = flag.String("token", "", "Auth token (required)")
	flagWorkspace   = flag.String("workspace", "/workspace", "Workspace root directory")
	flagUserID      = flag.String("user-id", "", "User ID (auto-detected from --server URL)")
	flagFullControl = flag.Bool("full-control", false, "Disable path restrictions (allow access to any file)")
	flagVerbose     = flag.Bool("v", false, "Verbose logging (log all requests)")
)

var verboseLog bool

func main() {
	flag.Parse()
	verboseLog = *flagVerbose
	fullControl = *flagFullControl

	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)

	if *flagServer == "" {
		log.Fatal("--server is required")
	}
	if *flagToken == "" {
		log.Fatal("--token is required")
	}

	userID := *flagUserID
	if userID == "" {
		if idx := strings.LastIndex(*flagServer, "/"); idx > 0 {
			userID = (*flagServer)[idx+1:]
		}
	}
	if userID == "" {
		log.Fatal("--user-id is required (or embed in server URL)")
	}

	log.Printf("Starting xbot-runner  server=%s  user=%s  workspace=%s  full-control=%v", *flagServer, userID, *flagWorkspace, *flagFullControl)

	// Connect to WebSocket server
	serverURL := *flagServer
	if !strings.Contains(serverURL, "://") {
		serverURL = "ws://" + serverURL
	}

	conn, err := connectToServer(serverURL, userID, *flagToken, *flagWorkspace)
	if err != nil {
		log.Fatalf("Failed to connect to server: %v", err)
	}
	defer conn.Close()
	log.Printf("Connected to server, registered as user=%s", userID)

	var writeMu sync.Mutex

	stopHeartbeat := make(chan struct{})
	go runHeartbeat(conn, stopHeartbeat, &writeMu)

	done := make(chan struct{})
	go runReadLoop(conn, *flagWorkspace, done, &writeMu)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Printf("Received signal %v, shutting down...", sig)
	close(stopHeartbeat)
	writeMu.Lock()
	conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"disconnect"}`))
	conn.Close()
	writeMu.Unlock()
	<-done
	log.Printf("Shutdown complete")
}
