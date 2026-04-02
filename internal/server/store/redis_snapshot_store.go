package store

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/qinyilin/go-agent/internal/server/model"
	"github.com/redis/go-redis/v9"
)

type RedisSnapshotStore struct {
	client        *redis.Client
	ttl           time.Duration
	snapshotsKey  string // HSET agent:snapshots { device_id => snapshot_json }
	metaKey       string // HSET agent:devices_meta { device_id => meta_json }
	expireKeyPref string // SET agent:snapshot:exp:{device_id} 1 EXPIRE(ttl)
}

func NewRedisSnapshotStore(client *redis.Client, ttl time.Duration) *RedisSnapshotStore {
	return &RedisSnapshotStore{
		client:        client,
		ttl:           ttl,
		snapshotsKey:  "agent:snapshots",
		metaKey:       "agent:devices_meta",
		expireKeyPref: "agent:snapshot:exp:",
	}
}

func (s *RedisSnapshotStore) expireKey(deviceID string) string {
	return s.expireKeyPref + deviceID
}

type storedSnapshot struct {
	Processes    json.RawMessage `json:"processes,omitempty"`
	SoftwareList json.RawMessage `json:"software_list,omitempty"`

	HostMetrics        json.RawMessage `json:"host_metrics,omitempty"`
	HardwareDetails    json.RawMessage `json:"hardware_details,omitempty"`
	SoftwareInventory  json.RawMessage `json:"software_inventory,omitempty"`
	ProcessSnapshot    json.RawMessage `json:"process_snapshot,omitempty"`
	ServiceSnapshot    json.RawMessage `json:"service_snapshot,omitempty"`
	NetworkConnections json.RawMessage `json:"network_connections,omitempty"`
	SecuritySnapshot   json.RawMessage `json:"security_snapshot,omitempty"`

	UpdatedAtSec int64 `json:"updated_at"`
}

// SaveSnapshot persists snapshot into Redis:
// - Data: HSET agent:snapshots device_id snapshot_json
// - TTL: SET agent:snapshot:exp:{device_id} 1 EXPIRE(24h)
func (s *RedisSnapshotStore) SaveSnapshot(ctx context.Context, snap model.SnapshotPush) error {
	if snap.DeviceID == "" {
		return errors.New("redis store: empty device_id")
	}

	stored := storedSnapshot{
		Processes:          json.RawMessage(snap.Processes),
		SoftwareList:       json.RawMessage(snap.SoftwareList),
		HostMetrics:        json.RawMessage(snap.HostMetrics),
		HardwareDetails:    json.RawMessage(snap.HardwareDetails),
		SoftwareInventory:  json.RawMessage(snap.SoftwareInventory),
		ProcessSnapshot:    json.RawMessage(snap.ProcessSnapshot),
		ServiceSnapshot:    json.RawMessage(snap.ServiceSnapshot),
		NetworkConnections: json.RawMessage(snap.NetworkConnections),
		SecuritySnapshot:   json.RawMessage(snap.SecuritySnapshot),
		UpdatedAtSec:       snap.UpdatedAtSec,
	}

	b, err := json.Marshal(stored)
	if err != nil {
		return err
	}

	meta := buildDeviceMeta(snap)
	metaBytes, err := json.Marshal(meta)
	if err != nil {
		return err
	}

	pipe := s.client.Pipeline()
	pipe.HSet(ctx, s.snapshotsKey, snap.DeviceID, string(b))
	pipe.HSet(ctx, s.metaKey, snap.DeviceID, string(metaBytes))
	if s.ttl > 0 {
		pipe.Set(ctx, s.expireKey(snap.DeviceID), "1", s.ttl)
	}
	_, err = pipe.Exec(ctx)
	return err
}

type storedMeta struct {
	DeviceID     string  `json:"device_id"`
	Hostname     string  `json:"hostname,omitempty"`
	OS           string  `json:"os,omitempty"`
	IP           string  `json:"ip,omitempty"`
	MAC          string  `json:"mac,omitempty"`
	IfaceType    string  `json:"iface_type,omitempty"`
	CPUPercent   float64 `json:"cpu_percent,omitempty"`
	MemUsedPct   float64 `json:"mem_used_percent,omitempty"`
	UpdatedAtSec int64   `json:"updated_at,omitempty"`
}

func buildDeviceMeta(snap model.SnapshotPush) storedMeta {
	meta := storedMeta{
		DeviceID:     snap.DeviceID,
		UpdatedAtSec: snap.UpdatedAtSec,
	}

	// Best-effort: parse hostname from host_metrics.
	// Expected patterns we support:
	// - { "hostname": "xxx" }
	// - { "Hostname": "xxx" }
	if len(snap.HostMetrics) > 0 {
		var obj map[string]any
		if err := json.Unmarshal(snap.HostMetrics, &obj); err == nil {
			if v, ok := obj["hostname"]; ok {
				if s, ok := v.(string); ok {
					meta.Hostname = strings.TrimSpace(s)
				}
			}
			if meta.Hostname == "" {
				if v, ok := obj["Hostname"]; ok {
					if s, ok := v.(string); ok {
						meta.Hostname = strings.TrimSpace(s)
					}
				}
			}

			// Best-effort: IP
			// Supported patterns:
			// - { "ip": "x.x.x.x" }
			// - { "local_ip": "x.x.x.x" }
			// - { "ips": ["x.x.x.x", ...] }
			// - { "Network": { "LocalIPs": ["x.x.x.x"] } }
			if meta.IP == "" {
				for _, k := range []string{"ip", "local_ip", "localIp", "IP"} {
					if v, ok := obj[k]; ok {
						if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
							meta.IP = strings.TrimSpace(s)
							break
						}
					}
				}
			}
			if meta.IP == "" {
				if v, ok := obj["ips"]; ok {
					if arr, ok := v.([]any); ok && len(arr) > 0 {
						if s, ok := arr[0].(string); ok && strings.TrimSpace(s) != "" {
							meta.IP = strings.TrimSpace(s)
						}
					}
				}
			}
			if meta.IP == "" {
				if v, ok := obj["Network"]; ok {
					if m2, ok := v.(map[string]any); ok {
						if v2, ok := m2["LocalIPs"]; ok {
							if arr, ok := v2.([]any); ok && len(arr) > 0 {
								if s, ok := arr[0].(string); ok && strings.TrimSpace(s) != "" {
									meta.IP = strings.TrimSpace(s)
								}
							}
						}
					}
				}
			}

			// Best-effort: CPU / Memory used%
			// Supported patterns:
			// - { "cpu_total_percent": 12.3, "memory_used_percent": 45.6 }
			// - { "CPU": { "Total": 12.3 }, "Memory": { "UsedPercent": 45.6 } }
			if meta.CPUPercent == 0 {
				meta.CPUPercent = getFloatAny(obj["cpu_total_percent"])
			}
			if meta.MemUsedPct == 0 {
				meta.MemUsedPct = getFloatAny(obj["memory_used_percent"])
			}
			if cpu, ok := obj["CPU"].(map[string]any); ok && meta.CPUPercent == 0 {
				meta.CPUPercent = getFloatAny(cpu["Total"])
			}
			if mem, ok := obj["Memory"].(map[string]any); ok && meta.MemUsedPct == 0 {
				meta.MemUsedPct = getFloatAny(mem["UsedPercent"])
			}
		}
	}

	// Best-effort: parse os / preferred active interface from hardware_details.
	// Expected patterns we support:
	// - { "os": "xxx" }
	// - { "OS": "xxx" }
	if len(snap.HardwareDetails) > 0 {
		var obj map[string]any
		if err := json.Unmarshal(snap.HardwareDetails, &obj); err == nil {
			if v, ok := obj["os"]; ok {
				if s, ok := v.(string); ok {
					meta.OS = strings.TrimSpace(s)
				}
			}
			if meta.OS == "" {
				if v, ok := obj["OS"]; ok {
					if s, ok := v.(string); ok {
						meta.OS = strings.TrimSpace(s)
					}
				}
			}

			ifacesRaw := obj["network_ifaces"]
			if ifacesRaw == nil {
				ifacesRaw = obj["NetworkIfaces"]
			}
			if ip, mac, ifaceType := pickPreferredInterfaceIPAndMAC(ifacesRaw); ip != "" {
				meta.IP = ip
				meta.MAC = mac
				meta.IfaceType = ifaceType
			}
		}
	}

	return meta
}

func pickPreferredInterfaceIPAndMAC(v any) (string, string, string) {
	arr, ok := v.([]any)
	if !ok || len(arr) == 0 {
		return "", "", ""
	}

	type ifaceCandidate struct {
		ip   string
		mac  string
		name string
	}

	var activePhysical []ifaceCandidate
	var activeWireless []ifaceCandidate

	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if !isIfaceUp(m) || isLoopbackIface(m) {
			continue
		}

		name := strings.ToLower(strings.TrimSpace(getStringAny(m["name"])))
		mac := strings.TrimSpace(getStringAny(firstNonNil(m["mac"], m["MAC"])))
		ip := firstUsableIPFromIface(m)
		if ip == "" {
			continue
		}

		c := ifaceCandidate{ip: ip, mac: mac, name: name}
		if isWirelessIface(name) {
			activeWireless = append(activeWireless, c)
		} else if isPhysicalIfaceName(name) {
			activePhysical = append(activePhysical, c)
		}
	}

	if len(activePhysical) > 0 {
		return activePhysical[0].ip, activePhysical[0].mac, "physical"
	}
	if len(activeWireless) > 0 {
		return activeWireless[0].ip, activeWireless[0].mac, "wireless"
	}
	return "", "", ""
}

func firstUsableIPFromIface(m map[string]any) string {
	ipv4 := firstStringFromArray(firstNonNil(m["ipv4"], m["IPv4"]))
	if isUsableIP(ipv4) {
		return ipv4
	}
	ipv6 := firstStringFromArray(firstNonNil(m["ipv6"], m["IPv6"]))
	if isUsableIP(ipv6) {
		return ipv6
	}
	return ""
}

func firstStringFromArray(v any) string {
	arr, ok := v.([]any)
	if !ok {
		return ""
	}
	for _, it := range arr {
		s := strings.TrimSpace(getStringAny(it))
		if s != "" {
			return s
		}
	}
	return ""
}

func isUsableIP(ip string) bool {
	v := strings.TrimSpace(strings.ToLower(ip))
	if v == "" {
		return false
	}
	if v == "127.0.0.1" || v == "::1" {
		return false
	}
	return true
}

func isIfaceUp(m map[string]any) bool {
	for _, k := range []string{"is_up", "IsUp"} {
		if v, ok := m[k]; ok {
			if b, ok := v.(bool); ok {
				return b
			}
		}
	}
	// If field is absent, conservatively treat as up.
	return true
}

func isLoopbackIface(m map[string]any) bool {
	for _, k := range []string{"is_loopback", "IsLoopback"} {
		if v, ok := m[k]; ok {
			if b, ok := v.(bool); ok && b {
				return true
			}
		}
	}
	name := strings.ToLower(strings.TrimSpace(getStringAny(m["name"])))
	return strings.HasPrefix(name, "lo")
}

func isWirelessIface(name string) bool {
	for _, k := range []string{"wlan", "wifi", "wi-fi", "wireless", "wl", "ath", "airport"} {
		if strings.Contains(name, k) {
			return true
		}
	}
	return false
}

func isPhysicalIfaceName(name string) bool {
	if name == "" {
		return false
	}
	for _, k := range []string{"docker", "veth", "br-", "virbr", "vmnet", "vboxnet", "utun", "tun", "tap", "zt", "tailscale"} {
		if strings.Contains(name, k) {
			return false
		}
	}
	return true
}

func firstNonNil(a, b any) any {
	if a != nil {
		return a
	}
	return b
}

func getStringAny(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	default:
		return ""
	}
}

func getFloatAny(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case float32:
		return float64(t)
	case int:
		return float64(t)
	case int64:
		return float64(t)
	case json.Number:
		f, _ := t.Float64()
		return f
	case string:
		if strings.TrimSpace(t) == "" {
			return 0
		}
		if n, err := json.Number(t).Float64(); err == nil {
			return n
		}
		return 0
	default:
		return 0
	}
}

func (s *RedisSnapshotStore) LoadSnapshot(ctx context.Context, deviceID string) (model.SnapshotPush, bool, error) {
	if deviceID == "" {
		return model.SnapshotPush{}, false, nil
	}

	// TTL marker check (auto expire).
	if s.ttl > 0 {
		exists, err := s.client.Exists(ctx, s.expireKey(deviceID)).Result()
		if err != nil && err != redis.Nil {
			return model.SnapshotPush{}, false, err
		}
		if exists == 0 {
			// Best-effort cleanup.
			_, _ = s.client.HDel(ctx, s.snapshotsKey, deviceID).Result()
			_, _ = s.client.HDel(ctx, s.metaKey, deviceID).Result()
			return model.SnapshotPush{}, false, nil
		}
	}

	val, err := s.client.HGet(ctx, s.snapshotsKey, deviceID).Result()
	if err == redis.Nil {
		return model.SnapshotPush{}, false, nil
	}
	if err != nil {
		return model.SnapshotPush{}, false, err
	}

	var stored storedSnapshot
	if err := json.Unmarshal([]byte(val), &stored); err != nil {
		return model.SnapshotPush{}, false, err
	}

	return model.SnapshotPush{
		DeviceID:           deviceID,
		Processes:          []byte(stored.Processes),
		SoftwareList:       []byte(stored.SoftwareList),
		HostMetrics:        []byte(stored.HostMetrics),
		HardwareDetails:    []byte(stored.HardwareDetails),
		SoftwareInventory:  []byte(stored.SoftwareInventory),
		ProcessSnapshot:    []byte(stored.ProcessSnapshot),
		ServiceSnapshot:    []byte(stored.ServiceSnapshot),
		NetworkConnections: []byte(stored.NetworkConnections),
		SecuritySnapshot:   []byte(stored.SecuritySnapshot),
		UpdatedAtSec:       stored.UpdatedAtSec,
	}, true, nil
}

// ListDeviceIDs lists device_ids that currently have a snapshot.
// If TTL is enabled, it filters out ids whose expire marker key is already gone.
func (s *RedisSnapshotStore) ListDeviceIDs(ctx context.Context) ([]string, error) {
	ids, err := s.client.HKeys(ctx, s.snapshotsKey).Result()
	if err != nil {
		return nil, err
	}
	if s.ttl <= 0 || len(ids) == 0 {
		return ids, nil
	}

	// Filter by TTL marker keys: agent:snapshot:exp:{device_id}
	pipe := s.client.Pipeline()
	cmds := make([]*redis.IntCmd, 0, len(ids))
	for _, id := range ids {
		cmds = append(cmds, pipe.Exists(ctx, s.expireKey(id)))
	}
	_, err = pipe.Exec(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]string, 0, len(ids))
	expired := make([]string, 0, 32)
	for i, id := range ids {
		if cmds[i].Val() == 1 {
			out = append(out, id)
		} else {
			expired = append(expired, id)
		}
	}
	// Best-effort cleanup of expired ids (avoid meta leak).
	if len(expired) > 0 {
		_, _ = s.client.HDel(ctx, s.snapshotsKey, expired...).Result()
		_, _ = s.client.HDel(ctx, s.metaKey, expired...).Result()
	}
	return out, nil
}

// ListDevices returns device list enriched with hostname/os/updated_at, sorted by updated_at desc.
func (s *RedisSnapshotStore) ListDevices(ctx context.Context, onlineWithin time.Duration) ([]model.DeviceInfo, error) {
	ids, err := s.ListDeviceIDs(ctx)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return []model.DeviceInfo{}, nil
	}

	// HMGET meta in one shot.
	vals, err := s.client.HMGet(ctx, s.metaKey, ids...).Result()
	if err != nil && err != redis.Nil {
		return nil, err
	}

	now := time.Now()
	out := make([]model.DeviceInfo, 0, len(ids))
	for i, id := range ids {
		info := model.DeviceInfo{DeviceID: id}
		// Default online based on updated_at (fallback).
		var updatedAt int64
		needFallback := true
		needEnrich := false

		if i < len(vals) && vals[i] != nil {
			if sVal, ok := vals[i].(string); ok && strings.TrimSpace(sVal) != "" {
				var meta storedMeta
				if err := json.Unmarshal([]byte(sVal), &meta); err == nil {
					if meta.Hostname != "" {
						info.Hostname = meta.Hostname
					}
					if meta.OS != "" {
						info.OS = meta.OS
					}
					if meta.IP != "" {
						info.IP = meta.IP
					}
					if meta.MAC != "" {
						info.MAC = meta.MAC
					}
					if meta.IfaceType != "" {
						info.IfaceType = meta.IfaceType
					}
					if meta.CPUPercent > 0 {
						info.CPUPercent = meta.CPUPercent
					}
					if meta.MemUsedPct > 0 {
						info.MemUsedPct = meta.MemUsedPct
					}
					if meta.UpdatedAtSec > 0 {
						updatedAt = meta.UpdatedAtSec
						info.UpdatedAtSec = meta.UpdatedAtSec
					}
					needFallback = info.Hostname == "" && info.OS == "" && info.IP == "" && info.UpdatedAtSec == 0
					needEnrich = meta.IP == "" || meta.MAC == "" || meta.IfaceType == "" || meta.Hostname == "" || meta.OS == ""
				}
			}
		}

		// Fallback: meta missing (existing redis data before this feature).
		// We read snapshot once, derive meta, and write it back (best-effort).
		if needFallback || needEnrich {
			if snap, ok, err := s.LoadSnapshot(ctx, id); err == nil && ok {
				m := buildDeviceMeta(snap)
				changed := false
				if info.Hostname == "" && m.Hostname != "" {
					info.Hostname = m.Hostname
					changed = true
				}
				if info.OS == "" && m.OS != "" {
					info.OS = m.OS
					changed = true
				}
				if info.IP == "" && m.IP != "" {
					info.IP = m.IP
					changed = true
				}
				if info.MAC == "" && m.MAC != "" {
					info.MAC = m.MAC
					changed = true
				}
				if info.IfaceType == "" && m.IfaceType != "" {
					info.IfaceType = m.IfaceType
					changed = true
				}
				if info.CPUPercent == 0 && m.CPUPercent > 0 {
					info.CPUPercent = m.CPUPercent
					changed = true
				}
				if info.MemUsedPct == 0 && m.MemUsedPct > 0 {
					info.MemUsedPct = m.MemUsedPct
					changed = true
				}
				if info.UpdatedAtSec == 0 && m.UpdatedAtSec > 0 {
					info.UpdatedAtSec = m.UpdatedAtSec
					updatedAt = m.UpdatedAtSec
					changed = true
				}
				if changed {
					merged := storedMeta{
						DeviceID:     id,
						Hostname:     info.Hostname,
						OS:           info.OS,
						IP:           info.IP,
						MAC:          info.MAC,
						IfaceType:    info.IfaceType,
						CPUPercent:   info.CPUPercent,
						MemUsedPct:   info.MemUsedPct,
						UpdatedAtSec: info.UpdatedAtSec,
					}
					if b, err := json.Marshal(merged); err == nil {
						_, _ = s.client.HSet(ctx, s.metaKey, id, string(b)).Result()
					}
				}
			}
		}

		if updatedAt > 0 && onlineWithin > 0 {
			info.Online = now.Sub(time.Unix(updatedAt, 0)) <= onlineWithin
		}
		out = append(out, info)
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].UpdatedAtSec > out[j].UpdatedAtSec
	})
	return out, nil
}
