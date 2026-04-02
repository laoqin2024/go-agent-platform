package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/qinyilin/go-agent/internal/server/handler"
	"github.com/qinyilin/go-agent/internal/server/router"
	"github.com/qinyilin/go-agent/internal/server/store"
	"github.com/qinyilin/go-agent/internal/server/ws"
)

func main() {
	var (
		addr          = flag.String("addr", "0.0.0.0:8080", "server listen address, e.g. 0.0.0.0:8080")
		redisAddr     = flag.String("redis-addr", "127.0.0.1:6379", "redis address")
		redisPassword = flag.String("redis-password", "", "redis password (optional)")
		redisDB       = flag.Int("redis-db", 0, "redis db index")
		redisTTL      = flag.Duration("snapshot-ttl", 24*time.Hour, "Redis TTL for each device snapshot (default 24h)")
		readTimeout   = flag.Duration("read-timeout", 10*time.Second, "http read timeout")
		writeTimeout  = flag.Duration("write-timeout", 30*time.Second, "http write timeout")
		idleTimeout   = flag.Duration("idle-timeout", 60*time.Second, "http idle timeout")
	)
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	redisClient := store.NewRedisClient(store.RedisClientConfig{
		Addr:     *redisAddr,
		Password: *redisPassword,
		DB:       *redisDB,
	})
	snapshotStore := store.NewRedisSnapshotStore(redisClient, *redisTTL)

	wsHub := ws.NewHub()
	reportHandler := handler.NewReportHandler(snapshotStore, wsHub, logger)
	wsHandler := handler.NewWSHandler(snapshotStore, wsHub, logger)
	snapshotHandler := handler.NewDeviceSnapshotHandler(snapshotStore, logger)
	devicesHandler := handler.NewDevicesHandler(snapshotStore)

	r := router.NewRouter(router.RouterConfig{
		ReportHandler:   reportHandler,
		WSHandler:       wsHandler,
		SnapshotHandler: snapshotHandler,
		DevicesHandler:  devicesHandler,
	})

	httpServer := &http.Server{
		Addr:              *addr,
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       *readTimeout,
		WriteTimeout:      *writeTimeout,
		IdleTimeout:       *idleTimeout,
	}

	go func() {
		logger.Info("server listening", "addr", *addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server exited", "err", err)
		}
	}()

	// Graceful shutdown.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	logger.Info("shutdown requested")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(ctx)
	logger.Info("shutdown complete")
}
