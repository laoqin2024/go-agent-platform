package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/qinyilin/go-agent/internal/server/model"
	"github.com/qinyilin/go-agent/internal/server/store"
	"github.com/qinyilin/go-agent/internal/server/ws"
)

type ReportHandler struct {
	store  *store.RedisSnapshotStore
	wsHub  *ws.Hub
	logger *slog.Logger
}

func NewReportHandler(snapshotStore *store.RedisSnapshotStore, wsHub *ws.Hub, logger *slog.Logger) *ReportHandler {
	return &ReportHandler{
		store:  snapshotStore,
		wsHub:  wsHub,
		logger: logger,
	}
}

// POST /api/v1/report
//
// Supported body formats:
//  1. Old:
//     {
//     "device_id": "xxx",
//     "processes": [...],
//     "software_list": [...],
//     "reported_at": 1710000000
//     }
//  2. DataDispatcher batch (compat):
//     { "items": [ { "dataType": "process|software", "payload": {...} }, ... ] }
func (h *ReportHandler) HandleReport(c *gin.Context) {
	body, err := c.GetRawData()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read request body"})
		return
	}

	// Try new batch format first.
	var batch ingestBatchRequest
	if err := json.Unmarshal(body, &batch); err == nil && len(batch.Items) > 0 {
		if err := h.handleBatch(c.Request.Context(), batch); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
		return
	}

	// Fallback to old direct-report format.
	var req model.ReportRequest
	if err := json.Unmarshal(body, &req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON"})
		return
	}
	if req.DeviceID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "device_id is required"})
		return
	}

	if err := h.handleOldDirect(c.Request.Context(), req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok", "device_id": req.DeviceID})
}

type ingestBatchRequest struct {
	Items []ingestBatchItem `json:"items"`
}

type ingestBatchItem struct {
	DataType string          `json:"dataType"`
	Payload  json.RawMessage `json:"payload"`
}

type devicePartialUpdate struct {
	hasProcesses    bool
	processesRaw    json.RawMessage
	hasSoftware     bool
	softwareListRaw json.RawMessage

	hasHostMetrics        bool
	hostMetricsRaw        json.RawMessage
	hasHardwareDetails    bool
	hardwareDetailsRaw    json.RawMessage
	hasSoftwareInventory  bool
	softwareInventoryRaw  json.RawMessage
	hasProcessSnapshot    bool
	processSnapshotRaw    json.RawMessage
	hasServiceSnapshot    bool
	serviceSnapshotRaw    json.RawMessage
	hasNetworkConnections bool
	networkConnectionsRaw json.RawMessage
	hasSecuritySnapshot   bool
	securitySnapshotRaw   json.RawMessage
}

func (h *ReportHandler) handleOldDirect(ctx context.Context, req model.ReportRequest) error {
	// Normalize nil => empty arrays to keep redis/ui simple.
	if len(req.Processes) == 0 {
		req.Processes = []byte("[]")
	}
	if len(req.SoftwareList) == 0 {
		req.SoftwareList = []byte("[]")
	}

	const maxListLen = 1000
	if out, truncated, origLen, err := truncateJSONArray(req.Processes, maxListLen); err != nil {
		return err
	} else if truncated {
		h.logger.Warn("snapshot truncated", "device_id", req.DeviceID, "field", "processes", "orig_len", origLen, "limit", maxListLen)
		req.Processes = out
	}
	if out, truncated, origLen, err := truncateJSONArray(req.SoftwareList, maxListLen); err != nil {
		return err
	} else if truncated {
		h.logger.Warn("snapshot truncated", "device_id", req.DeviceID, "field", "software_list", "orig_len", origLen, "limit", maxListLen)
		req.SoftwareList = out
	}

	updatedAt := req.ReportedAtSec
	if updatedAt == 0 {
		updatedAt = time.Now().Unix()
	}

	snap := model.SnapshotPush{
		DeviceID:     req.DeviceID,
		Processes:    req.Processes,
		SoftwareList: req.SoftwareList,
		UpdatedAtSec: updatedAt,
	}

	saveCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := h.store.SaveSnapshot(saveCtx, snap); err != nil {
		h.logger.Error("save snapshot to redis failed", "err", err, "device_id", req.DeviceID)
		return err
	}

	go h.broadcastSnapshot(req.DeviceID, snap)
	return nil
}

func (h *ReportHandler) handleBatch(ctx context.Context, batch ingestBatchRequest) error {
	// First pass: find unique fingerprint in payloads (used as device_id for items without fingerprint).
	fingerprints := make(map[string]struct{})
	for _, it := range batch.Items {
		fp := extractFingerprintFromPayload(it.Payload)
		if fp != "" {
			fingerprints[fp] = struct{}{}
		}
	}
	var batchDeviceID string
	if len(fingerprints) == 1 {
		for k := range fingerprints {
			batchDeviceID = k
		}
	}

	const maxListLen = 1000
	now := time.Now().Unix()

	partials := make(map[string]*devicePartialUpdate)
	getPartial := func(deviceID string) *devicePartialUpdate {
		p := partials[deviceID]
		if p == nil {
			p = &devicePartialUpdate{}
			partials[deviceID] = p
		}
		return p
	}

	// Second pass: extract processes/software per item and accumulate partial updates.
	for _, it := range batch.Items {
		dt := normalizeDataType(it.DataType)
		if it.Payload == nil {
			continue
		}
		switch dt {
		case "host_metrics":
			fp := extractFingerprintFromPayload(it.Payload)
			if fp == "" {
				fp = batchDeviceID
			}
			if fp == "" {
				h.logger.Warn("batch item missing fingerprint; skip host_metrics update", "dataType", it.DataType)
				continue
			}
			p := getPartial(fp)
			p.hasHostMetrics = true
			p.hostMetricsRaw = it.Payload

		case "hardware_details":
			fp := extractFingerprintFromPayload(it.Payload)
			if fp == "" {
				fp = batchDeviceID
			}
			if fp == "" {
				h.logger.Warn("batch item missing fingerprint; skip hardware_details update", "dataType", it.DataType)
				continue
			}
			p := getPartial(fp)
			p.hasHardwareDetails = true
			p.hardwareDetailsRaw = it.Payload

		case "process", "process_snapshot":
			processesRaw, ok := extractProcessesFromPayload(it.Payload)
			if !ok {
				continue
			}
			fp := extractFingerprintFromPayload(it.Payload)
			if fp == "" {
				fp = batchDeviceID
			}
			if fp == "" {
				h.logger.Warn("batch item missing fingerprint; skip processes update", "dataType", it.DataType)
				continue
			}

			out, truncated, origLen, err := truncateJSONArray(processesRaw, maxListLen)
			if err != nil {
				return err
			}
			if truncated {
				h.logger.Warn("snapshot truncated", "device_id", fp, "field", "processes", "orig_len", origLen, "limit", maxListLen)
			}
			p := getPartial(fp)
			p.hasProcesses = true
			p.processesRaw = out

			// Keep the raw snapshot too for debug_server-like panels.
			p.hasProcessSnapshot = true
			p.processSnapshotRaw = it.Payload

		case "software", "software_inventory":
			softwareRaw, ok := extractSoftwareFromPayload(it.Payload)
			if !ok {
				continue
			}
			fp := extractFingerprintFromPayload(it.Payload)
			if fp == "" {
				fp = batchDeviceID
			}
			if fp == "" {
				h.logger.Warn("batch item missing fingerprint; skip software update", "dataType", it.DataType)
				continue
			}

			out, truncated, origLen, err := truncateJSONArray(softwareRaw, maxListLen)
			if err != nil {
				return err
			}
			if truncated {
				h.logger.Warn("snapshot truncated", "device_id", fp, "field", "software_list", "orig_len", origLen, "limit", maxListLen)
			}
			p := getPartial(fp)
			p.hasSoftware = true
			p.softwareListRaw = out

			// Also keep the raw inventory payload.
			p.hasSoftwareInventory = true
			p.softwareInventoryRaw = it.Payload

		case "service_snapshot":
			fp := extractFingerprintFromPayload(it.Payload)
			if fp == "" {
				fp = batchDeviceID
			}
			if fp == "" {
				h.logger.Warn("batch item missing fingerprint; skip service_snapshot update", "dataType", it.DataType)
				continue
			}
			p := getPartial(fp)
			p.hasServiceSnapshot = true
			p.serviceSnapshotRaw = it.Payload

		case "network_connections":
			fp := extractFingerprintFromPayload(it.Payload)
			if fp == "" {
				fp = batchDeviceID
			}
			if fp == "" {
				h.logger.Warn("batch item missing fingerprint; skip network_connections update", "dataType", it.DataType)
				continue
			}
			p := getPartial(fp)
			p.hasNetworkConnections = true
			p.networkConnectionsRaw = it.Payload

		case "security_snapshot":
			fp := extractFingerprintFromPayload(it.Payload)
			if fp == "" {
				fp = batchDeviceID
			}
			if fp == "" {
				h.logger.Warn("batch item missing fingerprint; skip security_snapshot update", "dataType", it.DataType)
				continue
			}
			p := getPartial(fp)
			p.hasSecuritySnapshot = true
			p.securitySnapshotRaw = it.Payload
		default:
			// ignore other item types
		}
	}

	if len(partials) == 0 {
		// Batch without process/software updates is still OK.
		return nil
	}

	saveCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	// Persist merged full snapshot per device, then broadcast once per device (after full batch handling).
	for deviceID, p := range partials {
		var merged model.SnapshotPush
		merged.DeviceID = deviceID
		merged.UpdatedAtSec = now

		existing, ok, err := h.store.LoadSnapshot(saveCtx, deviceID)
		if err != nil {
			return err
		}
		if ok {
			merged.Processes = existing.Processes
			merged.SoftwareList = existing.SoftwareList
			merged.HostMetrics = existing.HostMetrics
			merged.HardwareDetails = existing.HardwareDetails
			merged.SoftwareInventory = existing.SoftwareInventory
			merged.ProcessSnapshot = existing.ProcessSnapshot
			merged.ServiceSnapshot = existing.ServiceSnapshot
			merged.NetworkConnections = existing.NetworkConnections
			merged.SecuritySnapshot = existing.SecuritySnapshot
		} else {
			merged.Processes = []byte("[]")
			merged.SoftwareList = []byte("[]")
		}

		if p.hasProcesses {
			merged.Processes = p.processesRaw
		}
		if p.hasSoftware {
			merged.SoftwareList = p.softwareListRaw
		}
		if p.hasHostMetrics {
			merged.HostMetrics = p.hostMetricsRaw
		}
		if p.hasHardwareDetails {
			merged.HardwareDetails = p.hardwareDetailsRaw
		}
		if p.hasSoftwareInventory {
			merged.SoftwareInventory = p.softwareInventoryRaw
		}
		if p.hasProcessSnapshot {
			merged.ProcessSnapshot = p.processSnapshotRaw
		}
		if p.hasServiceSnapshot {
			merged.ServiceSnapshot = p.serviceSnapshotRaw
		}
		if p.hasNetworkConnections {
			merged.NetworkConnections = p.networkConnectionsRaw
		}
		if p.hasSecuritySnapshot {
			merged.SecuritySnapshot = p.securitySnapshotRaw
		}

		if err := h.store.SaveSnapshot(saveCtx, merged); err != nil {
			h.logger.Error("save merged snapshot to redis failed", "err", err, "device_id", deviceID)
			return err
		}

		// Best-effort re-read to broadcast what is actually stored (TTL marker etc.).
		finalSnap := merged
		if snap2, ok2, err := h.store.LoadSnapshot(saveCtx, deviceID); err == nil && ok2 {
			finalSnap = snap2
		}

		go h.broadcastSnapshot(deviceID, finalSnap)
	}

	return nil
}

func (h *ReportHandler) broadcastSnapshot(deviceID string, snap model.SnapshotPush) {
	payloadBytes, err := json.Marshal(snap)
	if err != nil {
		h.logger.Error("marshal snapshot push failed", "err", err, "device_id", deviceID)
		return
	}
	if _, err := h.wsHub.BroadcastToDevice(deviceID, payloadBytes); err != nil {
		h.logger.Error("ws broadcast failed", "err", err, "device_id", deviceID)
	}
}

func normalizeDataType(dt string) string {
	return strings.ToLower(strings.TrimSpace(dt))
}

func extractFingerprintFromPayload(payload json.RawMessage) string {
	// Payload is expected to be an object containing a `fingerprint`/`Fingerprint` string.
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(payload, &obj); err != nil {
		return ""
	}
	for _, k := range []string{"fingerprint", "Fingerprint"} {
		if v, ok := obj[k]; ok {
			var s string
			if err := json.Unmarshal(v, &s); err == nil && strings.TrimSpace(s) != "" {
				return s
			}
		}
	}
	return ""
}

func extractProcessesFromPayload(payload json.RawMessage) (json.RawMessage, bool) {
	// Accept either:
	// - an array payload: [...] => processesRaw
	// - an object payload: { "processes": [...] }
	var arr []json.RawMessage
	if err := json.Unmarshal(payload, &arr); err == nil {
		return payload, true
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(payload, &obj); err != nil {
		return nil, false
	}
	for _, k := range []string{"processes", "Processes"} {
		if raw, ok := obj[k]; ok && raw != nil {
			var verify []json.RawMessage
			if err := json.Unmarshal(raw, &verify); err == nil {
				return raw, true
			}
		}
	}
	return nil, false
}

func extractSoftwareFromPayload(payload json.RawMessage) (json.RawMessage, bool) {
	// Accept either:
	// - an array payload: [...] => software_list
	// - an object payload: { "software_list": [...] } (or { "software": [...] })
	// - an object payload: { "items": [...] }  (agent software_inventory wrapper)
	var arr []json.RawMessage
	if err := json.Unmarshal(payload, &arr); err == nil {
		return payload, true
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(payload, &obj); err != nil {
		return nil, false
	}
	for _, k := range []string{"software_list", "softwareList", "software", "SoftwareList", "Software", "items", "Items", "apps", "Apps"} {
		if raw, ok := obj[k]; ok && raw != nil {
			var verify []json.RawMessage
			if err := json.Unmarshal(raw, &verify); err == nil {
				return raw, true
			}
		}
	}
	return nil, false
}

// truncateJSONArray ensures raw is a JSON array and truncates it to at most limit elements.
// Returns outRaw (either original or truncated), whether it was truncated, original length, and error.
func truncateJSONArray(raw json.RawMessage, limit int) (outRaw []byte, truncated bool, origLen int, err error) {
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil, false, 0, err
	}
	origLen = len(arr)
	if origLen <= limit {
		return raw, false, origLen, nil
	}
	arr = arr[:limit]
	b, err := json.Marshal(arr)
	if err != nil {
		return nil, false, origLen, err
	}
	return b, true, origLen, nil
}
