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
}
