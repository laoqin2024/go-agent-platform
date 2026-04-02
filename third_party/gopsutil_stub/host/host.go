package host

import (
	"context"
	"errors"
	"runtime"
	"time"
)

type InfoStat struct {
	Hostname      string
	OS            string
	KernelVersion string
	KernelArch    string
}

func InfoWithContext(ctx context.Context) (*InfoStat, error) {
	if ctx == nil {
		return nil, errors.New("gopsutil stub: nil context")
	}
	// Simulate a short call.
	timer := time.NewTimer(10 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
	}

	return &InfoStat{
		Hostname:      "stub-host",
		OS:            runtime.GOOS,
		KernelVersion: "0.0.0-stub",
		KernelArch:    runtime.GOARCH,
	}, nil
}

