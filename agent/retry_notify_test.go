package agent

import (
	"errors"
	"net"
	"testing"
)

func TestSummarizeRetryError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, "未知错误"},
		{"TLS handshake timeout", errors.New("TLS handshake timeout"), "网络超时"},
		{"connection refused", errors.New("dial tcp: connection refused"), "连接被拒绝"},
		{"429", errors.New(`POST "url": 429 Too Many Requests`), "请求限流"},
		{"rate limit", errors.New("rate limit exceeded"), "请求限流"},
		{"502", errors.New(`POST "url": 502 Bad Gateway`), "服务暂时不可用"},
		{"503", errors.New("CodeBuddy API error: status=503"), "服务暂时不可用"},
		{"500", errors.New(`POST "url": 500 Internal Server Error`), "服务端错误"},
		{"504", errors.New(`POST "url": 504 Gateway Timeout`), "服务端错误"},
		{"net.OpError timeout", &net.OpError{Op: "dial", Net: "tcp", Err: &timeoutErr{}}, "网络超时"},
		{"net.OpError non-timeout", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("refused")}, "网络错误"},
		{"generic error", errors.New("something went wrong"), "临时错误"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := summarizeRetryError(tt.err)
			if got != tt.want {
				t.Errorf("summarizeRetryError(%v) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}

// timeoutErr 实现 net.Error 接口，Timeout() 返回 true
type timeoutErr struct{}

func (e *timeoutErr) Error() string   { return "i/o timeout" }
func (e *timeoutErr) Timeout() bool   { return true }
func (e *timeoutErr) Temporary() bool { return true }
