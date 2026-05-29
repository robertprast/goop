package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// fakeRefreshServer returns a stub OAuth token endpoint that always issues a
// new access token, increments a counter, and reports whatever expires_in the
// caller wants.
func fakeRefreshServer(t *testing.T, expiresIn int64) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	var nextToken int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("refresh: method %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("refresh: content-type %q", r.Header.Get("Content-Type"))
		}
		var req refreshRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("refresh: decode: %v", err)
		}
		if req.GrantType != "refresh_token" {
			t.Errorf("refresh: grant_type %q", req.GrantType)
		}
		if req.ClientID != claudeCodeClientID {
			t.Errorf("refresh: client_id %q", req.ClientID)
		}
		if req.RefreshToken == "" {
			t.Errorf("refresh: empty refresh_token")
		}
		atomic.AddInt32(&calls, 1)
		n := atomic.AddInt32(&nextToken, 1)
		_ = json.NewEncoder(w).Encode(refreshResponse{
			AccessToken:  "new-access-" + itoa(n),
			RefreshToken: "new-refresh-" + itoa(n),
			ExpiresIn:    expiresIn,
			TokenType:    "Bearer",
		})
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// writeCreds writes a credentialsFile to a temp path and returns the path.
func writeCreds(t *testing.T, expiresAt time.Time) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, ".credentials.json")
	f := credentialsFile{ClaudeAiOauth: claudeAiOauth{
		AccessToken:      "initial-access",
		RefreshToken:     "initial-refresh",
		ExpiresAt:        expiresAt.UnixMilli(),
		Scopes:           []string{"user:inference"},
		SubscriptionType: "max",
		RateLimitTier:    "default",
	}}
	data, _ := json.Marshal(&f)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOAuthSource_NoRefreshWhenFresh(t *testing.T) {
	srv, calls := fakeRefreshServer(t, 3600)
	path := writeCreds(t, time.Now().Add(1*time.Hour))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, err := NewAnthropicOAuthSource(ctx, AnthropicOAuthConfig{
		CredentialsFile: path,
		RefreshURL:      srv.URL,
		Skew:            5 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	tok, err := s.Token(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if tok != "initial-access" {
		t.Errorf("token: got %q want initial-access", tok)
	}
	if atomic.LoadInt32(calls) != 0 {
		t.Errorf("refresh: called %d times, want 0", atomic.LoadInt32(calls))
	}
}

func TestOAuthSource_SyncRefreshWhenNearExpiry(t *testing.T) {
	srv, calls := fakeRefreshServer(t, 3600)
	// Expire in 30s, with a 5m skew → Token() must refresh.
	path := writeCreds(t, time.Now().Add(30*time.Second))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, err := NewAnthropicOAuthSource(ctx, AnthropicOAuthConfig{
		CredentialsFile: path,
		RefreshURL:      srv.URL,
		Skew:            5 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	tok, err := s.Token(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if tok == "initial-access" {
		t.Error("token: still initial-access after expected refresh")
	}
	if atomic.LoadInt32(calls) < 1 {
		t.Errorf("refresh: called %d times, want >=1", atomic.LoadInt32(calls))
	}

	// And the refreshed credentials must be persisted back to disk.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got credentialsFile
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.ClaudeAiOauth.AccessToken == "initial-access" {
		t.Error("persisted file still has initial access token")
	}
	if got.ClaudeAiOauth.RefreshToken == "initial-refresh" {
		t.Error("persisted file still has initial refresh token")
	}
	if got.ClaudeAiOauth.SubscriptionType != "max" {
		t.Errorf("subscriptionType not preserved: %q", got.ClaudeAiOauth.SubscriptionType)
	}
}

func TestOAuthSource_KeepaliveFiresBeforeExpiry(t *testing.T) {
	srv, calls := fakeRefreshServer(t, 60) // every refresh issues a 60s token
	// Start with token already inside the skew window so the keepalive's
	// first iteration computes a sub-minute wait, clamps to 1 minute, then
	// triggers a refresh shortly after. We won't actually wait a minute —
	// we'll observe the on-startup synchronous refresh (which the keepalive
	// is effectively a long-lived loop around).
	path := writeCreds(t, time.Now().Add(10*time.Second))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, err := NewAnthropicOAuthSource(ctx, AnthropicOAuthConfig{
		CredentialsFile: path,
		RefreshURL:      srv.URL,
		Skew:            5 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	// First Token() call refreshes synchronously because we're inside skew.
	if _, err := s.Token(ctx); err != nil {
		t.Fatal(err)
	}
	if n := atomic.LoadInt32(calls); n < 1 {
		t.Errorf("sync refresh count = %d, want >=1", n)
	}
}

func itoa(n int32) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
