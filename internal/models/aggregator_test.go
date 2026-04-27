package models

import (
	"context"
	"errors"
	"net/http"
	"net/http/httputil"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/robertprast/goop/internal/provider"
)

// fakeProvider is a minimal provider.Provider implementation for tests.
type fakeProvider struct {
	name      string
	prefix    string
	models    []provider.Model
	err       error
	listDelay time.Duration
	calls     atomic.Int64
}

func (f *fakeProvider) Name() string                 { return f.name }
func (f *fakeProvider) Prefix() string               { return f.prefix }
func (f *fakeProvider) Rewrite(_ *httputil.ProxyRequest) {}
func (f *fakeProvider) Transport() http.RoundTripper { return nil }

func (f *fakeProvider) ListModels(ctx context.Context) ([]provider.Model, error) {
	f.calls.Add(1)
	if f.listDelay > 0 {
		select {
		case <-time.After(f.listDelay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	// return a copy so callers can't mutate the canned slice
	out := make([]provider.Model, len(f.models))
	copy(out, f.models)
	return out, nil
}

func TestAggregator_DedupesByID(t *testing.T) {
	a := &fakeProvider{
		name:   "a",
		prefix: "/a",
		models: []provider.Model{{ID: "shared/m1", Provider: "a"}},
	}
	b := &fakeProvider{
		name:   "b",
		prefix: "/b",
		models: []provider.Model{{ID: "shared/m1", Provider: "b"}, {ID: "b/m2", Provider: "b"}},
	}
	reg := provider.NewRegistry(a, b)
	agg := New(reg, time.Minute, nil)

	got := agg.List(context.Background())
	if len(got) != 2 {
		t.Fatalf("len: got %d want 2 (%+v)", len(got), got)
	}
	// Sorted by ID so b/m2 comes after shared/m1 alphabetically? "b/m2" < "shared/m1".
	ids := []string{got[0].ID, got[1].ID}
	if ids[0] != "b/m2" || ids[1] != "shared/m1" {
		t.Errorf("ids: got %v want [b/m2 shared/m1]", ids)
	}
}

func TestAggregator_PerProviderFailureIsolation(t *testing.T) {
	good := &fakeProvider{
		name:   "good",
		prefix: "/good",
		models: []provider.Model{{ID: "good/m1"}, {ID: "good/m2"}},
	}
	bad := &fakeProvider{
		name:   "bad",
		prefix: "/bad",
		err:    errors.New("upstream is down"),
	}
	reg := provider.NewRegistry(good, bad)
	agg := New(reg, time.Minute, nil)

	got := agg.List(context.Background())
	if len(got) != 2 {
		t.Fatalf("len: got %d want 2 (%+v)", len(got), got)
	}
	for _, m := range got {
		if m.ID != "good/m1" && m.ID != "good/m2" {
			t.Errorf("unexpected model: %+v", m)
		}
	}
}

func TestAggregator_TTLCachesResults(t *testing.T) {
	p := &fakeProvider{
		name:   "p",
		prefix: "/p",
		models: []provider.Model{{ID: "p/m1"}},
	}
	reg := provider.NewRegistry(p)
	agg := New(reg, time.Minute, nil)

	_ = agg.List(context.Background())
	if got := p.calls.Load(); got != 1 {
		t.Fatalf("after first List: calls = %d want 1", got)
	}
	_ = agg.List(context.Background())
	if got := p.calls.Load(); got != 1 {
		t.Errorf("after second List: calls = %d want still 1 (cached)", got)
	}
}

func TestAggregator_InvalidateForcesRefetch(t *testing.T) {
	p := &fakeProvider{
		name:   "p",
		prefix: "/p",
		models: []provider.Model{{ID: "p/m1"}},
	}
	reg := provider.NewRegistry(p)
	agg := New(reg, time.Minute, nil)

	_ = agg.List(context.Background())
	if got := p.calls.Load(); got != 1 {
		t.Fatalf("first list: calls = %d want 1", got)
	}
	agg.Invalidate()
	_ = agg.List(context.Background())
	if got := p.calls.Load(); got != 2 {
		t.Errorf("after invalidate: calls = %d want 2", got)
	}
}

func TestAggregator_NoTTLAlwaysRefetches(t *testing.T) {
	p := &fakeProvider{
		name:   "p",
		prefix: "/p",
		models: []provider.Model{{ID: "p/m1"}},
	}
	reg := provider.NewRegistry(p)
	agg := New(reg, 0, nil) // ttl=0 disables caching

	_ = agg.List(context.Background())
	_ = agg.List(context.Background())
	if got := p.calls.Load(); got < 2 {
		t.Errorf("ttl=0 should not cache: calls = %d, want >= 2", got)
	}
}

func TestAggregator_ConcurrentListNoRace(t *testing.T) {
	// With ttl=0 (no cache) two concurrent List calls must both run without
	// data races. Use a small sleep to maximise interleaving.
	p := &fakeProvider{
		name:      "p",
		prefix:    "/p",
		models:    []provider.Model{{ID: "p/m1"}},
		listDelay: 50 * time.Millisecond,
	}
	reg := provider.NewRegistry(p)
	agg := New(reg, 0, nil)

	var wg sync.WaitGroup
	const N = 4
	wg.Add(N)
	for range N {
		go func() {
			defer wg.Done()
			out := agg.List(context.Background())
			if len(out) != 1 {
				t.Errorf("concurrent List: got %d models, want 1", len(out))
			}
		}()
	}
	wg.Wait()
}

func TestAggregator_NilLoggerOK(t *testing.T) {
	// New must accept nil logger without panicking, falling back to slog.Default.
	reg := provider.NewRegistry()
	agg := New(reg, time.Minute, nil)
	if agg.logger == nil {
		t.Error("expected logger to default; got nil")
	}
}
