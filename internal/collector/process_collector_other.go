//go:build !windows && !linux && !darwin

package collector

import (
	"context"
)

func collectProcessesWithContext(ctx context.Context) ([]ProcessInfo, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	return nil, ErrCollectUnsupported
}

