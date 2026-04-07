package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type ControlRunner struct {
	logger          *slog.Logger
	deviceID        string
	apiURL          string
	token           string
	allowedScripts  map[string]struct{}
	allowedServices map[string]struct{}
	updater         interface{ TriggerNow() }
}

type controlMessage struct {
	CommandID string   `json:"command_id"`
	Command   string   `json:"command"`
	Service   string   `json:"service,omitempty"`
	Script    string   `json:"script,omitempty"`
	Args      []string `json:"args,omitempty"`
	Token     string   `json:"token"`
}

type executionResult struct {
	Type        string `json:"type"`
	CommandID   string `json:"command_id"`
	ExitCode    int    `json:"exit_code"`
	StdoutBrief string `json:"stdout_brief,omitempty"`
}

type executionProgress struct {
	Type        string `json:"type"`
	CommandID   string `json:"command_id"`
	StdoutBrief string `json:"stdout_brief,omitempty"`
}

func NewControlRunner(logger *slog.Logger, deviceID, apiURL, token string, allowedScripts, allowedServices []string) *ControlRunner {
	toSet := func(items []string, defaults []string) map[string]struct{} {
		out := make(map[string]struct{}, len(items))
		src := items
		if len(src) == 0 {
			src = defaults
		}
		for _, s := range src {
			v := strings.ToLower(strings.TrimSpace(s))
			if v != "" {
				out[v] = struct{}{}
			}
		}
		return out
	}
	return &ControlRunner{
		logger:          logger,
		deviceID:        strings.TrimSpace(deviceID),
		apiURL:          strings.TrimSpace(apiURL),
		token:           strings.TrimSpace(token),
		allowedScripts:  toSet(allowedScripts, []string{"clean_cache", "restart_monitor"}),
		allowedServices: toSet(allowedServices, []string{"go-agent"}),
	}
}

func NewControlRunnerWithUpdater(
	logger *slog.Logger,
	deviceID, apiURL, token string,
	allowedScripts, allowedServices []string,
	updater interface{ TriggerNow() },
) *ControlRunner {
	r := NewControlRunner(logger, deviceID, apiURL, token, allowedScripts, allowedServices)
	r.updater = updater
	return r
}

func (r *ControlRunner) Run(ctx context.Context) {
	if r.deviceID == "" || r.apiURL == "" || r.token == "" {
		r.logger.Warn("control runner disabled: missing deviceID/apiURL/token")
		return
	}
	backoff := time.Second
	const maxBackoff = 60 * time.Second
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		err := r.runOnce(ctx)
		if err == nil || errors.Is(err, context.Canceled) {
			return
		}
		wait := backoffWithJitter(backoff)
		r.logger.Warn("control runner disconnected", "err", err, "retry_in", wait.String())
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

func (r *ControlRunner) runOnce(ctx context.Context) error {
	wsURL, err := r.buildWSURL()
	if err != nil {
		return err
	}
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return err
	}
	defer conn.Close()
	r.logger.Info("control channel connected", "device_id", r.deviceID)

	var writeMu sync.Mutex
	safeWriteJSON := func(v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return conn.WriteMessage(websocket.TextMessage, b)
	}
	safePing := func() error {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return conn.WriteControl(websocket.PingMessage, []byte("agent-heartbeat"), time.Now().Add(10*time.Second))
	}

	_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	conn.SetPingHandler(func(appData string) error {
		_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(10*time.Second))
	})
	conn.SetPongHandler(func(string) error {
		_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		return nil
	})

	stopPing := make(chan struct{})
	defer close(stopPing)
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stopPing:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := safePing(); err != nil {
					return
				}
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		_, data, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		var msg controlMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(msg.Command), "force_update") {
			_ = safeWriteJSON(executionProgress{
				Type:        "execution_progress",
				CommandID:   strings.TrimSpace(msg.CommandID),
				StdoutBrief: "正在更新...",
			})
		}
		exitCode, out := r.handleCommand(ctx, msg)
		_ = safeWriteJSON(executionResult{
			Type:        "execution_result",
			CommandID:   strings.TrimSpace(msg.CommandID),
			ExitCode:    exitCode,
			StdoutBrief: trimBrief(out, 240),
		})
	}
}

func (r *ControlRunner) handleCommand(ctx context.Context, msg controlMessage) (int, string) {
	if strings.TrimSpace(msg.Token) == "" || msg.Token != r.token {
		return 1, "invalid token"
	}
	cmd := strings.ToLower(strings.TrimSpace(msg.Command))
	switch cmd {
	case "custom_script":
		script := strings.ToLower(strings.TrimSpace(msg.Script))
		if _, ok := r.allowedScripts[script]; !ok {
			return 1, "script is not in whitelist"
		}
		return r.execSafeScript(ctx, script, msg.Args)
	case "restart_service":
		service := strings.ToLower(strings.TrimSpace(msg.Service))
		if _, ok := r.allowedServices[service]; !ok {
			return 1, "service is not in whitelist"
		}
		return r.restartService(ctx, service)
	case "shutdown":
		return 1, "shutdown disabled by policy"
	case "force_update":
		if r.updater == nil {
			return 1, "update runner unavailable"
		}
		r.updater.TriggerNow()
		return 0, "正在更新..."
	default:
		return 1, "unsupported command"
	}
}

func (r *ControlRunner) execSafeScript(ctx context.Context, script string, args []string) (int, string) {
	switch script {
	case "clean_cache":
		r.logger.Info("control script executed (simulated)", "script", script, "args", args)
		return 0, "cache clean simulated"
	case "restart_monitor":
		return r.restartService(ctx, "go-agent")
	default:
		return 1, "unsupported safe script"
	}
}

func (r *ControlRunner) restartService(ctx context.Context, service string) (int, string) {
	if runtime.GOOS != "linux" {
		r.logger.Info("restart_service simulated", "service", service, "os", runtime.GOOS)
		return 0, "restart simulated on non-linux"
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "systemctl", "restart", service)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return 1, fmt.Sprintf("restart failed: %v; %s", err, strings.TrimSpace(string(out)))
	}
	return 0, strings.TrimSpace(string(out))
}

func (r *ControlRunner) buildWSURL() (string, error) {
	u, err := url.Parse(r.apiURL)
	if err != nil {
		return "", err
	}
	scheme := "ws"
	if strings.EqualFold(u.Scheme, "https") {
		scheme = "wss"
	}
	base := &url.URL{Scheme: scheme, Host: u.Host, Path: "/control/ws/" + url.PathEscape(r.deviceID)}
	q := base.Query()
	q.Set("token", r.token)
	base.RawQuery = q.Encode()
	return base.String(), nil
}

func backoffWithJitter(base time.Duration) time.Duration {
	if base <= 0 {
		base = time.Second
	}
	span := int64(base / 5)
	if span <= 0 {
		span = 1
	}
	extra := time.Duration(time.Now().UnixNano() % span)
	return base + extra
}

func trimBrief(s string, max int) string {
	v := strings.TrimSpace(s)
	if max <= 0 || len(v) <= max {
		return v
	}
	return v[:max]
}
