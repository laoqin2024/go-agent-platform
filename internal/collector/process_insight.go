package collector

import (
	"context"
	"log"
	"os/exec"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	gnet "github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"
)

// collectTopProcessStatsWithContext collects an enriched Top-N process snapshot.
// Defensive design: any per-process failure will be logged (debug) and skipped.
func collectTopProcessStatsWithContext(ctx context.Context, topN int, includePIDs map[uint32]string, includeCGroups map[string]string) ([]ProcessStat, error) {
	if ctx == nil {
		return nil, nil
	}
	if topN <= 0 {
		topN = 10
	}

	ps, err := process.ProcessesWithContext(ctx)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	out := make([]ProcessStat, 0, len(ps))

	for _, p := range ps {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		pid := uint32(p.Pid)
		st := ProcessStat{PID: pid, CollectedAt: now}
		if includePIDs != nil {
			if svc, ok := includePIDs[pid]; ok && svc != "" {
				st.ServiceName = svc
			}
		}
		// Linux cgroup mapping is more stable than PID during restarts.
		if runtime.GOOS == "linux" {
			if cg := readProcessCGroup(pid); cg != "" {
				st.CGroup = cg
				if st.ServiceName == "" && includeCGroups != nil {
					if svc, ok := includeCGroups[cg]; ok && svc != "" {
						st.ServiceName = svc
					}
				}
			}
		}

		if name, e := p.NameWithContext(ctx); e == nil {
			st.Name = sanitizeUTF8(name)
		}
		if exe, e := p.ExeWithContext(ctx); e == nil {
			st.ExecPath = sanitizeUTF8(exe)
		}
		if u, e := p.UsernameWithContext(ctx); e == nil {
			st.Username = sanitizeUTF8(u)
		} else if runtime.GOOS == "darwin" {
			log.Printf("[process][debug][darwin] pid=%d username: %v", pid, e)
		}

		// CPU percent (best-effort). On some platforms this may be 0 without prior sampling.
		if cpuPct, e := p.CPUPercentWithContext(ctx); e == nil {
			st.CPUPercent = cpuPct
		}

		// Memory (RSS) best-effort.
		if mi, e := p.MemoryInfoWithContext(ctx); e == nil && mi != nil {
			st.MemoryBytes = mi.RSS
			st.MemoryMB = float64(mi.RSS) / (1024 * 1024)
		}

		// CreateTime is ms since epoch.
		if ctMs, e := p.CreateTimeWithContext(ctx); e == nil && ctMs > 0 {
			st.UptimeSec = now.Unix() - (ctMs / 1000)
			if st.UptimeSec < 0 {
				st.UptimeSec = 0
			}
		}

		// Status best-effort. gopsutil may return []string on some platforms.
		if ss, e := p.StatusWithContext(ctx); e == nil {
			switch v := any(ss).(type) {
			case string:
				st.Status = sanitizeUTF8(v)
			case []string:
				if len(v) > 0 {
					st.Status = sanitizeUTF8(v[0])
				}
			}
		}

		// LISTEN ports (best-effort; may require elevated permissions).
		ports, perr := collectListenPorts(ctx, p)
		if perr == nil && len(ports) > 0 {
			st.ListenPorts = ports
		} else if perr != nil {
			if runtime.GOOS == "darwin" {
				log.Printf("[process][debug][darwin] pid=%d gopsutil connections: %v", pid, perr)
			} else {
				log.Printf("[process][debug] pid=%d connections: %v", pid, perr)
			}
		}

		out = append(out, st)
	}

	// Sort by CPU desc then Memory desc.
	sort.Slice(out, func(i, j int) bool {
		// Prioritize processes that expose listening ports to avoid dropping short-lived servers (e.g., nc)
		li := len(out[i].ListenPorts) > 0
		lj := len(out[j].ListenPorts) > 0
		if li != lj {
			return li // with ports first
		}
		if out[i].CPUPercent != out[j].CPUPercent {
			return out[i].CPUPercent > out[j].CPUPercent
		}
		return out[i].MemoryMB > out[j].MemoryMB
	})

	// macOS fallback: if gopsutil fails to expose listen ports due to permissions,
	// use lsof best-effort for top active processes.
	if runtime.GOOS == "darwin" && len(out) > 0 {
		needFallback := 0
		topK := minInt(10, len(out))
		pidSet := make(map[uint32]struct{}, topK)
		for i := 0; i < topK; i++ {
			if len(out[i].ListenPorts) == 0 {
				needFallback++
			}
			pidSet[out[i].PID] = struct{}{}
		}
		if needFallback > 0 {
			if fb, ferr := collectListenPortsFallbackDarwin(ctx, pidSet); ferr != nil {
				log.Printf("[process][debug][darwin] lsof fallback failed: %v", ferr)
			} else {
				for i := 0; i < topK; i++ {
					if len(out[i].ListenPorts) == 0 {
						if ports := fb[out[i].PID]; len(ports) > 0 {
							out[i].ListenPorts = ports
						}
					}
				}
			}
		}
	}

	// Always include whitelisted service main pids if present, even if not in Top-N.
	// NOTE: len(nilMap) is defined as 0, so nil checks are unnecessary (staticcheck S1009).
	if len(includePIDs) == 0 && len(includeCGroups) == 0 {
		if len(out) <= topN {
			return out, nil
		}
		// Keep topN but ensure any process with listening ports is retained
		cut := out[:topN]
		// Try to replace entries without ports with later entries that have ports
		freeSlots := make([]int, 0, topN)
		for i := 0; i < len(cut); i++ {
			if len(cut[i].ListenPorts) == 0 {
				freeSlots = append(freeSlots, i)
			}
		}
		if len(freeSlots) > 0 {
			cursor := 0
			for i := topN; i < len(out) && cursor < len(freeSlots); i++ {
				if len(out[i].ListenPorts) > 0 {
					cut[freeSlots[cursor]] = out[i]
					cursor++
				}
			}
			// Re-sort the resulting slice to keep deterministic order: ports first, then CPU desc, then Memory desc.
			sort.Slice(cut, func(i, j int) bool {
				li := len(cut[i].ListenPorts) > 0
				lj := len(cut[j].ListenPorts) > 0
				if li != lj {
					return li
				}
				if cut[i].CPUPercent != cut[j].CPUPercent {
					return cut[i].CPUPercent > cut[j].CPUPercent
				}
				return cut[i].MemoryMB > cut[j].MemoryMB
			})
		}
		return cut, nil
	}

	extra := len(includePIDs) + len(includeCGroups)
	keep := make([]ProcessStat, 0, topN+extra)
	included := make(map[uint32]struct{}, topN+extra)
	for i := 0; i < len(out) && i < topN; i++ {
		keep = append(keep, out[i])
		included[out[i].PID] = struct{}{}
	}
	for _, st := range out {
		if _, ok := included[st.PID]; ok {
			continue
		}
		if includePIDs != nil {
			if _, ok := includePIDs[st.PID]; ok {
				keep = append(keep, st)
				included[st.PID] = struct{}{}
				continue
			}
		}
		// If we have cgroup mapping, also include processes that are in mapped cgroups.
		if includeCGroups != nil && st.CGroup != "" {
			if _, ok := includeCGroups[st.CGroup]; ok {
				keep = append(keep, st)
				included[st.PID] = struct{}{}
				continue
			}
		}
	}
	// Stable sort: keep Top-N priority first (already), then by CPU desc.
	sort.SliceStable(keep, func(i, j int) bool {
		if keep[i].CPUPercent != keep[j].CPUPercent {
			return keep[i].CPUPercent > keep[j].CPUPercent
		}
		return keep[i].MemoryMB > keep[j].MemoryMB
	})
	return keep, nil
}

func sanitizeUTF8(s string) string {
	// Replace invalid byte sequences to keep JSON payloads always valid UTF-8.
	return strings.ToValidUTF8(s, "")
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func readProcessCGroup(pid uint32) string {
	// /proc/<pid>/cgroup lines: "<hier>:<controllers>:<path>"
	// We prefer the unified v2 path if present, else first non-empty path.
	b, err := os.ReadFile("/proc/" + strconvU32(pid) + "/cgroup")
	if err != nil || len(b) == 0 {
		return ""
	}
	lines := strings.Split(string(b), "\n")
	best := ""
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		path := strings.TrimSpace(parts[2])
		if path == "" {
			continue
		}
		// cgroup v2 usually has empty controller field.
		if parts[1] == "" {
			return path
		}
		if best == "" {
			best = path
		}
	}
	return best
}

func strconvU32(v uint32) string {
	// tiny helper to avoid pulling strconv in hot path imports; uses base10 conversion.
	// (strconv would be fine too; this just keeps it tight.)
	if v == 0 {
		return "0"
	}
	var buf [10]byte
	i := len(buf)
	x := v
	for x > 0 {
		i--
		buf[i] = byte('0' + x%10)
		x /= 10
	}
	return string(buf[i:])
}

func collectListenPorts(ctx context.Context, p *process.Process) ([]int, error) {
	// Try common signatures across gopsutil versions.
	var conns []gnet.ConnectionStat
	var err error

	// v4 supports ConnectionsWithContext(ctx) / ConnectionsWithContext(ctx, kind)
	type connCtxNoKind interface {
		ConnectionsWithContext(context.Context) ([]gnet.ConnectionStat, error)
	}
	if c1, ok := any(p).(connCtxNoKind); ok {
		conns, err = c1.ConnectionsWithContext(ctx)
	} else {
		type connCtxWithKind interface {
			ConnectionsWithContext(context.Context, string) ([]gnet.ConnectionStat, error)
		}
		if c2, ok := any(p).(connCtxWithKind); ok {
			conns, err = c2.ConnectionsWithContext(ctx, "inet")
		} else {
			return nil, nil
		}
	}
	if err != nil {
		return nil, err
	}

	set := make(map[int]struct{}, 8)
	for _, c := range conns {
		if strings.EqualFold(c.Status, "LISTEN") || strings.EqualFold(c.Status, "LISTENING") {
			port := int(c.Laddr.Port)
			if port > 0 && port <= 65535 {
				set[port] = struct{}{}
			}
		}
	}
	if len(set) == 0 {
		return nil, nil
	}
	ports := make([]int, 0, len(set))
	for p := range set {
		ports = append(ports, p)
	}
	sort.Ints(ports)
	return ports, nil
}

func collectListenPortsFallbackDarwin(ctx context.Context, targetPIDs map[uint32]struct{}) (map[uint32][]int, error) {
	if len(targetPIDs) == 0 {
		return map[uint32][]int{}, nil
	}
	cmdCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	// Resolve absolute lsof path on macOS to avoid $PATH issues in privileged contexts.
	lsofBin := "/usr/sbin/lsof"
	if _, err := os.Stat(lsofBin); err != nil {
		// Fallback to PATH if absolute path not available
		lsofBin = "lsof"
	}
	// Use machine-friendly fields: p=pid, n=name(addr:port)
	out, err := exec.CommandContext(cmdCtx, lsofBin, "-iTCP", "-sTCP:LISTEN", "-P", "-n", "-F", "pn").CombinedOutput()
	if err != nil {
		return nil, err
	}
	res := make(map[uint32][]int, len(targetPIDs))
	curPID := uint32(0)
	portSet := make(map[uint32]map[int]struct{}, len(targetPIDs))
	for _, ln := range strings.Split(string(out), "\n") {
		if ln == "" {
			continue
		}
		switch ln[0] {
		case 'p':
			u, _ := strconv.ParseUint(strings.TrimSpace(ln[1:]), 10, 32)
			curPID = uint32(u)
		case 'n':
			if curPID == 0 {
				continue
			}
			if _, ok := targetPIDs[curPID]; !ok {
				continue
			}
			p := parsePortFromLsofName(ln[1:])
			if p <= 0 || p > 65535 {
				continue
			}
			if _, ok := portSet[curPID]; !ok {
				portSet[curPID] = make(map[int]struct{}, 4)
			}
			portSet[curPID][p] = struct{}{}
		}
	}
	for pid, set := range portSet {
		ports := make([]int, 0, len(set))
		for p := range set {
			ports = append(ports, p)
		}
		sort.Ints(ports)
		if len(ports) > 0 {
			res[pid] = ports
		}
	}
	return res, nil
}

func parsePortFromLsofName(v string) int {
	s := strings.TrimSpace(v)
	if s == "" {
		return 0
	}
	// lsof may emit "127.0.0.1:8080" or "*:631" or "[::1]:631"
	if i := strings.LastIndex(s, ":"); i >= 0 && i+1 < len(s) {
		p, _ := strconv.Atoi(strings.TrimSpace(s[i+1:]))
		return p
	}
	return 0
}
