//go:build windows
// +build windows

package collector

import (
	"context"
	"encoding/binary"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// Windows implementations using Registry and Win32 APIs (no WMI).

func collectPhysicalDisks(ctx context.Context) ([]DiskInfo, error) {
	// Prefer IOCTL_STORAGE_QUERY_PROPERTY (stable) and use registry only as fallback.
	regDisks := readDiskModelSerialFromRegistry()

	typeFlag := map[int]string{}
	for i := 0; i < 32; i++ {
		select {
		case <-ctx.Done():
			i = 32
			break
		default:
		}
		drivePath := fmt.Sprintf(`\\.\PhysicalDrive%d`, i)
		rot, ok := isRotationalDrive(drivePath)
		if !ok {
			continue
		}
		if rot {
			typeFlag[i] = "HDD"
		} else {
			typeFlag[i] = "SSD"
		}
	}

	var out []DiskInfo
	for i := 0; i < 32; i++ {
		md, ok := regDisks[i]
		path := fmt.Sprintf(`\\.\PhysicalDrive%d`, i)

		desc, _ := queryStorageDeviceDescriptor(path)
		model := ""
		serial := ""
		bus := ""
		if desc != nil {
			model = strings.TrimSpace(strings.TrimSpace(desc.vendor + " " + desc.product))
			serial = strings.TrimSpace(desc.serial)
			bus = busTypeToString(desc.busType)
		}
		// fallback to registry if IOCTL didn't give anything
		if model == "" && ok {
			model = strings.TrimSpace(md.model)
		}
		if serial == "" && ok {
			serial = strings.TrimSpace(md.serial)
		}
		if bus == "" {
			bus = queryBusTypeString(path)
		}

		if model == "" && serial == "" && bus == "" && typeFlag[i] == "" && !ok {
			continue
		}
		size := getDiskLengthBytes(path)
		out = append(out, DiskInfo{
			Model:     model,
			Serial:    serial,
			SizeBytes: size,
			BusType:   bus,
			DriveType: typeFlag[i],
		})
	}
	return out, nil
}

func collectMemorySlots(ctx context.Context) ([]RamInfo, error) {
	// Prefer SMBIOS Type17 (Memory Device) for per-slot details, fallback to total.
	table, err := getRawSMBIOSTable()
	if err == nil && len(table) > 0 {
		if slots := parseSMBIOSMemoryDevices(table); len(slots) > 0 {
			return slots, nil
		}
	}

	total := getPhysicallyInstalledSystemMemory()
	if total <= 0 {
		return nil, nil
	}
	return []RamInfo{{Slot: "SYSTEM", SizeBytes: total * 1024}}, nil
}

func collectGPUs(ctx context.Context) ([]GPUInfo, error) {
	// GPU via Display Class registry
	const classPath = `SYSTEM\CurrentControlSet\Control\Class\{4d36e968-e325-11ce-bfc1-08002be10318}`
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, classPath, registry.READ)
	if err != nil {
		return nil, nil
	}
	defer k.Close()
	subkeys, err := k.ReadSubKeyNames(-1)
	if err != nil {
		return nil, nil
	}
	var gpus []GPUInfo
	for _, sk := range subkeys {
		// Expect numeric like 0000, 0001...
		if len(sk) != 4 {
			continue
		}
		skKey, err := registry.OpenKey(k, sk, registry.READ)
		if err != nil {
			continue
		}
		desc, _, _ := skKey.GetStringValue("DriverDesc")
		ramDword, _, _ := skKey.GetIntegerValue("HardwareInformation.MemorySize")
		skKey.Close()
		if desc == "" && ramDword == 0 {
			continue
		}
		gpus = append(gpus, GPUInfo{
			Model:  desc,
			VRAMMB: uint64(ramDword) / (1024 * 1024),
		})
	}
	return gpus, nil
}

func collectMainboard(ctx context.Context) (*MainboardInfo, error) {
	// Baseboard via BIOS registry
	const biosPath = `HARDWARE\DESCRIPTION\System\BIOS`
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, biosPath, registry.READ)
	if err != nil {
		return nil, nil
	}
	defer k.Close()
	manu, _, _ := k.GetStringValue("BaseBoardManufacturer")
	product, _, _ := k.GetStringValue("BaseBoardProduct")
	version, _, _ := k.GetStringValue("BaseBoardVersion")
	serial, _, _ := k.GetStringValue("BaseBoardSerialNumber")
	if manu == "" && product == "" && version == "" && serial == "" {
		return nil, nil
	}
	return &MainboardInfo{
		Manufacturer: manu,
		Model:        product,
		Version:      version,
		Serial:       serial,
	}, nil
}

func collectPartitions(ctx context.Context) ([]PartitionInfo, error) {
	// Use WMIC for broad compatibility (works on Win7+). Output is locale-independent in list format.
	cmdCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cmdCtx, "wmic", "logicaldisk", "get", "DeviceID,FileSystem,Size,FreeSpace", "/format:list").CombinedOutput()
	if err != nil {
		return nil, nil // best-effort; don't fail inventory if WMIC not present
	}
	blocks := strings.Split(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n\n")
	var parts []PartitionInfo
	for _, b := range blocks {
		lines := strings.Split(b, "\n")
		var id, fs string
		var size, free uint64
		for _, ln := range lines {
			ln = strings.TrimSpace(ln)
			if ln == "" {
				continue
			}
			if strings.HasPrefix(ln, "DeviceID=") {
				id = strings.TrimPrefix(ln, "DeviceID=")
			} else if strings.HasPrefix(ln, "FileSystem=") {
				fs = strings.TrimPrefix(ln, "FileSystem=")
			} else if strings.HasPrefix(ln, "Size=") {
				if v, err := strconv.ParseUint(strings.TrimPrefix(ln, "Size="), 10, 64); err == nil {
					size = v
				}
			} else if strings.HasPrefix(ln, "FreeSpace=") {
				if v, err := strconv.ParseUint(strings.TrimPrefix(ln, "FreeSpace="), 10, 64); err == nil {
					free = v
				}
			}
		}
		if id == "" {
			continue
		}
		mp := id + `\`
		parts = append(parts, PartitionInfo{
			Name:       id,
			Mountpoint: mp,
			FSType:     fs,
			SizeBytes:  size,
			FreeBytes:  free,
		})
	}
	return parts, nil
}

// --- Helpers ---

type diskRegInfo struct {
	model  string
	serial string
}

func readDiskModelSerialFromRegistry() map[int]diskRegInfo {
	result := map[int]diskRegInfo{}
	// Probe SCSI and STORAGE enumerations
	for _, base := range []string{
		`SYSTEM\CurrentControlSet\Enum\SCSI`,
		`SYSTEM\CurrentControlSet\Enum\STORAGE`,
		`SYSTEM\CurrentControlSet\Enum\IDE`,
	} {
		k, err := registry.OpenKey(registry.LOCAL_MACHINE, base, registry.READ)
		if err != nil {
			continue
		}
		vendors, _ := k.ReadSubKeyNames(-1)
		for _, v := range vendors {
			vk, err := registry.OpenKey(k, v, registry.READ)
			if err != nil {
				continue
			}
			prods, _ := vk.ReadSubKeyNames(-1)
			for _, p := range prods {
				pk, err := registry.OpenKey(vk, p, registry.READ)
				if err != nil {
					continue
				}
				insts, _ := pk.ReadSubKeyNames(-1)
				for _, ins := range insts {
					ik, err := registry.OpenKey(pk, ins, registry.READ)
					if err != nil {
						continue
					}
					model, _, _ := ik.GetStringValue("FriendlyName")
					if model == "" {
						model, _, _ = ik.GetStringValue("DeviceDesc")
					}
					serial, _, _ := ik.GetStringValue("SerialNumber")
					// Try to parse PhysicalDrive index from LocationInformation or similar
					loc, _, _ := ik.GetStringValue("LocationInformation")
					index := parsePhysicalIndex(loc)
					if _, exists := result[index]; !exists && index >= 0 {
						result[index] = diskRegInfo{model: model, serial: serial}
					}
					ik.Close()
				}
				pk.Close()
			}
			vk.Close()
		}
		k.Close()
	}
	return result
}

func parsePhysicalIndex(s string) int {
	// Heuristic: find "...PhysicalDriveN" or trailing number
	if i := strings.Index(strings.ToLower(s), "physicaldrive"); i >= 0 {
		num := strings.TrimSpace(s[i+len("physicaldrive"):])
		if n, err := strconv.Atoi(num); err == nil {
			return n
		}
	}
	// fallback: find last number in string
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] < '0' || s[i] > '9' {
			if i+1 < len(s) {
				if n, err := strconv.Atoi(s[i+1:]); err == nil {
					return n
				}
			}
			break
		}
	}
	return -1
}

// Storage property query for seek penalty (SSD/HDD)
const (
	IOCTL_STORAGE_QUERY_PROPERTY = 0x2D1400
	IOCTL_DISK_GET_LENGTH_INFO   = 0x0007405C
)

type STORAGE_PROPERTY_ID uint32
type STORAGE_QUERY_TYPE uint32

const (
	StorageDeviceSeekPenaltyProperty STORAGE_PROPERTY_ID = 7
	StorageDeviceProperty            STORAGE_PROPERTY_ID = 0
	PropertyStandardQuery            STORAGE_QUERY_TYPE  = 0
)

type STORAGE_PROPERTY_QUERY struct {
	PropertyId STORAGE_PROPERTY_ID
	QueryType  STORAGE_QUERY_TYPE
	Additional [1]byte
}

type DEVICE_SEEK_PENALTY_DESCRIPTOR struct {
	Version           uint32
	Size              uint32
	IncursSeekPenalty uint8
}

type STORAGE_DESCRIPTOR_HEADER struct {
	Version uint32
	Size    uint32
}

type STORAGE_DEVICE_DESCRIPTOR struct {
	Version               uint32
	Size                  uint32
	DeviceType            byte
	DeviceTypeModifier    byte
	RemovableMedia        byte
	CommandQueueing       byte
	VendorIdOffset        uint32
	ProductIdOffset       uint32
	ProductRevisionOffset uint32
	SerialNumberOffset    uint32
	BusType               uint32 // STORAGE_BUS_TYPE
	RawPropertiesLength   uint32
	// followed by RawDeviceProperties[1]
}

type storageDevDesc struct {
	vendor  string
	product string
	serial  string
	busType uint32
}

func isRotationalDrive(path string) (bool, bool) {
	// returns (rotational, ok)
	p16, _ := windows.UTF16PtrFromString(path)
	h, err := windows.CreateFile(p16, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return false, false
	}
	defer windows.CloseHandle(h)

	var query STORAGE_PROPERTY_QUERY
	query.PropertyId = StorageDeviceSeekPenaltyProperty
	query.QueryType = PropertyStandardQuery
	var bytesReturned uint32
	var desc DEVICE_SEEK_PENALTY_DESCRIPTOR
	err = windows.DeviceIoControl(h, IOCTL_STORAGE_QUERY_PROPERTY,
		(*byte)(unsafe.Pointer(&query)), uint32(unsafe.Sizeof(query)),
		(*byte)(unsafe.Pointer(&desc)), uint32(unsafe.Sizeof(desc)),
		&bytesReturned, nil)
	if err != nil {
		return false, false
	}
	// IncursSeekPenalty == 1 -> HDD (rotational)
	return desc.IncursSeekPenalty != 0, true
}

func queryStorageDeviceDescriptor(path string) (*storageDevDesc, bool) {
	p16, _ := windows.UTF16PtrFromString(path)
	h, err := windows.CreateFile(p16, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return nil, false
	}
	defer windows.CloseHandle(h)

	var hdr STORAGE_DESCRIPTOR_HEADER
	q := STORAGE_PROPERTY_QUERY{PropertyId: StorageDeviceProperty, QueryType: PropertyStandardQuery}
	var br uint32
	err = windows.DeviceIoControl(h, IOCTL_STORAGE_QUERY_PROPERTY,
		(*byte)(unsafe.Pointer(&q)), uint32(unsafe.Sizeof(q)),
		(*byte)(unsafe.Pointer(&hdr)), uint32(unsafe.Sizeof(hdr)),
		&br, nil)
	if err != nil || hdr.Size == 0 || hdr.Size > 16*1024 {
		return nil, false
	}
	buf := make([]byte, hdr.Size)
	err = windows.DeviceIoControl(h, IOCTL_STORAGE_QUERY_PROPERTY,
		(*byte)(unsafe.Pointer(&q)), uint32(unsafe.Sizeof(q)),
		&buf[0], uint32(len(buf)),
		&br, nil)
	if err != nil || br < uint32(unsafe.Sizeof(STORAGE_DEVICE_DESCRIPTOR{})) {
		return nil, false
	}
	desc := (*STORAGE_DEVICE_DESCRIPTOR)(unsafe.Pointer(&buf[0]))
	out := &storageDevDesc{
		vendor:  readDescString(buf, desc.VendorIdOffset),
		product: readDescString(buf, desc.ProductIdOffset),
		serial:  readDescString(buf, desc.SerialNumberOffset),
		busType: desc.BusType,
	}
	return out, true
}

func readDescString(buf []byte, off uint32) string {
	if off == 0 || int(off) >= len(buf) {
		return ""
	}
	b := buf[off:]
	// descriptor strings are ANSI, NUL-terminated
	n := 0
	for n < len(b) && b[n] != 0 {
		n++
	}
	return strings.TrimSpace(string(b[:n]))
}

// Disk size via IOCTL_DISK_GET_LENGTH_INFO (bytes)
func getDiskLengthBytes(path string) uint64 {
	p16, _ := windows.UTF16PtrFromString(path)
	h, err := windows.CreateFile(p16, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return 0
	}
	defer windows.CloseHandle(h)
	type lengthInfo struct {
		Length int64
	}
	var out lengthInfo
	var br uint32
	err = windows.DeviceIoControl(h, IOCTL_DISK_GET_LENGTH_INFO,
		nil, 0,
		(*byte)(unsafe.Pointer(&out)), uint32(unsafe.Sizeof(out)),
		&br, nil)
	if err != nil {
		return 0
	}
	if out.Length < 0 {
		return 0
	}
	return uint64(out.Length)
}

// Map STORAGE_BUS_TYPE to string
func busTypeToString(t uint32) string {
	switch t {
	case 1:
		return "SCSI"
	case 2:
		return "ATAPI"
	case 3:
		return "ATA"
	case 4:
		return "IEEE1394"
	case 5:
		return "SSA"
	case 6:
		return "Fibre"
	case 7:
		return "USB"
	case 8:
		return "RAID"
	case 9:
		return "iSCSI"
	case 10:
		return "SAS"
	case 11:
		return "SATA"
	case 12:
		return "SD"
	case 13:
		return "MMC"
	case 14:
		return "Virtual"
	case 15:
		return "FileBackedVirtual"
	case 17:
		return "NVMe"
	default:
		return ""
	}
}

// Query StorageDeviceProperty to get BusType
func queryBusTypeString(path string) string {
	p16, _ := windows.UTF16PtrFromString(path)
	h, err := windows.CreateFile(p16, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)

	var hdr STORAGE_DESCRIPTOR_HEADER
	q := STORAGE_PROPERTY_QUERY{PropertyId: StorageDeviceProperty, QueryType: PropertyStandardQuery}
	var br uint32
	err = windows.DeviceIoControl(h, IOCTL_STORAGE_QUERY_PROPERTY,
		(*byte)(unsafe.Pointer(&q)), uint32(unsafe.Sizeof(q)),
		(*byte)(unsafe.Pointer(&hdr)), uint32(unsafe.Sizeof(hdr)),
		&br, nil)
	if err != nil || hdr.Size == 0 || hdr.Size > 4096 {
		return ""
	}
	buf := make([]byte, hdr.Size)
	err = windows.DeviceIoControl(h, IOCTL_STORAGE_QUERY_PROPERTY,
		(*byte)(unsafe.Pointer(&q)), uint32(unsafe.Sizeof(q)),
		&buf[0], uint32(len(buf)),
		&br, nil)
	if err != nil || br < 40 {
		return ""
	}
	// Interpret as STORAGE_DEVICE_DESCRIPTOR to read BusType
	desc := (*STORAGE_DEVICE_DESCRIPTOR)(unsafe.Pointer(&buf[0]))
	return busTypeToString(desc.BusType)
}

// GetPhysicallyInstalledSystemMemory (in KB)
func getPhysicallyInstalledSystemMemory() uint64 {
	mod := windows.NewLazySystemDLL("kernel32.dll")
	proc := mod.NewProc("GetPhysicallyInstalledSystemMemory")
	if err := mod.Load(); err != nil {
		return 0
	}
	var kb uint64
	r1, _, _ := proc.Call(uintptr(unsafe.Pointer(&kb)))
	if r1 == 0 {
		return 0
	}
	return kb
}

func parseSMBIOSMemoryDevices(table []byte) []RamInfo {
	var out []RamInfo
	walkSMBIOSStructures(table, func(s smbiosStruct) bool {
		if s.typ != 17 || s.length < 0x15 {
			return false
		}

		// Size field at offset 0x0C (2 bytes)
		if len(s.data) < 0x0E {
			return false
		}
		sizeField := binary.LittleEndian.Uint16(s.data[0x0C:0x0E])
		var sizeBytes uint64
		switch sizeField {
		case 0, 0xFFFF:
			sizeBytes = 0
		case 0x7FFF:
			// ExtendedSize at 0x1C (4 bytes) in MB
			if len(s.data) >= 0x20 {
				extMB := binary.LittleEndian.Uint32(s.data[0x1C:0x20])
				sizeBytes = uint64(extMB) * 1024 * 1024
			}
		default:
			// If bit 15 set => size in KB, else MB
			if sizeField&0x8000 != 0 {
				kb := uint64(sizeField &^ 0x8000)
				sizeBytes = kb * 1024
			} else {
				mb := uint64(sizeField)
				sizeBytes = mb * 1024 * 1024
			}
		}

		// Locator strings
		var locator, bank string
		if len(s.data) >= 0x12 {
			locator = smbiosString(s.strs, s.data[0x10])
			bank = smbiosString(s.strs, s.data[0x11])
		}
		slot := strings.TrimSpace(locator)
		if slot == "" {
			slot = strings.TrimSpace(bank)
		}
		if slot == "" {
			slot = "DIMM"
		}

		// Memory type
		memType := ""
		if len(s.data) >= 0x13 {
			memType = smbiosMemoryTypeToString(s.data[0x12])
		}

		// Speed (MHz): prefer ConfiguredMemoryClockSpeed (0x20) if present, else Speed (0x15)
		var speed uint16
		if len(s.data) >= 0x22 {
			speed = binary.LittleEndian.Uint16(s.data[0x20:0x22])
		}
		if speed == 0 && len(s.data) >= 0x17 {
			speed = binary.LittleEndian.Uint16(s.data[0x15:0x17])
		}

		// Manufacturer string index at 0x17 (if present)
		manu := ""
		if len(s.data) >= 0x18 {
			manu = smbiosString(s.strs, s.data[0x17])
		}

		// Skip uninstalled/empty slots (common in VMs with large virtual slot tables).
		// Keep only slots that actually report a non-zero capacity.
		if sizeBytes == 0 {
			return false
		}
		out = append(out, RamInfo{
			Slot:         slot,
			SizeBytes:    sizeBytes,
			SpeedMHz:     uint32(speed),
			Manufacturer: strings.TrimSpace(manu),
			Type:         memType,
		})
		return false
	})
	return out
}