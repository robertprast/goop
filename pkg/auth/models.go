package auth

import (
	"time"
)

// Role represents the access level for an API key
type Role string

const (
	RoleAdmin Role = "admin"
	RoleUser  Role = "user"
)

// APIKey represents an API key in the database
type APIKey struct {
	ID         int        `db:"id" json:"id"`
	KeyHash    string     `db:"key_hash" json:"-"`
	Name       string     `db:"name" json:"name"`
	Role       Role       `db:"role" json:"role"`
	IsActive   bool       `db:"is_active" json:"is_active"`
	CreatedAt  time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt  time.Time  `db:"updated_at" json:"updated_at"`
	LastUsedAt *time.Time `db:"last_used_at" json:"last_used_at,omitempty"`
}

// CreateAPIKeyRequest represents the request to create a new API key
type CreateAPIKeyRequest struct {
	Name string `json:"name" validate:"required"`
	Role Role   `json:"role" validate:"required,oneof=admin user"`
}

// UpdateAPIKeyRequest represents the request to update an API key
type UpdateAPIKeyRequest struct {
	Name     *string `json:"name,omitempty"`
	Role     *Role   `json:"role,omitempty" validate:"omitempty,oneof=admin user"`
	IsActive *bool   `json:"is_active,omitempty"`
}

// APIKeyResponse represents the response when creating an API key
type APIKeyResponse struct {
	APIKey
	Key string `json:"key,omitempty"` // Only returned when creating a new key
}
