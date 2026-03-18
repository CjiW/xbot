package tools

import (
	"encoding/json"
	"fmt"
)

// parseToolArgs parses JSON input into a typed args struct.
// This is a generic helper to reduce boilerplate in tool Execute methods.
func parseToolArgs[T any](input string) (*T, error) {
	var args T
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return nil, fmt.Errorf("parse args: %w", err)
	}
	return &args, nil
}
