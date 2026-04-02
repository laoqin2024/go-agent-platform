//go:build windows
// +build windows

package collector

import (
	"context"

	"golang.org/x/sys/windows"
)

func collectUptimeSeconds(ctx context.Context) uint64 {
	k := windows.NewLazySystemDLL("kernel32.dll")
	p := k.NewProc("GetTickCount64")
	if err := k.Load(); err != nil {
		return 0
	}
	r1, _, _ := p.Call()
	// milliseconds to seconds
	return uint64(r1) / 1000
}
