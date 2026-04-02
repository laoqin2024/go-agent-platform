//go:build linux

package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func collectPhysicalDisks(ctx context.Context) ([]DiskInfo, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cmdCtx, "lsblk", "-J", "-o", "NAME,MODEL,SERIAL,SIZE,ROTA,TYPE,TRAN").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("lsblk failed: %w", err)
	}
	var root struct {
		Blockdevices []map[string]any `json:"blockdevices"`
	}
	if err := json.Unmarshal(out, &root); err != nil {
		return nil, err
	}
	var disks []DiskInfo
	for _, dev := range root.Blockdevices {
		typ := strings.ToLower(fmt.Sprint(dev["type"]))
		if typ != "disk" && typ != "nvme" {
			continue
		}
		model := fmt.Sprint(dev["model"])
		serial := fmt.Sprint(dev["serial"])
		sizeStr := fmt.Sprint(dev["size"])
		rota := fmt.Sprint(dev["rota"])
		tran := fmt.Sprint(dev["tran"])
		driveType := "Unknown"
		if rota == "0" {
			driveType = "SSD"
		} else if rota == "1" {
			driveType = "HDD"
		}
		bus := strings.ToUpper(tran)
		if bus == "" && typ == "nvme" {
			bus = "NVMe"
		}
		disks = append(disks, DiskInfo{
			Model:     strings.TrimSpace(model),
			Serial:    strings.TrimSpace(serial),
			SizeBytes: parseUint(sizeStr),
			BusType:   bus,
			DriveType: driveType,
		})
	}
	return disks, nil
}

func collectMemorySlots(ctx context.Context) ([]RamInfo, error) {
	// Best-effort: dmidecode may not be available; we avoid it.
	// Many distros expose limited DMI via /sys/class/dmi/id but not per-slot.
	// Return empty slice rather than blocking or requiring root.
	return []RamInfo{}, nil
}

func collectGPUs(ctx context.Context) ([]GPUInfo, error) {
	// 1) Try sysfs uevent for primary card0
	uevent := "/sys/class/drm/card0/device/uevent"
	if b, err := os.ReadFile(uevent); err == nil {
		lines := strings.Split(string(b), "\n")
		var model string
		for _, ln := range lines {
			if strings.HasPrefix(ln, "DRIVER=") {
				// e.g. amdgpu, i915, nouveau, nvidia
				driver := strings.TrimPrefix(ln, "DRIVER=")
				model = strings.ToUpper(driver)
			}
			if strings.HasPrefix(ln, "PCI_ID=") && model == "" {
				model = strings.TrimPrefix(ln, "PCI_ID=")
			}
		}
		if model != "" {
			return []GPUInfo{{Model: model, VRAMMB: 0}}, nil
		}
	}
	// 2) Fallback to lspci | grep -i vga
	cmdCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cmdCtx, "sh", "-c", "lspci | grep -i 'vga\\|3d\\|display' -m 1").CombinedOutput()
	if err == nil {
		line := strings.TrimSpace(string(out))
		// typical: "00:02.0 VGA compatible controller: Intel Corporation UHD Graphics 630 (Mobile)"
		if idx := strings.Index(line, ":"); idx >= 0 && idx+1 < len(line) {
			model := strings.TrimSpace(line[idx+1:])
			return []GPUInfo{{Model: model, VRAMMB: 0}}, nil
		}
		if line != "" {
			return []GPUInfo{{Model: line, VRAMMB: 0}}, nil
		}
	}
	return []GPUInfo{}, nil
}

func collectMainboard(ctx context.Context) (*MainboardInfo, error) {
	read := func(p string) string {
		b, err := os.ReadFile(filepath.Clean(p))
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(b))
	}
	vendor := read("/sys/class/dmi/id/board_vendor")
	name := read("/sys/class/dmi/id/board_name")
	ver := read("/sys/class/dmi/id/board_version")
	if vendor == "" && name == "" && ver == "" {
		return nil, nil
	}
	return &MainboardInfo{
		Manufacturer: vendor,
		Model:        name,
		Version:      ver,
	}, nil
}

