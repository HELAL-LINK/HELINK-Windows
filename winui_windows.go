//go:build windows

package main

// The window's page lives in WebView2 (a copy of Edge made for programs), whose own
// programs run beside HELINK. They can end while HELINK waits hidden near the clock (the
// computer slept, a cleaning tool closed them, they crashed), or never draw when made while
// Windows was still starting; the window then showed nothing but white until HELINK was
// ended from the Task Manager. So:
//   - the page is made when the window first shows, not while HELINK starts hidden;
//   - WebView2 is told when the window shows and hides (a hidden one draws nothing);
//   - each time the window shows, the page must answer: one that does not is loaded again,
//     and made anew if that does not help; so is one whose WebView2 ended (ProcessFailed);
//   - hidden for a long time, the page is closed (WebView2's programs end, freeing their
//     memory), and made afresh when the window next shows;
//   - the window is painted like the page meanwhile, never white.
// Only the page is made again: the downloads go on.

import (
	"fmt"
	"log"
	"os"
	"runtime"
	"time"
	"unsafe"

	"github.com/jchv/go-webview2/webviewloader"
)

var (
	uiMakeWait   = 45 * time.Second // the longest wait for WebView2 to start (a slow computer just started)
	uiLoadWait   = 20 * time.Second // for a page to load and answer, before it is made anew
	uiPingWait   = 5 * time.Second  // for the page to answer when the window shows
	uiCheckEvery = 15 * time.Second // the page is not asked again sooner than this
	uiShowWait   = 6 * time.Second  // a small window shows after this even if its page is not ready
	uiRetryWait  = 3 * time.Second  // before making it again after a failure
	uiIdleClose  = 20 * time.Minute // hidden this long, the page is closed until the window shows again

	// webView2Version is the version of WebView2 on this computer ("" when there is none).
	webView2Version = func() string { v, _ := webviewloader.GetInstalledVersion(); return v }
	// uiFailedMessage tells that the window cannot open (missing: WebView2 is not there).
	uiFailedMessage = func(h *winHost, missing bool) {
		if missing {
			webView2Missing()
			return
		}
		msg := tr("تعذّر فتح نافذة HELINK لأن «Microsoft Edge WebView2» لا يعمل على هذا الكمبيوتر الآن. تستمر التنزيلات.\n\n"+
			"أعد تشغيل الكمبيوتر ثم افتح HELINK. إذا تكررت المشكلة، اضغط «موافق» لفتح صفحة تنزيل WebView2 من مايكروسوفت وثبّته مرة أخرى.",
			"HELINK's window couldn't open because Microsoft Edge WebView2 isn't working on this computer right now. Downloads go on.\n\n"+
				"Restart the computer, then open HELINK. If it happens again, click OK to open Microsoft's WebView2 download page and install it again.")
		if messageBox(h.hwnd, windowTitle, msg, 0x31) == 1 { // MB_OKCANCEL | MB_ICONWARNING → IDOK
			openURL("https://go.microsoft.com/fwlink/p/?LinkId=2124703")
		}
	}
)

type uiState int

const (
	uiNone    uiState = iota // no page: not made yet (HELINK started hidden), or closed because it broke
	uiLoading                // made, its page loading
	uiReady                  // the page answered (nz_ready)
)

var (
	procGetWindow       = user32.NewProc("GetWindow")
	procIsHungAppWindow = user32.NewProc("IsHungAppWindow")
)

// after runs f on the window's thread after d.
func (h *winHost) after(d time.Duration, f func()) {
	time.AfterFunc(d, func() { h.w.Dispatch(f) })
}

// shows reports whether the window is on the screen (shown and not minimized).
func (h *winHost) shows() bool {
	iconic, _, _ := procIsIconic.Call(h.hwnd)
	return windowVisible(h.hwnd) && iconic == 0
}

// wantUI reports whether a page is needed now: the window shows, or waits for one to show.
func (h *winHost) wantUI() bool { return h.shows() || len(h.onReady) > 0 }

// makeUI makes the page when there is none.
func (h *winHost) makeUI() {
	if h.ui != uiNone || h.uiMaking {
		return
	}
	h.uiMaking = true
	// From the main loop: making it waits for WebView2 while handling the window's
	// messages, which is not done inside the handling of another message.
	h.w.Dispatch(func() {
		h.uiMaking = false
		if h.ui != uiNone {
			return
		}
		start := time.Now()
		h.uiGen++
		gen := h.uiGen
		if !h.w.Rebuild(uiMakeWait) {
			h.uiFails++
			log.Printf("window: WebView2 did not start (%08x), %d time(s) in a row", uint32(h.w.Failed()), h.uiFails)
			h.makeFailed()
			return
		}
		h.ui = uiLoading
		h.uiMade++
		h.w.SetVisible(h.shows())
		log.Printf("window: page made in %s (%d since HELINK started)", time.Since(start).Round(time.Millisecond), h.uiMade)
		h.after(uiLoadWait, func() {
			if h.uiGen == gen && h.ui == uiLoading {
				h.remakeUI("its page did not load")
			}
		})
	})
}

// makeFailed tries again a little later; after three failures it tells why (the downloads
// go on, and the next time the window is asked for, it is tried again).
func (h *winHost) makeFailed() {
	v := webView2Version()
	if v != "" && h.uiFails < 3 {
		h.after(uiRetryWait, func() {
			if h.wantUI() {
				h.makeUI()
			}
		})
		return
	}
	h.uiFails = 0
	h.onReady = nil
	uiFailedMessage(h, v == "")
}

// remakeUI closes the page and its WebView2, and makes another if the window needs one
// now (else when it next shows).
func (h *winHost) remakeUI(why string) {
	log.Printf("window: making the page anew (%s)", why)
	h.ui = uiNone
	h.uiGen++
	h.w.Release()
	if h.wantUI() {
		h.makeUI()
	}
}

// reloadUI loads the page again in the same WebView2; made anew if it does not load.
func (h *winHost) reloadUI(why string) {
	log.Printf("window: loading the page again (%s)", why)
	h.ui = uiLoading
	h.uiGen++
	gen := h.uiGen
	h.w.Reload()
	h.after(uiLoadWait, func() {
		if h.uiGen == gen && h.ui == uiLoading {
			h.remakeUI("its page did not load again")
		}
	})
}

// pageStarted: the page is ready (it called nz_ready). It gets the events it missed,
// and what waited for it is done.
func (h *winHost) pageStarted() {
	if h.ui == uiReady {
		return
	}
	h.ui = uiReady
	h.uiFails = 0
	h.lastCheck = time.Now()
	for _, e := range h.early {
		h.w.Eval(e.js)
	}
	h.early = nil
	if h.compact && h.compactOf != "" {
		// A page made again while the window was a small one: back to that.
		if h.compactOf == "task" {
			for id := range h.miniIDs {
				h.Emit("browserTask", map[string]any{"id": id})
			}
		}
		h.Emit("mode", map[string]any{"compact": true, "reason": h.compactOf})
	}
	wait := h.onReady
	h.onReady = nil
	for _, f := range wait {
		f()
	}
}

// whenReady does f once the page is ready (making it if need be), and after uiShowWait
// anyway, so that what waits for it does show.
func (h *winHost) whenReady(f func()) {
	if h.ui == uiReady {
		f()
		return
	}
	done := false
	once := func() {
		if !done {
			done = true
			f()
		}
	}
	h.onReady = append(h.onReady, once)
	h.makeUI()
	h.after(uiShowWait, once)
}

// windowShown: the window came on the screen.
func (h *winHost) windowShown() {
	if !h.shows() {
		return
	}
	h.hideGen++
	h.w.SetVisible(true)
	h.checkUI()
}

// windowHidden: the window left the screen (hidden near the clock, or minimized).
func (h *winHost) windowHidden() {
	if h.shows() {
		return // shown again meanwhile
	}
	h.w.SetVisible(false)
	h.hideGen++
	gen := h.hideGen
	h.after(uiIdleClose, func() {
		if h.hideGen == gen && !h.shows() && !h.compact && len(h.onReady) == 0 && h.ui != uiNone && !h.uiMaking {
			log.Printf("window: hidden for %s, its page is closed until it shows again", uiIdleClose)
			h.ui = uiNone
			h.uiGen++
			h.w.Release()
		}
	})
}

// checkUI makes sure the page shown still works: made if there is none, made anew if
// WebView2's window is gone or hangs, loaded again if the page does not answer.
func (h *winHost) checkUI() {
	switch h.ui {
	case uiNone:
		h.makeUI()
		return
	case uiLoading:
		return // its timer looks after it
	}
	if !h.w.Ready() || !h.webViewAlive() {
		h.remakeUI("WebView2's window is gone or does not answer")
		return
	}
	if time.Since(h.lastCheck) < uiCheckEvery {
		return
	}
	h.lastCheck = time.Now()
	h.pingPage(uiPingWait, func() {
		if h.ui == uiReady && h.shows() {
			h.reloadUI("the page did not answer")
		}
	})
}

// pingPage asks the page whether it works; noAnswer runs if it does not answer within wait
// (and is still the same page).
func (h *winHost) pingPage(wait time.Duration, noAnswer func()) {
	h.pingSeq++
	seq, gen := h.pingSeq, h.uiGen
	h.w.Eval(fmt.Sprintf("window.nzPing&&window.nzPing(%d)", seq))
	h.after(wait, func() {
		if h.uiGen == gen && h.pongSeq < seq {
			noAnswer()
		}
	})
}

// closeSmall: the small window's close button. The page cancels what it shows, then hides
// it; without a page that answers, it is hidden all the same.
func (h *winHost) closeSmall() {
	if h.ui != uiReady {
		h.dialogDone()
		return
	}
	h.Emit("dialogClose", nil)
	h.pingPage(2*time.Second, func() {
		if h.compact {
			h.dialogDone()
		}
		if h.ui == uiReady {
			h.reloadUI("the page did not answer the close button")
		}
	})
}

// pong: the page answered nzPing.
func (h *winHost) pong(seq int) {
	if seq > h.pongSeq {
		h.pongSeq = seq
	}
}

// webViewAlive reports whether WebView2's window is in HELINK's (its program made it there,
// and it goes when that program ends) and answers.
func (h *winHost) webViewAlive() bool {
	child, _, _ := procGetWindow.Call(h.hwnd, 5) // GW_CHILD
	if child == 0 {
		return false
	}
	hung, _, _ := procIsHungAppWindow.Call(child)
	return hung == 0
}

// processFailed: one of WebView2's programs ended or stopped answering.
func (h *winHost) processFailed(kind int) {
	log.Printf("window: a WebView2 process failed (kind %d)", kind)
	switch kind {
	case 0: // the browser itself: the WebView is gone
		h.remakeUI("WebView2 ended")
	case 1, 2: // the page's own process ended, or stopped answering
		if h.ui != uiNone {
			h.reloadUI("the page's process ended or stopped answering")
		}
	case 6: // the drawing process: WebView2 starts another; draw again
		if h.shows() {
			h.w.SetVisible(false)
			h.w.SetVisible(true)
		}
	}
}

// webViewColors gives WebView2 and the window the page's background (light or dark), so
// that nothing shows white while the page loads. It returns the window's (0x00BBGGRR).
func webViewColors(dark bool) uint32 {
	if dark {
		os.Setenv("WEBVIEW2_DEFAULT_BACKGROUND_COLOR", "0xFF111111")
		return 0x00111111
	}
	os.Setenv("WEBVIEW2_DEFAULT_BACKGROUND_COLOR", "0xFFF3F3F3")
	return 0x00F3F3F3
}

// webView2Missing tells that WebView2 is not on this computer, and offers its download page.
func webView2Missing() {
	msg := tr("يحتاج HELINK إلى «Microsoft Edge WebView2» ليعمل، وهو غير موجود على هذا الكمبيوتر.\n\n"+
		"اضغط «موافق» لفتح صفحة تنزيله من مايكروسوفت، ثم افتح HELINK مرة أخرى بعد تثبيته.",
		"HELINK needs Microsoft Edge WebView2 to run, and it isn't installed on this computer.\n\n"+
			"Click OK to open its download page from Microsoft, then open HELINK again after installing it.")
	if messageBox(0, windowTitle, msg, 0x31) == 1 { // MB_OKCANCEL | MB_ICONWARNING → IDOK
		openURL("https://go.microsoft.com/fwlink/p/?LinkId=2124703")
	}
}

// watchWindowThread writes in the log when the window's thread stops handling messages
// for a long time (a window that does not draw), with what every part of HELINK was doing.
// A window of Windows open meanwhile (choosing a folder, a message) is no such stop.
func (h *winHost) watchWindowThread() {
	for times := 1; ; {
		time.Sleep(5 * time.Second)
		sent := time.Now()
		done := make(chan struct{})
		h.w.Dispatch(func() { close(done) })
		select {
		case <-done:
			continue
		case <-time.After(20 * time.Second):
		}
		if hung, _, _ := procIsHungAppWindow.Call(h.hwnd); hung == 0 {
			<-done // its messages are handled (a window of Windows waits for an answer)
			continue
		}
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		log.Printf("window: its thread has not answered for %s (%d):\n%s", time.Since(sent).Round(time.Second), times, buf[:n])
		times++
		<-done
		log.Printf("window: its thread answers again after %s", time.Since(sent).Round(time.Second))
	}
}

// windowPos is Windows' WINDOWPOS (of WM_WINDOWPOSCHANGED).
type windowPos struct {
	hwnd, insertAfter uintptr
	x, y, cx, cy      int32
	flags             uint32
}

// windowPosFlags reads the flags of a WM_WINDOWPOSCHANGED.
func windowPosFlags(lp uintptr) uint32 {
	if lp == 0 {
		return 0
	}
	return (*windowPos)(unsafe.Pointer(lp)).flags
}
