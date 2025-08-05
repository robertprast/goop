package proxy

import (
	"fmt"
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
		return nil, err
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
	switch engineType {
	case "openai":
		openaiEngine, err := openai.NewOpenAIEngineWithConfig(ec.config.Engines["openai"])
		if err != nil {
			return nil, err
		}
		return &openaiproxy.OpenAIProxy{
			OpenAIEngine: openaiEngine,
		}, nil
	case "bedrock":
		bedrockEngine, err := bedrock.NewBedrockEngine(ec.config.Engines["bedrock"])
		if err != nil {
			return nil, err
		}
		return &bedrockproxy.BedrockProxy{
			BedrockEngine: bedrockEngine,
		}, nil
	case "gemini":
		geminiEngine, err := gemini.NewGeminiEngine(ec.config.Engines["gemini"])
		if err != nil {
			return nil, err
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