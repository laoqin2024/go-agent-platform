//go:build linux

package collector

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"time"
)

func collectServicesWithContext(ctx context.Context) ([]ServiceInfo, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	// systemctl 输出列：UNIT LOAD ACTIVE SUB DESCRIPTION
	cmd := exec.CommandContext(cmdCtx, "systemctl", "list-units", "--type=service", "--all", "--no-pager", "--no-legend")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, err
	}

	lines := bytes.Split(out, []byte("\n"))
	now := time.Now()
	services := make([]ServiceInfo, 0, 128)

	for _, line := range lines {
		s := strings.TrimSpace(string(line))
		if s == "" {
			continue
		}
		fields := strings.Fields(s)
		if len(fields) < 4 {
			continue
		}

		unit := fields[0]
		active := fields[2]
		sub := fields[3]

		status := "Stopped"
		if active == "active" && strings.Contains(strings.ToLower(sub), "running") {
			status = "Running"
		}

		services = append(services, ServiceInfo{
			Name:        unit,
			Status:      status,
			CollectedAt: now,
		})
	}

	return services, nil
}

