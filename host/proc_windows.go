//go:build windows

package main

import (
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"
)

var procAllowSetForegroundWindow = windows.NewLazySystemDLL("user32.dll").NewProc("AllowSetForegroundWindow")

// allowForeground lets the app bring its window to the front: the browser started this
// helper, so it may pass that right on.
func allowForeground(pid int) {
	if pid > 0 {
		procAllowSetForegroundWindow.Call(uintptr(pid))
	}
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	ev, err := windows.WaitForSingleObject(h, 0)
	return err == nil && ev == uint32(windows.WAIT_TIMEOUT)
}

// startApp starts HELINK.exe hidden near the clock. It must not belong to the browser's
// job: closing the browser would close it.
func startApp(dir string) error {
	exe := appPath(dir)
	if exe == "" {
		self, err := os.Executable()
		if err != nil {
			return err
		}
		exe = filepath.Join(filepath.Dir(self), "HELINK.exe")
	}
	if _, err := os.Stat(exe); err != nil {
		return err
	}
	const breakaway = 0x01000000 // CREATE_BREAKAWAY_FROM_JOB
	flags := uint32(windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP)
	attr := &os.ProcAttr{Dir: filepath.Dir(exe), Sys: &syscall.SysProcAttr{CreationFlags: flags | breakaway}}
	p, err := os.StartProcess(exe, []string{exe, "--background"}, attr)
	if err != nil {
		// The browser's job does not allow leaving it: start the app inside it then.
		attr.Sys = &syscall.SysProcAttr{CreationFlags: flags}
		if p, err = os.StartProcess(exe, []string{exe, "--background"}, attr); err != nil {
			return err
		}
	}
	return p.Release()
}
