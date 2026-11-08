//go:build windows

package main

import (
	"bytes"
	"encoding/json"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"time"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
	"github.com/jchv/go-webview2/webviewloader"
	"golang.org/x/sys/windows"

	"nazzelha/ipc"
)

const windowTitle = "HELINK"

// The owner's numbers, for the About page.
const (
	contactPhone    = "01500025887"
	contactWhatsApp = "https://wa.me/201500025887"
)

// winHost connects the App to the WebView2 window and to Windows.
type winHost struct {
	w          webview2.WebView
	hwnd       uintptr
	app        *App
	paths      Paths
	tb         *taskbar
	tray       *tray
	allowClose atomic.Bool
	origProc   uintptr
	scale      float64
	// Only touched on the window's thread:
	awake     bool              // the computer is kept from sleeping while downloads run
	states    map[string]string // the last state of each download, to notice when one ends
	compact   bool              // the window is only the "new download" (or a video's) window
	compactOf string            // why: "file", "video", or "task" (the downloads started in the browser)
	miniH     float64           // the height the page asks for the downloads it shows ("task"), in CSS pixels
	miniIDs   map[string]bool   // those downloads (no message near the clock for them: the window shows them)
	normal    rect              // the window's place outside the compact mode
	reZoom    bool              // it was maximized before: maximized again when the full window shows
	trayHint  bool              // "HELINK is still here" was said once
	lastTip   time.Time
	shownOnce bool
	minimized bool         // the window is minimized on the taskbar
	early     []earlyEvent // events sent before the page was ready, given to it when it is
	// The window's page (winui_windows.go):
	ui        uiState
	uiMaking  bool      // its making is on its way
	uiGen     int       // counts the pages made and loaded: what was meant for an older one does nothing
	uiMade    int       // pages made since HELINK started
	uiFails   int       // makings that failed in a row
	onReady   []func()  // to do once the page is ready
	lastCheck time.Time // when the page last had to answer
	hideGen   int       // counts the window's hidings and showings (closing the page hidden long)
	pingSeq   int
	pongSeq   int
}

type earlyEvent struct{ name, js string }

// Emit delivers an event to the page. It may be called from any goroutine. Until the page
// has started (a browser can ask for a download while HELINK is still opening), events
// wait, in order.
func (h *winHost) Emit(event string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		log.Printf("emit %s: %v", event, err)
		return
	}
	name, _ := json.Marshal(event)
	js := "window.nzEvent&&window.nzEvent(" + string(name) + "," + string(data) + ")"
	h.w.Dispatch(func() {
		switch {
		case h.ui == uiReady:
			h.w.Eval(js)
		case h.ui == uiNone && !h.uiMaking && (event == "task" || event == "thumb"):
			// No page: the one made later gets the whole list, with its pictures (nz_init).
		default:
			h.keepEarly(event, js)
		}
		h.afterEvent(event, payload)
	})
}

// keepEarly keeps an event for the page until it is ready (at most a few hundred: the
// oldest progress of the downloads goes first, the list itself comes with nz_init).
func (h *winHost) keepEarly(event, js string) {
	const max = 400
	if len(h.early) >= max {
		drop := 0
		for i, e := range h.early {
			if e.name == "task" {
				drop = i
				break
			}
		}
		h.early = append(h.early[:drop], h.early[drop+1:]...)
	}
	h.early = append(h.early, earlyEvent{event, js})
}

// afterEvent mirrors progress on the taskbar button and the icon near the clock, tells
// when a download ends in the background, and keeps the computer awake while something
// downloads.
func (h *winHost) afterEvent(event string, payload any) {
	switch ev := payload.(type) {
	case taskEvent:
		active, pct := h.app.Progress()
		switch {
		case active == 0:
			h.tb.set(TBPF_NOPROGRESS, 0)
		case pct < 0:
			h.tb.set(TBPF_INDETERMINATE, 0)
		default:
			h.tb.set(TBPF_NORMAL, pct)
		}
		h.keepAwake(active > 0)
		h.updateTip(active, pct)
		if ev.Op == "remove" {
			delete(h.states, ev.ID)
			return
		}
		id, _ := ev.Task["id"].(string)
		st, _ := ev.Task["state"].(string)
		if old, seen := h.states[id]; seen && old != st && (st == stateDone || st == stateError) {
			flashIfInBackground(h.hwnd)
			fg, _, _ := procGetForegroundWindow.Call()
			inMini := h.compact && h.compactOf == "task" && h.miniIDs[id] && windowVisible(h.hwnd)
			if (fg != h.hwnd || !windowVisible(h.hwnd)) && !inMini && !h.app.store.Get().NoNotify {
				name, _ := ev.Task["title"].(string)
				sep := ev.Task["type"] == typeSep
				switch {
				case st == stateDone && sep:
					h.tray.balloon(tr("أُزيلت الموسيقى", "Music removed"), name)
				case st == stateDone:
					h.tray.balloon(tr("اكتمل التنزيل", "Download complete"), name)
				case sep:
					h.tray.balloon(tr("تعذّرت إزالة الموسيقى", "Couldn't remove the music"), name)
				default:
					h.tray.balloon(tr("تعذّر إكمال التنزيل", "Download failed"), name)
				}
			}
		}
		h.states[id] = st
	case SetupProgress:
		if ev.Phase == "done" {
			h.tb.set(TBPF_NOPROGRESS, 0)
		} else {
			h.tb.set(TBPF_NORMAL, ev.Percent)
		}
	case map[string]any:
		if event == "browserTask" {
			if id, _ := ev["id"].(string); id != "" {
				if h.miniIDs == nil {
					h.miniIDs = map[string]bool{}
				}
				h.miniIDs[id] = true
			}
		}
		if event == "setup" {
			h.tb.set(TBPF_NOPROGRESS, 0)
			flashIfInBackground(h.hwnd)
		}
		if event == "update" {
			// A new version, and the window is not open: a message from the icon near the
			// clock (clicking it opens the window, where the update is offered).
			if p, _ := ev["prompt"].(bool); p && !windowVisible(h.hwnd) {
				v, _ := ev["version"].(string)
				h.tray.balloonAbout("update", tr("يتوفر إصدار جديد من HELINK", "A new version of HELINK is available"),
					tr("الإصدار "+v+" جاهز. اضغط هنا لتحديث HELINK.", "Version "+v+" is ready. Click here to update HELINK."))
			}
		}
	}
}

// updateTip writes what downloads over the icon ("HELINK – 2 downloads, 45%").
func (h *winHost) updateTip(active int, pct float64) {
	if time.Since(h.lastTip) < time.Second && active > 0 {
		return
	}
	h.lastTip = time.Now()
	tip := "HELINK"
	switch {
	case active == 1 && pct >= 0:
		tip += tr(" – تنزيل واحد، ", " – 1 download, ") + trimFloat(pct, 0) + "%"
	case active > 1 && pct >= 0:
		tip += " – " + tr(itoa(active)+" تنزيلات، ", itoa(active)+" downloads, ") + trimFloat(pct, 0) + "%"
	case active > 0:
		tip += " – " + tr(itoa(active)+" تنزيل", itoa(active)+" downloading")
	}
	h.tray.setTip(tip)
}

func itoa(n int) string { return trimFloat(float64(n), 0) }

func (h *winHost) keepAwake(on bool) {
	if on == h.awake {
		return
	}
	h.awake = on
	const esContinuous, esSystemRequired = 0x80000000, 0x00000001
	if on {
		callIf(procSetThreadExecutionState, esContinuous|esSystemRequired)
	} else {
		callIf(procSetThreadExecutionState, esContinuous)
	}
}

func hasArg(a string) bool {
	for _, x := range os.Args[1:] {
		if strings.EqualFold(x, a) {
			return true
		}
	}
	return false
}

// showRunning asks the HELINK that runs already to show its window.
func showRunning(data string) bool {
	info, err := ipc.ReadInfo(data)
	if err != nil {
		return false
	}
	// The user just opened this copy, so it may bring the other's window to the front.
	callIf(procAllowSetForegroundWindow, uintptr(info.PID))
	resp, err := ipc.Call(info, ipc.Request{Type: "show", PID: os.Getpid()}, 5*time.Second)
	return err == nil && bytes.Contains(resp, []byte(`"ok":true`))
}

var procAllowSetForegroundWindow = user32.NewProc("AllowSetForegroundWindow")

// quitRunning asks the HELINK that runs to close (the installer does, before it replaces
// HELINK.exe; its downloads pause and continue next time), and waits until it has.
func quitRunning(data string) {
	info, err := ipc.ReadInfo(data)
	if err != nil {
		return
	}
	if _, err := ipc.Call(info, ipc.Request{Type: "quit", PID: os.Getpid()}, 5*time.Second); err != nil {
		return
	}
	p, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(info.PID))
	if err != nil {
		return
	}
	defer windows.CloseHandle(p)
	_, _ = windows.WaitForSingleObject(p, 15000)
}

func main() {
	paths := newPaths(dataBaseDir())
	// For the installer: close the HELINK that runs; undo the registration (uninstall).
	if hasArg("--quit") || hasArg("--unregister") {
		quitRunning(paths.Data)
		if hasArg("--unregister") {
			unregister(paths.Data)
		}
		return
	}
	setupLogging(paths.Data)
	// Started by the HELINK it replaces (a portable copy's update): wait until that one closed.
	waitForUpdatedFrom()
	store := OpenStore(filepath.Join(paths.Data, "settings.json"))
	setLang(store.Get().Lang)
	background := hasArg("--background")
	defer func() {
		if r := recover(); r != nil {
			log.Printf("panic: %v\n%s", r, debug.Stack())
			messageBox(0, windowTitle, tr("حدثت مشكلة غير متوقعة وسيُغلق التطبيق. افتحه مرة أخرى.",
				"Something unexpected happened and the app will close. Please open it again."), 0x10)
		}
	}()

	// One copy at a time: opening it again shows the window of the one running.
	if _, err := windows.CreateMutex(nil, false, utf16(`Local\HELINK.SingleInstance`)); err == windows.ERROR_ALREADY_EXISTS {
		if background || showRunning(paths.Data) || focusExistingWindow(windowTitle) {
			return
		}
	}
	callIf(procSetCurrentProcessExplicitAppUserModelID, uintptr(unsafe.Pointer(utf16("HELINK.App"))))
	go integrate(paths, store.Get())
	preloadWebView2Loader(paths.Data)
	wv2, err := webviewloader.GetInstalledVersion()
	if wv2 == "" && err == nil {
		log.Printf("WebView2 is not installed")
		webView2Missing()
		return
	}
	log.Printf("WebView2 %s %v", wv2, err)
	background0 := webViewColors(systemUsesDarkApps())

	scale := systemDPI() / 96
	width, height := int(980*scale), int(680*scale)
	wa := workArea()
	if aw := int(wa.Right - wa.Left); aw > 0 && width > aw-int(24*scale) {
		width = aw - int(24*scale)
	}
	if ah := int(wa.Bottom - wa.Top); ah > 0 && height > ah-int(16*scale) {
		height = ah - int(16*scale)
	}

	// The window only: its page is made when it shows (winui_windows.go).
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		AutoFocus: true,
		DataPath:  filepath.Join(paths.Data, "webview2"),
		Deferred:  true,
		WindowOptions: webview2.WindowOptions{
			Title: windowTitle, Width: uint(width), Height: uint(height), IconId: 1, Center: true, Hidden: background,
			Background: background0, HasBackground: true,
		},
	})
	if w == nil {
		log.Printf("the window could not be made")
		messageBox(0, windowTitle, tr("حدثت مشكلة غير متوقعة وسيُغلق التطبيق. افتحه مرة أخرى.",
			"Something unexpected happened and the app will close. Please open it again."), 0x10)
		return
	}

	h := &winHost{w: w, hwnd: uintptr(w.Window()), states: map[string]string{}, paths: paths, scale: scale}
	w.OnProcessFailed(func(kind int) {
		// In WebView2's own event: acted on a moment later, from the main loop.
		w.Dispatch(func() { h.processFailed(kind) })
	})
	h.app = NewApp(h, paths)
	h.app.Show = func(reason string) { w.Dispatch(func() { h.showFor(reason) }) }
	h.app.Quit = func() {
		w.Dispatch(func() {
			h.allowClose.Store(true)
			procPostMessageW.Call(h.hwnd, wmClose, 0, 0)
		})
	}
	h.app.SettingsChanged = func(s Settings) {
		go func() {
			if exe, err := os.Executable(); err == nil {
				setAutostart(exe, !s.NoAutostart)
			}
		}()
	}
	h.tb = newTaskbar(h.hwnd)
	w.SetSize(int(520*scale), int(440*scale), webview2.HintMin)
	centerWindow(h.hwnd)
	procGetWindowRect.Call(h.hwnd, uintptr(unsafe.Pointer(&h.normal)))
	h.shownOnce = !background
	applyTitleBar(h.hwnd)
	h.subclass()
	h.tray = newTray(h.hwnd)
	h.bind(paths)
	env, _ := json.Marshal(map[string]any{"lang": curLang(), "platform": "windows", "background": background})
	w.Init("window.__nzEnv=" + string(env) + ";")
	w.SetHtml(buildHTML())
	if !background {
		h.makeUI() // the window shows now; waiting near the clock, it is made when it first shows
	}
	go h.watchWindowThread()
	if len(h.app.tools.Missing()) == 0 {
		h.app.StartMaintenance()
	}
	h.app.StartUpdates()
	ln, err := h.app.ListenForBrowsers()
	if err != nil {
		log.Printf("browsers cannot reach HELINK: %v", err)
	}
	log.Printf("window ready (dpi scale %.2f, background %v)", scale, background)
	w.Run()
	h.tray.remove()
	if ln != nil {
		ln.Close()
	}
	h.app.Shutdown()
	log.Printf("bye")
}

// ---------------------------------------------------------------------------
// Showing and hiding the window
// ---------------------------------------------------------------------------

const (
	swHide            = 0
	swMaximize        = 3
	swShow            = 5
	swpNoSize         = 0x0001
	swpNoMove         = 0x0002
	swpShowWindowFlag = 0x0040
	swpHideWindowFlag = 0x0080
	sizeMinimized     = 1
)

var (
	hwndTopmost    = ^uintptr(0) // -1
	hwndNotTopmost = ^uintptr(1) // -2
)

// openWindow shows the full window (from the icon, or HELINK opened again).
func (h *winHost) openWindow() {
	if h.compact {
		h.leaveCompact()
	}
	if !h.shownOnce {
		h.shownOnce = true
		procSetWindowPos.Call(h.hwnd, 0, uintptr(h.normal.Left), uintptr(h.normal.Top),
			uintptr(h.normal.Right-h.normal.Left), uintptr(h.normal.Bottom-h.normal.Top), swpNoZOrder|swpNoActivate)
	}
	bringToFront(h.hwnd, false)
	if h.reZoom {
		h.reZoom = false
		procShowWindow.Call(h.hwnd, swMaximize)
	}
}

// showFor brings HELINK up for something the browser sent: the full window when it is
// open, else a small window with only the "new download" window (or the video's
// qualities), in front of the browser. For a download started from the browser's menu
// ("task"), the small window of downloads near the clock.
func (h *winHost) showFor(reason string) {
	switch reason {
	case "file", "video":
		if windowVisible(h.hwnd) && !h.compact {
			bringToFront(h.hwnd, false)
			return
		}
		h.enterCompact(reason)
	case "task":
		h.showTasks()
	default:
		h.openWindow()
	}
}

// saveNormal keeps the window's place before it becomes a small one. A window hidden
// while minimized or maximized comes back at the usual size (and maximized again).
func (h *winHost) saveNormal() {
	const gwlStyle, wsMinimize, wsMaximize = -16, 0x20000000, 0x01000000
	idx := int32(gwlStyle)
	style, _, _ := procGetWindowLongPtrW.Call(h.hwnd, uintptr(idx))
	if style&(wsMinimize|wsMaximize) != 0 {
		h.reZoom = style&wsMaximize != 0
		procSetWindowLongPtrW.Call(h.hwnd, uintptr(idx), style&^(wsMinimize|wsMaximize))
		h.normal = defaultWindowRect(h.scale)
		return
	}
	if windowVisible(h.hwnd) || h.shownOnce {
		procGetWindowRect.Call(h.hwnd, uintptr(unsafe.Pointer(&h.normal)))
	}
}

// defaultWindowRect is the full window's usual place: 980×680, in the middle of the screen.
func defaultWindowRect(scale float64) rect {
	w, ht := int(980*scale), int(680*scale)
	wa := workArea()
	aw, ah := int(wa.Right-wa.Left), int(wa.Bottom-wa.Top)
	if aw > 0 && w > aw-int(24*scale) {
		w = aw - int(24*scale)
	}
	if ah > 0 && ht > ah-int(16*scale) {
		ht = ah - int(16*scale)
	}
	x, y := int(wa.Left)+(aw-w)/2, int(wa.Top)+(ah-ht)/2
	return rect{int32(x), int32(y), int32(x + w), int32(y + ht)}
}

// enterCompact makes the window the small one with only the "new download" window (or
// the video's qualities), in the middle of the screen and in front of the browser.
func (h *winHost) enterCompact(reason string) {
	if h.compact && h.compactOf != "task" {
		bringToFront(h.hwnd, true) // it already shows a download waiting for an answer
		return
	}
	shown := h.compact // the small window of downloads: it becomes this one
	if !h.compact {
		h.saveNormal()
		h.compact = true
	}
	h.compactOf = reason
	w, ht := int(560*h.scale), int(500*h.scale)
	if reason == "video" {
		ht = int(640 * h.scale)
	}
	wa := workArea()
	if aw, ah := int(wa.Right-wa.Left), int(wa.Bottom-wa.Top); aw > 0 && ah > 0 {
		w, ht = min(w, aw), min(ht, ah)
	}
	x := int(wa.Left) + (int(wa.Right-wa.Left)-w)/2
	y := int(wa.Top) + (int(wa.Bottom-wa.Top)-ht)/2
	procSetWindowPos.Call(h.hwnd, hwndTopmost, uintptr(x), uintptr(y), uintptr(w), uintptr(ht), swpNoActivate)
	h.Emit("mode", map[string]any{"compact": true, "reason": reason})
	if shown {
		bringToFront(h.hwnd, true)
		return
	}
	// The page draws the small layout first (made first if need be), then the window appears.
	h.whenReady(func() {
		h.w.SetVisible(true)
		h.after(160*time.Millisecond, func() {
			if h.compact && h.compactOf == reason {
				bringToFront(h.hwnd, true)
			}
		})
	})
}

// miniWidth is the width of the small window of downloads, in CSS pixels.
const miniWidth = 400

// showTasks shows the downloads started from the browser's menu in a small window near the
// clock, above the other windows, leaving the keyboard to the browser. When the full
// window is open (or on the taskbar), the download is in its list and its taskbar button
// shows the progress; when a "new download" window waits for an answer, the page shows
// the downloads after it.
func (h *winHost) showTasks() {
	switch {
	case h.compact:
		return
	case windowVisible(h.hwnd):
		flashIfInBackground(h.hwnd)
		return
	}
	h.saveNormal()
	h.compact = true
	h.miniTasks(false)
}

// miniTasks makes the window the small window of downloads (from hidden, or from the small
// "new download" window once answered).
func (h *winHost) miniTasks(shown bool) {
	h.compactOf, h.miniH = "task", 0
	h.placeMini(shown)
	h.Emit("mode", map[string]any{"compact": true, "reason": "task"})
	if !shown {
		// The page draws the downloads first (made first if need be), then the window appears.
		h.whenReady(func() {
			h.w.SetVisible(true)
			h.after(160*time.Millisecond, func() {
				if h.compact && h.compactOf == "task" {
					h.placeMini(true)
				}
			})
		})
	}
}

// placeMini sizes the small window of downloads to what the page shows and puts it in the
// corner of the clock, showing it if show, without taking the keyboard.
func (h *winHost) placeMini(show bool) {
	var wr, cr rect
	procGetWindowRect.Call(h.hwnd, uintptr(unsafe.Pointer(&wr)))
	procGetClientRect.Call(h.hwnd, uintptr(unsafe.Pointer(&cr)))
	ncW := int((wr.Right - wr.Left) - (cr.Right - cr.Left))
	ncH := int((wr.Bottom - wr.Top) - (cr.Bottom - cr.Top))
	if ncW < 0 || ncW > int(64*h.scale) {
		ncW = int(16 * h.scale)
	}
	if ncH < 0 || ncH > int(96*h.scale) {
		ncH = int(40 * h.scale)
	}
	ch := h.miniH
	if ch <= 0 {
		ch = 132
	}
	w, ht := int(miniWidth*h.scale)+ncW, int(ch*h.scale)+ncH
	wa := workArea()
	if aw, ah := int(wa.Right-wa.Left), int(wa.Bottom-wa.Top); aw > 0 && ah > 0 {
		w, ht = min(w, aw), min(ht, ah)
	}
	// Windows draws the frame inside the window's rectangle on three sides, invisible.
	side := max(0, ncW/2-int(h.scale))
	m := int(14 * h.scale)
	left, top := trayCorner(wa)
	x, y := int(wa.Right)-m-w+side, int(wa.Bottom)-m-ht+side
	if left {
		x = int(wa.Left) + m - side
	}
	if top {
		y = int(wa.Top) + m
	}
	flags := uintptr(swpNoActivate)
	if show {
		flags |= swpShowWindowFlag
	}
	procSetWindowPos.Call(h.hwnd, hwndTopmost, uintptr(x), uintptr(y), uintptr(w), uintptr(ht), flags)
}

// leaveCompact makes the small window the full one again (the user asked to see all).
func (h *winHost) leaveCompact() {
	if !h.compact {
		return
	}
	h.compact, h.compactOf, h.miniIDs = false, "", nil
	h.shownOnce = true
	h.Emit("mode", map[string]any{"compact": false})
	procSetWindowPos.Call(h.hwnd, hwndNotTopmost, uintptr(h.normal.Left), uintptr(h.normal.Top),
		uintptr(h.normal.Right-h.normal.Left), uintptr(h.normal.Bottom-h.normal.Top), swpShowWindowFlag)
}

// dialogDone hides the small window once nothing waits in it.
func (h *winHost) dialogDone() {
	if !h.compact {
		return
	}
	h.compact, h.compactOf, h.miniIDs = false, "", nil
	procShowWindow.Call(h.hwnd, swHide)
	procSetWindowPos.Call(h.hwnd, hwndNotTopmost, uintptr(h.normal.Left), uintptr(h.normal.Top),
		uintptr(h.normal.Right-h.normal.Left), uintptr(h.normal.Bottom-h.normal.Top), swpNoActivate)
	h.Emit("mode", map[string]any{"compact": false})
}

// hideToTray closes the window but keeps HELINK running near the clock.
func (h *winHost) hideToTray() {
	procShowWindow.Call(h.hwnd, swHide)
	if !h.trayHint {
		h.trayHint = true
		h.tray.balloon(tr("ما زال HELINK يعمل هنا", "HELINK is still here"),
			tr("تستمر التنزيلات، ويلتقط HELINK التنزيلات من المتصفح. اضغط هذه الأيقونة لفتحه.",
				"Downloads go on, and HELINK still takes downloads from the browser. Click this icon to open it."))
	}
}

// pickSepFiles shows Windows' "open" window to choose files of the computer to remove the
// music from (several at once), and adds them. Run it on the UI thread.
func (h *winHost) pickSepFiles() {
	files := pickFiles(h.hwnd, tr("اختر ملفات صوت أو فيديو لإزالة الموسيقى منها", "Choose audio or video files to remove the music from"),
		tr("إزالة الموسيقى", "Remove the music"), []fileFilter{
			{tr("ملفات الصوت والفيديو", "Audio and video files"), sepFileTypes},
			{tr("كل الملفات", "All files"), "*.*"},
		})
	res := map[string]any{"ok": len(files) > 0, "ids": []string{}, "skipped": 0}
	if len(files) > 0 {
		r := h.app.AddSepFiles(files)
		res["ids"], res["skipped"] = r["ids"], r["skipped"]
	}
	// Emit also wakes the loop for anything queued while the window was open.
	h.Emit("sepPicked", res)
}

// exit quits HELINK (from the icon's menu), asking first when downloads run.
func (h *winHost) exit() {
	if h.app.Busy() {
		h.openWindow()
		what := "download"
		if h.app.SetupBusy() {
			what = "setup"
		}
		h.Emit("closeRequest", map[string]string{"what": what})
		return
	}
	h.allowClose.Store(true)
	procPostMessageW.Call(h.hwnd, wmClose, 0, 0)
}

func (h *winHost) trayMenu() {
	active := h.app.ActiveCount()
	paused := 0
	for _, t := range h.app.TaskList() {
		if s, _ := t["state"].(string); s == statePaused || s == stateError {
			paused++
		}
	}
	items := []menuItem{
		{id: 1, text: tr("فتح HELINK", "Open HELINK"), isFirst: true},
		{id: 2, text: tr("إضافة رابط…", "Add a link…")},
		{id: 6, text: tr("إزالة الموسيقى من ملفات…", "Remove the music from files…")},
	}
	if u := h.app.UpdateInfo(); u["state"] == "available" {
		v, _ := u["version"].(string)
		items = append(items, menuItem{id: 7, text: tr("تحديث HELINK إلى الإصدار "+v+"…", "Update HELINK to version "+v+"…")})
	}
	items = append(items, menuItem{},
		menuItem{id: 3, text: tr("إيقاف كل التنزيلات مؤقتًا", "Pause all downloads"), grayed: active == 0},
		menuItem{id: 4, text: tr("استئناف كل التنزيلات", "Resume all downloads"), grayed: paused == 0},
		menuItem{},
		menuItem{id: 5, text: tr("إنهاء HELINK", "Exit HELINK")})
	id := h.tray.popup(items)
	switch id {
	case 1:
		h.openWindow()
	case 2:
		h.openWindow()
		h.Emit("addLink", nil)
	case 6:
		h.openWindow()
		h.pickSepFiles()
	case 7:
		h.openWindow()
		h.Emit("showUpdate", nil)
	case 3:
		h.app.PauseAll()
	case 4:
		h.app.ResumeAll()
	case 5:
		h.exit()
	}
	// The menu's own message loop may have swallowed a wake-up of the window's queue.
	h.w.Dispatch(func() {})
}

// ---------------------------------------------------------------------------
// What the page can ask for
// ---------------------------------------------------------------------------

func (h *winHost) bind(paths Paths) {
	w, app := h.w, h.app
	w.Bind("nz_init", func() InitResult { return app.Init() })
	w.Bind("nz_ready", func() { h.pageStarted() })
	w.Bind("nz_pong", func(seq int) { h.pong(seq) })
	w.Bind("nz_setup", func() { app.StartSetup() })
	w.Bind("nz_setLang", func(lang string) { app.SetLang(lang) })
	w.Bind("nz_paste", func() string { return clipboardText(h.hwnd) })
	w.Bind("nz_fetch", func(text string) int { return app.Fetch(text) })
	w.Bind("nz_cancelFetch", func(forget bool) { app.CancelFetch(forget) })
	w.Bind("nz_download", func(item int, key string, noMusic bool) map[string]any { return app.DownloadWith(item, key, noMusic) })
	w.Bind("nz_downloadList", func(seq int, picks []int, key string, noMusic bool) map[string]any {
		return app.DownloadList(seq, picks, key, noMusic)
	})
	w.Bind("nz_update", func() map[string]any { return app.UpdateInfo() })
	w.Bind("nz_checkUpdate", func() map[string]any { return app.CheckUpdate() })
	w.Bind("nz_updateNow", func() map[string]any { return app.UpdateNow() })
	w.Bind("nz_updateLater", func() { app.UpdateLater() })
	w.Bind("nz_cancelUpdate", func() { app.CancelUpdate() })
	w.Bind("nz_removeMusic", func(id string) map[string]any { return app.RemoveMusicFrom(id) })
	w.Bind("nz_pickSepFiles", func() { w.Dispatch(h.pickSepFiles) })
	w.Bind("nz_pause", func(id string) { app.Pause(id) })
	w.Bind("nz_resume", func(id string) { app.Resume(id) })
	w.Bind("nz_pauseAll", func() { app.PauseAll() })
	w.Bind("nz_resumeAll", func() { app.ResumeAll() })
	w.Bind("nz_deleteTask", func(id string) { app.Delete(id) })
	w.Bind("nz_remove", func(ids []string, withFile bool) { app.Remove(ids, withFile) })
	w.Bind("nz_clearFinished", func() { app.ClearFinished() })
	w.Bind("nz_tasks", func() []map[string]any { return app.TaskList() })
	w.Bind("nz_addLink", func(text string) map[string]any { return app.AddLink(text) })
	w.Bind("nz_confirmFile", func(pid string, c FileChoice) map[string]any { return app.ConfirmFile(pid, c) })
	w.Bind("nz_cancelFile", func(pid string) { app.CancelFile(pid) })
	w.Bind("nz_pendingFiles", func() []map[string]any { return app.PendingFiles() })
	w.Bind("nz_useMedia", func(seq, index int) map[string]any { return app.UseMedia(seq, index) })
	w.Bind("nz_settings", func() SettingsView { return app.GetSettings() })
	w.Bind("nz_setSettings", func(ch map[string]any) SettingsView { return app.ChangeSettings(ch) })
	w.Bind("nz_dialogDone", func() { h.dialogDone() })
	w.Bind("nz_showMini", func() {
		if h.compact && h.compactOf != "task" {
			h.miniTasks(true)
		} else {
			h.showTasks()
		}
	})
	w.Bind("nz_miniFit", func(height float64) {
		if h.compact && h.compactOf == "task" && height > 0 && height != h.miniH {
			h.miniH = min(height, 600)
			h.placeMini(windowVisible(h.hwnd))
		}
	})
	w.Bind("nz_expand", func() { h.openWindow() })
	w.Bind("nz_hide", func() { h.hideToTray() })
	w.Bind("nz_exit", func() { h.exit() })
	w.Bind("nz_copyText", func(text string) bool { return setClipboardText(h.hwnd, text) })
	w.Bind("nz_openFile", func(path string) bool {
		if !app.FinishedPath(path) {
			return false
		}
		shellOpen(path)
		return true
	})
	w.Bind("nz_showInFolder", func(path string) bool {
		if !app.FinishedPath(path) {
			return false
		}
		showInFolder(path)
		return true
	})
	w.Bind("nz_openPage", func(link string) bool {
		u, err := url.Parse(link)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return false
		}
		openURL(u.String())
		return true
	})
	w.Bind("nz_openFolder", func(dir string) {
		if dir == "" || !isDir(dir) {
			dir = app.SaveDir()
		}
		_ = os.MkdirAll(dir, 0o755)
		shellOpen(dir)
	})
	w.Bind("nz_pickFolder", func(taskID string) {
		// Run the dialog from the window's own loop, not inside the browser's callback.
		w.Dispatch(func() {
			current := app.SaveDir()
			dir, ok := pickFolder(h.hwnd, tr("اختر المكان الذي تُحفظ فيه التنزيلات", "Choose where downloads are saved"),
				tr("الحفظ هنا", "Save here"), current)
			res := FolderResult{SaveDir: current, SaveDirLabel: folderLabel(current), TaskID: taskID}
			if ok {
				res = app.SetSaveDir(dir)
				res.TaskID = taskID
				res.Changed = res.Error == "" && !strings.EqualFold(filepath.Clean(dir), filepath.Clean(current))
				if res.Error == "" && taskID != "" {
					app.MoveTaskToSaveDir(taskID)
				}
			}
			// Emit also wakes the loop for anything queued while the dialog was open.
			h.Emit("folder", res)
		})
	})
	// A folder for one download (the "new download" window), without changing where
	// downloads go.
	w.Bind("nz_pickDir", func(purpose, start string) {
		w.Dispatch(func() {
			if start == "" || !isDir(start) {
				start = app.SaveDir()
			}
			dir, ok := pickFolder(h.hwnd, tr("اختر مكان حفظ هذا الملف", "Choose where to save this file"), tr("الحفظ هنا", "Save here"), start)
			res := map[string]any{"purpose": purpose, "ok": ok}
			if ok {
				res["folder"], res["label"] = dir, folderLabel(dir)
				if !dirWritable(dir) {
					res["ok"], res["error"] = false, NewUserError("permission")
				}
			}
			h.Emit("dirPicked", res)
		})
	})
	w.Bind("nz_contact", func(how string) bool {
		switch how {
		case "whatsapp":
			openURL(contactWhatsApp)
			return true
		case "copy":
			return setClipboardText(h.hwnd, contactPhone)
		}
		return false
	})
	w.Bind("nz_browsers", func() map[string]any {
		seen := int64(-1)
		if t := app.ExtSeen(); !t.IsZero() {
			seen = int64(time.Since(t).Seconds())
		}
		return map[string]any{"browsers": installedBrowsers(), "folder": chromiumExtFolder(paths.Data),
			"xpi": firefoxXPI(paths.Data), "id": chromiumExtensionIDs[0], "seenAgo": seen}
	})
	w.Bind("nz_openBrowser", func(id, page string) bool {
		switch {
		case id == "firefox" && page == "xpi":
			// Firefox offers to install the add-on it is given.
			page = firefoxXPI(paths.Data)
		case page != "" && !strings.HasSuffix(page, "://extensions") && page != "about:addons" && page != "about:debugging#/runtime/this-firefox":
			page = ""
		}
		return openBrowserPage(id, page)
	})
	w.Bind("nz_openExtFolder", func(kind string) {
		if kind == "firefox" {
			showInFolder(firefoxXPI(paths.Data))
			return
		}
		dir := chromiumExtFolder(paths.Data)
		_ = os.MkdirAll(dir, 0o755)
		shellOpen(dir)
	})
	w.Bind("nz_confirmClose", func() {
		h.allowClose.Store(true)
		app.CancelSetup()
		procPostMessageW.Call(h.hwnd, wmClose, 0, 0)
	})
	w.Bind("nz_log", func() string { return logTail(filepath.Join(paths.Data, "helink.log"), 60) })
}

// subclass watches a few window messages before the WebView library sees them.
func (h *winHost) subclass() {
	proc := windows.NewCallback(func(hwnd, msg, wp, lp uintptr) uintptr {
		if h.tray != nil && msg == h.tray.created && msg != 0 {
			h.tray.add() // Explorer started again
		}
		switch msg {
		case wmClose:
			if h.allowClose.Load() {
				break
			}
			if h.compact {
				h.closeSmall() // the page cancels what it shows, then hides
				return 0
			}
			if !h.app.store.Get().NoTray {
				h.hideToTray()
				return 0
			}
			if h.app.Busy() {
				what := "download"
				if h.app.SetupBusy() {
					what = "setup"
				}
				h.Emit("closeRequest", map[string]string{"what": what})
				return 0
			}
		case wmTray:
			switch lp & 0xFFFF {
			case ninSelect, ninKeySelect:
				h.openWindow()
			case ninBalloonClick:
				h.openWindow()
				if h.tray.lastAbout == "update" {
					h.Emit("showUpdate", nil) // "Click here to update HELINK"
				}
			case wmContextMenu:
				h.trayMenu()
			}
			return 0
		case wmQueryEndSession:
			return 1
		case wmEndSession:
			if wp != 0 {
				// Windows is shutting down: pause the downloads and save the list now.
				h.app.Shutdown()
			}
			return 0
		case wmGetMinMaxInfo:
			r, _, _ := procCallWindowProcW.Call(h.origProc, hwnd, msg, wp, lp)
			if h.compact && h.compactOf == "task" {
				mmi := (*[10]int32)(unsafe.Pointer(lp))
				mmi[6], mmi[7] = int32(160*h.scale), int32(90*h.scale) // ptMinTrackSize
			}
			return r
		case wmDpiChanged:
			// Moved to a screen with another zoom level: take the size Windows suggests.
			r := (*rect)(unsafe.Pointer(lp))
			procSetWindowPos.Call(hwnd, 0, uintptr(r.Left), uintptr(r.Top),
				uintptr(r.Right-r.Left), uintptr(r.Bottom-r.Top), swpNoZOrder|swpNoActivate)
			return 0
		case wmSettingChange:
			if lp != 0 && windows.UTF16PtrToString((*uint16)(unsafe.Pointer(lp))) == "ImmersiveColorSet" {
				applyTitleBar(hwnd)
				h.w.SetBackground(webViewColors(systemUsesDarkApps()))
			}
		case wmWindowPosChanged:
			// Shown or hidden, by any of the ways HELINK or Windows does it: WebView2 is told.
			r, _, _ := procCallWindowProcW.Call(h.origProc, hwnd, msg, wp, lp)
			if f := windowPosFlags(lp); f&swpShowWindowFlag != 0 {
				h.w.Dispatch(h.windowShown)
			} else if f&swpHideWindowFlag != 0 {
				h.w.Dispatch(h.windowHidden)
			}
			return r
		case wmSize:
			r, _, _ := procCallWindowProcW.Call(h.origProc, hwnd, msg, wp, lp)
			switch {
			case wp == sizeMinimized:
				h.minimized = true
				h.w.Dispatch(h.windowHidden)
			case h.minimized:
				h.minimized = false
				h.w.Dispatch(h.windowShown)
			}
			return r
		}
		r, _, _ := procCallWindowProcW.Call(h.origProc, hwnd, msg, wp, lp)
		return r
	})
	gwlpWndProc := int32(-4)
	h.origProc, _, _ = procSetWindowLongPtrW.Call(h.hwnd, uintptr(gwlpWndProc), proc)
}

// preloadWebView2Loader puts Microsoft's WebView2Loader.dll on disk and loads it
// normally, instead of the library's in-memory loading (which antivirus programs
// sometimes dislike).
func preloadWebView2Loader(dir string) {
	p := filepath.Join(dir, "WebView2Loader.dll")
	if b, err := os.ReadFile(p); err != nil || !bytes.Equal(b, webviewloader.WebView2Loader) {
		tmp := p + ".tmp"
		if err := os.WriteFile(tmp, webviewloader.WebView2Loader, 0o644); err == nil {
			_ = os.Rename(tmp, p)
		}
	}
	if _, err := windows.LoadLibrary(p); err != nil {
		log.Printf("WebView2Loader preload failed (%v); the built-in copy will be used", err)
	}
}

func setupLogging(dir string) {
	p := filepath.Join(dir, "helink.log")
	if fi, err := os.Stat(p); err == nil && fi.Size() > 2<<20 {
		_ = os.Rename(p, p+".old")
	}
	if f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		log.SetOutput(f)
	}
	log.Printf("start %s %v", appVersion, os.Args[1:])
}
