//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// updateTarget tells how this copy of HELINK is updated: the installed one (where the installer
// put it, next to "Uninstall HELINK.exe") by the new installer, a portable one by replacing its
// HELINK.exe, which needs a folder Windows lets it change.
func updateTarget() (updateTargetInfo, error) {
	exe, err := os.Executable()
	if err != nil {
		return updateTargetInfo{}, err
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	dir := filepath.Dir(exe)
	if fileOK(filepath.Join(dir, "Uninstall HELINK.exe")) {
		return updateTargetInfo{Kind: "setup", Exe: exe}, nil
	}
	if k, err := registry.OpenKey(registry.CURRENT_USER, `Software\HELINK`, registry.QUERY_VALUE); err == nil {
		d, _, _ := k.GetStringValue("InstallDir")
		k.Close()
		if d != "" && strings.EqualFold(filepath.Clean(d), filepath.Clean(dir)) {
			return updateTargetInfo{Kind: "setup", Exe: exe}, nil
		}
	}
	t := updateTargetInfo{Kind: "portable", Exe: exe}
	if !dirWritable(dir) {
		return t, fmt.Errorf("%s: %w", dir, os.ErrPermission)
	}
	return t, nil
}

// applyUpdate starts what replaces this HELINK. The installer asks the running HELINK to close
// (and waits), puts the new one in place, and opens it (/RELAUNCH); a portable copy's new
// HELINK.exe waits until this one has closed.
func applyUpdate(t updateTargetInfo, file string) error {
	if t.Kind == "setup" {
		return startDetached(file, "/S", "/RELAUNCH")
	}
	return swapPortableExe(t.Exe, file, func(exe string) error {
		return startDetached(exe, "--updated="+strconv.Itoa(os.Getpid()))
	})
}

// startDetached starts a program that outlives HELINK: outside the job a browser may have put
// HELINK in (when the browser's helper started it), with no console. (exec.Cmd gives it the
// three standard handles Windows needs, on NUL; os.StartProcess without them fails there.)
func startDetached(exe string, args ...string) error {
	const breakaway = 0x01000000 // CREATE_BREAKAWAY_FROM_JOB
	flags := uint32(windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP)
	start := func(flags uint32) (*exec.Cmd, error) {
		cmd := exec.Command(exe, args...)
		cmd.Dir = filepath.Dir(exe)
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags}
		return cmd, cmd.Start()
	}
	cmd, err := start(flags | breakaway)
	if err != nil {
		// The job does not let programs leave it: start it inside then.
		if cmd, err = start(flags); err != nil {
			return err
		}
	}
	return cmd.Process.Release()
}

// waitForUpdatedFrom waits (at most 30 seconds) until the HELINK that started this one to
// replace itself has closed: one copy runs at a time.
func waitForUpdatedFrom() {
	for _, a := range os.Args[1:] {
		v, ok := strings.CutPrefix(strings.ToLower(a), "--updated=")
		if !ok {
			continue
		}
		pid, err := strconv.Atoi(v)
		if err != nil || pid <= 0 || pid == os.Getpid() {
			return
		}
		h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
		if err != nil {
			return // already gone
		}
		defer windows.CloseHandle(h)
		_, _ = windows.WaitForSingleObject(h, 30000)
		return
	}
}
