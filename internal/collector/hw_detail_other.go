//go:build !windows && !linux && !darwin

package collector

import "context"

func collectPhysicalDisks(ctx context.Context) ([]DiskInfo, error) { return []DiskInfo{}, nil }
func collectMemorySlots(ctx context.Context) ([]RamInfo, error)    { return []RamInfo{}, nil }
func collectGPUs(ctx context.Context) ([]GPUInfo, error)           { return []GPUInfo{}, nil }
func collectMainboard(ctx context.Context) (*MainboardInfo, error) { return nil, nil }

