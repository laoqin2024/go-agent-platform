package collector

import (
	"context"
	"net"
	"strings"
)

func collectNetworkIfaces(ctx context.Context) ([]NetworkIfaceInfo, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	out := make([]NetworkIfaceInfo, 0, len(ifaces))
	for _, it := range ifaces {
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		default:
		}
		addrs, _ := it.Addrs()
		var ipv4 []string
		var ipv6 []string
		for _, a := range addrs {
			ipStr := a.String()
			host := ipStr
			if strings.Contains(ipStr, "/") {
				host = strings.SplitN(ipStr, "/", 2)[0]
			}
			ip := net.ParseIP(host)
			if ip == nil {
				continue
			}
			if ip.To4() != nil {
				ipv4 = append(ipv4, host)
			} else {
				ipv6 = append(ipv6, host)
			}
		}
		out = append(out, NetworkIfaceInfo{
			Name:       it.Name,
			MAC:        it.HardwareAddr.String(),
			IPv4:       ipv4,
			IPv6:       ipv6,
			MTU:        it.MTU,
			IsUp:       it.Flags&net.FlagUp != 0,
			IsLoopback: it.Flags&net.FlagLoopback != 0,
		})
	}
	return out, nil
}

