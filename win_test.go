//go:build windows

package main

// These tests run the Windows-specific code (job objects, paths, clipboard, the whole
// setup → fetch → download flow with fake tools). They are meant to run under Wine in CI-less
// development: FAKETOOL must point to devtools/faketool built for Windows.

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func fakeTool(t *testing.T) []byte {
	t.Helper()
	p := os.Getenv("FAKETOOL")
	if p == "" {
		t.Skip("FAKETOOL not set")
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func zipOf(t *testing.T, files map[string][]byte) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for n, c := range files {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(c)
	}
	zw.Close()
	return buf.Bytes()
}

func TestWinPlatform(t *testing.T) {
	d := defaultSaveDir()
	if d == "" || filepath.Base(d) != "HELINK" {
		t.Fatalf("save folder %q", d)
	}
	t.Logf("save=%s label=%s", d, folderLabel(d))
	if folderLabel(d) != "التنزيلات ← HELINK" || folderLabel(downloadsFolder()) != "التنزيلات (Downloads)" {
		t.Errorf("labels %q %q", folderLabel(d), folderLabel(downloadsFolder()))
	}
	if l := folderLabel(filepath.Join(downloadsFolder(), "Clips")); l != "التنزيلات ← Clips" {
		t.Errorf("subfolder label %q", l)
	}
	// The folders of the kinds of files, in the save folder or elsewhere.
	if l := folderLabel(filepath.Join(d, "Video")); l != "التنزيلات ← HELINK \u200f← Video" {
		t.Errorf("kind folder label %q", l)
	}
	if l := folderLabel(`D:\Video`); l != "القرص D \u200f← Video" {
		t.Errorf("kind folder on a drive %q", l)
	}
	if l := folderLabel(`D:\Movies\Music\`); l != "Movies (القرص D) \u200f← Music" {
		t.Errorf("kind folder in a folder %q", l)
	}
	setLang("en")
	if folderLabel(d) != "Downloads › HELINK" || folderLabel(`D:\`) != "Drive D" || folderLabel(`D:\Videos`) != "Videos (drive D)" {
		t.Errorf("English labels %q %q %q", folderLabel(d), folderLabel(`D:\`), folderLabel(`D:\Videos`))
	}
	if l := folderLabel(filepath.Join(d, "Compressed")); l != "Downloads › HELINK › Compressed" {
		t.Errorf("English kind folder label %q", l)
	}
	setLang("ar")
	tmp := t.TempDir()
	if free, ok := freeSpace(tmp); !ok || free <= 0 {
		t.Errorf("free space %d %v", free, ok)
	}
	if !dirWritable(tmp) {
		t.Error("temp not writable")
	}
	h := filepath.Join(tmp, ".hidden-test")
	os.Mkdir(h, 0o755)
	hideFolder(h)
	a, _ := windows.GetFileAttributes(utf16(h))
	if a&windows.FILE_ATTRIBUTE_HIDDEN == 0 {
		t.Error("folder not hidden")
	}
	if l := folderLabel(`D:\`); l != "القرص D" {
		t.Errorf("drive label %q", l)
	}
	t.Logf("dpi=%v", systemDPI())
	t.Logf("dark=%v", systemUsesDarkApps())
}

func TestWinClipboard(t *testing.T) {
	want := "https://youtu.be/abcdefghijk شوف ده"
	if !setClipboardText(0, want) {
		t.Skip("clipboard not available here")
	}
	if got := clipboardText(0); got != want {
		t.Errorf("clipboard %q", got)
	}
}

func processGone(pid int, wait time.Duration) bool {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return true // no such process
	}
	defer windows.CloseHandle(h)
	ev, _ := windows.WaitForSingleObject(h, uint32(wait/time.Millisecond))
	return ev == windows.WAIT_OBJECT_0
}

func TestWinKillTree(t *testing.T) {
	ft := fakeTool(t)
	dir := t.TempDir()
	exe := filepath.Join(dir, "yt-dlp.exe")
	os.WriteFile(exe, ft, 0o755)
	t.Setenv("FAKE_SPAWN", "1")
	t.Setenv("FAKE_SLOW_MS", "400")
	ctx, cancel := context.WithCancel(context.Background())
	child := make(chan int, 1)
	var lines []string
	done := make(chan error, 1)
	go func() {
		_, err := runTool(ctx, exe, []string{"-f", "136+140", "-P", "home:" + dir, "-o", "x.%(ext)s"}, toolEnv(), false,
			func(l string, _ bool) {
				lines = append(lines, l)
				if strings.HasPrefix(l, "CHILDPID|") {
					pid, _ := strconv.Atoi(strings.TrimPrefix(l, "CHILDPID|"))
					child <- pid
				}
			})
		done <- err
	}()
	var pid int
	select {
	case pid = <-child:
	case <-time.After(20 * time.Second):
		t.Fatal("no child started")
	}
	time.Sleep(500 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != errCancelled {
			t.Errorf("err %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("runTool did not return after cancel")
	}
	if !processGone(pid, 5*time.Second) {
		t.Errorf("grandchild %d still running after cancel", pid)
	}
	for _, l := range lines {
		if strings.HasSuffix(l, "\r") {
			t.Errorf("line kept CR: %q", l)
		}
	}
}

func TestWinFullFlow(t *testing.T) {
	ft := fakeTool(t)
	ffzip := zipOf(t, map[string][]byte{
		"ffmpeg-master-latest-win64-gpl-shared/bin/ffmpeg.exe":     ft,
		"ffmpeg-master-latest-win64-gpl-shared/bin/ffprobe.exe":    ft,
		"ffmpeg-master-latest-win64-gpl-shared/bin/ffplay.exe":     ft,
		"ffmpeg-master-latest-win64-gpl-shared/bin/avcodec-62.dll": []byte("dll"),
		"ffmpeg-master-latest-win64-gpl-shared/LICENSE.txt":        []byte("gpl"),
	})
	ytzip := zipOf(t, map[string][]byte{"yt-dlp.exe": ft, `_internal\base_library.zip`: []byte("x")})
	denozip := zipOf(t, map[string][]byte{"deno.exe": ft})
	mux := http.NewServeMux()
	mux.HandleFunc("/nightly/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/x/releases/tag/2026.10.10.233141", http.StatusFound)
	})
	mux.HandleFunc("/nightly/dl/", func(w http.ResponseWriter, r *http.Request) { w.Write(ytzip) })
	mux.HandleFunc("/deno/latest.txt", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "v2.9.7") })
	mux.HandleFunc("/deno/gh/", func(w http.ResponseWriter, r *http.Request) { w.Write(denozip) })
	mux.HandleFunc("/ff.zip", func(w http.ResponseWriter, r *http.Request) { w.Write(ffzip) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	ytdlpNightlyLatest = srv.URL + "/nightly/latest"
	ytdlpNightlyAsset = srv.URL + "/nightly/dl/%s/yt-dlp_win.zip"
	ytdlpFallbackURLs = nil
	denoLatestTxt = srv.URL + "/deno/latest.txt"
	denoAssetGitHub = srv.URL + "/deno/gh/%s/deno.zip"
	denoAssetCDN = srv.URL + "/deno/none/%s"
	denoFallbackURL = srv.URL + "/deno/none"
	ffmpegURLs = []string{srv.URL + "/ff.zip"}

	root := t.TempDir()
	save := filepath.Join(root, "فيديوهات")
	os.MkdirAll(save, 0o755)
	t.Setenv("FAKE_INFO_JSON", filepath.Join(mustWd(t), "testdata", "youtube_like.json"))

	em := &chanEmitter{ch: make(chan string, 10000)}
	app := NewApp(em, newPaths(filepath.Join(root, "data")))
	if r := app.SetSaveDir(save); r.Error != "" {
		t.Fatal(r.Error)
	}
	if r := app.Init(); r.Screen != "setup" || len(r.Missing) != 3 {
		t.Fatalf("init %+v", r)
	}
	app.StartSetup()
	em.wait(t, "setup", "done", 60*time.Second)
	if r := app.Init(); r.Screen != "ready" {
		t.Fatalf("after setup %+v", r)
	}
	if fileOK(filepath.Join(app.tools.FfmpegDir(), "ffplay.exe")) || !fileOK(filepath.Join(app.tools.FfmpegDir(), "avcodec-62.dll")) {
		t.Error("ffmpeg folder content wrong")
	}

	app.Fetch("خد اللينك ده https://youtu.be/abcdefghijk?si=x")
	info := em.wait(t, "info", "done", 30*time.Second)
	if sel := info["items"].([]any)[0].(map[string]any)["selected"]; sel != "v720" {
		t.Fatalf("selected %v", sel)
	}

	id := func(m map[string]any) string { s, _ := m["id"].(string); return s }
	d := em.waitTask(t, id(app.Download(0, "v720")), stateDone, 30*time.Second)
	path := d["result"].(map[string]any)["path"].(string)
	vid := filepath.Join(save, "Video") // each kind in its own folder
	if filepath.Dir(path) != vid || filepath.Base(path) != "فيديو - تجربة - 2026 - 720p.mp4" || !fileOK(path) ||
		!strings.HasSuffix(d["result"].(map[string]any)["folderLabel"].(string), "\u200f← Video") {
		t.Errorf("result %v", d)
	}
	if _, err := os.Stat(filepath.Join(vid, tempFolderName)); err == nil {
		t.Error("temporary folder left")
	}

	d = em.waitTask(t, id(app.Download(0, "mp3-192")), stateDone, 30*time.Second)
	if p := d["result"].(map[string]any)["path"].(string); !strings.HasSuffix(p, "فيديو - تجربة - 2026.mp3") || filepath.Dir(p) != filepath.Join(save, "Music") {
		t.Errorf("audio %v", d)
	}

	// A post on X with two videos: the second one, saved under its own number.
	t.Setenv("FAKE_INFO_JSON", filepath.Join(mustWd(t), "testdata", "x_multi.json"))
	app.Fetch("https://x.com/egfootball/status/1840000000000000000?s=20")
	post := em.wait(t, "info", "done", 30*time.Second)
	if items := post["items"].([]any); len(items) != 2 || post["siteName"] != "إكس" {
		t.Fatalf("x post: %v", post)
	}
	d = em.waitTask(t, id(app.Download(1, "v720")), stateDone, 30*time.Second)
	if p := d["result"].(map[string]any)["path"].(string); filepath.Base(p) != "هدف رائع في الدقيقة 90 - 2 - 720p.mp4" || !fileOK(p) {
		t.Errorf("x post file %q", p)
	}
	t.Setenv("FAKE_INFO_JSON", filepath.Join(mustWd(t), "testdata", "youtube_like.json"))
	app.Fetch("https://youtu.be/abcdefghijk")
	em.wait(t, "info", "done", 30*time.Second)

	// Pause a slow download: its program (and the child it started) stops, its folder
	// stays; resume finishes it.
	t.Setenv("FAKE_SLOW_MS", "300")
	t.Setenv("FAKE_SPAWN", "1")
	slow := id(app.Download(0, "v1080"))
	for {
		task := em.waitTask(t, slow, stateRunning, 30*time.Second)
		if task["percent"].(float64) > 10 {
			break
		}
	}
	app.Pause(slow)
	em.waitTask(t, slow, statePaused, 30*time.Second)
	if _, err := os.Stat(filepath.Join(vid, tempFolderName, slow)); err != nil {
		t.Errorf("paused download lost its folder: %v", err)
	}
	t.Setenv("FAKE_SLOW_MS", "10")
	t.Setenv("FAKE_SPAWN", "")
	app.Resume(slow)
	em.waitTask(t, slow, stateDone, 60*time.Second)
	if _, err := os.Stat(filepath.Join(vid, tempFolderName, slow)); err == nil {
		t.Error("finished download left its folder")
	}

	// Delete a running download: it disappears with its folder.
	t.Setenv("FAKE_SLOW_MS", "300")
	gone := id(app.Download(0, "v480"))
	em.waitTask(t, gone, stateRunning, 30*time.Second)
	app.Delete(gone)
	em.waitTask(t, gone, "removed", 30*time.Second)
	if _, err := os.Stat(filepath.Join(vid, tempFolderName, gone)); err == nil {
		t.Error("deleted download left its folder")
	}
	t.Setenv("FAKE_SLOW_MS", "")

	// A failing download ends with a friendly error, after one retry.
	t.Setenv("FAKE_FAIL", "download")
	failed := id(app.Download(0, "v360"))
	e := em.waitTaskErr(t, failed, 60*time.Second)
	if e["code"] != "forbidden" {
		t.Errorf("error %v", e)
	}
	t.Setenv("FAKE_FAIL", "fetch")
	app.Fetch("https://www.youtube.com/watch?v=abcdefghijk")
	e = em.waitErr(t, "info", 30*time.Second)
	if e["code"] != "unavailable" {
		t.Errorf("fetch error %v", e)
	}
	app.Shutdown()
	if b, _ := os.ReadFile(filepath.Join(root, "data", "downloads.json")); !strings.Contains(string(b), `"state":"error"`) {
		t.Errorf("list not saved: %s", b)
	}
}

func mustWd(t *testing.T) string {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return wd
}

type chanEmitter struct{ ch chan string }

func (c *chanEmitter) Emit(event string, payload any) {
	b, _ := json.Marshal(payload)
	c.ch <- event + " " + string(b)
}

func (c *chanEmitter) next(t *testing.T, event string, timeout time.Duration) map[string]any {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case s := <-c.ch:
			name, js, _ := strings.Cut(s, " ")
			if name != event {
				continue
			}
			var m map[string]any
			json.Unmarshal([]byte(js), &m)
			return m
		case <-deadline:
			t.Fatalf("timeout waiting for %s", event)
		}
	}
}

func status(m map[string]any) string {
	if s, _ := m["status"].(string); s != "" {
		return s
	}
	s, _ := m["phase"].(string)
	return s
}

func (c *chanEmitter) wait(t *testing.T, event, want string, timeout time.Duration) map[string]any {
	t.Helper()
	end := time.Now().Add(timeout)
	for time.Now().Before(end) {
		m := c.next(t, event, time.Until(end))
		switch status(m) {
		case want:
			return m
		case "error":
			t.Fatalf("%s error: %v", event, m)
		}
	}
	t.Fatalf("timeout waiting for %s/%s", event, want)
	return nil
}

// waitTask waits until download id reaches state ("removed" for its removal).
func (c *chanEmitter) waitTask(t *testing.T, id, state string, timeout time.Duration) map[string]any {
	t.Helper()
	end := time.Now().Add(timeout)
	for time.Now().Before(end) {
		m := c.next(t, "task", time.Until(end))
		if state == "removed" {
			if m["op"] == "remove" && m["id"] == id {
				return m
			}
			continue
		}
		task, _ := m["task"].(map[string]any)
		if task == nil || task["id"] != id {
			continue
		}
		switch task["state"] {
		case state:
			return task
		case stateError:
			t.Fatalf("download error: %v", task["error"])
		}
	}
	t.Fatalf("timeout waiting for download %s/%s", id, state)
	return nil
}

func (c *chanEmitter) waitTaskErr(t *testing.T, id string, timeout time.Duration) map[string]any {
	t.Helper()
	end := time.Now().Add(timeout)
	for time.Now().Before(end) {
		m := c.next(t, "task", time.Until(end))
		if task, _ := m["task"].(map[string]any); task != nil && task["id"] == id && task["state"] == stateError {
			e, _ := task["error"].(map[string]any)
			return e
		}
	}
	t.Fatalf("no error for download %s", id)
	return nil
}

func (c *chanEmitter) waitErr(t *testing.T, event string, timeout time.Duration) map[string]any {
	t.Helper()
	end := time.Now().Add(timeout)
	for time.Now().Before(end) {
		m := c.next(t, event, time.Until(end))
		if status(m) == "error" {
			e, _ := m["error"].(map[string]any)
			return e
		}
	}
	t.Fatalf("no error for %s", event)
	return nil
}

// The small window of downloads: sized to what the page asks, in the corner of the clock,
// shown without taking the keyboard; a window hidden while maximized comes back maximized.
func TestWinMiniPlacement(t *testing.T) {
	create, destroy := user32.NewProc("CreateWindowExW"), user32.NewProc("DestroyWindow")
	const wsOverlappedWindow = 0x00CF0000
	hwnd, _, err := create.Call(0, uintptr(unsafe.Pointer(utf16("STATIC"))), uintptr(unsafe.Pointer(utf16("HELINK test"))),
		wsOverlappedWindow, 100, 100, 900, 600, 0, 0, 0, 0)
	if hwnd == 0 {
		t.Skipf("no window here: %v", err)
	}
	defer destroy.Call(hwnd)
	h := &winHost{hwnd: hwnd, scale: 1, shownOnce: true}
	h.saveNormal()
	if h.normal.Right-h.normal.Left != 900 || h.reZoom {
		t.Errorf("normal place %+v %v", h.normal, h.reZoom)
	}
	h.compact, h.compactOf, h.miniH = true, "task", 150
	h.placeMini(false)
	if windowVisible(hwnd) {
		t.Error("shown before the page drew it")
	}
	h.placeMini(true)
	var wr, cr rect
	procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&wr)))
	procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&cr)))
	wa := workArea()
	t.Logf("work area %+v, window %+v, inside %dx%d", wa, wr, cr.Right-cr.Left, cr.Bottom-cr.Top)
	if cr.Right-cr.Left != miniWidth || cr.Bottom-cr.Top != 150 {
		t.Errorf("inside %dx%d, want %dx150", cr.Right-cr.Left, cr.Bottom-cr.Top, miniWidth)
	}
	left, top := trayCorner(wa)
	if !left && !top && (wr.Right > wa.Right+16 || wr.Right < wa.Right-40 || wr.Bottom > wa.Bottom+16 || wr.Bottom < wa.Bottom-40) {
		t.Errorf("not in the corner of the clock: %+v in %+v", wr, wa)
	}
	if !windowVisible(hwnd) {
		t.Error("not shown")
	}
	// Hidden while maximized: back at the usual size, maximized again when opened.
	procShowWindow.Call(hwnd, swMaximize)
	procShowWindow.Call(hwnd, swHide)
	h.compact = false
	h.saveNormal()
	style, _, _ := procGetWindowLongPtrW.Call(hwnd, uintptr(^uintptr(15))) // GWL_STYLE (-16)
	if !h.reZoom || style&0x01000000 != 0 || h.normal != defaultWindowRect(1) {
		t.Errorf("maximized window: reZoom %v style %#x normal %+v", h.reZoom, style, h.normal)
	}
}
