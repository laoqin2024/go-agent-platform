//go:build !windows

package collector

func platformSMBIOSUUID() string {
	return ""
}

