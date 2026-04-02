package collector

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
)

type HostCollector struct {
	rootPath string

	// CPU sample interval used by cpu.PercentWithContext (it internally performs two samples over this duration).
	cpuSampleInterval time.Duration

	// Collect timeout applied to each gopsutil call to avoid indefinite blocking under extreme load.
	collectTimeout time.Duration
}

type HostCollectorOption func(*HostCollector)

func WithRootPath(p string) HostCollectorOption {
	return func(c *HostCollector) {
		if p != "" {
			c.rootPath = p
		}
	}
}

func WithCPUSampleInterval(d time.Duration) HostCollectorOption {
	return func(c *HostCollector) {
		if d > 0 {
			c.cpuSampleInterval = d
		}
	}
}

func WithCollectTimeout(d time.Duration) HostCollectorOption {
	return func(c *HostCollector) {
		if d > 0 {
			c.collectTimeout = d
		}
	}
}

func NewHostCollector(opts ...HostCollectorOption) *HostCollector {
	root := defaultRootPath()
	h := &HostCollector{
		rootPath:          root,
		cpuSampleInterval: 500 * time.Millisecond,
		collectTimeout:    2 * time.Second,
	}
	for _, opt := range opts {
		opt(h)
	}
	if h.collectTimeout < h.cpuSampleInterval {
		// Ensure the cpu.PercentWithContext call has enough time to perform its internal sampling.
		h.collectTimeout = h.cpuSampleInterval + 250*time.Millisecond
	}
	return h
}

func defaultRootPath() string {
	if runtime.GOOS == "windows" {
		return `C:\`
	}
	// "root directory" for Unix-like systems.
	return string(filepath.Separator)
}

// Collect returns the host metrics. It uses an internal timeout to avoid indefinite blocking.
func (c *HostCollector) Collect() (HostMetrics, error) {
	return c.CollectWithContext(context.Background())
}

// CollectWithContext collects host metrics using ctx and applies an additional internal timeout per gopsutil call.
func (c *HostCollector) CollectWithContext(ctx context.Context) (HostMetrics, error) {
	if ctx == nil {
		return HostMetrics{}, errors.New("collector: nil context")
	}

	// Apply internal timeout for each call. Since cpu.PercentWithContext performs two samples over the interval,
	// we must ensure the timeout is > cpuSampleInterval.
	timeoutCtx := func() (context.Context, context.CancelFunc) {
		if c.collectTimeout <= 0 {
			return context.WithCancel(ctx)
		}
		return context.WithTimeout(ctx, c.collectTimeout)
	}

	// CPU: total CPU utilization percent.
	cpuCtx, cpuCancel := timeoutCtx()
	defer cpuCancel()
	cpuPercents, err := cpu.PercentWithContext(cpuCtx, c.cpuSampleInterval, false)
	if err != nil {
		return HostMetrics{}, fmt.Errorf("collector: cpu percent: %w", err)
	}
	var totalCPU float64
	if len(cpuPercents) > 0 {
		totalCPU = cpuPercents[0]
	}

	// Memory.
	memCtx, memCancel := timeoutCtx()
	defer memCancel()
	vm, err := mem.VirtualMemoryWithContext(memCtx)
	if err != nil {
		return HostMetrics{}, fmt.Errorf("collector: virtual memory: %w", err)
	}

	// Disk: root directory (or C:\ on Windows).
	diskCtx, diskCancel := timeoutCtx()
	defer diskCancel()
	usage, err := disk.UsageWithContext(diskCtx, c.rootPath)
	if err != nil {
		return HostMetrics{}, fmt.Errorf("collector: disk usage: %w", err)
	}

	// Network: cumulative bytes.
	netCtx, netCancel := timeoutCtx()
	defer netCancel()
	ioCounters, err := net.IOCountersWithContext(netCtx, false)
	if err != nil {
		return HostMetrics{}, fmt.Errorf("collector: io counters: %w", err)
	}
	var recv, sent uint64
	for _, v := range ioCounters {
		recv += v.BytesRecv
		sent += v.BytesSent
	}

	hostname, _ := os.Hostname()
	fingerprint := stableFingerprint(hostname)

	return HostMetrics{
		Hostname:   hostname,
		Fingerprint: fingerprint,
		CPU: CPUStats{
			Total: totalCPU,
		},
		Memory: MemoryStats{
			Total:       vm.Total,
			Used:        vm.Used,
			UsedPercent: vm.UsedPercent,
		},
		Disk: DiskStats{
			Path:        c.rootPath,
			Total:       usage.Total,
			Used:        usage.Used,
			Free:        usage.Free,
			UsedPercent: usage.UsedPercent,
		},
		Network: NetworkStats{
			BytesRecv: recv,
			BytesSent: sent,
		},
		CollectedAt: time.Now(),
	}, nil
}

