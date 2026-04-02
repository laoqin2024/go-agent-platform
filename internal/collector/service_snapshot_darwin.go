//go:build darwin

package collector

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"time"
)

func collectServicesWithContext(ctx context.Context) ([]ServiceInfo, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()

	// `launchctl list` output:
	// PID\tStatus\tLabel
	cmd := exec.CommandContext(cmdCtx, "launchctl", "list")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, err
	}

	lines := bytes.Split(out, []byte("\n"))
	now := time.Now()
	services := make([]ServiceInfo, 0, 128)

	for _, lb := range lines {
		line := strings.TrimSpace(string(lb))
		if line == "" {
			continue
		}
		// Try to parse with whitespace delim.
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		statusField := fields[1]
		label := fields[2]

		// Status 0 usually means loaded/running.
		status := "Stopped"
		if statusField == "0" {
			status = "Running"
		}

		services = append(services, ServiceInfo{
			Name:        label,
			Status:      status,
			CollectedAt: now,
		})
	}

	return services, nil
}

