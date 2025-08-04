package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Service handles authentication operations
type Service struct {
	db *pgxpool.Pool
}

// NewService creates a new auth service
func NewService(db *pgxpool.Pool) *Service {
	return &Service{db: db}
}

// InitDB initializes the database connection and creates tables
func InitDB(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	db, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	if err := createTables(ctx, db); err != nil {
		return nil, fmt.Errorf("failed to create tables: %w", err)
	}

	return db, nil
}

// createTables creates the necessary database tables with security constraints
func createTables(ctx context.Context, db *pgxpool.Pool) error {
	schema := `
	CREATE TABLE IF NOT EXISTS api_keys (
		id SERIAL PRIMARY KEY,
		key_hash VARCHAR(64) UNIQUE NOT NULL,
		name VARCHAR(255) NOT NULL CHECK (LENGTH(TRIM(name)) > 0),
		role VARCHAR(20) NOT NULL CHECK (role IN ('admin', 'user')),
		is_active BOOLEAN DEFAULT true NOT NULL,
		created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW() NOT NULL,
		updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW() NOT NULL,
		last_used_at TIMESTAMP WITH TIME ZONE,
		
		-- Additional constraints for security
		CONSTRAINT chk_key_hash_format CHECK (key_hash ~ '^[a-f0-9]{64}$'),
		CONSTRAINT chk_name_length CHECK (LENGTH(name) <= 255 AND LENGTH(TRIM(name)) > 0),
		CONSTRAINT chk_created_before_updated CHECK (created_at <= updated_at)
	);

	-- Indexes for performance and security
	CREATE INDEX IF NOT EXISTS idx_api_keys_key_hash ON api_keys(key_hash);
	CREATE INDEX IF NOT EXISTS idx_api_keys_is_active ON api_keys(is_active);
	CREATE INDEX IF NOT EXISTS idx_api_keys_role ON api_keys(role);
	CREATE INDEX IF NOT EXISTS idx_api_keys_created_at ON api_keys(created_at);
	
	-- Trigger to automatically update updated_at timestamp
	CREATE OR REPLACE FUNCTION update_updated_at_column()
	RETURNS TRIGGER AS $$
	BEGIN
		NEW.updated_at = NOW();
		RETURN NEW;
	END;
	$$ language 'plpgsql';

	DROP TRIGGER IF EXISTS update_api_keys_updated_at ON api_keys;
	CREATE TRIGGER update_api_keys_updated_at
		BEFORE UPDATE ON api_keys
		FOR EACH ROW
		EXECUTE FUNCTION update_updated_at_column();
	`

	_, err := db.Exec(ctx, schema)
	return err
}

// generateAPIKey generates a new random API key
func generateAPIKey() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

// hashAPIKey creates a SHA256 hash of the API key
func hashAPIKey(key string) string {
	hash := sha256.Sum256([]byte(key))
	return hex.EncodeToString(hash[:])
}

// CreateAPIKey creates a new API key with validation
func (s *Service) CreateAPIKey(ctx context.Context, name string, role Role) (*APIKeyResponse, error) {
	// Input validation
	if err := s.validateAPIKeyInput(name, role); err != nil {
		return nil, err
	}

	key, err := generateAPIKey()
	if err != nil {
		return nil, fmt.Errorf("failed to generate API key: %w", err)
	}

	keyHash := hashAPIKey(key)
	now := time.Now()

	// Use parameterized query with explicit column names
	query := `
		INSERT INTO api_keys (key_hash, name, role, is_active, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, name, role, is_active, created_at, updated_at, last_used_at
	`

	var apiKey APIKey
	err = s.db.QueryRow(ctx, query, keyHash, name, role, true, now, now).Scan(
		&apiKey.ID, &apiKey.Name, &apiKey.Role, &apiKey.IsActive,
		&apiKey.CreatedAt, &apiKey.UpdatedAt, &apiKey.LastUsedAt,
	)
	if err != nil {
		// Check for unique constraint violation (duplicate key hash - extremely unlikely but possible)
		if strings.Contains(err.Error(), "unique") || strings.Contains(err.Error(), "duplicate") {
			return nil, fmt.Errorf("failed to create API key: duplicate key generated, please try again")
		}
		return nil, fmt.Errorf("failed to create API key: %w", err)
	}

	apiKey.KeyHash = keyHash

	return &APIKeyResponse{
		APIKey: apiKey,
		Key:    key,
	}, nil
}

// validateAPIKeyInput validates the input parameters for API key creation/updates
func (s *Service) validateAPIKeyInput(name string, role Role) error {
	// Validate name
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("name cannot be empty")
	}
	if len(name) > 255 {
		return fmt.Errorf("name cannot exceed 255 characters")
	}

	// Check for potential SQL injection patterns in name (defense in depth)
	if strings.ContainsAny(name, "';\"--/*") {
		return fmt.Errorf("name contains invalid characters")
	}

	// Validate role
	if role != RoleAdmin && role != RoleUser {
		return fmt.Errorf("invalid role: must be 'admin' or 'user'")
	}

	return nil
}

// ValidateAPIKey validates an API key and returns the associated user info
func (s *Service) ValidateAPIKey(ctx context.Context, key string) (*APIKey, error) {
	// Input validation to prevent empty/malicious keys
	if strings.TrimSpace(key) == "" {
		return nil, fmt.Errorf("invalid API key")
	}

	// Validate key format (should be 64 hex characters)
	if len(key) != 64 {
		return nil, fmt.Errorf("invalid API key")
	}

	// Check if key contains only valid hex characters
	if _, err := hex.DecodeString(key); err != nil {
		return nil, fmt.Errorf("invalid API key")
	}

	keyHash := hashAPIKey(key)

	// Use explicit column selection for security
	query := `
		SELECT id, key_hash, name, role, is_active, created_at, updated_at, last_used_at
		FROM api_keys
		WHERE key_hash = $1 AND is_active = true
	`

	var apiKey APIKey
	err := s.db.QueryRow(ctx, query, keyHash).Scan(
		&apiKey.ID, &apiKey.KeyHash, &apiKey.Name, &apiKey.Role,
		&apiKey.IsActive, &apiKey.CreatedAt, &apiKey.UpdatedAt, &apiKey.LastUsedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("invalid API key")
		}
		return nil, fmt.Errorf("failed to validate API key: %w", err)
	}

	// Update last_used_at asynchronously to avoid blocking the response
	go s.updateLastUsed(context.Background(), apiKey.ID)

	return &apiKey, nil
}

// updateLastUsed updates the last_used_at timestamp for an API key
func (s *Service) updateLastUsed(ctx context.Context, keyID int) {
	// Validate keyID to prevent negative or zero values
	if keyID <= 0 {
		return
	}

	query := `UPDATE api_keys SET last_used_at = NOW() WHERE id = $1`
	if _, err := s.db.Exec(ctx, query, keyID); err != nil {
		// Log error but don't fail the request
		// In a production environment, you'd want to use a proper logger
		// For now, we'll silently handle the error to avoid blocking the main flow
		_ = err
	}
}

// GetAPIKeys retrieves all API keys (admin only)
func (s *Service) GetAPIKeys(ctx context.Context) ([]APIKey, error) {
	query := `
		SELECT id, key_hash, name, role, is_active, created_at, updated_at, last_used_at
		FROM api_keys
		ORDER BY created_at DESC
	`

	rows, err := s.db.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to get API keys: %w", err)
	}
	defer rows.Close()

	var keys []APIKey
	for rows.Next() {
		var apiKey APIKey
		err := rows.Scan(
			&apiKey.ID, &apiKey.KeyHash, &apiKey.Name, &apiKey.Role,
			&apiKey.IsActive, &apiKey.CreatedAt, &apiKey.UpdatedAt, &apiKey.LastUsedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan API key: %w", err)
		}
		keys = append(keys, apiKey)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate over API keys: %w", err)
	}

	return keys, nil
}

// GetAPIKey retrieves a specific API key by ID with validation
func (s *Service) GetAPIKey(ctx context.Context, id int) (*APIKey, error) {
	// Validate ID
	if id <= 0 {
		return nil, fmt.Errorf("invalid API key ID")
	}

	query := `
		SELECT id, key_hash, name, role, is_active, created_at, updated_at, last_used_at
		FROM api_keys
		WHERE id = $1
	`

	var apiKey APIKey
	err := s.db.QueryRow(ctx, query, id).Scan(
		&apiKey.ID, &apiKey.KeyHash, &apiKey.Name, &apiKey.Role,
		&apiKey.IsActive, &apiKey.CreatedAt, &apiKey.UpdatedAt, &apiKey.LastUsedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("API key not found")
		}
		return nil, fmt.Errorf("failed to get API key: %w", err)
	}

	return &apiKey, nil
}


// DeleteAPIKey deletes an API key with validation
func (s *Service) DeleteAPIKey(ctx context.Context, id int) error {
	// Validate ID
	if id <= 0 {
		return fmt.Errorf("invalid API key ID")
	}

	query := `DELETE FROM api_keys WHERE id = $1`
	result, err := s.db.Exec(ctx, query, id)
	if err != nil {
		return fmt.Errorf("failed to delete API key: %w", err)
	}

	rowsAffected := result.RowsAffected()
	if rowsAffected == 0 {
		return fmt.Errorf("API key not found")
	}

	return nil
}
