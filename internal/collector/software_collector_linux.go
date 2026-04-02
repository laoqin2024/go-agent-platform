//go:build linux

package collector

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"
)

func collectSoftwareWithContext(ctx context.Context) ([]SoftwareInfo, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	// Try dpkg first; if not available, fall back to rpm.
	infos, err := collectFromDPKG(ctx)
	if err == nil {
		return infos, nil
	}
	if !errors.Is(err, errCommandNotAvailable) {
		// If dpkg exists but failed, still try rpm as fallback.
	}
	return collectFromRPM(ctx)
}

var errCommandNotAvailable = errors.New("collector: package manager command not available")

func collectFromDPKG(ctx context.Context) ([]SoftwareInfo, error) {
	// dpkg-query lists installed packages.
	// Output: <package>\t<version>
	cmdCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "dpkg-query", "-W", "-f", "${Package}\t${Version}\n")
	out, err := cmd.CombinedOutput()
	if err != nil {
		if isCommandNotFound(err) {
			return nil, errCommandNotAvailable
		}
		return nil, err
	}

	lines := bytes.Split(out, []byte("\n"))
	infos := make([]SoftwareInfo, 0, 256)
	for _, line := range lines {
		s := strings.TrimSpace(string(line))
		if s == "" {
			continue
		}
		fields := strings.SplitN(s, "\t", 2)
		if len(fields) != 2 {
			continue
		}
		name := strings.TrimSpace(fields[0])
		version := strings.TrimSpace(fields[1])
		infos = append(infos, SoftwareInfo{
			Name:        name,
			Version:     version,
			Publisher:   "",
			InstallDate: nil,
		})
	}
	return infos, nil
}

func collectFromRPM(ctx context.Context) ([]SoftwareInfo, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "rpm", "-qa")
	out, err := cmd.CombinedOutput()
	if err != nil {
		if isCommandNotFound(err) {
			return nil, errCommandNotAvailable
		}
		return nil, err
	}

	lines := bytes.Split(out, []byte("\n"))
	infos := make([]SoftwareInfo, 0, 256)
	for _, line := range lines {
		s := strings.TrimSpace(string(line))
		if s == "" {
			continue
		}
		// Example: bash-5.2.15-2.el9.x86_64
		name, version := parseRPMLine(s)
		if name == "" {
			continue
		}
		infos = append(infos, SoftwareInfo{
			Name:        name,
			Version:     version,
			Publisher:   "",
			InstallDate: nil,
		})
	}
	return infos, nil
}

func parseRPMLine(line string) (name string, version string) {
	// Split first '-' as boundary between NAME and rest.
	parts := strings.SplitN(line, "-", 2)
	if len(parts) < 2 {
		return "", ""
	}
	name = strings.TrimSpace(parts[0])
	rest := strings.TrimSpace(parts[1])
	// Remove arch suffix (roughly) by dropping last '.' segment if it looks like arch.
	// This keeps "version-release" part.
	if idx := strings.LastIndex(rest, "."); idx > 0 {
		ver := rest[:idx]
		if ver != "" {
			return name, ver
		}
	}
	return name, rest
}

func isCommandNotFound(err error) bool {
	// Best-effort: different distros/Go versions may wrap errors differently.
	// We just look for typical substrings.
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "executable file not found") ||
		strings.Contains(msg, "no such file or directory") ||
		strings.Contains(msg, "not found")
}

