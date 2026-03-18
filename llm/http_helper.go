package llm

import (
	"fmt"
	"io"
	"net/http"
)

// doLLMRequest 执行 HTTP 请求并检查状态码。
// 成功时返回 response（调用方负责关闭 Body）。
// 非 200 状态码时读取 body 并返回格式化错误。
func doLLMRequest(client *http.Client, req *http.Request, provider string) (*http.Response, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("%s API error: status=%d, body=%s", provider, resp.StatusCode, string(body))
	}
	return resp, nil
}
