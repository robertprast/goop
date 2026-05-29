package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"
)

// Claude Code's public OAuth client_id, used by the official CLI. The refresh
// endpoint validates it against the issued refresh token's audience.
const claudeCodeClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"

// AnthropicOAuthSource holds and refreshes a Claude Code OAuth credential pair
// (access + refresh token). Concurrent callers get the same in-memory token;
// refresh happens lazily on access when the token is near expiry and in a
// background goroutine for keep-alive between requests.
//
// File layout matches ~/.claude/.credentials.json so a single source of truth
// can be shared with the official CLI:
//
//	{"claudeAiOauth": {"accessToken": "...", "refreshToken": "...",
//	                   "expiresAt": <unix ms>, ...}}
type AnthropicOAuthSource struct {
	mu           sync.RWMutex
	accessToken  string
	refreshToken string
	expiresAt    time.Time
	// preserved across refreshes so we write back a complete file
	scopes           []string
	subscriptionType string
	rateLimitTier    string

	path     string // credentials file path; refreshed tokens are written here
	refreshURL string
	clientID   string
	skew       time.Duration // refresh this far before expiry
	httpClient *http.Client
	log        *slog.Logger
}

// AnthropicOAuthConfig configures the credential source.
type AnthropicOAuthConfig struct {
	// CredentialsFile is the path to ~/.claude/.credentials.json (or equivalent).
	// Must exist at startup; refreshed tokens are written back atomically.
	CredentialsFile string
	// RefreshURL overrides the token endpoint (default: console.anthropic.com).
	RefreshURL string
	// Skew is how far before expiry to proactively refresh (default 5m).
	Skew time.Duration
	// Logger; defaults to slog.Default().
	Logger *slog.Logger
}

// NewAnthropicOAuthSource loads the credentials file and starts a background
// keep-alive that refreshes the token ~Skew before expiry. The returned source
// is safe for concurrent use; cancel ctx to stop the keep-alive.
func NewAnthropicOAuthSource(ctx context.Context, cfg AnthropicOAuthConfig) (*AnthropicOAuthSource, error) {
	if cfg.CredentialsFile == "" {
		return nil, fmt.Errorf("anthropic-oauth: credentials_file is required")
	}
	skew := cfg.Skew
	if skew <= 0 {
		skew = 5 * time.Minute
	}
	refreshURL := cfg.RefreshURL
	if refreshURL == "" {
		refreshURL = "https://console.anthropic.com/v1/oauth/token"
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	s := &AnthropicOAuthSource{
		path:       cfg.CredentialsFile,
		refreshURL: refreshURL,
		clientID:   claudeCodeClientID,
		skew:       skew,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		log:        log,
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	go s.keepalive(ctx)
	return s, nil
}

// Token returns a fresh access token, refreshing synchronously if the cached
// one is within skew of expiry.
func (s *AnthropicOAuthSource) Token(ctx context.Context) (string, error) {
	s.mu.RLock()
	tok, exp := s.accessToken, s.expiresAt
	s.mu.RUnlock()
	if tok != "" && time.Until(exp) > s.skew {
		return tok, nil
	}
	if err := s.refresh(ctx); err != nil {
		return "", err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.accessToken, nil
}

// load reads the credentials file into memory.
func (s *AnthropicOAuthSource) load() error {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return fmt.Errorf("anthropic-oauth: read %s: %w", s.path, err)
	}
	var f credentialsFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return fmt.Errorf("anthropic-oauth: parse %s: %w", s.path, err)
	}
	o := f.ClaudeAiOauth
	if o.AccessToken == "" || o.RefreshToken == "" {
		return fmt.Errorf("anthropic-oauth: %s missing accessToken/refreshToken", s.path)
	}
	s.mu.Lock()
	s.accessToken = o.AccessToken
	s.refreshToken = o.RefreshToken
	s.expiresAt = time.UnixMilli(o.ExpiresAt)
	s.scopes = o.Scopes
	s.subscriptionType = o.SubscriptionType
	s.rateLimitTier = o.RateLimitTier
	s.mu.Unlock()
	return nil
}

// refresh exchanges the refresh token for a new access token and persists it.
// Concurrent refreshes are serialized via the write lock.
func (s *AnthropicOAuthSource) refresh(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Re-check under the lock; another goroutine may have already refreshed.
	if s.accessToken != "" && time.Until(s.expiresAt) > s.skew {
		return nil
	}
	body, _ := json.Marshal(refreshRequest{
		GrantType:    "refresh_token",
		RefreshToken: s.refreshToken,
		ClientID:     s.clientID,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.refreshURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("anthropic-oauth: build refresh: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("anthropic-oauth: refresh request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("anthropic-oauth: refresh HTTP %d: %s", resp.StatusCode, truncate(string(b), 256))
	}
	var rr refreshResponse
	if err := json.NewDecoder(resp.Body).Decode(&rr); err != nil {
		return fmt.Errorf("anthropic-oauth: decode refresh: %w", err)
	}
	if rr.AccessToken == "" {
		return fmt.Errorf("anthropic-oauth: refresh returned empty access_token")
	}
	s.accessToken = rr.AccessToken
	if rr.RefreshToken != "" {
		s.refreshToken = rr.RefreshToken
	}
	if rr.ExpiresIn > 0 {
		s.expiresAt = time.Now().Add(time.Duration(rr.ExpiresIn) * time.Second)
	}
	if err := s.persistLocked(); err != nil {
		// Don't fail the request — the in-memory token is good; just warn.
		s.log.Warn("anthropic-oauth: persist refreshed token failed", "err", err, "path", s.path)
	} else {
		s.log.Info("anthropic-oauth: refreshed token", "expires_at", s.expiresAt)
	}
	return nil
}

// persistLocked writes the current state back to the credentials file using
// atomic rename. Caller must hold s.mu.
func (s *AnthropicOAuthSource) persistLocked() error {
	f := credentialsFile{ClaudeAiOauth: claudeAiOauth{
		AccessToken:      s.accessToken,
		RefreshToken:     s.refreshToken,
		ExpiresAt:        s.expiresAt.UnixMilli(),
		Scopes:           s.scopes,
		SubscriptionType: s.subscriptionType,
		RateLimitTier:    s.rateLimitTier,
	}}
	data, err := json.Marshal(&f)
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// keepalive proactively refreshes the token in the background so requests
// never block on a network round-trip for credentials.
func (s *AnthropicOAuthSource) keepalive(ctx context.Context) {
	for {
		s.mu.RLock()
		wait := time.Until(s.expiresAt) - s.skew
		s.mu.RUnlock()
		if wait < time.Minute {
			wait = time.Minute
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
			if err := s.refresh(ctx); err != nil {
				s.log.Warn("anthropic-oauth: keepalive refresh failed", "err", err)
				// Back off but keep trying.
				select {
				case <-ctx.Done():
					return
				case <-time.After(2 * time.Minute):
				}
			}
		}
	}
}

// --- on-disk schema (mirrors ~/.claude/.credentials.json) ---

type credentialsFile struct {
	ClaudeAiOauth claudeAiOauth `json:"claudeAiOauth"`
}

type claudeAiOauth struct {
	AccessToken      string   `json:"accessToken"`
	RefreshToken     string   `json:"refreshToken"`
	ExpiresAt        int64    `json:"expiresAt"` // unix ms
	Scopes           []string `json:"scopes,omitempty"`
	SubscriptionType string   `json:"subscriptionType,omitempty"`
	RateLimitTier    string   `json:"rateLimitTier,omitempty"`
}

// --- token endpoint wire types ---

type refreshRequest struct {
	GrantType    string `json:"grant_type"`
	RefreshToken string `json:"refresh_token"`
	ClientID     string `json:"client_id"`
}

type refreshResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresIn    int64  `json:"expires_in,omitempty"`
	TokenType    string `json:"token_type,omitempty"`
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
