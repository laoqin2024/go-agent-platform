//go:build darwin

package collector

import (
	"bytes"
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func collectProcessesWithContext(ctx context.Context) ([]ProcessInfo, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// ps output columns: pid comm rss_kb command
	// rss is in KB; command is the full command line.
	cmd := exec.CommandContext(cmdCtx, "ps", "-axo", "pid=,comm=,rss=,command=")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, err
	}

	lines := bytes.Split(out, []byte("\n"))
	now := time.Now()
	procs := make([]ProcessInfo, 0, 256)

	for _, lb := range lines {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		line := strings.TrimSpace(string(lb))
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		// Expected: pid, comm, rss, command...
		if len(fields) < 4 {
			continue
		}

		pid64, err := strconv.ParseUint(fields[0], 10, 32)
		if err != nil {
			continue
		}
		name := fields[1]
		rssKB, err := strconv.ParseUint(fields[2], 10, 64)
		if err != nil {
			rssKB = 0
		}
		execPath := fields[3] // best-effort: first token of command

		procs = append(procs, ProcessInfo{
			PID:          uint32(pid64),
			Name:         name,
			ExecPath:     execPath,
			MemoryBytes:  rssKB * 1024,
			CollectedAt:  now,
		})
	}

	return procs, nil
}

