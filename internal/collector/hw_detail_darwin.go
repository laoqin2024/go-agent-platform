//go:build darwin

package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func collectPhysicalDisks(ctx context.Context) ([]DiskInfo, error) {
	nvmeSerialIdx := buildNVMeSerialIndex(ctx)
	cmdCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cmdCtx, "system_profiler", "SPStorageDataType", "-json").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("system_profiler SPStorageDataType failed: %w", err)
	}
	if len(out) > 0 {
		s := string(out)
		if len(s) > 200 {
			s = s[:200]
		}
		log.Printf("debug: SPStorageDataType head: %q", s)
	}
	var root map[string]any
	if err := json.Unmarshal(out, &root); err != nil {
		return nil, err
	}
	arr, _ := root["SPStorageDataType"].([]any)
	var disks []DiskInfo
	for _, it := range arr {
		m, _ := it.(map[string]any)
		if m == nil {
			continue
		}
		// top-level volume info
		volName := firstStr(m, "_name", "volumename")
		bsdName := firstStr(m, "bsd_name")
		bus := firstStr(m, "bus_protocol", "BusProtocol")
		mediaType := firstStr(m, "medium_type")
		// Prefer size_in_bytes if present; fallback to human string in size/Size
		sizeStrBytes := firstStr(m, "size_in_bytes")
		sizeStrHuman := firstStr(m, "size", "Size")
		size := uint64(0)
		if v := parseUint(sizeStrBytes); v > 0 {
			size = v
		} else if v := parseUint(sizeStrHuman); v > 0 {
			size = v
		}
		// Not used yet by UI, but parse free space for completeness
		_ = firstStr(m, "free_space_in_bytes")
		model := firstStr(m, "device_name")
		serial := firstStr(m, "serial_number")

		// nested physical_drive has richer details
		if pdRaw, ok := m["physical_drive"]; ok {
			if pd, ok := pdRaw.(map[string]any); ok {
				if v := firstStr(pd, "device_name"); v != "" {
					model = v
				}
				if v := firstStr(pd, "serial_number"); v != "" {
					serial = v
				}
				if v := firstStr(pd, "medium_type"); v != "" {
					mediaType = v
				}
				if v := firstStr(pd, "bus_protocol"); v != "" {
					bus = v
				}
				// Some outputs include only a human size string here
				if size == 0 {
					if v := firstStr(pd, "size", "Size"); v != "" {
						if parsed := parseUint(v); parsed > 0 {
							size = parsed
						}
					}
				}
			}
		}

		driveType := "Unknown"
		lm := strings.ToLower(mediaType)
		if strings.Contains(lm, "solid") || strings.Contains(lm, "ssd") {
			driveType = "SSD"
		} else if mediaType != "" {
			driveType = "HDD"
		}

		// Prefer physical device model; fallback to volume/bsd name
		if model == "" {
			model = volName
		}
		if model == "" {
			model = bsdName
		}
		if size == 0 && bsdName != "" {
			size = queryDiskSizeFromDiskutil(ctx, bsdName)
		}
		if serial == "" {
			serial = queryDiskSerialFromDiskutil(ctx, bsdName)
		}
		if serial == "" && model != "" {
			serial = nvmeSerialIdx[strings.ToLower(strings.TrimSpace(model))]
		}

		disks = append(disks, DiskInfo{
			Model:     model,
			Serial:    serial,
			SizeBytes: size,
			BusType:   bus,
			DriveType: driveType,
		})
	}
	return disks, nil
}

func collectMemorySlots(ctx context.Context) ([]RamInfo, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cmdCtx, "system_profiler", "SPMemoryDataType", "-json").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("system_profiler SPMemoryDataType failed: %w", err)
	}
	if len(out) > 0 {
		s := string(out)
		if len(s) > 200 {
			s = s[:200]
		}
		log.Printf("debug: SPMemoryDataType head: %q", s)
	}
	var root map[string]any
	if err := json.Unmarshal(out, &root); err != nil {
		return nil, err
	}
	arr, _ := root["SPMemoryDataType"].([]any)
	var slots []RamInfo
	if len(arr) > 0 {
		m, _ := arr[0].(map[string]any)
		if m != nil {
			manu := firstStr(m, "dimm_manufacturer", "Manufacturer")
			memType := firstStr(m, "dimm_type", "Type")
			speed := parseMHz(firstStr(m, "dimm_speed", "Speed"))
			// Capacity string can be embedded differently; try multiple keys
			capStr := firstStr(m, "SPMemoryDataType", "dimm_size", "Size", "memory")
			if banks, ok := m["_items"].([]any); ok && len(banks) > 0 {
				for i, b := range banks {
					bm, ok := b.(map[string]any)
					if !ok {
						continue
					}
					slot := firstStr(bm, "dimm_name", "slot", "_name")
					if slot == "" {
						slot = fmt.Sprintf("BANK %d", i)
					}
					bCap := firstStr(bm, "dimm_size", "Size")
					if bCap == "" {
						bCap = capStr
					}
					bManu := firstStr(bm, "dimm_manufacturer", "Manufacturer")
					if bManu == "" {
						bManu = manu
					}
					bType := firstStr(bm, "dimm_type", "Type")
					if bType == "" {
						bType = memType
					}
					bSpeed := parseMHz(firstStr(bm, "dimm_speed", "Speed"))
					if bSpeed == 0 {
						bSpeed = speed
					}
					slots = append(slots, RamInfo{
						Slot:         slot,
						SizeBytes:    parseMemSize(bCap),
						SpeedMHz:     uint32(bSpeed),
						Manufacturer: bManu,
						Type:         bType,
					})
				}
			}
			if len(slots) == 0 {
				slots = append(slots, RamInfo{
					Slot:         "BANK 0",
					SizeBytes:    parseMemSize(capStr),
					SpeedMHz:     uint32(speed),
					Manufacturer: manu,
					Type:         memType,
				})
			}
		}
	}
	return slots, nil
}

func collectGPUs(ctx context.Context) ([]GPUInfo, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cmdCtx, "system_profiler", "SPDisplaysDataType", "-json").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("system_profiler SPDisplaysDataType failed: %w", err)
	}
	if len(out) > 0 {
		s := string(out)
		if len(s) > 200 {
			s = s[:200]
		}
		log.Printf("debug: SPDisplaysDataType head: %q", s)
	}
	var root map[string]any
	if err := json.Unmarshal(out, &root); err != nil {
		return nil, err
	}
	arr, _ := root["SPDisplaysDataType"].([]any)
	var gpus []GPUInfo
	if len(arr) > 0 {
		m, _ := arr[0].(map[string]any)
		if m != nil {
			model := firstStr(m, "_name", "sppci_model", "chipset_model", "spdisplays_device")
			// Try several VRAM keys; Apple Silicon may be unified memory
			vram := firstStr(m, "spdisplays_vram", "spdisplays_vram_shared", "spdisplays_videoram_vram", "VRAM")
			vramMB := parseVRAMMB(vram)
			if vramMB == 0 && model != "" {
				// Mark unified memory by appending a suffix to the model for UI clarity
				model = model + " (统一内存)"
			}
			gpus = append(gpus, GPUInfo{
				Model:  model,
				VRAMMB: vramMB,
			})
		}
	}
	return gpus, nil
}

func collectPartitions(ctx context.Context) ([]PartitionInfo, error) {
	// Combine df -kP for mount sizes with 'mount' for fstype
	type mountInfo struct {
		fs   string
		mp   string
		fst  string
		size uint64
		free uint64
	}
	infos := map[string]*mountInfo{}

	// df -kP: Filesystem 512-blocks Used Available Capacity Mounted on
	dfCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(dfCtx, "df", "-kP").CombinedOutput()
	lines := strings.Split(string(out), "\n")
	for i, ln := range lines {
		if i == 0 || strings.TrimSpace(ln) == "" {
			continue
		}
		// Normalize multiple spaces
		f := strings.Fields(ln)
		// Expected: FS, 1K-blocks, Used, Available, Capacity, Mountpoint
		if len(f) < 6 {
			continue
		}
		fs := f[0]
		sizeKB, _ := strconv.ParseUint(f[1], 10, 64)
		availKB, _ := strconv.ParseUint(f[3], 10, 64)
		mp := f[len(f)-1]
		mi := &mountInfo{fs: fs, mp: mp, size: sizeKB * 1024, free: availKB * 1024}
		infos[mp] = mi
	}
	// 'mount' has fstype in parentheses
	mntCtx, cancel2 := context.WithTimeout(ctx, 5*time.Second)
	defer cancel2()
	mout, _ := exec.CommandContext(mntCtx, "sh", "-c", "mount").CombinedOutput()
	for _, ln := range strings.Split(string(mout), "\n") {
		// e.g. /dev/disk3s1 on / (apfs, local, read-only, journaled)
		if !strings.Contains(ln, " on ") || !strings.Contains(ln, " (") {
			continue
		}
		parts := strings.SplitN(ln, " on ", 2)
		if len(parts) != 2 {
			continue
		}
		left := parts[0]
		right := parts[1]
		mp := strings.TrimSpace(strings.SplitN(right, " (", 2)[0])
		ft := ""
		if i := strings.Index(right, "("); i >= 0 && strings.Contains(right, ")") {
			inside := right[i+1 : strings.Index(right, ")")]
			// first token is fstype typically
			ft = strings.TrimSpace(strings.Split(inside, ",")[0])
		}
		// Bind if exists by mountpoint; else create with size 0
		mi, ok := infos[mp]
		if !ok {
			mi = &mountInfo{fs: left, mp: mp}
			infos[mp] = mi
		}
		if mi.fst == "" {
			mi.fst = ft
		}
	}
	var parts []PartitionInfo
	for _, mi := range infos {
		if mi.mp == "" || mi.fs == "" {
			continue
		}
		parts = append(parts, PartitionInfo{
			Name:       mi.fs,
			Mountpoint: mi.mp,
			FSType:     mi.fst,
			SizeBytes:  mi.size,
			FreeBytes:  mi.free,
		})
	}
	return parts, nil
}

// helper to optionally read Apple chip type (e.g., "Apple M4") from SPHardwareDataType
func appleChipType(ctx context.Context) string {
	cmdCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cmdCtx, "system_profiler", "SPHardwareDataType", "-json").CombinedOutput()
	if err != nil || len(out) == 0 {
		return ""
	}
	var root map[string]any
	if err := json.Unmarshal(out, &root); err != nil {
		return ""
	}
	arr, _ := root["SPHardwareDataType"].([]any)
	if len(arr) == 0 {
		return ""
	}
	if m, ok := arr[0].(map[string]any); ok {
		return firstStr(m, "chip_type", "ChipType")
	}
	return ""
}

func collectMainboard(ctx context.Context) (*MainboardInfo, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cmdCtx, "system_profiler", "SPHardwareDataType", "-json").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("system_profiler SPHardwareDataType failed: %w", err)
	}
	if len(out) > 0 {
		s := string(out)
		if len(s) > 200 {
			s = s[:200]
		}
		log.Printf("debug: SPHardwareDataType head: %q", s)
	}
	var root map[string]any
	if err := json.Unmarshal(out, &root); err != nil {
		return nil, err
	}
	arr, _ := root["SPHardwareDataType"].([]any)
	if len(arr) == 0 {
		return nil, nil
	}
	m, _ := arr[0].(map[string]any)
	if m == nil {
		return nil, nil
	}
	manu := firstStr(m, "machine_name", "Manufacturer")
	model := firstStr(m, "model_number", "ModelNumber", "model_identifier", "ModelIdentifier", "model-id", "board-id", "board_id")
	ver := firstStr(m, "boot_rom_version", "BootROMVersion")
	serial := firstStr(m, "serial_number", "SerialNumber")
	return &MainboardInfo{
		Manufacturer: manu,
		Model:        model,
		Version:      ver,
		Serial:       serial,
	}, nil
}

func firstStr(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return s
			}
		}
	}
	return ""
}

func parseUint(s string) uint64 {
	s = strings.TrimSpace(strings.ReplaceAll(s, ",", ""))
	if s == "" {
		return 0
	}
	// If string includes explicit "bytes", prefer the largest integer fragment as bytes.
	// Example: "500.3 GB (500277790720 bytes)"
	if strings.Contains(strings.ToLower(s), "byte") {
		maxDigits := ""
		cur := ""
		for i := 0; i < len(s); i++ {
			c := s[i]
			if c >= '0' && c <= '9' {
				cur += string(c)
				continue
			}
			if len(cur) > len(maxDigits) {
				maxDigits = cur
			}
			cur = ""
		}
		if len(cur) > len(maxDigits) {
			maxDigits = cur
		}
		if maxDigits != "" {
			if v, err := strconv.ParseUint(maxDigits, 10, 64); err == nil {
				return v
			}
		}
	}
	// try plain number
	if v, err := strconv.ParseUint(s, 10, 64); err == nil {
		return v
	}
	// try like "500 GB"
	parts := strings.Fields(s)
	if len(parts) >= 2 {
		if v, err := strconv.ParseFloat(parts[0], 64); err == nil {
			unit := strings.ToUpper(parts[1])
			mult := float64(1)
			switch unit {
			case "KB":
				mult = 1 << 10
			case "MB":
				mult = 1 << 20
			case "GB":
				mult = 1 << 30
			case "TB":
				mult = 1 << 40
			}
			return uint64(v * mult)
		}
	}
	return 0
}

func queryDiskSizeFromDiskutil(ctx context.Context, bsdName string) uint64 {
	cmdCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cmdCtx, "diskutil", "info", bsdName).CombinedOutput()
	if err != nil {
		return 0
	}
	lines := strings.Split(string(out), "\n")
	for _, ln := range lines {
		l := strings.ToLower(strings.TrimSpace(ln))
		if strings.Contains(l, "disk size:") || strings.Contains(l, "total size:") {
			parts := strings.SplitN(ln, ":", 2)
			if len(parts) == 2 {
				if v := parseUint(parts[1]); v > 0 {
					return v
				}
			}
			if v := parseUint(ln); v > 0 {
				return v
			}
		}
	}
	return 0
}

func queryDiskSerialFromDiskutil(ctx context.Context, bsdName string) string {
	if strings.TrimSpace(bsdName) == "" {
		return ""
	}
	cmdCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cmdCtx, "diskutil", "info", bsdName).CombinedOutput()
	if err != nil {
		return ""
	}
	lines := strings.Split(string(out), "\n")
	for _, ln := range lines {
		l := strings.ToLower(strings.TrimSpace(ln))
		if !strings.Contains(l, "serial") {
			continue
		}
		parts := strings.SplitN(ln, ":", 2)
		if len(parts) != 2 {
			continue
		}
		v := strings.TrimSpace(parts[1])
		if v != "" && v != "-" {
			return v
		}
	}
	return ""
}

func buildNVMeSerialIndex(ctx context.Context) map[string]string {
	idx := map[string]string{}
	cmdCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cmdCtx, "system_profiler", "SPNVMeDataType", "-json").CombinedOutput()
	if err != nil || len(out) == 0 {
		return idx
	}
	var root map[string]any
	if err := json.Unmarshal(out, &root); err != nil {
		return idx
	}
	arr, _ := root["SPNVMeDataType"].([]any)
	for _, it := range arr {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		model := firstStr(m, "_name", "device_name", "spsnvme_model")
		serial := findSerialInMap(m)
		if model != "" && serial != "" {
			idx[strings.ToLower(strings.TrimSpace(model))] = serial
		}
		if items, ok := m["_items"].([]any); ok {
			for _, sub := range items {
				sm, ok := sub.(map[string]any)
				if !ok {
					continue
				}
				subModel := firstStr(sm, "_name", "device_name", "spsnvme_model")
				subSerial := findSerialInMap(sm)
				if subSerial == "" {
					subSerial = serial
				}
				if subModel != "" && subSerial != "" {
					idx[strings.ToLower(strings.TrimSpace(subModel))] = subSerial
				}
			}
		}
	}
	return idx
}

func findSerialInMap(m map[string]any) string {
	for k, v := range m {
		if !strings.Contains(strings.ToLower(k), "serial") {
			continue
		}
		if s, ok := v.(string); ok {
			s = strings.TrimSpace(s)
			if s != "" && s != "-" {
				return s
			}
		}
	}
	return ""
}

func parseMemSize(s string) uint64 {
	return parseUint(s)
}

func parseMHz(s string) int {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimSuffix(s, " mhz")
	v, _ := strconv.Atoi(strings.TrimSpace(s))
	return v
}

func parseVRAMMB(s string) uint64 {
	// Examples: "4096 MB", "1.5 GB"
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	parts := strings.Fields(s)
	if len(parts) >= 2 {
		if v, err := strconv.ParseFloat(parts[0], 64); err == nil {
			unit := strings.ToUpper(parts[1])
			mult := float64(1)
			switch unit {
			case "KB":
				mult = 1.0 / 1024.0
			case "MB":
				mult = 1
			case "GB":
				mult = 1024
			}
			return uint64(v * mult)
		}
	}
	return 0
}

