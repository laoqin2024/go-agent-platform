//go:build windows

package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

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

const (
	ioctlStorageQueryProperty  = 0x2D1400
	ioctlStorageGetDeviceNum   = 0x2D1080
	ioctlStorageGetHotplugInfo = 0x2D0C14

	busTypeUSB = 7  // STORAGE_BUS_TYPE::BusTypeUsb
	busTypeUAS = 14 // STORAGE_BUS_TYPE::BusTypeUsbFibre (UASP)
	// Other common values: 11 = BusTypeSata, 17 = BusTypeNVMe (not treated as USB storage here).

	driveTypeRemovable = 2
	fileDeviceDisk     = 0x00000007
)

var (
	user32                         = windows.NewLazySystemDLL("user32.dll")
	kernel32                       = windows.NewLazySystemDLL("kernel32.dll")
	procRegisterClassExW           = user32.NewProc("RegisterClassExW")
	procCreateWindowExW            = user32.NewProc("CreateWindowExW")
	procDefWindowProcW             = user32.NewProc("DefWindowProcW")
	procGetMessageW                = user32.NewProc("GetMessageW")
	procTranslateMessage           = user32.NewProc("TranslateMessage")
	procDispatchMessageW           = user32.NewProc("DispatchMessageW")
	procRegisterDeviceNotification = user32.NewProc("RegisterDeviceNotificationW")
	procUnregisterDeviceNotif      = user32.NewProc("UnregisterDeviceNotification")
	procPostMessageW               = user32.NewProc("PostMessageW")
	procDestroyWindow              = user32.NewProc("DestroyWindow")
	procPostQuitMessage            = user32.NewProc("PostQuitMessage")
	procQueryDosDeviceW            = kernel32.NewProc("QueryDosDeviceW")
)

const (
	wmDeviceChange           = 0x0219
	wmClose                  = 0x0010
	wmDestroy                = 0x0002
	dbtDeviceArrival         = 0x8000
	dbtDeviceRemoveComplete  = 0x8004
	dbtDevTypeDeviceIface    = 0x00000005
	deviceNotifyWindowHandle = 0x00000000
)

var (
	guidDevInterfaceUSBDevice = windows.GUID{Data1: 0xA5DCBF10, Data2: 0x6530, Data3: 0x11D2, Data4: [8]byte{0x90, 0x1F, 0x00, 0xC0, 0x4F, 0xB9, 0x51, 0xED}}
	guidDevInterfaceDisk      = windows.GUID{Data1: 0x53f56307, Data2: 0xB6BF, Data3: 0x11D0, Data4: [8]byte{0x94, 0xF2, 0x00, 0xA0, 0xC9, 0x1E, 0xFB, 0x8B}}
	guidDevInterfaceStorage   = windows.GUID{Data1: 0x2ACCFE60, Data2: 0xC130, Data3: 0x11D2, Data4: [8]byte{0xB0, 0x82, 0x00, 0xA0, 0xC9, 0x1E, 0xFB, 0x8B}}
)

// storageDeviceNumber matches Windows STORAGE_DEVICE_NUMBER (ntddstor.h):
//
//	DEVICE_TYPE DeviceType; ULONG DeviceNumber; ULONG PartitionNumber;
//
// All three are 32-bit; use queryStorageDeviceNumberBytes for IOCTL decode.
type storageDeviceNumber struct {
	DeviceType      uint32
	DeviceNumber    uint32
	PartitionNumber uint32
}

// STORAGE_DEVICE_NUMBER is three ULONG fields (12 bytes). IOCTL matching depends on exact layout.
var (
	_ [unsafe.Sizeof(storageDeviceNumber{}) - 12]byte
	_ [12 - unsafe.Sizeof(storageDeviceNumber{})]byte
)

type storagePropertyQuery struct {
	PropertyID uint32
	QueryType  uint32
	Additional [1]byte
}

type storageDescriptorHeader struct {
	Version uint32
	Size    uint32
}

// Match Windows STORAGE_DEVICE_DESCRIPTOR field order.
type storageDeviceDescriptor struct {
	Version               uint32
	Size                  uint32
	DeviceType            byte
	DeviceTypeModifier    byte
	RemovableMedia        byte
	CommandQueueing       byte
	VendorIDOffset        uint32
	ProductIDOffset       uint32
	ProductRevisionOffset uint32
	SerialNumberOffset    uint32
	BusType               byte
	RawPropertiesLength   uint32
}

type devBroadcastDeviceInterface struct {
	Size       uint32
	DeviceType uint32
	Reserved   uint32
	ClassGuid  windows.GUID
}

type point struct {
	X int32
	Y int32
}

type msg struct {
	Hwnd     windows.Handle
	Message  uint32
	WParam   uintptr
	LParam   uintptr
	Time     uint32
	Pt       point
	LPrivate uint32
}

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   windows.Handle
	Icon       windows.Handle
	Cursor     windows.Handle
	Background windows.Handle
	MenuName   *uint16
	ClassName  *uint16
	IconSm     windows.Handle
}

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
		Allowed        []string `json:"allowed_instance_ids"`
		AllowedDevices []string `json:"allowed_devices"`
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

// isMassStorageInstanceID returns true for USB/SCSI removable mass-storage-like IDs.
// It is intentionally permissive to include mobile HDD/SSD device chains.
func isMassStorageInstanceID(rawID string) bool {
	id := strings.ToUpper(strings.TrimSpace(sanitizeDeviceID(rawID)))
	if id == "" {
		return false
	}
	// Explicitly exclude known internal/non-removable buses.
	if strings.Contains(id, "NVME") || strings.Contains(id, "SATA") || strings.Contains(id, "RAID") || strings.Contains(id, "AHCI") {
		return false
	}
	// Common patterns seen in Windows PnP IDs for removable storage.
	if strings.Contains(id, "USBSTOR") {
		return true
	}
	if strings.HasPrefix(id, `SCSI\DISK`) || strings.Contains(id, `\SCSI\DISK`) {
		return true
	}
	if strings.Contains(id, "MASS") && strings.Contains(id, "STORAGE") {
		return true
	}
	// Some USB disk IDs appear as USB\VID_xxxx&PID_xxxx
	if strings.Contains(id, `USB\VID_`) && strings.Contains(id, "PID_") {
		return true
	}
	return false
}

func looksLikeInternalStorage(rawID, caption, interfaceType string) bool {
	id := strings.ToUpper(strings.TrimSpace(sanitizeDeviceID(rawID)))
	cp := strings.ToUpper(strings.TrimSpace(caption))
	_ = interfaceType // WMI InterfaceType is unreliable for USB vs internal; never use alone to skip.
	if strings.Contains(id, "NVME") || strings.Contains(cp, "NVME") {
		return true
	}
	// Do NOT treat InterfaceType=="IDE" as internal: USB/SAS/ATA disks often report IDE/SCSI in WMI.
	if strings.Contains(id, "SATA") || strings.Contains(cp, "SATA") {
		return true
	}
	if strings.Contains(id, "RAID") || strings.Contains(cp, "RAID") {
		return true
	}
	return false
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
		pollInterval = 15 * time.Second
	}

	// Track by kernel disk device number so PNP string changes (PHYSICALDRIVE\N -> USBSTOR\...)
	// do not create duplicate "new device" churn or leave known=0 while devices are present.
	knownDevNum := make(map[uint32]struct{})
	lastUSBID := make(map[uint32]string)
	lastVol := make(map[uint32]string)
	lastProduct := make(map[uint32]string)
	// Avoid remove+reinsert churn when WMI/enumeration briefly drops a disk between scans.
	firstMissingAt := make(map[uint32]time.Time)
	var mu sync.Mutex
	const blockWindow = 3 * time.Second
	const removeMissingGrace = 4 * time.Second
	blockedUntil := make(map[uint32]time.Time)

	applyScan := func(trigger string) {
		usbDebugf(logger, "applyScan trigger=%s", trigger)
		present, err := listExternalStorageByDeviceNumber(logger)
		if err != nil {
			logger.Debug("[USBDBG] usb monitor list query error", "err", err)
			usbDebugf(logger, "applyScan trigger=%s query_error=%v", trigger, err)
			return
		}
		now := time.Now()
		currentByDev := make(map[uint32]usbDiskPresent, len(present))
		for _, p := range present {
			currentByDev[p.DevNum] = p
		}

		mu.Lock()
		defer mu.Unlock()

		if trigger == "device-notify" {
			for devNum := range knownDevNum {
				if _, still := currentByDev[devNum]; still {
					continue
				}
				id := strings.TrimSpace(lastUSBID[devNum])
				vol := strings.TrimSpace(lastVol[devNum])
				prod := strings.TrimSpace(lastProduct[devNum])
				delete(knownDevNum, devNum)
				delete(lastUSBID, devNum)
				delete(lastVol, devNum)
				delete(lastProduct, devNum)
				delete(firstMissingAt, devNum)
				delete(blockedUntil, devNum)
				if onEvent != nil && id != "" {
					usbDebugf(logger, "device-notify purge stale devNum=%d usb_id=%s", devNum, id)
					onEvent(models.USBEvent{
						Action:      "remove",
						USBID:       id,
						VolumeName:  vol,
						ProductName: prod,
						Timestamp:   now.Unix(),
					})
				}
			}
			firstMissingAt = make(map[uint32]time.Time)
			blockedUntil = make(map[uint32]time.Time)
		}

		// Same devNum may initially report synthetic PHYSICALDRIVE\N then WMI association fills USBSTOR\...
		for devNum, p := range currentByDev {
			if _, ok := knownDevNum[devNum]; !ok {
				continue
			}
			id := strings.TrimSpace(p.USBID)
			vol := strings.TrimSpace(p.Vol)
			prev := strings.TrimSpace(lastUSBID[devNum])
			if prev != "" && normalizeInstanceIDKey(prev) != normalizeInstanceIDKey(id) && onEvent != nil {
				np, pp := usbPNPInstancePriority(id), usbPNPInstancePriority(prev)
				if np < pp {
					usbDebugf(logger, "ignore id downgrade devNum=%d keep=%s scan_id=%s pri=%d<%d", devNum, prev, id, np, pp)
					if vol != "" {
						lastVol[devNum] = vol
					}
					if strings.TrimSpace(p.ProductName) != "" {
						lastProduct[devNum] = strings.TrimSpace(p.ProductName)
					}
					continue
				}
				usbDebugf(logger, "emit id-swap devNum=%d %s -> %s vol=%s", devNum, prev, id, vol)
				onEvent(models.USBEvent{
					Action:      "remove",
					USBID:       prev,
					VolumeName:  lastVol[devNum],
					ProductName: lastProduct[devNum],
					Timestamp:   now.Unix(),
				})
				onEvent(models.USBEvent{
					Action:      "insert",
					USBID:       id,
					VolumeName:  vol,
					ProductName: strings.TrimSpace(p.ProductName),
					Timestamp:   now.Unix(),
				})
				lastUSBID[devNum] = id
				lastVol[devNum] = vol
				lastProduct[devNum] = strings.TrimSpace(p.ProductName)
			}
		}

		for devNum, p := range currentByDev {
			if _, ok := knownDevNum[devNum]; ok {
				continue
			}
			if until := blockedUntil[devNum]; !until.IsZero() && now.Before(until) {
				continue
			}
			func() {
				cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
				defer cancel()
				if ids := fetchAllowedInstanceIDsOnce(cctx); len(ids) > 0 {
					SetUSBPolicy(USBPolicy{AllowedInstanceIDs: ids})
				}
			}()
			id := strings.TrimSpace(p.USBID)
			vol := strings.TrimSpace(p.Vol)
			if !IsUSBInstanceAllowed(id) {
				logger.Debug("[USBDBG] policy blocking unauthorized device", "usb_id", id)
				knownDevNum[devNum] = struct{}{}
				lastUSBID[devNum] = id
				lastVol[devNum] = vol
				lastProduct[devNum] = strings.TrimSpace(p.ProductName)
				if onEvent != nil {
					usbDebugf(logger, "emit insert (unauthorized, audit) usb_id=%s volume=%s devNum=%d trigger=%s", id, vol, devNum, trigger)
					onEvent(models.USBEvent{
						Action:      "insert",
						USBID:       id,
						VolumeName:  vol,
						ProductName: strings.TrimSpace(p.ProductName),
						Timestamp:   now.Unix(),
					})
				}
				_ = removeUSBDeviceByInstanceID(id)
				blockedUntil[devNum] = now.Add(blockWindow)
				continue
			}
			knownDevNum[devNum] = struct{}{}
			lastUSBID[devNum] = id
			lastVol[devNum] = vol
			lastProduct[devNum] = strings.TrimSpace(p.ProductName)
			if onEvent != nil {
				usbDebugf(logger, "emit insert usb_id=%s volume=%s devNum=%d trigger=%s", id, vol, devNum, trigger)
				onEvent(models.USBEvent{
					Action:      "insert",
					USBID:       id,
					VolumeName:  vol,
					ProductName: strings.TrimSpace(p.ProductName),
					Timestamp:   now.Unix(),
				})
			}
		}

		for devNum := range knownDevNum {
			if _, ok := currentByDev[devNum]; ok {
				delete(firstMissingAt, devNum)
				continue
			}
			t0, started := firstMissingAt[devNum]
			if !started {
				firstMissingAt[devNum] = now
				usbDebugf(logger, "devNum=%d absent start grace=%s trigger=%s", devNum, removeMissingGrace, trigger)
				continue
			}
			if now.Sub(t0) < removeMissingGrace {
				continue
			}
			delete(firstMissingAt, devNum)
			id := lastUSBID[devNum]
			vol := lastVol[devNum]
			prod := lastProduct[devNum]
			delete(knownDevNum, devNum)
			delete(lastUSBID, devNum)
			delete(lastVol, devNum)
			delete(lastProduct, devNum)
			delete(blockedUntil, devNum)
			if onEvent != nil {
				usbDebugf(logger, "emit remove usb_id=%s volume=%s devNum=%d trigger=%s", id, vol, devNum, trigger)
				onEvent(models.USBEvent{
					Action:      "remove",
					USBID:       id,
					VolumeName:  vol,
					ProductName: prod,
					Timestamp:   now.Unix(),
				})
			}
		}
		usbDebugf(logger, "applyScan done trigger=%s current=%d known=%d", trigger, len(currentByDev), len(knownDevNum))
	}

	applyScan("startup")

	evtCh := make(chan struct{}, 8)
	notifyErr := startDeviceNotificationLoop(ctx, evtCh)
	if notifyErr != nil {
		logger.Warn("usb monitor event mode unavailable, fallback to polling", "err", notifyErr)
	}

	if notifyErr != nil {
		t := time.NewTicker(pollInterval)
		go func() {
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					applyScan("poll-fallback")
				}
			}
		}()
		return nil
	}

	go func() {
		// Keep very slow fallback tick for resilience while primarily event-driven.
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-evtCh:
				applyScan("device-notify")
			case <-t.C:
				applyScan("resync-ticker")
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

// usbPNPInstancePriority ranks PnP instance IDs so we keep a stable audit key per physical disk
// (DeviceNumber) and drop synthetic PHYSICALDRIVE\N when WMI later exposes USBSTOR\...
func usbPNPInstancePriority(id string) int {
	u := strings.ToUpper(strings.TrimSpace(sanitizeDeviceID(id)))
	switch {
	case strings.HasPrefix(u, `USBSTOR\`):
		return 100
	case strings.Contains(u, `USB\VID_`) && strings.Contains(u, `PID_`):
		return 95
	case strings.Contains(u, `USB\`):
		return 85
	case strings.HasPrefix(u, `SCSI\`):
		return 50
	case strings.HasPrefix(u, `PHYSICALDRIVE\`):
		return 10
	default:
		return 40
	}
}

type usbDiskByDevNum struct {
	id      string
	vol     string
	product string
	pri     int
}

// usbDiskPresent is one USB-attached disk volume for the monitor state machine.
type usbDiskPresent struct {
	DevNum      uint32
	USBID       string
	Vol         string
	ProductName string
}

// usbDebugf writes USB trace lines to the agent slog logger (debug.log when log level is debug).
func usbDebugf(logger *slog.Logger, format string, args ...any) {
	if logger == nil {
		return
	}
	logger.Debug(fmt.Sprintf("[USBDBG] "+format, args...))
}

// wmiPNPDeviceIDByDiskNumber matches Win32_DiskDrive rows to STORAGE_DEVICE_NUMBER.DeviceNumber
// (often same as \\.\PhysicalDriveN index). Helps when association queries miss but WMI already lists the disk.
func wmiPNPDeviceIDByDiskNumber(logger *slog.Logger, diskNumber uint32) string {
	type diskRow struct {
		PNPDeviceID *string
		DeviceID    *string
		Name        *string
		Index       *uint32
	}
	var disks []diskRow
	err := wmi.Query("SELECT PNPDeviceID, DeviceID, Name, Index FROM Win32_DiskDrive", &disks)
	if err != nil {
		usbDebugf(logger, "wmi Win32_DiskDrive+Index failed: %v, retry without Index", err)
		type diskRowNoIdx struct {
			PNPDeviceID *string
			DeviceID    *string
			Name        *string
		}
		var disks2 []diskRowNoIdx
		if err2 := wmi.Query("SELECT PNPDeviceID, DeviceID, Name FROM Win32_DiskDrive", &disks2); err2 != nil {
			usbDebugf(logger, "wmi Win32_DiskDrive (by disk number) failed: %v", err2)
			return ""
		}
		needle := fmt.Sprintf("PHYSICALDRIVE%d", diskNumber)
		for _, d := range disks2 {
			if d.PNPDeviceID == nil {
				continue
			}
			pnp := strings.TrimSpace(*d.PNPDeviceID)
			if pnp == "" {
				continue
			}
			if d.Name != nil && strings.Contains(strings.ToUpper(*d.Name), needle) {
				usbDebugf(logger, "wmi upgrade by Name PhysicalDrive%d -> pnp=%s", diskNumber, sanitizeDeviceID(pnp))
				return pnp
			}
			if d.DeviceID != nil && strings.Contains(strings.ToUpper(*d.DeviceID), needle) {
				usbDebugf(logger, "wmi upgrade by DeviceID PhysicalDrive%d -> pnp=%s", diskNumber, sanitizeDeviceID(pnp))
				return pnp
			}
		}
		return ""
	}
	needle := fmt.Sprintf("PHYSICALDRIVE%d", diskNumber)
	for _, d := range disks {
		if d.PNPDeviceID == nil {
			continue
		}
		pnp := strings.TrimSpace(*d.PNPDeviceID)
		if pnp == "" {
			continue
		}
		if d.Index != nil && *d.Index == diskNumber {
			usbDebugf(logger, "wmi upgrade by Index=%d -> pnp=%s", diskNumber, sanitizeDeviceID(pnp))
			return pnp
		}
		if d.Name != nil && strings.Contains(strings.ToUpper(*d.Name), needle) {
			usbDebugf(logger, "wmi upgrade by Name PhysicalDrive%d -> pnp=%s", diskNumber, sanitizeDeviceID(pnp))
			return pnp
		}
		if d.DeviceID != nil && strings.Contains(strings.ToUpper(*d.DeviceID), needle) {
			usbDebugf(logger, "wmi upgrade by DeviceID PhysicalDrive%d -> pnp=%s", diskNumber, sanitizeDeviceID(pnp))
			return pnp
		}
	}
	return ""
}

func upgradeSyntheticPNPIDs(logger *slog.Logger, byDevNum map[uint32]*usbDiskByDevNum) {
	for devNum, ent := range byDevNum {
		if ent == nil {
			continue
		}
		u := strings.ToUpper(ent.id)
		if !strings.HasPrefix(u, `PHYSICALDRIVE\`) {
			continue
		}
		// Volume labels must never be inferred via WMI partition associations; PNP upgrade uses disk number only.
		real := wmiPNPDeviceIDByDiskNumber(logger, devNum)
		if real != "" {
			usbDebugf(logger, "upgrade devNum=%d id %s -> %s (volume %s)", devNum, ent.id, sanitizeDeviceID(real), ent.vol)
			ent.id = sanitizeDeviceID(real)
			ent.pri = usbPNPInstancePriority(ent.id)
		}
	}
}

// mergeUSBByDevNum keeps one logical row per STORAGE_DEVICE_NUMBER.DeviceNumber.
func mergeUSBByDevNum(logger *slog.Logger, m map[uint32]*usbDiskByDevNum, devNum uint32, pnp, vol, product string) {
	id := sanitizeDeviceID(pnp)
	pri := usbPNPInstancePriority(id)
	prod := strings.TrimSpace(product)
	ex, ok := m[devNum]
	if !ok {
		m[devNum] = &usbDiskByDevNum{id: id, vol: strings.TrimSpace(vol), product: prod, pri: pri}
		return
	}
	if pri > ex.pri {
		if ex.id != id {
			usbDebugf(logger, "merge devNum=%d prefer id=%s pri=%d over id=%s pri=%d", devNum, id, pri, ex.id, ex.pri)
		}
		ex.id = id
		ex.pri = pri
	}
	v := strings.TrimSpace(vol)
	if v != "" && ex.vol == "" {
		ex.vol = v
	}
	if prod != "" && ex.product == "" {
		ex.product = prod
	}
}

func listExternalStorageByDeviceNumber(logger *slog.Logger) ([]usbDiskPresent, error) {
	usedVolume := make(map[string]struct{})
	seenDevNum := make(map[uint32]struct{})
	byDevNum := make(map[uint32]*usbDiskByDevNum)
	usbDebugf(logger, "listExternalStorageByDeviceNumber enter")

	type win32DiskDrive struct {
		Index         uint32
		PNPDeviceID   *string
		Caption       *string
		InterfaceType *string
	}
	var drives []win32DiskDrive
	if err := wmi.Query("SELECT Index,PNPDeviceID,Caption,InterfaceType FROM Win32_DiskDrive", &drives); err != nil {
		usbDebugf(logger, "wmi query Win32_DiskDrive failed: %v", err)
		return nil, err
	}
	usbDebugf(logger, "Win32_DiskDrive count=%d", len(drives))

	var maxDiskIndex uint32
	for _, d := range drives {
		if d.Index > maxDiskIndex {
			maxDiskIndex = d.Index
		}
	}
	seenPhysicalIndex := make(map[uint32]struct{})

	for _, d := range drives {
		seenPhysicalIndex[d.Index] = struct{}{}
		physicalPath := fmt.Sprintf(`\\.\PhysicalDrive%d`, d.Index)
		busType, hotplug, devType, devNum, productHint, err := queryPhysicalStorageInfo(logger, physicalPath)
		if err != nil {
			usbDebugf(logger, "queryPhysicalStorageInfo failed path=%s err=%v", physicalPath, err)
			continue
		}
		// Bus-type gate first: ignore SATA/NVMe/etc. before any PnP merge or candidate logging.
		if busType != busTypeUSB && busType != busTypeUAS {
			continue
		}
		seenDevNum[devNum] = struct{}{}

		pnp := ""
		if d.PNPDeviceID != nil {
			pnp = strings.TrimSpace(*d.PNPDeviceID)
		}
		if pnp == "" {
			pnp = fmt.Sprintf(`PHYSICALDRIVE\%d`, d.Index)
			usbDebugf(logger, "synthetic pnp index=%d (WMI empty) bus-gated USB/UAS", d.Index)
		}
		caption := ""
		if d.Caption != nil {
			caption = strings.TrimSpace(*d.Caption)
		}
		iface := ""
		if d.InterfaceType != nil {
			iface = strings.TrimSpace(*d.InterfaceType)
		}
		if looksLikeInternalStorage(pnp, caption, iface) {
			usbDebugf(logger, "skip disk index=%d reason=internal_storage pnp=%s iface=%s caption=%s", d.Index, sanitizeDeviceID(pnp), iface, caption)
			continue
		}
		if !isMassStorageInstanceID(pnp) && !strings.EqualFold(iface, "USB") {
			usbDebugf(logger, "weak_mass_storage_match index=%d pnp=%s iface=%s (bus already USB/UAS)", d.Index, sanitizeDeviceID(pnp), iface)
		}

		usbDebugf(logger, "candidate index=%d pnp=%s bus=%d hotplug=%v devType=%d devNum=%d iface=%s product=%q", d.Index, sanitizeDeviceID(pnp), busType, hotplug, devType, devNum, iface, productHint)
		if logger != nil {
			logger.Debug("[USBDBG] usb storage candidate",
				"device_id", sanitizeDeviceID(pnp),
				"bus_type", busType,
				"device_type", devType,
				"interface_type", iface,
				"hotplug", hotplug,
			)
		}
		if devType != fileDeviceDisk {
			usbDebugf(logger, "skip device_id=%s reason=not_disk devType=%d", sanitizeDeviceID(pnp), devType)
			continue
		}
		vol := mapVolumeByDeviceNumber(logger, devNum)
		if vol != "" {
			if _, exists := usedVolume[vol]; exists {
				usbDebugf(logger, "suppress duplicate volume volume=%s device_id=%s", vol, sanitizeDeviceID(pnp))
				vol = ""
			} else {
				usedVolume[vol] = struct{}{}
			}
		}
		mergeUSBByDevNum(logger, byDevNum, devNum, pnp, vol, productHint)
	}

	// WMI sometimes omits disks that still exist as \\.\PhysicalDriveN (e.g. timing or filter quirks).
	maxSweep := int(maxDiskIndex) + 8
	if maxSweep < 16 {
		maxSweep = 16
	}
	if maxSweep > 64 {
		maxSweep = 64
	}
	for i := 0; i < maxSweep; i++ {
		ui := uint32(i)
		if _, ok := seenPhysicalIndex[ui]; ok {
			continue
		}
		path := fmt.Sprintf(`\\.\PhysicalDrive%d`, i)
		busType, _, devType, devNum, productHint, err := queryPhysicalStorageInfo(logger, path)
		if err != nil {
			continue
		}
		if busType != busTypeUSB && busType != busTypeUAS {
			continue
		}
		if _, dup := seenDevNum[devNum]; dup {
			continue
		}
		seenDevNum[devNum] = struct{}{}
		seenPhysicalIndex[ui] = struct{}{}
		usbDebugf(logger, "physicaldrive-sweep index=%d bus=%d devType=%d devNum=%d product=%q", i, busType, devType, devNum, productHint)
		if devType != fileDeviceDisk {
			continue
		}
		pnp := ""
		caption := ""
		iface := ""
		for _, dd := range drives {
			if dd.Index == ui {
				if dd.PNPDeviceID != nil {
					pnp = strings.TrimSpace(*dd.PNPDeviceID)
				}
				if dd.Caption != nil {
					caption = strings.TrimSpace(*dd.Caption)
				}
				if dd.InterfaceType != nil {
					iface = strings.TrimSpace(*dd.InterfaceType)
				}
				break
			}
		}
		if pnp == "" {
			pnp = fmt.Sprintf(`PHYSICALDRIVE\%d`, i)
			usbDebugf(logger, "sweep synthetic pnp=%s (no WMI row for this index)", pnp)
		}
		if looksLikeInternalStorage(pnp, caption, iface) {
			usbDebugf(logger, "sweep skip internal_storage pnp=%s", sanitizeDeviceID(pnp))
			continue
		}
		vol := mapVolumeByDeviceNumber(logger, devNum)
		if vol != "" {
			if _, exists := usedVolume[vol]; exists {
				usbDebugf(logger, "sweep suppress duplicate volume=%s device_id=%s", vol, sanitizeDeviceID(pnp))
				vol = ""
			} else {
				usedVolume[vol] = struct{}{}
			}
		}
		mergeUSBByDevNum(logger, byDevNum, devNum, pnp, vol, productHint)
		ent := byDevNum[devNum]
		if ent != nil {
			usbDebugf(logger, "sweep merged devNum=%d usb_id=%s volume=%s", devNum, ent.id, ent.vol)
		}
	}

	upgradeSyntheticPNPIDs(logger, byDevNum)

	present := make([]usbDiskPresent, 0, len(byDevNum))
	for devNum, ent := range byDevNum {
		if ent == nil || ent.id == "" {
			continue
		}
		present = append(present, usbDiskPresent{DevNum: devNum, USBID: ent.id, Vol: ent.vol, ProductName: ent.product})
	}

	mappedVol := 0
	for _, p := range present {
		if p.Vol != "" {
			mappedVol++
		}
	}
	usbDebugf(logger, "listExternalStorageByDeviceNumber done devices=%d mapped_volumes=%d", len(present), mappedVol)
	return present, nil
}

func queryPhysicalStorageInfo(logger *slog.Logger, path string) (uint32, bool, uint32, uint32, string, error) {
	h, err := windows.CreateFile(windows.StringToUTF16Ptr(path), windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return 0, false, 0, 0, "", err
	}
	defer windows.CloseHandle(h)

	devType, devNum, err := queryDeviceNumberByHandle(h)
	if err != nil {
		return 0, false, 0, 0, "", err
	}
	buf, busType, err := queryStorageDescriptorBuffer(logger, h)
	if err != nil {
		return 0, false, 0, 0, "", err
	}
	productHint := descriptorInquiryLabel(buf)
	hotplug := queryHotplugByHandle(h)
	return busType, hotplug, devType, devNum, productHint, nil
}

// queryStorageDeviceNumberBytes runs IOCTL_STORAGE_GET_DEVICE_NUMBER and decodes the
// 12-byte STORAGE_DEVICE_NUMBER layout explicitly (little-endian). Match uses
// DeviceNumber (bytes 4–7), never PartitionNumber (bytes 8–11).
func queryStorageDeviceNumberBytes(h windows.Handle) (deviceType, deviceNumber, partitionNumber uint32, err error) {
	var buf [12]byte
	var br uint32
	if err := windows.DeviceIoControl(h, ioctlStorageGetDeviceNum, nil, 0, &buf[0], uint32(len(buf)), &br, nil); err != nil {
		return 0, 0, 0, err
	}
	if br < uint32(len(buf)) {
		return 0, 0, 0, fmt.Errorf("STORAGE_DEVICE_NUMBER short read: got %d want %d", br, len(buf))
	}
	deviceType = binary.LittleEndian.Uint32(buf[0:4])
	deviceNumber = binary.LittleEndian.Uint32(buf[4:8])
	partitionNumber = binary.LittleEndian.Uint32(buf[8:12])
	return deviceType, deviceNumber, partitionNumber, nil
}

func queryDeviceNumberByHandle(h windows.Handle) (uint32, uint32, error) {
	dt, dn, _, err := queryStorageDeviceNumberBytes(h)
	return dt, dn, err
}

func queryStorageDescriptorBuffer(logger *slog.Logger, h windows.Handle) ([]byte, uint32, error) {
	query := storagePropertyQuery{PropertyID: 0, QueryType: 0}
	var head storageDescriptorHeader
	var br uint32
	if err := windows.DeviceIoControl(
		h,
		ioctlStorageQueryProperty,
		(*byte)(unsafe.Pointer(&query)),
		uint32(unsafe.Sizeof(query)),
		(*byte)(unsafe.Pointer(&head)),
		uint32(unsafe.Sizeof(head)),
		&br,
		nil,
	); err != nil {
		return nil, 0, err
	}
	size := head.Size
	if size < 36 {
		size = 64
	}
	buf := make([]byte, size)
	if err := windows.DeviceIoControl(
		h,
		ioctlStorageQueryProperty,
		(*byte)(unsafe.Pointer(&query)),
		uint32(unsafe.Sizeof(query)),
		&buf[0],
		uint32(len(buf)),
		&br,
		nil,
	); err != nil {
		return nil, 0, err
	}
	off := int(unsafe.Offsetof(storageDeviceDescriptor{}.BusType))
	if len(buf) <= off+3 {
		return nil, 0, errors.New("descriptor too small")
	}
	var busType uint32
	if off+4 <= len(buf) {
		busType = binary.LittleEndian.Uint32(buf[off : off+4])
		if busType > 32 {
			busType = uint32(buf[off])
		}
	} else {
		busType = uint32(buf[off])
	}
	usbDebugf(logger, "STORAGE_DEVICE_DESCRIPTOR offset(busType)=%d arch=%s size=%d busType=%d", off, runtime.GOARCH, len(buf), busType)
	dumpLen := 64
	if len(buf) < dumpLen {
		dumpLen = len(buf)
	}
	if dumpLen > 0 {
		usbDebugf(logger, "STORAGE_DEVICE_DESCRIPTOR first%d=%s", dumpLen, strings.ToUpper(hex.EncodeToString(buf[:dumpLen])))
	}
	if len(buf) > 28 && off != 28 {
		usbDebugf(logger, "BusType offset mismatch unsafe=%d raw28=%d", off, uint32(buf[28]))
	}
	return buf, busType, nil
}

func readDescriptorCString(buf []byte, off int) string {
	if off <= 0 || off >= len(buf) {
		return ""
	}
	end := bytes.IndexByte(buf[off:], 0)
	if end < 0 {
		return strings.TrimSpace(string(buf[off:]))
	}
	return strings.TrimSpace(string(buf[off : off+end]))
}

// descriptorInquiryLabel returns "Vendor Product" from STORAGE_DEVICE_DESCRIPTOR string fields.
func descriptorInquiryLabel(buf []byte) string {
	if len(buf) < 20 {
		return ""
	}
	vOff := int(binary.LittleEndian.Uint32(buf[12:16]))
	pOff := int(binary.LittleEndian.Uint32(buf[16:20]))
	ven := readDescriptorCString(buf, vOff)
	prod := readDescriptorCString(buf, pOff)
	switch {
	case ven != "" && prod != "":
		return strings.TrimSpace(ven + " " + prod)
	case prod != "":
		return prod
	case ven != "":
		return ven
	default:
		return ""
	}
}

func queryHotplugByHandle(h windows.Handle) bool {
	buf := make([]byte, 16)
	var br uint32
	if err := windows.DeviceIoControl(h, ioctlStorageGetHotplugInfo, nil, 0, &buf[0], uint32(len(buf)), &br, nil); err != nil {
		return false
	}
	// STORAGE_HOTPLUG_INFO booleans are after Size field.
	for i := 4; i < len(buf); i++ {
		if buf[i] != 0 {
			return true
		}
	}
	return false
}

func queryDosDeviceTarget(letter string) (string, bool) {
	if strings.TrimSpace(letter) == "" {
		return "", false
	}
	name := strings.TrimSuffix(strings.TrimSpace(letter), ":") + ":"
	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return "", false
	}
	buf := make([]uint16, 1024)
	r0, _, _ := procQueryDosDeviceW.Call(
		uintptr(unsafe.Pointer(namePtr)),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
	)
	if r0 == 0 {
		return "", false
	}
	n := 0
	for n < len(buf) && buf[n] != 0 {
		n++
	}
	return windows.UTF16ToString(buf[:n]), true
}

func mapVolumeByDeviceNumber(logger *slog.Logger, target uint32) string {
	// Enumerate A–Z via QueryDosDevice; collect every letter whose IOCTL STORAGE_GET_DEVICE_NUMBER
	// DeviceNumber matches this disk. No WMI; no full-machine volume snapshot.
	var foundVolumes []string
	for i := 0; i < 26; i++ {
		letter := string(rune('A' + i))
		drive := letter + ":"
		if _, ok := queryDosDeviceTarget(letter); !ok {
			continue
		}
		volPath := `\\.\` + drive
		h, err := windows.CreateFile(windows.StringToUTF16Ptr(volPath), windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
		if err != nil {
			continue
		}
		_, currentDiskNum, partNum, numErr := queryStorageDeviceNumberBytes(h)
		_ = windows.CloseHandle(h)
		if numErr != nil {
			continue
		}
		usbDebugf(logger, "Checking Drive %s: TargetNum=%d, CurrentDriveNum=%d, PartitionNum=%d", drive, target, currentDiskNum, partNum)
		if currentDiskNum == target {
			usbDebugf(logger, "matched volume=%s device_number(disk)=%d", drive, currentDiskNum)
			foundVolumes = append(foundVolumes, drive)
		}
	}
	if len(foundVolumes) == 0 {
		usbDebugf(logger, "mapVolumeByDeviceNumber: no letter matched TargetNum=%d (return empty)", target)
		return ""
	}
	sort.Strings(foundVolumes)
	return strings.Join(foundVolumes, ", ")
}

func startDeviceNotificationLoop(ctx context.Context, trigger chan<- struct{}) error {
	ready := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		className, _ := windows.UTF16PtrFromString("GoAgentUSBWatcherClass")
		var hwnd windows.Handle
		wndProc := syscall.NewCallback(func(h windows.Handle, msg uint32, wParam, lParam uintptr) uintptr {
			switch msg {
			case wmDeviceChange:
				if wParam == dbtDeviceArrival || wParam == dbtDeviceRemoveComplete {
					select {
					case trigger <- struct{}{}:
					default:
					}
				}
				return 0
			case wmClose:
				procDestroyWindow.Call(uintptr(h))
				return 0
			case wmDestroy:
				procPostQuitMessage.Call(0)
				return 0
			default:
				r, _, _ := procDefWindowProcW.Call(uintptr(h), uintptr(msg), wParam, lParam)
				return r
			}
		})

		wc := wndClassEx{
			Size:      uint32(unsafe.Sizeof(wndClassEx{})),
			WndProc:   wndProc,
			Instance:  0,
			ClassName: className,
		}
		atom, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
		if atom == 0 {
			ready <- err
			return
		}
		hwndRaw, _, cerr := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(className)), 0, 0, 0, 0, 0, 0, 0, 0, 0)
		if hwndRaw == 0 {
			ready <- cerr
			return
		}
		hwnd = windows.Handle(hwndRaw)

		register := func(g windows.GUID) uintptr {
			f := devBroadcastDeviceInterface{
				Size:       uint32(unsafe.Sizeof(devBroadcastDeviceInterface{})),
				DeviceType: dbtDevTypeDeviceIface,
				ClassGuid:  g,
			}
			h, _, _ := procRegisterDeviceNotification.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&f)), deviceNotifyWindowHandle)
			return h
		}
		handles := []uintptr{
			register(guidDevInterfaceUSBDevice),
			register(guidDevInterfaceDisk),
			register(guidDevInterfaceStorage),
		}
		ready <- nil

		go func(localH windows.Handle) {
			<-ctx.Done()
			procPostMessageW.Call(uintptr(localH), wmClose, 0, 0)
		}(hwnd)

		var m msg
		for {
			ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
			if int32(ret) <= 0 {
				break
			}
			procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
			procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
		}
		for _, h := range handles {
			if h != 0 {
				procUnregisterDeviceNotif.Call(h)
			}
		}
	}()
	return <-ready
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
		// Only force-remove devices whose Instance ID indicates removable mass storage.
		if !isMassStorageInstanceID(instanceID) {
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
