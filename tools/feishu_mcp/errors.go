package feishu_mcp

import (
	"fmt"
)

// APIError represents a Feishu API error response.
type APIError struct {
	Code    int    `json:"code"`
	Msg     string `json:"msg"`
	Details string // Additional error details
}

// Error returns a human-readable error message.
func (e *APIError) Error() string {
	if e.Details != "" {
		return fmt.Sprintf("Feishu API error: %s (code: %d, details: %s)", e.Msg, e.Code, e.Details)
	}
	return fmt.Sprintf("Feishu API error: %s (code: %d)", e.Msg, e.Code)
}

// NewAPIError creates a new APIError.
func NewAPIError(code int, msg string) *APIError {
	return &APIError{Code: code, Msg: msg}
}

// NewAPIErrorWithDetails creates a new APIError with additional details.
func NewAPIErrorWithDetails(code int, msg, details string) *APIError {
	return &APIError{Code: code, Msg: msg, Details: details}
}

// IsAPIError checks if an error is an APIError.
func IsAPIError(err error) bool {
	_, ok := err.(*APIError)
	return ok
}

// Common Feishu API error codes
const (
	ErrCodeInvalidToken     = 99991663 // Invalid access token
	ErrCodeTokenExpired     = 99991663 // Token expired
	ErrCodePermissionDenied = 99991404 // No permission
	ErrCodeResourceNotFound = 99991403 // Resource not found
)

// IsTokenError checks if the error is related to authentication/authorization.
func IsTokenError(err error) bool {
	if apiErr, ok := err.(*APIError); ok {
		return apiErr.Code == ErrCodeInvalidToken || apiErr.Code == ErrCodeTokenExpired
	}
	return false
}

// IsPermissionError checks if the error is related to permissions.
func IsPermissionError(err error) bool {
	if apiErr, ok := err.(*APIError); ok {
		return apiErr.Code == ErrCodePermissionDenied
	}
	return false
}

// IsNotFoundError checks if the error is related to a missing resource.
func IsNotFoundError(err error) bool {
	if apiErr, ok := err.(*APIError); ok {
		return apiErr.Code == ErrCodeResourceNotFound
	}
	return false
}

// BuildFeishuURL constructs a Feishu URL for a given token and type.
// The domain is a placeholder since we don't have the user's domain from the API.
func BuildFeishuURL(token, objType string) string {
	if token == "" {
		return ""
	}

	// Determine URL path based on object type
	var path string
	switch {
	case len(token) >= 5 && token[:5] == "wikcn":
		path = "/wiki/" + token
	case len(token) >= 5 && token[:5] == "doxcn":
		path = "/docx/" + token
	case len(token) >= 4 && token[:4] == "basc":
		path = "/base/" + token
	case len(token) >= 4 && token[:4] == "basc":
		path = "/base/" + token
	case len(token) >= 4 && token[:4] == "sheet":
		path = "/sheets/" + token
	default:
		// Default based on objType
		switch objType {
		case "docx", "doc":
			path = "/docx/" + token
		case "wiki":
			path = "/wiki/" + token
		case "bitable", "base":
			path = "/base/" + token
		case "sheet":
			path = "/sheets/" + token
		default:
			path = "/docx/" + token
		}
	}

	return fmt.Sprintf("https://your-domain.feishu.cn%s", path)
}
