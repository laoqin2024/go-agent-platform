package mem

import (
	"context"
	"errors"
	"time"
)

type VirtualMemoryStat struct {
	Total       uint64
	Used        uint64
	UsedPercent float64
}

func VirtualMemoryWithContext(ctx context.Context) (*VirtualMemoryStat, error) {
	if ctx == nil {
		return nil, errors.New("gopsutil stub: nil context")
	}
	// Simulate some work but keep it short.
	timer := time.NewTimer(10 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
	}

	total := uint64(16 * 1024 * 1024 * 1024) // 16 GiB
	used := uint64(6 * 1024 * 1024 * 1024)   // 6 GiB
	return &VirtualMemoryStat{
		Total:       total,
		Used:        used,
		UsedPercent: (float64(used) / float64(total)) * 100,
	}, nil
}

