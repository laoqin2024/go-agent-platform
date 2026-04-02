package disk

import (
	"context"
	"errors"
	"path/filepath"
	"time"
)

type UsageStat struct {
	Total       uint64
	Used        uint64
	Free        uint64
	UsedPercent float64
}

func UsageWithContext(ctx context.Context, path string) (*UsageStat, error) {
	if ctx == nil {
		return nil, errors.New("gopsutil stub: nil context")
	}
	// Simulate a quick call.
	timer := time.NewTimer(10 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
	}

	// Deterministic values derived from path length (just to vary slightly).
	seed := uint64(len(filepath.Clean(path)) + 1)
	total := uint64(1000*1024*1024) + seed*1024*1024
	used := uint64(float64(total) * 0.42) + seed*1000
	free := total - used

	return &UsageStat{
		Total:       total,
		Used:        used,
		Free:        free,
		UsedPercent: (float64(used) / float64(total)) * 100,
	}, nil
}

