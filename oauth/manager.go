package oauth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	log "xbot/logger"
)

// Manager is the central OAuth state manager.
// It handles pending OAuth flows and token storage for all providers.
type Manager struct {
	mu        sync.RWMutex
	providers map[string]Provider
	flows     map[string]*Flow
	storage   TokenStorage
}

// GetStorage returns the token storage (for tool managers)
func (m *Manager) GetStorage() TokenStorage {
	return m.storage
}

// Flow represents a pending OAuth flow.
// When a user starts an OAuth authorization, a flow is created
// and tracked until completion or timeout.
type Flow struct {
	State     string    // Unique state token for CSRF protection
	Provider  string    // Provider name (e.g., "feishu")
	Channel   string    // Channel identifier (e.g., "feishu")
	ChatID    string    // Chat/session identifier
	Scopes    []string  // Requested OAuth scopes
	CreatedAt time.Time // When the flow was created
}

// NewManager creates a new OAuth manager.
func NewManager(storage TokenStorage) *Manager {
	return &Manager{
		providers: make(map[string]Provider),
		flows:     make(map[string]*Flow),
		storage:   storage,
	}
}

// RegisterProvider registers an OAuth provider.
func (m *Manager) RegisterProvider(p Provider) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.providers[p.Name()] = p
	log.WithField("provider", p.Name()).Info("OAuth provider registered")
}

// GetProvider returns a registered provider by name.
func (m *Manager) GetProvider(name string) (Provider, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.providers[name]
	return p, ok
}

// StartFlow initiates an OAuth flow and returns the auth URL.
// The caller should direct the user to visit this URL to authorize.
func (m *Manager) StartFlow(providerName, channel, chatID string, scopes []string) (authURL, state string, err error) {
	provider, ok := m.GetProvider(providerName)
	if !ok {
		return "", "", fmt.Errorf("unknown provider: %s", providerName)
	}

	state, err = m.generateState()
	if err != nil {
		return "", "", fmt.Errorf("generate state: %w", err)
	}

	authURL = provider.BuildAuthURL(state, scopes)

	flow := &Flow{
		State:     state,
		Provider:  providerName,
		Channel:   channel,
		ChatID:    chatID,
		Scopes:    scopes,
		CreatedAt: time.Now(),
	}

	m.mu.Lock()
	m.flows[state] = flow
	m.mu.Unlock()

	log.WithFields(log.Fields{
		"provider": providerName,
		"state":    state,
		"channel":  channel,
		"chat_id":  chatID,
	}).Info("OAuth flow started")

	return authURL, state, nil
}

// GetFlow retrieves a pending flow by state token.
func (m *Manager) GetFlow(state string) (*Flow, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	flow, ok := m.flows[state]
	return flow, ok
}

// CompleteFlow exchanges the authorization code for tokens and stores them.
// This is called when the OAuth provider redirects back with a code.
func (m *Manager) CompleteFlow(ctx context.Context, state, code string) (*Token, error) {
	flow, ok := m.GetFlow(state)
	if !ok {
		return nil, fmt.Errorf("invalid or expired state token")
	}

	provider, ok := m.GetProvider(flow.Provider)
	if !ok {
		return nil, fmt.Errorf("provider not found: %s", flow.Provider)
	}

	// Exchange code for token
	token, err := provider.ExchangeCode(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("exchange code: %w", err)
	}

	// Store token
	if err := m.storage.SetToken(ctx, flow.Provider, flow.Channel, flow.ChatID, token); err != nil {
		return nil, fmt.Errorf("store token: %w", err)
	}

	// Remove completed flow
	m.mu.Lock()
	delete(m.flows, state)
	m.mu.Unlock()

	log.WithFields(log.Fields{
		"provider": flow.Provider,
		"channel":  flow.Channel,
		"chat_id":  flow.ChatID,
	}).Info("OAuth flow completed, token stored")

	return token, nil
}

// GetToken retrieves stored tokens for a session.
func (m *Manager) GetToken(providerName, channel, chatID string) (*Token, error) {
	return m.storage.GetToken(context.Background(), providerName, channel, chatID)
}

// SetToken stores tokens for a session.
func (m *Manager) SetToken(providerName, channel, chatID string, token *Token) error {
	return m.storage.SetToken(context.Background(), providerName, channel, chatID, token)
}

// GetClientForSession returns a provider client, auto-refreshing if needed.
// If no valid token exists, returns TokenNeededError.
func (m *Manager) GetClientForSession(ctx context.Context, providerName, channel, chatID string) (any, error) {
	provider, ok := m.GetProvider(providerName)
	if !ok {
		return nil, fmt.Errorf("provider not found: %s", providerName)
	}

	// Get existing token
	token, err := m.GetToken(providerName, channel, chatID)
	if err != nil {
		return nil, fmt.Errorf("get token: %w", err)
	}

	// Check if we need to request authorization
	if token == nil {
		return nil, &TokenNeededError{
			Provider: providerName,
			Channel:  channel,
			ChatID:   chatID,
			Reason:   "missing",
		}
	}

	// Check if token needs refresh
	if token.NeedsRefresh() && token.RefreshToken != "" {
		log.WithFields(log.Fields{
			"provider": providerName,
			"channel":  channel,
			"chat_id":  chatID,
		}).Info("Token needs refresh, attempting auto-refresh")

		newToken, err := provider.RefreshToken(ctx, token.RefreshToken)
		if err != nil {
			log.WithError(err).Warn("Failed to refresh token, user needs to re-authorize")
			return nil, &TokenNeededError{
				Provider: providerName,
				Channel:  channel,
				ChatID:   chatID,
				Reason:   "expired",
			}
		}

		// Store refreshed token
		if err := m.SetToken(providerName, channel, chatID, newToken); err != nil {
			log.WithError(err).Warn("Failed to store refreshed token")
		}
		token = newToken

		log.WithFields(log.Fields{
			"provider": providerName,
			"channel":  channel,
			"chat_id":  chatID,
		}).Info("Token refreshed successfully")
	}

	// Check if token is valid
	if !token.IsValid() {
		return nil, &TokenNeededError{
			Provider: providerName,
			Channel:  channel,
			ChatID:   chatID,
			Reason:   "expired",
		}
	}

	return provider.GetClient(token.AccessToken), nil
}

// CleanupExpiredFlows removes old pending flows.
// This should be called periodically to prevent memory leaks.
func (m *Manager) CleanupExpiredFlows(timeout time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	cutoff := time.Now().Add(-timeout)
	for state, flow := range m.flows {
		if flow.CreatedAt.Before(cutoff) {
			delete(m.flows, state)
			log.WithFields(log.Fields{
				"state":    state,
				"provider": flow.Provider,
			}).Debug("Cleaned up expired OAuth flow")
		}
	}
}

// generateState creates a secure random state token for CSRF protection.
func (m *Manager) generateState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// ListProviders returns all registered provider names.
func (m *Manager) ListProviders() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	names := make([]string, 0, len(m.providers))
	for name := range m.providers {
		names = append(names, name)
	}
	return names
}

// DeleteFlow removes a pending flow (e.g., if user cancels).
func (m *Manager) DeleteFlow(state string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.flows[state]; ok {
		delete(m.flows, state)
		return true
	}
	return false
}
