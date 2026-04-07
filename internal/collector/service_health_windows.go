//go:build windows
// +build windows

package collector

import (
	"context"
	"strings"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

func collectServiceHealthWithWhitelist(ctx context.Context, whitelist []string) ([]ServiceStat, map[uint32]string, map[string]string, error) {
	if ctx == nil {
		return nil, nil, nil, nil
	}
	wl := whitelistSet(whitelist)

	// Use SCM APIs; filter to failed + whitelist only.
	m, err := mgr.Connect()
	if err != nil {
		return nil, nil, nil, err
	}
	defer m.Disconnect()

	names, err := m.ListServices()
	if err != nil {
		return nil, nil, nil, err
	}

	deadline := time.Now().Add(8 * time.Second)
	stats := make([]ServiceStat, 0, 64)
	pidToSvc := make(map[uint32]string, 16)  // best-effort
	// cgroup -> service mapping is not applicable on Windows; keep an empty map to satisfy callers.
	cgToSvc := make(map[string]string)       // capacity hint not supported in make(map) signature

	for _, name := range names {
		if time.Now().After(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return stats, pidToSvc, cgToSvc, nil
		default:
		}

		s, err := m.OpenService(name)
		if err != nil {
			continue
		}
		st, _ := s.Query()
		cfg, _ := s.Config()
		_ = s.Close()

		display := cfg.DisplayName
		if display == "" {
			display = name
		}

		// Whitelist match by service name or display name.
		nameLower := strings.ToLower(name)
		displayLower := strings.ToLower(display)
		_, inWL := wl[nameLower]
		if !inWL {
			_, inWL = wl[displayLower]
		}

		// Strict failure definition:
		// - ProcessId == 0 (not running)
		// - Win32ExitCode != 0
		// This avoids false "failed" for trigger services that stop normally.
		exit := int64(st.Win32ExitCode)
		isFailed := st.ProcessId == 0 && st.Win32ExitCode != 0

		if !isFailed && !inWL {
			continue
		}

		status := "Inactive"
		if st.State == svc.Running || st.ProcessId != 0 {
			status = "Running"
		} else if isFailed {
			status = "Failed"
		}

		startType := ""
		switch cfg.StartType {
		case mgr.StartAutomatic:
			startType = "auto"
		case mgr.StartManual:
			startType = "manual"
		case mgr.StartDisabled:
			startType = "disabled"
		}

		stats = append(stats, ServiceStat{
			Name:      display,
			Status:    status,
			ExitCode:  exit,
			StartType: startType,
			// RestartCount/UptimeSec not available via this API without extra calls.
		})
	}

	return stats, pidToSvc, cgToSvc, nil
}

