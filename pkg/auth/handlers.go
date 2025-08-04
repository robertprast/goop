package auth

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/sirupsen/logrus"
)

// Handler handles admin endpoints for API key management
type Handler struct {
	service *Service
	logger  *logrus.Logger
}

// NewHandler creates a new admin handler
func NewHandler(service *Service, logger *logrus.Logger) *Handler {
	return &Handler{
		service: service,
		logger:  logger,
	}
}

// ServeHTTP handles all admin API key endpoints
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/admin/keys")

	switch {
	case path == "" || path == "/":
		switch r.Method {
		case http.MethodGet:
			h.listAPIKeys(w, r)
		case http.MethodPost:
			h.createAPIKey(w, r)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	case strings.HasPrefix(path, "/"):
		// Extract key ID from path
		idStr := strings.TrimPrefix(path, "/")

		// Additional validation for ID string
		if len(idStr) == 0 || len(idStr) > 10 { // Reasonable limit for ID length
			http.Error(w, "Invalid key ID", http.StatusBadRequest)
			return
		}

		// Check for non-numeric characters
		for _, r := range idStr {
			if r < '0' || r > '9' {
				http.Error(w, "Invalid key ID", http.StatusBadRequest)
				return
			}
		}

		id, err := strconv.Atoi(idStr)
		if err != nil || id <= 0 {
			http.Error(w, "Invalid key ID", http.StatusBadRequest)
			return
		}

		switch r.Method {
		case http.MethodGet:
			h.getAPIKey(w, r, id)
		case http.MethodPut:
			h.updateAPIKey(w, r, id)
		case http.MethodDelete:
			h.deleteAPIKey(w, r, id)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	default:
		http.Error(w, "Not found", http.StatusNotFound)
	}
}

// listAPIKeys handles GET /admin/keys
func (h *Handler) listAPIKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := h.service.GetAPIKeys()
	if err != nil {
		h.logger.WithError(err).Error("Failed to list API keys")
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(keys); err != nil {
		h.logger.WithError(err).Error("Failed to encode API keys response")
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// createAPIKey handles POST /admin/keys
func (h *Handler) createAPIKey(w http.ResponseWriter, r *http.Request) {
	// Check Content-Type
	if r.Header.Get("Content-Type") != "application/json" {
		http.Error(w, "Content-Type must be application/json", http.StatusBadRequest)
		return
	}

	var req CreateAPIKeyRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Validate role
	if req.Role != RoleAdmin && req.Role != RoleUser {
		http.Error(w, "Invalid role. Must be 'admin' or 'user'", http.StatusBadRequest)
		return
	}

	// Validate name
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		http.Error(w, "Name is required", http.StatusBadRequest)
		return
	}

	if len(req.Name) > 255 {
		http.Error(w, "Name cannot exceed 255 characters", http.StatusBadRequest)
		return
	}

	// Check for potential injection patterns
	if strings.ContainsAny(req.Name, "';\"--/*<>") {
		http.Error(w, "Name contains invalid characters", http.StatusBadRequest)
		return
	}

	response, err := h.service.CreateAPIKey(req.Name, req.Role)
	if err != nil {
		h.logger.WithError(err).Error("Failed to create API key")
		if strings.Contains(err.Error(), "invalid") || strings.Contains(err.Error(), "duplicate") {
			http.Error(w, err.Error(), http.StatusBadRequest)
		} else {
			http.Error(w, "Internal server error", http.StatusInternalServerError)
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		h.logger.WithError(err).Error("Failed to encode create API key response")
	}
}

// getAPIKey handles GET /admin/keys/{id}
func (h *Handler) getAPIKey(w http.ResponseWriter, r *http.Request, id int) {
	key, err := h.service.GetAPIKey(id)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.Error(w, "API key not found", http.StatusNotFound)
			return
		}
		h.logger.WithError(err).Error("Failed to get API key")
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(key); err != nil {
		h.logger.WithError(err).Error("Failed to encode API key response")
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// updateAPIKey handles PUT /admin/keys/{id}
func (h *Handler) updateAPIKey(w http.ResponseWriter, r *http.Request, id int) {
	// Validate ID
	if id <= 0 {
		http.Error(w, "Invalid key ID", http.StatusBadRequest)
		return
	}

	// Check Content-Type
	if r.Header.Get("Content-Type") != "application/json" {
		http.Error(w, "Content-Type must be application/json", http.StatusBadRequest)
		return
	}

	var req UpdateAPIKeyRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Validate role if provided
	if req.Role != nil && *req.Role != RoleAdmin && *req.Role != RoleUser {
		http.Error(w, "Invalid role. Must be 'admin' or 'user'", http.StatusBadRequest)
		return
	}

	// Validate name if provided
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			http.Error(w, "Name cannot be empty", http.StatusBadRequest)
			return
		}
		if len(name) > 255 {
			http.Error(w, "Name cannot exceed 255 characters", http.StatusBadRequest)
			return
		}
		// Check for potential injection patterns
		if strings.ContainsAny(name, "';\"--/*<>") {
			http.Error(w, "Name contains invalid characters", http.StatusBadRequest)
			return
		}
		*req.Name = name // Update with trimmed name
	}

	key, err := h.service.UpdateAPIKey(id, req)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.Error(w, "API key not found", http.StatusNotFound)
			return
		}
		if strings.Contains(err.Error(), "invalid") {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		h.logger.WithError(err).Error("Failed to update API key")
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(key); err != nil {
		h.logger.WithError(err).Error("Failed to encode update API key response")
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// deleteAPIKey handles DELETE /admin/keys/{id}
func (h *Handler) deleteAPIKey(w http.ResponseWriter, r *http.Request, id int) {
	if err := h.service.DeleteAPIKey(id); err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.Error(w, "API key not found", http.StatusNotFound)
			return
		}
		h.logger.WithError(err).Error("Failed to delete API key")
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
