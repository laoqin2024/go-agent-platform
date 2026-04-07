package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/qinyilin/go-agent/internal/collector"
	"github.com/qinyilin/go-agent/internal/buffer"
)

// SimCollector simulates a device telemetry collection engine.
type SimCollector struct {
	logger       *slog.Logger
	hostEvery    time.Duration
	host         *collector.HostCollector

	// agentVersion is a build-time version string injected by cmd/agent (e.g. v1.0.1).
	agentVersion string

	hardware *collector.HardwareCollector

	processSnapshot *collector.ProcessSnapshotCollector
	security        *collector.SecurityCollector
	netConnections  *collector.NetworkConnectionsCollector

	software            *collector.SoftwareCollector
	softwareScanEvery  time.Duration
	softwareScanMu      sync.Mutex
	softwareScanRunning bool

	hardwareScanEvery   time.Duration
	hardwareScanMu      sync.Mutex
	hardwareScanRunning bool
	hardwareRetryAfter  time.Duration

	processSnapshotMu      sync.Mutex
	processSnapshotRunning bool
	lastProcessHashMu      sync.Mutex
	lastProcessHash        string
	// Data-diff for software inventory
	lastSoftwareHashMu sync.Mutex
	lastSoftwareHash   string

	serviceSnapshotMu      sync.Mutex
	serviceSnapshotRunning bool

	netConnMu      sync.Mutex
	netConnRunning bool

	securityScanEvery   time.Duration
	securityScanMu      sync.Mutex
	securityScanRunning bool

	buffer *buffer.DataBuffer
}

type SimCollectorOption func(*SimCollector)

func WithAgentVersion(v string) SimCollectorOption {
	return func(c *SimCollector) {
		c.agentVersion = v
	}
}

func WithSoftwareScanEvery(d time.Duration) SimCollectorOption {
	return func(c *SimCollector) {
		if d > 0 {
			c.softwareScanEvery = d
		}
	}
}

func WithHardwareScanEvery(d time.Duration) SimCollectorOption {
	return func(c *SimCollector) {
		if d > 0 {
			c.hardwareScanEvery = d
		}
	}
}

func WithSecurityScanEvery(d time.Duration) SimCollectorOption {
	return func(c *SimCollector) {
		if d > 0 {
			c.securityScanEvery = d
		}
	}
}

func WithHardwareRetryAfter(d time.Duration) SimCollectorOption {
	return func(c *SimCollector) {
		if d > 0 {
			c.hardwareRetryAfter = d
		}
	}
}

func WithHostEvery(d time.Duration) SimCollectorOption {
	return func(c *SimCollector) {
		if d > 0 {
			c.hostEvery = d
		}
	}
}

func NewSimCollector(logger *slog.Logger, collectEvery time.Duration, buf *buffer.DataBuffer, opts ...SimCollectorOption) *SimCollector {
	// Backward-compat: keep the old param but default to T1=15s if not set.
	hostEvery := collectEvery
	if hostEvery <= 0 {
		hostEvery = 15 * time.Second
	}
	c := &SimCollector{
		logger:       logger,
		hostEvery:    hostEvery,
		host:         collector.NewHostCollector(collector.WithCollectTimeout(hostEvery - 50*time.Millisecond)),
		agentVersion: "",
		hardware:     collector.NewHardwareCollector(collector.WithHardwareLogger(logger)),
		processSnapshot: collector.NewProcessSnapshotCollector(),
		security:        collector.NewSecurityCollector(),
		netConnections:  collector.NewNetworkConnectionsCollector(),
		software:     collector.NewSoftwareCollector(),
		softwareScanEvery: 12 * time.Hour,
		buffer:       buf,

		hardwareScanEvery: 12 * time.Hour,
		securityScanEvery: 12 * time.Hour,
		hardwareRetryAfter: 2 * time.Minute,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	return c
}

func (c *SimCollector) Run(ctx context.Context) {
	// 3-tier scheduler:
	// T1 (High): host_metrics every 15s (or hostEvery override).
	// T2 (Medium): process_snapshot + service_snapshot + network_connections every 60s.
	// T3 (Low): hardware_details + software_inventory every 12h (and at startup).
	const t2Every = 60 * time.Second

	c.logger.Info("collection engine: starting scheduler",
		"T1_hostEvery", c.hostEvery,
		"T2_every", t2Every,
		"T3_hardwareEvery", c.hardwareScanEvery,
		"T3_softwareEvery", c.softwareScanEvery,
	)

	t1 := time.NewTicker(c.hostEvery)
	defer t1.Stop()
	t2 := time.NewTicker(t2Every)
	defer t2.Stop()

	t3Software := time.NewTicker(c.softwareScanEvery)
	defer t3Software.Stop()
	t3Hardware := time.NewTicker(c.hardwareScanEvery)
	defer t3Hardware.Stop()
	t3Security := time.NewTicker(c.securityScanEvery)
	defer t3Security.Stop()

	// Startup: run low tier once (async, except hardware scan is synchronous to populate early).
	c.triggerSoftwareScan(ctx, "startup")
	c.runHardwareScan(ctx, "startup")
	c.triggerSecurityScan(ctx, "startup")
	// Startup: also kick a T2 refresh quickly (async).
	c.triggerProcessAndServiceSnapshots(ctx, "startup")
	c.triggerNetworkConnections(ctx, "startup")

	for {
		select {
		case <-ctx.Done():
			c.logger.Info("collection engine: stop requested", "reason", ctx.Err())
			c.logger.Info("collection engine: stopped")
			return
		case t := <-t1.C:
			metrics, err := c.host.CollectWithContext(ctx)
			if err != nil {
				c.logger.Warn("collection engine: failed to collect host metrics", "err", err, "ts", t.Format(time.RFC3339))
				continue
			}
			// Attach agent version for deployment dashboard / fleet rollout insights.
			if c.agentVersion != "" {
				metrics.AgentVersion = c.agentVersion
			}
			if c.buffer != nil {
				payload, err := json.Marshal(metrics)
				if err != nil {
					c.logger.Warn("collection engine: failed to marshal host metrics", "err", err)
					continue
				}
				if err := c.buffer.Save("host_metrics", payload); err != nil {
					c.logger.Warn("collection engine: failed to buffer host metrics", "err", err)
					continue
				}
				c.logger.Info("collection engine: host metrics buffered", "ts", metrics.CollectedAt.Format(time.RFC3339))
			}
		case t := <-t2.C:
			// Medium tier: run heavy dynamics asynchronously; don't block host ticker.
			c.triggerProcessAndServiceSnapshots(ctx, t.Format(time.RFC3339))
			c.triggerNetworkConnections(ctx, t.Format(time.RFC3339))
		case t := <-t3Software.C:
			c.triggerSoftwareScan(ctx, t.Format(time.RFC3339))
		case t := <-t3Hardware.C:
			c.triggerHardwareScan(ctx, t.Format(time.RFC3339))
		case t := <-t3Security.C:
			c.triggerSecurityScan(ctx, t.Format(time.RFC3339))
		}
	}
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum[:])
}

func (c *SimCollector) triggerSoftwareScan(ctx context.Context, reason string) {
	c.softwareScanMu.Lock()
	if c.softwareScanRunning {
		c.softwareScanMu.Unlock()
		return
	}
	c.softwareScanRunning = true
	c.softwareScanMu.Unlock()

	go func() {
		defer func() {
			c.softwareScanMu.Lock()
			c.softwareScanRunning = false
			c.softwareScanMu.Unlock()
		}()

		c.logger.Info("collection engine: software scan started", "reason", reason, "cacheTTL", c.softwareScanEvery)
		apps, err := c.software.Collect(ctx)
		if err != nil {
			c.logger.Warn("collection engine: software scan failed", "err", err, "reason", reason)
			return
		}
		if c.buffer != nil {
			// Wrap with fingerprint so backend can always attribute this batch item to a device,
			// even when DataDispatcher sends only software_inventory in a batch.
			fp := ""
			if hw, err := c.hardware.CollectWithContext(ctx); err == nil {
				fp = hw.Fingerprint
			}
			type softwareInventoryPayload struct {
				Fingerprint string                 `json:"fingerprint,omitempty"`
				Items       []collector.SoftwareInfo `json:"items"`
			}
			full := softwareInventoryPayload{Fingerprint: fp, Items: apps}
			payload, err := json.Marshal(full)
			if err != nil {
				c.logger.Warn("collection engine: failed to marshal software inventory", "err", err)
				return
			}
			h := hashBytes(payload)
			// Compare with last hash to optionally send a tiny no_change marker
			c.lastSoftwareHashMu.Lock()
			same := (h != "" && h == c.lastSoftwareHash)
			if !same {
				c.lastSoftwareHash = h
			}
			c.lastSoftwareHashMu.Unlock()
			if same {
				// Send a tiny diff marker so backend may reuse last version
				diffPayload, _ := json.Marshal(struct {
					Fingerprint string `json:"fingerprint,omitempty"`
					Status      string `json:"status"`
					Hash        string `json:"hash,omitempty"`
					Reason      string `json:"reason,omitempty"`
				}{Fingerprint: fp, Status: "no_change", Hash: h, Reason: reason})
				if err := c.buffer.Save("software_inventory", diffPayload); err != nil {
					c.logger.Warn("collection engine: failed to buffer software_inventory no_change", "err", err)
					return
				}
			} else {
				if err := c.buffer.Save("software_inventory", payload); err != nil {
					c.logger.Warn("collection engine: failed to buffer software inventory", "err", err)
					return
				}
			}
		}
		if len(apps) == 0 {
			c.logger.Info(
				"collection engine: software scan buffered",
				"reason", reason,
				"count", 0,
				"sample", "empty",
			)
		} else {
			first := apps[0]
			c.logger.Info(
				"collection engine: software scan buffered",
				"reason", reason,
				"count", len(apps),
				"sample_name", first.Name,
				"sample_version", first.Version,
				"sample_publisher", first.Publisher,
			)
		}
	}()
}

func (c *SimCollector) triggerHardwareScan(ctx context.Context, reason string) {
	c.hardwareScanMu.Lock()
	if c.hardwareScanRunning {
		c.hardwareScanMu.Unlock()
		return
	}
	c.hardwareScanRunning = true
	c.hardwareScanMu.Unlock()

	go func() {
		defer func() {
			c.hardwareScanMu.Lock()
			c.hardwareScanRunning = false
			c.hardwareScanMu.Unlock()
		}()
		c.runHardwareScan(ctx, reason)
	}()
}

func (c *SimCollector) runHardwareScan(ctx context.Context, reason string) {
	c.hardwareScanMu.Lock()
	running := c.hardwareScanRunning
	c.hardwareScanMu.Unlock()
	if !running {
		// If called directly (startup), mark running here.
		c.hardwareScanMu.Lock()
		if c.hardwareScanRunning {
			c.hardwareScanMu.Unlock()
			return
		}
		c.hardwareScanRunning = true
		c.hardwareScanMu.Unlock()
		defer func() {
			c.hardwareScanMu.Lock()
			c.hardwareScanRunning = false
			c.hardwareScanMu.Unlock()
		}()
	}

	c.logger.Info("collection engine: hardware scan started", "reason", reason, "cacheTTL", c.hardwareScanEvery)
	hw, err := c.hardware.CollectWithContext(ctx)
	if err != nil {
		c.logger.Warn("collection engine: hardware scan failed", "err", err, "reason", reason)
		// If we fail at startup (or any reason), schedule a short retry so the debug dashboard
		// doesn't get stuck without hardware_details until the next long interval.
		if ctx != nil && ctx.Err() == nil && c.hardwareRetryAfter > 0 {
			delay := c.hardwareRetryAfter
			time.AfterFunc(delay, func() {
				if ctx.Err() != nil {
					return
				}
				c.triggerHardwareScan(ctx, "retry_after_error")
			})
			c.logger.Info("collection engine: hardware scan retry scheduled", "after", delay)
		}
		return
	}
	c.logger.Info(
		"collection engine: hardware details collected",
		"reason", reason,
		"hostname", hw.Hostname,
		"fingerprint", hw.Fingerprint,
		"disk_count", len(hw.PhysicalDisks),
		"memory_slot_count", len(hw.MemorySlots),
		"gpu_count", len(hw.GPUs),
		"has_mainboard", hw.Mainboard != nil,
	)

	if c.buffer != nil {
		payload, err := json.Marshal(hw)
		if err != nil {
			c.logger.Warn("collection engine: failed to marshal hardware details", "err", err)
			return
		}
		c.logger.Info("collection engine: hardware details marshaled", "reason", reason, "payload_bytes", len(payload))
		if err := c.buffer.Save("hardware_details", payload); err != nil {
			c.logger.Warn("collection engine: failed to buffer hardware details", "err", err)
			return
		}
		c.logger.Info("collection engine: hardware details buffered", "reason", reason, "payload_bytes", len(payload))
	}
	c.logger.Info("collection engine: hardware scan buffered", "reason", reason)
}

func (c *SimCollector) triggerProcessSnapshot(ctx context.Context, reason string) {
	// Backward-compat wrapper: the new scheduler collects process + service snapshots together.
	c.triggerProcessAndServiceSnapshots(ctx, reason)
}

// triggerProcessAndServiceSnapshots collects a combined snapshot and buffers
// - process_snapshot (full snapshot)
// - service_snapshot (services only)
// with a mutex to prevent reentry.
func (c *SimCollector) triggerProcessAndServiceSnapshots(ctx context.Context, reason string) {
	c.processSnapshotMu.Lock()
	if c.processSnapshotRunning {
		c.processSnapshotMu.Unlock()
		return
	}
	c.processSnapshotRunning = true
	c.processSnapshotMu.Unlock()

	go func() {
		defer func() {
			c.processSnapshotMu.Lock()
			c.processSnapshotRunning = false
			c.processSnapshotMu.Unlock()
		}()

		c.logger.Info("collection engine: process+service snapshot started", "reason", reason)
		snap, err := c.processSnapshot.CollectWithContext(ctx)
		if err != nil {
			c.logger.Warn("collection engine: process snapshot failed", "err", err, "reason", reason)
			return
		}

		if c.buffer == nil {
			return
		}

		payload, err := json.Marshal(snap)
		if err != nil {
			c.logger.Warn("collection engine: failed to marshal process snapshot", "err", err)
			return
		}

		// Optional perf protection: if full snapshot unchanged, send tiny no_change marker.
		h := hashBytes(payload)
		c.lastProcessHashMu.Lock()
		same := (h != "" && h == c.lastProcessHash)
		if !same {
			c.lastProcessHash = h
		}
		c.lastProcessHashMu.Unlock()
		if same {
			// Ensure fingerprint present even for no_change markers.
			fp := ""
			if hw, err := c.hardware.CollectWithContext(ctx); err == nil {
				fp = hw.Fingerprint
			}
			diffPayload, _ := json.Marshal(struct {
				Fingerprint string `json:"fingerprint,omitempty"`
				Status      string `json:"status"`
				Hash        string `json:"hash,omitempty"`
				Note        string `json:"note,omitempty"`
			}{Fingerprint: fp, Status: "no_change", Hash: h, Note: reason})
			if err := c.buffer.Save("process_snapshot", diffPayload); err != nil {
				c.logger.Warn("collection engine: failed to buffer process_snapshot no_change", "err", err)
			} else {
				c.logger.Info("collection engine: process snapshot unchanged; buffered no_change", "reason", reason)
			}
			return
		}

		// Attach fingerprint to process_snapshot payload.
		fp := ""
		if hw, err := c.hardware.CollectWithContext(ctx); err == nil {
			fp = hw.Fingerprint
		}
		type processSnapshotWithFP struct {
			Fingerprint string                  `json:"fingerprint,omitempty"`
			Processes   []collector.ProcessInfo `json:"processes"`
			Services    []collector.ServiceInfo `json:"services"`
			CollectedAt time.Time               `json:"collected_at"`
		}
		fpPayload, _ := json.Marshal(processSnapshotWithFP{
			Fingerprint: fp,
			Processes:   snap.Processes,
			Services:    snap.Services,
			CollectedAt: snap.CollectedAt,
		})
		if err := c.buffer.Save("process_snapshot", fpPayload); err != nil {
			c.logger.Warn("collection engine: failed to buffer process snapshot", "err", err)
			return
		}
		c.logger.Info(
			"collection engine: process_snapshot buffered",
			"reason", reason,
			"process_count", len(snap.Processes),
			"service_count", len(snap.Services),
		)

		// Buffer services only as service_snapshot for T2 consumers.
		// Also include fingerprint for service_snapshot.
		type serviceSnapshotWithFP struct {
			Fingerprint string                  `json:"fingerprint,omitempty"`
			Services    []collector.ServiceInfo `json:"services"`
			CollectedAt time.Time               `json:"collected_at"`
		}
		fpSvc := ""
		if hw, err := c.hardware.CollectWithContext(ctx); err == nil {
			fpSvc = hw.Fingerprint
		}
		svcPayload, err := json.Marshal(serviceSnapshotWithFP{
			Fingerprint: fpSvc,
			Services:    snap.Services,
			CollectedAt: snap.CollectedAt,
		})
		if err != nil {
			c.logger.Warn("collection engine: failed to marshal service snapshot", "err", err)
			return
		}
		if err := c.buffer.Save("service_snapshot", svcPayload); err != nil {
			c.logger.Warn("collection engine: failed to buffer service snapshot", "err", err)
			return
		}
		c.logger.Info("collection engine: service_snapshot buffered", "reason", reason, "service_count", len(snap.Services))
	}()
}

func (c *SimCollector) triggerNetworkConnections(ctx context.Context, reason string) {
	c.netConnMu.Lock()
	if c.netConnRunning {
		c.netConnMu.Unlock()
		return
	}
	c.netConnRunning = true
	c.netConnMu.Unlock()

	go func() {
		defer func() {
			c.netConnMu.Lock()
			c.netConnRunning = false
			c.netConnMu.Unlock()
		}()

		c.logger.Info("collection engine: network connections started", "reason", reason)
		snap, err := c.netConnections.CollectWithContext(ctx)
		if err != nil {
			c.logger.Warn("collection engine: network connections failed", "err", err, "reason", reason)
			return
		}
		if c.buffer == nil {
			return
		}
		// Include fingerprint for network_connections
		fp := ""
		if hw, err := c.hardware.CollectWithContext(ctx); err == nil {
			fp = hw.Fingerprint
		}
		type netConnsWithFP struct {
			Fingerprint string                      `json:"fingerprint,omitempty"`
			Connections []collector.NetConnInfo     `json:"connections"`
			CollectedAt time.Time                   `json:"collected_at"`
		}
		payload, err := json.Marshal(netConnsWithFP{
			Fingerprint: fp,
			Connections: snap.Connections,
			CollectedAt: snap.CollectedAt,
		})
		if err != nil {
			c.logger.Warn("collection engine: failed to marshal network connections", "err", err)
			return
		}
		if err := c.buffer.Save("network_connections", payload); err != nil {
			c.logger.Warn("collection engine: failed to buffer network connections", "err", err)
			return
		}
		c.logger.Info("collection engine: network_connections buffered", "reason", reason, "conn_count", len(snap.Connections))
	}()
}

func (c *SimCollector) triggerSecurityScan(ctx context.Context, reason string) {
	c.securityScanMu.Lock()
	if c.securityScanRunning {
		c.securityScanMu.Unlock()
		return
	}
	c.securityScanRunning = true
	c.securityScanMu.Unlock()

	go func() {
		defer func() {
			c.securityScanMu.Lock()
			c.securityScanRunning = false
			c.securityScanMu.Unlock()
		}()

		snap, err := c.security.CollectWithContext(ctx)
		if err != nil {
			c.logger.Warn("collection engine: security snapshot failed", "err", err, "reason", reason)
			return
		}
		if c.buffer != nil {
			// Include fingerprint for security_snapshot
			fp := ""
			if hw, err := c.hardware.CollectWithContext(ctx); err == nil {
				fp = hw.Fingerprint
			}
			type securityWithFP struct {
				Fingerprint string               `json:"fingerprint,omitempty"`
				SecuritySnapshot collector.SecuritySnapshot `json:"-"`
				Connections []collector.NetConnInfo  `json:"connections"`
				Startup     []collector.StartupItem  `json:"startup"`
				Hotfixes    []collector.HotfixInfo   `json:"hotfixes"`
				CollectedAt time.Time                `json:"collected_at"`
			}
			payload, err := json.Marshal(struct {
				Fingerprint string                   `json:"fingerprint,omitempty"`
				Connections []collector.NetConnInfo  `json:"connections"`
				Startup     []collector.StartupItem  `json:"startup"`
				Hotfixes    []collector.HotfixInfo   `json:"hotfixes"`
				CollectedAt time.Time                `json:"collected_at"`
			}{
				Fingerprint: fp,
				Connections: snap.Connections,
				Startup:     snap.Startup,
				Hotfixes:    snap.Hotfixes,
				CollectedAt: snap.CollectedAt,
			})
			if err != nil {
				c.logger.Warn("collection engine: failed to marshal security snapshot", "err", err)
				return
			}
			if err := c.buffer.Save("security_snapshot", payload); err != nil {
				c.logger.Warn("collection engine: failed to buffer security snapshot", "err", err)
				return
			}
		}
		c.logger.Info("collection engine: security snapshot buffered",
			"reason", reason,
			"conn_count", len(snap.Connections),
			"startup_count", len(snap.Startup),
			"hotfix_count", len(snap.Hotfixes),
		)
	}()
}
