//go:build windows

package main

import (
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
)

// fakeView stands for WebView2 (which does not run here): it records what HELINK asks of
// it, and runs what is given to Dispatch one after the other on a thread that also handles
// Windows' messages, like the window's thread.
type fakeView struct {
	mu       sync.Mutex
	q        chan func()
	ready    bool
	failNext int // makings that fail
	builds   int
	releases int
	reloads  int
	visible  []bool
	evals    []string
	onEval   func(js string)
}

func newFakeView() *fakeView {
	f := &fakeView{q: make(chan func(), 1000)}
	peek, dispatch := user32.NewProc("PeekMessageW"), user32.NewProc("DispatchMessageW")
	go func() {
		runtime.LockOSThread()
		var msg [48]byte
		for {
			select {
			case fn := <-f.q:
				fn()
			case <-time.After(5 * time.Millisecond):
			}
			for {
				if r, _, _ := peek.Call(uintptr(unsafe.Pointer(&msg[0])), 0, 0, 0, 1); r == 0 { // PM_REMOVE
					break
				}
				dispatch.Call(uintptr(unsafe.Pointer(&msg[0])))
			}
		}
	}()
	return f
}

func (f *fakeView) Run()                            {}
func (f *fakeView) Terminate()                      {}
func (f *fakeView) Dispatch(fn func())              { f.q <- fn }
func (f *fakeView) Destroy()                        {}
func (f *fakeView) Window() unsafe.Pointer          { return nil }
func (f *fakeView) SetTitle(string)                 {}
func (f *fakeView) SetSize(int, int, webview2.Hint) {}
func (f *fakeView) Navigate(string)                 {}
func (f *fakeView) SetHtml(string)                  {}
func (f *fakeView) Init(string)                     {}
func (f *fakeView) Bind(string, interface{}) error  { return nil }
func (f *fakeView) OnProcessFailed(func(int))       {}
func (f *fakeView) SetBackground(uint32)            {}
func (f *fakeView) Failed() uintptr                 { return 0x80004005 }

func (f *fakeView) Eval(js string) {
	f.mu.Lock()
	f.evals = append(f.evals, js)
	on := f.onEval
	f.mu.Unlock()
	if on != nil {
		on(js)
	}
}

func (f *fakeView) Rebuild(time.Duration) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.builds++
	if f.failNext > 0 {
		f.failNext--
		f.ready = false
		return false
	}
	f.ready = true
	return true
}

func (f *fakeView) Release() {
	f.mu.Lock()
	f.releases++
	f.ready = false
	f.mu.Unlock()
}

func (f *fakeView) Reload() {
	f.mu.Lock()
	f.reloads++
	f.mu.Unlock()
}

func (f *fakeView) Ready() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ready
}

func (f *fakeView) SetVisible(v bool) {
	f.mu.Lock()
	f.visible = append(f.visible, v)
	f.mu.Unlock()
}

func (f *fakeView) counts() (builds, releases, reloads int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.builds, f.releases, f.reloads
}

func (f *fakeView) lastVisible() (bool, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.visible) == 0 {
		return false, false
	}
	return f.visible[len(f.visible)-1], true
}

func (f *fakeView) evalsWith(s string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, e := range f.evals {
		if strings.Contains(e, s) {
			n++
		}
	}
	return n
}

// The window's page made when the window first shows, checked each time it shows again,
// loaded again or made anew when it broke, and what waits for it done once it is ready.
func TestWinPageLifecycle(t *testing.T) {
	waits := []*time.Duration{&uiLoadWait, &uiPingWait, &uiCheckEvery, &uiShowWait, &uiRetryWait, &uiIdleClose}
	saved := make([]time.Duration, len(waits))
	for i, p := range waits {
		saved[i] = *p
	}
	defer func() {
		for i, p := range waits {
			*p = saved[i]
		}
	}()
	uiLoadWait, uiPingWait, uiCheckEvery, uiShowWait, uiRetryWait = 600*time.Millisecond, 300*time.Millisecond, 0, 500*time.Millisecond, 200*time.Millisecond
	uiIdleClose = time.Hour // below
	savedVersion, savedMessage := webView2Version, uiFailedMessage
	defer func() { webView2Version, uiFailedMessage = savedVersion, savedMessage }()
	webView2Version = func() string { return "130.0.2849.80" }
	var told []bool
	uiFailedMessage = func(_ *winHost, missing bool) { told = append(told, missing) }

	create, destroy := user32.NewProc("CreateWindowExW"), user32.NewProc("DestroyWindow")
	const wsOverlappedWindow, wsChild, wsVisible = 0x00CF0000, 0x40000000, 0x10000000
	f := newFakeView()
	onUI := func(fn func()) {
		done := make(chan struct{})
		f.Dispatch(func() { fn(); close(done) })
		<-done
	}
	// The window belongs to the thread that runs what is dispatched, as HELINK's does.
	var hwnd uintptr
	var err error
	onUI(func() {
		hwnd, _, err = create.Call(0, uintptr(unsafe.Pointer(utf16("STATIC"))), uintptr(unsafe.Pointer(utf16("HELINK test"))),
			wsOverlappedWindow, 100, 100, 900, 600, 0, 0, 0, 0)
	})
	if hwnd == 0 {
		t.Skipf("no window here: %v", err)
	}
	defer onUI(func() { destroy.Call(hwnd) })
	h := &winHost{w: f, hwnd: hwnd, states: map[string]string{}, scale: 1}
	show := func(cmd uintptr) { onUI(func() { procShowWindow.Call(hwnd, cmd) }) }
	settle := func(d time.Duration) { time.Sleep(d); onUI(func() {}) }
	state := func() (s uiState) { onUI(func() { s = h.ui }); return }

	// Started hidden near the clock: no page, and the downloads' progress is not kept for one.
	h.Emit("task", map[string]any{"id": "a"})
	h.Emit("info", map[string]any{"seq": 1})
	settle(50 * time.Millisecond)
	if b, _, _ := f.counts(); b != 0 || state() != uiNone {
		t.Fatalf("made while hidden: %d builds, state %v", b, state())
	}
	onUI(func() {
		if len(h.early) != 1 || h.early[0].name != "info" {
			t.Errorf("kept for the page: %v", h.early)
		}
	})

	// The window shows: the page is made, told it shows, and gets what it missed once ready.
	show(swShow)
	onUI(h.windowShown)
	settle(50 * time.Millisecond)
	if b, _, _ := f.counts(); b != 1 || state() != uiLoading {
		t.Fatalf("on showing: %d builds, state %v", b, state())
	}
	if v, ok := f.lastVisible(); !ok || !v {
		t.Error("WebView2 not told the window shows")
	}
	onUI(h.pageStarted)
	if state() != uiReady || f.evalsWith(`"info"`) != 1 || f.evalsWith(`"task"`) != 0 {
		t.Fatalf("ready: state %v, evals %v", state(), f.evals)
	}

	// WebView2's window is not there (its program ended): made anew at the next showing.
	onUI(h.windowShown)
	settle(50 * time.Millisecond)
	if b, r, _ := f.counts(); b != 2 || r != 1 {
		t.Errorf("WebView2's window gone: %d builds, %d releases", b, r)
	}
	onUI(h.pageStarted)

	// With its window there, the page is asked; one that does not answer is loaded again,
	// and made anew if it does not load.
	// Like WebView2's, the window inside is made by a thread that handles its messages.
	childc := make(chan uintptr)
	go func() {
		runtime.LockOSThread()
		c, _, _ := create.Call(0, uintptr(unsafe.Pointer(utf16("STATIC"))), 0, wsChild|wsVisible, 0, 0, 10, 10, hwnd, 0, 0, 0)
		childc <- c
		getMessage := user32.NewProc("GetMessageW")
		dispatch := user32.NewProc("DispatchMessageW")
		var msg [48]byte
		for {
			if r, _, _ := getMessage.Call(uintptr(unsafe.Pointer(&msg[0])), 0, 0, 0); r == 0 || int32(r) == -1 {
				return
			}
			dispatch.Call(uintptr(unsafe.Pointer(&msg[0])))
		}
	}()
	child := <-childc
	if child == 0 {
		t.Fatal("no child window")
	}
	onUI(h.windowShown)
	if f.evalsWith("nzPing") != 1 {
		t.Fatalf("not asked: %v", f.evals)
	}
	settle(uiPingWait + 150*time.Millisecond)
	if _, _, rl := f.counts(); rl != 1 || state() != uiLoading {
		t.Fatalf("no answer: %d reloads, state %v", rl, state())
	}
	settle(uiLoadWait + 150*time.Millisecond)
	if b, r, _ := f.counts(); b != 3 || r != 2 {
		t.Fatalf("not loaded again: %d builds, %d releases", b, r)
	}
	onUI(h.pageStarted)

	// A page that answers stays.
	ping := regexp.MustCompile(`nzPing\((\d+)\)`)
	f.mu.Lock()
	f.onEval = func(js string) {
		if m := ping.FindStringSubmatch(js); m != nil {
			n, _ := strconv.Atoi(m[1])
			h.pong(n) // as nz_pong does, on the window's thread
		}
	}
	f.mu.Unlock()
	onUI(h.windowShown)
	settle(uiPingWait + 150*time.Millisecond)
	if b, _, rl := f.counts(); b != 3 || rl != 1 || state() != uiReady {
		t.Errorf("answered, yet: %d builds, %d reloads, state %v", b, rl, state())
	}

	// WebView2's own events: the page's process ended → loaded again; WebView2 ended →
	// made anew (now, as the window shows).
	onUI(func() { h.processFailed(1) })
	if _, _, rl := f.counts(); rl != 2 {
		t.Errorf("page process ended: %d reloads", rl)
	}
	onUI(h.pageStarted)
	onUI(func() { h.processFailed(0) })
	settle(50 * time.Millisecond)
	if b, r, _ := f.counts(); b != 4 || r != 3 {
		t.Errorf("WebView2 ended: %d builds, %d releases", b, r)
	}
	onUI(h.pageStarted)

	// Hidden: WebView2 is told; ended while hidden → closed, made again only when it shows.
	show(swHide)
	onUI(h.windowHidden)
	if v, _ := f.lastVisible(); v {
		t.Error("WebView2 not told the window is hidden")
	}
	onUI(func() { h.processFailed(0) })
	settle(50 * time.Millisecond)
	if b, r, _ := f.counts(); b != 4 || r != 4 || state() != uiNone {
		t.Errorf("ended while hidden: %d builds, %d releases, state %v", b, r, state())
	}

	// A small window waits for its page; it shows after a while even without one.
	shown := make(chan struct{}, 2)
	onUI(func() { h.whenReady(func() { shown <- struct{}{} }) })
	settle(50 * time.Millisecond)
	if b, _, _ := f.counts(); b != 5 {
		t.Errorf("not made for the small window: %d builds", b)
	}
	select {
	case <-shown:
		t.Fatal("shown before its page was ready")
	default:
	}
	onUI(h.pageStarted)
	select {
	case <-shown:
	case <-time.After(time.Second):
		t.Fatal("not shown once ready")
	}
	settle(uiShowWait + 100*time.Millisecond)
	if len(shown) != 0 {
		t.Error("shown twice")
	}

	// The small window's close button: hidden even when its page does not answer.
	onUI(h.pageStarted)
	show(swShow)
	f.mu.Lock()
	f.onEval = nil
	f.mu.Unlock()
	onUI(func() { h.compact, h.compactOf = true, "file"; h.closeSmall() })
	settle(50 * time.Millisecond)
	if f.evalsWith(`"dialogClose"`) == 0 {
		t.Error("the page was not asked to close")
	}
	settle(2*time.Second + 200*time.Millisecond)
	_, _, rl := f.counts()
	onUI(func() {
		if h.compact || windowVisible(hwnd) || rl != 3 {
			t.Errorf("not closed without an answer: compact %v, shown %v, reloads %d", h.compact, windowVisible(hwnd), rl)
		}
	})
	onUI(func() { h.compact, h.compactOf = true, "file"; h.ui = uiLoading; h.closeSmall() })
	onUI(func() {
		if h.compact {
			t.Error("not closed while its page loads")
		}
	})

	// WebView2 failing to start: tried three times, then said once.
	onUI(func() { h.remakeUI("test") })
	f.mu.Lock()
	f.failNext = 3
	f.mu.Unlock()
	onUI(func() { h.whenReady(func() {}) })
	settle(4*uiRetryWait + 300*time.Millisecond)
	if b, _, _ := f.counts(); b != 8 || len(told) != 1 || told[0] {
		t.Errorf("failing: %d builds, told %v", b, told)
	}
	// Asked again later: it works.
	onUI(func() { h.whenReady(func() {}) })
	settle(100 * time.Millisecond)
	if b, _, _ := f.counts(); b != 9 || state() != uiLoading {
		t.Errorf("asked again: %d builds, state %v", b, state())
	}
	// WebView2 not installed: said at once.
	webView2Version = func() string { return "" }
	f.mu.Lock()
	f.failNext = 1
	f.mu.Unlock()
	onUI(func() { h.remakeUI("test") }) // the small window above still waits: made again
	settle(100 * time.Millisecond)
	if len(told) != 2 || !told[1] {
		t.Errorf("not installed: told %v", told)
	}

	// Hidden a long time: the page is closed (WebView2's programs end), and made afresh
	// when the window shows again; shown again sooner, it stays.
	webView2Version = func() string { return "130.0.2849.80" }
	uiIdleClose = 400 * time.Millisecond
	f.mu.Lock()
	f.onEval = func(js string) { // the page answers
		if m := ping.FindStringSubmatch(js); m != nil {
			n, _ := strconv.Atoi(m[1])
			h.pong(n)
		}
	}
	f.mu.Unlock()
	show(swShow)
	onUI(h.windowShown)
	settle(50 * time.Millisecond)
	onUI(h.pageStarted)
	b0, r0, _ := f.counts()
	show(swHide)
	onUI(h.windowHidden)
	settle(200 * time.Millisecond)
	show(swShow)
	onUI(h.windowShown)
	settle(400 * time.Millisecond)
	if b, r, _ := f.counts(); b != b0 || r != r0 || state() != uiReady {
		t.Errorf("shown again soon: %d builds, %d releases, state %v", b-b0, r-r0, state())
	}
	show(swHide)
	onUI(h.windowHidden)
	settle(600 * time.Millisecond)
	if b, r, _ := f.counts(); b != b0 || r != r0+1 || state() != uiNone {
		t.Errorf("hidden long: %d builds, %d releases, state %v", b-b0, r-r0, state())
	}
	show(swShow)
	onUI(h.windowShown)
	settle(50 * time.Millisecond)
	if b, _, _ := f.counts(); b != b0+1 || state() != uiLoading {
		t.Errorf("shown after that: %d builds, state %v", b-b0, state())
	}
}
