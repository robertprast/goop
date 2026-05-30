package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// codexCLIClientID is the public OAuth client_id baked into the Codex CLI.
// The refresh endpoint validates it against the issued refresh token.
const codexCLIClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

// OpenAIOAuthSource holds and refreshes a Codex ChatGPT OAuth credential pair
// (access + refresh) plus the associated chatgpt-account-id, which the backend
// requires on every request. Concurrent callers get the same in-memory tokens;
// refresh fires when the JWT's exp claim is within Skew of now.
//
// File layout matches ~/.codex/auth.json so the official CLI can share the
// same source of truth on disk:
//
//	{
//	  "auth_mode": "chatgpt",
//	  "OPENAI_API_KEY": null,
//	  "tokens": {
//	    "id_token": "...", "access_token": "...", "refresh_token": "...",
//	    "account_id": "..."
//	  },
//	  "last_refresh": "<RFC3339>"
//	}
type OpenAIOAuthSource struct {
	mu           sync.RWMutex
	idToken      string
	accessToken  string
	refreshToken string
	accountID    string
	expiresAt    time.Time

	path       string
	refreshURL string
	clientID   string
	skew       time.Duration
	httpClient *http.Client
	log        *slog.Logger
}

// OpenAIOAuthConfig configures the source.
type OpenAIOAuthConfig struct {
	// CredentialsFile is the path to ~/.codex/auth.json. Required.
	CredentialsFile string
	// RefreshURL overrides the token endpoint (default: auth.openai.com).
	RefreshURL string
	// Skew is how far before JWT exp to proactively refresh (default 5m).
	Skew time.Duration
	// Logger; defaults to slog.Default().
	Logger *slog.Logger
}

// NewOpenAIOAuthSource loads the credentials file and starts a keepalive
// goroutine that refreshes ~Skew before the access JWT's exp. Cancel ctx
// to stop the keepalive.
func NewOpenAIOAuthSource(ctx context.Context, cfg OpenAIOAuthConfig) (*OpenAIOAuthSource, error) {
	if cfg.CredentialsFile == "" {
		return nil, fmt.Errorf("openai-oauth: credentials_file is required")
	}
	skew := cfg.Skew
	if skew <= 0 {
		skew = 5 * time.Minute
	}
	refreshURL := cfg.RefreshURL
	if refreshURL == "" {
		refreshURL = "https://auth.openai.com/oauth/token"
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	s := &OpenAIOAuthSource{
		path:       cfg.CredentialsFile,
		refreshURL: refreshURL,
		clientID:   codexCLIClientID,
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

// Token returns a fresh access token + the associated chatgpt account id,
// refreshing synchronously if the cached access token is within skew of expiry.
func (s *OpenAIOAuthSource) Token(ctx context.Context) (accessToken, accountID string, err error) {
	s.mu.RLock()
	tok, acct, exp := s.accessToken, s.accountID, s.expiresAt
	s.mu.RUnlock()
	if tok != "" && time.Until(exp) > s.skew {
		return tok, acct, nil
	}
	if err := s.refresh(ctx); err != nil {
		return "", "", err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.accessToken, s.accountID, nil
}

func (s *OpenAIOAuthSource) load() error {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return fmt.Errorf("openai-oauth: read %s: %w", s.path, err)
	}
	var f codexAuthFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return fmt.Errorf("openai-oauth: parse %s: %w", s.path, err)
	}
	if f.Tokens.AccessToken == "" || f.Tokens.RefreshToken == "" {
		return fmt.Errorf("openai-oauth: %s missing access_token/refresh_token", s.path)
	}
	exp, err := jwtExpiry(f.Tokens.AccessToken)
	if err != nil {
		// Treat as expired so we refresh immediately rather than fail startup.
		s.log.Warn("openai-oauth: could not parse JWT exp, forcing refresh", "err", err)
		exp = time.Now().Add(-time.Hour)
	}
	s.mu.Lock()
	s.idToken = f.Tokens.IDToken
	s.accessToken = f.Tokens.AccessToken
	s.refreshToken = f.Tokens.RefreshToken
	s.accountID = f.Tokens.AccountID
	s.expiresAt = exp
	s.mu.Unlock()
	return nil
}

func (s *OpenAIOAuthSource) refresh(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Another goroutine may have already refreshed under the lock.
	if s.accessToken != "" && time.Until(s.expiresAt) > s.skew {
		return nil
	}
	body, _ := json.Marshal(openaiRefreshRequest{
		ClientID:     s.clientID,
		GrantType:    "refresh_token",
		RefreshToken: s.refreshToken,
		// Match the scopes the Codex CLI itself requests on refresh
		// (codex-rs/login/src/server.rs). Omitting the api.connectors.*
		// scopes silently narrows the token on every rotation.
		Scope: "openid profile email offline_access api.connectors.read api.connectors.invoke",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.refreshURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("openai-oauth: build refresh: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("openai-oauth: refresh request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("openai-oauth: refresh HTTP %d: %s", resp.StatusCode, truncate(string(b), 256))
	}
	var rr openaiRefreshResponse
	if err := json.NewDecoder(resp.Body).Decode(&rr); err != nil {
		return fmt.Errorf("openai-oauth: decode refresh: %w", err)
	}
	if rr.AccessToken == "" {
		return fmt.Errorf("openai-oauth: refresh returned empty access_token")
	}
	s.idToken = rr.IDToken
	s.accessToken = rr.AccessToken
	if rr.RefreshToken != "" {
		s.refreshToken = rr.RefreshToken
	}
	exp, err := jwtExpiry(rr.AccessToken)
	if err != nil {
		// Fall back to a short window so we refresh again soon rather than
		// trust a token whose lifetime we can't read.
		exp = time.Now().Add(10 * time.Minute)
		s.log.Warn("openai-oauth: refreshed token has unparseable exp; will retry soon", "err", err)
	}
	s.expiresAt = exp
	if err := s.persistLocked(); err != nil {
		s.log.Warn("openai-oauth: persist refreshed token failed", "err", err, "path", s.path)
	} else {
		s.log.Info("openai-oauth: refreshed token", "expires_at", s.expiresAt)
	}
	return nil
}

// persistLocked writes the current state back to disk atomically. The file
// shape mirrors what the Codex CLI writes so the same file can be used by
// either side. Caller must hold s.mu.
func (s *OpenAIOAuthSource) persistLocked() error {
	f := codexAuthFile{
		AuthMode:     "chatgpt",
		OpenAIAPIKey: nil,
		Tokens: codexTokens{
			IDToken:      s.idToken,
			AccessToken:  s.accessToken,
			RefreshToken: s.refreshToken,
			AccountID:    s.accountID,
		},
		LastRefresh: time.Now().UTC().Format(time.RFC3339Nano),
	}
	data, err := json.MarshalIndent(&f, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *OpenAIOAuthSource) keepalive(ctx context.Context) {
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
				s.log.Warn("openai-oauth: keepalive refresh failed", "err", err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(2 * time.Minute):
				}
			}
		}
	}
}

// jwtExpiry extracts the `exp` claim from an unverified JWT. We don't need to
// verify the signature — the only consumer is the refresh-timing decision, and
// the worst-case mistake is "we refresh too eagerly," which is harmless.
func jwtExpiry(token string) (time.Time, error) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return time.Time{}, fmt.Errorf("not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, fmt.Errorf("decode payload: %w", err)
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return time.Time{}, fmt.Errorf("parse claims: %w", err)
	}
	if claims.Exp == 0 {
		return time.Time{}, fmt.Errorf("no exp claim")
	}
	return time.Unix(claims.Exp, 0), nil
}

// --- on-disk schema (mirrors ~/.codex/auth.json) ---

type codexAuthFile struct {
	AuthMode     string      `json:"auth_mode"`
	OpenAIAPIKey *string     `json:"OPENAI_API_KEY"`
	Tokens       codexTokens `json:"tokens"`
	LastRefresh  string      `json:"last_refresh"`
}

type codexTokens struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	AccountID    string `json:"account_id"`
}

// --- token endpoint wire types ---

type openaiRefreshRequest struct {
	ClientID     string `json:"client_id"`
	GrantType    string `json:"grant_type"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
}

type openaiRefreshResponse struct {
	IDToken      string `json:"id_token,omitempty"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	TokenType    string `json:"token_type,omitempty"`
	ExpiresIn    int64  `json:"expires_in,omitempty"`
}
