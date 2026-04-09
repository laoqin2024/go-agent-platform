package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"time"

	"github.com/qinyilin/go-agent/internal/buffer"
	"github.com/qinyilin/go-agent/internal/transport"
)

// DataDispatcher periodically sends buffered records to backend via mTLS.
type DataDispatcher struct {
	logger *slog.Logger
	buffer *buffer.DataBuffer
	client *transport.HttpClient

	interval   time.Duration
	batchLimit int
	requestTTL time.Duration
	apiPath    string
}

type DataDispatcherOption func(*DataDispatcher)

func WithDispatcherInterval(d time.Duration) DataDispatcherOption {
	return func(disp *DataDispatcher) {
		if d > 0 {
			disp.interval = d
		}
	}
}

func WithDispatcherBatchLimit(n int) DataDispatcherOption {
	return func(disp *DataDispatcher) {
		if n > 0 {
			disp.batchLimit = n
		}
	}
}

func WithDispatcherRequestTTL(d time.Duration) DataDispatcherOption {
	return func(disp *DataDispatcher) {
		if d > 0 {
			disp.requestTTL = d
		}
	}
}

func WithDispatcherAPIPath(path string) DataDispatcherOption {
	return func(disp *DataDispatcher) {
		if path != "" {
			disp.apiPath = path
		}
	}
}

// NewDataDispatcher creates a dispatcher.
// Note: apiPath is appended to the HttpClient endpoint inside this module by simply setting client.endpoint as full URL;
// to keep HttpClient generic, we send to apiPath as-is by overriding endpoint with apiPath in caller config.
func NewDataDispatcher(
	logger *slog.Logger,
	buf *buffer.DataBuffer,
	client *transport.HttpClient,
	opts ...DataDispatcherOption,
) *DataDispatcher {
	d := &DataDispatcher{
		logger:      logger,
		buffer:      buf,
		client:      client,
		interval:    30 * time.Second,
		batchLimit:  10,
		requestTTL:  15 * time.Second,
		apiPath:     "",
	}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

func (d *DataDispatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			d.logger.Info("data dispatcher: stop requested", "reason", ctx.Err())
			d.logger.Info("data dispatcher: stopped")
			return
		case <-ticker.C:
			d.dispatchOnce(ctx)
		}
	}
}

// DispatchNow exposes a one-shot dispatch for urgent events.
// Safe to call from other goroutines.
func (d *DataDispatcher) DispatchNow(ctx context.Context) {
	d.dispatchOnce(ctx)
}

type apiBatchRequestItem struct {
	DataType string          `json:"dataType"`
	Payload  json.RawMessage `json:"payload"`
}

type apiBatchRequest struct {
	Items []apiBatchRequestItem `json:"items"`
}

func (d *DataDispatcher) dispatchOnce(ctx context.Context) {
	records, err := d.peekFairBatch(d.batchLimit)
	if err != nil {
		d.logger.Warn("data dispatcher: peek batch failed", "err", err)
		return
	}
	if len(records) == 0 {
		return
	}

	items := make([]apiBatchRequestItem, 0, len(records))
	for _, r := range records {
		if !json.Valid(r.Payload) {
			// Keep the payload as raw bytes; if backend expects JSON only, this will fail and we keep cache.
			d.logger.Warn("data dispatcher: payload is not valid JSON; keeping record", "dataType", r.DataType)
			return
		}
		items = append(items, apiBatchRequestItem{
			DataType: r.DataType,
			Payload:  json.RawMessage(r.Payload),
		})
	}

	reqBody, err := json.Marshal(apiBatchRequest{Items: items})
	if err != nil {
		d.logger.Warn("data dispatcher: marshal request failed", "err", err)
		return
	}

	sendCtx, cancel := context.WithTimeout(ctx, d.requestTTL)
	defer cancel()

	// This implementation assumes client.endpoint is already the full ingest URL.
	status, respBody, err := d.client.PostJSON(sendCtx, reqBody)
	if err != nil {
		// Network timeout / transport errors => keep cache.
		var netErr interface{ Timeout() bool }
		if errors.As(err, &netErr) && netErr.Timeout() {
			d.logger.Warn("data dispatcher: request timeout; keeping cache", "err", err)
		} else {
			d.logger.Warn("data dispatcher: request failed; keeping cache", "err", err)
		}
		return
	}

	if status == 200 {
		keys := make([][]byte, 0, len(records))
		for _, r := range records {
			keys = append(keys, r.Key)
		}
		if err := d.buffer.DeleteBatch(keys); err != nil {
			d.logger.Warn("data dispatcher: delete batch failed after success", "err", err)
			return
		}
		d.logger.Info("data dispatcher: batch sent and cleared", "count", len(records))
		return
	}

	// For non-200: keep cache and retry later (as required for 5xx / network failures).
	d.logger.Warn(
		"data dispatcher: backend rejected request; keeping cache",
		"status", status,
		"resp", string(respBody),
	)
}

// peekFairBatch returns up to limit records but tries to avoid starving low-frequency types
// (e.g. hardware_details/software_inventory) under sustained high-frequency metrics.
//
// Strategy:
// - Peek a larger time-ordered window from the buffer.
// - Pick records in a weighted round-robin by type, then by time order within type.
// - Preserve "oldest-first" as much as possible while ensuring each type gets airtime.
func (d *DataDispatcher) peekFairBatch(limit int) ([]buffer.BufferRecord, error) {
	if limit <= 0 {
		return nil, nil
	}

	// Scan a bigger window to give rare types a chance to be selected.
	scan := limit * 20
	if scan < 200 {
		scan = 200
	}
	if scan > 2000 {
		scan = 2000
	}

	window, err := d.buffer.PeekBatch(scan)
	if err != nil {
		return nil, err
	}
	if len(window) <= limit {
		return window, nil
	}

	// Group by type.
	type group struct {
		typ string
		rs  []buffer.BufferRecord
		i   int
	}
	gm := make(map[string]*group, 8)
	order := make([]string, 0, 8)
	for _, r := range window {
		g := gm[r.DataType]
		if g == nil {
			g = &group{typ: r.DataType}
			gm[r.DataType] = g
			order = append(order, r.DataType)
		}
		g.rs = append(g.rs, r)
	}

	// Deterministic: keep type order stable (lexicographic) so behavior is reproducible.
	sort.Strings(order)

	// Weights: give low-frequency/large payload types more chances per cycle.
	// This is conservative; it won't block metrics, just guarantees progress.
	weightOf := func(dt string) int {
		switch dt {
		case "hardware_details", "software_inventory", "security_snapshot":
			return 3
		case "process_snapshot", "service_snapshot", "network_connections":
			return 2
		default:
			return 1
		}
	}

	out := make([]buffer.BufferRecord, 0, limit)
	for len(out) < limit {
		progress := false
		for _, dt := range order {
			g := gm[dt]
			if g == nil || g.i >= len(g.rs) {
				continue
			}
			w := weightOf(dt)
			for j := 0; j < w && len(out) < limit && g.i < len(g.rs); j++ {
				out = append(out, g.rs[g.i])
				g.i++
				progress = true
			}
		}
		if !progress {
			break
		}
	}
	return out, nil
}
