//go:build linux

package collector

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func collectServiceHealthWithWhitelist(ctx context.Context, whitelist []string) ([]ServiceStat, map[uint32]string, map[string]string, error) {
	if ctx == nil {
		return nil, nil, nil, nil
	}
	// If Docker is installed, ensure docker.service is monitored by default.
	if isDockerAvailable(ctx) {
		whitelist = append(whitelist, "docker.service")
	}
	wl := whitelistSet(whitelist)

	cmdCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	// UNIT LOAD ACTIVE SUB DESCRIPTION
	cmd := exec.CommandContext(cmdCtx, "systemctl", "list-units", "--type=service", "--all", "--no-pager", "--no-legend")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, nil, nil, err
	}

	failedOrWL := make(map[string]struct{}, 64)
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		unit := fields[0]
		active := strings.ToLower(fields[2])
		sub := strings.ToLower(fields[3])

		// Whitelist match: either exact unit (nginx.service) or substring token (nginx)
		unitLower := strings.ToLower(unit)
		_, inWL := wl[unitLower]
		if !inWL {
			// also match by stripping ".service"
			base := strings.TrimSuffix(unitLower, ".service")
			_, inWL = wl[base]
		}
		isFailed := active == "failed" || sub == "failed"
		if isFailed || inWL {
			failedOrWL[unit] = struct{}{}
		}
	}

	uptimeSec, _ := readLinuxUptimeSec()
	stats := make([]ServiceStat, 0, len(failedOrWL))
	pidToSvc := make(map[uint32]string, 32)
	cgToSvc := make(map[string]string, 32)

	for unit := range failedOrWL {
		select {
		case <-ctx.Done():
			return stats, pidToSvc, cgToSvc, nil
		default:
		}

		s, mainPID, cg := systemdShowService(ctx, unit, uptimeSec)
		if s.Name == "" {
			continue
		}
		// Docker metadata: container count (best-effort)
		if strings.Contains(strings.ToLower(s.Name), "docker") && isDockerAvailable(ctx) {
			if n, err := dockerContainerCount(ctx); err == nil && n >= 0 {
				s.ContainerCount = n
			}
		}
		stats = append(stats, s)
		if mainPID > 0 {
			pidToSvc[mainPID] = s.Name
		}
		if cg != "" {
			cgToSvc[cg] = s.Name
		}
	}
	return stats, pidToSvc, cgToSvc, nil
}

func isDockerAvailable(ctx context.Context) bool {
	cmdCtx, cancel := context.WithTimeout(ctx, 700*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, "docker", "version", "--format", "{{.Server.Version}}")
	out, err := cmd.CombinedOutput()
	return err == nil && len(bytes.TrimSpace(out)) > 0
}

func dockerContainerCount(ctx context.Context) (int64, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, 900*time.Millisecond)
	defer cancel()
	// Count running containers; lightweight and fast.
	cmd := exec.CommandContext(cmdCtx, "docker", "ps", "-q")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return 0, err
	}
	lines := bytes.Split(bytes.TrimSpace(out), []byte("\n"))
	if len(lines) == 1 && len(lines[0]) == 0 {
		return 0, nil
	}
	return int64(len(lines)), nil
}

func readLinuxUptimeSec() (int64, error) {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, err
	}
	parts := strings.Fields(string(b))
	if len(parts) < 1 {
		return 0, nil
	}
	f, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return 0, err
	}
	return int64(f), nil
}

func systemdShowService(ctx context.Context, unit string, bootUptimeSec int64) (ServiceStat, uint32, string) {
	cmdCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	// Use systemctl show for structured properties.
	props := []string{
		"Id",
		"ActiveState",
		"SubState",
		"ExecMainStatus",
		"NRestarts",
		"MainPID",
		"ControlGroup",
		"ActiveEnterTimestampMonotonic",
	}

	args := []string{"show", unit, "--no-pager"}
	for _, p := range props {
		args = append(args, "-p", p)
	}
	cmd := exec.CommandContext(cmdCtx, "systemctl", args...)
	out, err := cmd.CombinedOutput()
	if err != nil || len(out) == 0 {
		return ServiceStat{}, 0, ""
	}

	m := make(map[string]string, 16)
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		i := strings.IndexByte(line, '=')
		if i <= 0 {
			continue
		}
		k := line[:i]
		v := ""
		if i+1 < len(line) {
			v = line[i+1:]
		}
		m[k] = v
	}

	name := m["Id"]
	if name == "" {
		name = unit
	}
	active := m["ActiveState"]
	sub := m["SubState"]
	status := strings.TrimSpace(active)
	if sub != "" && sub != active {
		status = status + "/" + sub
	}

	exitCode, _ := strconv.ParseInt(m["ExecMainStatus"], 10, 64)
	restarts, _ := strconv.ParseInt(m["NRestarts"], 10, 64)
	mainPID64, _ := strconv.ParseUint(m["MainPID"], 10, 32)
	mainPID := uint32(mainPID64)
	cg := strings.TrimSpace(m["ControlGroup"])

	var uptime int64
	if bootUptimeSec > 0 {
		// microseconds since boot
		enterMonoUs, _ := strconv.ParseInt(m["ActiveEnterTimestampMonotonic"], 10, 64)
		if enterMonoUs > 0 {
			// now (since boot) ≈ /proc/uptime seconds
			nowUs := bootUptimeSec * 1_000_000
			delta := nowUs - enterMonoUs
			if delta > 0 {
				uptime = delta / 1_000_000
			}
		}
	}

	return ServiceStat{
		Name:         name,
		Status:       status,
		ExitCode:     exitCode,
		RestartCount: restarts,
		UptimeSec:    uptime,
	}, mainPID, cg
}

