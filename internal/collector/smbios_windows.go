//go:build windows

package collector

import (
	"encoding/binary"
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	smbiosKernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	procGetSystemFirmwareTableSMBIOS = smbiosKernel32.NewProc("GetSystemFirmwareTable")
)

// getRawSMBIOSTable returns the SMBIOS table bytes (structures only, without RawSMBIOSData header).
func getRawSMBIOSTable() ([]byte, error) {
	const providerRSMB uint32 = ('R' << 24) | ('S' << 16) | ('M' << 8) | ('B')

	r0, _, _ := procGetSystemFirmwareTableSMBIOS.Call(uintptr(providerRSMB), uintptr(0), uintptr(0), uintptr(0))
	size := uint32(r0)
	if size == 0 {
		return nil, errors.New("smbios: GetSystemFirmwareTable returned size=0")
	}

	buf := make([]byte, size)
	r1, _, _ := procGetSystemFirmwareTableSMBIOS.Call(uintptr(providerRSMB), uintptr(0), uintptr(unsafe.Pointer(&buf[0])), uintptr(size))
	if r1 == 0 {
		return nil, errors.New("smbios: GetSystemFirmwareTable returned 0")
	}
	if uint32(r1) < 8 {
		return nil, errors.New("smbios: RawSMBIOSData too small")
	}
	buf = buf[:r1]

	// RawSMBIOSData header is 8 bytes; table length is little-endian at [4:8].
	tableLen := binary.LittleEndian.Uint32(buf[4:8])
	if tableLen == 0 {
		return nil, errors.New("smbios: table length is 0")
	}
	if 8+int(tableLen) > len(buf) {
		// best-effort clamp
		if len(buf) <= 8 {
			return nil, errors.New("smbios: invalid table length")
		}
		tableLen = uint32(len(buf) - 8)
	}
	return buf[8 : 8+tableLen], nil
}

type smbiosStruct struct {
	typ    byte
	length int
	data   []byte // formatted section bytes, includes header
	strs   []string
}

func walkSMBIOSStructures(table []byte, fn func(s smbiosStruct) bool) {
	for i := 0; i+4 <= len(table); {
		typ := table[i]
		l := int(table[i+1])
		if l < 4 || i+l > len(table) {
			return
		}

		formatted := table[i : i+l]
		// parse string-set
		j := i + l
		var strs []string
		start := j
		for j+1 < len(table) {
			if table[j] == 0x00 {
				if j+1 < len(table) && table[j+1] == 0x00 {
					// end of strings
					if j > start {
						// there were strings; parse segments
					}
					j += 2
					break
				}
				// one string end
				strs = append(strs, string(table[start:j]))
				j++
				start = j
				continue
			}
			j++
		}
		// if no double-nul, stop
		if j <= i+l {
			return
		}
		s := smbiosStruct{typ: typ, length: l, data: formatted, strs: strs}
		if stop := fn(s); stop {
			return
		}
		i = j
	}
}

func smbiosString(strs []string, idx byte) string {
	if idx == 0 {
		return ""
	}
	i := int(idx) - 1
	if i < 0 || i >= len(strs) {
		return ""
	}
	return strs[i]
}

func smbiosMemoryTypeToString(v byte) string {
	switch v {
	case 0x12:
		return "DDR"
	case 0x13:
		return "DDR2"
	case 0x18:
		return "DDR3"
	case 0x1A:
		return "DDR4"
	case 0x22:
		return "DDR5"
	case 0x1F:
		return "LPDDR"
	case 0x20:
		return "LPDDR2"
	case 0x21:
		return "LPDDR3"
	case 0x24:
		return "LPDDR4"
	case 0x25:
		return "LPDDR5"
	default:
		if v == 0 {
			return ""
		}
		return fmt.Sprintf("0x%02X", v)
	}
}

