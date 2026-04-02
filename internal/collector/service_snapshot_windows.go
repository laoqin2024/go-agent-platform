//go:build windows
// +build windows

package collector

import (
	"context"
	"time"

	"golang.org/x/sys/windows/svc/mgr"
)

func collectServicesWithContext(ctx context.Context) ([]ServiceInfo, error) {
	// Use SCM APIs via x/sys/windows/svc/mgr to avoid WMI/exec
	m, err := mgr.Connect()
	if err != nil {
		return nil, err
	}
	defer m.Disconnect()

	names, err := m.ListServices()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	out := make([]ServiceInfo, 0, len(names))
	for _, name := range names {
		select {
		case <-ctx.Done():
			return out, nil
		default:
		}
		s, err := m.OpenService(name)
		if err != nil {
			continue
		}
		status, _ := s.Query()
		cfg, _ := s.Config()
		_ = s.Close()
		st := "Stopped"
		if status.State == 4 /* SERVICE_RUNNING */ {
			st = "Running"
		}
		display := cfg.DisplayName
		if display == "" {
			display = name
		}
		out = append(out, ServiceInfo{
			Name:        display,
			Status:      st,
			CollectedAt: now,
		})
	}
	return out, nil
}

