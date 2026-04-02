package collector

import (
	"context"
	"errors"
	"os"
	"runtime"
	"sync"
	"time"

	"log/slog"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/host"
)

type HardwareDetails struct {
	Hostname      string `json:"hostname"`
	Fingerprint   string `json:"fingerprint"`
	OS            string `json:"os"`
	KernelVersion string `json:"kernel_version"`
	KernelArch    string `json:"kernel_arch"`
	CPUModel      string `json:"cpu_model"`
	CoreCount     int32  `json:"core_count"`
	// Uptime in seconds (platform-specific; Windows via GetTickCount64)
	UptimeSeconds uint64 `json:"uptime_seconds,omitempty"`
	// Deep inventory
	PhysicalDisks []DiskInfo     `json:"physical_disks,omitempty"`
	MemorySlots   []RamInfo      `json:"memory_slots,omitempty"`
	GPUs          []GPUInfo      `json:"gpus,omitempty"`
	Mainboard     *MainboardInfo `json:"mainboard,omitempty"`
	NetworkIfaces []NetworkIfaceInfo `json:"network_ifaces,omitempty"`
	CollectedAt   time.Time      `json:"collected_at"`
}

// DiskInfo describes a physical disk device.
type DiskInfo struct {
	Model     string `json:"model,omitempty"`
	Serial    string `json:"serial,omitempty"`
	SizeBytes uint64 `json:"size_bytes,omitempty"`
	BusType   string `json:"bus_type,omitempty"`   // NVMe/SATA/USB/PCIe...
	DriveType string `json:"drive_type,omitempty"` // SSD/HDD/Unknown
}

// RamInfo describes a memory dimm/slot.
type RamInfo struct {
	Slot         string `json:"slot,omitempty"`
	SizeBytes    uint64 `json:"size_bytes,omitempty"`
	SpeedMHz     uint32 `json:"speed_mhz,omitempty"`
	Manufacturer string `json:"manufacturer,omitempty"`
	Type         string `json:"type,omitempty"`
}

// GPUInfo describes a GPU and its VRAM.
type GPUInfo struct {
	Model  string `json:"model,omitempty"`
	VRAMMB uint64 `json:"vram_mb,omitempty"`
}

// MainboardInfo describes baseboard.
type MainboardInfo struct {
	Manufacturer string `json:"manufacturer,omitempty"`
	Model        string `json:"model,omitempty"`
	Version      string `json:"version,omitempty"`
	Serial       string `json:"serial,omitempty"`
}

// NetworkIfaceInfo describes a network interface and its addresses.
type NetworkIfaceInfo struct {
	Name      string   `json:"name,omitempty"`
	MAC       string   `json:"mac,omitempty"`
	IPv4      []string `json:"ipv4,omitempty"`
	IPv6      []string `json:"ipv6,omitempty"`
	MTU       int      `json:"mtu,omitempty"`
	IsUp      bool     `json:"is_up"`
	IsLoopback bool    `json:"is_loopback"`
}

// HardwareCollector collects host hardware inventory.
// It uses a small in-memory cache to avoid repeatedly calling gopsutil/machineid.
type HardwareCollector struct {
	cacheTTL time.Duration
	timeout  time.Duration

	mu     sync.Mutex
	lastAt time.Time
	cache  HardwareDetails
	logger *slog.Logger
}

type HardwareCollectorOption func(*HardwareCollector)

func WithHardwareCacheTTL(d time.Duration) HardwareCollectorOption {
	return func(h *HardwareCollector) {
		if d > 0 {
			h.cacheTTL = d
		}
	}
}

func WithHardwareTimeout(d time.Duration) HardwareCollectorOption {
	return func(h *HardwareCollector) {
		if d > 0 {
			h.timeout = d
		}
	}
}

func WithHardwareLogger(l *slog.Logger) HardwareCollectorOption {
	return func(h *HardwareCollector) {
		h.logger = l
	}
}

func NewHardwareCollector(opts ...HardwareCollectorOption) *HardwareCollector {
	h := &HardwareCollector{
		cacheTTL: 24 * time.Hour,
		timeout:  8 * time.Second,
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

func (h *HardwareCollector) CollectWithContext(ctx context.Context) (HardwareDetails, error) {
	if ctx == nil {
		return HardwareDetails{}, errors.New("collector: nil context")
	}

	now := time.Now()
	h.mu.Lock()
	if !h.lastAt.IsZero() && now.Sub(h.lastAt) < h.cacheTTL {
		out := h.cache
		h.mu.Unlock()
		return out, nil
	}
	h.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()

	hostInfo, err := host.InfoWithContext(ctx)
	if err != nil && h.logger != nil {
		h.logger.Debug("hardware: host.InfoWithContext failed; fallback to runtime fields", "err", err)
	}

	cpuInfos, err := cpu.InfoWithContext(ctx)
	if err != nil && h.logger != nil {
		h.logger.Debug("hardware: cpu.InfoWithContext failed; fallback to empty cpu model", "err", err)
	}

	cores, err := cpu.CountsWithContext(ctx, false /* logical */)
	if err != nil {
		cores = 0
	}
	var cpuModel string
	var coreCount int32
	if len(cpuInfos) > 0 {
		cpuModel = cpuInfos[0].ModelName
		coreCount = cpuInfos[0].Cores
	}
	if cores > 0 {
		coreCount = int32(cores)
	}

	hostname, _ := os.Hostname()
	if hostInfo != nil && hostInfo.Hostname != "" {
		hostname = hostInfo.Hostname
	}

	fingerprint := stableFingerprint(hostname)

	out := HardwareDetails{
		Hostname:      hostname,
		Fingerprint:   fingerprint,
		OS:            runtime.GOOS,
		KernelVersion: "",
		KernelArch:    runtime.GOARCH,
		CPUModel:      cpuModel,
		CoreCount:     coreCount,
		UptimeSeconds: collectUptimeSeconds(ctx),
		CollectedAt:   now,
	}
	if hostInfo != nil {
		if hostInfo.OS != "" {
			out.OS = hostInfo.OS
		}
		if hostInfo.KernelVersion != "" {
			out.KernelVersion = hostInfo.KernelVersion
		}
		if hostInfo.KernelArch != "" {
			out.KernelArch = hostInfo.KernelArch
		}
	}

	// Deep inventory using best-effort platform helpers (with same ctx).
	if disks, err := collectPhysicalDisks(ctx); err == nil {
		out.PhysicalDisks = disks
	} else if h.logger != nil {
		h.logger.Debug("hardware: collectPhysicalDisks failed", "err", err)
	}
	if rams, err := collectMemorySlots(ctx); err == nil {
		out.MemorySlots = rams
	} else if h.logger != nil {
		h.logger.Debug("hardware: collectMemorySlots failed", "err", err)
	}
	if gpus, err := collectGPUs(ctx); err == nil {
		out.GPUs = gpus
	} else if h.logger != nil {
		h.logger.Debug("hardware: collectGPUs failed", "err", err)
	}
	if mb, err := collectMainboard(ctx); err == nil {
		out.Mainboard = mb
	} else if h.logger != nil {
		h.logger.Debug("hardware: collectMainboard failed", "err", err)
	}
	if ifaces, err := collectNetworkIfaces(ctx); err == nil {
		out.NetworkIfaces = ifaces
	} else if h.logger != nil {
		h.logger.Debug("hardware: collectNetworkIfaces failed", "err", err)
	}

	h.mu.Lock()
	h.cache = out
	h.lastAt = now
	h.mu.Unlock()

	return out, nil
}
