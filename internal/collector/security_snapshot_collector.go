package collector

import (
	"context"
	"strings"
	"time"
)

type NetConnInfo struct {
	Protocol    string `json:"protocol"`
	LocalAddr   string `json:"local_addr"`
	LocalPort   uint16 `json:"local_port"`
	RemoteAddr  string `json:"remote_addr,omitempty"`
	RemotePort  uint16 `json:"remote_port,omitempty"`
	State       string `json:"state,omitempty"` // LISTEN / ESTABLISHED
	PID         uint32 `json:"pid"`
	ProcessName string `json:"process_name,omitempty"`
}

type StartupItem struct {
	Location string `json:"location"`
	Name     string `json:"name"`
	Command  string `json:"command"`
}

type HotfixInfo struct {
	KB          string `json:"kb"`
	InstalledAt string `json:"installed_at,omitempty"`
}

type SecuritySnapshot struct {
	Connections []NetConnInfo  `json:"connections"`
	Startup     []StartupItem  `json:"startup"`
	Hotfixes    []HotfixInfo   `json:"hotfixes"`
	Listening   []ListeningPort `json:"listening,omitempty"`
	CollectedAt time.Time      `json:"collected_at"`
}

type SecurityCollector struct {
	timeout time.Duration
}

func NewSecurityCollector() *SecurityCollector {
	return &SecurityCollector{timeout: 8 * time.Second}
}

func (s *SecurityCollector) CollectWithContext(ctx context.Context) (SecuritySnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	conns, _ := collectNetworkConnections(ctx)
	conns = filterExternalEstablishedConns(conns)
	startup, _ := collectStartupItems(ctx)
	hotfixes, _ := collectHotfixes(ctx)

	return SecuritySnapshot{
		Connections: conns,
		Startup:     startup,
		Hotfixes:    hotfixes,
		CollectedAt: time.Now(),
	}, nil
}

func filterExternalEstablishedConns(in []NetConnInfo) []NetConnInfo {
	out := make([]NetConnInfo, 0, len(in))
	for _, c := range in {
		if !strings.EqualFold(strings.TrimSpace(c.State), "ESTABLISHED") {
			continue
		}
		ra := strings.TrimSpace(strings.ToLower(c.RemoteAddr))
		if ra == "" || ra == "127.0.0.1" || ra == "::1" || ra == "localhost" {
			continue
		}
		out = append(out, c)
	}
	return out
}
