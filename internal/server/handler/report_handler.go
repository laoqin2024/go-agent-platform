package handler

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/qinyilin/go-agent/internal/collector"
	"github.com/qinyilin/go-agent/internal/server/model"
	"github.com/qinyilin/go-agent/internal/server/store"
	"github.com/qinyilin/go-agent/internal/server/usb"
	"github.com/qinyilin/go-agent/internal/server/ws"
)

type ReportHandler struct {
	store    *store.RedisSnapshotStore
	usbStore *store.USBLogStore
	wsHub    *ws.Hub
	logger   *slog.Logger
	usbMgr   usb.Service
}

// ApiBatchRequest is a Swagger-facing schema for /report batch ingest.
// Real runtime parsing still uses ingestBatchRequest for compatibility.
type ApiBatchRequest struct {
	Items []ApiBatchItem `json:"items"`
}

// ApiReportSwaggerRequest documents /api/v1/report accepted payloads (direct + batch).
// It exists mainly so Swagger can "see" model types like model.USBEvent and render them in the Models section.
type ApiReportSwaggerRequest struct {
	// Batch format (compat).
	Items []ApiBatchItem `json:"items,omitempty"`

	// Old direct-report format.
	DeviceID     string          `json:"device_id,omitempty"`
	// Use `any` so Swagger can render a generic schema (process snapshots can be huge/variable).
	Processes    any `json:"processes,omitempty"`
	SoftwareList any `json:"software_list,omitempty"`
	// Backward-compatible: some agents may send a single event.
	USBEvent      any `json:"usb_event,omitempty"`
	ReportedAtSec int64           `json:"reported_at,omitempty"`

	// Put this field near the end so Swagger UI tends to list this model near the bottom.
	USBEvents []model.USBEvent `json:"usb_events,omitempty"`
}

// ApiBatchItem documents possible payload models by dataType.
type ApiBatchItem struct {
	DataType string         `json:"dataType"`
	Payload  map[string]any `json:"payload,omitempty"`

	// Optional typed hints for Swagger model expansion:
	HostMetrics      *collector.HostMetrics    `json:"host_metrics,omitempty"`
	SecuritySnapshot *collector.SecuritySnapshot `json:"security_snapshot,omitempty"`
}

func NewReportHandler(snapshotStore *store.RedisSnapshotStore, usbStore *store.USBLogStore, wsHub *ws.Hub, logger *slog.Logger) *ReportHandler {
	return &ReportHandler{
		store:    snapshotStore,
		usbStore: usbStore,
		wsHub:    wsHub,
		logger:   logger,
	}
}

// WithUSBManager injects a usb manager for decoupled USB handling and broadcasts.
func (h *ReportHandler) WithUSBManager(m usb.Service) *ReportHandler {
	if h != nil {
		h.usbMgr = m
	}
	return h
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
//
// ReportMetrics 接收 Agent 上报的批量指标数据
// @Summary      上报设备指标
// @Description  接收经过 Gzip 压缩的上报包（支持 direct 与 batch 两种格式）。其中也包含 USB 安全审计事件（usb_events / usb_event / batch items dataType=usb_event|usb|usb_audit）。
//
// 示例（USB 事件 - 旧版 direct）:
// {
//   "device_id": "HOST_FINGERPRINT_ABC",
//   "usb_events": [
//     {
//       "device_id": "HOST_FINGERPRINT_ABC",
//       "action": "insert",
//       "usb_id": "USB\\\\VID_0781&PID_5580&REV_0001",
//       "volume_name": "E:",
//       "timestamp": 1710000000
//     }
//   ],
//   "reported_at": 1710000000
// }
//
// 示例（USB 事件 - batch items）:
// {
//   "items": [
//     {
//       "dataType": "usb_event",
//       "payload": {
//         "device_id": "HOST_FINGERPRINT_ABC",
//         "usb_id": "USB\\\\VID_0781&PID_5580&REV_0001",
//         "volume_name": "E:",
//         "action": "insert",
//         "timestamp": 1710000000
//       }
//     }
//   ]
// }
// @Accept       json
// @Produce      json
// @Param        Content-Encoding  header  string  true  "必须为 gzip"
// @Param        payload           body    ApiReportSwaggerRequest  true  "指标数据包（兼容 direct 与 batch；含 USB 事件 usb_events/usb_event 或 batch items）"
// @Success      200  {object}  map[string]string "{"status":"ok"}"
// @Failure      400  {object}  map[string]string "{"error":"invalid JSON"}"
// @Failure      413  {object}  map[string]string "{"error":"payload too large"}"
// @Router       /report [post]
func (h *ReportHandler) HandleReport(c *gin.Context) {
	body, err := readReportBody(c, maxReportBodyBytes())
	if err != nil {
		h.logger.Warn("report read body failed", "err", err)
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
	} else if err != nil && isJSONSyntaxErr(err) {
		h.logInvalidJSON(c, err, body, "batch")
	}

	// Fallback to old direct-report format.
	var req model.ReportRequest
	if err := json.Unmarshal(body, &req); err != nil {
		h.logInvalidJSON(c, err, body, "direct")
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

func maxReportBodyBytes() int64 {
	// Default is intentionally generous to tolerate large host_metrics/process snapshots.
	const def int64 = 32 << 20 // 32 MiB
	v := strings.TrimSpace(os.Getenv("REPORT_MAX_BODY_BYTES"))
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

func readReportBody(c *gin.Context, maxBodyBytes int64) ([]byte, error) {
	req := c.Request
	if req == nil || req.Body == nil {
		return nil, errors.New("empty request body")
	}
	limited := http.MaxBytesReader(c.Writer, req.Body, maxBodyBytes)
	encoding := strings.ToLower(strings.TrimSpace(req.Header.Get("Content-Encoding")))
	var reader io.Reader = limited
	if strings.Contains(encoding, "gzip") {
		gzr, err := gzip.NewReader(limited)
		if err != nil {
			return nil, fmt.Errorf("invalid gzip body: %w", err)
		}
		defer gzr.Close()
		reader = gzr
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	return body, nil
}

func isJSONSyntaxErr(err error) bool {
	var se *json.SyntaxError
	var ute *json.UnmarshalTypeError
	return errors.As(err, &se) || errors.As(err, &ute)
}

func (h *ReportHandler) logInvalidJSON(c *gin.Context, err error, body []byte, phase string) {
	offset := int64(-1)
	var se *json.SyntaxError
	var ute *json.UnmarshalTypeError
	switch {
	case errors.As(err, &se):
		offset = se.Offset
	case errors.As(err, &ute):
		offset = ute.Offset
	}
	before, after := snippetAroundOffset(body, offset, 200)
	contentEncoding := ""
	contentLengthHeader := ""
	contentLength := int64(-1)
	if c != nil && c.Request != nil {
		contentEncoding = c.GetHeader("Content-Encoding")
		contentLengthHeader = c.GetHeader("Content-Length")
		contentLength = c.Request.ContentLength
	}

	h.logger.Warn(
		"invalid JSON in report payload",
		"phase", phase,
		"err", err.Error(),
		"offset", offset,
		"payload_len", len(body),
		"content_encoding", contentEncoding,
		"content_length_header", contentLengthHeader,
		"content_length", contentLength,
		"before_200b", before,
		"after_200b", after,
	)
}

func snippetAroundOffset(body []byte, offset int64, radius int) (string, string) {
	if len(body) == 0 {
		return "", ""
	}
	// json.SyntaxError offset is 1-based.
	pos := int(offset) - 1
	if pos < 0 {
		pos = 0
	}
	if pos > len(body) {
		pos = len(body)
	}
	start := pos - radius
	if start < 0 {
		start = 0
	}
	end := pos + radius
	if end > len(body) {
		end = len(body)
	}
	before := bytes.TrimSpace(body[start:pos])
	after := bytes.TrimSpace(body[pos:end])
	return string(before), string(after)
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

	const maxProcessListLen = 100
	const maxSoftwareListLen = 1000
	if out, truncated, origLen, err := truncateJSONArray(req.Processes, maxProcessListLen); err != nil {
		return err
	} else if truncated {
		h.logger.Warn("snapshot truncated", "device_id", req.DeviceID, "field", "processes", "orig_len", origLen, "limit", maxProcessListLen)
		req.Processes = out
	}
	if out, truncated, origLen, err := truncateJSONArray(req.SoftwareList, maxSoftwareListLen); err != nil {
		return err
	} else if truncated {
		h.logger.Warn("snapshot truncated", "device_id", req.DeviceID, "field", "software_list", "orig_len", origLen, "limit", maxSoftwareListLen)
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

	// Parse and persist USBEvents in batch.
	if len(req.USBEvents) > 0 {
		entries := make([]store.USBLogEntry, 0, len(req.USBEvents))
		for _, e := range req.USBEvents {
			hostID := strings.TrimSpace(e.DeviceID)
			if hostID == "" {
				hostID = strings.TrimSpace(req.DeviceID)
			}
			if hostID == "" {
				continue
			}
			at := time.Unix(e.Timestamp, 0)
			if e.Timestamp <= 0 {
				at = time.Now()
			}
			action := strings.ToLower(strings.TrimSpace(e.Action))
			originalAction := action
			// Treat initial-state "existing" as "insert" so the frontend can reflect current USB usage.
			if action == "existing" {
				action = "insert"
			}
			entries = append(entries, store.USBLogEntry{
				HostDeviceID: hostID,
				USBID:        strings.TrimSpace(e.USBID),
				VolumeName:   strings.TrimSpace(e.VolumeName),
				Action:       action,
				CreatedAt:    at,
			})
			h.logger.Info("usb event received", "device_id", hostID, "action", action, "usb_id", strings.TrimSpace(e.USBID))
			if action == "insert" && h.store.IsUSBDisabled(ctx, hostID) {
				h.logger.Error(
					"SECURITY ALERT: USB insert/initial-existing detected while policy is disabled",
					"device_id", hostID,
					"usb_id", e.USBID,
					"volume_name", e.VolumeName,
					"event_action", originalAction,
				)
			}
		}
		if h.usbStore != nil && len(entries) > 0 {
			if err := h.usbStore.BatchInsert(saveCtx, entries); err != nil {
				h.logger.Warn("batch insert usb_logs failed", "err", err, "device_id", req.DeviceID, "count", len(entries))
			} else {
				h.logger.Info("usb logs persisted", "source", "direct", "count", len(entries))
				// Realtime notify DeviceDetail to refresh USB audit.
				if h.usbMgr != nil {
					h.usbMgr.BroadcastUSBUpdate(req.DeviceID)
				}
			}
		}
	}

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

	const maxProcessListLen = 100
	const maxSoftwareListLen = 1000
	now := time.Now().Unix()

	partials := make(map[string]*devicePartialUpdate)
	usbEntries := make([]store.USBLogEntry, 0, 16)
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
		case "usb_event", "usb", "usb_audit":
			// Try to determine host device id
			fp := extractFingerprintFromPayload(it.Payload)
			if fp == "" {
				// Fallback keys commonly used for host id
				var obj map[string]json.RawMessage
				if json.Unmarshal(it.Payload, &obj) == nil {
					for _, k := range []string{"host_fingerprint", "hostDeviceID", "host_device_id", "host", "device_id"} {
						if v, ok := obj[k]; ok {
							var s string
							if json.Unmarshal(v, &s) == nil && strings.TrimSpace(s) != "" {
								fp = strings.TrimSpace(s)
								break
							}
						}
					}
				}
			}
			if fp == "" {
				// If batch has a unique device id, use it
				fp = batchDeviceID
			}
			if fp == "" {
				h.logger.Warn("usb_event missing device fingerprint; skipping audit save")
				continue
			}
			// Parse fields
			var obj map[string]any
			if err := json.Unmarshal(it.Payload, &obj); err != nil {
				h.logger.Warn("usb_event payload parse failed", "device_id", fp, "err", err)
				continue
			}
			usbID := strings.TrimSpace(getStringAny(obj["usb_id"]))
			if usbID == "" {
				// Backward-compatible: some agents may send DeviceID for USB device id
				usbID = strings.TrimSpace(getStringAny(firstNonNil(obj["device_id"], obj["DeviceID"])))
			}
			volumeName := strings.TrimSpace(getStringAny(firstNonNil(obj["volume_name"], obj["VolumeName"])))
			action := strings.ToLower(strings.TrimSpace(getStringAny(firstNonNil(obj["action"], obj["Action"]))))
			originalAction := action
			// Treat initial-state "existing" as "insert" so the frontend can reflect current USB usage.
			if action == "existing" {
				action = "insert"
			}
			ts := time.Now().Unix()
			tmpTs := firstNonNil(obj["timestamp"], obj["Timestamp"])
			srcTs := firstNonNil(obj["ts"], tmpTs)
			if v := strings.TrimSpace(getStringAny(srcTs)); v != "" {
				// Accept RFC3339 or unix seconds
				if t, err := time.Parse(time.RFC3339, v); err == nil {
					ts = t.Unix()
				} else if n, err2 := strconv.ParseInt(v, 10, 64); err2 == nil && n > 0 {
					ts = n
				}
			}
			if h.usbMgr != nil {
				h.usbMgr.SaveAudit(ctx, fp, usbID, volumeName, ts, action)
			} else if err := h.store.SaveUSBAuditLog(ctx, fp, usbID, volumeName, ts, action); err != nil {
				h.logger.Warn("save usb audit failed", "device_id", fp, "err", err)
			}
			usbEntries = append(usbEntries, store.USBLogEntry{
				HostDeviceID: fp,
				USBID:        usbID,
				VolumeName:   volumeName,
				Action:       action,
				CreatedAt:    time.Unix(ts, 0),
			})
			h.logger.Info("usb event received", "device_id", fp, "action", action, "usb_id", usbID)
			// Real-time alert: insert detected while USB disabled
			if action == "insert" && h.store.IsUSBDisabled(ctx, fp) {
				h.logger.Error(
					"SECURITY ALERT: USB insert/initial-existing detected while policy is disabled",
					"device_id", fp,
					"usb_id", usbID,
					"volume_name", volumeName,
					"event_action", originalAction,
				)
				go h.sendUSBDisabledAlert(fp, usbID, volumeName, ts)
			}

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
			// Optional debug: verify host_metrics contains per-NIC drop counters.
			if os.Getenv("DEBUG_REPORT_HOSTMETRICS_NETWORK") == "1" {
				var dbg struct {
					NetworkInterfaces []struct {
						DropIn  uint64 `json:"drop_in"`
						DropOut uint64 `json:"drop_out"`
					} `json:"network_interfaces"`
				}
				if err := json.Unmarshal(it.Payload, &dbg); err == nil && len(dbg.NetworkInterfaces) > 0 {
					first := dbg.NetworkInterfaces[0]
					h.logger.Info("debug report host_metrics network drops",
						"device_id", fp,
						"interfaces", len(dbg.NetworkInterfaces),
						"first_drop_in", first.DropIn,
						"first_drop_out", first.DropOut,
					)
				} else if err != nil {
					h.logger.Info("debug report host_metrics network drops parse failed", "device_id", fp, "err", err.Error())
				} else {
					h.logger.Info("debug report host_metrics network drops missing interfaces", "device_id", fp)
				}
			}

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

			out, truncated, origLen, err := truncateJSONArray(processesRaw, maxProcessListLen)
			if err != nil {
				return err
			}
			if truncated {
				h.logger.Warn("snapshot truncated", "device_id", fp, "field", "processes", "orig_len", origLen, "limit", maxProcessListLen)
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

			out, truncated, origLen, err := truncateJSONArray(softwareRaw, maxSoftwareListLen)
			if err != nil {
				return err
			}
			if truncated {
				h.logger.Warn("snapshot truncated", "device_id", fp, "field", "software_list", "orig_len", origLen, "limit", maxSoftwareListLen)
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

	saveCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if h.usbStore != nil && len(usbEntries) > 0 {
		if err := h.usbStore.BatchInsert(saveCtx, usbEntries); err != nil {
			h.logger.Warn("batch insert usb_logs failed", "err", err, "count", len(usbEntries))
		} else {
			h.logger.Info("usb logs persisted", "source", "batch", "count", len(usbEntries))
			if h.usbMgr != nil && batchDeviceID != "" {
				h.usbMgr.BroadcastUSBUpdate(batchDeviceID)
			}
		}
	}

	if len(partials) == 0 {
		// Batch without process/software updates is still OK.
		return nil
	}

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
			if hasCritical3389Public(p.securitySnapshotRaw) {
				go h.sendCriticalAlert(deviceID, p.securitySnapshotRaw)
			}
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

func hasCritical3389Public(raw []byte) bool {
	var obj map[string]any
	if json.Unmarshal(raw, &obj) != nil {
		return false
	}
	listening, _ := obj["listening"].([]any)
	if len(listening) == 0 {
		listening, _ = obj["Listening"].([]any)
	}
	for _, it := range listening {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		scopeRaw := m["scope"]
		if scopeRaw == nil {
			scopeRaw = m["Scope"]
		}
		scope := strings.ToLower(strings.TrimSpace(fmt.Sprintf("%v", scopeRaw)))
		if scope != "public" {
			continue
		}
		portRaw := m["port"]
		if portRaw == nil {
			portRaw = m["Port"]
		}
		p, _ := strconv.ParseInt(strings.TrimSpace(fmt.Sprintf("%v", portRaw)), 10, 64)
		if p == 3389 {
			return true
		}
	}
	return false
}

func (h *ReportHandler) sendCriticalAlert(deviceID string, raw []byte) {
	webhook := strings.TrimSpace(os.Getenv("ALERT_WEBHOOK_URL"))
	if webhook == "" {
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"text":      "[AutoAlert] 检测到公网暴露 CRITICAL 端口 3389",
		"device_id": deviceID,
		"ts":        time.Now().Unix(),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhook, bytes.NewReader(payload))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.logger.Warn("auto alert send failed", "device_id", deviceID, "err", err)
		return
	}
	_ = resp.Body.Close()
}

func (h *ReportHandler) sendUSBDisabledAlert(deviceID, usbID, volumeName string, ts int64) {
	webhook := strings.TrimSpace(os.Getenv("ALERT_WEBHOOK_URL"))
	if webhook == "" {
		return
	}
	body := map[string]any{
		"text":        "[SecurityAlert] USB 已禁用，但检测到插入尝试",
		"device_id":   deviceID,
		"usb_id":      usbID,
		"volume_name": volumeName,
		"ts":          ts,
	}
	payload, _ := json.Marshal(body)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhook, bytes.NewReader(payload))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.logger.Warn("usb disabled alert send failed", "device_id", deviceID, "err", err)
		return
	}
	_ = resp.Body.Close()
}

// local helpers (mirroring store helpers) to parse arbitrary JSON objects
func firstNonNil(a, b any) any {
	if a != nil {
		return a
	}
	return b
}
func getStringAny(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	default:
		return ""
	}
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
