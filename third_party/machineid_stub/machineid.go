package machineid

import "errors"

// SMBIOSUUID returns a stable machine identifier (SMBIOS UUID).
//
// This is an offline stub used when outbound network is unavailable.
// Replace this stub with the real implementation in production builds.
func SMBIOSUUID() (string, error) {
	// Deterministic placeholder; production should return the real SMBIOS UUID.
	return "00000000-0000-0000-0000-000000000000", nil
}

var _ = errors.New

