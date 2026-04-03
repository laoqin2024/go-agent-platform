//go:build darwin

package collector

import (
	"context"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// collectNetworkConnections on macOS uses lsof if available (best-effort).
// It falls back to an empty list if lsof is not present or times out.
func collectNetworkConnections(ctx context.Context) ([]NetConnInfo, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// -nP: no DNS/port names; -i: network; -F p c n P T: machine-readable fields
	out, err := exec.CommandContext(cmdCtx, "lsof", "-nP", "-i", "-F", "pcnPT").CombinedOutput()
	if err != nil || len(out) == 0 {
		return []NetConnInfo{}, nil
	}
	lines := strings.Split(string(out), "\n")
	var (
		curPID   uint32
		curProc  string
		curProto string
	)
	var conns []NetConnInfo
	flush := func(nv string, st string) {
		if nv == "" || curProto == "" || curPID == 0 {
			return
		}
		la, lp, ra, rp := parseNameAddrPort(nv)
		if la == "" && lp == 0 {
			return
		}
		conns = append(conns, NetConnInfo{
			Protocol:    strings.ToUpper(curProto),
			LocalAddr:   la,
			LocalPort:   lp,
			RemoteAddr:  ra,
			RemotePort:  rp,
			State:       st,
			PID:         curPID,
			ProcessName: curProc,
		})
	}
	var lastName string
	var lastState string
	for _, ln := range lines {
		if ln == "" {
			continue
		}
		switch ln[0] {
		case 'p':
			// new process
			if lastName != "" {
				flush(lastName, lastState)
				lastName, lastState = "", ""
			}
			curPID = parseUint32(ln[1:])
			curProc = ""
			curProto = ""
		case 'c':
			curProc = ln[1:]
		case 'P':
			curProto = ln[1:] // tcp / udp
		case 'T':
			kv := ln[1:]
			// Expect TST=LISTEN/ESTABLISHED...
			if strings.HasPrefix(kv, "ST=") {
				lastState = strings.ToUpper(strings.TrimSpace(strings.TrimPrefix(kv, "ST=")))
			}
		case 'n':
			// If a previous n exists, flush it first
			if lastName != "" {
				flush(lastName, lastState)
				lastState = ""
			}
			lastName = ln[1:]
		default:
			// ignore other fields
		}
	}
	if lastName != "" {
		flush(lastName, lastState)
	}
	return dedupNetConns(conns), nil
}

func parseUint32(s string) uint32 {
	u, _ := strconv.ParseUint(strings.TrimSpace(s), 10, 32)
	return uint32(u)
}

// parseNameAddrPort parses lsof "n" value:
// Examples:
//  - 127.0.0.1:8080
//  - 127.0.0.1:8080->127.0.0.1:52344
//  - *:68
//  - [::1]:631
func parseNameAddrPort(n string) (la string, lp uint16, ra string, rp uint16) {
	n = strings.TrimSpace(n)
	if n == "" {
		return
	}
	parts := strings.Split(n, "->")
	lhs := parts[0]
	rhs := ""
	if len(parts) > 1 {
		rhs = parts[1]
	}
	la, lp = splitHostPort(lhs)
	if rhs != "" {
		ra, rp = splitHostPort(rhs)
	}
	return
}

func splitHostPort(s string) (host string, port uint16) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", 0
	}
	// IPv6 in [::1]:631
	if strings.HasPrefix(s, "[") {
		if i := strings.LastIndex(s, "]:"); i > 0 {
			host = strings.TrimPrefix(s[:i+1], "[")
			host = strings.TrimSuffix(host, "]")
			p := s[i+2:]
			if v, _ := strconv.ParseUint(p, 10, 16); v > 0 {
				port = uint16(v)
			}
			return
		}
	}
	// Wildcard "*:port"
	if strings.HasPrefix(s, "*:") {
		p := strings.TrimPrefix(s, "*:")
		if v, _ := strconv.ParseUint(p, 10, 16); v > 0 {
			return "", uint16(v)
		}
		return "", 0
	}
	// IPv4 or hostname host:port
	if i := strings.LastIndex(s, ":"); i > 0 {
		h := s[:i]
		p := s[i+1:]
		if net.ParseIP(h) != nil || h != "" {
			host = h
		}
		if v, _ := strconv.ParseUint(p, 10, 16); v > 0 {
			port = uint16(v)
		}
	}
	return
}

func dedupNetConns(in []NetConnInfo) []NetConnInfo {
	seen := map[string]struct{}{}
	out := make([]NetConnInfo, 0, len(in))
	for _, c := range in {
		key := strings.Join([]string{
			c.Protocol, c.LocalAddr, strconv.Itoa(int(c.LocalPort)),
			c.RemoteAddr, strconv.Itoa(int(c.RemotePort)),
			c.State, strconv.Itoa(int(c.PID)), c.ProcessName,
		}, "|")
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, c)
	}
	return out
}

// For now we don't collect startup items and hotfixes on macOS.
func collectStartupItems(ctx context.Context) ([]StartupItem, error) { return nil, nil }
func collectHotfixes(ctx context.Context) ([]HotfixInfo, error) { return nil, nil }

