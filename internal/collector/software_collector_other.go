//go:build !windows && !linux && !darwin

package collector

import (
	"context"
)

func collectSoftwareWithContext(ctx context.Context) ([]SoftwareInfo, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	return nil, ErrSoftwareCollectUnsupported
}

