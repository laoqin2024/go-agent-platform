package cpu

import (
	"context"
	"errors"
	"time"
)

type InfoStat struct {
	ModelName string
	Cores     int32
}

// PercentWithContext is a stub implementation that respects context cancellation.
// In the real gopsutil, it performs two samples over the provided interval.
func PercentWithContext(ctx context.Context, interval time.Duration, _ bool) ([]float64, error) {
	if ctx == nil {
		return nil, errors.New("gopsutil stub: nil context")
	}
	if interval > 0 {
		timer := time.NewTimer(interval)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			// sampled
		}
	}
	// Return a deterministic pseudo value.
	return []float64{12.34}, nil
}

func InfoWithContext(ctx context.Context) ([]InfoStat, error) {
	if ctx == nil {
		return nil, errors.New("gopsutil stub: nil context")
	}
	// Keep it fast.
	return []InfoStat{
		{ModelName: "Stub CPU Model", Cores: 8},
	}, nil
}

// CountsWithContext returns number of logical or physical cores.
func CountsWithContext(ctx context.Context, logical bool) (int, error) {
	if ctx == nil {
		return 0, errors.New("gopsutil stub: nil context")
	}
	if logical {
		return 16, nil
	}
	return 8, nil
}
