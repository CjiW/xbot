package main

import (
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

var (
	flagServer      = flag.String("server", "", "WebSocket server URL (required)")
	flagToken       = flag.String("token", "", "Auth token (required)")
	flagWorkspace   = flag.String("workspace", "/workspace", "Workspace root directory")
	flagUserID      = flag.String("user-id", "", "User ID (auto-detected from --server URL)")
	flagFullControl = flag.Bool("full-control", false, "Disable path restrictions (allow access to any file)")
	flagVerbose     = flag.Bool("v", false, "Verbose logging (log all requests)")
	flagMode        = flag.String("mode", "native", "Runner mode: native or docker")
	flagDockerImage = flag.String("docker-image", "ubuntu:22.04", "Docker image (docker mode)")
)

var verboseLog bool

const (
	baseDelay  = 1 * time.Second
	maxDelay   = 60 * time.Second
	maxRetries = 0 // 0 = infinite retries
)

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

	var err error
	if *flagMode == "docker" {
		log.Printf("Docker mode: image=%s, workspace=%s", *flagDockerImage, *flagWorkspace)
		executor, err = newDockerExecutor(userID, *flagDockerImage, *flagWorkspace)
		if err != nil {
			log.Fatalf("Failed to create docker executor: %v", err)
		}
		dockerMode = true
		execWorkspace = "/workspace"
	} else {
		executor = newNativeExecutor(*flagWorkspace)
		dockerMode = false
		execWorkspace = *flagWorkspace
	}
	defer func() {
		if cerr := executor.Close(); cerr != nil {
			log.Printf("Executor close error: %v", cerr)
		}
	}()

	registerWorkspace := execWorkspace

	log.Printf("Starting xbot-runner  mode=%s server=%s  user=%s  workspace=%s  full-control=%v", *flagMode, *flagServer, userID, registerWorkspace, *flagFullControl)

	serverURL := *flagServer
	if !strings.Contains(serverURL, "://") {
		serverURL = "ws://" + serverURL
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Printf("Received shutdown signal, stopping...")
		os.Exit(0)
	}()

	attempt := 0
	for {
		err := runSession(serverURL, userID, *flagToken, registerWorkspace)
		if err == nil {
			return
		}
		attempt++
		if maxRetries > 0 && attempt >= maxRetries {
			log.Fatalf("Max reconnect attempts (%d) reached, giving up", maxRetries)
		}
		delay := backoff(attempt)
		log.Printf("Connection lost: %v  — reconnecting in %v (attempt %d)", err, delay, attempt)
		time.Sleep(delay)
	}
}

// runSession connects to the server and runs read/write loops.
// Returns an error when the connection is lost (triggers reconnect).
func runSession(serverURL, userID, authToken, workspace string) error {
	conn, err := connectToServer(serverURL, userID, authToken, workspace)
	if err != nil {
		return err
	}
	log.Printf("Connected to server, registered as user=%s", userID)

	writeCh := make(chan writeMsg, 64)
	stopWrite := make(chan struct{})
	writeDone := make(chan struct{})

	// Expose write channel to stdio handlers for push messages.
	sessionWriteCh = writeCh
	sessionWriteDone = writeDone

	go writePump(conn, writeCh, stopWrite, writeDone)
	runReadLoop(conn, writeCh, writeDone)

	// Signal writePump to exit immediately.
	close(stopWrite)

	// Kill any active stdio processes on disconnect.
	cleanupStdioProcs()

	// Kill any background tasks.
	cleanupBgTasks()

	return fmt.Errorf("read loop exited")
}

// backoff returns an exponential backoff delay with jitter.
func backoff(attempt int) time.Duration {
	delay := baseDelay
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay > maxDelay {
			delay = maxDelay
			break
		}
	}
	jitter := time.Duration(rand.Int63n(int64(delay) / 4))
	return delay + jitter
}

// detectShell finds the best available shell.
// Docker mode: queries /etc/passwd inside the container (same as DockerSandbox.detectShell).
// Native mode: checks host filesystem.
func detectShell() string {
	if dockerMode {
		out, err := exec.Command("docker", "exec", "-i", executor.(*dockerExecutor).containerName,
			"sh", "-c", "grep '^root:' /etc/passwd | cut -d: -f7").Output()
		if err == nil {
			shell := strings.TrimSpace(string(out))
			if shell != "" {
				return shell
			}
		}
	}
	// Fallback: check host or default
	for _, candidate := range []string{"/bin/bash", "/usr/bin/bash", "/bin/sh"} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return "/bin/sh"
}
