package utils

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// GetEnv fetches an environment variable or returns a default value if not set
// Deprecated: Use GetEnvWithDefault instead
func GetEnv(key, defaultValue string) string {
	return GetEnvWithDefault(key, defaultValue)
}

// GetEnvWithDefault fetches an environment variable or returns a default value if not set
func GetEnvWithDefault(key, defaultValue string) string {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return value
}

// GetEnvIntWithDefault fetches an environment variable as an integer or returns a default value
func GetEnvIntWithDefault(key string, defaultValue int) int {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	intValue, err := strconv.Atoi(value)
	if err != nil {
		return defaultValue
	}
	return intValue
}

// GetEnvBoolWithDefault fetches an environment variable as a boolean or returns a default value
func GetEnvBoolWithDefault(key string, defaultValue bool) bool {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	switch strings.ToLower(value) {
	case "true", "1", "yes", "on":
		return true
	case "false", "0", "no", "off":
		return false
	default:
		return defaultValue
	}
}

// MustGetEnv fetches an environment variable or panics if not set
func MustGetEnv(key string) string {
	value := os.Getenv(key)
	if value == "" {
		panic("Required environment variable not set: " + key)
	}
	return value
}

// GetEnvSlice fetches an environment variable as a slice split by separator
func GetEnvSlice(key, separator string) []string {
	value := os.Getenv(key)
	if value == "" {
		return []string{}
	}
	return strings.Split(value, separator)
}

// RequiredEnvVars validates that all required environment variables are set
func RequiredEnvVars(keys ...string) error {
	var missing []string
	for _, key := range keys {
		if os.Getenv(key) == "" {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("required environment variables not set: %s", strings.Join(missing, ", "))
	}
	return nil
}
