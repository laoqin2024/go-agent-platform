//go:build darwin

package collector

import (
	"bufio"
	"bytes"
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func collectServiceHealthWithWhitelist(ctx context.Context, whitelist []string) ([]ServiceStat, map[uint32]string, map[string]string, error) {
	if ctx == nil {
		return nil, nil, nil, nil
	}
	wl := whitelistSet(whitelist)

	cmdCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()

	// PID\tStatus\tLabel
	cmd := exec.CommandContext(cmdCtx, "launchctl", "list")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, nil, nil, err
	}

	stats := make([]ServiceStat, 0, 64)
	pidToSvc := make(map[uint32]string, 16)
	cgToSvc := make(map[string]string, 1)

	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "PID") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pidField := fields[0]
		statusField := fields[1] // last exit status
		label := fields[2]

		labelLower := strings.ToLower(label)
		_, inWL := wl[labelLower]
		if !inWL {
			// allow matching by substring token
			for k := range wl {
				if k != "" && strings.Contains(labelLower, k) {
					inWL = true
					break
				}
			}
		}

		exit, _ := strconv.ParseInt(statusField, 10, 64)
		isFailed := exit != 0
		if !isFailed && !inWL {
			continue
		}

		pid := int64(0)
		if pidField != "-" {
			pid, _ = strconv.ParseInt(pidField, 10, 64)
		}
		status := "Stopped"
		if pid > 0 {
			status = "Running"
		} else if isFailed {
			status = "Failed"
		}

		uptime := int64(0)
		if pid > 0 {
			uptime = psElapsedSeconds(ctx, pid)
		}

		stats = append(stats, ServiceStat{
			Name:      label,
			Status:    status,
			ExitCode:  exit,
			UptimeSec: uptime,
		})
		if pid > 0 && pid <= int64(^uint32(0)) {
			pidToSvc[uint32(pid)] = label
		}
	}

	return stats, pidToSvc, cgToSvc, nil
}

func psElapsedSeconds(ctx context.Context, pid int64) int64 {
	cmdCtx, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()
	// etimes: elapsed time since the process was started, in seconds.
	cmd := exec.CommandContext(cmdCtx, "ps", "-p", strconv.FormatInt(pid, 10), "-o", "etimes=")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return 0
	}
	s := strings.TrimSpace(string(out))
	v, _ := strconv.ParseInt(s, 10, 64)
	if v < 0 {
		return 0
	}
	return v
}

