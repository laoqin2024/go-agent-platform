//go:build windows

package collector

import (
	"context"
	"regexp"
	"sort"
	"time"

	"golang.org/x/sys/windows/registry"
)

var kbRe = regexp.MustCompile(`KB\d+`)

type hotfixTmp struct {
	kb string
	t  time.Time
}

func collectHotfixes(ctx context.Context) ([]HotfixInfo, error) {
	base := `SOFTWARE\Microsoft\Windows\CurrentVersion\Component Based Servicing\Packages`
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, base, registry.READ)
	if err != nil {
		return nil, nil
	}
	defer k.Close()

	sub, err := k.ReadSubKeyNames(-1)
	if err != nil {
		return nil, nil
	}

	tmp := make([]hotfixTmp, 0, 256)
	for _, sk := range sub {
		select {
		case <-ctx.Done():
			break
		default:
		}
		m := kbRe.FindString(sk)
		if m == "" {
			continue
		}
		skKey, err := registry.OpenKey(k, sk, registry.READ)
		if err != nil {
			continue
		}
		lo, _, _ := skKey.GetIntegerValue("InstallTimeLow")
		hi, _, _ := skKey.GetIntegerValue("InstallTimeHigh")
		_ = skKey.Close()
		t := filetimeToTime(uint32(hi), uint32(lo))
		tmp = append(tmp, hotfixTmp{kb: m, t: t})
	}

	// Dedup by KB and keep latest time.
	mm := map[string]time.Time{}
	for _, x := range tmp {
		if old, ok := mm[x.kb]; !ok || x.t.After(old) {
			mm[x.kb] = x.t
		}
	}
	tmp = tmp[:0]
	for kb, t := range mm {
		tmp = append(tmp, hotfixTmp{kb: kb, t: t})
	}

	sort.Slice(tmp, func(i, j int) bool { return tmp[i].t.After(tmp[j].t) })
	if len(tmp) > 10 {
		tmp = tmp[:10]
	}
	out := make([]HotfixInfo, 0, len(tmp))
	for _, x := range tmp {
		s := ""
		if !x.t.IsZero() {
			s = x.t.Format(time.RFC3339)
		}
		out = append(out, HotfixInfo{KB: x.kb, InstalledAt: s})
	}
	return out, nil
}

func filetimeToTime(high, low uint32) time.Time {
	ft := (uint64(high) << 32) | uint64(low)
	if ft == 0 {
		return time.Time{}
	}
	// Windows epoch 1601-01-01, units are 100ns.
	const windowsToUnix100ns = uint64(116444736000000000)
	if ft <= windowsToUnix100ns {
		return time.Time{}
	}
	ns := (ft - windowsToUnix100ns) * 100
	return time.Unix(0, int64(ns)).UTC()
}

