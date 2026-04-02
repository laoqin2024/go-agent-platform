package router

import (
	"net/http"
	"os"
	"strings"
	"time"

	cors "github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	serverHandler "github.com/qinyilin/go-agent/internal/server/handler"
	"github.com/qinyilin/go-agent/internal/server/middleware"
)

type RouterConfig struct {
	ReportHandler   *serverHandler.ReportHandler
	WSHandler       *serverHandler.WSHandler
	SnapshotHandler *serverHandler.DeviceSnapshotHandler
	DevicesHandler  *serverHandler.DevicesHandler
}

func NewRouter(cfg RouterConfig) *gin.Engine {
	if cfg.ReportHandler == nil || cfg.WSHandler == nil || cfg.SnapshotHandler == nil || cfg.DevicesHandler == nil {
		panic("router: nil handlers")
	}

	r := gin.New()
	r.Use(middleware.RequestID())
	r.Use(middleware.Recover())

	// CORS for browser-based frontend (e.g. Vite dev server).
	allowedOrigins := []string{"http://localhost:5173"}
	if v := strings.TrimSpace(os.Getenv("CORS_ORIGINS")); v != "" {
		allowedOrigins = strings.Split(v, ",")
		for i := range allowedOrigins {
			allowedOrigins[i] = strings.TrimSpace(allowedOrigins[i])
		}
	}
	r.Use(cors.New(cors.Config{
		AllowOrigins:     allowedOrigins,
		AllowMethods:     []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Authorization"},
		AllowCredentials: false,
		MaxAge:           12 * time.Hour,
	}))

	// Basic health for orchestration.
	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// Backward-compatible ingest endpoint.
	// The agent's default demo/debug URL commonly posts to "/ingest".
	// We reuse the same batch parsing logic as POST /api/v1/report.
	r.POST("/ingest", cfg.ReportHandler.HandleReport)

	api := r.Group("/api/v1")
	{
		api.POST("/report", cfg.ReportHandler.HandleReport)
		api.GET("/device/:device_id/snapshot", cfg.SnapshotHandler.HandleSnapshot)
		api.GET("/devices", cfg.DevicesHandler.HandleDevices)
	}

	// Subscribe by device_id.
	// Example: ws://host/ws/{device_id}
	r.GET("/ws/:device_id", cfg.WSHandler.HandleWS)

	return r
}
