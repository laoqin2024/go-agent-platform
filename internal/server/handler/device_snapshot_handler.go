package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/qinyilin/go-agent/internal/collector"
	"github.com/qinyilin/go-agent/internal/server/store"
)

type DeviceSnapshotHandler struct {
	store  *store.RedisSnapshotStore
	logger *slog.Logger
}

func NewDeviceSnapshotHandler(snapshotStore *store.RedisSnapshotStore, logger *slog.Logger) *DeviceSnapshotHandler {
	return &DeviceSnapshotHandler{
		store:  snapshotStore,
		logger: logger,
	}
}

// GET /api/v1/device/:device_id/snapshot
//
// GetDeviceDetail 获取单个设备的实时快照
// @Summary      获取设备详情
// @Param        device_id  path  string  true  "设备唯一指纹"
// @Success      200  {object}  DeviceSnapshotResponse
// @Failure      400  {object}  map[string]string "{"error":"device_id is required"}"
// @Failure      404  {object}  map[string]string "{"error":"snapshot not found"}"
// @Failure      500  {object}  map[string]string "{"error":"redis load failed"}"
// @Router       /device/{device_id}/snapshot [get]
func (h *DeviceSnapshotHandler) HandleSnapshot(c *gin.Context) {
	deviceID := c.Param("device_id")
	if deviceID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "device_id is required"})
		return
	}

	snap, ok, err := h.store.LoadSnapshot(c.Request.Context(), deviceID)
	if err != nil {
		h.logger.Error("load snapshot failed", "err", err, "device_id", deviceID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "redis load failed"})
		return
	}
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "snapshot not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"device_id":     deviceID,
		"processes":     json.RawMessage(snap.Processes),
		"software_list": json.RawMessage(snap.SoftwareList),
		"host_metrics":        json.RawMessage(snap.HostMetrics),
		"hardware_details":    json.RawMessage(snap.HardwareDetails),
		"software_inventory":  json.RawMessage(snap.SoftwareInventory),
		"process_snapshot":    json.RawMessage(snap.ProcessSnapshot),
		"service_snapshot":    json.RawMessage(snap.ServiceSnapshot),
		"network_connections": json.RawMessage(snap.NetworkConnections),
		"security_snapshot":   json.RawMessage(snap.SecuritySnapshot),
		"updated_at":    snap.UpdatedAtSec,
	})
}

// DeviceSnapshotResponse 用于 Swagger 文档，描述设备详情返回结构
type DeviceSnapshotResponse struct {
	DeviceID           string                    `json:"device_id"`
	Processes          []collector.ProcessStat   `json:"processes"`
	SoftwareList       []map[string]any          `json:"software_list"`
	HostMetrics        collector.HostMetrics     `json:"host_metrics"`
	HardwareDetails    map[string]any            `json:"hardware_details"`
	SoftwareInventory  []map[string]any          `json:"software_inventory"`
	ProcessSnapshot    map[string]any            `json:"process_snapshot"`
	ServiceSnapshot    []collector.ServiceStat   `json:"service_snapshot"`
	NetworkConnections []map[string]any          `json:"network_connections"`
	SecuritySnapshot   map[string]any            `json:"security_snapshot"`
	UpdatedAt          int64                     `json:"updated_at"`
}
