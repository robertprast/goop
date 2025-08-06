package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/robertprast/goop/pkg/utils"
	"github.com/sirupsen/logrus"
)

// ContextKey is used for storing values in request context
type ContextKey string

const (
	// ContextKeyAPIKey is the key for storing API key info in context
	ContextKeyAPIKey ContextKey = "api_key"
)

// Middleware handles authentication for HTTP requests
type Middleware struct {
	service *Service
	logger  *logrus.Logger
}

// NewMiddleware creates a new auth middleware
func NewMiddleware(service *Service, logger *logrus.Logger) *Middleware {
	return &Middleware{
		service: service,
		logger:  logger,
	}
}

// RequireAuth is a middleware that requires a valid API key
func (m *Middleware) RequireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Check if auth is disabled for local development
		if utils.GetEnvBoolWithDefault("GOOP_DISABLE_AUTH", false) {
			m.logger.Debug("Authentication disabled for local development")
			next(w, r)
			return
		}

		apiKey, err := m.extractAndValidateAPIKey(r)
		if err != nil {
			m.logger.WithError(err).Warn("Authentication failed")
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		// Add API key info to request context
		ctx := context.WithValue(r.Context(), ContextKeyAPIKey, apiKey)
		next(w, r.WithContext(ctx))
	}
}

// RequireAdminAuth is a middleware that requires admin role
func (m *Middleware) RequireAdminAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Check if auth is disabled for local development
		if utils.GetEnvBoolWithDefault("GOOP_DISABLE_AUTH", false) {
			m.logger.Debug("Authentication disabled for local development")
			next(w, r)
			return
		}

		apiKey, err := m.extractAndValidateAPIKey(r)
		if err != nil {
			m.logger.WithError(err).Warn("Authentication failed")
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		if apiKey.Role != RoleAdmin {
			m.logger.WithFields(logrus.Fields{
				"key_id": apiKey.ID,
				"role":   apiKey.Role,
			}).Warn("Access denied: admin role required")
			http.Error(w, "Forbidden: admin role required", http.StatusForbidden)
			return
		}

		// Add API key info to request context
		ctx := context.WithValue(r.Context(), ContextKeyAPIKey, apiKey)
		next(w, r.WithContext(ctx))
	}
}

// extractAndValidateAPIKey extracts and validates the API key from the request
func (m *Middleware) extractAndValidateAPIKey(r *http.Request) (*APIKey, error) {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return nil, &AuthError{Message: "missing Authorization header"}
	}

	// Prevent header injection attacks by checking for newlines
	if strings.ContainsAny(authHeader, "\r\n") {
		return nil, &AuthError{Message: "invalid Authorization header"}
	}

	// Extract token from "Bearer <token>" format
	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
		return nil, &AuthError{Message: "invalid Authorization header format"}
	}

	token := strings.TrimSpace(parts[1])
	if token == "" {
		return nil, &AuthError{Message: "empty API key"}
	}

	// Additional security checks
	if len(token) > 100 { // Reasonable upper limit for API key length
		return nil, &AuthError{Message: "invalid API key"}
	}

	// Check for obvious malicious patterns
	if strings.ContainsAny(token, "\r\n\t") {
		return nil, &AuthError{Message: "invalid API key"}
	}

	apiKey, err := m.service.ValidateAPIKey(r.Context(), token)
	if err != nil {
		return nil, &AuthError{Message: "invalid API key", Cause: err}
	}

	return apiKey, nil
}

// GetAPIKeyFromContext retrieves the API key from request context
func GetAPIKeyFromContext(ctx context.Context) (*APIKey, bool) {
	apiKey, ok := ctx.Value(ContextKeyAPIKey).(*APIKey)
	return apiKey, ok
}

// AuthError represents authentication errors
type AuthError struct {
	Message string
	Cause   error
}

func (e *AuthError) Error() string {
	if e.Cause != nil {
		return e.Message + ": " + e.Cause.Error()
	}
	return e.Message
}
