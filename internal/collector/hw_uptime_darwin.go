//go:build darwin

package collector

import "context"

func collectUptimeSeconds(ctx context.Context) uint64 {
	return 0
}

