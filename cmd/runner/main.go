package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/gorilla/websocket"
)

var (
	flagServer    = flag.String("server", "", "WebSocket server URL (required)")
	flagToken     = flag.String("token", "", "Auth token (required)")
	flagWorkspace = flag.String("workspace", "/workspace", "Workspace root directory")
	flagHTTPAddr  = flag.String("http-addr", ":0", "HTTP server listen address (random port)")
	flagUserID    = flag.String("user-id", "", "User ID (auto-detected from --server URL)")
)

func main() {
	flag.Parse()

	if *flagServer == "" {
		fmt.Fprintln(os.Stderr, "Error: --server is required")
		flag.Usage()
		os.Exit(1)
	}
	if *flagToken == "" {
		fmt.Fprintln(os.Stderr, "Error: --token is required")
		flag.Usage()
		os.Exit(1)
	}

	userID := *flagUserID
	if userID == "" {
		// Extract user_id from server URL path (e.g., ws://server:8080/ws/user123)
		if idx := strings.LastIndex(*flagServer, "/"); idx > 0 {
			userID = (*flagServer)[idx+1:]
		}
	}
	if userID == "" {
		fmt.Fprintln(os.Stderr, "Error: --user-id is required (or embed in server URL)")
		os.Exit(1)
	}

	fmt.Printf("Starting xbot-runner...\n")
	fmt.Printf("  Server:    %s\n", *flagServer)
	fmt.Printf("  User ID:   %s\n", userID)
	fmt.Printf("  Workspace: %s\n", *flagWorkspace)

	// Start HTTP server for large file transfers
	httpPort, err := startHTTPServer(*flagHTTPAddr, *flagToken, *flagWorkspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start HTTP server: %v\n", err)
		os.Exit(1)
	}

	// Detect local IP for HTTP address
	localIP := detectLocalIP()
	httpAddr := fmt.Sprintf("http://%s:%d", localIP, httpPort)
	fmt.Printf("  HTTP:      %s\n", httpAddr)

	// Connect to WebSocket server
	serverURL := *flagServer
	if !strings.Contains(serverURL, "://") {
		serverURL = "ws://" + serverURL
	}

	conn, err := connectToServer(serverURL, userID, *flagToken)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to connect: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()
	fmt.Printf("  Connected!\n")

	// Send updated registration with HTTP address
	if err := sendRegistration(conn, userID, *flagToken, httpAddr); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to register: %v\n", err)
		os.Exit(1)
	}

	// WebSocket write mutex — gorilla/websocket requires single concurrent writer.
	var writeMu sync.Mutex

	// Start heartbeat
	stopHeartbeat := make(chan struct{})
	go runHeartbeat(conn, stopHeartbeat, &writeMu)

	// Start read loop
	done := make(chan struct{})
	go runReadLoop(conn, *flagWorkspace, done, &writeMu)

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	fmt.Println("\nShutting down...")
	close(stopHeartbeat)
	writeMu.Lock()
	conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"disconnect"}`))
	conn.Close()
	writeMu.Unlock()
}

// detectLocalIP returns the first non-loopback local IP address.
func detectLocalIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "127.0.0.1"
	}
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() && ipnet.IP.To4() != nil {
			return ipnet.IP.String()
		}
	}
	return "127.0.0.1"
}
