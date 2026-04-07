package collector

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
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
	Partitions    []PartitionInfo    `json:"partitions,omitempty"`
	SmartInfo     []DiskSmartInfo    `json:"smart_info,omitempty"`
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

// PartitionInfo describes a logical volume/partition/mount.
type PartitionInfo struct {
	Name       string `json:"name,omitempty"`       // e.g. C:, /dev/sda1, disk3s1
	Mountpoint string `json:"mountpoint,omitempty"` // e.g. C:\, /, /Volumes/Macintosh HD
	FSType     string `json:"fs_type,omitempty"`    // ntfs, ext4, apfs...
	SizeBytes  uint64 `json:"size_bytes,omitempty"`
	FreeBytes  uint64 `json:"free_bytes,omitempty"`
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
	if parts, err := collectPartitions(ctx); err == nil {
		out.Partitions = parts
	} else if h.logger != nil {
		h.logger.Debug("hardware: collectPartitions failed", "err", err)
	}

	if smart, err := collectDiskSmart(ctx, h.logger); err == nil {
		out.SmartInfo = smart
	} else if h.logger != nil {
		h.logger.Debug("hardware: collectDiskSmart failed", "err", err)
	}

	h.mu.Lock()
	h.cache = out
	h.lastAt = now
	h.mu.Unlock()

	return out, nil
}

func collectDiskSmart(ctx context.Context, logger *slog.Logger) ([]DiskSmartInfo, error) {
	switch runtime.GOOS {
	case "linux", "darwin":
		return collectDiskSmartWithSmartctl(ctx, logger)
	case "windows":
		return collectDiskSmartWindows(ctx, logger)
	default:
		return nil, nil
	}
}

func hwDebugEnabled() bool {
	return strings.TrimSpace(os.Getenv("HW_DEBUG")) == "1"
}

// collectDiskSmartWithSmartctl uses smartctl -j -a to gather SMART info on Unix-like OSes.
func collectDiskSmartWithSmartctl(ctx context.Context, logger *slog.Logger) ([]DiskSmartInfo, error) {
	// smartctl can be slow; cap overall time.
	sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Discover devices via smartctl --scan-open
	scanCmd := exec.CommandContext(sctx, "smartctl", "--scan-open")
	out, err := scanCmd.Output()
	if err != nil {
		if hwDebugEnabled() && logger != nil {
			logger.Debug("hardware: smartctl --scan-open failed", "err", err)
		}
		return nil, nil
	}

	lines := strings.Split(string(out), "\n")
	var devs []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		dev := fields[0]
		if strings.HasPrefix(dev, "/dev/") {
			devs = append(devs, dev)
		}
	}
	if len(devs) == 0 {
		return nil, nil
	}

	type smartJSON struct {
		ModelName string `json:"model_name"`
		Device    struct {
			Name string `json:"name"`
		} `json:"device"`
		SmartStatus struct {
			Passed bool `json:"passed"`
		} `json:"smart_status"`
		Temperature struct {
			Current int64 `json:"current"`
		} `json:"temperature"`
		PowerOnTime struct {
			Hours int64 `json:"hours"`
		} `json:"power_on_time"`
	}

	var outInfos []DiskSmartInfo

	for _, dev := range devs {
		devCtx, cancelDev := context.WithTimeout(sctx, 6*time.Second)
		cmd := exec.CommandContext(devCtx, "smartctl", "-j", "-a", dev)
		b, err := cmd.Output()
		cancelDev()
		if err != nil {
			if hwDebugEnabled() && logger != nil {
				logger.Debug("hardware: smartctl -j -a failed", "device", dev, "err", err)
			}
			continue
		}

		var sj smartJSON
		if err := json.Unmarshal(b, &sj); err != nil {
			if hwDebugEnabled() && logger != nil {
				logger.Debug("hardware: smartctl json unmarshal failed", "device", dev, "err", err)
			}
			continue
		}

		deviceName := dev
		if sj.Device.Name != "" {
			deviceName = sj.Device.Name
		}
		model := sj.ModelName
		status := "UNKNOWN"
		if sj.SmartStatus.Passed {
			status = "PASSED"
		} else {
			status = "FAILED"
		}

		info := DiskSmartInfo{
			DeviceName:   deviceName,
			Model:        model,
			Status:       status,
			Temperature:  sj.Temperature.Current,
			PowerOnHours: sj.PowerOnTime.Hours,
		}
		outInfos = append(outInfos, info)

		if hwDebugEnabled() && logger != nil {
			logger.Info("[HW] Disk SMART (smartctl)",
				"device", info.DeviceName,
				"status", info.Status,
				"temp_c", info.Temperature,
				"power_on_hours", info.PowerOnHours,
			)
		}
	}

	return outInfos, nil
}

// collectDiskSmartWindows uses wmic to get basic health status on Windows.
func collectDiskSmartWindows(ctx context.Context, logger *slog.Logger) ([]DiskSmartInfo, error) {
	wctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	cmd := exec.CommandContext(wctx, "wmic", "diskdrive", "get", "DeviceID,Model,Status")
	out, err := cmd.Output()
	if err != nil {
		if hwDebugEnabled() && logger != nil {
			logger.Debug("hardware: wmic diskdrive get failed", "err", err)
		}
		return nil, nil
	}

	lines := strings.Split(string(out), "\n")
	if len(lines) <= 1 {
		return nil, nil
	}

	var infos []DiskSmartInfo
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 3 {
			continue
		}
		deviceID := parts[0]
		statusRaw := parts[len(parts)-1]
		model := strings.Join(parts[1:len(parts)-1], " ")

		status := "UNKNOWN"
		if strings.EqualFold(statusRaw, "OK") || strings.EqualFold(statusRaw, "PASSED") {
			status = "PASSED"
		} else if statusRaw != "" {
			status = "FAILED"
		}

		info := DiskSmartInfo{
			DeviceName: deviceID,
			Model:      model,
			Status:     status,
		}
		infos = append(infos, info)

		if hwDebugEnabled() && logger != nil {
			logger.Info("[HW] Disk SMART (wmic)",
				"device", info.DeviceName,
				"status", info.Status,
			)
		}
	}
	return infos, nil
}
