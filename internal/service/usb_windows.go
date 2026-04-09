//go:build windows

package service

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/qinyilin/go-agent/internal/models"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/yusufpapurcu/wmi"
)

// USB storage service registry path and value for Windows.
const (
	usbStorRegPath  = `SYSTEM\CurrentControlSet\Services\USBSTOR`
	usbStorStartVal = "Start"
)

// AllowedVIDPIDs reserves a hardware allowlist for approved encrypted USB devices.
// Format: "VID_1234&PID_ABCD" (uppercase). When matched, force-remove will skip it.
// Keep empty by default; maintain entries through code review/CMDB workflow.
var AllowedVIDPIDs = map[string]struct{}{}

type USBPolicy struct {
	AllowedInstanceIDs []string `json:"allowed_instance_ids"`
	AllowedDevices     []string `json:"allowed_devices"`
}

var usbPolicyState = struct {
	mu             sync.RWMutex
	allowed        map[string]struct{}
	allowedDevices map[string]struct{}
}{
	allowed:        map[string]struct{}{},
	allowedDevices: map[string]struct{}{},
}

// Remote policy fetcher configuration (set by agent main).
var policyEndpoint struct {
	mu       sync.RWMutex
	base     string // http(s)://host:port
	deviceID string
	client   *http.Client
}

// ConfigureUSBPolicyFetcher wires the server base and device id for fetching allowlist at runtime.
func ConfigureUSBPolicyFetcher(apiURL, deviceID string) {
	u, err := url.Parse(strings.TrimSpace(apiURL))
	if err != nil || strings.TrimSpace(u.Scheme) == "" || strings.TrimSpace(u.Host) == "" {
		return
	}
	base := (&url.URL{Scheme: u.Scheme, Host: u.Host}).String()
	policyEndpoint.mu.Lock()
	policyEndpoint.base = base
	policyEndpoint.deviceID = strings.TrimSpace(deviceID)
	if policyEndpoint.client == nil {
		policyEndpoint.client = &http.Client{Timeout: 3 * time.Second}
	}
	policyEndpoint.mu.Unlock()
}

func fetchAllowedInstanceIDsOnce(ctx context.Context) []string {
	policyEndpoint.mu.RLock()
	base := policyEndpoint.base
	devID := policyEndpoint.deviceID
	client := policyEndpoint.client
	policyEndpoint.mu.RUnlock()
	if base == "" || devID == "" || client == nil {
		return nil
	}
	urlStr := fmt.Sprintf("%s/api/v1/device/%s/usb_policy", strings.TrimRight(base, "/"), url.PathEscape(devID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}
	// Content-Length safety and bounded read
	if resp.ContentLength > 0 && resp.ContentLength > 1<<20 {
		return nil
	}
	lim := &io.LimitedReader{R: resp.Body, N: 256 << 10}
	body, err := io.ReadAll(lim)
	if err != nil {
		return nil
	}
	var obj struct {
		Allowed         []string `json:"allowed_instance_ids"`
		AllowedDevices  []string `json:"allowed_devices"`
	}
	if json.Unmarshal(body, &obj) != nil {
		return nil
	}
	out := make([]string, 0, len(obj.Allowed))
	for _, it := range obj.Allowed {
		v := strings.ToUpper(strings.TrimSpace(it))
		if v != "" {
			out = append(out, v)
		}
	}
	// Also update device models in memory for Contains checks.
	if len(obj.AllowedDevices) > 0 {
		SetUSBPolicy(USBPolicy{AllowedInstanceIDs: out, AllowedDevices: obj.AllowedDevices})
	}
	return out
}

// IsCurrentProcessElevated returns true if the current process has administrator privileges.
func IsCurrentProcessElevated() bool {
	var sid *windows.SID
	// Built-in Administrators group SID
	adminSID, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err == nil {
		sid = adminSID
	}
	if sid == nil {
		return false
	}

	token := windows.Token(0)
	isMember, err := token.IsMember(sid)
	if err != nil {
		return false
	}
	return isMember
}

// SetUSBStorageEnabled enables (start=3) or disables (start=4) USB mass storage via registry.
// Requires administrator privileges.
func SetUSBStorageEnabled(enable bool) error {
	if !IsCurrentProcessElevated() {
		return errors.New("insufficient privileges: administrator rights required")
	}
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, usbStorRegPath, registry.SET_VALUE)
	if err != nil {
		// Clarify permission requirement for hardening scenarios
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return fmt.Errorf("open registry key: access denied (administrator or TrustedInstaller may be required): %w", err)
		}
		return fmt.Errorf("open registry key: %w", err)
	}
	defer k.Close()

	var startVal uint32 = 4 // disabled
	if enable {
		startVal = 3 // manual (enabled)
	}
	if err := k.SetDWordValue(usbStorStartVal, startVal); err != nil {
		return fmt.Errorf("set Start value: %w", err)
	}
	// If we are disabling, attempt to force-remove currently connected mass storage devices
	if !enable {
		_ = forceRemoveConnectedUSBMassStorage()
	}
	return nil
}

// EnableUSBStorage is a helper that enables USB mass storage.
func EnableUSBStorage() error { return SetUSBStorageEnabled(true) }

// DisableUSBStorage is a helper that disables USB mass storage.
func DisableUSBStorage() error { return SetUSBStorageEnabled(false) }

func normalizeInstanceIDKey(id string) string {
	return strings.ToUpper(strings.TrimSpace(sanitizeDeviceID(id)))
}

func SetUSBPolicy(p USBPolicy) {
	next := make(map[string]struct{}, len(p.AllowedInstanceIDs))
	for _, id := range p.AllowedInstanceIDs {
		k := normalizeInstanceIDKey(id)
		if k != "" {
			next[k] = struct{}{}
		}
	}
	nextDev := make(map[string]struct{}, len(p.AllowedDevices))
	for _, d := range p.AllowedDevices {
		k := strings.ToUpper(strings.TrimSpace(d))
		if k != "" {
			nextDev[k] = struct{}{}
		}
	}
	usbPolicyState.mu.Lock()
	usbPolicyState.allowed = next
	usbPolicyState.allowedDevices = nextDev
	usbPolicyState.mu.Unlock()
}

func GetUSBPolicy() USBPolicy {
	usbPolicyState.mu.RLock()
	out := make([]string, 0, len(usbPolicyState.allowed))
	for id := range usbPolicyState.allowed {
		out = append(out, id)
	}
	devs := make([]string, 0, len(usbPolicyState.allowedDevices))
	for id := range usbPolicyState.allowedDevices {
		devs = append(devs, id)
	}
	usbPolicyState.mu.RUnlock()
	sort.Strings(out)
	sort.Strings(devs)
	return USBPolicy{AllowedInstanceIDs: out, AllowedDevices: devs}
}

func AddAllowedInstanceID(id string) {
	k := normalizeInstanceIDKey(id)
	if k == "" {
		return
	}
	usbPolicyState.mu.Lock()
	usbPolicyState.allowed[k] = struct{}{}
	usbPolicyState.mu.Unlock()
}

func IsUSBInstanceAllowed(id string) bool {
	k := normalizeInstanceIDKey(id)
	if k == "" {
		return false
	}
	usbPolicyState.mu.RLock()
	_, ok := usbPolicyState.allowed[k]
	if !ok {
		for dev := range usbPolicyState.allowedDevices {
			if strings.Contains(k, dev) { // VID_xxxx&PID_yyyy token match
				ok = true
				break
			}
		}
	}
	usbPolicyState.mu.RUnlock()
	return ok
}

func DisconnectUSBInstanceID(instanceID string) error {
	return removeUSBDeviceByInstanceID(instanceID)
}

// USBEventHandler is a callback invoked when a USB event is detected.
type USBEventHandler func(e models.USBEvent)

// StartUSBMonitor polls WMIC to detect USB device insert/remove events and invokes handler upon changes.
// This approach favors robustness and zero external deps over Win32 window message loops.
// It is best-effort: on newer Windows versions WMIC may be absent; in that case the monitor exits quietly.
func StartUSBMonitor(ctx context.Context, logger *slog.Logger, pollInterval time.Duration, onEvent func(models.USBEvent)) error {
	if logger == nil {
		logger = slog.Default()
	}
	if pollInterval <= 0 {
		// Enterprise-friendly default: 1 minute to reduce overhead on 2,000 nodes
		pollInterval = time.Minute
	}

	known := make(map[string]struct{})
	blockedUntil := make(map[string]time.Time) // anti-loop: recently ejected instance IDs
	var mu sync.Mutex
	const blockWindow = 10 * time.Second

	listOnce := func() (map[string]struct{}, []string, error) {
		devs := make(map[string]struct{})

		// --- Refactor: pnputil + WMI PlanB + DiskDrive->DiskPartition->LogicalDisk volume chain ---
		addUSBID := func(rawID string) {
			if strings.TrimSpace(rawID) == "" {
				return
			}
			sanitized := sanitizeDeviceID(rawID)
			if sanitized != rawID {
				// Help verify sanitizeDeviceID isn't collapsing IDs too aggressively.
				logger.Debug(
					"usb monitor sanitize changed",
					"raw_device_id", rawID,
					"sanitized_device_id", sanitized,
				)
			}
			fmt.Printf("[DEBUG] Detected Device: %s\n", sanitized)
			// Skip devices we just ejected to avoid churn loops.
			mu.Lock()
			until := blockedUntil[normalizeInstanceIDKey(sanitized)]
			mu.Unlock()
			if !until.IsZero() && time.Now().Before(until) {
				return
			}
			devs[sanitized] = struct{}{}
		}

		debugEnabled := logger.Enabled(ctx, slog.LevelDebug)

		escapeWQL := func(s string) string {
			// WQL string literal quoting: single quote => doubled single quote.
			return strings.ReplaceAll(s, "'", "''")
		}

		type win32DiskPartitionA struct {
			DeviceID string
		}
		type win32LogicalDiskA struct {
			DeviceID   string
			VolumeName *string
		}

		getVolumesByDiskDriveChain := func(diskDriveDeviceIDs []string) ([]string, error) {
			uniq := make(map[string]struct{})
			for _, driveDevID := range diskDriveDeviceIDs {
				driveDevID = strings.TrimSpace(driveDevID)
				if driveDevID == "" {
					continue
				}

				// DiskDrive -> DiskPartition
				wqlParts := fmt.Sprintf(
					"ASSOCIATORS OF {Win32_DiskDrive.DeviceID='%s'} WHERE AssocClass=Win32_DiskPartition",
					escapeWQL(driveDevID),
				)
				var parts []win32DiskPartitionA
				if err := wmi.Query(wqlParts, &parts); err != nil {
					continue
				}

				for _, p := range parts {
					pid := strings.TrimSpace(p.DeviceID)
					if pid == "" {
						continue
					}

					// DiskPartition -> LogicalDisk
					wqlLogical := fmt.Sprintf(
						"ASSOCIATORS OF {Win32_DiskPartition.DeviceID='%s'} WHERE AssocClass=Win32_LogicalDisk",
						escapeWQL(pid),
					)
					var logicals []win32LogicalDiskA
					if err := wmi.Query(wqlLogical, &logicals); err != nil {
						continue
					}

					for _, ld := range logicals {
						lbl := ""
						if ld.VolumeName != nil {
							lbl = strings.TrimSpace(*ld.VolumeName)
						}
						disp := formatVolumeDisplay(ld.DeviceID, lbl)
						if disp != "" {
							uniq[disp] = struct{}{}
						}
					}
				}
			}

			out := make([]string, 0, len(uniq))
			for k := range uniq {
				out = append(out, k)
			}
			return prioritizeVolumeNames(out), nil
		}

		// Plan A: enumerate all connected devices via pnputil, then match by Instance ID contains "USBSTOR".
		pnputilInstanceIDs := []string{}
		if _, err := exec.LookPath("pnputil.exe"); err == nil {
			out, cmdErr := exec.Command("pnputil", "/enum-devices", "/connected").CombinedOutput()
			if debugEnabled {
				lines := strings.Split(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n")
				if len(lines) > 20 {
					lines = lines[:20]
				}
				logger.Debug(
					"usb monitor pnputil /enum-devices /connected output head (first 20 lines)",
					"head", strings.Join(lines, "\n"),
				)
			}
			if cmdErr == nil {
				normalized := strings.ReplaceAll(string(out), "\r\n", "\n")
				blocks := strings.Split(normalized, "\n\n")
				for _, blk := range blocks {
					blk = strings.TrimSpace(blk)
					if blk == "" {
						continue
					}

					instanceID := ""
					sc := bufio.NewScanner(strings.NewReader(blk))
					for sc.Scan() {
						line := strings.TrimSpace(sc.Text())
						if strings.HasPrefix(line, "Instance ID:") {
							instanceID = strings.TrimSpace(strings.TrimPrefix(line, "Instance ID:"))
							break
						}
						if strings.HasPrefix(line, "InstanceId:") {
							instanceID = strings.TrimSpace(strings.TrimPrefix(line, "InstanceId:"))
							break
						}
					}

					if instanceID == "" {
						continue
					}
					if strings.Contains(strings.ToUpper(instanceID), "USBSTOR") {
						pnputilInstanceIDs = append(pnputilInstanceIDs, instanceID)
					}
				}
			}
		}

		// If pnputil found candidates, use them as USBID list.
		if len(pnputilInstanceIDs) > 0 {
			for _, id := range pnputilInstanceIDs {
				addUSBID(id)
			}

			// Volume labels: DiskDrive->DiskPartition->LogicalDisk chain from WMI USB drives.
			type win32DiskDriveA struct {
				DeviceID      string
				PNPDeviceID   *string
				InterfaceType *string
				Caption       *string
				Size          *uint64
			}
			var drives []win32DiskDriveA
			if err := wmi.Query("SELECT DeviceID,PNPDeviceID,InterfaceType,Caption,Size FROM Win32_DiskDrive WHERE InterfaceType='USB'", &drives); err == nil {
				driveDevIDs := make([]string, 0, len(drives))
				for _, d := range drives {
					if strings.TrimSpace(d.DeviceID) == "" {
						continue
					}
					pnp := ""
					if d.PNPDeviceID != nil {
						pnp = strings.TrimSpace(*d.PNPDeviceID)
					}
					caption := ""
					if d.Caption != nil {
						caption = strings.TrimSpace(*d.Caption)
					}
					looksLikeStorage := strings.Contains(strings.ToUpper(pnp), "USBSTOR") || strings.Contains(strings.ToUpper(caption), "DISK") || strings.Contains(strings.ToUpper(caption), "DRIVE")
					if !looksLikeStorage {
						continue
					}
					driveDevIDs = append(driveDevIDs, d.DeviceID)
				}
				if vols, _ := getVolumesByDiskDriveChain(driveDevIDs); len(vols) > 0 {
					return devs, vols, nil
				}
			}

			// Fallback if chain yields nothing.
			type win32LogicalDiskB struct {
				DeviceID   string
				VolumeName *string
				DriveType  uint32
			}
			var pnputilVolsRaw []win32LogicalDiskB
			_ = wmi.Query("SELECT DeviceID,VolumeName,DriveType FROM Win32_LogicalDisk WHERE DriveType = 2", &pnputilVolsRaw)
			var pnputilVolNames []string
			for _, v := range pnputilVolsRaw {
				name := ""
				if v.VolumeName != nil {
					name = strings.TrimSpace(*v.VolumeName)
				}
				name = formatVolumeDisplay(v.DeviceID, name)
				if name != "" {
					pnputilVolNames = append(pnputilVolNames, name)
				}
			}
			return devs, prioritizeVolumeNames(pnputilVolNames), nil
		}

		// Plan B: pnputil parsing fails -> WMI Win32_DiskDrive InterfaceType='USB'.
		type win32DiskDrivePlanB struct {
			DeviceID      string
			PNPDeviceID   *string
			InterfaceType string
			Caption       *string
			Size          *uint64
		}
		var diskEnts []win32DiskDrivePlanB
		if err := wmi.Query("SELECT DeviceID,PNPDeviceID,InterfaceType,Caption,Size FROM Win32_DiskDrive WHERE InterfaceType='USB'", &diskEnts); err != nil {
			return devs, nil, err
		}

		driveDevIDs := make([]string, 0, len(diskEnts))
		for _, d := range diskEnts {
			if strings.TrimSpace(d.DeviceID) == "" {
				continue
			}
			rawUSBID := ""
			if d.PNPDeviceID != nil {
				rawUSBID = strings.TrimSpace(*d.PNPDeviceID)
			}
			if rawUSBID == "" {
				rawUSBID = strings.TrimSpace(d.DeviceID)
			}
			caption := ""
			if d.Caption != nil {
				caption = strings.TrimSpace(*d.Caption)
			}
			looksLikeStorage := strings.Contains(strings.ToUpper(rawUSBID), "USBSTOR") || strings.Contains(strings.ToUpper(caption), "DISK") || strings.Contains(strings.ToUpper(caption), "DRIVE")
			if looksLikeStorage {
				addUSBID(rawUSBID)
				driveDevIDs = append(driveDevIDs, d.DeviceID)
			}
		}

		if vols, _ := getVolumesByDiskDriveChain(driveDevIDs); len(vols) > 0 {
			return devs, vols, nil
		}

		// Final fallback if chain yields nothing.
		type win32LogicalDiskC struct {
			DeviceID   string
			VolumeName *string
			DriveType  uint32
		}
		var planBVolsRaw []win32LogicalDiskC
		_ = wmi.Query("SELECT DeviceID,VolumeName,DriveType FROM Win32_LogicalDisk WHERE DriveType = 2", &planBVolsRaw)
		var planBVolNames []string
		for _, v := range planBVolsRaw {
			name := ""
			if v.VolumeName != nil {
				name = strings.TrimSpace(*v.VolumeName)
			}
			name = formatVolumeDisplay(v.DeviceID, name)
			if name != "" {
				planBVolNames = append(planBVolNames, name)
			}
		}
		return devs, prioritizeVolumeNames(planBVolNames), nil

		type win32PnPEntity struct {
			DeviceID string
			PNPClass *string
			Service  *string
			Name     *string
		}

		deref := func(s *string) string {
			if s == nil {
				return ""
			}
			return strings.TrimSpace(*s)
		}

		addDevice := func(rawID, matchedReason, serviceVal, pnpClassVal string) {
			if strings.TrimSpace(rawID) == "" {
				return
			}
			sanitized := sanitizeDeviceID(rawID)
			if sanitized != rawID {
				// Help verify sanitizeDeviceID isn't collapsing IDs too aggressively.
				logger.Debug(
					"usb monitor sanitize changed",
					"raw_device_id", rawID,
					"sanitized_device_id", sanitized,
					"reason", matchedReason,
					"service", serviceVal,
					"pnp_class", pnpClassVal,
				)
			}
			devs[sanitized] = struct{}{}
		}

		// Prefer native WMI library to avoid process creation overhead.
		// We intentionally loosen matching rules to handle different Windows device enumeration behaviors.
		var usbstorEnts []win32PnPEntity
		errUSBSTOR := wmi.Query("SELECT DeviceID,PNPClass,Service,Name FROM Win32_PnPEntity WHERE Service='USBSTOR'", &usbstorEnts)

		var diskDriveEnts []win32PnPEntity
		errDiskDrive := wmi.Query("SELECT DeviceID,PNPClass,Service,Name FROM Win32_PnPEntity WHERE PNPClass='DiskDrive'", &diskDriveEnts)

		if errUSBSTOR != nil || errDiskDrive != nil {
			// Fallback to wmic if native query fails at all.
			devs, vols, ferr := listViaWMIC()
			return devs, vols, ferr
		}

		// Debug + relaxed matching.
		// Match rules:
		// - Service == "USBSTOR"
		// - OR (PNPClass == "DiskDrive" AND DeviceID contains "USBSTOR" OR contains "USB\\")
		devsFromPnPEntity := 0
		for _, e := range usbstorEnts {
			rawID := strings.TrimSpace(e.DeviceID)
			serviceVal := deref(e.Service)
			pnpClassVal := deref(e.PNPClass)
			logger.Debug(
				"usb monitor pnpe candidate",
				"device_id", rawID,
				"service", serviceVal,
				"pnp_class", pnpClassVal,
				"matched", true,
				"reason", "service==USBSTOR",
			)
			addDevice(rawID, "service==USBSTOR", serviceVal, pnpClassVal)
			devsFromPnPEntity++
		}

		for _, e := range diskDriveEnts {
			rawID := strings.TrimSpace(e.DeviceID)
			serviceVal := deref(e.Service)
			pnpClassVal := deref(e.PNPClass)
			idUpper := strings.ToUpper(rawID)
			hasUSBSTOR := strings.Contains(idUpper, "USBSTOR")
			hasUSBPrefix := strings.Contains(idUpper, "USB\\") // "USB\" substring

			matched := strings.EqualFold(pnpClassVal, "DiskDrive") && (hasUSBSTOR || hasUSBPrefix)
			logger.Debug(
				"usb monitor pnpe candidate",
				"device_id", rawID,
				"service", serviceVal,
				"pnp_class", pnpClassVal,
				"matched", matched,
				"reason", func() string {
					if matched {
						if hasUSBSTOR {
							return "pnpclass==DiskDrive && device_id contains USBSTOR"
						}
						return "pnpclass==DiskDrive && device_id contains USB\\"
					}
					return "pnpclass!=DiskDrive/does not look like USB mass storage"
				}(),
			)
			if matched {
				addDevice(rawID, "pnpclass==DiskDrive (USBSTOR/USB\\)", serviceVal, pnpClassVal)
				devsFromPnPEntity++
			}
		}

		// Compatibility search: if Win32_PnPEntity results are effectively empty, fall back to Win32_DiskDrive.
		if len(devs) == 0 && len(usbstorEnts) == 0 && len(diskDriveEnts) == 0 {
			type win32DiskDrive struct {
				DeviceID      string
				InterfaceType *string
			}
			var diskEnts []win32DiskDrive
			if err := wmi.Query("SELECT DeviceID,InterfaceType FROM Win32_DiskDrive WHERE InterfaceType='USB'", &diskEnts); err != nil {
				// Last resort: try wmic walker.
				return listViaWMIC()
			}
			for _, d := range diskEnts {
				rawID := strings.TrimSpace(d.DeviceID)
				it := deref(d.InterfaceType)
				logger.Debug(
					"usb monitor diskdrive candidate",
					"device_id", rawID,
					"interface_type", it,
					"matched", true,
					"reason", "Win32_DiskDrive.InterfaceType==USB",
				)
				addDevice(rawID, "Win32_DiskDrive.InterfaceType==USB", it, "DiskDrive")
				devsFromPnPEntity++
			}
		}

		logger.Debug(
			"usb monitor scan summary",
			"candidates_scanned", devsFromPnPEntity,
			"matched_usb_devices", len(devs),
		)

		// Also gather removable volumes to enrich events (DriveType=2)
		type win32LogicalDisk struct {
			DeviceID   string
			VolumeName *string
			DriveType  uint32
		}
		var volsRaw []win32LogicalDisk
		_ = wmi.Query("SELECT DeviceID,VolumeName,DriveType FROM Win32_LogicalDisk WHERE DriveType = 2", &volsRaw)
		var volNames []string
		for _, v := range volsRaw {
			name := ""
			if v.VolumeName != nil {
				name = strings.TrimSpace(*v.VolumeName)
			}
			name = formatVolumeDisplay(v.DeviceID, name)
			if name != "" {
				volNames = append(volNames, name)
			}
		}
		return devs, prioritizeVolumeNames(volNames), nil
	}

	// seed initial set (also emit "existing" so UI can reflect pre-inserted devices)
	initial, volumes, err := listOnce()
	if err != nil {
		logger.Warn("usb monitor initial query failed; monitor disabled", "err", err)
		return nil
	}
	// Preload allowlist once at startup (best-effort).
	if ids := fetchAllowedInstanceIDsOnce(context.WithValue(ctx, struct{}{}, nil)); len(ids) > 0 {
		SetUSBPolicy(USBPolicy{AllowedInstanceIDs: ids})
	}
	mu.Lock()
	for k := range initial {
		if !IsUSBInstanceAllowed(k) {
			logger.Info("[POLICY] Blocking unauthorized device (initial): " + k)
			_ = removeUSBDeviceByInstanceID(k)
			blockedUntil[normalizeInstanceIDKey(k)] = time.Now().Add(blockWindow)
			continue
		}
		known[k] = struct{}{}
	}
	mu.Unlock()

	if onEvent != nil && len(initial) > 0 {
		now := time.Now()
		volSummary := strings.Join(volumes, ",")
		for id := range initial {
			onEvent(models.USBEvent{
				Action:     "existing",
				USBID:      id,
				VolumeName: volSummary,
				Timestamp:  now.Unix(),
			})
		}
	}

	t := time.NewTicker(pollInterval)
	go func() {
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				current, volumes, err := listOnce()
				if err != nil {
					logger.Debug("usb monitor query error", "err", err)
					continue
				}
				now := time.Now()
				volSummary := strings.Join(volumes, ",")
				// detect inserts
				mu.Lock()
				for id := range current {
					if _, ok := known[id]; !ok {
						// Anti-loop: if just ejected, ignore for a short window.
						if until := blockedUntil[normalizeInstanceIDKey(id)]; !until.IsZero() && time.Now().Before(until) {
							continue
						}
						// Fetch latest policy from server (best-effort, short timeout).
						func() {
							cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
							defer cancel()
							if ids := fetchAllowedInstanceIDsOnce(cctx); len(ids) > 0 {
								SetUSBPolicy(USBPolicy{AllowedInstanceIDs: ids})
							}
						}()
						if !IsUSBInstanceAllowed(id) {
							logger.Info("[POLICY] Blocking unauthorized device: " + id)
							_ = removeUSBDeviceByInstanceID(id)
							blockedUntil[normalizeInstanceIDKey(id)] = time.Now().Add(blockWindow)
							continue
						}
						known[id] = struct{}{}
						if onEvent != nil {
							onEvent(models.USBEvent{
								Action:     "insert",
								USBID:      id,
								VolumeName: volSummary,
								Timestamp:  now.Unix(),
							})
						}
					}
				}
				// detect removals
				for id := range known {
					if _, ok := current[id]; !ok {
						delete(known, id)
						if onEvent != nil {
							onEvent(models.USBEvent{
								Action:     "remove",
								USBID:      id,
								VolumeName: volSummary,
								Timestamp:  now.Unix(),
							})
						}
					}
				}
				mu.Unlock()
			}
		}
	}()
	return nil
}

// sanitizeDeviceID normalizes DeviceID backslashes to single form and trims spaces.
func sanitizeDeviceID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return id
	}
	// Some Windows APIs / command outputs may include extra escaping (e.g. `\\\\`).
	// Collapse consecutive backslashes into a single `\` to keep DeviceID clean.
	var b strings.Builder
	b.Grow(len(id))
	prevSlash := false
	for _, r := range id {
		if r == '\\' {
			if prevSlash {
				continue
			}
			prevSlash = true
			b.WriteRune(r)
			continue
		}
		prevSlash = false
		b.WriteRune(r)
	}
	return b.String()
}

func formatVolumeDisplay(driveLetter, volumeName string) string {
	driveLetter = strings.TrimSpace(driveLetter)
	volumeName = strings.TrimSpace(volumeName)
	if driveLetter != "" && volumeName != "" {
		return fmt.Sprintf("%s [%s]", driveLetter, volumeName)
	}
	if volumeName != "" {
		return volumeName
	}
	return driveLetter
}

func isSystemVolumeName(name string) bool {
	v := strings.ToLower(strings.TrimSpace(name))
	if v == "" {
		return true
	}
	keywords := []string{
		"system reserved",
		"recovery",
		"efi",
		"msr",
		"系统保留",
		"恢复",
	}
	for _, kw := range keywords {
		if strings.Contains(v, kw) {
			return true
		}
	}
	return false
}

func prioritizeVolumeNames(names []string) []string {
	seen := make(map[string]struct{}, len(names))
	normal := make([]string, 0, len(names))
	system := make([]string, 0, len(names))
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		if isSystemVolumeName(name) {
			system = append(system, name)
		} else {
			normal = append(normal, name)
		}
	}
	target := normal
	if len(target) == 0 {
		target = system
	}
	sort.SliceStable(target, func(i, j int) bool {
		// Prefer drive-letter + label style like "G: [我的闪存盘]".
		a := strings.Contains(target[i], ":") && strings.Contains(target[i], "[")
		b := strings.Contains(target[j], ":") && strings.Contains(target[j], "[")
		if a != b {
			return a
		}
		// Then prefer plain drive-letter style.
		a2 := strings.Contains(target[i], ":")
		b2 := strings.Contains(target[j], ":")
		if a2 != b2 {
			return a2
		}
		return target[i] < target[j]
	})
	return target
}

// listViaWMIC is a fallback walker using the legacy wmic command, filtered by DeviceID containing USBSTOR.
func listViaWMIC() (map[string]struct{}, []string, error) {
	type void struct{}
	devs := make(map[string]struct{})
	// If wmic is absent, exit gracefully
	if _, err := exec.LookPath("wmic.exe"); err != nil {
		return devs, nil, err
	}
	cmd := exec.Command("wmic", "path", "Win32_PnPEntity", "where", "PNPClass=\"USB\"", "get", "DeviceID,Service", "/value")
	out, err := cmd.Output()
	if err != nil {
		return devs, nil, err
	}
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	curDev := ""
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			if curDev != "" && strings.Contains(strings.ToUpper(curDev), "USBSTOR") {
				devs[sanitizeDeviceID(curDev)] = struct{}{}
			}
			curDev = ""
			continue
		}
		if strings.HasPrefix(line, "DeviceID=") {
			curDev = strings.TrimSpace(strings.TrimPrefix(line, "DeviceID="))
		}
	}
	if curDev != "" && strings.Contains(strings.ToUpper(curDev), "USBSTOR") {
		devs[sanitizeDeviceID(curDev)] = struct{}{}
	}

	// Removable volume names via WMI (no extra process)
	type win32LogicalDisk struct {
		DeviceID   string
		VolumeName *string
		DriveType  uint32
	}
	var volsRaw []win32LogicalDisk
	_ = wmi.Query("SELECT DeviceID,VolumeName,DriveType FROM Win32_LogicalDisk WHERE DriveType = 2", &volsRaw)
	var volNames []string
	for _, v := range volsRaw {
		name := ""
		if v.VolumeName != nil {
			name = strings.TrimSpace(*v.VolumeName)
		}
		name = formatVolumeDisplay(v.DeviceID, name)
		if name != "" {
			volNames = append(volNames, name)
		}
	}
	return devs, prioritizeVolumeNames(volNames), nil
}

func removeUSBDeviceByInstanceID(instanceID string) error {
	instanceID = strings.TrimSpace(sanitizeDeviceID(instanceID))
	if instanceID == "" {
		return errors.New("empty instance id")
	}
	if _, err := exec.LookPath("pnputil.exe"); err != nil {
		return nil
	}
	cmd := exec.Command("pnputil", "/remove-device", instanceID)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("pnputil remove-device failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// forceRemoveConnectedUSBMassStorage enumerates connected devices and calls pnputil to remove them.
// Requires administrator privileges and may not be supported on very old Windows builds.
func forceRemoveConnectedUSBMassStorage() error {
	if _, err := exec.LookPath("pnputil.exe"); err != nil {
		return nil // silently ignore if pnputil is unavailable
	}
	// Enumerate connected devices and parse blocks
	out, err := exec.Command("pnputil", "/enum-devices", "/connected").CombinedOutput()
	if err != nil {
		return nil // avoid being noisy; disabling via registry is already effective after reboot
	}
	blocks := strings.Split(string(out), "\r\n\r\n")
	for _, b := range blocks {
		sc := bufio.NewScanner(strings.NewReader(b))
		instanceID := ""
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if strings.HasPrefix(line, "Instance ID:") {
				instanceID = strings.TrimSpace(strings.TrimPrefix(line, "Instance ID:"))
				break
			}
			if strings.HasPrefix(line, "InstanceId:") {
				instanceID = strings.TrimSpace(strings.TrimPrefix(line, "InstanceId:"))
				break
			}
		}
		if instanceID == "" {
			continue
		}
		// Only force-remove devices whose Instance ID indicates USBSTOR.
		if !strings.Contains(strings.ToUpper(instanceID), "USBSTOR") {
			continue
		}
		if isAllowedUSBDevice(instanceID) {
			// Approved device, skip force removal.
			continue
		}
		// Attempt removal
		_ = exec.Command("pnputil", "/remove-device", instanceID).Run()
	}
	return nil
}

func isAllowedUSBDevice(instanceID string) bool {
	vp := extractVIDPID(instanceID)
	if vp == "" {
		return false
	}
	_, ok := AllowedVIDPIDs[vp]
	return ok
}

func extractVIDPID(instanceID string) string {
	s := strings.ToUpper(strings.TrimSpace(instanceID))
	vidIdx := strings.Index(s, "VID_")
	pidIdx := strings.Index(s, "PID_")
	if vidIdx < 0 || pidIdx < 0 {
		return ""
	}
	if vidIdx+8 > len(s) || pidIdx+8 > len(s) {
		return ""
	}
	vid := s[vidIdx : vidIdx+8] // VID_XXXX
	pid := s[pidIdx : pidIdx+8] // PID_XXXX
	return vid + "&" + pid
}
