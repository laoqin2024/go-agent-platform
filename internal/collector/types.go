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

type NetworkStats struct {
	// Cumulative bytes since system boot (or last reset depending on OS semantics).
	BytesRecv uint64
	BytesSent uint64
}

type HostMetrics struct {
	Hostname   string `json:"hostname"`
	Fingerprint string `json:"fingerprint"`
	CPU      CPUStats
	Memory   MemoryStats
	Disk     DiskStats
	Network  NetworkStats
	CollectedAt time.Time
}

