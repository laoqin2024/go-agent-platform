//go:build windows

package collector

import (
	"encoding/binary"
	"fmt"
	"syscall"
)

func platformSMBIOSUUID() string {
	table, err := getRawSMBIOSTable()
	if err != nil || len(table) == 0 {
		return ""
	}
	uuid := findType1UUIDFromTable(table)
	if len(uuid) != 16 {
		return ""
	}
	if isAllZeroOrFF(uuid) {
		return ""
	}

	// SMBIOS UUID is stored in mixed-endian (first 3 fields little-endian).
	d1 := binary.LittleEndian.Uint32(uuid[0:4])
	d2 := binary.LittleEndian.Uint16(uuid[4:6])
	d3 := binary.LittleEndian.Uint16(uuid[6:8])
	return fmt.Sprintf("%08x-%04x-%04x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		d1, d2, d3,
		uuid[8], uuid[9],
		uuid[10], uuid[11], uuid[12], uuid[13], uuid[14], uuid[15],
	)
}

func isAllZeroOrFF(b []byte) bool {
	all0 := true
	allF := true
	for _, v := range b {
		if v != 0x00 {
			all0 = false
		}
		if v != 0xFF {
			allF = false
		}
	}
	return all0 || allF
}

func findType1UUIDFromTable(table []byte) []byte {
	var out []byte
	walkSMBIOSStructures(table, func(s smbiosStruct) bool {
		if s.typ != 1 || s.length < 24 {
			return false
		}
		u := s.data[8:24]
		out = make([]byte, 16)
		copy(out, u)
		return true
	})
	return out
}

// Ensure syscall is linked on older toolchains that require it for LazyDLL.
var _ = syscall.Errno(0)

