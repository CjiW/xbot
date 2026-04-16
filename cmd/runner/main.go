package main

import (
	"flag"
	"fmt"
	"log"
	"crypto/rand"
	"math/big"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"xbot/internal/runnerclient"
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
	flagLLMProvider = flag.String("llm-provider", "", "LLM provider: openai or anthropic (enables local LLM mode)")
	flagLLMBaseURL  = flag.String("llm-base-url", "", "LLM API base URL (for OpenAI-compatible endpoints)")
	flagLLMAPIKey   = flag.String("llm-api-key", "", "LLM API key")
	flagLLMModel    = flag.String("llm-model", "", "LLM model name")
)

const (
	baseDelay  = 1 * time.Second
	maxDelay   = 60 * time.Second
	maxRetries = 0 // 0 = infinite retries
)

func main() {
	flag.Parse()

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
	var exec runnerclient.Executor
	var dockerMode bool
	var execWorkspace string

	if *flagMode == "docker" {
		log.Printf("Docker mode: image=%s, workspace=%s", *flagDockerImage, *flagWorkspace)
		exec, err = runnerclient.NewDockerExecutor(userID, *flagDockerImage, *flagWorkspace)
		if err != nil {
			log.Fatalf("Failed to create docker executor: %v", err)
		}
		dockerMode = true
		execWorkspace = "/workspace"
	} else {
		exec = runnerclient.NewNativeExecutor(*flagWorkspace)
		dockerMode = false
		execWorkspace = *flagWorkspace
	}
	defer func() {
		if cerr := exec.Close(); cerr != nil {
			log.Printf("Executor close error: %v", cerr)
		}
	}()

	// 创建 handler
	runnerLogf := func(format string, args ...interface{}) {
		log.Printf(format, args...)
	}
	handler := runnerclient.NewHandler(exec,
		runnerclient.WithVerbose(*flagVerbose),
		runnerclient.WithPathGuard(&runnerclient.PathGuard{
			Workspace:   execWorkspace,
			FullControl: *flagFullControl,
			DockerMode:  dockerMode,
		}),
		runnerclient.WithDockerMode(dockerMode),
		runnerclient.WithLogFunc(runnerLogf),
	)

	// 初始化本地 LLM 客户端
	if *flagLLMProvider != "" {
		if err := handler.InitLLM(*flagLLMProvider, *flagLLMBaseURL, *flagLLMAPIKey, *flagLLMModel); err != nil {
			log.Fatalf("Failed to init local LLM: %v", err)
		}
	}

	// 检测 shell
	shell := runnerclient.DetectShell(dockerMode, exec)

	log.Printf("Starting xbot-runner  mode=%s server=%s  user=%s  workspace=%s  full-control=%v",
		*flagMode, *flagServer, userID, execWorkspace, *flagFullControl)

	serverURL := *flagServer
	if !strings.Contains(serverURL, "://") {
		serverURL = "ws://" + serverURL
	}

	sigCh := make(chan os.Signal, 1)
	stopCh := make(chan struct{})
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Printf("Received shutdown signal, stopping...")
		close(stopCh)
	}()

	attempt := 0
	for {
		select {
		case <-stopCh:
			return
		default:
		}
		err := runSession(serverURL, userID, *flagToken, execWorkspace, shell, handler)
		if err == nil {
			return
		}
		select {
		case <-stopCh:
			return
		default:
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

// runSession 连接 server 并运行读写循环。
// 连接丢失时返回错误（触发重连）。
func runSession(serverURL, userID, authToken, workspace, shell string, handler *runnerclient.Handler) error {
	runnerLogf := handler.LogFunc
	conn, err := runnerclient.Connect(serverURL, userID, authToken, workspace, shell, runnerclient.ConnectOptions{
		LLMProvider: handler.LLMProvider(),
		LLMModel:    handler.LLMModel(),
		LogFunc:     runnerLogf,
	})
	if err != nil {
		return err
	}
	log.Printf("Connected to server, registered as user=%s", userID)

	writeCh := make(chan runnerclient.WriteMsg, 64)
	stopWrite := make(chan struct{})
	writeDone := make(chan struct{})

	// 将写通道暴露给 stdio 处理器（用于推送消息）
	handler.SetWriteChannels(writeCh, writeDone)

	go runnerclient.WritePump(conn, writeCh, stopWrite, writeDone, runnerLogf)
	runnerclient.ReadLoop(conn, handler, writeCh, writeDone, runnerLogf)

	// 通知 writePump 立即退出
	close(stopWrite)

	// 断开连接时杀死活跃的 stdio 进程和后台任务
	handler.Cleanup()

	return fmt.Errorf("read loop exited")
}

// mustRandInt63n returns a cryptographically random int64 in [0, n).
func mustRandInt63n(n int64) int64 {
	if n <= 0 {
		return 0
	}
	r, err := rand.Int(rand.Reader, big.NewInt(n))
	if err != nil {
		return 0 // fallback to no jitter on error
	}
	return r.Int64()
}

// backoff 返回带随机抖动的指数退避延迟。
func backoff(attempt int) time.Duration {
	delay := baseDelay
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay > maxDelay {
			delay = maxDelay
			break
		}
	}
	jitter := time.Duration(mustRandInt63n(int64(delay) / 4))
	return delay + jitter
}
