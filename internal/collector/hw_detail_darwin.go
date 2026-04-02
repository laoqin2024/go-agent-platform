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
			// Capacity string can be embedded differently; try multiple keys
			capStr := firstStr(m, "SPMemoryDataType", "dimm_size", "Size", "memory")
			if banks, ok := m["_items"].([]any); ok && len(banks) > 0 && capStr == "" {
				if bm, ok := banks[0].(map[string]any); ok {
					if v := firstStr(bm, "dimm_size", "Size"); v != "" {
						capStr = v
					}
					if v := firstStr(bm, "dimm_manufacturer", "Manufacturer"); v != "" {
						manu = v
					}
					if v := firstStr(bm, "dimm_type", "Type"); v != "" {
						memType = v
					}
				}
			}
			slots = append(slots, RamInfo{
				Slot:         "BANK 0",
				SizeBytes:    parseMemSize(capStr),
				SpeedMHz:     0,
				Manufacturer: manu,
				Type:         memType,
			})
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

