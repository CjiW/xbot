package llm

import (
	"bufio"
	"context"
	"io"
	"strings"
)

// ReadSSEEvents 从 bufio.Reader 中读取 SSE 事件流，返回一个 channel。
// 每个发送到 channel 的字符串是 "data: " 之后的 payload（已去除前缀和空白）。
// 遇到 "[DONE]" 标记、EOF 或 ctx 取消时关闭 channel。
// "[DONE]" 本身不会发送到 channel。
func ReadSSEEvents(ctx context.Context, reader *bufio.Reader) <-chan string {
	ch := make(chan string, 100)
	go func() {
		defer close(ch)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			line, err := reader.ReadString('\n')
			if err != nil {
				if err == io.EOF && strings.TrimSpace(line) != "" {
					// Handle last line without trailing newline
					line = strings.TrimSpace(line)
					if strings.HasPrefix(line, "data: ") {
						data := strings.TrimPrefix(line, "data: ")
						if data != "[DONE]" && data != "" {
							ch <- data
						}
					}
				}
				return
			}

			line = strings.TrimSpace(line)
			if line == "" || !strings.HasPrefix(line, "data: ") {
				continue
			}

			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" || data == "" {
				return
			}

			ch <- data
		}
	}()
	return ch
}
