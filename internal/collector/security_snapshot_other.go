//go:build !windows

package collector

import "context"

func collectNetworkConnections(ctx context.Context) ([]NetConnInfo, error) { return nil, nil }
func collectStartupItems(ctx context.Context) ([]StartupItem, error)       { return nil, nil }
func collectHotfixes(ctx context.Context) ([]HotfixInfo, error)            { return nil, nil }

