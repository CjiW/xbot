package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	log "xbot/logger"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RemoteStdioTransport implements mcp.Transport for MCP stdio servers
// running on a remote runner. It proxies stdin/stdout over the runner's
// WebSocket connection using the stdio streaming protocol.
type RemoteStdioTransport struct {
	Sandbox    *RemoteSandbox
	UserID     string
	StreamID   string
	Command    string
	Args       []string
	Env        []string
	Dir        string
	ServerName string
}

// Connect starts the remote process and returns an MCP Connection that
// proxies stdin/stdout over the runner WebSocket.
func (t *RemoteStdioTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	rc, err := t.Sandbox.getRunner(t.UserID)
	if err != nil {
		return nil, fmt.Errorf("no runner for user %q: %w", t.UserID, err)
	}

	reqBody, _ := json.Marshal(StdioStartRequest{
		StreamID: t.StreamID,
		Command:  t.Command,
		Args:     t.Args,
		Env:      t.Env,
		Dir:      t.Dir,
	})
	msg := &RunnerMessage{
		ID:     generateID(),
		Type:   ProtoStdioStart,
		UserID: t.UserID,
		Body:   reqBody,
	}

	resp, err := t.Sandbox.sendRequest(ctx, rc, msg, defaultRequestTimeout)
	if err != nil {
		return nil, fmt.Errorf("stdio_start: %w", err)
	}
	if resp.Type == ProtoError {
		var e ErrorResponse
		json.Unmarshal(resp.Body, &e)
		return nil, fmt.Errorf("stdio_start: %s", e.Message)
	}

	// Create a pipe for proxying stdout data from the runner into the MCP reader.
	pr, pw := io.Pipe()

	stream := &stdioStream{
		pw:       pw,
		exitCh:   make(chan struct{}),
		streamID: t.StreamID,
	}
	t.Sandbox.registerStdioStream(t.StreamID, stream)

	writer := &remoteStdinWriter{
		sandbox:  t.Sandbox,
		userID:   t.UserID,
		streamID: t.StreamID,
	}

	log.WithFields(log.Fields{
		"user_id":     t.UserID,
		"stream_id":   t.StreamID,
		"server_name": t.ServerName,
		"command":     t.Command,
	}).Info("Remote stdio MCP transport connected")

	inner := &mcp.IOTransport{Reader: pr, Writer: &stdinWriteCloser{w: writer, sandbox: t.Sandbox, streamID: t.StreamID, userID: t.UserID}}
	return inner.Connect(ctx)
}

// stdioStream tracks an active stdio stream on the server side.
type stdioStream struct {
	pw       *io.PipeWriter
	exitCh   chan struct{} // closed when stdio_exit is received
	streamID string

	mu       sync.Mutex
	exitCode int
	exitErr  string
	exited   bool

	pwMu sync.Mutex // protects pw.Write and pw.Close from concurrent access
}

// remoteStdinWriter sends data to the runner's stdin via WebSocket.
type remoteStdinWriter struct {
	sandbox  *RemoteSandbox
	userID   string
	streamID string
}

func (w *remoteStdinWriter) Write(p []byte) (int, error) {
	rc, err := w.sandbox.getRunner(w.userID)
	if err != nil {
		return 0, fmt.Errorf("runner disconnected: %w", err)
	}

	reqBody, _ := json.Marshal(StdioWriteRequest{
		StreamID: w.streamID,
		Data:     base64.StdEncoding.EncodeToString(p),
	})
	msg := &RunnerMessage{
		Type:   ProtoStdioWrite,
		UserID: w.userID,
		Body:   reqBody,
	}

	if err := w.sandbox.sendOnly(rc, msg); err != nil {
		return 0, err
	}
	return len(p), nil
}

// stdinWriteCloser wraps the writer and sends stdio_close on Close.
type stdinWriteCloser struct {
	w        *remoteStdinWriter
	sandbox  *RemoteSandbox
	streamID string
	userID   string
}

func (c *stdinWriteCloser) Write(p []byte) (int, error) {
	return c.w.Write(p)
}

func (c *stdinWriteCloser) Close() error {
	rc, err := c.sandbox.getRunner(c.userID)
	if err != nil {
		return nil // runner already gone
	}

	reqBody, _ := json.Marshal(StdioCloseRequest{StreamID: c.streamID})
	msg := &RunnerMessage{
		ID:     generateID(),
		Type:   ProtoStdioClose,
		UserID: c.userID,
		Body:   reqBody,
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultRequestTimeout)
	defer cancel()

	c.sandbox.sendRequest(ctx, rc, msg, defaultRequestTimeout) //nolint:errcheck
	c.sandbox.removeStdioStream(c.streamID)
	return nil
}

// === RemoteSandbox stdio stream management ===

func (rs *RemoteSandbox) registerStdioStream(streamID string, stream *stdioStream) {
	rs.stdioMu.Lock()
	defer rs.stdioMu.Unlock()
	if rs.stdioStreams == nil {
		rs.stdioStreams = make(map[string]*stdioStream)
	}
	rs.stdioStreams[streamID] = stream
}

func (rs *RemoteSandbox) removeStdioStream(streamID string) {
	rs.stdioMu.Lock()
	defer rs.stdioMu.Unlock()
	if s, ok := rs.stdioStreams[streamID]; ok {
		s.pw.Close()
		delete(rs.stdioStreams, streamID)
	}
}

// handleStdioPush routes incoming stdio push messages from the runner.
// Returns true if the message was handled as a stdio push.
func (rs *RemoteSandbox) handleStdioPush(resp *RunnerMessage) bool {
	switch resp.Type {
	case ProtoStdioData:
		var data StdioDataMessage
		if err := json.Unmarshal(resp.Body, &data); err != nil {
			return true
		}
		rs.stdioMu.Lock()
		stream, ok := rs.stdioStreams[data.StreamID]
		rs.stdioMu.Unlock()
		if !ok {
			return true
		}
		decoded, err := base64.StdEncoding.DecodeString(data.Data)
		if err != nil {
			return true
		}
		stream.pwMu.Lock()
		stream.pw.Write(decoded) //nolint:errcheck
		stream.pwMu.Unlock()
		return true

	case ProtoStdioExit:
		var exit StdioExitMessage
		if err := json.Unmarshal(resp.Body, &exit); err != nil {
			return true
		}
		rs.stdioMu.Lock()
		stream, ok := rs.stdioStreams[exit.StreamID]
		rs.stdioMu.Unlock()
		if !ok {
			return true
		}
		stream.mu.Lock()
		stream.exited = true
		stream.exitCode = exit.ExitCode
		stream.exitErr = exit.Error
		stream.mu.Unlock()
		// Close the pipe writer to signal EOF to the MCP reader.
		stream.pwMu.Lock()
		stream.pw.Close()
		stream.pwMu.Unlock()
		close(stream.exitCh)
		log.WithFields(log.Fields{
			"stream_id": exit.StreamID,
			"exit_code": exit.ExitCode,
		}).Debug("Remote stdio process exited")
		return true
	}
	return false
}
