package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/qinyilin/go-agent/internal/buffer"
	"github.com/qinyilin/go-agent/internal/collector"
	"github.com/qinyilin/go-agent/internal/models"
	internalservice "github.com/qinyilin/go-agent/internal/service"
	"github.com/qinyilin/go-agent/internal/transport"

	kservice "github.com/kardianos/service"
)

func hasFlagArg(args []string, longName string) bool {
	want := "-" + strings.TrimSpace(longName)
	want2 := "--" + strings.TrimSpace(longName)
	for i := 0; i < len(args); i++ {
		a := strings.TrimSpace(args[i])
		if a == want || a == want2 || strings.HasPrefix(a, want+"=") || strings.HasPrefix(a, want2+"=") {
			return true
		}
	}
	return false
}

// Build-time version info. Override via:
//
//	go build -ldflags "-X 'github.com/qinyilin/go-agent/cmd/agent.Version=1.2.3' ..."
var (
	Version   = "dev"
	BuildTime = "unknown"
	GitCommit = "unknown"
)

func main() {
	// Auto runtime args: allow launching exe directly without manual data/log flags.
	// Users can still override by explicitly passing flags.
	if exePath, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exePath)
		if !hasFlagArg(os.Args[1:], "data-dir") {
			os.Args = append(os.Args, "-data-dir", exeDir)
		}
	}
	if !hasFlagArg(os.Args[1:], "log-level") && !hasFlagArg(os.Args[1:], "debug") {
		os.Args = append(os.Args, "-log-level", "debug")
	}

	var (
		debug              = flag.Bool("debug", false, "run agent in debug mode (foreground) and stop on Ctrl+C")
		agentVersion       = flag.String("version", Version, "agent runtime version reported in metrics and used for auto-update compare")
		logLevel           = flag.String("log-level", "info", "log level: debug|info|warn|error")
		collectEvery       = flag.Duration("collect-every", 2*time.Second, "interval for simulated device collection")
		stopTimeout        = flag.Duration("stop-timeout", 10*time.Second, "timeout waiting for graceful stop")
		dataDir            = flag.String("data-dir", "", "data directory for cache persistence (default: executable directory)")
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

	// Default data directory to executable directory so running from any cwd is stable.
	if strings.TrimSpace(*dataDir) == "" {
		if exePath, err := os.Executable(); err == nil {
			*dataDir = filepath.Dir(exePath)
		} else {
			// Fallback to current directory when executable path is unavailable.
			*dataDir = "."
		}
	}

	// `-debug` is intended to enable debug logging during troubleshooting.
	// If user didn't explicitly set `--log-level`, switch it to `debug`.
	if *debug && strings.EqualFold(strings.TrimSpace(*logLevel), "info") {
		*logLevel = "debug"
	}

	logDir := filepath.Join(*dataDir, "logs")
	logger, closeLogger, err := internalservice.NewLogger(*logLevel, logDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to init logger: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		if closeLogger != nil {
			_ = closeLogger()
		}
	}()

	if strings.TrimSpace(*agentVersion) == "" {
		*agentVersion = Version
	}
	logger.Info("agent starting",
		"version", *agentVersion,
		"buildTime", BuildTime,
		"gitCommit", GitCommit,
		"dataDir", *dataDir,
	)
	logger.Debug("debug logging is active", "logLevel", strings.TrimSpace(*logLevel), "debugFlag", *debug)
	logger.Info("effective log level", "logLevel", strings.TrimSpace(*logLevel), "debugFlag", *debug)

	dbDir := filepath.Join(*dataDir, "db")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "failed to create db dir: %v\n", err)
		os.Exit(1)
	}
	dbPath := filepath.Join(dbDir, "agent_cache.db")
	cache, err := buffer.NewDataBuffer(dbPath)
	if err != nil {
		logger.Error("failed to create data buffer", "err", err, "dbPath", dbPath)
		os.Exit(1)
	}
	defer func() {
		_ = cache.Close()
	}()

	engine := internalservice.NewSimCollector(logger, *collectEvery, cache, internalservice.WithAgentVersion(*agentVersion))
	runForeground := *debug || (runtime.GOOS == "windows" && kservice.Interactive())
	if runForeground {
		// In debug mode, retry hardware scan frequently so hardware_details shows up quickly
		// even if the startup scan fails due to transient Windows/WMI/permission issues.
		engine = internalservice.NewSimCollector(
			logger,
			*collectEvery,
			cache,
			internalservice.WithAgentVersion(*agentVersion),
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
		// Retry a few times so transient startup/WMI hiccups do not leave USB events without host ownership.
		for i := 0; i < 3 && strings.TrimSpace(controlDeviceID) == ""; i++ {
			hw, err := collector.NewHardwareCollector().CollectWithContext(context.Background())
			if err == nil && strings.TrimSpace(hw.Fingerprint) != "" {
				controlDeviceID = strings.TrimSpace(hw.Fingerprint)
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		if strings.TrimSpace(controlDeviceID) == "" {
			// Last-resort fallback to keep audit rows attributable; server can still display records.
			if hn, err := os.Hostname(); err == nil && strings.TrimSpace(hn) != "" {
				controlDeviceID = "host-" + strings.TrimSpace(hn)
				logger.Warn("hardware fingerprint unavailable at startup, fallback to hostname for control/usb device_id", "device_id", controlDeviceID)
			}
		}
	}
	updateRunner := internalservice.NewUpdateRunner(
		logger,
		*agentVersion,
		*apiURL,
		controlDeviceID,
		*serviceName,
		*caCertPath,
		*clientCert,
		*clientKey,
		*insecureSkipVerify,
	)
	controlRunner := internalservice.NewControlRunnerWithUpdater(
		logger,
		controlDeviceID,
		*apiURL,
		*controlToken,
		splitCSV(*allowedScripts),
		splitCSV(*allowedServices),
		updateRunner,
	)
	wrapper := internalservice.NewAgentService(
		engine,
		logger,
		internalservice.WithStopTimeout(*stopTimeout),
		internalservice.WithDispatcher(dispatcher),
		internalservice.WithControlRunner(controlRunner),
		internalservice.WithUpdateRunner(updateRunner),
	)

	// USB monitor: run asynchronously and push events immediately via dispatcher.
	{
		logger.Debug("[USBDBG] usb monitor engine starting")
		// Configure runtime policy fetcher for fine-grained allowlist.
		internalservice.ConfigureUSBPolicyFetcher(*apiURL, controlDeviceID)
		// Suppress duplicate bursts very lightly; keep near-realtime insert/remove visibility.
		const usbEventSuppressWindow = 500 * time.Millisecond
		var usbSupMu sync.Mutex
		lastUSBProcessed := make(map[string]time.Time, 16)

		usbCtx, cancel := context.WithCancel(context.Background())
		_ = cancel // attached to service lifecycle via wrapper; keep for future wiring if needed
		// Use a short poll interval so remove events are reflected quickly in usb_logs/UI.
		if err := internalservice.StartUSBMonitor(usbCtx, logger, 2*time.Second, func(e models.USBEvent) {
			// Fill host id for audit ownership.
			e.DeviceID = strings.TrimSpace(controlDeviceID)
			if e.DeviceID == "" {
				// Guardrail: never send unowned USB audit records.
				if hn, err := os.Hostname(); err == nil && strings.TrimSpace(hn) != "" {
					e.DeviceID = "host-" + strings.TrimSpace(hn)
				}
			}
			if e.Timestamp <= 0 {
				e.Timestamp = time.Now().Unix()
			}

			// Suppress repeated events for the same (host + usb + action) to avoid dispatch storms.
			actionKey := strings.ToLower(strings.TrimSpace(e.Action))
			hostID := strings.TrimSpace(e.DeviceID)
			usbID := strings.ToUpper(strings.TrimSpace(e.USBID))
			if usbID == "" {
				usbID = "UNKNOWN"
			}
			key := hostID + "|" + usbID + "|" + actionKey
			now := time.Now()
			usbSupMu.Lock()
			if lastAt, ok := lastUSBProcessed[key]; ok && now.Sub(lastAt) < usbEventSuppressWindow {
				usbSupMu.Unlock()
				return
			}
			lastUSBProcessed[key] = now
			usbSupMu.Unlock()
			logger.Debug("[USBDBG] usb event captured",
				"device_id", e.DeviceID,
				"action", e.Action,
				"usb_id", e.USBID,
				"volume_name", e.VolumeName,
				"timestamp", e.Timestamp,
			)

			payload, err := json.Marshal(e)
			if err != nil {
				logger.Warn("marshal usb event failed", "err", err)
				return
			}
			// Realtime fast path: post USB event immediately to backend report endpoint.
			// Keep local cache path as fallback to tolerate transient network failures.
			func() {
				reqBody, merr := json.Marshal(map[string]any{
					"device_id":   e.DeviceID,
					"usb_events":  []models.USBEvent{e},
					"reported_at": time.Now().Unix(),
				})
				if merr != nil {
					logger.Warn("marshal usb direct report failed", "err", merr)
					return
				}
				cctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
				defer cancel()
				status, _, perr := httpClient.PostJSON(cctx, reqBody)
				if perr != nil || status < 200 || status >= 300 {
					if perr != nil {
						logger.Warn("usb direct report failed, fallback to cache", "err", perr)
					} else {
						logger.Warn("usb direct report rejected, fallback to cache", "status", status)
					}
					return
				}
				logger.Debug("[USBDBG] usb direct report sent", "device_id", e.DeviceID, "usb_id", e.USBID, "action", e.Action, "status", status)
				// Direct post succeeded; no need to duplicate through cache.
				payload = nil
			}()
			if payload == nil {
				return
			}
			if err := cache.Save("usb_event", payload); err != nil {
				logger.Warn("cache usb event failed", "err", err)
				return
			}
			logger.Debug("[USBDBG] usb event cached for dispatcher", "device_id", e.DeviceID, "usb_id", e.USBID, "action", e.Action)
			// Attempt immediate dispatch
			dispatcher.DispatchNow(context.Background())
		}); err != nil {
			logger.Error("usb monitor engine failed to start", "err", err)
		}
	}

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
