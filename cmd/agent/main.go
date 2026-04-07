package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/qinyilin/go-agent/internal/buffer"
	"github.com/qinyilin/go-agent/internal/collector"
	internalservice "github.com/qinyilin/go-agent/internal/service"
	"github.com/qinyilin/go-agent/internal/transport"

	kservice "github.com/kardianos/service"
)

// Build-time version info. Override via:
//
//	go build -ldflags "-X 'github.com/qinyilin/go-agent/cmd/agent.Version=1.2.3' ..."
var (
	Version   = "dev"
	BuildTime = "unknown"
	GitCommit = "unknown"
)

func main() {
	var (
		debug              = flag.Bool("debug", false, "run agent in debug mode (foreground) and stop on Ctrl+C")
		logLevel           = flag.String("log-level", "info", "log level: debug|info|warn|error")
		collectEvery       = flag.Duration("collect-every", 2*time.Second, "interval for simulated device collection")
		stopTimeout        = flag.Duration("stop-timeout", 10*time.Second, "timeout waiting for graceful stop")
		dataDir            = flag.String("data-dir", "./data", "data directory for cache persistence")
		apiURL             = flag.String("api-url", "http://192.168.8.168:8080/ingest", "backend ingest URL")
		caCertPath         = flag.String("ca-cert", "./certs/ca.pem", "CA root certificate path (PEM)")
		clientCert         = flag.String("client-cert", "./certs/client.pem", "client certificate path (PEM)")
		clientKey          = flag.String("client-key", "./certs/client-key.pem", "client private key path (PEM)")
		insecureSkipVerify = flag.Bool("insecure-skip-verify", true, "demo mode: skip TLS server certificate verification")
		serviceName        = flag.String("name", "go-agent", "kardianos/service service name")
		displayName        = flag.String("display-name", "Go Agent", "service display name")
		description        = flag.String("description", "Cross-platform IT device data collection agent", "service description")
		deviceID           = flag.String("device-id", "", "control channel device id, default hardware fingerprint")
		controlToken       = flag.String("control-token", "", "control channel shared token")
		allowedScripts     = flag.String("control-allowed-scripts", "clean_cache,restart_monitor", "allowed custom scripts")
		allowedServices    = flag.String("control-allowed-services", "go-agent", "allowed restart service names")
	)
	flag.Parse()

	logger := internalservice.NewLogger(*logLevel)

	logger.Info("agent starting",
		"version", Version,
		"buildTime", BuildTime,
		"gitCommit", GitCommit,
		"dataDir", *dataDir,
	)

	dbPath := filepath.Join(*dataDir, "agent_cache.db")
	cache, err := buffer.NewDataBuffer(dbPath)
	if err != nil {
		logger.Error("failed to create data buffer", "err", err, "dbPath", dbPath)
		os.Exit(1)
	}
	defer func() {
		_ = cache.Close()
	}()

	engine := internalservice.NewSimCollector(logger, *collectEvery, cache)
	runForeground := *debug || (runtime.GOOS == "windows" && kservice.Interactive())
	if runForeground {
		// In debug mode, retry hardware scan frequently so hardware_details shows up quickly
		// even if the startup scan fails due to transient Windows/WMI/permission issues.
		engine = internalservice.NewSimCollector(
			logger,
			*collectEvery,
			cache,
			internalservice.WithHardwareScanEvery(2*time.Minute),
			internalservice.WithHardwareRetryAfter(30*time.Second),
		)
	}

	httpClient, err := transport.NewHttpClient(
		*apiURL,
		*caCertPath,
		*clientCert,
		*clientKey,
		*insecureSkipVerify,
		transport.WithLogger(logger),
	)
	if err != nil {
		// If mTLS config is missing, allow running in demo mode via insecureSkipVerify=true.
		logger.Error("failed to create mTLS HttpClient", "err", err)
		os.Exit(1)
	}

	dispatcher := internalservice.NewDataDispatcher(logger, cache, httpClient)
	// In foreground mode, increase dispatch throughput so low-frequency inventory items
	// (hardware_details/software_inventory) are not starved by high-frequency metrics.
	if runForeground {
		dispatcher = internalservice.NewDataDispatcher(
			logger,
			cache,
			httpClient,
			internalservice.WithDispatcherInterval(2*time.Second),
			internalservice.WithDispatcherBatchLimit(200),
			internalservice.WithDispatcherRequestTTL(10*time.Second),
		)
	}
	controlDeviceID := *deviceID
	if controlDeviceID == "" {
		hw, err := collector.NewHardwareCollector().CollectWithContext(context.Background())
		if err == nil && hw.Fingerprint != "" {
			controlDeviceID = hw.Fingerprint
		}
	}
	controlRunner := internalservice.NewControlRunner(
		logger,
		controlDeviceID,
		*apiURL,
		*controlToken,
		splitCSV(*allowedScripts),
		splitCSV(*allowedServices),
	)
	wrapper := internalservice.NewAgentService(
		engine,
		logger,
		internalservice.WithStopTimeout(*stopTimeout),
		internalservice.WithDispatcher(dispatcher),
		internalservice.WithControlRunner(controlRunner),
	)

	if runForeground {
		logger.Info("foreground mode: starting agent service (will stop on Ctrl+C/window close)")
		if err := wrapper.Start(nil); err != nil {
			logger.Error("failed to start service", "err", err)
			os.Exit(1)
		}

		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
		<-sigCh

		logger.Info("foreground mode: stop signal received")
		if err := wrapper.Stop(nil); err != nil {
			logger.Error("failed to stop service", "err", err)
			os.Exit(1)
		}
		logger.Info("foreground mode: agent stopped cleanly")
		return
	}

	svcConfig := &kservice.Config{
		Name:        *serviceName,
		DisplayName: *displayName,
		Description: *description,
	}

	kardService, err := kservice.New(wrapper, svcConfig)
	if err != nil {
		logger.Error("failed to create kardianos service", "err", err)
		os.Exit(1)
	}

	// Run blocks until the service manager stops it.
	if err := kardService.Run(); err != nil {
		logger.Error("service terminated with error", "err", err)
		os.Exit(1)
	}
}

var _ = slog.LevelInfo

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		v := strings.TrimSpace(p)
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}
