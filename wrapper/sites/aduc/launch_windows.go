//go:build windows

package main

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"syscall"
	"unsafe"
)

const (
	logonNetCredentialsOnly  = 0x2
	createUnicodeEnvironment = 0x400
	infinite                 = 0xFFFFFFFF
)

var (
	advapi32                    = syscall.NewLazyDLL("advapi32.dll")
	procCreateProcessWithLogonW = advapi32.NewProc("CreateProcessWithLogonW")
	kernel32                    = syscall.NewLazyDLL("kernel32.dll")
	procGetConsoleWindow        = kernel32.NewProc("GetConsoleWindow")
	user32                      = syscall.NewLazyDLL("user32.dll")
	procShowWindow              = user32.NewProc("ShowWindow")
)

// runNetOnly starts cmdline like `runas /netonly`: local token unchanged, outbound
// authentication as domain\user. It blocks until the process exits.
func runNetOnly(user, domain, password, cmdline string, hideConsole bool) (uint32, error) {
	u, err := syscall.UTF16PtrFromString(user)
	if err != nil {
		return 0, err
	}
	var d *uint16
	if domain != "" {
		if d, err = syscall.UTF16PtrFromString(domain); err != nil {
			return 0, err
		}
	}
	pw, err := syscall.UTF16FromString(password)
	if err != nil {
		return 0, fmt.Errorf("password contains a NUL character")
	}
	defer clear(pw)
	cl, err := syscall.UTF16FromString(cmdline) // lpCommandLine must be writable
	if err != nil {
		return 0, err
	}
	env := envBlock(append(os.Environ(),
		// mmc.exe asks for highestAvailable; for an admin RDS user CreateProcess would
		// then fail with ERROR_ELEVATION_REQUIRED (740). ADUC doesn't need elevation.
		"__COMPAT_LAYER=RunAsInvoker"))
	cwd, _ := syscall.UTF16PtrFromString(system32())

	si := syscall.StartupInfo{}
	si.Cb = uint32(unsafe.Sizeof(si))
	var pi syscall.ProcessInformation
	r, _, callErr := procCreateProcessWithLogonW.Call(
		uintptr(unsafe.Pointer(u)),
		uintptr(unsafe.Pointer(d)),
		uintptr(unsafe.Pointer(&pw[0])),
		logonNetCredentialsOnly,
		0,
		uintptr(unsafe.Pointer(&cl[0])),
		createUnicodeEnvironment,
		uintptr(unsafe.Pointer(&env[0])),
		uintptr(unsafe.Pointer(cwd)),
		uintptr(unsafe.Pointer(&si)),
		uintptr(unsafe.Pointer(&pi)),
	)
	clear(pw)
	if r == 0 {
		return 0, fmt.Errorf("CreateProcessWithLogonW: %w (is the Secondary Logon service running?)", callErr)
	}
	defer syscall.CloseHandle(pi.Process)
	syscall.CloseHandle(pi.Thread)
	slog.Info("console started", "pid", pi.ProcessId)

	if hideConsole {
		if h, _, _ := procGetConsoleWindow.Call(); h != 0 {
			procShowWindow.Call(h, 0) // SW_HIDE: keep only ADUC visible in the RemoteApp
		}
	}

	if _, err := syscall.WaitForSingleObject(pi.Process, infinite); err != nil {
		return 0, err
	}
	var code uint32
	_ = syscall.GetExitCodeProcess(pi.Process, &code)
	return code, nil
}

// envBlock builds a CREATE_UNICODE_ENVIRONMENT block; later entries override
// earlier ones with the same (case-insensitive) name.
func envBlock(vars []string) []uint16 {
	idx := map[string]int{}
	var kept []string
	for _, kv := range vars {
		name := strings.ToUpper(strings.SplitN(kv, "=", 2)[0])
		if name == "" { // drive-letter entries like "=C:=C:\"
			kept = append(kept, kv)
			continue
		}
		if i, ok := idx[name]; ok {
			kept[i] = kv
			continue
		}
		idx[name] = len(kept)
		kept = append(kept, kv)
	}
	var b []uint16
	for _, kv := range kept {
		if strings.IndexByte(kv, 0) >= 0 {
			continue
		}
		s, _ := syscall.UTF16FromString(kv)
		b = append(b, s...)
	}
	return append(b, 0)
}
