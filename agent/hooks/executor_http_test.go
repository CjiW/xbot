package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newTestHTTPExecutor creates an HTTPExecutor with a custom client pointed at
// the given httptest server and a mock env lookup for deterministic tests.
func newTestHTTPExecutor(server *httptest.Server) *HTTPExecutor {
	e := NewHTTPExecutor()
	e.client = server.Client()
	// Whitelist loopback so httptest server (127.0.0.1) is accessible.
	e.SetAllowedNets([]string{"127.0.0.0/8"})
	e.envLookupFn = func(key string) (string, bool) {
		env := map[string]string{
			"MY_TOKEN":   "secret-abc-123",
			"MY_API_KEY": "key-xyz-789",
		}
		v, ok := env[key]
		return v, ok
	}
	return e
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestHTTPExecutor_Type(t *testing.T) {
	e := NewHTTPExecutor()
	if got := e.Type(); got != "http" {
		t.Errorf("Type() = %q, want %q", got, "http")
	}
}

func TestHTTPExecutor_Success2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify method and content type.
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}

		// Read and echo back the payload.
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode body: %v", err)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"decision": "deny",
			"reason":   "tool not allowed",
			"context":  "please use a different tool",
		})
	}))
	defer srv.Close()

	e := newTestHTTPExecutor(srv)
	def := &HookDef{
		Type: "http",
		URL:  srv.URL,
	}
	event := &testEvent{payload: map[string]any{"tool": "shell_exec"}}

	result, err := e.Execute(context.Background(), def, event)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if result.Decision != "deny" {
		t.Errorf("Decision = %q, want %q", result.Decision, "deny")
	}
	if result.Reason != "tool not allowed" {
		t.Errorf("Reason = %q, want %q", result.Reason, "tool not allowed")
	}
	if result.Context != "please use a different tool" {
		t.Errorf("Context = %q, want %q", result.Context, "please use a different tool")
	}
}

func TestHTTPExecutor_Non2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, "access denied")
	}))
	defer srv.Close()

	e := newTestHTTPExecutor(srv)
	def := &HookDef{
		Type: "http",
		URL:  srv.URL,
	}
	event := &testEvent{payload: map[string]any{}}

	result, err := e.Execute(context.Background(), def, event)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	// Non-2xx → non-blocking error, decision is "allow".
	if result.Decision != "allow" {
		t.Errorf("Decision = %q, want %q", result.Decision, "allow")
	}
	if !strings.Contains(result.Reason, "403") {
		t.Errorf("Reason = %q, want to contain '403'", result.Reason)
	}
}

func TestHTTPExecutor_ConnectionFailure(t *testing.T) {
	e := NewHTTPExecutor()
	// Whitelist loopback so SSRF check passes, but the connection will fail
	// because nothing listens on port 1.
	e.SetAllowedNets([]string{"127.0.0.0/8"})
	def := &HookDef{
		Type: "http",
		URL:  "http://127.0.0.1:1/impossible",
	}
	event := &testEvent{payload: map[string]any{}}

	result, err := e.Execute(context.Background(), def, event)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	// Connection failure → non-blocking, decision is "allow".
	if result.Decision != "allow" {
		t.Errorf("Decision = %q, want %q", result.Decision, "allow")
	}
	if !strings.Contains(result.Reason, "HTTP request failed") {
		t.Errorf("Reason = %q, want to contain 'HTTP request failed'", result.Reason)
	}
}

func TestHTTPExecutor_CustomHeaders(t *testing.T) {
	var receivedHeaders http.Header

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"decision":"allow"}`)
	}))
	defer srv.Close()

	e := newTestHTTPExecutor(srv)
	def := &HookDef{
		Type: "http",
		URL:  srv.URL,
		Headers: map[string]string{
			"X-Custom-Auth": "bearer-token-xyz",
			"X-Request-Id":  "req-123",
		},
	}
	event := &testEvent{payload: map[string]any{}}

	_, err := e.Execute(context.Background(), def, event)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	if got := receivedHeaders.Get("X-Custom-Auth"); got != "bearer-token-xyz" {
		t.Errorf("X-Custom-Auth = %q, want %q", got, "bearer-token-xyz")
	}
	if got := receivedHeaders.Get("X-Request-Id"); got != "req-123" {
		t.Errorf("X-Request-Id = %q, want %q", got, "req-123")
	}
}

func TestHTTPExecutor_SSRF_PrivateIP(t *testing.T) {
	e := NewHTTPExecutor()
	def := &HookDef{
		Type: "http",
		URL:  "http://127.0.0.1/test",
	}
	event := &testEvent{payload: map[string]any{}}

	_, err := e.Execute(context.Background(), def, event)
	if err == nil {
		t.Fatal("Execute() expected error for private IP, got nil")
	}
	if !strings.Contains(err.Error(), "SSRF") {
		t.Errorf("error = %q, want to contain 'SSRF'", err.Error())
	}
}

func TestHTTPExecutor_SSRF_AllowedNet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"decision":"allow"}`)
	}))
	defer srv.Close()

	e := newTestHTTPExecutor(srv)
	// Whitelist loopback so we can connect to the test server.
	e.SetAllowedNets([]string{"127.0.0.0/8"})

	def := &HookDef{
		Type: "http",
		URL:  srv.URL,
	}
	event := &testEvent{payload: map[string]any{}}

	result, err := e.Execute(context.Background(), def, event)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if result.Decision != "allow" {
		t.Errorf("Decision = %q, want %q", result.Decision, "allow")
	}
}

func TestHTTPExecutor_DefaultTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond) // short delay, should succeed
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"decision":"allow"}`)
	}))
	defer srv.Close()

	e := newTestHTTPExecutor(srv)
	def := &HookDef{
		Type: "http",
		URL:  srv.URL,
		// No Timeout → default 10s
	}
	event := &testEvent{payload: map[string]any{}}

	result, err := e.Execute(context.Background(), def, event)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if result.Decision != "allow" {
		t.Errorf("Decision = %q, want %q", result.Decision, "allow")
	}
}

func TestHTTPExecutor_EnvVarHeaders(t *testing.T) {
	var receivedAuth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"decision":"allow"}`)
	}))
	defer srv.Close()

	e := newTestHTTPExecutor(srv)
	def := &HookDef{
		Type: "http",
		URL:  srv.URL,
		Headers: map[string]string{
			"Authorization": "Bearer $MY_TOKEN",
		},
	}
	event := &testEvent{payload: map[string]any{}}

	_, err := e.Execute(context.Background(), def, event)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	want := "Bearer secret-abc-123"
	if receivedAuth != want {
		t.Errorf("Authorization = %q, want %q", receivedAuth, want)
	}
}

func TestHTTPExecutor_AllowedEnvVars(t *testing.T) {
	var receivedKey string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedKey = r.Header.Get("X-Hook-Env-MY_API_KEY")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"decision":"allow"}`)
	}))
	defer srv.Close()

	e := newTestHTTPExecutor(srv)
	def := &HookDef{
		Type:           "http",
		URL:            srv.URL,
		AllowedEnvVars: []string{"MY_API_KEY"},
	}
	event := &testEvent{payload: map[string]any{}}

	_, err := e.Execute(context.Background(), def, event)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	if receivedKey != "key-xyz-789" {
		t.Errorf("X-Hook-Env-MY_API_KEY = %q, want %q", receivedKey, "key-xyz-789")
	}
}

func TestHTTPExecutor_UpdatedInput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"decision":     "allow",
			"updatedInput": map[string]any{"command": "ls -la"},
		})
	}))
	defer srv.Close()

	e := newTestHTTPExecutor(srv)
	def := &HookDef{
		Type: "http",
		URL:  srv.URL,
	}
	event := &testEvent{payload: map[string]any{}}

	result, err := e.Execute(context.Background(), def, event)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if result.UpdatedInput == nil {
		t.Fatal("UpdatedInput is nil")
	}
	if result.UpdatedInput["command"] != "ls -la" {
		t.Errorf("UpdatedInput[command] = %v, want %q", result.UpdatedInput["command"], "ls -la")
	}
}

// ---------------------------------------------------------------------------
// Unit tests for helpers
// ---------------------------------------------------------------------------

func TestIsPrivateIP(t *testing.T) {
	tests := []struct {
		ip   string
		want bool
	}{
		{"127.0.0.1", true},
		{"10.0.0.1", true},
		{"172.16.0.1", true},
		{"192.168.1.1", true},
		{"169.254.1.1", true},
		{"::1", true},
		{"fd00::1", true},
		{"fe80::1", true},
		{"8.8.8.8", false},
		{"1.2.3.4", false},
		{"203.0.113.1", false},
	}

	for _, tt := range tests {
		t.Run(tt.ip, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			if ip == nil {
				t.Fatalf("failed to parse IP %q", tt.ip)
			}
			if got := isPrivateIP(ip); got != tt.want {
				t.Errorf("isPrivateIP(%s) = %v, want %v", tt.ip, got, tt.want)
			}
		})
	}
}

func TestExtractHost(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"http://example.com/path", "example.com"},
		{"https://example.com:8080/path", "example.com"},
		{"http://127.0.0.1:9090/hook", "127.0.0.1"},
		{"https://[::1]:8080/test", "::1"},
		{"http://my-host.local", "my-host.local"},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			got := extractHost(tt.url)
			if got != tt.want {
				t.Errorf("extractHost(%q) = %q, want %q", tt.url, got, tt.want)
			}
		})
	}
}
