//go:build windows

package collector

import (
	"context"
	"log"
	"fmt"
	"regexp"
	"strings"
	"time"

	"golang.org/x/sys/windows/registry"
)

func collectSoftwareWithContext(ctx context.Context) ([]SoftwareInfo, error) {
	const (
		uninstallPath      = `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`
		uninstallPathWOW32 = `SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall`
	)

	type hivePath struct {
		root registry.Key
		path string
	}
	paths := []hivePath{
		{registry.LOCAL_MACHINE, uninstallPath},
		{registry.LOCAL_MACHINE, uninstallPathWOW32},
		{registry.CURRENT_USER, uninstallPath},
		{registry.CURRENT_USER, uninstallPathWOW32}, // may not exist; handle gracefully
	}

	apps := make([]SoftwareInfo, 0, 512)
	seen := make(map[string]struct{}, 1024) // dedup by Name+Version

	for _, hp := range paths {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		k, err := registry.OpenKey(hp.root, hp.path, registry.READ|registry.ENUMERATE_SUB_KEYS)
		if err != nil {
			continue
		}
		subNames, err := k.ReadSubKeyNames(-1)
		if err != nil {
			_ = k.Close()
			continue
		}

		for _, sub := range subNames {
			select {
			case <-ctx.Done():
				_ = k.Close()
				return nil, ctx.Err()
			default:
			}
			sk, err := registry.OpenKey(hp.root, hp.path+`\`+sub, registry.READ)
			if err != nil {
				continue
			}

			displayName, _, _ := sk.GetStringValue("DisplayName")
			name := strings.TrimSpace(displayName)
			if name == "" {
				// 回退使用子键名
				name = strings.TrimSpace(sub)
			}
			// 宽松过滤：只要有名字就保留
			if name == "" {
				_ = sk.Close()
				continue
			}

			// 过滤系统组件（如果有标记）
			if v, _, err := sk.GetIntegerValue("SystemComponent"); err == nil && v != 0 {
				_ = sk.Close()
				continue
			}

			version, _, _ := sk.GetStringValue("DisplayVersion")
			publisher, _, _ := sk.GetStringValue("Publisher")
			if strings.TrimSpace(publisher) == "" {
				publisher, _, _ = sk.GetStringValue("DisplayPublisher")
			}
			installDateStr, _, _ := sk.GetStringValue("InstallDate")
			var installDate *time.Time
			if strings.TrimSpace(installDateStr) != "" {
				if t, ok := parseWindowsInstallDate(installDateStr); ok {
					installDate = &t
				}
			}

			key := fmt.Sprintf("%s|%s", strings.ToLower(name), strings.TrimSpace(version))
			if _, ok := seen[key]; ok {
				_ = sk.Close()
				continue
			}
			seen[key] = struct{}{}

			apps = append(apps, SoftwareInfo{
				Name:        name,
				Version:     strings.TrimSpace(version),
				Publisher:   strings.TrimSpace(publisher),
				InstallDate: installDate,
			})
			_ = sk.Close()
		}
		_ = k.Close()
	}

	log.Printf("Windows 软件扫描完成，共发现 %d 个应用", len(apps))
	return apps, nil
}

func parseWindowsInstallDate(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	// Common format: "YYYYMMDD" (e.g. 20240301).
	if matched, _ := regexp.MatchString(`^\d{8}$`, s); matched {
		t, err := time.Parse("20060102", s)
		if err == nil {
			return t, true
		}
	}
	// Fallback: try locale-like dates.
	for _, layout := range []string{"2006-01-02", "01/02/2006", "1/2/2006"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

