package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/qinyilin/go-agent/internal/server/store"
	"github.com/qinyilin/go-agent/internal/server/ws"
)

type WSHandler struct {
	store  *store.RedisSnapshotStore
	wsHub  *ws.Hub
	logger *slog.Logger
}

func NewWSHandler(snapshotStore *store.RedisSnapshotStore, wsHub *ws.Hub, logger *slog.Logger) *WSHandler {
	return &WSHandler{
		store:  snapshotStore,
		wsHub:  wsHub,
		logger: logger,
	}
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  2048,
	WriteBufferSize: 2048,
	// For internal dashboards you can allow all origins; tighten in production.
	CheckOrigin: func(r *http.Request) bool { return true },
}

// GET /ws/:device_id
func (h *WSHandler) HandleWS(c *gin.Context) {
	deviceID := c.Param("device_id")
	if deviceID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "device_id is required"})
		return
	}

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		h.logger.Warn("ws upgrade failed", "err", err, "device_id", deviceID)
		return
	}
	remoteAddr := c.Request.RemoteAddr

	client := &ws.WSClient{Conn: conn}
	h.wsHub.AddWSClient(deviceID, client)
	h.logger.Debug("ws client connected", "device_id", deviceID, "remote_addr", remoteAddr)

	// On connect: best-effort push latest snapshot from Redis (async, so the WS handshake returns instantly).
	go func() {
		if snap, ok, err := h.store.LoadSnapshot(context.Background(), deviceID); err == nil && ok {
			if b, err := json.Marshal(snap); err == nil {
				_ = client.WriteMessage(websocket.TextMessage, b)
			}
		}
	}()

	// Keep the connection alive; we read in a loop only to detect disconnect.
	conn.SetReadLimit(1024 * 64)
	_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error {
		_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	stopCh := make(chan struct{})
	var cleanupOnce sync.Once
	cleanup := func(reason string) {
		cleanupOnce.Do(func() {
			close(stopCh)
			h.wsHub.RemoveClient(deviceID, client)
			_ = client.Close()
			h.logger.Debug("ws client disconnected", "device_id", deviceID, "remote_addr", remoteAddr, "reason", reason)
		})
	}

	go func() {
		defer cleanup("read_loop_exit")
		// Read loop: discard messages. Clients might send pings; we just consume.
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				h.logger.Debug("ws read loop exit", "device_id", deviceID, "err", err)
				break
			}
		}
	}()

	go func() {
		// Heartbeat: send PingMessage every 30s; close if we haven't received Pong within 60s.
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				if err := client.Ping(); err != nil {
					cleanup("ping_failed")
					return
				}
			}
		}
	}()

	// IMPORTANT: After Upgrade the connection is hijacked by Gorilla WS.
	// Do NOT write any HTTP status/headers via gin; simply return to finish the handler.
	return
}
