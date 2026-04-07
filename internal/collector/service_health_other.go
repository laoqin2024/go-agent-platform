//go:build !linux && !windows && !darwin

package collector

import "context"

func collectServiceHealthWithWhitelist(ctx context.Context, whitelist []string) ([]ServiceStat, map[uint32]string, map[string]string, error) {
	return nil, nil, nil, nil
}

