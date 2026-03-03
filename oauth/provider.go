package oauth

import (
	"context"
	"time"
)

// Provider defines the interface for OAuth providers.
// Each OAuth provider (Feishu, GitHub, Google, etc.) implements this interface.
type Provider interface {
	// Name returns the unique provider identifier (e.g., "feishu", "github")
	Name() string

	// BuildAuthURL generates the authorization URL for the user to visit.
	// The state parameter is used for CSRF protection.
	BuildAuthURL(state string, scopes []string) string

	// ExchangeCode exchanges the authorization code for tokens.
	// This is called when the user completes the OAuth flow.
	ExchangeCode(ctx context.Context, code string) (*Token, error)

	// RefreshToken uses a refresh token to get a new access token.
	// If the provider doesn't support refresh tokens, it can return an error.
	RefreshToken(ctx context.Context, refreshToken string) (*Token, error)

	// GetClient returns a provider-specific client using the access token.
	// The return type is any to allow different client types.
	GetClient(accessToken string) any
}

// Token represents OAuth credentials.
type Token struct {
	AccessToken  string         // Access token for API calls
	RefreshToken string         // Token to refresh the access token
	ExpiresAt    time.Time      // When the access token expires
	Scopes       []string       // Granted OAuth scopes
	Raw          map[string]any // Provider-specific data
}

// IsValid checks if the token is still valid (not expired).
func (t *Token) IsValid() bool {
	if t == nil || t.AccessToken == "" {
		return false
	}
	// Add 30 second buffer to avoid edge cases
	return time.Now().Add(30 * time.Second).Before(t.ExpiresAt)
}

// NeedsRefresh checks if the token should be refreshed.
// Returns true if the token expires in less than 5 minutes.
func (t *Token) NeedsRefresh() bool {
	if t == nil || t.RefreshToken == "" {
		return false
	}
	return time.Until(t.ExpiresAt) < 5*time.Minute
}

// TokenNeededError is returned when a tool requires OAuth authorization
// but no valid token is available for the session.
type TokenNeededError struct {
	Provider string   // Provider name (e.g., "feishu", "github")
	Channel  string   // Channel identifier
	ChatID   string   // Chat/session identifier
	Reason   string   // Why authorization is needed ("missing", "expired", "insufficient_scope")
	Scopes   []string // Required OAuth scopes
}

// Error returns a human-readable error message.
func (e *TokenNeededError) Error() string {
	reason := e.Reason
	if reason == "" {
		reason = "missing"
	}
	return "oauth token needed: " + reason + " for provider " + e.Provider
}

// IsTokenNeededError checks if an error is a TokenNeededError.
func IsTokenNeededError(err error) bool {
	_, ok := err.(*TokenNeededError)
	return ok
}
