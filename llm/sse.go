package llm

import (
	"bufio"
	"io"
	"strings"
)

// SSEReader 封装 Server-Sent Events 流的读取。
// 只负责行级解析，不解析事件内容。
type SSEReader struct {
	reader *bufio.Reader
}

// NewSSEReader 创建 SSE 读取器
func NewSSEReader(r io.Reader) *SSEReader {
	return &SSEReader{reader: bufio.NewReader(r)}
}

// Next 读取下一个 SSE data 事件的内容。
// 返回值：
//   - data: 事件数据（去掉 "data: " 前缀）
//   - done: 是否收到 [DONE] 标记或 EOF
//   - err: 读取错误（EOF 不算错误，会设置 done=true）
func (s *SSEReader) Next() (data string, done bool, err error) {
	for {
		line, readErr := s.reader.ReadString('\n')
		if readErr != nil {
			if readErr == io.EOF {
				return "", true, nil
			}
			return "", false, readErr
		}
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "data: ") {
			continue
		}
		data = strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" || data == "" {
			return "", true, nil
		}
		return data, false, nil
	}
}
