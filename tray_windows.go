//go:build windows

package main

// HELINK's icon near the clock: it stays there while the window is closed, so downloads
// go on and the browsers can hand files over at any time. A click opens the window, a
// right-click its menu; a message says when a download finishes.

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procShellNotifyIconW         = shell32.NewProc("Shell_NotifyIconW")
	procRegisterWindowMessageW   = user32.NewProc("RegisterWindowMessageW")
	procCreatePopupMenu          = user32.NewProc("CreatePopupMenu")
	procAppendMenuW              = user32.NewProc("AppendMenuW")
	procTrackPopupMenuEx         = user32.NewProc("TrackPopupMenuEx")
	procDestroyMenu              = user32.NewProc("DestroyMenu")
	procGetCursorPos             = user32.NewProc("GetCursorPos")
	procLoadImageW               = user32.NewProc("LoadImageW")
	procGetSystemMetrics         = user32.NewProc("GetSystemMetrics")
	procSetMenuDefaultItem       = user32.NewProc("SetMenuDefaultItem")
	procIsWindowVisible          = user32.NewProc("IsWindowVisible")
	procGetWindowThreadProcessId = user32.NewProc("GetWindowThreadProcessId")
	procAttachThreadInput        = user32.NewProc("AttachThreadInput")
	procBringWindowToTop         = user32.NewProc("BringWindowToTop")
	procGetCurrentThreadId       = kernel32.NewProc("GetCurrentThreadId")
)

const (
	wmTray          = 0x8000 + 77 // WM_APP + 77: the icon's messages
	wmNull          = 0x0000
	wmContextMenu   = 0x007B
	wmLButtonUp     = 0x0202
	wmRButtonUp     = 0x0205
	ninSelect       = 0x0400
	ninKeySelect    = 0x0401
	ninBalloonClick = 0x0405
	nimAdd          = 0
	nimModify       = 1
	nimDelete       = 2
	nimSetVersion   = 4
	nifMessage      = 0x01
	nifIcon         = 0x02
	nifTip          = 0x04
	nifInfo         = 0x10
	nifShowTip      = 0x80
	niifUser        = 0x04
	niifLargeIcon   = 0x20
	mfString        = 0x0000
	mfSeparator     = 0x0800
	mfGrayed        = 0x0001
	tpmReturnCmd    = 0x0100
	tpmRightButton  = 0x0002
	tpmLayoutRTL    = 0x8000
	tpmRightAlign   = 0x0008
	tpmBottomAlign  = 0x0020
)

type notifyIconData struct {
	CbSize           uint32
	HWnd             uintptr
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            uintptr
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         windows.GUID
	HBalloonIcon     uintptr
}

type tray struct {
	hwnd    uintptr
	icon    uintptr
	big     uintptr
	created uintptr // "TaskbarCreated": Explorer started again, the icon must be added again
	tip     string
	// lastAbout is what the last message from the icon was about ("update": a new version of
	// HELINK, which clicking the message shows).
	lastAbout string
}

func loadIcon(size int) uintptr {
	var inst windows.Handle
	_ = windows.GetModuleHandleEx(0, nil, &inst)
	h, _, _ := procLoadImageW.Call(uintptr(inst), 1, 1 /* IMAGE_ICON */, uintptr(size), uintptr(size), 0)
	return h
}

func newTray(hwnd uintptr) *tray {
	small, _, _ := procGetSystemMetrics.Call(49) // SM_CXSMICON
	if small == 0 {
		small = 16
	}
	t := &tray{hwnd: hwnd, icon: loadIcon(int(small)), big: loadIcon(64), tip: "HELINK"}
	t.created, _, _ = procRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(utf16("TaskbarCreated"))))
	t.add()
	return t
}

func (t *tray) base() notifyIconData {
	n := notifyIconData{HWnd: t.hwnd, UID: 1}
	n.CbSize = uint32(unsafe.Sizeof(n))
	return n
}

func copyUTF16(dst []uint16, s string) {
	u, _ := windows.UTF16FromString(s)
	if len(u) > len(dst) {
		u = u[:len(dst)]
		u[len(u)-1] = 0
	}
	copy(dst, u)
}

func (t *tray) add() {
	n := t.base()
	n.UFlags = nifMessage | nifIcon | nifTip | nifShowTip
	n.UCallbackMessage = wmTray
	n.HIcon = t.icon
	copyUTF16(n.SzTip[:], t.tip)
	procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&n)))
	n.UVersion = 4 // NOTIFYICON_VERSION_4
	procShellNotifyIconW.Call(nimSetVersion, uintptr(unsafe.Pointer(&n)))
}

func (t *tray) remove() {
	n := t.base()
	procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&n)))
}

// setTip changes the words shown over the icon.
func (t *tray) setTip(s string) {
	if s == t.tip {
		return
	}
	t.tip = s
	n := t.base()
	n.UFlags = nifTip | nifShowTip
	copyUTF16(n.SzTip[:], s)
	procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&n)))
}

// balloon shows a message from the icon (a notification on Windows 10 and 11).
func (t *tray) balloon(title, text string) { t.balloonAbout("", title, text) }

// balloonAbout shows a message that is about something (lastAbout), which a click on it opens.
func (t *tray) balloonAbout(about, title, text string) {
	t.lastAbout = about
	n := t.base()
	n.UFlags = nifInfo
	copyUTF16(n.SzInfoTitle[:], title)
	copyUTF16(n.SzInfo[:], text)
	n.DwInfoFlags = niifUser | niifLargeIcon
	n.HBalloonIcon = t.big
	procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&n)))
}

// menuItem is one line of the icon's menu (id 0: a separator).
type menuItem struct {
	id      uintptr
	text    string
	grayed  bool
	isFirst bool // shown in bold (what a click does)
}

// popup shows the menu at the mouse and returns the chosen id (0: none).
func (t *tray) popup(items []menuItem) uintptr {
	m, _, _ := procCreatePopupMenu.Call()
	if m == 0 {
		return 0
	}
	defer procDestroyMenu.Call(m)
	for _, it := range items {
		if it.id == 0 {
			procAppendMenuW.Call(m, mfSeparator, 0, 0)
			continue
		}
		flags := uintptr(mfString)
		if it.grayed {
			flags |= mfGrayed
		}
		procAppendMenuW.Call(m, flags, it.id, uintptr(unsafe.Pointer(utf16(it.text))))
		if it.isFirst {
			procSetMenuDefaultItem.Call(m, it.id, 0)
		}
	}
	var pt struct{ X, Y int32 }
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	flags := uintptr(tpmReturnCmd | tpmRightButton | tpmBottomAlign)
	if !isEnglish() {
		flags |= tpmLayoutRTL | tpmRightAlign
	}
	// Without this, the menu would not close when clicking elsewhere.
	procSetForegroundWindow.Call(t.hwnd)
	id, _, _ := procTrackPopupMenuEx.Call(m, flags, uintptr(pt.X), uintptr(pt.Y), t.hwnd, 0)
	procPostMessageW.Call(t.hwnd, wmNull, 0, 0)
	return id
}

func windowVisible(hwnd uintptr) bool {
	r, _, _ := procIsWindowVisible.Call(hwnd)
	return r != 0
}

// bringToFront shows the window above the others and gives it the keyboard. Windows lets
// a program in the background do this only with permission (helink-host passes on the
// browser's); otherwise the window joins the thread of the one in front for a moment.
func bringToFront(hwnd uintptr, stayOnTop bool) {
	if iconic, _, _ := procIsIconic.Call(hwnd); iconic != 0 {
		procShowWindow.Call(hwnd, swRestore)
	} else {
		procShowWindow.Call(hwnd, 5) // SW_SHOW
	}
	const swpNoMove, swpNoSize, swpShow = 0x0002, 0x0001, 0x0040
	topmost, notTopmost := ^uintptr(0), ^uintptr(1) // HWND_TOPMOST (-1), HWND_NOTOPMOST (-2)
	procSetWindowPos.Call(hwnd, topmost, 0, 0, 0, 0, swpNoMove|swpNoSize|swpShow)
	if r, _, _ := procSetForegroundWindow.Call(hwnd); r == 0 {
		fg, _, _ := procGetForegroundWindow.Call()
		fgThread, _, _ := procGetWindowThreadProcessId.Call(fg, 0)
		me, _, _ := procGetCurrentThreadId.Call()
		if fgThread != 0 && fgThread != me {
			procAttachThreadInput.Call(me, fgThread, 1)
			procBringWindowToTop.Call(hwnd)
			procSetForegroundWindow.Call(hwnd)
			procAttachThreadInput.Call(me, fgThread, 0)
		}
	}
	if !stayOnTop {
		procSetWindowPos.Call(hwnd, notTopmost, 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoActivate)
	}
}
