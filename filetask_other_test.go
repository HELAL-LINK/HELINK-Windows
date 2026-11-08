//go:build !windows

package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fileApp is an app that saves in <tmp>/Downloads (files need no yt-dlp).
func fileApp(t *testing.T, data string) (*App, *recEmitter, string) {
	t.Helper()
	fastWaits(t)
	tmp := t.TempDir()
	save := filepath.Join(tmp, "Downloads")
	t.Setenv("NAZZELHA_SAVE", save)
	t.Setenv("NAZZELHA_DEV_TOOLS", "1")
	if data == "" {
		data = filepath.Join(tmp, "data")
	}
	em := newRec()
	app := NewApp(em, newPaths(data))
	t.Cleanup(app.Shutdown)
	return app, em, save
}

// waitFile waits for the "file" event of pending file pid.
func waitFile(t *testing.T, em *recEmitter, pid string, timeout time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		name, m := em.next(t, deadline)
		if name == "file" && m["pid"] == pid && m["status"] != "checking" {
			return m
		}
	}
}

// nextTask returns the next change of download id (an error fails the test).
func nextTask(t *testing.T, em *recEmitter, id string, timeout time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		name, m := em.next(t, deadline)
		task, _ := m["task"].(map[string]any)
		if name != "task" || task == nil || task["id"] != id {
			continue
		}
		if task["state"] == stateError {
			t.Fatalf("unexpected error: %v", task["error"])
		}
		return task
	}
}

func addLinkFile(t *testing.T, app *App, em *recEmitter, link string) map[string]any {
	t.Helper()
	r := app.AddLink(link)
	if r["kind"] != "file" {
		t.Fatalf("add link %s: %v", link, r)
	}
	return waitFile(t, em, r["pid"].(string), 20*time.Second)
}

func TestFileTaskFromLink(t *testing.T) {
	app, em, save := fileApp(t, "")
	app.ChangeSettings(map[string]any{"categoryDirs": false}) // everything in the save folder itself
	fs := newFileServer(5<<20 + 321)
	fs.set(func() { fs.rate = 2 << 20 })
	fs.disposition = `attachment; filename="HELINK Setup 2.0.exe"`
	srv := httptest.NewServer(fs)
	defer srv.Close()
	r0 := app.AddLink("شوف ده " + srv.URL + "/get?id=7 ")
	if name, m := em.next(t, time.Now().Add(5*time.Second)); name != "file" || m["status"] != "checking" || m["pid"] != r0["pid"] ||
		m["url"] != srv.URL+"/get?id=7" || m["from"] != "link" {
		t.Fatalf("first event %s %v", name, m)
	}
	ev := waitFile(t, em, r0["pid"].(string), 20*time.Second)
	if ev["status"] != "ready" || ev["name"] != "HELINK Setup 2.0.exe" || ev["category"] != catPrograms ||
		ev["size"].(float64) != float64(len(fs.data)) || ev["resumable"] != true || ev["folder"] != save || ev["existing"] != nil {
		t.Fatalf("new download window: %v", ev)
	}
	r := app.ConfirmFile(ev["pid"].(string), FileChoice{Start: true})
	id, _ := r["id"].(string)
	if id == "" {
		t.Fatalf("confirm: %v", r)
	}
	maxConns := 0.0
	for {
		task := nextTask(t, em, id, 30*time.Second)
		if c, _ := task["conns"].(float64); c > maxConns {
			maxConns = c
		}
		if task["state"] == stateDone {
			res := task["result"].(map[string]any)
			if res["path"] != filepath.Join(save, "HELINK Setup 2.0.exe") || task["label"] != "EXE" || task["category"] != catPrograms {
				t.Fatalf("done: %v", task)
			}
			break
		}
	}
	checkFile(t, filepath.Join(save, "HELINK Setup 2.0.exe"), fs.data)
	if maxConns < 2 {
		t.Errorf("connections seen: %v", maxConns)
	}
	if entries, _ := os.ReadDir(filepath.Join(save, tempFolderName)); len(entries) != 0 {
		t.Errorf("temporary files left: %v", entries)
	}
	// The same file again gets a new name.
	ev = addLinkFile(t, app, em, srv.URL+"/get?id=7")
	r = app.ConfirmFile(ev["pid"].(string), FileChoice{Start: true, Name: "  my copy.exe "})
	em.waitTask(t, r["id"].(string), stateDone, 30*time.Second)
	checkFile(t, filepath.Join(save, "my copy.exe"), fs.data)
	ev = addLinkFile(t, app, em, srv.URL+"/get?id=7")
	r = app.ConfirmFile(ev["pid"].(string), FileChoice{Start: true})
	em.waitTask(t, r["id"].(string), stateDone, 30*time.Second)
	checkFile(t, filepath.Join(save, "HELINK Setup 2.0 (2).exe"), fs.data)
}

func TestFileTaskPauseAndRestart(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	app, em, save := fileApp(t, data)
	fs := newFileServer(8 << 20)
	fs.set(func() { fs.rate = 1 << 20 })
	srv := httptest.NewServer(fs)
	defer srv.Close()
	ev := addLinkFile(t, app, em, srv.URL+"/files/movie.mkv")
	if ev["category"] != catVideo {
		t.Fatalf("category %v", ev["category"])
	}
	id := app.ConfirmFile(ev["pid"].(string), FileChoice{Start: true})["id"].(string)
	for {
		task := em.waitTask(t, id, stateRunning, 30*time.Second)
		if task["done"].(float64) > 2<<20 {
			break
		}
	}
	app.Pause(id)
	paused := em.waitTask(t, id, statePaused, 10*time.Second)
	done := paused["done"].(float64)
	if done < 2<<20 || paused["doneText"] == "" || paused["resumable"] != true {
		t.Fatalf("paused: %v", paused)
	}
	// Close the app, open it again: the download is there, and continues.
	app.Shutdown()
	time.Sleep(300 * time.Millisecond)
	before := fs.served.Load()
	em2 := newRec()
	app2 := NewApp(em2, newPaths(data))
	defer app2.Shutdown()
	again := taskIn(app2, id)
	if again == nil || again["state"] != statePaused || again["done"].(float64) != done || again["fileName"] != "movie.mkv" {
		t.Fatalf("after restart: %v", again)
	}
	fs.set(func() { fs.rate = 0 })
	app2.Resume(id)
	em2.waitTask(t, id, stateDone, 30*time.Second)
	checkFile(t, filepath.Join(save, "Video", "movie.mkv"), fs.data)
	if sent := fs.served.Load() - before; float64(sent) > float64(len(fs.data))-done+float64(1<<20) {
		t.Fatalf("started over: %d bytes sent again, %v were done", sent, done)
	}
}

func TestFileTaskLaterFoldersAndContinue(t *testing.T) {
	app, em, save := fileApp(t, "")
	app.ChangeSettings(map[string]any{"categoryDirs": true, "maxParallel": 1.0})
	fs := newFileServer(4 << 20)
	fs.set(func() { fs.rate = 1 << 20 })
	mux := http.NewServeMux()
	mux.Handle("/a/", fs)
	mux.Handle("/b/", fs)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	ev := addLinkFile(t, app, em, srv.URL+"/a/archive.zip")
	folders := ev["folders"].(map[string]any)
	if ev["folder"] != filepath.Join(save, "Compressed") || folders[catMusic].(map[string]any)["folder"] != filepath.Join(save, "Music") {
		t.Fatalf("folders: %v", ev)
	}
	// "Later": in the list, not started.
	id := app.ConfirmFile(ev["pid"].(string), FileChoice{})["id"].(string)
	if task := em.waitTask(t, id, statePaused, 5*time.Second); task["folderLabel"] != "التنزيلات ← HELINK \u200f← Compressed" {
		t.Fatalf("later: %v", task)
	}
	app.Resume(id)
	for {
		if task := em.waitTask(t, id, stateRunning, 30*time.Second); task["done"].(float64) > 1<<20 {
			break
		}
	}
	app.Pause(id)
	em.waitTask(t, id, statePaused, 10*time.Second)
	// The same file from another address: the window offers to continue it.
	ev = addLinkFile(t, app, em, srv.URL+"/b/archive.zip")
	ex, _ := ev["existing"].(map[string]any)
	if ex == nil || ex["id"] != id {
		t.Fatalf("existing: %v", ev)
	}
	fs.set(func() { fs.rate = 0 })
	r := app.ConfirmFile(ev["pid"].(string), FileChoice{Continue: id})
	if r["id"] != id || r["existing"] != true {
		t.Fatalf("continue: %v", r)
	}
	task := em.waitTask(t, id, stateDone, 30*time.Second)
	t.Logf("continued: %v", task["result"])
	if !strings.Contains(task["url"].(string), "/b/") {
		t.Errorf("address not updated: %v", task["url"])
	}
	checkFile(t, filepath.Join(save, "Compressed", "archive.zip"), fs.data)
	if n := len(app.TaskList()); n != 1 {
		t.Errorf("%d downloads in the list", n)
	}
	// One at a time.
	var ids []string
	for i := 0; i < 3; i++ {
		ev := addLinkFile(t, app, em, fmt.Sprintf("%s/a/f%d.pdf", srv.URL, i))
		ids = append(ids, app.ConfirmFile(ev["pid"].(string), FileChoice{Start: true})["id"].(string))
	}
	fs.set(func() { fs.rate = 4 << 20 })
	deadline := time.Now().Add(40 * time.Second)
	for {
		running, done := 0, 0
		for _, id := range ids {
			switch taskIn(app, id)["state"] {
			case stateRunning:
				running++
			case stateDone:
				done++
			}
		}
		if running > 1 {
			t.Fatalf("%d run at once", running)
		}
		if done == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timeout")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(save, "Documents", "f2.pdf")); err != nil {
		t.Fatal(err)
	}
	// Removing with the file.
	app.Remove([]string{ids[0]}, true)
	em.waitTask(t, ids[0], "removed", 5*time.Second)
	if _, err := os.Stat(filepath.Join(save, "Documents", "f0.pdf")); !os.IsNotExist(err) {
		t.Errorf("file still there: %v", err)
	}
	app.Remove([]string{ids[1]}, false)
	em.waitTask(t, ids[1], "removed", 5*time.Second)
	if _, err := os.Stat(filepath.Join(save, "Documents", "f1.pdf")); err != nil {
		t.Errorf("file gone: %v", err)
	}
}

func TestFileTaskFailures(t *testing.T) {
	app, em, _ := fileApp(t, "")
	defer setLang("ar")
	fs := newFileServer(1 << 20)
	srv := httptest.NewServer(fs)
	defer srv.Close()
	fs.set(func() { fs.check = func(*http.Request) int { return 404 } })
	ev := addLinkFile(t, app, em, srv.URL+"/missing.zip")
	if e, _ := ev["error"].(map[string]any); ev["status"] != "error" || e["code"] != "file_gone" || !strings.Contains(e["message"].(string), "127.0.0.1") {
		t.Fatalf("404: %v", ev)
	}
	// The link worked when added, then the server refuses: the task says the link expired.
	fs.set(func() { fs.check = nil })
	fs.set(func() { fs.rate = 1 << 20 })
	ev = addLinkFile(t, app, em, srv.URL+"/soon.bin")
	id := app.ConfirmFile(ev["pid"].(string), FileChoice{Start: true})["id"].(string)
	for {
		if task := em.waitTask(t, id, stateRunning, 30*time.Second); task["done"].(float64) > 100<<10 {
			break
		}
	}
	fs.set(func() { fs.check = func(*http.Request) int { return 403 } })
	app.Pause(id)
	em.waitTask(t, id, statePaused, 10*time.Second)
	app.Resume(id)
	deadline := time.Now().Add(20 * time.Second)
	for {
		name, m := em.next(t, deadline)
		task, _ := m["task"].(map[string]any)
		if name != "task" || task == nil || task["id"] != id || task["state"] != stateError {
			continue
		}
		if e := task["error"].(map[string]any); e["code"] != "link_expired" || e["title"] != "انتهت صلاحية رابط التنزيل" {
			t.Fatalf("error: %v", e)
		}
		break
	}
	setLang("en")
	if e := taskIn(app, id)["error"].(map[string]any); e["title"] != "The download link expired" {
		t.Fatalf("english: %v", e)
	}
}

// A web page that shows a video: no file there, so yt-dlp reads the page.
func TestAddLinkPageWithVideo(t *testing.T) {
	if _, err := exec.LookPath("yt-dlp"); err != nil {
		t.Skip("yt-dlp not installed")
	}
	if _, err := os.Stat(e2eMedia); err != nil {
		t.Skip("local media not prepared")
	}
	app, em, save := fileApp(t, "")
	mux := http.NewServeMux()
	mux.Handle("/media/", http.StripPrefix("/media/", http.FileServer(http.Dir(e2eMedia))))
	mux.HandleFunc("/watch", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><html><head><title>A cooking lesson</title></head><body>
<video controls src="/media/c360.mp4" width="640" height="360"></video></body></html>`)
	})
	mux.HandleFunc("/empty", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><html><head><title>Nothing</title></head><body><p>No video here.</p></body></html>`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	ev := addLinkFile(t, app, em, srv.URL+"/watch")
	if ev["status"] != "media" {
		t.Fatalf("page: %v", ev)
	}
	seq := ev["seq"].(float64)
	var info map[string]any
	for deadline := time.Now().Add(60 * time.Second); ; {
		name, m := em.next(t, deadline)
		if name == "info" && m["seq"].(float64) == seq && (m["status"] == "done" || m["status"] == "error") {
			info = m
			break
		}
	}
	if info["status"] != "done" || info["siteName"] != "127.0.0.1" {
		t.Fatalf("read: %v", info)
	}
	items := info["items"].([]any)
	opts := items[0].(map[string]any)["options"].([]any)
	key := opts[0].(map[string]any)["key"].(string)
	r := app.Download(0, key)
	id, _ := r["id"].(string)
	if id == "" {
		t.Fatalf("download: %v", r)
	}
	task := em.waitTask(t, id, stateDone, 90*time.Second)
	res := task["result"].(map[string]any)
	if !strings.HasPrefix(res["path"].(string), save) || !strings.HasSuffix(res["path"].(string), ".mp4") {
		t.Fatalf("saved: %v", res)
	}
	// A page with nothing to download.
	ev = addLinkFile(t, app, em, srv.URL+"/empty")
	seq = ev["seq"].(float64)
	for deadline := time.Now().Add(60 * time.Second); ; {
		name, m := em.next(t, deadline)
		if name == "info" && m["seq"].(float64) == seq && m["status"] == "error" {
			if e := m["error"].(map[string]any); e["code"] != "nothing_found" {
				t.Fatalf("empty page: %v", e)
			}
			break
		}
	}
}

// The cookies file is what yt-dlp (Python's MozillaCookieJar) reads.
func TestNetscapeCookies(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("no python3")
	}
	p := filepath.Join(t.TempDir(), "c.txt")
	err := writeNetscapeCookies(p, []Cookie{
		{Name: "sid", Value: "a=b c", Domain: "www.example.com", HostOnly: true, Path: "/", Secure: true, Expires: 1999999999.5},
		{Name: "pref", Value: "1", Domain: ".example.com"},
		{Name: "lang", Value: "ar", Domain: "example.org", Path: "/app"},
		{Name: "bad\tname", Value: "x", Domain: "example.org"},
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("python3", "-I", "-c", `
import http.cookiejar, sys
j = http.cookiejar.MozillaCookieJar(sys.argv[1])
j.load(ignore_discard=True, ignore_expires=True)
for c in sorted(j, key=lambda c: c.name):
    print(c.name, c.value, c.domain, c.domain_specified, c.path, c.secure, c.expires)
`, p).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	want := "lang ar .example.org True /app False None\npref 1 .example.com True / False None\nsid a=b c www.example.com False / True 1999999999\n"
	if string(out) != want {
		t.Fatalf("python read:\n%s", out)
	}
}
