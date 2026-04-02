//go:build linux

package collector

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"time"
)

func collectProcessesWithContext(ctx context.Context) ([]ProcessInfo, error) {
	// Read /proc to enumerate processes and gather basic memory info (RSS).
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}

	procs := make([]ProcessInfo, 0, len(entries))
	now := time.Now()
	for _, e := range entries {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		if !e.IsDir() {
			continue
		}

		pidStr := e.Name()
		if pidStr == "" {
			continue
		}

		// Only consider numeric directories.
		isNumeric := true
		for _, r := range pidStr {
			if !unicode.IsDigit(r) {
				isNumeric = false
				break
			}
		}
		if !isNumeric {
			continue
		}

		pid64, err := strconv.ParseUint(pidStr, 10, 32)
		if err != nil {
			continue
		}
		pid := uint32(pid64)

		name := readProcessName(pid)
		exePath := readProcessExePath(pid)
		memBytes := readProcessRSSBytes(pid)

		procs = append(procs, ProcessInfo{
			PID:          pid,
			Name:         name,
			ExecPath:     exePath,
			MemoryBytes:  memBytes,
			CollectedAt:  now,
		})
	}

	return procs, nil
}

func readProcessName(pid uint32) string {
	// Prefer /proc/<pid>/comm (short name), fallback to cmdline first token.
	commPath := filepath.Join("/proc", strconv.FormatUint(uint64(pid), 10), "comm")
	if b, err := os.ReadFile(commPath); err == nil {
		return strings.TrimSpace(string(b))
	}

	cmdlinePath := filepath.Join("/proc", strconv.FormatUint(uint64(pid), 10), "cmdline")
	if b, err := os.ReadFile(cmdlinePath); err == nil {
		s := strings.ReplaceAll(string(b), "\x00", " ")
		s = strings.TrimSpace(s)
		if s == "" {
			return ""
		}
		parts := strings.Fields(s)
		if len(parts) > 0 {
			return parts[0]
		}
		return s
	}
	return ""
}

func readProcessRSSBytes(pid uint32) uint64 {
	// /proc/<pid>/status contains "VmRSS:" line in kB.
	statusPath := filepath.Join("/proc", strconv.FormatUint(uint64(pid), 10), "status")
	b, err := os.ReadFile(statusPath)
	if err != nil {
		return 0
	}

	lines := strings.Split(string(b), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "VmRSS:") {
			// Example: "VmRSS:   1234 kB"
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				v, err := strconv.ParseUint(fields[1], 10, 64)
				if err == nil {
					return v * 1024
				}
			}
			break
		}
	}
	return 0
}

func readProcessExePath(pid uint32) string {
	exeLink := filepath.Join("/proc", strconv.FormatUint(uint64(pid), 10), "exe")
	p, err := os.Readlink(exeLink)
	if err != nil {
		return ""
	}
	return p
}
