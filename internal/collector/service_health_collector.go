package collector

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

func serviceWhitelistFromEnv() []string {
	// Comma-separated list; examples: "nginx,docker,mysql,agent-service"
	// New: MONITOR_SERVICES takes precedence.
	raw := strings.TrimSpace(os.Getenv("MONITOR_SERVICES"))
	if raw == "" {
		// Optional config file fallback for dynamic config.
		// If MONITOR_SERVICES_FILE is set, read it; else try ./monitor_services.txt (best-effort).
		if fp := strings.TrimSpace(os.Getenv("MONITOR_SERVICES_FILE")); fp != "" {
			if b, err := os.ReadFile(fp); err == nil {
				raw = strings.TrimSpace(string(b))
			}
		} else {
			if b, err := os.ReadFile(filepath.Join(".", "monitor_services.txt")); err == nil {
				raw = strings.TrimSpace(string(b))
			}
		}
	}
	if raw == "" {
		return []string{"nginx", "mysql", "agent-service"}
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		s := strings.TrimSpace(p)
		if s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return []string{"nginx", "mysql", "agent-service"}
	}
	return out
}

func whitelistSet(names []string) map[string]struct{} {
	m := make(map[string]struct{}, len(names))
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n != "" {
			m[strings.ToLower(n)] = struct{}{}
		}
	}
	return m
}

// collectServiceHealthWithContext returns failed services + user whitelist services only.
// It also returns best-effort mappings for process annotation/jump:
// - pidToService: main pid -> service name
// - cgroupToService: cgroup path -> service name (linux)
func collectServiceHealthWithContext(ctx context.Context) ([]ServiceStat, map[uint32]string, map[string]string, error) {
	wl := serviceWhitelistFromEnv()
	return collectServiceHealthWithWhitelist(ctx, wl)
}

// collectServiceHealthWithWhitelist is implemented per-OS in service_health_*.go.
// It returns:
// - stats: filtered service list
// - pidToService: best-effort mapping from main pid -> service name for process annotation/jump
// - cgroupToService: best-effort mapping from cgroup path -> service name (linux)
// (see OS-specific implementations)
