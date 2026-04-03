//go:build darwin

package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

func collectSoftwareWithContext(ctx context.Context) ([]SoftwareInfo, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	// system_profiler SPApplicationsDataType -json
	cmdCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "system_profiler", "SPApplicationsDataType", "-json")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("collector: system_profiler failed: %w: %s", err, strings.TrimSpace(string(out)))
	}

	var root map[string]any
	if err := json.Unmarshal(out, &root); err != nil {
		return nil, fmt.Errorf("collector: invalid system_profiler json: %w", err)
	}

	appItems := extractSystemProfilerApps(root)
	infos := make([]SoftwareInfo, 0, len(appItems))
	seen := make(map[string]struct{}, len(appItems))
	for _, item := range appItems {
		name := firstNonEmptyString(item, "_name", "name", "Name", "title")
		version := firstNonEmptyString(item, "version", "Version", "CFBundleShortVersionString", "CFBundleVersion")
		if strings.TrimSpace(name) == "" {
			continue
		}
		dedupKey := strings.ToLower(strings.TrimSpace(name)) + "|" + strings.TrimSpace(version)
		if _, ok := seen[dedupKey]; ok {
			continue
		}
		seen[dedupKey] = struct{}{}

		publisher := firstNonEmptyString(item, "obtained_from", "ObtainedFrom", "publisher", "Publisher")
		// Try to derive install/update date:
		// 1) lastModified/LastModified if present
		// 2) mdls of bundle path: kMDItemFSContentChangeDate
		// 3) file mtime of bundle path
		var installAt *time.Time
		var when time.Time
		lastModStr := firstNonEmptyString(item, "last_modified", "LastModified", "lastModified")
		if t, ok := parseDarwinTime(lastModStr); ok {
			when = t
			installAt = &when
		} else {
			path := firstNonEmptyString(item, "path", "Path", "location", "Location", "_path")
			if path != "" {
				if t, ok := mdlsContentChangeDate(cmdCtx, path); ok {
					when = t
					installAt = &when
				} else if fi, err := os.Stat(path); err == nil {
					tm := fi.ModTime()
					installAt = &tm
				}
			}
		}
		infos = append(infos, SoftwareInfo{
			Name:        name,
			Version:     version,
			Publisher:   publisher,
			InstallDate: installAt,
		})
	}
	return infos, nil
}

func extractSystemProfilerApps(root map[string]any) []map[string]any {
	// Expected rough shape:
	// {
	//   "SPApplicationsDataType": [
	//      {"_items":[{"_name":"AppName", ...}, ...]}
	//   ]
	// }
	top, ok := root["SPApplicationsDataType"]
	if !ok {
		return nil
	}
	arr, ok := top.([]any)
	if !ok {
		return nil
	}

	var result []map[string]any
	for _, entry := range arr {
		m, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		// 新版/某些环境下 SPApplicationsDataType 可能直接就是 app item 数组。
		if _, ok := m["_name"]; ok {
			result = append(result, m)
			continue
		}
		items, ok := m["_items"].([]any)
		if !ok {
			continue
		}
		for _, it := range items {
			im, ok := it.(map[string]any)
			if !ok {
				continue
			}
			result = append(result, im)
		}
	}
	return result
}

func firstNonEmptyString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch t := v.(type) {
			case string:
				if strings.TrimSpace(t) != "" {
					return t
				}
			case json.Number:
				s := strings.TrimSpace(t.String())
				if s != "" && s != "0" {
					return s
				}
			}
		}
	}
	return ""
}

// parseDarwinTime tries to parse common date strings from system_profiler/mdls.
func parseDarwinTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	// RFC3339
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	// "2006-01-02 15:04:05 -0700"
	if t, err := time.Parse("2006-01-02 15:04:05 -0700", s); err == nil {
		return t, true
	}
	// "2006-01-02 15:04:05 +0000"
	if t, err := time.Parse("2006-01-02 15:04:05 +0000", s); err == nil {
		return t, true
	}
	// "2006-01-02"
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// mdlsContentChangeDate queries kMDItemFSContentChangeDate for a given path.
func mdlsContentChangeDate(ctx context.Context, path string) (time.Time, bool) {
	mdCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(mdCtx, "mdls", "-raw", "-name", "kMDItemFSContentChangeDate", path).CombinedOutput()
	if err != nil {
		return time.Time{}, false
	}
	// Output examples:
	// 2024-03-01 10:20:30 +0000
	str := strings.TrimSpace(string(out))
	if t, ok := parseDarwinTime(str); ok {
		return t, true
	}
	return time.Time{}, false
}
