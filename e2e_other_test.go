//go:build !windows

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// These tests run the real yt-dlp and ffmpeg (from PATH) against a local web server that
// serves the media of /home/claude/ytest (made by makeinfo.py), with yt-dlp's answer for
// the link read from a file.

const (
	e2eMedia = "/home/claude/ytest/media"
	e2eInfo  = "/home/claude/ytest/info_youtube.json"
)

type recEmitter struct {
	mu     sync.Mutex
	events []string
	ch     chan string
}

func newRec() *recEmitter { return &recEmitter{ch: make(chan string, 20000)} }

func (r *recEmitter) Emit(event string, payload any) {
	b, _ := json.Marshal(payload)
	r.mu.Lock()
	r.events = append(r.events, event+" "+string(b))
	r.mu.Unlock()
	select {
	case r.ch <- event + " " + string(b):
	default:
	}
}

func (r *recEmitter) next(t *testing.T, deadline time.Time) (string, map[string]any) {
	t.Helper()
	select {
	case s := <-r.ch:
		name, js, _ := strings.Cut(s, " ")
		var m map[string]any
		json.Unmarshal([]byte(js), &m)
		return name, m
	case <-time.After(time.Until(deadline)):
		t.Fatalf("timeout")
		return "", nil
	}
}

func (r *recEmitter) waitFor(t *testing.T, event, status string, timeout time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		name, m := r.next(t, deadline)
		if name != event {
			continue
		}
		st, _ := m["status"].(string)
		if st == "" {
			st, _ = m["phase"].(string)
		}
		if st == status {
			return m
		}
		if st == "error" {
			t.Fatalf("unexpected error event: %v", m)
		}
	}
}

// waitTask waits until download id reaches state ("removed" for its removal). An error
// state fails the test.
func (r *recEmitter) waitTask(t *testing.T, id, state string, timeout time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		name, m := r.next(t, deadline)
		if name != "task" {
			continue
		}
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
		if task["state"] == state {
			return task
		}
		if task["state"] == stateError {
			t.Fatalf("unexpected error: %v", task["error"])
		}
	}
}

// mediaServer serves files with range requests at a limited speed, and records each
// request and the bytes sent per file.
type mediaServer struct {
	*httptest.Server
	mu     sync.Mutex
	served []string // "file range"
	sent   map[string]int64
}

func newMediaServer(dir string, bytesPerSec int) *mediaServer {
	m := &mediaServer{sent: map[string]int64{}}
	fs := http.FileServer(http.Dir(dir))
	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		m.mu.Lock()
		m.served = append(m.served, name+" "+r.Header.Get("Range"))
		m.mu.Unlock()
		fs.ServeHTTP(&slowWriter{ResponseWriter: w, rate: bytesPerSec, count: func(n int) {
			m.mu.Lock()
			m.sent[name] += int64(n)
			m.mu.Unlock()
		}}, r)
	}))
	return m
}

func (m *mediaServer) reset() {
	m.mu.Lock()
	m.served, m.sent = nil, map[string]int64{}
	m.mu.Unlock()
}

type slowWriter struct {
	http.ResponseWriter
	rate  int
	count func(int)
}

func (s *slowWriter) Write(p []byte) (int, error) {
	total := 0
	for len(p) > 0 {
		n := min(len(p), 16384)
		k, err := s.ResponseWriter.Write(p[:n])
		total += k
		s.count(k)
		if err != nil {
			return total, err
		}
		p = p[n:]
		time.Sleep(time.Duration(float64(n) / float64(s.rate) * float64(time.Second)))
	}
	return total, nil
}

// e2eApp prepares an app that downloads from srv, saving in <tmp>/Downloads.
func e2eApp(t *testing.T, srv *mediaServer, infoJSON []byte) (app *App, em *recEmitter, tmp, save string) {
	t.Helper()
	if _, err := exec.LookPath("yt-dlp"); err != nil {
		t.Skip("yt-dlp not installed")
	}
	tmp = t.TempDir()
	if infoJSON == nil {
		b, err := os.ReadFile(e2eInfo)
		if err != nil {
			t.Skip("local media not prepared")
		}
		infoJSON = b
	}
	infoJSON = []byte(strings.ReplaceAll(string(infoJSON), "http://127.0.0.1:18765/", srv.URL+"/"))
	infoFile := filepath.Join(tmp, "info.json")
	os.WriteFile(infoFile, infoJSON, 0o644)
	save = filepath.Join(tmp, "Downloads")
	t.Setenv("NAZZELHA_DEV_TOOLS", "1")
	t.Setenv("NAZZELHA_DEV_INFOJSON", infoFile)
	t.Setenv("NAZZELHA_SAVE", save)
	em = newRec()
	app = NewApp(em, newPaths(filepath.Join(tmp, "data")))
	return app, em, tmp, save
}

func ffprobe(t *testing.T, path, entries string) string {
	out, _ := exec.Command("ffprobe", "-v", "error", "-show_entries", entries, "-of", "csv=p=0", path).Output()
	return strings.Join(strings.Fields(string(out)), ",")
}

// processesUsing reports programs whose command line mentions dir (a yt-dlp left running).
func processesUsing(dir string) bool {
	out, _ := exec.Command("ps", "-eo", "args").Output()
	for _, l := range strings.Split(string(out), "\n") {
		if strings.Contains(l, dir) && !strings.Contains(l, "ps -eo") {
			return true
		}
	}
	return false
}

func taskIn(app *App, id string) map[string]any {
	for _, m := range app.TaskList() {
		if m["id"] == id {
			b, _ := json.Marshal(m)
			var out map[string]any
			json.Unmarshal(b, &out)
			return out
		}
	}
	return nil
}

func TestEndToEndDownload(t *testing.T) {
	if _, err := os.Stat(e2eMedia); err != nil {
		t.Skip("local media not prepared")
	}
	srv := newMediaServer(e2eMedia, 1_500_000)
	defer srv.Close()
	app, em, tmp, save := e2eApp(t, srv, nil)
	defer setLang("ar")
	if r := app.Init(); r.Screen != "ready" || r.Lang != "ar" || r.Platform != "windows" || r.SaveDirLabel != "التنزيلات ← HELINK" {
		t.Fatalf("init %+v", r)
	}

	app.Fetch("https://youtu.be/TESTVID0001")
	info := em.waitFor(t, "info", "done", 60*time.Second)
	item0 := info["items"].([]any)[0].(map[string]any)
	var keys []string
	for _, o := range item0["options"].([]any) {
		keys = append(keys, o.(map[string]any)["key"].(string))
	}
	if strings.Join(keys, ",") != "v1080,v720,v360,mp3-320,mp3-192,mp3-128" || item0["selected"] != "v720" || info["siteName"] != "يوتيوب" {
		t.Fatalf("options %v selected %v site %v", keys, item0["selected"], info["siteName"])
	}
	if f := app.Init().Fetch; f == nil || f.Status != "done" || f.Seq != int(info["seq"].(float64)) {
		t.Errorf("a page that missed the result can catch up: %+v", f)
	}
	// Switching the language rewrites what is already on screen, and is remembered.
	app.SetLang("en")
	b, _ := json.Marshal(app.Init())
	var en struct {
		Lang         string
		SaveDirLabel string
		Restore      struct {
			Info struct {
				SiteName string
				Items    []struct {
					Options []struct{ Key, Desc string }
				}
			}
		}
	}
	json.Unmarshal(b, &en)
	if en.Lang != "en" || en.SaveDirLabel != "Downloads › HELINK" || en.Restore.Info.SiteName != "YouTube" ||
		en.Restore.Info.Items[0].Options[1].Desc != "Good quality, reasonable size" {
		t.Errorf("post shown again in English: %s", b)
	}
	if OpenStore(filepath.Join(tmp, "data", "settings.json")).Get().Lang != "en" {
		t.Error("language not remembered")
	}
	app.SetLang("ar")

	// 1) 720p: H.264 + AAC joined into MP4.
	r := app.Download(0, "v720")
	id1, _ := r["id"].(string)
	if id1 == "" {
		t.Fatalf("download not added: %v", r)
	}
	seen := map[string]bool{}
	var done map[string]any
	deadline := time.Now().Add(90 * time.Second)
	for done == nil {
		name, m := em.next(t, deadline)
		if name != "task" {
			continue
		}
		task, _ := m["task"].(map[string]any)
		if task == nil || task["id"] != id1 {
			continue
		}
		switch task["state"] {
		case stateRunning:
			seen[task["stage"].(string)] = true
		case stateDone:
			done = task
		case stateError:
			t.Fatalf("download error: %v", task["error"])
		}
	}
	if !seen[stageVideo] || !seen[stageAudio] || !seen[stageMerge] || !seen[stageSave] {
		t.Errorf("stages seen %v", seen)
	}
	res := done["result"].(map[string]any)
	path := res["path"].(string)
	// Videos go to their own folder in the save folder.
	if filepath.Dir(path) != filepath.Join(save, "Video") || res["name"] != "أغنية 'تجربة' - الحلقة 1 - 100% كوميدي نسخة جديدة - 720p.mp4" ||
		res["folderLabel"] != "التنزيلات ← HELINK \u200f← Video" || done["percent"] != 100.0 {
		t.Errorf("result %v", done)
	}
	if got := ffprobe(t, path, "stream=codec_name"); got != "h264,aac" {
		t.Errorf("streams %v", got)
	}
	if _, err := os.Stat(filepath.Join(save, "Video", tempFolderName)); err == nil {
		t.Error("temporary folder left behind")
	}

	// 2) Same quality again gets a new name instead of overwriting.
	id2, _ := app.Download(0, "v720")["id"].(string)
	if id2 == "" || id2 == id1 {
		t.Fatalf("second download %q", id2)
	}
	if p := em.waitTask(t, id2, stateDone, 90*time.Second)["result"].(map[string]any)["path"].(string); !strings.HasSuffix(p, " - 720p (2).mp4") {
		t.Errorf("second path %q", p)
	}

	// 3) MP3 at 128 kb/s.
	id3, _ := app.Download(0, "mp3-128")["id"].(string)
	res3 := em.waitTask(t, id3, stateDone, 90*time.Second)["result"].(map[string]any)
	p3 := res3["path"].(string)
	if !strings.HasSuffix(p3, ".mp3") || res3["kind"] != "audio" {
		t.Errorf("audio %v", res3)
	}
	if br := ffprobe(t, p3, "format=bit_rate"); !strings.HasPrefix(br, "12") && !strings.HasPrefix(br, "13") {
		t.Errorf("mp3 bitrate %q, want about 128000", br)
	}
	if r := app.Init(); r.LastKind != "audio" || len(r.Tasks) != 3 || r.Tasks[0]["id"] != id3 {
		t.Errorf("last kind %q, %d tasks, newest first %v", r.LastKind, len(r.Tasks), r.Tasks[0]["id"])
	}

	// 4) Cancel and delete in the middle of a download.
	id4, _ := app.Download(0, "v1080")["id"].(string)
	for {
		task := em.waitTask(t, id4, stateRunning, 30*time.Second)
		if task["percent"].(float64) > 0 {
			break
		}
	}
	time.Sleep(300 * time.Millisecond)
	if !app.Busy() {
		t.Error("should be busy while downloading")
	}
	app.Delete(id4)
	em.waitTask(t, id4, "removed", 30*time.Second)
	time.Sleep(300 * time.Millisecond)
	if app.Busy() {
		t.Error("still busy after delete")
	}
	if _, err := os.Stat(filepath.Join(save, "Video", tempFolderName)); err == nil {
		t.Error("temporary folder left behind after delete")
	}
	entries, _ := os.ReadDir(filepath.Join(save, "Video"))
	for _, e := range entries {
		if strings.Contains(e.Name(), "1080p") {
			t.Errorf("partial file left: %s", e.Name())
		}
	}
	if processesUsing(tmp) {
		t.Error("a download program is still running")
	}
	// Removing a finished download from the list keeps its file.
	app.Delete(id1)
	em.waitTask(t, id1, "removed", 5*time.Second)
	if !fileOK(path) {
		t.Error("file deleted with its entry")
	}
	app.ClearFinished()
	if n := len(app.TaskList()); n != 0 {
		t.Errorf("list not cleared: %d", n)
	}

	// 5) A link from another site is read by yt-dlp too, named by its address.
	app.Fetch("https://www.dailymotion.com/video/x8abc")
	m := map[string]any{}
	for name := ""; name != "info"; {
		name, m = em.next(t, time.Now().Add(5*time.Second))
	}
	if m["status"] != "loading" || m["siteName"] != "dailymotion.com" {
		t.Errorf("other site: %v", m)
	}
	app.CancelFetch(true)
	// Text without any web address is refused at once.
	if r := app.AddLink("مرحبا بك"); r["error"] == nil || r["error"].(*UserError).Code != "invalid_url" {
		t.Errorf("no address: %v", r)
	}
	app.Shutdown()
}

// A post with two videos (like a post on X), both downloaded at the same time.
func TestEndToEndPost(t *testing.T) {
	if _, err := os.Stat(e2eMedia); err != nil {
		t.Skip("local media not prepared")
	}
	srv := newMediaServer(e2eMedia, 3_000_000)
	defer srv.Close()
	b, err := os.ReadFile(e2eInfo)
	if err != nil {
		t.Skip("local media not prepared")
	}
	var entry map[string]any
	if err := json.Unmarshal(b, &entry); err != nil {
		t.Fatal(err)
	}
	mk := func(id string, n int) map[string]any {
		e := map[string]any{}
		for k, v := range entry {
			e[k] = v
		}
		e["id"], e["title"] = id, fmt.Sprintf("Egypt Football - هدف #%d", n)
		e["extractor"], e["extractor_key"] = "twitter", "Twitter"
		e["webpage_url"] = "https://x.com/egfootball/status/1840000000000000000"
		delete(e, "_type")
		return e
	}
	post := map[string]any{"_type": "playlist", "id": "1840000000000000000", "title": "Egypt Football - هدفين",
		"extractor": "twitter", "extractor_key": "Twitter", "webpage_url": "https://x.com/egfootball/status/1840000000000000000",
		"entries": []any{mk("1840000000000000001", 1), mk("1840000000000000002", 2)}}
	pb, _ := json.Marshal(post)
	app, em, _, _ := e2eApp(t, srv, pb)

	app.Fetch("https://x.com/egfootball/status/1840000000000000000?s=20")
	info := em.waitFor(t, "info", "done", 60*time.Second)
	items := info["items"].([]any)
	if len(items) != 2 || info["site"] != siteX || info["siteName"] != "إكس" {
		t.Fatalf("post info: %d items, site %v", len(items), info["site"])
	}
	if idx := items[1].(map[string]any)["index"]; idx != float64(2) {
		t.Errorf("second item index %v", idx)
	}
	a, _ := app.Download(1, "v720")["id"].(string)
	bID, _ := app.Download(0, "mp3-192")["id"].(string)
	p := em.waitTask(t, a, stateDone, 90*time.Second)["result"].(map[string]any)["path"].(string)
	if !strings.HasSuffix(p, "Egypt Football - هدف - 2 - 720p.mp4") || !fileOK(p) {
		t.Errorf("post download %q", p)
	}
	tb := taskIn(app, bID)
	if tb["state"] != stateDone {
		tb = em.waitTask(t, bID, stateDone, 90*time.Second)
	}
	if p := tb["result"].(map[string]any)["path"].(string); !strings.HasSuffix(p, "Egypt Football - هدف - 1.mp3") {
		t.Errorf("post mp3 %q", p)
	}
	app.Shutdown()
}

func findPart(dir string) (string, int64) {
	var found string
	var size int64
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".part") && found == "" {
			found = p
			if fi, err := d.Info(); err == nil {
				size = fi.Size()
			}
		}
		return nil
	})
	return found, size
}

// Two downloads at a time, pause and resume (continuing the partial file), and the list
// surviving a restart.
func TestEndToEndQueue(t *testing.T) {
	if _, err := os.Stat(e2eMedia); err != nil {
		t.Skip("local media not prepared")
	}
	srv := newMediaServer(e2eMedia, 400_000)
	defer srv.Close()
	app, em, tmp, save := e2eApp(t, srv, nil)
	const parallelDownloads = 2
	app.store.Update(func(s *Settings) { s.MaxParallel = parallelDownloads })
	app.Fetch("https://youtu.be/TESTVID0001")
	em.waitFor(t, "info", "done", 60*time.Second)

	// 1) Three downloads: two run, the third waits for its turn.
	id := func(m map[string]any) string { s, _ := m["id"].(string); return s }
	a, b, c := id(app.Download(0, "v720")), id(app.Download(0, "v360")), id(app.Download(0, "v1080"))
	running := 0
	for until := time.Now().Add(10 * time.Second); time.Now().Before(until); time.Sleep(50 * time.Millisecond) {
		running = 0
		for _, m := range app.TaskList() {
			if m["state"] == stateRunning {
				running++
			}
		}
		if running == parallelDownloads {
			break
		}
	}
	if running != parallelDownloads || taskIn(app, c)["state"] != stateQueued {
		t.Fatalf("running %d, third %v", running, taskIn(app, c)["state"])
	}
	states, revs := map[string]string{}, map[string]float64{}
	maxRunning, outOfOrder := 0, false
	deadline := time.Now().Add(120 * time.Second)
	for !(states[a] == stateDone && states[b] == stateDone && states[c] == stateDone) {
		name, m := em.next(t, deadline)
		task, _ := m["task"].(map[string]any)
		if name != "task" || task == nil {
			continue
		}
		tid := task["id"].(string)
		if r, ok := revs[tid]; ok && task["rev"].(float64) <= r {
			outOfOrder = true
		}
		revs[tid] = task["rev"].(float64)
		states[tid] = task["state"].(string)
		if states[tid] == stateError {
			t.Fatalf("error: %v", task["error"])
		}
		n := 0
		for _, st := range states {
			if st == stateRunning {
				n++
			}
		}
		maxRunning = max(maxRunning, n)
	}
	if maxRunning > parallelDownloads || outOfOrder {
		t.Errorf("max running %d, revisions out of order %v", maxRunning, outOfOrder)
	}

	// 2) Pause in the middle: the part stays on disk and nothing more is downloaded.
	p := id(app.Download(0, "v720"))
	for {
		task := em.waitTask(t, p, stateRunning, 60*time.Second)
		if task["percent"].(float64) >= 30 && task["stage"] == stageVideo {
			break
		}
	}
	app.Pause(p)
	paused := em.waitTask(t, p, statePaused, 30*time.Second)
	pct := paused["percent"].(float64)
	if pct < 25 || pct >= 100 {
		t.Errorf("progress kept when paused: %v", pct)
	}
	job := filepath.Join(save, "Video", tempFolderName, p)
	part, partSize := findPart(job)
	if part == "" || partSize == 0 {
		t.Fatalf("partial file not kept in %s", job)
	}
	time.Sleep(400 * time.Millisecond)
	if processesUsing(tmp) {
		t.Error("the download program did not stop")
	}
	time.Sleep(1200 * time.Millisecond)
	if _, s := findPart(job); s != partSize {
		t.Errorf("downloaded while paused: %d → %d", partSize, s)
	}
	app.Shutdown()

	// 3) The app is closed and opened again (even right in the middle of a download).
	list := filepath.Join(tmp, "data", "downloads.json")
	saved, _ := os.ReadFile(list)
	if !strings.Contains(string(saved), `"`+p+`"`) {
		t.Fatal("the list is not on disk")
	}
	os.WriteFile(list, []byte(strings.ReplaceAll(string(saved), `"state":"paused"`, `"state":"running"`)), 0o644)
	em2 := newRec()
	app2 := NewApp(em2, newPaths(filepath.Join(tmp, "data")))
	back := taskIn(app2, p)
	if back == nil || back["state"] != statePaused || back["percent"].(float64)-pct > 0.5 || pct-back["percent"].(float64) > 0.5 {
		t.Fatalf("interrupted download should come back paused with its progress: %v", back)
	}
	if ta := taskIn(app2, a); ta["state"] != stateDone || !strings.HasPrefix(fmt.Sprint(ta["thumb"]), "https://") {
		t.Errorf("finished one after restart: %v %v", ta["state"], ta["thumb"])
	}

	// 4) Resume continues from the last byte instead of starting over.
	srv.reset()
	app2.Resume(p)
	res := em2.waitTask(t, p, stateDone, 90*time.Second)["result"].(map[string]any)
	srv.mu.Lock()
	first := ""
	for _, r := range srv.served {
		if strings.HasPrefix(r, "v720_avc.mp4 ") {
			first = r
			break
		}
	}
	sent := srv.sent["v720_avc.mp4"]
	srv.mu.Unlock()
	if !strings.HasPrefix(first, fmt.Sprintf("v720_avc.mp4 bytes=%d-", partSize)) {
		t.Errorf("resumed with %q, part was %d bytes", first, partSize)
	}
	full := int64(3020458)
	if sent > full-partSize+65536 {
		t.Errorf("downloaded %d of %d again, had %d", sent, full, partSize)
	}
	if got := ffprobe(t, res["path"].(string), "stream=codec_name"); got != "h264,aac" {
		t.Errorf("resumed file streams %v", got)
	}
	if _, err := os.Stat(job); err == nil {
		t.Error("temporary folder left after finishing")
	}

	// 5) Pausing a waiting download, adding one that is already there, deleting a paused one.
	app2.Fetch("https://youtu.be/TESTVID0001")
	em2.waitFor(t, "info", "done", 60*time.Second)
	r1, r2, r3 := id(app2.Download(0, "v1080")), id(app2.Download(0, "v360")), id(app2.Download(0, "mp3-128"))
	time.Sleep(300 * time.Millisecond)
	if st := taskIn(app2, r3)["state"]; st != stateQueued {
		t.Errorf("third should wait: %v (first %v %v, second %v %v)", st, taskIn(app2, r1)["state"], taskIn(app2, r1)["error"],
			taskIn(app2, r2)["state"], taskIn(app2, r2)["error"])
	}
	app2.Pause(r3)
	if st := taskIn(app2, r3)["state"]; st != statePaused {
		t.Errorf("a waiting download pauses at once: %v", st)
	}
	app2.Pause(r1)
	em2.waitTask(t, r1, statePaused, 30*time.Second)
	dup := app2.Download(0, "v1080")
	if dup["id"] != r1 || dup["existing"] != true {
		t.Errorf("same video and quality added twice: %v", dup)
	}
	time.Sleep(100 * time.Millisecond)
	if st := taskIn(app2, r1)["state"]; st != stateQueued && st != stateRunning {
		t.Errorf("adding it again should resume it: %v", st)
	}
	app2.Delete(r1)
	em2.waitTask(t, r1, "removed", 30*time.Second)
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(save, "Video", tempFolderName, r1)); err == nil {
		t.Error("deleted download left its folder")
	}
	if taskIn(app2, r1) != nil {
		t.Error("deleted download still in the list")
	}
	app2.Resume(r3)
	em2.waitTask(t, r3, stateDone, 90*time.Second)
	if st := taskIn(app2, r2)["state"]; st != stateDone {
		em2.waitTask(t, r2, stateDone, 90*time.Second)
	}
	time.Sleep(300 * time.Millisecond)
	if app2.Busy() || processesUsing(tmp) {
		t.Error("not idle at the end")
	}
	app2.Shutdown()
}

// A download whose folder is full stops with a clear message and continues in another
// folder chosen by the user.
func TestEndToEndDiskAndMove(t *testing.T) {
	if _, err := os.Stat(e2eMedia); err != nil {
		t.Skip("local media not prepared")
	}
	srv := newMediaServer(e2eMedia, 3_000_000)
	defer srv.Close()
	b, err := os.ReadFile(e2eInfo)
	if err != nil {
		t.Skip("local media not prepared")
	}
	// Pretend the 720p file is huge so the space check fails.
	huge := strings.Replace(string(b), `"filesize": 3020458`, `"filesize": 9000000000000000`, 1)
	if huge == string(b) {
		t.Skip("fixture changed")
	}
	app, em, tmp, _ := e2eApp(t, srv, []byte(huge))
	app.Fetch("https://youtu.be/TESTVID0001")
	em.waitFor(t, "info", "done", 60*time.Second)
	id, _ := app.Download(0, "v720")["id"].(string)
	var task map[string]any
	for deadline := time.Now().Add(30 * time.Second); ; {
		name, m := em.next(t, deadline)
		if tk, _ := m["task"].(map[string]any); name == "task" && tk != nil && tk["id"] == id && tk["state"] == stateError {
			task = tk
			break
		}
	}
	e := task["error"].(map[string]any)
	if e["code"] != "disk" || e["action"] != "chooseFolder" || !strings.Contains(e["message"].(string), "«\u2068التنزيلات ← HELINK \u200f← Video\u2069»") {
		t.Fatalf("disk error %v", e)
	}
	// Another folder: the setting changes and the download continues there, in its
	// Video folder (still too big here, so it fails again, now naming the new folder).
	other := filepath.Join(tmp, "Elsewhere")
	os.MkdirAll(other, 0o755)
	if r := app.SetSaveDir(other); r.Error != "" {
		t.Fatal(r.Error)
	}
	app.MoveTaskToSaveDir(id)
	for deadline := time.Now().Add(30 * time.Second); ; {
		name, m := em.next(t, deadline)
		if tk, _ := m["task"].(map[string]any); name == "task" && tk != nil && tk["id"] == id && tk["state"] == stateError {
			task = tk
			break
		}
	}
	if msg := task["error"].(map[string]any)["message"].(string); !strings.Contains(msg, "«\u2068Elsewhere \u200f← Video\u2069»") {
		t.Errorf("message should name the new folder: %s", msg)
	}
	app.Shutdown()
	saved, _ := os.ReadFile(filepath.Join(tmp, "data", "downloads.json"))
	if !strings.Contains(string(saved), `"saveDir":"`+filepath.Join(other, "Video")+`"`) {
		t.Errorf("new folder not saved with the download: %s", saved)
	}
}
