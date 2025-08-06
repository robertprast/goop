package proxy

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/robertprast/goop/pkg/engine/bedrock"
	"github.com/robertprast/goop/pkg/engine/gemini"
	"github.com/robertprast/goop/pkg/engine/openai"
	bedrockproxy "github.com/robertprast/goop/pkg/transformers/bedrock"
	geminiproxy "github.com/robertprast/goop/pkg/transformers/gemini"
	openaiproxy "github.com/robertprast/goop/pkg/transformers/openai"
	"github.com/robertprast/goop/pkg/utils"
	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"
)

// CachedEngine represents a cached engine with its creation time
type CachedEngine struct {
	Engine    OpenAIProxyEngine
	CreatedAt time.Time
}

// EngineCache manages cached engine instances
type EngineCache struct {
	cache     map[string]*CachedEngine
	mutex     sync.RWMutex
	config    *utils.Config
	logger    *logrus.Logger
	cacheTTL  time.Duration
}

// NewEngineCache creates a new engine cache
func NewEngineCache(config *utils.Config, logger *logrus.Logger) *EngineCache {
	return &EngineCache{
		cache:    make(map[string]*CachedEngine),
		config:   config,
		logger:   logger,
		cacheTTL: 30 * time.Minute, // Cache engines for 30 minutes
	}
}

// GetEngine returns a cached engine or creates a new one
func (ec *EngineCache) GetEngine(engineType string, model string) (OpenAIProxyEngine, error) {
	cacheKey := engineType + ":" + model
	
	// Try to get from cache first
	ec.mutex.RLock()
	if cached, exists := ec.cache[cacheKey]; exists {
		// Check if cache entry is still valid
		if time.Since(cached.CreatedAt) < ec.cacheTTL {
			ec.mutex.RUnlock()
			ec.logger.Debugf("Using cached engine for %s", cacheKey)
			return cached.Engine, nil
		}
	}
	ec.mutex.RUnlock()

	// Cache miss or expired, create new engine
	ec.mutex.Lock()
	defer ec.mutex.Unlock()

	// Double-check pattern: another goroutine might have created it
	if cached, exists := ec.cache[cacheKey]; exists {
		if time.Since(cached.CreatedAt) < ec.cacheTTL {
			ec.logger.Debugf("Using cached engine for %s (double-check)", cacheKey)
			return cached.Engine, nil
		}
	}

	// Create new engine
	proxyEngine, err := ec.createEngine(engineType, model)
	if err != nil {
		ec.logger.Warnf("Failed to create engine %s: %v", engineType, err)
		return nil, fmt.Errorf("engine %s not available: %w", engineType, err)
	}

	// Cache the new engine
	ec.cache[cacheKey] = &CachedEngine{
		Engine:    proxyEngine,
		CreatedAt: time.Now(),
	}

	ec.logger.Infof("Created and cached new engine for %s", cacheKey)
	return proxyEngine, nil
}

// createEngine creates a new engine instance
func (ec *EngineCache) createEngine(engineType string, _ string) (OpenAIProxyEngine, error) {
	// Check if engine configuration exists
	engineConfig, exists := ec.config.Engines[engineType]
	if !exists {
		return nil, fmt.Errorf("engine %s not configured", engineType)
	}

	switch engineType {
	case "openai":
		openaiEngine, err := openai.NewOpenAIEngineWithConfig(engineConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to create OpenAI engine: %w", err)
		}
		return &openaiproxy.OpenAIProxy{
			OpenAIEngine: openaiEngine,
		}, nil
	case "bedrock":
		bedrockEngine, err := bedrock.NewBedrockEngine(engineConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to create Bedrock engine: %w", err)
		}
		return &bedrockproxy.BedrockProxy{
			BedrockEngine: bedrockEngine,
		}, nil
	case "gemini":
		geminiEngine, err := gemini.NewGeminiEngine(engineConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to create Gemini engine: %w", err)
		}
		return &geminiproxy.GeminiProxy{
			GeminiEngine: geminiEngine,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported engine type: %s", engineType)
	}
}

// InvalidateCache removes expired entries from the cache
func (ec *EngineCache) InvalidateCache() {
	ec.mutex.Lock()
	defer ec.mutex.Unlock()

	now := time.Now()
	for key, cached := range ec.cache {
		if now.Sub(cached.CreatedAt) >= ec.cacheTTL {
			delete(ec.cache, key)
			ec.logger.Debugf("Invalidated cached engine for %s", key)
		}
	}
}

// ClearCache removes all entries from the cache
func (ec *EngineCache) ClearCache() {
	ec.mutex.Lock()
	defer ec.mutex.Unlock()
	
	ec.cache = make(map[string]*CachedEngine)
	ec.logger.Info("Cleared engine cache")
}

// StartCleanupRoutine starts a background routine to clean up expired cache entries
func (ec *EngineCache) StartCleanupRoutine() {
	go func() {
		ticker := time.NewTicker(10 * time.Minute) // Cleanup every 10 minutes
		defer ticker.Stop()
		
		for range ticker.C {
			ec.InvalidateCache()
		}
	}()
}

// GetAvailableEngines returns a list of engines that have their required credentials configured
func (ec *EngineCache) GetAvailableEngines() []string {
	var available []string

	for engineType := range ec.config.Engines {
		if ec.isEngineAvailable(engineType) {
			available = append(available, engineType)
		}
	}

	return available
}

// isEngineAvailable checks if the given engine has its required credentials
func (ec *EngineCache) isEngineAvailable(engineType string) bool {
	engineConfig, exists := ec.config.Engines[engineType]
	if !exists {
		return false
	}

	switch engineType {
	case "openai":
		return ec.checkOpenAICredentials(engineConfig)
	case "gemini":
		return ec.checkGeminiCredentials(engineConfig)
	case "bedrock":
		return ec.checkBedrockCredentials(engineConfig)
	default:
		return false
	}
}

// checkOpenAICredentials checks if OpenAI API key is available
func (ec *EngineCache) checkOpenAICredentials(configStr string) bool {
	var config struct {
		APIKey string `yaml:"api_key"`
	}

	if err := yaml.Unmarshal([]byte(configStr), &config); err != nil {
		return false
	}

	return strings.TrimSpace(config.APIKey) != ""
}

// checkGeminiCredentials checks if Gemini API key is available
func (ec *EngineCache) checkGeminiCredentials(configStr string) bool {
	var config struct {
		APIKey string `yaml:"api_key"`
	}

	if err := yaml.Unmarshal([]byte(configStr), &config); err != nil {
		return false
	}

	// Check only config API key (config system handles env vars)
	return strings.TrimSpace(config.APIKey) != ""
}

// checkBedrockCredentials checks if AWS credentials are available
func (ec *EngineCache) checkBedrockCredentials(configStr string) bool {
	var config struct {
		AccessKeyID     string `yaml:"access_key_id"`
		SecretAccessKey string `yaml:"secret_access_key"`
	}

	if err := yaml.Unmarshal([]byte(configStr), &config); err != nil {
		return false
	}

	// Check only config credentials (config system handles env vars)
	return strings.TrimSpace(config.AccessKeyID) != "" && strings.TrimSpace(config.SecretAccessKey) != ""
}