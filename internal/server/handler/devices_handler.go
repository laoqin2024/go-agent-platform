package handler

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/qinyilin/go-agent/internal/server/store"
)

type DevicesHandler struct {
	store *store.RedisSnapshotStore
}

func NewDevicesHandler(snapshotStore *store.RedisSnapshotStore) *DevicesHandler {
	return &DevicesHandler{store: snapshotStore}
}

// GET /api/v1/devices
// Returns devices enriched with lightweight meta for UI.
func (h *DevicesHandler) HandleDevices(c *gin.Context) {
	// Consider "online" if updated within last 90s (works well with 30s ping + typical agent intervals).
	devices, err := h.store.ListDevices(c.Request.Context(), 90*time.Second)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list devices"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"devices": devices})
}

