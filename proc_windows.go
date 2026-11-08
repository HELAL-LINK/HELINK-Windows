//go:build windows

package main

import (
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// prepareCmd hides the console window of yt-dlp (its own children are hidden by yt-dlp itself).
func prepareCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW,
	}
}

// processTree wraps a Windows job object so cancelling kills yt-dlp together with
// the ffmpeg/deno processes it started. Closing the job (also when the app exits or
// crashes) kills everything that is still running.
type processTree struct {
	mu  sync.Mutex
	job windows.Handle
	cmd *exec.Cmd
}

func attachProcessTree(cmd *exec.Cmd) *processTree {
	t := &processTree{cmd: cmd}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return t
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return t
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		windows.CloseHandle(job)
		return t
	}
	defer windows.CloseHandle(h)
	if err := windows.AssignProcessToJobObject(job, h); err != nil {
		windows.CloseHandle(job)
		return t
	}
	t.job = job
	return t
}

func (t *processTree) kill() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.job != 0 {
		windows.TerminateJobObject(t.job, 1)
		return
	}
	if t.cmd.Process != nil {
		t.cmd.Process.Kill()
	}
}

func (t *processTree) release() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.job != 0 {
		windows.CloseHandle(t.job) // kills leftovers thanks to KILL_ON_JOB_CLOSE
		t.job = 0
	}
}
