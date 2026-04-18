package channel

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"xbot/bus"
	"xbot/storage/sqlite"

	"github.com/gorilla/websocket"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	f, err := os.CreateTemp("", "xbot_web_test_*.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(f.Name()) })

	db, err := sqlite.Open(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db.Conn()
}

func newTestWebChannel(t *testing.T, db *sql.DB) (*WebChannel, *bus.MessageBus) {
	t.Helper()
	msgBus := bus.NewMessageBus()
	wc := NewWebChannel(WebChannelConfig{
		Host: "127.0.0.1",
		Port: 0, // random port
		DB:   db,
	}, msgBus)

	return wc, msgBus
}

func startTestServer(t *testing.T, wc *WebChannel) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", wc.handleWS)
	mux.HandleFunc("/api/auth/register", wc.handleRegister)
	mux.HandleFunc("/api/auth/login", wc.handleLogin)
	mux.HandleFunc("/api/auth/logout", wc.handleLogout)
	mux.HandleFunc("/api/history", wc.authMiddleware(wc.handleHistory))

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return server
}

func makeWSConnection(t *testing.T, serverURL, cookie string) *websocket.Conn {
	t.Helper()
	dialer := websocket.Dialer{}
	header := http.Header{}
	if cookie != "" {
		header.Set("Cookie", cookie)
	}
	// Convert http:// to ws:// for WebSocket dial
	wsURL := strings.Replace(serverURL, "http://", "ws://", 1)
	conn, resp, err := dialer.Dial(wsURL+"/ws", header)
	if err != nil {
		status := -1
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("WebSocket dial failed: %v (status: %d)", err, status)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// ---------------------------------------------------------------------------
// Auth tests
// ---------------------------------------------------------------------------

func TestRegisterAndLogin(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	server := startTestServer(t, wc)

	// Register
	regBody := `{"username":"testuser","password":"secret123"}`
	resp, err := http.Post(server.URL+"/api/auth/register", "application/json", strings.NewReader(regBody))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}

	var regResp authResponse
	json.NewDecoder(resp.Body).Decode(&regResp)
	if !regResp.OK || regResp.UserID == 0 {
		t.Fatal("registration failed")
	}

	// Duplicate registration
	resp2, _ := http.Post(server.URL+"/api/auth/register", "application/json", strings.NewReader(regBody))
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 for duplicate, got %d", resp2.StatusCode)
	}

	// Login
	loginBody := `{"username":"testuser","password":"secret123"}`
	resp3, err := http.Post(server.URL+"/api/auth/login", "application/json", strings.NewReader(loginBody))
	if err != nil {
		t.Fatal(err)
	}
	defer resp3.Body.Close()

	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp3.StatusCode)
	}

	// Check cookie is set
	cookies := resp3.Cookies()
	found := false
	for _, c := range cookies {
		if c.Name == webSessionCookieName {
			found = true
			if !c.HttpOnly {
				t.Error("cookie should be HttpOnly")
			}
		}
	}
	if !found {
		t.Error("session cookie not set")
	}

	// Wrong password
	badLogin := `{"username":"testuser","password":"wrong"}`
	resp4, _ := http.Post(server.URL+"/api/auth/login", "application/json", strings.NewReader(badLogin))
	if resp4.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for wrong password, got %d", resp4.StatusCode)
	}
}

func TestLogout(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	server := startTestServer(t, wc)

	// Register + Login
	http.Post(server.URL+"/api/auth/register", "application/json", strings.NewReader(`{"username":"u1","password":"p1"}`))
	loginResp, _ := http.Post(server.URL+"/api/auth/login", "application/json", strings.NewReader(`{"username":"u1","password":"p1"}`))

	var cookies []*http.Cookie
	for _, c := range loginResp.Cookies() {
		if c.Name == webSessionCookieName {
			cookies = append(cookies, c)
		}
	}

	// Logout
	req, _ := http.NewRequest("POST", server.URL+"/api/auth/logout", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Cookie should be cleared
	for _, c := range resp.Cookies() {
		if c.Name == webSessionCookieName && c.MaxAge != -1 {
			t.Error("cookie should be cleared (MaxAge=-1)")
		}
	}
}

func TestAuthMiddleware(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	server := startTestServer(t, wc)

	// No cookie → 401
	resp, _ := http.Get(server.URL + "/api/history")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}

	// Register + Login
	http.Post(server.URL+"/api/auth/register", "application/json", strings.NewReader(`{"username":"u2","password":"p2"}`))
	loginResp, _ := http.Post(server.URL+"/api/auth/login", "application/json", strings.NewReader(`{"username":"u2","password":"p2"}`))

	// With cookie → 200 (no history, but authenticated)
	var sessionCookie *http.Cookie
	for _, c := range loginResp.Cookies() {
		if c.Name == webSessionCookieName {
			sessionCookie = c
		}
	}

	req, _ := http.NewRequest("GET", server.URL+"/api/history", nil)
	req.AddCookie(sessionCookie)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 with valid cookie, got %d", resp2.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// WebSocket tests
// ---------------------------------------------------------------------------

func TestWebSocketAuth(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	server := startTestServer(t, wc)

	// No cookie → 401
	dialer := websocket.Dialer{}
	wsURL := strings.Replace(server.URL, "http://", "ws://", 1)
	_, resp, err := dialer.Dial(wsURL+"/ws", nil)
	if err == nil {
		t.Fatal("expected WebSocket upgrade to fail without auth")
	}
	if resp != nil && resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}

	// Register
	regResp, err := http.Post(server.URL+"/api/auth/register", "application/json", strings.NewReader(`{"username":"u3","password":"p3"}`))
	if err != nil {
		t.Fatal(err)
	}
	regResp.Body.Close()
	if regResp.StatusCode != http.StatusCreated {
		t.Fatalf("register failed: %d", regResp.StatusCode)
	}

	// Login
	loginResp, err := http.Post(server.URL+"/api/auth/login", "application/json", strings.NewReader(`{"username":"u3","password":"p3"}`))
	if err != nil {
		t.Fatal(err)
	}
	loginResp.Body.Close()
	if loginResp.StatusCode != http.StatusOK {
		t.Fatalf("login failed: %d", loginResp.StatusCode)
	}

	var sessionCookie *http.Cookie
	for _, c := range loginResp.Cookies() {
		if c.Name == webSessionCookieName {
			sessionCookie = c
		}
	}
	if sessionCookie == nil {
		t.Fatal("no session cookie returned from login")
	}

	conn := makeWSConnection(t, server.URL, sessionCookie.Name+"="+sessionCookie.Value)
	conn.Close()
}

func TestWebSocketChat(t *testing.T) {
	db := newTestDB(t)
	wc, msgBus := newTestWebChannel(t, db)
	server := startTestServer(t, wc)

	// Register + Login
	regResp, err := http.Post(server.URL+"/api/auth/register", "application/json", strings.NewReader(`{"username":"chatuser","password":"pw"}`))
	if err != nil {
		t.Fatal(err)
	}
	regResp.Body.Close()

	loginResp, err := http.Post(server.URL+"/api/auth/login", "application/json", strings.NewReader(`{"username":"chatuser","password":"pw"}`))
	if err != nil {
		t.Fatal(err)
	}
	loginResp.Body.Close()

	var sessionCookie *http.Cookie
	for _, c := range loginResp.Cookies() {
		if c.Name == webSessionCookieName {
			sessionCookie = c
		}
	}

	conn := makeWSConnection(t, server.URL, sessionCookie.Name+"="+sessionCookie.Value)

	// Send message
	if err := conn.WriteJSON(wsClientMessage{Type: "message", Content: "hello"}); err != nil {
		t.Fatal(err)
	}

	// Verify inbound message on bus
	select {
	case msg := <-msgBus.Inbound:
		if msg.Channel != "web" {
			t.Errorf("expected channel 'web', got '%s'", msg.Channel)
		}
		if msg.Content != "hello" {
			t.Errorf("expected content 'hello', got '%s'", msg.Content)
		}
		if msg.SenderName != "chatuser" {
			t.Errorf("expected sender 'chatuser', got '%s'", msg.SenderName)
		}
		if !strings.HasPrefix(msg.SenderID, "web-") {
			t.Errorf("expected senderID to start with 'web-', got '%s'", msg.SenderID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for inbound message")
	}
}

func TestSendToWebSocket(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	server := startTestServer(t, wc)

	// Register + Login
	regResp, _ := http.Post(server.URL+"/api/auth/register", "application/json", strings.NewReader(`{"username":"recv","password":"pw"}`))
	regResp.Body.Close()
	loginResp, err := http.Post(server.URL+"/api/auth/login", "application/json", strings.NewReader(`{"username":"recv","password":"pw"}`))
	if err != nil {
		t.Fatal(err)
	}
	loginResp.Body.Close()

	var sessionCookie *http.Cookie
	for _, c := range loginResp.Cookies() {
		if c.Name == webSessionCookieName {
			sessionCookie = c
		}
	}

	conn := makeWSConnection(t, server.URL, sessionCookie.Name+"="+sessionCookie.Value)

	// Send a message to the client
	msgID, err := wc.Send(bus.OutboundMessage{
		Channel: "web",
		ChatID:  "web-1", // matches senderID format
		Content: "Hello from agent!",
	})
	if err != nil {
		t.Fatal(err)
	}
	if msgID == "" {
		t.Error("expected non-empty message ID")
	}

	// Read from WebSocket
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}

	var wsMsg wsMessage
	json.Unmarshal(raw, &wsMsg)
	if wsMsg.Content != "Hello from agent!" {
		t.Errorf("expected 'Hello from agent!', got '%s'", wsMsg.Content)
	}
	if wsMsg.ID == "" {
		t.Error("expected non-empty WS message ID")
	}
}

// ---------------------------------------------------------------------------
// __FEISHU_CARD__ protocol adaptation test
// ---------------------------------------------------------------------------

func TestConvertFeishuCard(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantLen int // if > 0, check min length
	}{
		{
			name:  "valid card with title",
			input: `__FEISHU_CARD__:card_id:{"header":{"title":{"content":"Test Title"}},"elements":[{"tag":"markdown","content":"Some **bold** text"}]}`,
			want:  "Test Title",
		},
		{
			name:  "card with div text",
			input: `__FEISHU_CARD__:id:{"header":{"title":{"content":"Title"}},"elements":[{"tag":"div","text":"plain text"}]}`,
			want:  "Title",
		},
		{
			name:  "card with div JSON text",
			input: `__FEISHU_CARD__:id:{"header":{"title":{"content":"Title"}},"elements":[{"tag":"div","text":"{\"content\":\"extracted content\"}"}]}`,
			want:  "extracted content",
		},
		{
			name:  "empty card",
			input: `__FEISHU_CARD__:id:{}`,
			want:  "{}",
		},
		{
			name:  "invalid JSON",
			input: `__FEISHU_CARD__:id:not-json`,
			want:  "not-json",
		},
		{
			name:  "plain text (no prefix)",
			input: "Hello world",
			want:  "Hello world",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var result string
			if strings.HasPrefix(tt.input, "__FEISHU_CARD__") {
				result = ConvertFeishuCard(tt.input)
			} else {
				result = tt.input
			}
			if !strings.Contains(result, tt.want) {
				t.Errorf("ConvertFeishuCard() = %q, want to contain %q", result, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Ring buffer test
// ---------------------------------------------------------------------------

func TestRingBuffer(t *testing.T) {
	rb := newRingBuffer(3)

	rb.push(wsMessage{Content: "a"})
	rb.push(wsMessage{Content: "b"})
	rb.push(wsMessage{Content: "c"})

	msgs := rb.flush()
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}
	if msgs[0].Content != "a" || msgs[1].Content != "b" || msgs[2].Content != "c" {
		t.Error("messages not in order")
	}

	// Overflow: push more than capacity
	rb.push(wsMessage{Content: "d"})
	rb.push(wsMessage{Content: "e"})
	rb.push(wsMessage{Content: "f"})
	rb.push(wsMessage{Content: "g"}) // should evict "d"

	msgs = rb.flush()
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages after overflow, got %d", len(msgs))
	}
	if msgs[0].Content != "e" {
		t.Errorf("expected first msg 'e', got '%s'", msgs[0].Content)
	}
}

// ---------------------------------------------------------------------------
// Hub test
// ---------------------------------------------------------------------------

func TestHubOfflineBuffering(t *testing.T) {
	hub := newHub()

	senderID := "web-1"
	msg := wsMessage{Content: "offline msg"}

	// Send to offline user → should be buffered
	ok := hub.sendToClient(senderID, msg)
	if ok {
		t.Error("expected sendToClient to return false for offline user")
	}

	// Verify buffered message count
	if hub.offline[senderID] == nil || hub.offline[senderID].count != 1 {
		t.Error("expected 1 buffered message")
	}

	// Add client → should flush offline messages
	client := &Client{
		sendCh: make(chan wsMessage, 10),
		done:   make(chan struct{}),
		id:     "test-client-1",
	}
	hub.addClient(senderID, client.id, client)

	// Wait for flush
	time.Sleep(100 * time.Millisecond)

	select {
	case m := <-client.sendCh:
		if m.Content != "offline msg" {
			t.Errorf("expected 'offline msg', got '%s'", m.Content)
		}
	default:
		t.Error("expected offline message to be flushed")
	}
}

// ---------------------------------------------------------------------------
// Concurrent test
// ---------------------------------------------------------------------------

func TestConcurrentSends(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	server := startTestServer(t, wc)

	// Register + Login
	regResp, _ := http.Post(server.URL+"/api/auth/register", "application/json", strings.NewReader(`{"username":"concurrent","password":"pw"}`))
	regResp.Body.Close()
	loginResp, _ := http.Post(server.URL+"/api/auth/login", "application/json", strings.NewReader(`{"username":"concurrent","password":"pw"}`))
	loginResp.Body.Close()

	var sessionCookie *http.Cookie
	for _, c := range loginResp.Cookies() {
		if c.Name == webSessionCookieName {
			sessionCookie = c
		}
	}
	if sessionCookie == nil {
		t.Fatal("no session cookie")
	}

	conn := makeWSConnection(t, server.URL, sessionCookie.Name+"="+sessionCookie.Value)

	// Send messages concurrently — must not block (non-blocking design)
	// sendCh has capacity 64, so some may be dropped for rapid sends
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			// Send must not block — this is the key invariant
			done := make(chan string, 1)
			go func() {
				id, _ := wc.Send(bus.OutboundMessage{
					Channel: "web",
					ChatID:  "web-1",
					Content: "concurrent msg",
				})
				done <- id
			}()
			select {
			case <-done:
				// OK — Send returned (message may or may not be delivered)
			case <-time.After(5 * time.Second):
				t.Errorf("Send blocked for message %d", idx)
			}
		}(i)
	}
	wg.Wait()

	// Read whatever messages arrived
	time.Sleep(200 * time.Millisecond)
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))

	received := 0
	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			break
		}
		received++
	}

	// We expect at least some messages to be delivered (not all dropped)
	// With 100 rapid concurrent sends and 64-capacity buffer, typically ~64 are delivered
	if received == 0 {
		t.Error("expected at least some messages to be delivered")
	}
	if received > 100 {
		t.Errorf("received more messages than sent: %d", received)
	}
	t.Logf("Delivered %d/100 messages (buffer capacity: 64, non-blocking design)", received)
}
