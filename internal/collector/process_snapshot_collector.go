package collector

import (
	"context"
	"sync"
	"time"
)

type ServiceInfo struct {
	Name        string    `json:"name"`
	Status      string    `json:"status"` // "Running" or "Stopped"
	CollectedAt time.Time `json:"collected_at"`
}

type ProcessSnapshot struct {
	Processes   []ProcessInfo `json:"processes"`
	Services    []ServiceInfo `json:"services"`
	CollectedAt time.Time     `json:"collected_at"`
}

// ProcessSnapshotCollector collects a combined snapshot of processes + services.
// It is designed for periodic "real-time" debug/reporting.
type ProcessSnapshotCollector struct {
	timeout time.Duration
}

type ProcessSnapshotCollectorOption func(*ProcessSnapshotCollector)

func WithProcessSnapshotTimeout(d time.Duration) ProcessSnapshotCollectorOption {
	return func(ps *ProcessSnapshotCollector) {
		if d > 0 {
			ps.timeout = d
		}
	}
}

func NewProcessSnapshotCollector(opts ...ProcessSnapshotCollectorOption) *ProcessSnapshotCollector {
	ps := &ProcessSnapshotCollector{
		timeout: 10 * time.Second,
	}
	for _, opt := range opts {
		opt(ps)
	}
	return ps
}

func (ps *ProcessSnapshotCollector) CollectWithContext(ctx context.Context) (ProcessSnapshot, error) {
	if ctx == nil {
		return ProcessSnapshot{}, nil
	}

	ctx, cancel := context.WithTimeout(ctx, ps.timeout)
	defer cancel()

	now := time.Now()

	var (
		procs []ProcessInfo
		svcs  []ServiceInfo
		err1  error
		err2  error
	)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		procs, err1 = collectProcessesWithContext(ctx)
	}()
	go func() {
		defer wg.Done()
		svcs, err2 = collectServicesWithContext(ctx)
	}()
	wg.Wait()

	if err1 != nil {
		return ProcessSnapshot{}, err1
	}
	// If service collection fails, still return process data to keep pipeline flowing.
	if err2 == nil {
		for i := range svcs {
			svcs[i].CollectedAt = now
		}
	}

	return ProcessSnapshot{
		Processes:   procs,
		Services:    svcs,
		CollectedAt: now,
	}, nil
}

