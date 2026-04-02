//go:build !windows && !linux && !darwin

package collector

import "context"

func collectServicesWithContext(ctx context.Context) ([]ServiceInfo, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	return nil, ErrCollectUnsupported
}

