//go:build windows

package collector

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"path/filepath"
	"sort"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	afInet              = 2
	tcpTableOwnerPidAll = 5
	udpTableOwnerPid    = 1
	tcpListen           = 2
	tcpEstablished      = 5
)

var (
	iphlpapi                = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetExtendedTcpTable = iphlpapi.NewProc("GetExtendedTcpTable")
	procGetExtendedUdpTable = iphlpapi.NewProc("GetExtendedUdpTable")
)

func collectNetworkConnections(ctx context.Context) ([]NetConnInfo, error) {
	out := make([]NetConnInfo, 0, 256)
	tcp, _ := collectTCPTable(ctx)
	udp, _ := collectUDPTable(ctx)
	out = append(out, tcp...)
	out = append(out, udp...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Protocol != out[j].Protocol {
			return out[i].Protocol < out[j].Protocol
		}
		if out[i].LocalPort != out[j].LocalPort {
			return out[i].LocalPort < out[j].LocalPort
		}
		return out[i].PID < out[j].PID
	})
	return out, nil
}

func collectTCPTable(ctx context.Context) ([]NetConnInfo, error) {
	buf, err := queryExtendedTable(procGetExtendedTcpTable, tcpTableOwnerPidAll)
	if err != nil || len(buf) < 4 {
		return nil, err
	}
	n := int(binary.LittleEndian.Uint32(buf[:4]))
	rowSize := 24 // MIB_TCPROW_OWNER_PID (IPv4)
	rows := make([]NetConnInfo, 0, n)
	for i := 0; i < n; i++ {
		select {
		case <-ctx.Done():
			return rows, nil
		default:
		}
		off := 4 + i*rowSize
		if off+rowSize > len(buf) {
			break
		}
		state := binary.LittleEndian.Uint32(buf[off : off+4])
		if state != tcpListen && state != tcpEstablished {
			continue
		}
		localAddr := binary.LittleEndian.Uint32(buf[off+4 : off+8])
		localPort := ntohs(binary.BigEndian.Uint16(buf[off+8 : off+10]))
		remoteAddr := binary.LittleEndian.Uint32(buf[off+12 : off+16])
		remotePort := ntohs(binary.BigEndian.Uint16(buf[off+16 : off+18]))
		pid := binary.LittleEndian.Uint32(buf[off+20 : off+24])

		rows = append(rows, NetConnInfo{
			Protocol:    "TCP",
			LocalAddr:   ipv4ToString(localAddr),
			LocalPort:   localPort,
			RemoteAddr:  ipv4ToString(remoteAddr),
			RemotePort:  remotePort,
			State:       mapTCPState(state),
			PID:         pid,
			ProcessName: processNameByPID(pid),
		})
	}
	return rows, nil
}

func collectUDPTable(ctx context.Context) ([]NetConnInfo, error) {
	buf, err := queryExtendedTable(procGetExtendedUdpTable, udpTableOwnerPid)
	if err != nil || len(buf) < 4 {
		return nil, err
	}
	n := int(binary.LittleEndian.Uint32(buf[:4]))
	rowSize := 12 // MIB_UDPROW_OWNER_PID (IPv4)
	rows := make([]NetConnInfo, 0, n)
	for i := 0; i < n; i++ {
		select {
		case <-ctx.Done():
			return rows, nil
		default:
		}
		off := 4 + i*rowSize
		if off+rowSize > len(buf) {
			break
		}
		localAddr := binary.LittleEndian.Uint32(buf[off : off+4])
		localPort := ntohs(binary.BigEndian.Uint16(buf[off+4 : off+6]))
		pid := binary.LittleEndian.Uint32(buf[off+8 : off+12])
		rows = append(rows, NetConnInfo{
			Protocol:    "UDP",
			LocalAddr:   ipv4ToString(localAddr),
			LocalPort:   localPort,
			State:       "LISTEN",
			PID:         pid,
			ProcessName: processNameByPID(pid),
		})
	}
	return rows, nil
}

func queryExtendedTable(proc *windows.LazyProc, tableClass uint32) ([]byte, error) {
	var sz uint32
	r1, _, _ := proc.Call(0, uintptr(unsafe.Pointer(&sz)), 0, uintptr(afInet), uintptr(tableClass), 0)
	// GetExtended{Tcp,Udp}Table returns ERROR_INSUFFICIENT_BUFFER on first call to indicate required size.
	if r1 != uintptr(windows.ERROR_INSUFFICIENT_BUFFER) || sz == 0 {
		return nil, fmt.Errorf("iphlpapi: size query failed: %d", r1)
	}
	buf := make([]byte, sz)
	r2, _, _ := proc.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&sz)), 0, uintptr(afInet), uintptr(tableClass), 0)
	if uint32(r2) != 0 {
		return nil, fmt.Errorf("iphlpapi: table query failed: %d", r2)
	}
	return buf, nil
}

func ntohs(v uint16) uint16 { return (v<<8)&0xff00 | v>>8 }

func ipv4ToString(v uint32) string {
	ip := net.IPv4(byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
	return ip.String()
}

func mapTCPState(s uint32) string {
	switch s {
	case tcpListen:
		return "LISTEN"
	case tcpEstablished:
		return "ESTABLISHED"
	default:
		return ""
	}
}

func processNameByPID(pid uint32) string {
	p := getProcessExePath(pid)
	if p == "" {
		return ""
	}
	return filepath.Base(p)
}
