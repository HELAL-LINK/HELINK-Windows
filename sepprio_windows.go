//go:build windows

package main

import (
	"runtime"

	"golang.org/x/sys/windows"
)

// While the music is removed, its threads run below normal priority: the computer stays
// as quick as before for everything else (the window, the browser, a game), and the
// removal takes the time left over.

var (
	procSetThreadPriority = kernel32.NewProc("SetThreadPriority")
	procGetThreadPriority = kernel32.NewProc("GetThreadPriority")
)

const threadPriorityErrorReturn = 0x7fffffff

func init() { sepThreadHook = lowerThreadPriority }

func lowerThreadPriority() (restore func()) {
	runtime.LockOSThread()
	h := uintptr(windows.CurrentThread())
	old, _, _ := procGetThreadPriority.Call(h)
	if uint32(old) == threadPriorityErrorReturn {
		old = 0 // THREAD_PRIORITY_NORMAL
	}
	procSetThreadPriority.Call(h, ^uintptr(0)) // THREAD_PRIORITY_BELOW_NORMAL (-1)
	return func() {
		procSetThreadPriority.Call(h, uintptr(uint32(old)))
		runtime.UnlockOSThread()
	}
}
