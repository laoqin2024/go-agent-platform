package collector

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"encoding/hex"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
	gnet "github.com/shirou/gopsutil/v4/net"
)

type HostCollector struct {
	rootPath string

	// CPU sample interval used by cpu.PercentWithContext (it internally performs two samples over this duration).
	cpuSampleInterval time.Duration

	// Collect timeout applied to each gopsutil call to avoid indefinite blocking under extreme load.
	collectTimeout time.Duration

	// ping targets for best-effort RTT baseline.
	pingTargets []string
	pingTimeout time.Duration

	// lastDiskIO keeps previous disk.IOCounters snapshot for delta-based metrics.
	ioMu     sync.Mutex
	prevIO   map[string]disk.IOCountersStat
	lastIOAt time.Time

	// Software inventory (low-frequency, hash-differential reporting).
	softwareCollector *SoftwareCollector
	softwareMu        sync.Mutex
	lastSoftwareHash  string
}

type HostCollectorOption func(*HostCollector)

func WithRootPath(p string) HostCollectorOption {
	return func(c *HostCollector) {
		if p != "" {
			c.rootPath = p
		}
	}
}

func WithCPUSampleInterval(d time.Duration) HostCollectorOption {
	return func(c *HostCollector) {
		if d > 0 {
			c.cpuSampleInterval = d
		}
	}
}

func WithCollectTimeout(d time.Duration) HostCollectorOption {
	return func(c *HostCollector) {
		if d > 0 {
			c.collectTimeout = d
		}
	}
}

func WithPingTargets(targets []string) HostCollectorOption {
	return func(c *HostCollector) {
		if len(targets) == 0 {
			return
		}
		c.pingTargets = targets
	}
}

func WithPingTimeout(d time.Duration) HostCollectorOption {
	return func(c *HostCollector) {
		if d > 0 {
			c.pingTimeout = d
		}
	}
}

func NewHostCollector(opts ...HostCollectorOption) *HostCollector {
	root := defaultRootPath()
	h := &HostCollector{
		rootPath:          root,
		cpuSampleInterval: 500 * time.Millisecond,
		collectTimeout:    2 * time.Second,

		pingTargets: defaultPingTargets(),
		pingTimeout: 1 * time.Second,

		softwareCollector: NewSoftwareCollector(),
	}
	for _, opt := range opts {
		opt(h)
	}
	if h.collectTimeout < h.cpuSampleInterval {
		// Ensure the cpu.PercentWithContext call has enough time to perform its internal sampling.
		h.collectTimeout = h.cpuSampleInterval + 250*time.Millisecond
	}
	return h
}

func defaultPingTargets() []string {
	// Simple env-based configuration; format: "8.8.8.8,1.1.1.1"
	raw := strings.TrimSpace(os.Getenv("PING_TARGETS"))
	if raw == "" {
		return []string{"8.8.8.8"}
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
		return []string{"8.8.8.8"}
	}
	return out
}

func defaultRootPath() string {
	if runtime.GOOS == "windows" {
		return `C:\`
	}
	// "root directory" for Unix-like systems.
	return string(filepath.Separator)
}

// Collect returns the host metrics. It uses an internal timeout to avoid indefinite blocking.
func (c *HostCollector) Collect() (HostMetrics, error) {
	return c.CollectWithContext(context.Background())
}

// CollectWithContext collects host metrics using ctx and applies an additional internal timeout per gopsutil call.
func (c *HostCollector) CollectWithContext(ctx context.Context) (HostMetrics, error) {
	if ctx == nil {
		return HostMetrics{}, errors.New("collector: nil context")
	}

	// Apply internal timeout for each call. Since cpu.PercentWithContext performs two samples over the interval,
	// we must ensure the timeout is > cpuSampleInterval.
	timeoutCtx := func() (context.Context, context.CancelFunc) {
		if c.collectTimeout <= 0 {
			return context.WithCancel(ctx)
		}
		return context.WithTimeout(ctx, c.collectTimeout)
	}

	// Best-effort ping RTT in parallel with CPU/mem/disk/net collection.
	// We avoid blocking the entire telemetry collection if ping isn't fast enough.
	pingCh := make(chan uint64, 1)
	go func() {
		pingCh <- c.collectPingLatencyMs(ctx)
	}()

	// CPU: total CPU utilization percent.
	cpuCtx, cpuCancel := timeoutCtx()
	defer cpuCancel()
	cpuPercents, err := cpu.PercentWithContext(cpuCtx, c.cpuSampleInterval, false)
	if err != nil {
		return HostMetrics{}, fmt.Errorf("collector: cpu percent: %w", err)
	}
	var totalCPU float64
	if len(cpuPercents) > 0 {
		totalCPU = cpuPercents[0]
	}

	// Memory.
	memCtx, memCancel := timeoutCtx()
	defer memCancel()
	vm, err := mem.VirtualMemoryWithContext(memCtx)
	if err != nil {
		return HostMetrics{}, fmt.Errorf("collector: virtual memory: %w", err)
	}

	// Disk: root directory (or C:\ on Windows).
	diskCtx, diskCancel := timeoutCtx()
	defer diskCancel()
	usage, err := disk.UsageWithContext(diskCtx, c.rootPath)
	if err != nil {
		return HostMetrics{}, fmt.Errorf("collector: disk usage: %w", err)
	}

	// Disk I/O performance (per-device, delta-based).
	ioStats, err := c.collectDiskIOStats(ctx)
	if err != nil {
		// Best-effort: do not fail host_metrics collection if IO counters are unavailable.
		ioStats = nil
	}

	// Network: cumulative bytes.
	netCtx, netCancel := timeoutCtx()
	defer netCancel()
	totalNet, ifaceMap, err := c.CollectNetworkStats(netCtx)
	if err != nil {
		// Network permissions/capabilities vary across environments; best-effort for network.
		totalNet = NetworkStats{}
		ifaceMap = map[string]NetworkInterfaceStats{}
	}
	ifaces := make([]NetworkInterfaceStats, 0, len(ifaceMap))
	for _, v := range ifaceMap {
		ifaces = append(ifaces, v)
	}

	// GPU telemetry (best-effort; do not block overall collection).
	var gpuStats []GpuStat
	{
		gpuCtx, gpuCancel := timeoutCtx()
		defer gpuCancel()
		if gs, gerr := collectGpuStats(gpuCtx); gerr == nil {
			gpuStats = gs
		}
	}

	// Security: listening ports snapshot (best-effort).
	var secSnap SecuritySnapshot
	{
		secCtx, secCancel := timeoutCtx()
		defer secCancel()
		if lp, lerr := c.collectListeningPorts(secCtx); lerr == nil {
			secSnap.Listening = lp
		}
	}

	// Software inventory: only report when content hash changes (to avoid network congestion).
	var softwareList []SoftwareItem
	if c.softwareCollector != nil {
		swCtx, swCancel := context.WithTimeout(ctx, 60*time.Second)
		infos, swErr := c.softwareCollector.Collect(swCtx)
		swCancel()
		if swErr == nil && len(infos) > 0 {
			items := make([]SoftwareItem, 0, len(infos))
			for _, si := range infos {
				var install time.Time
				if si.InstallDate != nil {
					install = *si.InstallDate
				}
				items = append(items, SoftwareItem{
					Name:        si.Name,
					Version:     si.Version,
					Publisher:   si.Publisher,
					InstallDate: install,
				})
			}
			if c.shouldReportSoftwareList(items) {
				softwareList = items
			}
		}
	}

	hostname, _ := os.Hostname()
	fingerprint := stableFingerprint(hostname)

	// Top-N process insight (best-effort; permission errors are tolerated).
	var procTop []ProcessStat
	var services []ServiceStat
	var pidToSvc map[uint32]string
	var cgToSvc map[string]string
	{
		sctx, scancel := timeoutCtx()
		defer scancel()
		if ss, pidMap, cgMap, serr := collectServiceHealthWithContext(sctx); serr == nil {
			services = ss
			pidToSvc = pidMap
			cgToSvc = cgMap
		}
		pctx, pcancel := timeoutCtx()
		defer pcancel()
		if ps, perr := collectTopProcessStatsWithContext(pctx, 100, pidToSvc, cgToSvc); perr == nil {
			procTop = ps
		}
	}

	var pingLatencyMs uint64
	select {
	case pingLatencyMs = <-pingCh:
	case <-time.After(250 * time.Millisecond):
		// Ping may still be running; keep best-effort 0.
	}

	return HostMetrics{
		Hostname:    hostname,
		Fingerprint: fingerprint,
		CPU: CPUStats{
			Total: totalCPU,
		},
		Memory: MemoryStats{
			Total:       vm.Total,
			Used:        vm.Used,
			UsedPercent: vm.UsedPercent,
		},
		Disk: DiskStats{
			Path:        c.rootPath,
			Total:       usage.Total,
			Used:        usage.Used,
			Free:        usage.Free,
			UsedPercent: usage.UsedPercent,
		},
		DiskIO: ioStats,
		Network: NetworkStats{
			BytesRecv: totalNet.BytesRecv,
			BytesSent: totalNet.BytesSent,
		},
		NetworkInterfaces: ifaces,
		GpuStats:          gpuStats,
		SoftwareList:      softwareList,
		Processes:         procTop,
		Services:          services,
		SecuritySnapshot:  secSnap,
		PingLatencyMs:     pingLatencyMs,
		CollectedAt:       time.Now(),
	}, nil
}

func (c *HostCollector) shouldReportSoftwareList(items []SoftwareItem) bool {
	hash := softwareListHash(items)
	if hash == "" {
		return false
	}
	c.softwareMu.Lock()
	defer c.softwareMu.Unlock()
	if c.lastSoftwareHash == "" || c.lastSoftwareHash != hash {
		c.lastSoftwareHash = hash
		return true
	}
	return false
}

func softwareListHash(items []SoftwareItem) string {
	if len(items) == 0 {
		return ""
	}

	// Stable ordering is required to avoid false positives.
	cp := append([]SoftwareItem(nil), items...)
	sort.Slice(cp, func(i, j int) bool {
		a, b := cp[i], cp[j]
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.Version != b.Version {
			return a.Version < b.Version
		}
		if a.Publisher != b.Publisher {
			return a.Publisher < b.Publisher
		}
		return a.InstallDate.Before(b.InstallDate)
	})

	b, err := json.Marshal(cp)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// collectGpuStats tries common OS-specific ways to read GPU metrics.
// - NVIDIA: use nvidia-smi (name, utilization, memory used/total, temperature)
// - macOS: best-effort model via system_profiler (util not available without privileges)
// - others: return empty
func collectGpuStats(ctx context.Context) ([]GpuStat, error) {
	switch runtime.GOOS {
	case "linux", "windows":
		// Try NVIDIA first; if unavailable on Linux, try ROCm (AMD)
		stats, err := collectGpuNvidiaSMI(ctx)
		if err == nil && len(stats) > 0 {
			return stats, nil
		}
		if runtime.GOOS == "linux" {
			if amd, aerr := collectGpuRocmSMI(ctx); aerr == nil && len(amd) > 0 {
				return amd, nil
			}
		}
		return stats, nil
	case "darwin":
		return collectGpuDarwin(ctx)
	default:
		return nil, nil
	}
}

func collectGpuNvidiaSMI(ctx context.Context) ([]GpuStat, error) {
	// Try PATH first, then common absolute locations (Windows/Linux)
	candidates := []string{
		"nvidia-smi",
		`C:\Program Files\NVIDIA Corporation\NVSMI\nvidia-smi.exe`,
		`C:\Windows\System32\nvidia-smi.exe`,
		"/usr/bin/nvidia-smi",
		"/usr/local/bin/nvidia-smi",
		"/bin/nvidia-smi",
	}
	var out []byte
	var err error
	for _, exe := range candidates {
		cmd := exec.CommandContext(ctx, exe,
			"--query-gpu=name,utilization.gpu,memory.used,memory.total,temperature.gpu",
			"--format=csv,noheader,nounits",
		)
		out, err = cmd.Output()
		if err == nil && len(out) > 0 {
			break
		}
	}
	if err != nil || len(out) == 0 {
		return nil, nil
	}
	lines := strings.Split(string(out), "\n")
	var stats []GpuStat
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) < 5 {
			continue
		}
		model := strings.TrimSpace(parts[0])
		util := parseFloat(strings.TrimSpace(parts[1]))
		memUsed := parseFloat(strings.TrimSpace(parts[2]))
		memTotal := parseFloat(strings.TrimSpace(parts[3]))
		temp := parseFloat(strings.TrimSpace(parts[4]))
		stats = append(stats, GpuStat{
			Model:         model,
			UtilPercent:   util,
			MemoryUsedMB:  memUsed,
			MemoryTotalMB: memTotal,
			TemperatureC:  temp,
		})
	}
	return stats, nil
}

// collectGpuRocmSMI attempts to read AMD GPU stats using rocm-smi on Linux.
// Best-effort: if rocm-smi is not installed or JSON parsing fails, return empty.
func collectGpuRocmSMI(ctx context.Context) ([]GpuStat, error) {
	// Prefer JSON output if available.
	candidates := [][]string{
		{"rocm-smi", "--json"},
		{"/usr/bin/rocm-smi", "--json"},
		{"/opt/rocm/bin/rocm-smi", "--json"},
	}
	type rocmJSON struct {
		GPUS []struct {
			CardSKU      string  `json:"Card SKU"`
			GPUUse       float64 `json:"GPU use (%)"`
			MemUseMB     float64 `json:"GPU memory use (MB)"`
			MemTotalMB   float64 `json:"VRAM Total Memory (B)"`
			TemperatureC float64 `json:"Temperature (Sensor die) (C)"`
		} `json:"card"`
	}
	for _, args := range candidates {
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		out, err := cmd.Output()
		if err != nil || len(out) == 0 {
			continue
		}
		// Try to unmarshal flexible JSON structure
		var (
			js  map[string]any
			res []GpuStat
		)
		if err := json.Unmarshal(out, &js); err != nil {
			continue
		}
		// Heuristic extraction
		for _, v := range js {
			// Expect nested objects per gpu index
			if m, ok := v.(map[string]any); ok {
				for _, mv := range m {
					if gm, ok := mv.(map[string]any); ok {
						model := strField(gm, "Card SKU")
						util := numField(gm, "GPU use (%)")
						memUsed := numField(gm, "GPU memory use (MB)")
						memTotal := numField(gm, "VRAM Total Memory (B)") / (1024 * 1024)
						temp := numField(gm, "Temperature (Sensor die) (C)")
						// Some versions use other keys
						if model == "" {
							model = strField(gm, "Card model")
						}
						if memTotal <= 0 {
							memTotal = numField(gm, "VRAM Total (MB)")
						}
						res = append(res, GpuStat{
							Model:         model,
							UtilPercent:   util,
							MemoryUsedMB:  memUsed,
							MemoryTotalMB: memTotal,
							TemperatureC:  temp,
						})
					}
				}
			}
		}
		if len(res) > 0 {
			return res, nil
		}
	}
	return nil, nil
}

func strField(m map[string]any, k string) string {
	if v, ok := m[k]; ok {
		if s, ok := v.(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func numField(m map[string]any, k string) float64 {
	if v, ok := m[k]; ok {
		switch t := v.(type) {
		case float64:
			return t
		case string:
			f, _ := strconv.ParseFloat(strings.TrimSpace(t), 64)
			return f
		}
	}
	return 0
}

func collectGpuDarwin(ctx context.Context) ([]GpuStat, error) {
	cmd := exec.CommandContext(ctx, "system_profiler", "SPDisplaysDataType", "-json")
	out, err := cmd.Output()
	if err != nil {
		return nil, nil
	}
	// naive parse for model
	model := "Apple GPU"
	s := string(out)
	if i := strings.Index(s, "spdisplays_model"); i >= 0 {
		sub := s[i:]
		if j := strings.Index(sub, ":"); j >= 0 {
			k := strings.Index(sub[j+1:], "\"")
			if k >= 0 {
				rest := sub[j+1+k+1:]
				if mEnd := strings.Index(rest, "\""); mEnd >= 0 {
					mv := rest[:mEnd]
					mv = strings.TrimSpace(mv)
					if mv != "" {
						model = mv
					}
				}
			}
		}
	}
	return []GpuStat{{
		Model:         model,
		UtilPercent:   0,
		MemoryUsedMB:  0,
		MemoryTotalMB: 0,
		TemperatureC:  0,
	}}, nil
}

func parseFloat(s string) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return f
}

// collectDiskIOStats uses gopsutil/disk.IOCounters to derive per-device
// read/write throughput and IOPS over the last collection interval.
func (c *HostCollector) collectDiskIOStats(ctx context.Context) ([]DiskIOStats, error) {
	ioCtx, cancel := context.WithTimeout(ctx, c.collectTimeout)
	defer cancel()

	current, err := disk.IOCountersWithContext(ioCtx, "")
	if err != nil {
		return nil, err
	}
	now := time.Now()

	c.ioMu.Lock()
	defer c.ioMu.Unlock()

	if c.prevIO == nil {
		c.prevIO = make(map[string]disk.IOCountersStat, len(current))
	}

	var dt float64
	if !c.lastIOAt.IsZero() {
		dt = now.Sub(c.lastIOAt).Seconds()
	}
	c.lastIOAt = now

	// First run: only prime the cache, return empty metrics.
	if dt <= 0 {
		for name, s := range current {
			c.prevIO[name] = s
		}
		return nil, nil
	}

	out := make([]DiskIOStats, 0, len(current))

	for name, cur := range current {
		prev, ok := c.prevIO[name]
		c.prevIO[name] = cur

		// If we have no previous sample, skip this round for this device.
		if !ok {
			continue
		}

		// Handle counter reset/wrap: if any key metric decreased, re-baseline.
		if cur.ReadBytes < prev.ReadBytes ||
			cur.WriteBytes < prev.WriteBytes ||
			cur.ReadCount < prev.ReadCount ||
			cur.WriteCount < prev.WriteCount {
			continue
		}

		dReadBytes := float64(cur.ReadBytes - prev.ReadBytes)
		dWriteBytes := float64(cur.WriteBytes - prev.WriteBytes)
		dReadCount := float64(cur.ReadCount - prev.ReadCount)
		dWriteCount := float64(cur.WriteCount - prev.WriteCount)

		if dReadBytes < 0 || dWriteBytes < 0 || dReadCount < 0 || dWriteCount < 0 {
			continue
		}

		readKBps := (dReadBytes / 1024.0) / dt
		writeKBps := (dWriteBytes / 1024.0) / dt
		readIOPS := dReadCount / dt
		writeIOPS := dWriteCount / dt

		// Utilization is tricky without explicit queue length; we approximate
		// using weighted service times (in milliseconds) if available.
		var utilPercent float64
		// gopsutil exposes WeightedIO and IoTime as milliseconds.
		if cur.IoTime > prev.IoTime {
			dIoMs := float64(cur.IoTime - prev.IoTime)
			utilPercent = (dIoMs / (dt * 1000.0)) * 100.0
			if utilPercent < 0 {
				utilPercent = 0
			}
			if utilPercent > 100 {
				utilPercent = 100
			}
		}

		out = append(out, DiskIOStats{
			DeviceName:  name,
			ReadKBps:    readKBps,
			WriteKBps:   writeKBps,
			ReadIOPS:    readIOPS,
			WriteIOPS:   writeIOPS,
			UtilPercent: utilPercent,
		})
	}

	return out, nil
}

// CollectNetworkStats collects best-effort per-NIC cumulative counters.
// It returns both overall totals (for backward-compat UI) and a per-NIC map.
func (c *HostCollector) CollectNetworkStats(ctx context.Context) (NetworkStats, map[string]NetworkInterfaceStats, error) {
	// Determine "active NICs" set (UP + not loopback) for filtering.
	active := map[string]bool{}
	ifaces, err := net.Interfaces()
	if err == nil {
		for _, i := range ifaces {
			if i.Flags&net.FlagUp == 0 {
				continue
			}
			if i.Flags&net.FlagLoopback != 0 {
				continue
			}
			active[i.Name] = true
		}
	}

	// pernic=true returns interface-level counters.
	stats, err := gnet.IOCountersWithContext(ctx, true)
	if err != nil {
		return NetworkStats{}, nil, err
	}

	per := make(map[string]NetworkInterfaceStats, len(stats))
	var totalRecv uint64
	var totalSent uint64

	for _, s := range stats {
		name := strings.TrimSpace(s.Name)
		if name == "" {
			continue
		}

		// Do not strictly filter by "active" set; some platforms report counters
		// for interfaces that net.Interfaces() doesn't mark as UP, leading to 0 rates.
		// We keep all entries gopsutil reports and rely on UI to display meaningful ones.

		per[name] = NetworkInterfaceStats{
			Name:        name,
			BytesRecv:   s.BytesRecv,
			BytesSent:   s.BytesSent,
			PacketsRecv: s.PacketsRecv,
			PacketsSent: s.PacketsSent,
			ErrorsIn:    s.Errin,
			ErrorsOut:   s.Errout,
			DropIn:      s.Dropin,
			DropOut:     s.Dropout,
		}
		totalRecv += s.BytesRecv
		totalSent += s.BytesSent
	}

	// Ensure each active NIC appears even if some counters are missing/unsupported.
	for name := range active {
		if _, ok := per[name]; !ok {
			per[name] = NetworkInterfaceStats{Name: name}
		}
	}

	return NetworkStats{
		BytesRecv: totalRecv,
		BytesSent: totalSent,
	}, per, nil
}

func (c *HostCollector) collectPingLatencyMs(ctx context.Context) uint64 {
	if len(c.pingTargets) == 0 {
		return 0
	}
	if c.pingTimeout <= 0 {
		c.pingTimeout = 1 * time.Second
	}

	targets := c.pingTargets

	var sum float64
	var count int
	var tcpSum float64
	var tcpCount int

	for _, target := range targets {
		target = strings.TrimSpace(target)
		if target == "" {
			continue
		}

		pctx, cancel := context.WithTimeout(ctx, c.pingTimeout)
		latMs, ok := pingOnceMs(pctx, target)
		if ok && latMs > 0 {
			sum += latMs
			count++
		} else {
			// Fallback: TCP connect RTT (port 80 by default).
			if tcpMs, tcpOk := tcpOnceMs(pctx, target); tcpOk && tcpMs > 0 {
				tcpSum += tcpMs
				tcpCount++
			}
		}
		cancel()
	}

	if count == 0 {
		if tcpCount == 0 {
			return 0
		}
		avg := tcpSum / float64(tcpCount)
		return uint64(avg + 0.5)
	}

	avg := sum / float64(count)
	return uint64(avg + 0.5)
}

func tcpOnceMs(ctx context.Context, target string) (float64, bool) {
	addr := target
	if !strings.Contains(addr, ":") {
		addr = net.JoinHostPort(target, "80")
	}

	dialer := net.Dialer{}
	start := time.Now()
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return 0, false
	}
	_ = conn.Close()

	ms := float64(time.Since(start).Milliseconds())
	if ms <= 0 {
		ms = 0.0
	}
	return ms, true
}

func pingOnceMs(ctx context.Context, target string) (float64, bool) {
	// Use system ping command for ICMP RTT.
	// If ping isn't available or permission is insufficient, we treat it as "no data".
	var cmd *exec.Cmd
	timeoutMs := 1000
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining > 0 {
			timeoutMs = int(remaining / time.Millisecond)
		}
	}
	if timeoutMs < 100 {
		timeoutMs = 100
	}

	switch runtime.GOOS {
	case "windows":
		// -n count, -w timeout in ms
		cmd = exec.CommandContext(ctx, "ping", "-n", "1", "-w", strconv.Itoa(timeoutMs), target)
	case "darwin":
		// -c count, -W timeout in ms
		cmd = exec.CommandContext(ctx, "ping", "-c", "1", "-W", strconv.Itoa(timeoutMs), target)
	default:
		// Linux/others: -c count, -W timeout (ms on iputils ping)
		cmd = exec.CommandContext(ctx, "ping", "-c", "1", "-W", strconv.Itoa(timeoutMs), target)
	}

	out, err := cmd.CombinedOutput()
	if err != nil && len(out) == 0 {
		return 0, false
	}

	// Typical: time=12.3 ms / time<1ms / time=0.045 ms
	re := regexp.MustCompile(`time[=<]([0-9]+(?:\.[0-9]+)?)\s*ms`)
	m := re.FindStringSubmatch(string(out))
	valStr := ""
	if len(m) >= 2 {
		valStr = m[1]
	} else {
		// Some ping outputs use "time=... ms" with different spacing; fallback regex.
		re2 := regexp.MustCompile(`time[=<]([0-9]+(?:\.[0-9]+)?)`)
		m2 := re2.FindStringSubmatch(string(out))
		if len(m2) < 2 {
			return 0, false
		}
		valStr = m2[1]
	}

	v, err := strconv.ParseFloat(valStr, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}
