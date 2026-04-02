package model

import "encoding/json"

// ReportRequest is the required payload format for POST /api/v1/report.
// processes and software_list are stored as raw JSON arrays/objects.
type ReportRequest struct {
	DeviceID      string          `json:"device_id"`
	Processes     json.RawMessage `json:"processes"`
	SoftwareList  json.RawMessage `json:"software_list"`
	ReportedAtSec int64           `json:"reported_at,omitempty"`
}

type SnapshotPush struct {
	DeviceID     string          `json:"device_id"`
	Processes    json.RawMessage `json:"processes,omitempty"`
	SoftwareList json.RawMessage `json:"software_list,omitempty"`

	// Extended dashboard fields (compatible with scripts/debug_server).
	HostMetrics        json.RawMessage `json:"host_metrics,omitempty"`
	HardwareDetails    json.RawMessage `json:"hardware_details,omitempty"`
	SoftwareInventory  json.RawMessage `json:"software_inventory,omitempty"`
	ProcessSnapshot    json.RawMessage `json:"process_snapshot,omitempty"`
	ServiceSnapshot    json.RawMessage `json:"service_snapshot,omitempty"`
	NetworkConnections json.RawMessage `json:"network_connections,omitempty"`
	SecuritySnapshot   json.RawMessage `json:"security_snapshot,omitempty"`

	UpdatedAtSec int64 `json:"updated_at"`
}

