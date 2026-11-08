//go:build windows

package main

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	ole32    = windows.NewLazySystemDLL("ole32.dll")
	dwmapi   = windows.NewLazySystemDLL("dwmapi.dll")

	procOpenClipboard                           = user32.NewProc("OpenClipboard")
	procCloseClipboard                          = user32.NewProc("CloseClipboard")
	procEmptyClipboard                          = user32.NewProc("EmptyClipboard")
	procGetClipboardData                        = user32.NewProc("GetClipboardData")
	procSetClipboardData                        = user32.NewProc("SetClipboardData")
	procSetWindowLongPtrW                       = user32.NewProc("SetWindowLongPtrW")
	procCallWindowProcW                         = user32.NewProc("CallWindowProcW")
	procSetWindowPos                            = user32.NewProc("SetWindowPos")
	procGetWindowRect                           = user32.NewProc("GetWindowRect")
	procPostMessageW                            = user32.NewProc("PostMessageW")
	procGetDpiForSystem                         = user32.NewProc("GetDpiForSystem")
	procSystemParametersInfoW                   = user32.NewProc("SystemParametersInfoW")
	procMessageBoxW                             = user32.NewProc("MessageBoxW")
	procFindWindowW                             = user32.NewProc("FindWindowW")
	procFindWindowExW                           = user32.NewProc("FindWindowExW")
	procGetClientRect                           = user32.NewProc("GetClientRect")
	procGetWindowLongPtrW                       = user32.NewProc("GetWindowLongPtrW")
	procShowWindow                              = user32.NewProc("ShowWindow")
	procSetForegroundWindow                     = user32.NewProc("SetForegroundWindow")
	procIsIconic                                = user32.NewProc("IsIconic")
	procFlashWindowEx                           = user32.NewProc("FlashWindowEx")
	procGetForegroundWindow                     = user32.NewProc("GetForegroundWindow")
	procGlobalAlloc                             = kernel32.NewProc("GlobalAlloc")
	procGlobalFree                              = kernel32.NewProc("GlobalFree")
	procGlobalLock                              = kernel32.NewProc("GlobalLock")
	procGlobalUnlock                            = kernel32.NewProc("GlobalUnlock")
	procGlobalSize                              = kernel32.NewProc("GlobalSize")
	procILCreateFromPathW                       = shell32.NewProc("ILCreateFromPathW")
	procILFree                                  = shell32.NewProc("ILFree")
	procSHOpenFolderAndSelect                   = shell32.NewProc("SHOpenFolderAndSelectItems")
	procSHCreateItemFromParsing                 = shell32.NewProc("SHCreateItemFromParsingName")
	procCoCreateInstance                        = ole32.NewProc("CoCreateInstance")
	procDwmSetWindowAttribute                   = dwmapi.NewProc("DwmSetWindowAttribute")
	procSetCurrentProcessExplicitAppUserModelID = shell32.NewProc("SetCurrentProcessExplicitAppUserModelID")
	procSetThreadExecutionState                 = kernel32.NewProc("SetThreadExecutionState")
)

const (
	wmClose            = 0x0010
	wmQueryEndSession  = 0x0011
	wmEndSession       = 0x0016
	wmSettingChange    = 0x001A
	wmGetMinMaxInfo    = 0x0024
	wmWindowPosChanged = 0x0047
	wmSize             = 0x0005
	wmDpiChanged       = 0x02E0
	cfUnicodeText      = 13
	gmemMoveable       = 0x0002
	swpNoZOrder        = 0x0004
	swpNoActivate      = 0x0010
	swRestore          = 9
	spiGetWorkArea     = 0x0030
)

type rect struct{ Left, Top, Right, Bottom int32 }

func hresultFailed(hr uintptr) bool { return int32(hr) < 0 }

// callIf calls an optional Windows function and does nothing if this Windows lacks it.
func callIf(p *windows.LazyProc, args ...uintptr) uintptr {
	if p.Find() != nil {
		return 0
	}
	r, _, _ := p.Call(args...)
	return r
}

func utf16(s string) *uint16 {
	p, err := windows.UTF16PtrFromString(s)
	if err != nil {
		p, _ = windows.UTF16PtrFromString("")
	}
	return p
}

// ---------------------------------------------------------------------------
// Clipboard (called on the UI thread)
// ---------------------------------------------------------------------------

func openClipboard(hwnd uintptr) bool {
	for i := 0; i < 10; i++ {
		if r, _, _ := procOpenClipboard.Call(hwnd); r != 0 {
			return true
		}
		time.Sleep(25 * time.Millisecond) // another program may hold it for a moment
	}
	return false
}

func clipboardText(hwnd uintptr) string {
	if !openClipboard(hwnd) {
		return ""
	}
	defer procCloseClipboard.Call()
	h, _, _ := procGetClipboardData.Call(cfUnicodeText)
	if h == 0 {
		return ""
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		return ""
	}
	defer procGlobalUnlock.Call(h)
	size, _, _ := procGlobalSize.Call(h)
	n := int(size / 2)
	if n <= 0 {
		return ""
	}
	if n > 1<<20 {
		n = 1 << 20
	}
	return windows.UTF16ToString(unsafe.Slice((*uint16)(unsafe.Pointer(p)), n))
}

func setClipboardText(hwnd uintptr, s string) bool {
	u, err := windows.UTF16FromString(s)
	if err != nil {
		return false
	}
	if !openClipboard(hwnd) {
		return false
	}
	defer procCloseClipboard.Call()
	procEmptyClipboard.Call()
	h, _, _ := procGlobalAlloc.Call(gmemMoveable, uintptr(len(u)*2))
	if h == 0 {
		return false
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		procGlobalFree.Call(h)
		return false
	}
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(p)), len(u)), u)
	procGlobalUnlock.Call(h)
	if r, _, _ := procSetClipboardData.Call(cfUnicodeText, h); r == 0 {
		procGlobalFree.Call(h)
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// COM helpers
// ---------------------------------------------------------------------------

// comObj is any COM interface pointer: its first field points at the method table.
type comObj struct{ vtbl unsafe.Pointer }

func (o *comObj) call(method int, args ...uintptr) uintptr {
	fn := *(*uintptr)(unsafe.Add(o.vtbl, method*int(unsafe.Sizeof(uintptr(0)))))
	all := append([]uintptr{uintptr(unsafe.Pointer(o))}, args...)
	r, _, _ := syscall.SyscallN(fn, all...)
	return r
}

func (o *comObj) release() { o.call(2) }

// runSTA runs f on its own OS thread with COM ready, so shell calls never block the window.
func runSTA(f func()) {
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		_ = windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED|windows.COINIT_DISABLE_OLE1DDE)
		defer windows.CoUninitialize()
		f()
	}()
}

var (
	clsidFileOpenDialog = windows.GUID{Data1: 0xDC1C5A9C, Data2: 0xE88A, Data3: 0x4DDE, Data4: [8]byte{0xA5, 0xA1, 0x60, 0xF8, 0x2A, 0x20, 0xAE, 0xF7}}
	iidIFileOpenDialog  = windows.GUID{Data1: 0xD57C7288, Data2: 0xD4AD, Data3: 0x4768, Data4: [8]byte{0xBE, 0x02, 0x9D, 0x96, 0x95, 0x32, 0xD9, 0x60}}
	iidIShellItem       = windows.GUID{Data1: 0x43826D1E, Data2: 0xE718, Data3: 0x42EE, Data4: [8]byte{0xBC, 0x55, 0xA1, 0xE2, 0x61, 0xC3, 0x7B, 0xFE}}
	clsidTaskbarList    = windows.GUID{Data1: 0x56FDF344, Data2: 0xFD6D, Data3: 0x11D0, Data4: [8]byte{0x95, 0x8A, 0x00, 0x60, 0x97, 0xC9, 0xA0, 0x90}}
	iidITaskbarList3    = windows.GUID{Data1: 0xEA1AFB91, Data2: 0x9E28, Data3: 0x4B86, Data4: [8]byte{0x90, 0xE9, 0x9E, 0x9F, 0x8A, 0x5E, 0xEF, 0xAF}}
)

const (
	clsctxInprocServer = 0x1
	clsctxAll          = 0x17
	fosNoChangeDir     = 0x8
	fosPickFolders     = 0x20
	fosForceFileSystem = 0x40
	fosPathMustExist   = 0x800
	sigdnFileSysPath   = 0x80058000

	// IFileOpenDialog method numbers (IUnknown 0-2, IModalWindow 3, IFileDialog 4-26).
	fdShow             = 3
	fdSetFileTypes     = 4
	fdSetFileTypeIndex = 5
	fdSetOptions       = 9
	fdGetOptions       = 10
	fdSetFolder        = 12
	fdSetTitle         = 17
	fdSetOkButtonLabel = 18
	fdGetResult        = 20
	fdGetResults       = 27 // IFileOpenDialog
	siGetDisplayName   = 5
	siaGetCount        = 7 // IShellItemArray
	siaGetItemAt       = 8

	fosAllowMultiSelect = 0x200
	fosFileMustExist    = 0x1000
)

// fileFilter is one line of the "type of file" list of the open window ("*.mp3;*.mp4").
type fileFilter struct{ name, spec string }

type comdlgFilterSpec struct{ name, spec *uint16 }

// pickFiles shows Windows' own "open" window, where several files can be chosen. It must
// run on the UI thread. Windows remembers the folder it was in last time.
func pickFiles(owner uintptr, title, okLabel string, filters []fileFilter) []string {
	var dlg *comObj
	hr, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidFileOpenDialog)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidIFileOpenDialog)), uintptr(unsafe.Pointer(&dlg)))
	if hresultFailed(hr) || dlg == nil {
		return nil
	}
	defer dlg.release()
	var opts uint32
	dlg.call(fdGetOptions, uintptr(unsafe.Pointer(&opts)))
	dlg.call(fdSetOptions, uintptr(opts|fosAllowMultiSelect|fosFileMustExist|fosForceFileSystem|fosPathMustExist|fosNoChangeDir))
	specs := make([]comdlgFilterSpec, len(filters))
	for i, f := range filters {
		specs[i] = comdlgFilterSpec{utf16(f.name), utf16(f.spec)}
	}
	if len(specs) > 0 {
		dlg.call(fdSetFileTypes, uintptr(len(specs)), uintptr(unsafe.Pointer(&specs[0])))
		dlg.call(fdSetFileTypeIndex, 1)
	}
	dlg.call(fdSetTitle, uintptr(unsafe.Pointer(utf16(title))))
	dlg.call(fdSetOkButtonLabel, uintptr(unsafe.Pointer(utf16(okLabel))))
	shown := dlg.call(fdShow, owner)
	runtime.KeepAlive(specs)
	if hresultFailed(shown) {
		return nil // includes "cancelled"
	}
	var arr *comObj
	if hr := dlg.call(fdGetResults, uintptr(unsafe.Pointer(&arr))); hresultFailed(hr) || arr == nil {
		return nil
	}
	defer arr.release()
	var n uint32
	arr.call(siaGetCount, uintptr(unsafe.Pointer(&n)))
	var out []string
	for i := uint32(0); i < n; i++ {
		var item *comObj
		if hr := arr.call(siaGetItemAt, uintptr(i), uintptr(unsafe.Pointer(&item))); hresultFailed(hr) || item == nil {
			continue
		}
		var psz *uint16
		if hr := item.call(siGetDisplayName, sigdnFileSysPath, uintptr(unsafe.Pointer(&psz))); !hresultFailed(hr) && psz != nil {
			out = append(out, windows.UTF16PtrToString(psz))
			windows.CoTaskMemFree(unsafe.Pointer(psz))
		}
		item.release()
	}
	return out
}

// pickFolder shows Windows' own "choose a folder" window. It must run on the UI thread.
func pickFolder(owner uintptr, title, okLabel, start string) (string, bool) {
	var dlg *comObj
	hr, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidFileOpenDialog)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidIFileOpenDialog)), uintptr(unsafe.Pointer(&dlg)))
	if hresultFailed(hr) || dlg == nil {
		return "", false
	}
	defer dlg.release()
	var opts uint32
	dlg.call(fdGetOptions, uintptr(unsafe.Pointer(&opts)))
	dlg.call(fdSetOptions, uintptr(opts|fosPickFolders|fosForceFileSystem|fosPathMustExist|fosNoChangeDir))
	dlg.call(fdSetTitle, uintptr(unsafe.Pointer(utf16(title))))
	dlg.call(fdSetOkButtonLabel, uintptr(unsafe.Pointer(utf16(okLabel))))
	if start != "" {
		var item *comObj
		hr, _, _ := procSHCreateItemFromParsing.Call(uintptr(unsafe.Pointer(utf16(start))), 0,
			uintptr(unsafe.Pointer(&iidIShellItem)), uintptr(unsafe.Pointer(&item)))
		if !hresultFailed(hr) && item != nil {
			dlg.call(fdSetFolder, uintptr(unsafe.Pointer(item)))
			item.release()
		}
	}
	if hr := dlg.call(fdShow, owner); hresultFailed(hr) {
		return "", false // includes "cancelled"
	}
	var res *comObj
	if hr := dlg.call(fdGetResult, uintptr(unsafe.Pointer(&res))); hresultFailed(hr) || res == nil {
		return "", false
	}
	defer res.release()
	var psz *uint16
	if hr := res.call(siGetDisplayName, sigdnFileSysPath, uintptr(unsafe.Pointer(&psz))); hresultFailed(hr) || psz == nil {
		return "", false
	}
	defer windows.CoTaskMemFree(unsafe.Pointer(psz))
	return windows.UTF16PtrToString(psz), true
}

// ---------------------------------------------------------------------------
// Taskbar button progress (ITaskbarList3), used from the UI thread
// ---------------------------------------------------------------------------

const (
	tbHrInit           = 3
	tbSetProgressValue = 9
	tbSetProgressState = 10

	TBPF_NOPROGRESS    = 0
	TBPF_INDETERMINATE = 0x1
	TBPF_NORMAL        = 0x2
	TBPF_ERROR         = 0x4
)

type taskbar struct {
	obj   *comObj
	hwnd  uintptr
	state int
}

func newTaskbar(hwnd uintptr) *taskbar {
	t := &taskbar{hwnd: hwnd, state: -1}
	var obj *comObj
	hr, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidTaskbarList)), 0, clsctxAll,
		uintptr(unsafe.Pointer(&iidITaskbarList3)), uintptr(unsafe.Pointer(&obj)))
	if hresultFailed(hr) || obj == nil {
		return t
	}
	if hr := obj.call(tbHrInit); hresultFailed(hr) {
		obj.release()
		return t
	}
	t.obj = obj
	return t
}

func (t *taskbar) set(state int, percent float64) {
	if t == nil || t.obj == nil {
		return
	}
	if state != t.state {
		t.obj.call(tbSetProgressState, t.hwnd, uintptr(state))
		t.state = state
	}
	if state == TBPF_NORMAL || state == TBPF_ERROR {
		p := uint64(percent * 10)
		if p > 1000 {
			p = 1000
		}
		t.obj.call(tbSetProgressValue, t.hwnd, uintptr(p), 1000)
	}
}

type flashInfo struct {
	cbSize    uint32
	hwnd      uintptr
	dwFlags   uint32
	uCount    uint32
	dwTimeout uint32
}

// flashIfInBackground makes the taskbar button blink until the user looks at the window.
func flashIfInBackground(hwnd uintptr) {
	if fg, _, _ := procGetForegroundWindow.Call(); fg == hwnd {
		return
	}
	fi := flashInfo{hwnd: hwnd, dwFlags: 0x2 | 0xC} // FLASHW_TRAY | FLASHW_TIMERNOFG
	fi.cbSize = uint32(unsafe.Sizeof(fi))
	procFlashWindowEx.Call(uintptr(unsafe.Pointer(&fi)))
}

// ---------------------------------------------------------------------------
// Shell actions (run on their own thread)
// ---------------------------------------------------------------------------

func shellOpen(path string) {
	runSTA(func() {
		_ = windows.ShellExecute(0, utf16("open"), utf16(path), nil, utf16(filepath.Dir(path)), windows.SW_SHOWNORMAL)
	})
}

// openURL opens a web address in the default browser (or the app registered for it).
func openURL(url string) {
	runSTA(func() {
		_ = windows.ShellExecute(0, utf16("open"), utf16(url), nil, nil, windows.SW_SHOWNORMAL)
	})
}

func showInFolder(path string) {
	runSTA(func() {
		pidl := callIf(procILCreateFromPathW, uintptr(unsafe.Pointer(utf16(path))))
		if pidl != 0 {
			hr := callIf(procSHOpenFolderAndSelect, pidl, 0, 0, 0)
			if procILFree.Find() == nil {
				procILFree.Call(pidl)
			}
			if procSHOpenFolderAndSelect.Find() == nil && !hresultFailed(hr) {
				return
			}
		}
		cmd := exec.Command("explorer.exe")
		cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `explorer.exe /select,"` + path + `"`}
		_ = cmd.Start()
	})
}

// ---------------------------------------------------------------------------
// Window look
// ---------------------------------------------------------------------------

func systemUsesDarkApps() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("AppsUseLightTheme")
	return err == nil && v == 0
}

// applyTitleBar paints the Windows 11 title bar black, like the header below it.
func applyTitleBar(hwnd uintptr) {
	dark := systemUsesDarkApps()
	var on int32
	if dark {
		on = 1
	}
	callIf(procDwmSetWindowAttribute, hwnd, 20, uintptr(unsafe.Pointer(&on)), 4) // DWMWA_USE_IMMERSIVE_DARK_MODE
	caption := uint32(0x00000000)                                                // black, like the header (0x00BBGGRR)
	text := uint32(0x00FFFFFF)
	callIf(procDwmSetWindowAttribute, hwnd, 35, uintptr(unsafe.Pointer(&caption)), 4) // DWMWA_CAPTION_COLOR
	callIf(procDwmSetWindowAttribute, hwnd, 34, uintptr(unsafe.Pointer(&caption)), 4) // DWMWA_BORDER_COLOR
	callIf(procDwmSetWindowAttribute, hwnd, 36, uintptr(unsafe.Pointer(&text)), 4)    // DWMWA_TEXT_COLOR
}

func systemDPI() float64 {
	if procGetDpiForSystem.Find() == nil {
		if d, _, _ := procGetDpiForSystem.Call(); d >= 96 {
			return float64(d)
		}
	}
	return 96
}

func workArea() rect {
	var r rect
	procSystemParametersInfoW.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&r)), 0)
	return r
}

// trayCorner tells in which corner of the screen the clock and its icons are (bottom
// right usually, bottom left on Windows in Arabic, top when the taskbar is up there).
func trayCorner(wa rect) (left, top bool) {
	tray, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(utf16("Shell_TrayWnd"))), 0)
	if tray == 0 {
		return false, false
	}
	// Only a real taskbar, along a side of the screen, tells.
	var bar rect
	procGetWindowRect.Call(tray, uintptr(unsafe.Pointer(&bar)))
	sw, _, _ := procGetSystemMetrics.Call(0) // SM_CXSCREEN
	sh, _, _ := procGetSystemMetrics.Call(1) // SM_CYSCREEN
	if int(bar.Right-bar.Left) < int(sw)/2 && int(bar.Bottom-bar.Top) < int(sh)/2 {
		return false, false
	}
	area := tray
	if n, _, _ := procFindWindowExW.Call(tray, 0, uintptr(unsafe.Pointer(utf16("TrayNotifyWnd"))), 0); n != 0 {
		area = n
	}
	var r rect
	if ok, _, _ := procGetWindowRect.Call(area, uintptr(unsafe.Pointer(&r))); ok == 0 {
		return false, false
	}
	return (r.Left+r.Right)/2 < (wa.Left+wa.Right)/2, (r.Top+r.Bottom)/2 < (wa.Top+wa.Bottom)/2
}

func centerWindow(hwnd uintptr) {
	var wr rect
	procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&wr)))
	wa := workArea()
	w, h := wr.Right-wr.Left, wr.Bottom-wr.Top
	if wa.Right-wa.Left <= 0 || w <= 0 {
		return
	}
	x := wa.Left + (wa.Right-wa.Left-w)/2
	y := wa.Top + (wa.Bottom-wa.Top-h)/2
	if y < wa.Top {
		y = wa.Top
	}
	procSetWindowPos.Call(hwnd, 0, uintptr(x), uintptr(y), 0, 0, swpNoZOrder|swpNoActivate|0x0001) // SWP_NOSIZE
}

func messageBox(hwnd uintptr, title, text string, flags uint32) int {
	const mbRight, mbRTLReading = 0x00080000, 0x00100000
	if !isEnglish() {
		flags |= mbRight | mbRTLReading
	}
	r, _, _ := procMessageBoxW.Call(hwnd, uintptr(unsafe.Pointer(utf16(text))), uintptr(unsafe.Pointer(utf16(title))), uintptr(flags))
	return int(r)
}

// focusExistingWindow brings an already running copy to the front.
func focusExistingWindow(title string) bool {
	h, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(utf16("webview"))), uintptr(unsafe.Pointer(utf16(title))))
	if h == 0 {
		return false
	}
	if iconic, _, _ := procIsIconic.Call(h); iconic != 0 {
		procShowWindow.Call(h, swRestore)
	}
	procSetForegroundWindow.Call(h)
	return true
}
