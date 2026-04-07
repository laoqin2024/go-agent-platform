package service

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

type UpdateRunner struct {
	logger             *slog.Logger
	currentVersion     string
	apiURL             string
	deviceID           string
	serviceName        string
	checkEvery         time.Duration
	maxJitter          time.Duration
	caCertPath         string
	clientCertPath     string
	clientKeyPath      string
	insecureSkipVerify bool
	triggerCh          chan struct{}
	checkMu            sync.Mutex
}

type UpdateRunnerOption func(*UpdateRunner)

func WithUpdateCheckEvery(d time.Duration) UpdateRunnerOption {
	return func(r *UpdateRunner) {
		if d > 0 {
			r.checkEvery = d
		}
	}
}

func WithUpdateJitter(d time.Duration) UpdateRunnerOption {
	return func(r *UpdateRunner) {
		if d >= 0 {
			r.maxJitter = d
		}
	}
}

func NewUpdateRunner(
	logger *slog.Logger,
	currentVersion string,
	apiURL string,
	deviceID string,
	serviceName string,
	caCertPath string,
	clientCertPath string,
	clientKeyPath string,
	insecureSkipVerify bool,
	opts ...UpdateRunnerOption,
) *UpdateRunner {
	r := &UpdateRunner{
		logger:             logger,
		currentVersion:     strings.TrimSpace(currentVersion),
		apiURL:             strings.TrimSpace(apiURL),
		deviceID:           strings.TrimSpace(deviceID),
		serviceName:        strings.TrimSpace(serviceName),
		checkEvery:         6 * time.Hour,
		maxJitter:          30 * time.Minute,
		caCertPath:         strings.TrimSpace(caCertPath),
		clientCertPath:     strings.TrimSpace(clientCertPath),
		clientKeyPath:      strings.TrimSpace(clientKeyPath),
		insecureSkipVerify: insecureSkipVerify,
		triggerCh:          make(chan struct{}, 1),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(r)
		}
	}
	if r.serviceName == "" {
		r.serviceName = "go-agent"
	}
	return r
}

type agentVersionResp struct {
	LatestVersion string `json:"latest_version"`
	DownloadURL   string `json:"download_url"`
	SHA256        string `json:"sha256"`
}

type updateState struct {
	TargetVersion   string `json:"target_version"`
	PreviousVersion string `json:"previous_version"`
	BackupPath      string `json:"backup_path"`
	LastStartAtUnix int64  `json:"last_start_at_unix"`
	FailCount       int    `json:"fail_count"`
}

func (r *UpdateRunner) Run(ctx context.Context) {
	if r.apiURL == "" {
		r.logger.Warn("update runner disabled: empty apiURL")
		return
	}
	needRestart, err := r.handleStartupGuard()
	if err != nil {
		r.logger.Warn("startup rollback guard failed", "err", err)
	}
	if needRestart {
		r.logger.Error("rollback applied; exiting so service manager restarts with restored binary")
		os.Exit(1)
	}
	r.markHealthyAfter(ctx, time.Minute)
	if err := r.checkAndApplySafely(ctx); err != nil {
		r.logger.Warn("startup update check failed", "err", err)
	}
	ticker := time.NewTicker(r.checkEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.triggerCh:
			if err := r.checkAndApplySafely(ctx); err != nil {
				r.logger.Warn("force update check failed", "err", err)
			}
		case <-ticker.C:
			ok, forced := r.waitWithJitter(ctx)
			if !ok {
				return
			}
			if forced {
				r.logger.Info("update runner: jitter interrupted by force_update trigger")
			}
			if err := r.checkAndApplySafely(ctx); err != nil {
				r.logger.Warn("update check failed", "err", err)
			}
		}
	}
}

func (r *UpdateRunner) TriggerNow() {
	select {
	case r.triggerCh <- struct{}{}:
	default:
	}
}

func (r *UpdateRunner) waitWithJitter(ctx context.Context) (bool, bool) {
	if r.maxJitter <= 0 {
		return true, false
	}
	sec := int64(r.maxJitter / time.Second)
	if sec <= 0 {
		return true, false
	}
	wait := time.Duration(rand.New(rand.NewSource(time.Now().UnixNano())).Int63n(sec+1)) * time.Second
	r.logger.Info("update runner jitter delay", "wait", wait.String())
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false, false
	case <-r.triggerCh:
		return true, true
	case <-timer.C:
		return true, false
	}
}

func (r *UpdateRunner) checkAndApply(ctx context.Context) error {
	meta, err := r.fetchVersionMeta(ctx)
	if err != nil {
		return err
	}
	if !isNewerVersion(meta.LatestVersion, r.currentVersion) {
		r.logger.Debug("update runner: already latest", "current", r.currentVersion, "latest", meta.LatestVersion)
		return nil
	}
	exePath, err := os.Executable()
	if err != nil {
		return err
	}
	exePath, _ = filepath.Abs(exePath)

	bin, err := r.downloadBinary(ctx, meta.DownloadURL)
	if err != nil {
		return err
	}
	if err := verifySHA256(bin, meta.SHA256); err != nil {
		return err
	}
	if err := r.swapBinary(exePath, strings.TrimSpace(meta.LatestVersion), bin); err != nil {
		return err
	}
	r.logger.Info("update runner: binary updated", "from", r.currentVersion, "to", meta.LatestVersion)
	r.currentVersion = strings.TrimSpace(meta.LatestVersion)
	if runtime.GOOS == "windows" {
		// Windows replacement is done by detached .bat. Exit now to release file lock.
		os.Exit(0)
	}
	return nil
}

func (r *UpdateRunner) checkAndApplySafely(ctx context.Context) error {
	r.checkMu.Lock()
	defer r.checkMu.Unlock()
	return r.checkAndApply(ctx)
}

func (r *UpdateRunner) fetchVersionMeta(ctx context.Context) (*agentVersionResp, error) {
	base, err := url.Parse(r.apiURL)
	if err != nil {
		return nil, err
	}
	base.Path = "/api/v1/agent/version"
	q := base.Query()
	q.Set("os", runtime.GOOS)
	q.Set("arch", runtime.GOARCH)
	if strings.TrimSpace(r.deviceID) != "" {
		q.Set("device_id", strings.TrimSpace(r.deviceID))
	}
	base.RawQuery = q.Encode()

	client, err := r.newHTTPClient()
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("version api status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var out agentVersionResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if strings.TrimSpace(out.LatestVersion) == "" || strings.TrimSpace(out.DownloadURL) == "" || strings.TrimSpace(out.SHA256) == "" {
		return nil, fmt.Errorf("version api returned incomplete metadata")
	}
	return &out, nil
}

func (r *UpdateRunner) downloadBinary(ctx context.Context, u string) ([]byte, error) {
	client, err := r.newHTTPClient()
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSpace(u), nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download status=%d", resp.StatusCode)
	}
	// Hard cap update payload size to avoid unexpected memory spikes.
	const maxBinarySize = 200 << 20
	return io.ReadAll(io.LimitReader(resp.Body, maxBinarySize))
}

func (r *UpdateRunner) swapBinary(exePath, targetVersion string, bin []byte) error {
	state := updateState{
		TargetVersion:   strings.TrimSpace(targetVersion),
		PreviousVersion: r.currentVersion,
		BackupPath:      exePath + ".bak",
		LastStartAtUnix: time.Now().Unix(),
		FailCount:       0,
	}
	if err := copyFile(exePath, state.BackupPath, 0o755); err != nil {
		return fmt.Errorf("backup old binary failed: %w", err)
	}
	if err := r.writeUpdateState(exePath, state); err != nil {
		return fmt.Errorf("write update state failed: %w", err)
	}
	if runtime.GOOS == "windows" {
		return r.swapWindows(exePath, state.BackupPath, bin)
	}
	return r.swapUnix(exePath, bin)
}

func (r *UpdateRunner) swapUnix(exePath string, bin []byte) error {
	tmpPath := exePath + ".new"
	if err := os.WriteFile(tmpPath, bin, 0o755); err != nil {
		return err
	}
	return os.Rename(tmpPath, exePath)
}

func (r *UpdateRunner) swapWindows(exePath, backupPath string, bin []byte) error {
	newPath := exePath + ".new"
	if err := os.WriteFile(newPath, bin, 0o755); err != nil {
		return err
	}
	script := "@echo off\r\n" +
		"setlocal enabledelayedexpansion\r\n" +
		"\r\n" +
		":: %1: 服务名称\r\n" +
		":: %2: 旧二进制路径\r\n" +
		":: %3: 新二进制路径\r\n" +
		"\r\n" +
		"set SERVICE_NAME=%~1\r\n" +
		"set OLD_EXE=%~2\r\n" +
		"set NEW_EXE=%~3\r\n" +
		"\r\n" +
		"echo [Update] Starting update for %SERVICE_NAME%...\r\n" +
		"net stop %SERVICE_NAME% >nul 2>&1\r\n" +
		"\r\n" +
		"set retry=0\r\n" +
		":wait_loop\r\n" +
		"tasklist /FI \"IMAGENAME eq go-agent.exe\" 2>NUL | find /I /N \"go-agent.exe\">NUL\r\n" +
		"if \"%ERRORLEVEL%\"==\"0\" (\r\n" +
		"    set /a retry+=1\r\n" +
		"    if !retry! GTR 10 (\r\n" +
		"        echo [Error] Process still running, killing it...\r\n" +
		"        taskkill /F /IM go-agent.exe >nul 2>&1\r\n" +
		"    )\r\n" +
		"    timeout /t 1 /nobreak >nul\r\n" +
		"    goto wait_loop\r\n" +
		")\r\n" +
		"\r\n" +
		"timeout /t 5 /nobreak >nul\r\n" +
		"\r\n" +
		"echo [Update] Replacing binary...\r\n" +
		"if not exist \"" + backupPath + "\" copy /Y \"%OLD_EXE%\" \"" + backupPath + "\" >nul\r\n" +
		"del \"%OLD_EXE%\" >nul 2>&1\r\n" +
		"move /Y \"%NEW_EXE%\" \"%OLD_EXE%\" >nul\r\n" +
		"\r\n" +
		"echo [Update] Restarting service...\r\n" +
		"net start %SERVICE_NAME% >nul 2>&1\r\n" +
		"\r\n" +
		"echo [Update] Done.\r\n" +
		"(goto) 2>nul & del \"%~f0\"\r\n" +
		"endlocal\r\n"
	batPath := filepath.Join(os.TempDir(), "go-agent-update-"+strconv.FormatInt(time.Now().Unix(), 10)+".bat")
	if err := os.WriteFile(batPath, []byte(script), 0o700); err != nil {
		return err
	}
	serviceName := strings.TrimSpace(r.serviceName)
	if serviceName == "" {
		serviceName = "GoAgent"
	}
	cmd := exec.Command("cmd.exe", "/C", "start", "", "/B", batPath, serviceName, exePath, newPath)
	return cmd.Start()
}

func (r *UpdateRunner) newHTTPClient() (*http.Client, error) {
	tlsConfig := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: r.insecureSkipVerify,
	}
	if r.caCertPath != "" {
		caPEM, err := os.ReadFile(r.caCertPath)
		if err == nil {
			pool := x509.NewCertPool()
			if pool.AppendCertsFromPEM(caPEM) {
				tlsConfig.RootCAs = pool
			}
		}
	}
	if r.clientCertPath != "" && r.clientKeyPath != "" {
		cert, err := tls.LoadX509KeyPair(r.clientCertPath, r.clientKeyPath)
		if err == nil {
			tlsConfig.Certificates = []tls.Certificate{cert}
		}
	}
	return &http.Client{
		Transport: &http.Transport{TLSClientConfig: tlsConfig},
		Timeout:   60 * time.Second,
	}, nil
}

func verifySHA256(bin []byte, expected string) error {
	want := strings.ToLower(strings.TrimSpace(expected))
	want = strings.TrimPrefix(want, "sha256:")
	sum := sha256.Sum256(bin)
	got := hex.EncodeToString(sum[:])
	if got != want {
		return fmt.Errorf("sha256 mismatch: got=%s want=%s", got, want)
	}
	return nil
}

func isNewerVersion(latest, current string) bool {
	lv := parseVer(strings.TrimSpace(latest))
	cv := parseVer(strings.TrimSpace(current))
	max := len(lv)
	if len(cv) > max {
		max = len(cv)
	}
	for i := 0; i < max; i++ {
		a := 0
		b := 0
		if i < len(lv) {
			a = lv[i]
		}
		if i < len(cv) {
			b = cv[i]
		}
		if a > b {
			return true
		}
		if a < b {
			return false
		}
	}
	return false
}

func parseVer(v string) []int {
	v = strings.TrimSpace(strings.TrimPrefix(strings.ToLower(v), "v"))
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n := 0
		for i := 0; i < len(p); i++ {
			ch := p[i]
			if ch < '0' || ch > '9' {
				break
			}
			n = n*10 + int(ch-'0')
		}
		out = append(out, n)
	}
	return out
}

func (r *UpdateRunner) handleStartupGuard() (bool, error) {
	exePath, err := os.Executable()
	if err != nil {
		return false, err
	}
	exePath, _ = filepath.Abs(exePath)
	st, err := r.readUpdateState(exePath)
	if err != nil {
		return false, nil
	}
	now := time.Now().Unix()
	if st.LastStartAtUnix > 0 && now-st.LastStartAtUnix <= 60 {
		st.FailCount++
	}
	st.LastStartAtUnix = now
	if st.FailCount >= 3 && st.BackupPath != "" {
		if err := copyFile(st.BackupPath, exePath, 0o755); err != nil {
			return false, err
		}
		_ = os.Remove(r.updateStatePath(exePath))
		r.logger.Warn("auto rollback completed", "backup", st.BackupPath, "fail_count", st.FailCount)
		return true, nil
	}
	_ = r.writeUpdateState(exePath, st)
	return false, nil
}

func (r *UpdateRunner) markHealthyAfter(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	go func() {
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		exePath, err := os.Executable()
		if err != nil {
			return
		}
		exePath, _ = filepath.Abs(exePath)
		st, err := r.readUpdateState(exePath)
		if err != nil {
			return
		}
		_ = os.Remove(r.updateStatePath(exePath))
		if strings.TrimSpace(st.BackupPath) != "" {
			_ = os.Remove(st.BackupPath)
		}
		r.logger.Info("update startup considered healthy, rollback state cleared")
	}()
}

func (r *UpdateRunner) updateStatePath(exePath string) string {
	return exePath + ".update_state.json"
}

func (r *UpdateRunner) writeUpdateState(exePath string, st updateState) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return os.WriteFile(r.updateStatePath(exePath), b, 0o600)
}

func (r *UpdateRunner) readUpdateState(exePath string) (updateState, error) {
	var st updateState
	b, err := os.ReadFile(r.updateStatePath(exePath))
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return st, err
	}
	return st, nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
