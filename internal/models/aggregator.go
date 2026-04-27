// Package models implements the unified /v1/models endpoint. It fans out to
// every configured provider's listing API and unions the results, with a
// TTL cache so we don't hammer upstreams.
package models

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/robertprast/goop/internal/provider"
)

// Aggregator caches provider model lists with a TTL.
type Aggregator struct {
	registry *provider.Registry
	ttl      time.Duration
	logger   *slog.Logger

	mu       sync.RWMutex
	cached   []provider.Model
	cachedAt time.Time
}

// New builds an Aggregator. ttl <= 0 disables caching.
func New(reg *provider.Registry, ttl time.Duration, logger *slog.Logger) *Aggregator {
	if logger == nil {
		logger = slog.Default()
	}
	return &Aggregator{registry: reg, ttl: ttl, logger: logger}
}

// List returns the merged model list, refreshing the cache when stale.
// Per-provider failures are logged and skipped — one failing upstream
// must not blank the unified catalog.
func (a *Aggregator) List(ctx context.Context) []provider.Model {
	a.mu.RLock()
	if a.fresh() {
		out := a.cached
		a.mu.RUnlock()
		return out
	}
	a.mu.RUnlock()

	return a.refresh(ctx)
}

func (a *Aggregator) fresh() bool {
	if a.cachedAt.IsZero() {
		return false
	}
	if a.ttl <= 0 {
		return false
	}
	return time.Since(a.cachedAt) < a.ttl
}

// refresh fetches every provider's models in parallel and replaces the cache.
func (a *Aggregator) refresh(ctx context.Context) []provider.Model {
	a.mu.Lock()
	defer a.mu.Unlock()
	// Re-check after acquiring the write lock so concurrent callers don't
	// all stampede the upstreams.
	if a.fresh() {
		return a.cached
	}

	providers := a.registry.All()
	results := make([][]provider.Model, len(providers))
	var wg sync.WaitGroup
	for i, p := range providers {
		i, p := i, p
		wg.Add(1)
		go func() {
			defer wg.Done()
			fetchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			ms, err := p.ListModels(fetchCtx)
			if err != nil {
				a.logger.Warn("list models failed", "provider", p.Name(), "err", err)
				return
			}
			results[i] = ms
		}()
	}
	wg.Wait()

	// Merge and dedupe by ID. If the same model is exposed via two providers
	// (e.g. "gemini" native and "gemini-openai" both yield "gemini/<id>"),
	// keep the first one we saw. Provider order in the registry is the
	// tie-breaker — earlier-registered providers win.
	seen := make(map[string]struct{}, 256)
	merged := make([]provider.Model, 0, 256)
	for _, ms := range results {
		for _, m := range ms {
			if _, dup := seen[m.ID]; dup {
				continue
			}
			seen[m.ID] = struct{}{}
			merged = append(merged, m)
		}
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].ID < merged[j].ID })

	a.cached = merged
	a.cachedAt = time.Now()
	return merged
}

// Invalidate forces the next List call to re-fetch.
func (a *Aggregator) Invalidate() {
	a.mu.Lock()
	a.cachedAt = time.Time{}
	a.cached = nil
	a.mu.Unlock()
}
