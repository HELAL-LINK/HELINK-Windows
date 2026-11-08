//go:build !windows

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nazzelha/ipc"
)

var (
	hostOnce sync.Once
	hostBin  string
	hostErr  error
)

// buildHost builds helink-host once for the tests.
func buildHost(t *testing.T) string {
	t.Helper()
	hostOnce.Do(func() {
		dir, err := os.MkdirTemp("", "helink-host-*")
		if err != nil {
			hostErr = err
			return
		}
		hostBin = filepath.Join(dir, "helink-host")
		out, err := exec.Command("go", "build", "-o", hostBin, "./host").CombinedOutput()
		if err != nil {
			hostErr = fmt.Errorf("%v: %s", err, out)
		}
	})
	if hostErr != nil {
		t.Fatal(hostErr)
	}
	return hostBin
}

// hostProc is helink-host as a browser runs it: messages on its input, answers on its output.
type hostProc struct {
	cmd *exec.Cmd
	in  io.WriteCloser
	out io.ReadCloser
}

func startHost(t *testing.T, env ...string) *hostProc {
	t.Helper()
	cmd := exec.Command(buildHost(t), "chrome-extension://abcdefghijklmnopabcdefghijklmnop/")
	cmd.Env = append(os.Environ(), env...)
	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	h := &hostProc{cmd: cmd, in: in, out: out}
	t.Cleanup(func() {
		in.Close()
		_ = cmd.Wait()
	})
	return h
}

func (h *hostProc) ask(t *testing.T, msg map[string]any) map[string]any {
	t.Helper()
	b, _ := json.Marshal(msg)
	if err := ipc.WriteMessage(h.in, b); err != nil {
		t.Fatal(err)
	}
	type res struct {
		b   []byte
		err error
	}
	ch := make(chan res, 1)
	go func() {
		b, err := ipc.ReadMessage(h.out)
		ch <- res{b, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatal(r.err)
		}
		var m map[string]any
		if err := json.Unmarshal(r.b, &m); err != nil {
			t.Fatalf("answer %q: %v", r.b, err)
		}
		return m
	case <-time.After(60 * time.Second):
		t.Fatal("no answer from the helper")
		return nil
	}
}

func TestBrowserHelperWithApp(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	app, em, save := fileApp(t, data)
	fs := newFileServer(3<<20 + 5)
	fs.check = func(r *http.Request) int {
		if !strings.Contains(r.Header.Get("Cookie"), "sid=s3cr3t") || r.Header.Get("Referer") != "https://shop.example/item" {
			return 403
		}
		return 0
	}
	srv := httptest.NewServer(fs)
	defer srv.Close()
	ln, err := app.ListenForBrowsers()
	if err != nil {
		t.Fatal(err)
	}
	h := startHost(t, "HELINK_DATA="+data)
	if r := h.ask(t, map[string]any{"type": "ping"}); r["ok"] != true || r["host"] != "2.2.1" {
		t.Fatalf("ping: %v", r)
	}
	r := h.ask(t, map[string]any{"type": "hello"})
	st, _ := r["settings"].(map[string]any)
	if r["ok"] != true || r["running"] != true || st["catch"] != true || !strings.Contains(fmt.Sprint(st["catchTypes"]), "zip") {
		t.Fatalf("hello: %v", r)
	}
	host := strings.Split(strings.TrimPrefix(srv.URL, "http://"), ":")[0]
	dl := func(path string, size int64, cookie string) map[string]any {
		return h.ask(t, map[string]any{"type": "download", "url": srv.URL + path, "filename": "C:\\Users\\me\\Downloads\\report.zip",
			"mime": "application/zip", "size": size, "referrer": "https://shop.example/item", "userAgent": "TestBrowser",
			"pageUrl": "https://shop.example/item", "cookies": []map[string]any{{"name": "sid", "value": cookie, "domain": host, "hostOnly": true, "path": "/"}}})
	}
	if r := dl("/dl/report", int64(len(fs.data)), "s3cr3t"); r["ok"] != true {
		t.Fatalf("download: %v", r)
	}
	var ev map[string]any
	for deadline := time.Now().Add(10 * time.Second); ; {
		name, m := em.next(t, deadline)
		if name == "file" {
			ev = m
			break
		}
	}
	if ev["status"] != "ready" || ev["name"] != "report.zip" || ev["from"] != "browser" || ev["page"] != "https://shop.example/item" {
		t.Fatalf("new download window: %v", ev)
	}
	id := app.ConfirmFile(ev["pid"].(string), FileChoice{Start: true})["id"].(string)
	em.waitTask(t, id, stateDone, 30*time.Second)
	checkFile(t, filepath.Join(save, "Compressed", "report.zip"), fs.data)
	// The browser keeps what HELINK cannot get.
	if r := dl("/dl/report", int64(len(fs.data)), "wrong"); r["ok"] != false || r["reason"] != "forbidden" {
		t.Fatalf("refused: %v", r)
	}
	if r := dl("/dl/report", 12345, "s3cr3t"); r["ok"] != false || r["reason"] != "size" {
		t.Fatalf("another size: %v", r)
	}
	// The extension's settings page.
	r = h.ask(t, map[string]any{"type": "setSettings", "settings": map[string]any{"catch": false, "catchTypes": []string{"ISO", ".zip"}, "skipSites": []string{"https://www.Bank.example/login"}, "maxParallel": 9}})
	st, _ = r["settings"].(map[string]any)
	if r["ok"] != true || st["catch"] != false || fmt.Sprint(st["catchTypes"]) != "[iso zip]" || fmt.Sprint(st["skipSites"]) != "[bank.example]" {
		t.Fatalf("settings: %v", r)
	}
	if app.store.Get().MaxParallel != 0 {
		t.Error("the extension changed a setting it should not")
	}
	if r := dl("/dl/report", int64(len(fs.data)), "s3cr3t"); r["ok"] != false || r["reason"] != "off" {
		t.Fatalf("catching off: %v", r)
	}
	// A link chosen in the page ("Download with HELINK").
	if r := h.ask(t, map[string]any{"type": "link", "url": srv.URL + "/dl/second.bin", "referrer": "https://shop.example/item",
		"cookies": []map[string]any{{"name": "sid", "value": "s3cr3t", "domain": host, "hostOnly": true}}}); r["ok"] != true {
		t.Fatalf("link: %v", r)
	}
	for deadline := time.Now().Add(10 * time.Second); ; {
		name, m := em.next(t, deadline)
		if name == "file" && m["status"] != "checking" {
			if m["status"] != "ready" || m["name"] != "second.bin" || m["from"] != "browser" {
				t.Fatalf("link window: %v", m)
			}
			break
		}
	}
	// The helper noted the extension's visit (shown in "Add HELINK to your browser").
	if since := time.Since(app.ExtSeen()); since > time.Minute || !pathExists(filepath.Join(data, "seen.json")) {
		t.Fatalf("extension seen %s ago", since)
	}
	// The installer's "close HELINK" (before it replaces HELINK.exe).
	quit := make(chan struct{}, 1)
	app.Quit = func() { quit <- struct{}{} }
	info, err := ipc.ReadInfo(data)
	if err != nil {
		t.Fatal(err)
	}
	if resp, err := ipc.Call(info, ipc.Request{Type: "quit"}, 5*time.Second); err != nil || !strings.Contains(string(resp), `"ok":true`) {
		t.Fatalf("quit: %s %v", resp, err)
	}
	select {
	case <-quit:
	case <-time.After(5 * time.Second):
		t.Fatal("quit was not passed on")
	}
	// HELINK closed: the helper answers "hello" from ext.json, without starting it.
	ln.Close()
	r = h.ask(t, map[string]any{"type": "hello"})
	st, _ = r["settings"].(map[string]any)
	if r["ok"] != true || r["running"] != false || st["catch"] != false {
		t.Fatalf("hello while closed: %v", r)
	}
	if r := h.ask(t, map[string]any{"type": "download", "url": srv.URL + "/x"}); r["ok"] != false || r["error"] != "app_not_found" {
		t.Fatalf("download while closed: %v", r)
	}
}

// TestHelperBrowserApp is HELINK itself for TestBrowserHelperStartsApp (run by helink-host).
func TestHelperBrowserApp(t *testing.T) {
	if os.Getenv("HELINK_HELPER_APP") != "1" {
		t.Skip("only as the helper's app")
	}
	data := os.Getenv("HELINK_DATA")
	var em Emitter = newRec()
	if p := os.Getenv("HELINK_EVENTS"); p != "" {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		em = &fileEmitter{f: f}
	}
	app := NewApp(em, newPaths(data))
	app.store.Update(func(s *Settings) { s.NoDialog = true })
	ln, err := app.ListenForBrowsers()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 600 && !pathExists(filepath.Join(data, "stop")); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	ln.Close()
	app.Shutdown()
}

func TestBrowserHelperStartsApp(t *testing.T) {
	tmp := t.TempDir()
	data, save := filepath.Join(tmp, "data"), filepath.Join(tmp, "Downloads")
	fs := newFileServer(1<<20 + 9)
	srv := httptest.NewServer(fs)
	defer srv.Close()
	defer os.WriteFile(filepath.Join(data, "stop"), nil, 0o644)
	h := startHost(t, "HELINK_DATA="+data, "NAZZELHA_SAVE="+save, "HELINK_HELPER_APP=1",
		"HELINK_APP_CMD="+os.Args[0]+" -test.run=^TestHelperBrowserApp$")
	start := time.Now()
	if r := h.ask(t, map[string]any{"type": "download", "url": srv.URL + "/files/tool.7z", "filename": "tool.7z"}); r["ok"] != true {
		t.Fatalf("download: %v", r)
	}
	t.Logf("answered in %s", time.Since(start))
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		if b, err := os.ReadFile(filepath.Join(save, "Compressed", "tool.7z")); err == nil && len(b) == len(fs.data) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the file did not arrive")
		}
	}
	checkFile(t, filepath.Join(save, "Compressed", "tool.7z"), fs.data)
}

// A video button clicked in a page: the page is read, and when that finds nothing, what
// the page loaded can be downloaded instead.
func TestBrowserVideo(t *testing.T) {
	if _, err := exec.LookPath("yt-dlp"); err != nil {
		t.Skip("yt-dlp not installed")
	}
	if _, err := os.Stat(e2eMedia); err != nil {
		t.Skip("local media not prepared")
	}
	app, em, save := fileApp(t, "")
	var refs sync.Map
	mux := http.NewServeMux()
	mux.Handle("/media/", http.StripPrefix("/media/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refs.Store(r.Header.Get("Referer"), true)
		http.FileServer(http.Dir(e2eMedia)).ServeHTTP(w, r)
	})))
	mux.HandleFunc("/app", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><html><head><title>Player</title></head><body><div id="player"></div><script src="/player.js"></script></body></html>`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	r := app.ExtMessage([]byte(`{"type":"video","pageUrl":"` + srv.URL + `/app","title":"Player",
		"media":[{"url":"` + srv.URL + `/media/c360.mp4","kind":"video","mime":"video/mp4","size":859951}]}`))
	if r["ok"] != true {
		t.Fatalf("video: %v", r)
	}
	var seq float64
	for deadline := time.Now().Add(5 * time.Second); ; {
		name, m := em.next(t, deadline)
		if name == "browserVideo" {
			seq = m["seq"].(float64)
			if m["oldExt"] != true { // sent the way extensions before 2.2 do (no version)
				t.Errorf("an old extension not noticed: %v", m)
			}
			break
		}
	}
	var info map[string]any
	for deadline := time.Now().Add(60 * time.Second); ; {
		name, m := em.next(t, deadline)
		if name == "info" && m["seq"].(float64) == seq && m["status"] == "error" {
			info = m
			break
		}
	}
	media, _ := info["media"].([]any)
	if e := info["error"].(map[string]any); e["code"] != "nothing_found" || len(media) != 1 || media[0].(map[string]any)["label"] != "MP4" {
		t.Fatalf("page read: %v", info)
	}
	u := app.UseMedia(int(seq), 0)
	if u["kind"] != "file" {
		t.Fatalf("use media: %v", u)
	}
	ev := waitFile(t, em, u["pid"].(string), 10*time.Second)
	if ev["name"] != "c360.mp4" || ev["category"] != catVideo {
		t.Fatalf("window: %v", ev)
	}
	id := app.ConfirmFile(ev["pid"].(string), FileChoice{Start: true})["id"].(string)
	em.waitTask(t, id, stateDone, 30*time.Second)
	if _, err := os.Stat(filepath.Join(save, "Video", "c360.mp4")); err != nil {
		t.Fatal(err)
	}
	if _, ok := refs.Load(srv.URL + "/app"); !ok {
		t.Error("the page was not sent as Referer")
	}
}

// fileEmitter writes the app's events to a file, one per line (for the extension's tests).
type fileEmitter struct {
	mu sync.Mutex
	f  *os.File
}

func (e *fileEmitter) Emit(event string, payload any) {
	b, _ := json.Marshal(map[string]any{"event": event, "data": payload})
	e.mu.Lock()
	e.f.Write(append(b, '\n'))
	e.mu.Unlock()
}

// The quality menu of the button on a video: the qualities read once for both askers (the
// mouse on the button, then the click), apart from the window's post; the one chosen
// downloaded and shown; an old menu refused.
func TestBrowserQualities(t *testing.T) {
	if _, err := exec.LookPath("yt-dlp"); err != nil {
		t.Skip("yt-dlp not installed")
	}
	if _, err := os.Stat(e2eMedia); err != nil {
		t.Skip("local media not prepared")
	}
	app, em, save := fileApp(t, "")
	var shown []string
	var mu sync.Mutex
	app.Show = func(reason string) { mu.Lock(); shown = append(shown, reason); mu.Unlock() }
	var pageReads atomic.Int32
	mux := http.NewServeMux()
	mux.Handle("/media/", http.StripPrefix("/media/", http.FileServer(http.Dir(e2eMedia))))
	mux.HandleFunc("/watch", func(w http.ResponseWriter, r *http.Request) {
		pageReads.Add(1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><html><head><title>A cooking lesson</title></head><body>
<video controls src="/media/c360.mp4" width="640" height="360"></video></body></html>`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	ask := []byte(`{"type":"qualities","pageUrl":"` + srv.URL + `/watch","postUrl":"` + srv.URL + `/watch","title":"x"}`)
	answers := make(chan map[string]any, 2)
	for i := 0; i < 2; i++ {
		go func() { answers <- app.ExtMessage(ask) }()
	}
	a, b := <-answers, <-answers
	if a["ok"] != true || b["ok"] != true || a["id"] != b["id"] {
		t.Fatalf("qualities: %v / %v", a, b)
	}
	if n := pageReads.Load(); n > 2 {
		t.Errorf("the page was read %d times for one reading", n)
	}
	items := a["items"].([]map[string]any)
	opts := items[0]["options"].([]map[string]any)
	var video, mp3 string
	for _, o := range opts {
		switch {
		case o["kind"] == "video" && video == "":
			video = o["key"].(string)
		case o["kind"] == "audio" && o["recommended"] == true:
			mp3 = o["key"].(string)
		}
	}
	if title, _ := items[0]["title"].(string); video == "" || mp3 == "" || !strings.HasPrefix(title, "A cooking lesson") {
		t.Fatalf("items: %v", items)
	}
	app.mu.Lock()
	cur := app.cur
	app.mu.Unlock()
	if cur != nil {
		t.Error("the window's post changed")
	}
	// Asked again later: the same reading, at once.
	start := time.Now()
	if c := app.ExtMessage(ask); c["id"] != a["id"] || time.Since(start) > time.Second {
		t.Errorf("again: %v in %s", c["id"], time.Since(start))
	}
	g := app.ExtMessage([]byte(`{"type":"get","id":"` + a["id"].(string) + `","item":0,"key":"` + mp3 + `"}`))
	if g["ok"] != true {
		t.Fatalf("get: %v", g)
	}
	done := em.waitTask(t, g["taskId"].(string), stateDone, 90*time.Second)
	p := done["result"].(map[string]any)["path"].(string)
	if filepath.Ext(p) != ".mp3" || !strings.HasPrefix(p, save) {
		t.Errorf("saved %s", p)
	}
	mu.Lock()
	if len(shown) != 1 || shown[0] != "task" {
		t.Errorf("shown %v", shown)
	}
	mu.Unlock()
	// The same again: the same download.
	if g2 := app.ExtMessage([]byte(`{"type":"get","id":"` + a["id"].(string) + `","item":0,"key":"` + mp3 + `"}`)); g2["taskId"] != g["taskId"] && g2["existing"] != true {
		t.Logf("again: %v", g2)
	}
	if e := app.ExtMessage([]byte(`{"type":"get","id":"b999","item":0,"key":"v1"}`)); e["ok"] != false || e["error"] != "expired" {
		t.Errorf("unknown menu: %v", e)
	}
	if e := app.ExtMessage([]byte(`{"type":"qualities","postUrl":"` + srv.URL + `/nothing-here"}`)); e["ok"] != false || e["message"] == "" {
		t.Errorf("nothing there: %v", e)
	}
}
