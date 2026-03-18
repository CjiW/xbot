package tools

import (
	"encoding/json"
	"fmt"
)

// ParseInput 解析工具输入 JSON 为指定类型。
// 统一所有工具的 JSON 解析错误处理。
func ParseInput[T any](input string) (T, error) {
	var params T
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return params, fmt.Errorf("parse input: %w", err)
	}
	return params, nil
}
