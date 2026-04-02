package collector

import (
	"crypto/sha256"
	"encoding/hex"
	"runtime"
	"sync"
	"time"
)

const allZeroUUID = "00000000-0000-0000-0000-000000000000"

var (
	// cache SMBIOS UUID once it becomes available.
	// On some systems/VMs SMBIOS reads can be transient; caching prevents host_metrics
	// and hardware_details from ending up with different fingerprints.
	smbiosUUIDCacheMu sync.RWMutex
	smbiosUUIDCache   string
)

// stableFingerprint returns a best-effort stable machine fingerprint.
// Prefer SMBIOS UUID; if missing/all-zero, fallback to a deterministic hash.
func stableFingerprint(hostname string) string {
	// Fast path: if we already have a non-empty UUID, always reuse it.
	smbiosUUIDCacheMu.RLock()
	id := smbiosUUIDCache
	smbiosUUIDCacheMu.RUnlock()
	if id != "" && id != allZeroUUID {
		return id
	}

	// Retry a few times before hashing to avoid transient SMBIOS read failures.
	const attempts = 3
	for i := 0; i < attempts && (id == "" || id == allZeroUUID); i++ {
		id = platformSMBIOSUUID()
		if id != "" && id != allZeroUUID {
			smbiosUUIDCacheMu.Lock()
			smbiosUUIDCache = id
			smbiosUUIDCacheMu.Unlock()
			return id
		}
		if i+1 < attempts {
			// Small backoff; avoids hammering GetSystemFirmwareTable in tight loops.
			time.Sleep(20 * time.Millisecond)
		}
	}

	input := hostname + "|" + runtime.GOOS + "|" + runtime.GOARCH
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:])
}



