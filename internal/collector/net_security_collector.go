package collector

import (
	"context"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	gnet "github.com/shirou/gopsutil/v4/net"
	gproc "github.com/shirou/gopsutil/v4/process"
)

func normalizeListenIP(ip string) string {
	s := strings.TrimSpace(strings.ToLower(ip))
	if s == "" || s == "*" {
		return "0.0.0.0"
	}
	if s == "localhost" {
		return "127.0.0.1"
	}
	// Strip IPv6 brackets if any
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") && len(s) >= 2 {
		s = strings.TrimPrefix(strings.TrimSuffix(s, "]"), "[")
	}
	// Normalize common unspecified IPv6 variants
	if s == "::" || s == "::0" {
		return "::"
	}
	return s
}

const maxListeningPortsPerSnapshot = 200

// collectListeningPorts enumerates TCP/UDP sockets and extracts LISTENing endpoints.
// - TCP: Status == LISTEN
// - UDP: include if local address exists and port > 0 (no LISTEN state for UDP)
// Scope rule:
// - 0.0.0.0 / ::  => public
// - 127.0.0.1 / ::1 => local
// - others => other
func (c *HostCollector) collectListeningPorts(ctx context.Context) ([]ListeningPort, error) {
	// Apply an internal timeout smaller than collectTimeout to avoid blocking host collection.
	timeout := c.collectTimeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	lctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Prefer explicit families so protocol classification is accurate.
	type connWithProto struct {
		proto string
		conn  gnet.ConnectionStat
	}
	var all []connWithProto
	var errs []error
	for _, fam := range []string{"tcp4", "tcp6", "udp4", "udp6"} {
		cs, err := gnet.ConnectionsWithContext(lctx, fam)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		proto := "udp"
		if strings.HasPrefix(fam, "tcp") {
			proto = "tcp"
		}
		for _, conn := range cs {
			all = append(all, connWithProto{proto: proto, conn: conn})
		}
	}
	// As a last resort, try "all" if nothing succeeded.
	if len(all) == 0 {
		if cs, err := gnet.ConnectionsWithContext(lctx, "all"); err == nil {
			for _, conn := range cs {
				// Fallback heuristic: LISTEN => tcp, else udp.
				proto := "udp"
				if strings.EqualFold(strings.TrimSpace(conn.Status), "LISTEN") {
					proto = "tcp"
				}
				all = append(all, connWithProto{proto: proto, conn: conn})
			}
		} else if len(errs) > 0 {
			// return the last error
			return nil, errs[len(errs)-1]
		} else {
			return nil, err
		}
	}

	out := make([]ListeningPort, 0, len(all))
	seen := make(map[string]struct{}, len(all))
	for _, item := range all {
		conn := item.conn
		ip := normalizeListenIP(conn.Laddr.IP)
		port := conn.Laddr.Port
		if port == 0 {
			continue
		}

		status := strings.ToUpper(strings.TrimSpace(conn.Status))
		isTCP := item.proto == "tcp"
		// Include only TCP LISTEN; UDP does not expose LISTEN state.
		if isTCP && status != "LISTEN" {
			continue
		}
		proto := item.proto

		scope := "other"
		switch ip {
		case "0.0.0.0", "::":
			scope = "public"
		case "127.0.0.1", "::1":
			scope = "local"
		default:
			// leave as "other"
		}
		// High-risk ports exposed on public address.
		isHighRisk := false
		if scope == "public" {
			switch uint32(port) {
			case 22, 3389, 445, 3306, 6379:
				isHighRisk = true
			}
		}
		// Debug sampling: log discovered listening endpoints (best-effort).
		// This helps validate tools like `nc -lk 0.0.0.0 6379` are detected by the agent.
		log.Printf("[Security] Found listening port: %s:%d on %s (scope=%s)", proto, port, ip, scope)
		key := proto + "|" + ip + "|" + strings.TrimSpace(strings.ToLower(scope)) + "|" + strconv.FormatUint(uint64(port), 10)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}

		pid := uint32(conn.Pid)
		procName := ""
		// Best-effort process association (may fail on macOS/Linux without root).
		if pid > 0 {
			if pr, err := gproc.NewProcessWithContext(lctx, int32(pid)); err == nil && pr != nil {
				if name, err := pr.NameWithContext(lctx); err == nil {
					procName = strings.TrimSpace(name)
				} else {
					// If we can see PID but cannot resolve name due to permissions, keep record with explicit marker.
					if strings.Contains(strings.ToLower(err.Error()), "permission") || strings.Contains(strings.ToLower(err.Error()), "access is denied") {
						procName = "Permission Denied (Unknown)"
					}
				}
			}
		}

		out = append(out, ListeningPort{
			Protocol:    proto,
			Address:     ip,
			Port:        uint32(port),
			PID:         pid,
			ProcessName: procName,
			Scope:       scope,
			IsHighRisk:  isHighRisk,
		})
	}
	// Sort for stable output: protocol asc, port asc, address asc
	sort.Slice(out, func(i, j int) bool {
		if out[i].Protocol != out[j].Protocol {
			return out[i].Protocol < out[j].Protocol
		}
		if out[i].Port != out[j].Port {
			return out[i].Port < out[j].Port
		}
		return out[i].Address < out[j].Address
	})

	// Payload safety: cap number of listening ports to avoid exploding JSON under attack/misreporting.
	if len(out) > maxListeningPortsPerSnapshot {
		log.Printf("[Security][WARN] too many listening ports (%d), truncating to %d", len(out), maxListeningPortsPerSnapshot)
		out = out[:maxListeningPortsPerSnapshot]
	}
	return out, nil
}

