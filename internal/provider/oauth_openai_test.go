package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// mintJWT builds an unsigned JWT whose payload carries `exp` so jwtExpiry can
// parse it. The header+signature are placeholders — we never verify.
func mintJWT(t *testing.T, exp time.Time) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload, _ := json.Marshal(struct {
		Exp int64 `json:"exp"`
	}{Exp: exp.Unix()})
	body := base64.RawURLEncoding.EncodeToString(payload)
	sig := base64.RawURLEncoding.EncodeToString([]byte("not-a-real-signature"))
	return header + "." + body + "." + sig
}

func fakeOpenAIRefresh(t *testing.T, newExp time.Time) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("refresh: method %s", r.Method)
		}
		var req openaiRefreshRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("refresh: decode: %v", err)
		}
		if req.GrantType != "refresh_token" {
			t.Errorf("refresh: grant_type %q", req.GrantType)
		}
		if req.ClientID != codexCLIClientID {
			t.Errorf("refresh: client_id %q", req.ClientID)
		}
		if req.RefreshToken == "" {
			t.Errorf("refresh: empty refresh_token")
		}
		atomic.AddInt32(&calls, 1)
		_ = json.NewEncoder(w).Encode(openaiRefreshResponse{
			IDToken:      "new-id-token",
			AccessToken:  mintJWT(t, newExp),
			RefreshToken: "new-refresh-token",
			TokenType:    "Bearer",
			ExpiresIn:    3600,
		})
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func writeCodexCreds(t *testing.T, accessExp time.Time) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	f := codexAuthFile{
		AuthMode: "chatgpt",
		Tokens: codexTokens{
			IDToken:      "initial-id",
			AccessToken:  mintJWT(t, accessExp),
			RefreshToken: "initial-refresh",
			AccountID:    "acct-test",
		},
		LastRefresh: time.Unix(1, 0).UTC().Format(time.RFC3339Nano),
	}
	data, _ := json.MarshalIndent(&f, "", "  ")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOpenAIOAuthSource_NoRefreshWhenFresh(t *testing.T) {
	srv, calls := fakeOpenAIRefresh(t, time.Now().Add(2*time.Hour))
	path := writeCodexCreds(t, time.Now().Add(1*time.Hour))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, err := NewOpenAIOAuthSource(ctx, OpenAIOAuthConfig{
		CredentialsFile: path,
		RefreshURL:      srv.URL,
		Skew:            5 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	tok, acct, err := s.Token(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if tok == "" {
		t.Error("empty access token")
	}
	if acct != "acct-test" {
		t.Errorf("account_id: got %q want acct-test", acct)
	}
	if atomic.LoadInt32(calls) != 0 {
		t.Errorf("refresh: called %d times, want 0", atomic.LoadInt32(calls))
	}
}

func TestOpenAIOAuthSource_SyncRefreshWhenNearExpiry(t *testing.T) {
	newExp := time.Now().Add(2 * time.Hour)
	srv, calls := fakeOpenAIRefresh(t, newExp)
	// Original token expires in 30s; skew is 5m → must refresh on Token().
	path := writeCodexCreds(t, time.Now().Add(30*time.Second))
	originalToken := readCodex(t, path).Tokens.AccessToken

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, err := NewOpenAIOAuthSource(ctx, OpenAIOAuthConfig{
		CredentialsFile: path,
		RefreshURL:      srv.URL,
		Skew:            5 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := s.Token(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if tok == originalToken {
		t.Error("Token did not refresh near expiry")
	}
	if atomic.LoadInt32(calls) < 1 {
		t.Errorf("refresh: called %d times, want >=1", atomic.LoadInt32(calls))
	}

	got := readCodex(t, path)
	if got.Tokens.AccessToken == originalToken {
		t.Error("persisted file still has the original access_token")
	}
	if got.Tokens.RefreshToken != "new-refresh-token" {
		t.Errorf("refresh_token not rotated on disk: %q", got.Tokens.RefreshToken)
	}
	if got.Tokens.AccountID != "acct-test" {
		t.Errorf("account_id changed across refresh: %q", got.Tokens.AccountID)
	}
}

func TestJWTExpiry(t *testing.T) {
	want := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	tok := mintJWT(t, want)
	got, err := jwtExpiry(tok)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(want) {
		t.Errorf("exp: got %v want %v", got, want)
	}
}

func TestJWTExpiry_Garbage(t *testing.T) {
	if _, err := jwtExpiry("not-a-jwt"); err == nil {
		t.Error("expected error on garbage input")
	}
}

func readCodex(t *testing.T, path string) codexAuthFile {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f codexAuthFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return f
}
