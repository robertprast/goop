package utils

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"
)

type Config struct {
	Engines     map[string]string `yaml:"engines"`
	DatabaseURL string            `yaml:"database_url"`
	Server      ServerConfig      `yaml:"server"`
	Logging     LoggingConfig     `yaml:"logging"`
	Auth        AuthConfig        `yaml:"auth"`
	Environment string            `yaml:"environment"`
}

type ServerConfig struct {
	Port            int    `yaml:"port"`
	Host            string `yaml:"host"`
	ReadTimeout     int    `yaml:"read_timeout"`
	WriteTimeout    int    `yaml:"write_timeout"`
	ShutdownTimeout int    `yaml:"shutdown_timeout"`
}

type LoggingConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

type AuthConfig struct {
	Disabled bool `yaml:"disabled"`
}

// LoadConfig reads the config file, substitutes environment variables, and converts engine configs to strings
func LoadConfig(filename string) (Config, error) {
	var rawConfig map[string]interface{}
	var finalConfig Config

	// Set defaults
	finalConfig = getDefaultConfig()

	data, err := os.ReadFile(filename)
	if err != nil {
		return finalConfig, err
	}

	substitutedData := substituteEnvVars(string(data))

	err = yaml.Unmarshal([]byte(substitutedData), &rawConfig)
	if err != nil {
		return finalConfig, fmt.Errorf("error parsing YAML: %w", err)
	}

	// Process engines
	finalConfig.Engines = make(map[string]string)
	
	if enginesInterface, exists := rawConfig["engines"]; exists && enginesInterface != nil {
		enginesRaw, ok := enginesInterface.(map[interface{}]interface{})
		if !ok {
			return finalConfig, fmt.Errorf("invalid format for engines section")
		}

		for engineName, engineConfig := range enginesRaw {
			engineConfigStr, err := yaml.Marshal(engineConfig)
			if err != nil {
				return finalConfig, fmt.Errorf("error marshaling engine config for %s: %w", engineName, err)
			}

			finalConfig.Engines[fmt.Sprintf("%v", engineName)] = string(engineConfigStr)
		}
	}
	// If engines section is empty/commented out, finalConfig.Engines remains empty map

	// Apply environment variable overrides
	finalConfig = applyEnvOverrides(finalConfig)

	// Validate configuration
	if err := validateConfig(finalConfig); err != nil {
		return finalConfig, fmt.Errorf("config validation failed: %w", err)
	}

	return finalConfig, nil
}

// substituteEnvVars replaces ${VAR} and ${VAR:-default} with environment variable values
func substituteEnvVars(content string) string {
	// Handle ${VAR:-default} pattern
	reWithDefault := regexp.MustCompile(`\$\{(\w+):-([^}]+)\}`)
	content = reWithDefault.ReplaceAllStringFunc(content, func(match string) string {
		parts := strings.Split(match[2:len(match)-1], ":-")
		if len(parts) != 2 {
			return match
		}
		envVar := parts[0]
		defaultValue := parts[1]
		value := os.Getenv(envVar)
		if value == "" {
			return defaultValue
		}
		return value
	})

	// Handle ${VAR} pattern
	re := regexp.MustCompile(`\$\{(\w+)\}`)
	return re.ReplaceAllStringFunc(content, func(match string) string {
		if len(match) < 4 {
			return match
		}
		envVar := match[2 : len(match)-1]
		value := os.Getenv(envVar)
		if value == "" {
			logrus.Warnf("Environment variable %s is not set, using empty string", envVar)
		}
		return value
	})
}

// getDefaultConfig returns a configuration with sensible defaults
func getDefaultConfig() Config {
	return Config{
		Engines:     make(map[string]string),
		Environment: GetEnvWithDefault("GOOP_ENV", "development"),
		Server: ServerConfig{
			Port:            GetEnvIntWithDefault("PORT", 8080),
			Host:            GetEnvWithDefault("HOST", "0.0.0.0"),
			ReadTimeout:     GetEnvIntWithDefault("READ_TIMEOUT", 30),
			WriteTimeout:    GetEnvIntWithDefault("WRITE_TIMEOUT", 30),
			ShutdownTimeout: GetEnvIntWithDefault("SHUTDOWN_TIMEOUT", 5),
		},
		Logging: LoggingConfig{
			Level:  GetEnvWithDefault("LOG_LEVEL", "info"),
			Format: GetEnvWithDefault("LOG_FORMAT", "json"),
		},
		Auth: AuthConfig{
			Disabled: GetEnvBoolWithDefault("GOOP_DISABLE_AUTH", false),
		},
	}
}

// applyEnvOverrides applies environment variable overrides to config
func applyEnvOverrides(config Config) Config {
	// Database URL can be overridden by env var
	if dbURL := os.Getenv("DATABASE_URL"); dbURL != "" {
		config.DatabaseURL = dbURL
	}

	return config
}

// validateConfig validates the configuration
func validateConfig(config Config) error {
	var errors []string

	// Validate server config
	if config.Server.Port < 1 || config.Server.Port > 65535 {
		errors = append(errors, "server port must be between 1 and 65535")
	}

	// Validate logging level
	validLogLevels := []string{"trace", "debug", "info", "warn", "error", "fatal", "panic"}
	validLevel := false
	for _, level := range validLogLevels {
		if strings.ToLower(config.Logging.Level) == level {
			validLevel = true
			break
		}
	}
	if !validLevel {
		errors = append(errors, fmt.Sprintf("invalid log level: %s (must be one of: %s)", config.Logging.Level, strings.Join(validLogLevels, ", ")))
	}

	// Validate environment
	validEnvs := []string{"development", "staging", "production"}
	validEnv := false
	for _, env := range validEnvs {
		if strings.ToLower(config.Environment) == env {
			validEnv = true
			break
		}
	}
	if !validEnv {
		errors = append(errors, fmt.Sprintf("invalid environment: %s (must be one of: %s)", config.Environment, strings.Join(validEnvs, ", ")))
	}

	if len(errors) > 0 {
		return fmt.Errorf(strings.Join(errors, "; "))
	}

	return nil
}

// IsProduction returns true if running in production environment
func (c *Config) IsProduction() bool {
	return strings.ToLower(c.Environment) == "production"
}

// IsStaging returns true if running in staging environment
func (c *Config) IsStaging() bool {
	return strings.ToLower(c.Environment) == "staging"
}

// IsDevelopment returns true if running in development environment
func (c *Config) IsDevelopment() bool {
	return strings.ToLower(c.Environment) == "development"
}
