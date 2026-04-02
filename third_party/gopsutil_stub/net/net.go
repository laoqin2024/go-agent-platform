package net

import (
	"context"
	"errors"
	"time"
)

type IOCountersStat struct {
	BytesRecv uint64
	BytesSent uint64
}

func IOCountersWithContext(ctx context.Context, _ bool) ([]IOCountersStat, error) {
	if ctx == nil {
		return nil, errors.New("gopsutil stub: nil context")
	}
	timer := time.NewTimer(10 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
	}

	// Deterministic totals.
	return []IOCountersStat{
		{BytesRecv: 123456789, BytesSent: 987654321},
	}, nil
}

