package runnerclient

import (
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"github.com/gorilla/websocket"

	"xbot/internal/runnerproto"
)

// ConnectOptions 连接选项。
type ConnectOptions struct {
	LLMProvider string  // LLM provider，空 = 无 LLM
	LLMModel    string  // 默认模型名
	LogFunc     LogFunc // 日志回调（nil 时静默）
}

// Connect 建立 WebSocket 连接并发送注册消息。
func Connect(serverURL, userID, authToken, workspace, shell string, opts ...ConnectOptions) (*websocket.Conn, error) {
	var opt ConnectOptions
	if len(opts) > 0 {
		opt = opts[0]
	}
	u, err := url.Parse(serverURL)
	if err != nil {
		return nil, fmt.Errorf("parse server URL: %w", err)
	}

	callLogf(opt.LogFunc, "Dialing server %s ...", u.String())
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("dial server: %w", err)
	}

	// 收到 server 心跳时重置读超时
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(PongWait))
		return nil
	})
	conn.SetPingHandler(func(appData string) error {
		conn.SetReadDeadline(time.Now().Add(PongWait))
		return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(WriteWait))
	})
	conn.SetReadDeadline(time.Now().Add(PongWait))

	regBody, _ := json.Marshal(runnerproto.RegisterRequest{
		UserID:      userID,
		AuthToken:   authToken,
		Workspace:   workspace,
		Shell:       shell,
		LLMProvider: opt.LLMProvider,
		LLMModel:    opt.LLMModel,
	})
	regMsg, _ := json.Marshal(runnerproto.RunnerMessage{
		Type:   "register",
		UserID: userID,
		Body:   regBody,
	})
	if err := conn.WriteMessage(websocket.TextMessage, regMsg); err != nil {
		conn.Close()
		return nil, fmt.Errorf("send registration: %w", err)
	}

	// 等待 server 确认或拒绝
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("waiting for registration response: %w", err)
	}
	var resp runnerproto.RunnerMessage
	if err := json.Unmarshal(raw, &resp); err != nil {
		conn.Close()
		return nil, fmt.Errorf("invalid registration response: %w", err)
	}
	if resp.Type == "error" {
		var e runnerproto.ErrorResponse
		json.Unmarshal(resp.Body, &e)
		conn.Close()
		return nil, fmt.Errorf("registration rejected: %s", e.Message)
	}

	// 重置读超时为正常操作的 pongWait
	conn.SetReadDeadline(time.Now().Add(PongWait))

	callLogf(opt.LogFunc, "Registration sent  user=%s  workspace=%s", userID, workspace)
	return conn, nil
}

// WritePump 是唯一向 WebSocket 连接写入的协程。
// 所有写入（响应、心跳）都通过 writeCh 进行，避免并发写入。
func WritePump(conn *websocket.Conn, writeCh <-chan WriteMsg, stop <-chan struct{}, done chan<- struct{}, logf LogFunc) {
	ticker := time.NewTicker(PingPeriod)
	defer func() {
		ticker.Stop()
		conn.Close()
		close(done)
	}()

	for {
		select {
		case msg := <-writeCh:
			if msg.Err != nil {
				// 控制消息（ping）— 使用 WriteControl
				err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(WriteWait))
				msg.Err <- err
			} else {
				err := conn.WriteMessage(websocket.TextMessage, msg.Data)
				if err != nil {
					callLogf(logf, "WebSocket write error: %v", err)
					return
				}
			}
		case <-ticker.C:
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(WriteWait)); err != nil {
				callLogf(logf, "Ping failed: %v", err)
				return
			}
		case <-stop:
			return
		}
	}
}

// ReadLoop 从 server 读取消息并分发到 handler。
// 请求异步处理，使读循环可以在长时间操作期间继续处理 WebSocket 控制帧（ping/pong）。
func ReadLoop(conn *websocket.Conn, handler *Handler, writeCh chan<- WriteMsg, writeDone <-chan struct{}, logf LogFunc) {
	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				callLogf(logf, "WebSocket read error: %v", err)
			} else {
				callLogf(logf, "WebSocket closed: %v", err)
			}
			return
		}

		var msg runnerproto.RunnerMessage
		if err := json.Unmarshal(message, &msg); err != nil {
			callLogf(logf, "Invalid message from server: %v", err)
			continue
		}

		if handler.Verbose {
			callLogf(logf, "→ %s [id=%s]", msg.Type, msg.ID)
		}

		// Fire-and-forget 消息（无需响应）
		if msg.Type == runnerproto.ProtoStdioWrite {
			go handler.DispatchFireAndForget(msg)
			continue
		}

		go func() {
			resp := handler.HandleRequest(msg)
			data, _ := json.Marshal(resp)
			select {
			case writeCh <- WriteMsg{Data: data}:
			case <-writeDone:
			}
		}()
	}
}
