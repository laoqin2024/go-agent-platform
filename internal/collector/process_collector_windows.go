//go:build windows
// +build windows

package collector

import (
	"context"
	"errors"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func collectProcessesWithContext(ctx context.Context) ([]ProcessInfo, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snapshot)

	procs := make([]ProcessInfo, 0, 256)
	now := time.Now()

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))

	if err := windows.Process32First(snapshot, &entry); err != nil {
		return nil, err
	}

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		pid := entry.ProcessID
		name := windows.UTF16ToString(entry.ExeFile[:])
		exePath := getProcessExePath(pid)
		memBytes := uint64(getWorkingSetBytes(pid))

		procs = append(procs, ProcessInfo{
			PID:         pid,
			Name:        name,
			ExecPath:    exePath,
			MemoryBytes: memBytes,
			CollectedAt: now,
		})

		err := windows.Process32Next(snapshot, &entry)
		if err != nil {
			// Typical termination condition.
			var errno syscall.Errno
			if errors.As(err, &errno) && errno == windows.ERROR_NO_MORE_FILES {
				break
			}
			// If a single process enumeration fails, still return what we collected so far.
			break
		}
	}

	return procs, nil
}

func getWorkingSetBytes(pid uint32) uintptr {
	// PROCESS_QUERY_INFORMATION is sufficient for GetProcessWorkingSetSizeEx in most cases.
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION, false, pid)
	if err != nil {
		return 0
	}
	defer windows.CloseHandle(h)

	var minWS uintptr
	var maxWS uintptr
	var flags uint32 = 0
	windows.GetProcessWorkingSetSizeEx(h, &minWS, &maxWS, &flags)
	return maxWS
}

func getProcessExePath(pid uint32) string {
	// 1) Try QueryFullProcessImageNameW
	const PROCESS_QUERY_LIMITED_INFORMATION = 0x1000
	h, err := windows.OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err == nil {
		defer windows.CloseHandle(h)
		// Dynamically load QueryFullProcessImageNameW to avoid extra deps
		kernel32 := windows.NewLazySystemDLL("kernel32.dll")
		proc := kernel32.NewProc("QueryFullProcessImageNameW")
		if e := kernel32.Load(); e == nil {
			var buf [windows.MAX_PATH * 2]uint16
			n := uint32(len(buf))
			r1, _, _ := proc.Call(uintptr(h), uintptr(0), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)))
			if r1 != 0 {
				return windows.UTF16ToString(buf[:n])
			}
		}
	}

	// 2) Fallback: enumerate first module path
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPMODULE|windows.TH32CS_SNAPMODULE32, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(snapshot)

	var me windows.ModuleEntry32
	me.Size = uint32(unsafe.Sizeof(me))
	if err := windows.Module32First(snapshot, &me); err != nil {
		return ""
	}
	for {
		if me.ProcessID == pid {
			path := windows.UTF16ToString(me.ExePath[:])
			if path != "" {
				return path
			}
		}
		err := windows.Module32Next(snapshot, &me)
		if err != nil {
			var errno syscall.Errno
			if errors.As(err, &errno) && errno == windows.ERROR_NO_MORE_FILES {
				break
			}
			break
		}
	}
	return ""
}
