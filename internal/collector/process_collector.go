package collector

import (
	"context"
	"errors"
	"sync"
	"time"
)

var ErrCollectUnsupported = errors.New("collector: process collection unsupported on this platform")

// ProcessCollector collects a snapshot of all processes with a small in-memory cache.
// Repeated Collect() calls within CacheTTL will return the cached snapshot immediately.
type ProcessCollector struct {
	cacheTTL time.Duration

	mu          sync.Mutex
	lastUpdated time.Time
	cache       []ProcessStat
}

type ProcessCollectorOption func(*ProcessCollector)

func WithCacheTTL(d time.Duration) ProcessCollectorOption {
	return func(pc *ProcessCollector) {
		if d > 0 {
			pc.cacheTTL = d
		}
	}
}

func NewProcessCollector(opts ...ProcessCollectorOption) *ProcessCollector {
	pc := &ProcessCollector{
		cacheTTL: time.Minute,
	}
	for _, opt := range opts {
		opt(pc)
	}
	return pc
}

// Collect collects processes with a background context.
func (pc *ProcessCollector) Collect() ([]ProcessStat, error) {
	return pc.CollectWithContext(context.Background())
}

// CollectWithContext collects a snapshot of processes, respecting ctx cancellation and using cache.
func (pc *ProcessCollector) CollectWithContext(ctx context.Context) ([]ProcessStat, error) {
	if ctx == nil {
		return nil, errors.New("collector: nil context")
	}

	now := time.Now()
	pc.mu.Lock()
	if pc.cache != nil && now.Sub(pc.lastUpdated) < pc.cacheTTL {
		// Return a copy to prevent callers from mutating the cached slice.
		out := append([]ProcessStat(nil), pc.cache...)
		pc.mu.Unlock()
		return out, nil
	}
	pc.mu.Unlock()

	procs, err := collectTopProcessStatsWithContext(ctx, 100, nil, nil)
	if err != nil {
		return nil, err
	}

	// Cache results (always cache successful calls).
	pc.mu.Lock()
	pc.cache = procs
	pc.lastUpdated = now
	pc.mu.Unlock()

	// Return a copy to prevent callers from mutating the cached slice.
	out := append([]ProcessStat(nil), procs...)
	return out, nil
}

