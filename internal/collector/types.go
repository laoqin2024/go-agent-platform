package collector

import "time"

type CPUStats struct {
	// Total is the overall CPU utilization percent (0-100).
	Total float64
}

type MemoryStats struct {
	Total       uint64
	Used        uint64
	UsedPercent float64
}

type DiskStats struct {
	Path        string
	Total       uint64
	Used        uint64
	Free        uint64
	UsedPercent float64
}

// DiskIOStats represents derived per-device I/O performance metrics over the
// last collection interval.
type DiskIOStats struct {
	DeviceName  string  `json:"device_name,omitempty"`
	ReadKBps    float64 `json:"read_kbps,omitempty"`
	WriteKBps   float64 `json:"write_kbps,omitempty"`
	ReadIOPS    float64 `json:"read_iops,omitempty"`
	WriteIOPS   float64 `json:"write_iops,omitempty"`
	UtilPercent float64 `json:"util_percent,omitempty"`
}

type NetworkStats struct {
	// Cumulative bytes since system boot (or last reset depending on OS semantics).
	BytesRecv uint64
	BytesSent uint64
}

// NetworkInterfaceStats describes per-interface cumulative counters.
// Values are best-effort; on some platforms/capabilities they may be 0.
type NetworkInterfaceStats struct {
	Name string `json:"name,omitempty"`

	BytesRecv uint64 `json:"bytes_recv,omitempty"`
	BytesSent uint64 `json:"bytes_sent,omitempty"`

	PacketsRecv uint64 `json:"packets_recv,omitempty"`
	PacketsSent uint64 `json:"packets_sent,omitempty"`

	ErrorsIn  uint64 `json:"errors_in,omitempty"`
	ErrorsOut uint64 `json:"errors_out,omitempty"`

	DropIn  uint64 `json:"drop_in,omitempty"`
	DropOut uint64 `json:"drop_out,omitempty"`
}

type HostMetrics struct {
	Hostname    string `json:"hostname"`
	Fingerprint string `json:"fingerprint"`
	CPU         CPUStats
	Memory      MemoryStats
	Disk        DiskStats
	Network     NetworkStats

	// DiskIO contains per-device I/O performance metrics derived from gopsutil
	// disk.IOCounters deltas between collection intervals.
	DiskIO []DiskIOStats `json:"disk_io,omitempty"`

	// network_interfaces provides per-active-NIC counters for deeper monitoring.
	NetworkInterfaces []NetworkInterfaceStats `json:"network_interfaces,omitempty"`

	// GpuStats provides per-GPU realtime telemetry (best-effort).
	GpuStats []GpuStat `json:"gpu_stats,omitempty"`

	// SoftwareList provides a best-effort snapshot of installed software assets on the host.
	// For detailed, low-frequency inventory, see software_inventory batch items.
	SoftwareList []SoftwareItem `json:"software_list,omitempty"`

	// Processes contains a best-effort Top-N process insight snapshot (for UI).
	Processes []ProcessStat `json:"processes,omitempty"`

	// Services contains a best-effort service health snapshot (failed + whitelist only).
	Services []ServiceStat `json:"services,omitempty"`

	// SecuritySnapshot contains best-effort host security telemetry (e.g., listening ports).
	SecuritySnapshot SecuritySnapshot `json:"security_snapshot,omitempty"`

	// PingLatencyMs is the best-effort baseline RTT (ms) against configured targets.
	PingLatencyMs uint64 `json:"ping_latency_ms,omitempty"`
	CollectedAt   time.Time
}

type ServiceStat struct {
	Name string `json:"name"`

	// Status is best-effort: Active/Inactive/Failed/Running/Stopped...
	Status string `json:"status,omitempty"`

	ExitCode     int64 `json:"exit_code,omitempty"`
	RestartCount int64 `json:"restart_count,omitempty"`
	UptimeSec    int64 `json:"uptime_sec,omitempty"`

	// StartType is best-effort (Windows): auto/manual/disabled
	StartType string `json:"start_type,omitempty"`

	// ContainerCount is best-effort (Docker service): number of containers (usually running) on this host.
	ContainerCount int64 `json:"container_count,omitempty"`
}

// ProcessStat is a compact but enriched view of a process for monitoring UI.
// NOTE: Fields are best-effort; some platforms may return empty values due to permissions.
type ProcessStat struct {
	PID      uint32 `json:"pid"`
	Name     string `json:"name,omitempty"`
	ExecPath string `json:"exec_path,omitempty"`

	Username    string `json:"username,omitempty"`
	ListenPorts []int  `json:"listening_ports,omitempty"`

	// UptimeSec is seconds since process creation time.
	UptimeSec int64 `json:"uptime_sec,omitempty"`

	// CPUPercent is the best-effort CPU usage percentage.
	CPUPercent float64 `json:"cpu_percent,omitempty"`

	// MemoryMB is the best-effort resident memory usage (MB).
	MemoryMB float64 `json:"memory_mb,omitempty"`

	// Status is a best-effort running state string (e.g., running/sleeping).
	Status string `json:"status,omitempty"`

	// ServiceName is a best-effort owning service label (systemd/launchd/Windows service).
	ServiceName string `json:"service_name,omitempty"`

	// CGroup is a best-effort cgroup path (Linux) used to keep service mapping stable across PID changes.
	CGroup string `json:"cgroup,omitempty"`

	// MemoryBytes keeps backwards compatibility with existing UI fields.
	MemoryBytes uint64 `json:"memory_bytes,omitempty"`

	CollectedAt time.Time `json:"collected_at,omitempty"`
}

// ProcessInfo is kept for backward compatibility with existing collectors/snapshots.
// It is an alias of ProcessStat.
type ProcessInfo = ProcessStat

// DiskSmartInfo contains best-effort SMART/health information per disk.
type DiskSmartInfo struct {
	DeviceName   string `json:"device_name,omitempty"`
	Model        string `json:"model,omitempty"`
	Status       string `json:"status,omitempty"`         // PASSED / FAILED / UNKNOWN
	Temperature  int64  `json:"temperature,omitempty"`    // Celsius
	PowerOnHours int64  `json:"power_on_hours,omitempty"` // Hours
}

// GpuStat describes one GPU device's realtime utilization metrics.
type GpuStat struct {
	Model         string  `json:"model,omitempty"`
	UtilPercent   float64 `json:"util_percent,omitempty"`
	MemoryUsedMB  float64 `json:"memory_used_mb,omitempty"`
	MemoryTotalMB float64 `json:"memory_total_mb,omitempty"`
	TemperatureC  float64 `json:"temperature,omitempty"`
}

// SoftwareItem represents one installed software asset on the host.
// Fields are best-effort and may be empty on some platforms.
type SoftwareItem struct {
	Name        string    `json:"name,omitempty"`
	Version     string    `json:"version,omitempty"`
	Publisher   string    `json:"publisher,omitempty"`
	InstallDate time.Time `json:"install_date,omitempty"`
}

// ListeningPort represents one socket that is currently listening.
type ListeningPort struct {
	Protocol string `json:"protocol,omitempty"` // tcp / udp
	Address  string `json:"address,omitempty"`  // e.g. 0.0.0.0 / 127.0.0.1 / ::
	Port     uint32 `json:"port,omitempty"`
	// PID is best-effort. On macOS/Linux without elevated privileges, PID may be 0.
	PID uint32 `json:"pid,omitempty"`
	// ProcessName is best-effort. If PID exists but permissions prevent resolving the name,
	// collector fills it as "Permission Denied (Unknown)" so the record isn't dropped.
	ProcessName string `json:"process_name,omitempty"`
	// Scope: public (0.0.0.0/::), local (127.0.0.1/::1), private/other best-effort classification
	Scope string `json:"scope,omitempty"` // public | local | other
	// IsHighRisk is true when a high-risk port (22, 3389, 445, 3306, 6379) is exposed on a public address (0.0.0.0/::).
	IsHighRisk bool `json:"is_high_risk,omitempty"`
}

