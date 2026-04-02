//go:build windows

package collector

import (
	"context"

	"golang.org/x/sys/windows/registry"
)

func collectStartupItems(ctx context.Context) ([]StartupItem, error) {
	paths := []struct {
		root registry.Key
		path string
	}{
		{registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\Run`},
		{registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\RunOnce`},
		{registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Run`},
	}

	out := make([]StartupItem, 0, 64)
	for _, p := range paths {
		select {
		case <-ctx.Done():
			return out, nil
		default:
		}
		k, err := registry.OpenKey(p.root, p.path, registry.READ)
		if err != nil {
			continue
		}
		names, _ := k.ReadValueNames(-1)
		for _, n := range names {
			select {
			case <-ctx.Done():
				_ = k.Close()
				return out, nil
			default:
			}
			val, _, err := k.GetStringValue(n)
			if err != nil || val == "" {
				continue
			}
			out = append(out, StartupItem{
				Location: p.path,
				Name:     n,
				Command:  val,
			})
		}
		_ = k.Close()
	}
	return out, nil
}

