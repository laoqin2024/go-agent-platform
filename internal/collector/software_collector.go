package collector

import (
	"context"
	"errors"
	"sync"
	"time"
)

// SoftwareInfo represents an installed application in a platform-agnostic format.
type SoftwareInfo struct {
	Name         string
	Version      string
	Publisher    string
	InstallDate  *time.Time
	CollectedAt  time.Time
}

var ErrSoftwareCollectUnsupported = errors.New("collector: software collection unsupported on this platform")

// SoftwareCollector collects installed software with a simple in-memory cache.
// Cache TTL is useful because enumerating installed applications can be expensive.
type SoftwareCollector struct {
	cacheTTL time.Duration

	mu          sync.Mutex
	lastUpdated time.Time
	cache       []SoftwareInfo
}

type SoftwareCollectorOption func(*SoftwareCollector)

func WithSoftwareCacheTTL(d time.Duration) SoftwareCollectorOption {
	return func(sc *SoftwareCollector) {
		if d > 0 {
			sc.cacheTTL = d
		}
	}
}

func NewSoftwareCollector(opts ...SoftwareCollectorOption) *SoftwareCollector {
	sc := &SoftwareCollector{
		cacheTTL: 12 * time.Hour,
	}
	for _, opt := range opts {
		opt(sc)
	}
	return sc
}

func (sc *SoftwareCollector) Collect(ctx context.Context) ([]SoftwareInfo, error) {
	if ctx == nil {
		return nil, errors.New("collector: nil context")
	}

	now := time.Now()
	sc.mu.Lock()
	if sc.cache != nil && now.Sub(sc.lastUpdated) < sc.cacheTTL {
		out := append([]SoftwareInfo(nil), sc.cache...)
		sc.mu.Unlock()
		return out, nil
	}
	sc.mu.Unlock()

	items, err := collectSoftwareWithContext(ctx)
	if err != nil {
		return nil, err
	}

	// Stamp collected time.
	for i := range items {
		items[i].CollectedAt = now
	}

	sc.mu.Lock()
	sc.cache = items
	sc.lastUpdated = now
	sc.mu.Unlock()

	out := append([]SoftwareInfo(nil), items...)
	return out, nil
}

