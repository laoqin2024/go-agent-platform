package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
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
