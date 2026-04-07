package collector

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
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

	// Data cleaning (security hardening):
	// - Force valid UTF-8 (drop invalid sequences)
	// - Remove control characters to avoid UI/log corruption
	// - Trim and drop empty names
	items = cleanSoftwareInfos(items)

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

func cleanSoftwareInfos(in []SoftwareInfo) []SoftwareInfo {
	if len(in) == 0 {
		return nil
	}
	out := make([]SoftwareInfo, 0, len(in))
	for _, it := range in {
		it.Name = cleanSoftwareString(it.Name)
		it.Version = cleanSoftwareString(it.Version)
		it.Publisher = cleanSoftwareString(it.Publisher)

		if strings.TrimSpace(it.Name) == "" {
			continue
		}
		out = append(out, it)
	}
	return out
}

func cleanSoftwareString(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}

	// Force valid UTF-8; drop invalid byte sequences.
	if !utf8.ValidString(s) {
		s = string(bytes.ToValidUTF8([]byte(s), nil))
	}

	// Remove control characters (keep common whitespace).
	s = strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', '\t':
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		// Remove explicit replacement chars often produced by broken encodings.
		if r == unicode.ReplacementChar {
			return -1
		}
		return r
	}, s)

	// Normalize whitespace a bit.
	s = strings.Join(strings.Fields(s), " ")
	return strings.TrimSpace(s)
}
