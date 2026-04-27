// Package auth implements goop's single shared-bearer authentication.
// If the configured API key is empty, the middleware is a no-op so local
// development and trusted homelab environments can run unauthenticated.
package auth

import (
	"crypto/subtle"
	"net/http"
)

// Middleware returns an HTTP middleware that requires a Bearer token matching
// apiKey. An empty apiKey disables the check.
func Middleware(apiKey string) func(http.Handler) http.Handler {
	if apiKey == "" {
		return func(next http.Handler) http.Handler { return next }
	}
	expected := []byte(apiKey)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !valid(r.Header.Get("Authorization"), expected) {
				w.Header().Set("WWW-Authenticate", `Bearer realm="goop"`)
				http.Error(w, `{"error":{"message":"invalid api key","type":"authentication_error"}}`, http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func valid(header string, expected []byte) bool {
	const prefix = "Bearer "
	if len(header) < len(prefix) || header[:len(prefix)] != prefix {
		return false
	}
	got := []byte(header[len(prefix):])
	if len(got) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare(got, expected) == 1
}
