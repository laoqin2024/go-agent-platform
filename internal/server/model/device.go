package model

// DeviceInfo is a lightweight representation for device list UI.
type DeviceInfo struct {
	DeviceID     string  `json:"device_id"`
	Hostname     string  `json:"hostname,omitempty"`
	OS           string  `json:"os,omitempty"`
	IP           string  `json:"ip,omitempty"`
	MAC          string  `json:"mac,omitempty"`
	IfaceType    string  `json:"iface_type,omitempty"`
	CPUPercent   float64 `json:"cpu_percent,omitempty"`
	MemUsedPct   float64 `json:"mem_used_percent,omitempty"`
	UpdatedAtSec int64   `json:"updated_at,omitempty"`
	Online       bool    `json:"online"`

	// Risk summary for device list UI (derived from latest security_snapshot).
	HasCriticalRisk   bool  `json:"has_critical_risk,omitempty"`   // currently critical (last snapshot)
	HadCriticalRisk   bool  `json:"had_critical_risk,omitempty"`   // ever critical (persisted until TTL expiry)
	LastCriticalAtSec int64 `json:"last_critical_at,omitempty"`    // last time seen critical
}
