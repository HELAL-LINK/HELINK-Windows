//go:build !windows

package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewerVersion(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"2.3.1", "2.3.0", true}, {"2.3.0", "2.3.0", false}, {"2.2.9", "2.3.0", false}, {"2.10.0", "2.9.9", true},
		{"3", "2.99.99", true}, {"2.3", "2.3.0", false}, {"2.3.0.1", "2.3.0", true}, {"v2.4.0", "2.3.0", true},
		{"2.x", "2.3.0", false}, {"", "2.3.0", false}, {"-1.0", "0.1", false},
	} {
		if got := newerVersion(c.a, c.b); got != c.want {
			t.Errorf("%q after %q: %v", c.a, c.b, got)
		}
	}
}

// testKey makes a key pair for the test and has HELINK trust it instead of the real one.
func testKey(t *testing.T) ed25519.PrivateKey {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	old := updatePublicKey
	updatePublicKey = pub
	t.Cleanup(func() { updatePublicKey = old })
	return priv
}

func sign(priv ed25519.PrivateKey, m any) []byte {
	body, _ := json.Marshal(m)
	b, _ := json.Marshal(signedManifest{Manifest: base64.StdEncoding.EncodeToString(body),
		Sig: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, body))})
	return b
}

func fileOf(data []byte, urls ...string) *updateFile {
	h := sha256.Sum256(data)
	return &updateFile{URLs: urls, Size: int64(len(data)), SHA256: hex.EncodeToString(h[:])}
}

func TestParseSignedManifest(t *testing.T) {
	// HELINK's own key is a valid Ed25519 public key.
	if len(updatePublicKey) != ed25519.PublicKeySize {
		t.Fatalf("HELINK's key: %d bytes", len(updatePublicKey))
	}
	priv := testKey(t)
	good := updateManifest{App: updateAppID, Version: "2.99.1", Notes: map[string][]string{"ar": {"جديد"}},
		Setup: fileOf([]byte("x"), "https://example.com/s.exe"), Portable: fileOf([]byte("y"), "https://example.com/p.zip")}
	m, err := parseSignedManifest(sign(priv, good), updatePublicKey)
	if err != nil || m.Version != "2.99.1" || m.Setup.Size != 1 || m.Notes["ar"][0] != "جديد" {
		t.Fatalf("good: %v %+v", err, m)
	}
	// Changed after signing.
	var s signedManifest
	json.Unmarshal(sign(priv, good), &s)
	body, _ := base64.StdEncoding.DecodeString(s.Manifest)
	s.Manifest = base64.StdEncoding.EncodeToString(bytes.Replace(body, []byte("2.99.1"), []byte("9.9.9"), 1))
	tampered, _ := json.Marshal(s)
	if _, err := parseSignedManifest(tampered, updatePublicKey); !errors.Is(err, errBadManifest) {
		t.Errorf("tampered: %v", err)
	}
	// Signed with another key.
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := parseSignedManifest(sign(other, good), updatePublicKey); !errors.Is(err, errBadManifest) {
		t.Errorf("other key: %v", err)
	}
	// Signed, but wrong: another program, a plain http address, a file without its hash.
	bad := []updateManifest{good, good, good, good}
	bad[0].App = "helink-android"
	bad[1].Setup = fileOf([]byte("x"), "http://example.com/s.exe")
	bad[2].Portable = &updateFile{URLs: []string{"https://example.com/p.zip"}, Size: 1}
	bad[3].Version = "next"
	for i, b := range bad {
		if _, err := parseSignedManifest(sign(priv, b), updatePublicKey); !errors.Is(err, errBadManifest) {
			t.Errorf("bad %d: %v", i, err)
		}
	}
	for _, junk := range []string{"", "{}", "<html>404</html>", `{"manifest":"!!","sig":"??"}`} {
		if _, err := parseSignedManifest([]byte(junk), updatePublicKey); err == nil {
			t.Errorf("junk %q accepted", junk)
		}
	}
}

// updServer serves latest.json and the files of a version, over HTTPS.
type updServer struct {
	*httptest.Server
	mu       sync.Mutex
	manifest []byte
	files    map[string][]byte
	fail     int32 // answer 503 to this many requests for latest.json first
	hits     int32
}

func newUpdServer(t *testing.T) *updServer {
	s := &updServer{files: map[string][]byte{}}
	s.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if r.URL.Path == "/latest.json" {
			atomic.AddInt32(&s.hits, 1)
			if atomic.LoadInt32(&s.fail) > 0 {
				atomic.AddInt32(&s.fail, -1)
				http.Error(w, "busy", http.StatusServiceUnavailable)
				return
			}
			if s.manifest == nil {
				http.NotFound(w, r)
				return
			}
			w.Write(s.manifest)
			return
		}
		b, ok := s.files[strings.TrimPrefix(r.URL.Path, "/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(b))
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *updServer) set(manifest []byte, files map[string][]byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.manifest = manifest
	for k, v := range files {
		s.files[k] = v
	}
}

// updateApp is an app that updates from srv, its program being <tmp>/app/HELINK.exe.
func updateApp(t *testing.T, srv *updServer, kind string) (app *App, em *recEmitter, exe, data string) {
	t.Helper()
	tmp := t.TempDir()
	exe = filepath.Join(tmp, "app", "HELINK.exe")
	os.MkdirAll(filepath.Dir(exe), 0o755)
	os.WriteFile(exe, []byte("MZ the old HELINK"), 0o755)
	t.Setenv("HELINK_TEST_EXE", exe)
	t.Setenv("HELINK_TEST_UPDATE_KIND", kind)
	t.Setenv("NAZZELHA_SAVE", filepath.Join(tmp, "Downloads"))
	oldURLs := updateManifestURLs
	updateManifestURLs = []string{srv.URL + "/latest.json"}
	t.Cleanup(func() { updateManifestURLs = oldURLs })
	updateLaunchMu.Lock()
	updateLaunched = nil
	updateLaunchMu.Unlock()
	data = filepath.Join(tmp, "data")
	em = newRec()
	app = NewApp(em, newPaths(data))
	app.tools.client = srv.Client()
	return app, em, exe, data
}

func portableZip(exe []byte) []byte {
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	w, _ := z.Create("HELINK-2.99.1-portable/طريقة التثبيت والاستخدام.txt")
	w.Write([]byte("guide"))
	w, _ = z.Create("HELINK-2.99.1-portable/HELINK.exe")
	w.Write(exe)
	z.Close()
	return b.Bytes()
}

// waitUpdate waits for an "update" event in state (fails on another error).
func waitUpdate(t *testing.T, em *recEmitter, state string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		name, m := em.next(t, deadline)
		if name != "update" {
			continue
		}
		if m["state"] == state {
			return m
		}
		if m["state"] == "error" && state != "error" {
			t.Fatalf("update failed: %v", m["error"])
		}
	}
}

func lastLaunched() string {
	updateLaunchMu.Lock()
	defer updateLaunchMu.Unlock()
	if len(updateLaunched) == 0 {
		return ""
	}
	return updateLaunched[len(updateLaunched)-1]
}

// The portable copy: told about 2.99.1, "Not now", then "Update now": its HELINK.exe is
// replaced, the new one started, this one closed; the new one continues the downloads.
func TestUpdatePortable(t *testing.T) {
	defer setLang("ar")
	setLang("ar")
	priv := testKey(t)
	srv := newUpdServer(t)
	newExe := []byte("MZ the new HELINK, version 2.99.1")
	zipData := portableZip(newExe)
	setupData := []byte("MZ the installer")
	m := updateManifest{App: updateAppID, Version: "2.99.1", Date: "2026-11-01",
		Notes: map[string][]string{"ar": {"تنزيل الكورسات كاملة"}, "en": {"Whole courses"}},
		Setup: fileOf(setupData, srv.URL+"/files/HELINK-2.99.1-Setup.exe"), Portable: fileOf(zipData, srv.URL+"/files/HELINK-2.99.1-portable.zip")}
	srv.set(sign(priv, m), map[string][]byte{"files/HELINK-2.99.1-Setup.exe": setupData, "files/HELINK-2.99.1-portable.zip": zipData})
	app, em, exe, data := updateApp(t, srv, "portable")

	// Nothing known before GitHub answered.
	if u := app.UpdateInfo(); u["state"] != "" || u["version"] != nil || u["current"] != appVersion {
		t.Errorf("before: %v", u)
	}
	if err := app.checkUpdate(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	waitUpdate(t, em, "checking")
	ev := waitUpdate(t, em, "available")
	if ev["version"] != "2.99.1" || ev["prompt"] != true || ev["sizeText"] != HumanSize(int64(len(zipData))) ||
		jsonOf(ev["notes"]) != `["تنزيل الكورسات كاملة"]` {
		t.Fatalf("available: %v", ev)
	}
	setLang("en")
	if u := app.UpdateInfo(); jsonOf(u["notes"]) != `["Whole courses"]` {
		t.Errorf("notes in English: %v", u["notes"])
	}
	setLang("ar")
	// Shown once while HELINK runs; "Not now" keeps it quiet for a day.
	if app.shouldPrompt("2.99.1") {
		t.Error("prompted twice")
	}
	app.UpdateLater()
	if st := app.store.Get(); st.UpdateSnoozed != "2.99.1" || time.Until(st.UpdateSnoozeUntil) < 23*time.Hour {
		t.Errorf("snooze %q %v", st.UpdateSnoozed, st.UpdateSnoozeUntil)
	}
	if u := waitUpdate(t, em, "available"); u["snoozed"] != true {
		t.Errorf("snoozed: %v", u)
	}
	app.upd.prompted = ""
	if app.shouldPrompt("2.99.1") {
		t.Error("prompted although snoozed")
	}
	if !app.shouldPrompt("2.3.2") {
		t.Error("a newer version than the one snoozed is offered")
	}

	// A download in the list when the update starts: it continues in the new version.
	task := &DownloadTask{ID: "dtest1", URL: "https://www.youtube.com/watch?v=aaaaaaaaaa1", Site: siteYouTube, Kind: "video", Key: "v720",
		Label: "720p", Base: "test", SaveDir: filepath.Join(filepath.Dir(data), "Downloads"), State: stateQueued, ETA: -1, FormatSpec: "bv*+ba/b"}
	app.mu.Lock()
	app.tasks = append(app.tasks, task)
	app.mu.Unlock()
	app.saveTasks(true)

	quit := make(chan struct{})
	app.Quit = func() { close(quit) }
	if r := app.UpdateNow(); r["ok"] != true {
		t.Fatalf("update now: %v", r)
	}
	waitUpdate(t, em, "downloading")
	waitUpdate(t, em, "installing")
	select {
	case <-quit:
	case <-time.After(15 * time.Second):
		t.Fatal("HELINK did not close for the update")
	}
	if b, _ := os.ReadFile(exe); !bytes.Equal(b, newExe) {
		t.Errorf("HELINK.exe: %q", b)
	}
	if b, _ := os.ReadFile(exe + ".old"); string(b) != "MZ the old HELINK" {
		t.Errorf("HELINK.exe.old: %q", b)
	}
	if l := lastLaunched(); l != exe+" --updated" {
		t.Errorf("started %q", l)
	}
	if st := app.store.Get(); jsonOf(st.ResumeAfterUpdate) != `["dtest1"]` || st.UpdatingTo != "2.99.1" || st.UpdatingFrom != appVersion {
		t.Errorf("for the new version: %+v", st)
	}
	app.Shutdown()

	// The new HELINK opens: it says so, continues the download, cleans up.
	old := appVersion
	appVersion = "2.99.1"
	defer func() { appVersion = old }()
	em2 := newRec()
	app2 := NewApp(em2, newPaths(data))
	r := app2.Init()
	if r.Updated != "2.99.1" || r.UpdateFailed {
		t.Errorf("init after the update: updated %q failed %v", r.Updated, r.UpdateFailed)
	}
	if r2 := app2.Init(); r2.Updated != "" {
		t.Error("said twice")
	}
	app2.mu.Lock()
	st := ""
	for _, x := range app2.tasks {
		if x.ID == "dtest1" {
			st = x.State
		}
	}
	app2.mu.Unlock()
	if st == statePaused || st == "" {
		t.Errorf("the download did not continue: %q", st)
	}
	if _, err := os.Stat(exe + ".old"); err == nil {
		t.Error("HELINK.exe.old left behind")
	}
	if _, err := os.Stat(filepath.Join(data, "update")); err == nil {
		t.Error("the downloaded update left behind")
	}
	if s := app2.store.Get(); s.UpdatingTo != "" || s.ResumeAfterUpdate != nil || s.UpdateSnoozed != "" {
		t.Errorf("settings after: %+v", s)
	}
	app2.Shutdown()
}

// The installed copy: a file that is not what latest.json says is refused; the right one is
// started quietly, to put the new version in place and open it.
func TestUpdateInstalled(t *testing.T) {
	priv := testKey(t)
	srv := newUpdServer(t)
	setupData := []byte("MZ the installer of 2.99.1")
	m := updateManifest{App: updateAppID, Version: "2.99.1", Setup: fileOf(setupData, srv.URL+"/HELINK-2.99.1-Setup.exe"),
		Portable: fileOf([]byte("zip"), srv.URL+"/HELINK-2.99.1-portable.zip")}
	srv.set(sign(priv, m), map[string][]byte{"HELINK-2.99.1-Setup.exe": []byte("MZ something else entirely!")})
	app, em, _, data := updateApp(t, srv, "setup")
	if err := app.checkUpdate(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if ev := waitUpdate(t, em, "available"); ev["sizeText"] != HumanSize(int64(len(setupData))) {
		t.Errorf("size of the installer: %v", ev)
	}
	app.Quit = func() { t.Error("closed for a bad update") }
	app.UpdateNow()
	ev := waitUpdate(t, em, "error")
	if e := ev["error"].(map[string]any); e["code"] != "update_verify" || ev["failedIn"] != "install" {
		t.Errorf("bad file: %v", ev)
	}
	if _, err := os.Stat(filepath.Join(data, "update", "HELINK-2.99.1-Setup.exe")); err == nil {
		t.Error("the bad file was kept")
	}
	if l := lastLaunched(); l != "" {
		t.Errorf("started %q", l)
	}
	if st := app.store.Get(); st.UpdatingTo != "" {
		t.Errorf("updating to %q", st.UpdatingTo)
	}

	srv.set(nil, map[string][]byte{"HELINK-2.99.1-Setup.exe": setupData})
	srv.set(sign(priv, m), nil)
	quit := make(chan struct{})
	app.Quit = func() { close(quit) }
	app.UpdateNow()
	select {
	case <-quit:
	case <-time.After(15 * time.Second):
		t.Fatal("HELINK did not close for the update")
	}
	if l := lastLaunched(); l != filepath.Join(data, "update", "HELINK-2.99.1-Setup.exe")+" /S /RELAUNCH" {
		t.Errorf("started %q", l)
	}
	app.Shutdown()

	// The installer did not replace it (this is still the old version): said once, the
	// downloads continue anyway.
	app2 := NewApp(newRec(), newPaths(data))
	if r := app2.Init(); r.Updated != "" || !r.UpdateFailed {
		t.Errorf("after a failed update: %q %v", r.Updated, r.UpdateFailed)
	}
	app2.Shutdown()
}

func TestUpdateCheckFailures(t *testing.T) {
	priv := testKey(t)
	srv := newUpdServer(t)
	app, em, _, _ := updateApp(t, srv, "portable")
	defer app.Shutdown()

	// No latest.json yet (the page exists, the file not): said when asked, not a network trouble.
	err := app.checkUpdate(context.Background(), true)
	if err == nil || isNetworkTrouble(err) {
		t.Errorf("404: %v", err)
	}
	if ev := waitUpdate(t, em, "error"); ev["error"].(map[string]any)["code"] != "update_check" || ev["failedIn"] != "check" {
		t.Errorf("404 event: %v", ev)
	}
	// A forged answer.
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	srv.set(sign(other, updateManifest{App: updateAppID, Version: "9.0.0"}), nil)
	err = app.checkUpdate(context.Background(), true)
	if !errors.Is(err, errBadManifest) || isNetworkTrouble(err) {
		t.Errorf("forged: %v", err)
	}
	if ev := waitUpdate(t, em, "error"); ev["error"].(map[string]any)["code"] != "update_verify" {
		t.Errorf("forged event: %v", ev)
	}
	// The same version: nothing to do.
	srv.set(sign(priv, updateManifest{App: updateAppID, Version: appVersion}), nil)
	if err := app.checkUpdate(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if ev := waitUpdate(t, em, "latest"); ev["version"] != nil {
		t.Errorf("latest: %v", ev)
	}
	if r := app.UpdateNow(); r["error"] == nil {
		t.Error("nothing to update to")
	}
	// GitHub unreachable, its copy reachable.
	updateManifestURLs = []string{"https://127.0.0.1:1/latest.json", srv.URL + "/latest.json"}
	srv.set(sign(priv, updateManifest{App: updateAppID, Version: "2.9.0"}), nil)
	if err := app.checkUpdate(context.Background(), false); err != nil {
		t.Fatalf("through the copy: %v", err)
	}
	if ev := waitUpdate(t, em, "available"); ev["version"] != "2.9.0" {
		t.Errorf("copy: %v", ev)
	}
	// No internet at all.
	updateManifestURLs = []string{"https://127.0.0.1:1/latest.json"}
	if err := app.checkUpdate(context.Background(), false); !isNetworkTrouble(err) {
		t.Errorf("offline: %v", err)
	}
	if u := app.UpdateInfo(); u["state"] != "available" {
		t.Errorf("a known update stays offered while offline: %v", u["state"])
	}
}

// The checks in the background: GitHub busy at first, then the update is offered by itself.
func TestUpdateLoop(t *testing.T) {
	priv := testKey(t)
	srv := newUpdServer(t)
	srv.set(sign(priv, updateManifest{App: updateAppID, Version: "2.4.0", Portable: fileOf([]byte("z"), srv.URL+"/p.zip")}), nil)
	atomic.StoreInt32(&srv.fail, 2)
	saved := []time.Duration{updateFirstDelay, updateTick, updateRetryOffline, updateRetryLater}
	updateFirstDelay, updateTick, updateRetryOffline, updateRetryLater = 30*time.Millisecond, 20*time.Millisecond, 60*time.Millisecond, 60*time.Millisecond
	defer func() { updateFirstDelay, updateTick, updateRetryOffline, updateRetryLater = saved[0], saved[1], saved[2], saved[3] }()
	app, em, _, _ := updateApp(t, srv, "portable")
	app.StartUpdates()
	app.StartUpdates() // once is enough
	ev := waitUpdate(t, em, "available")
	if ev["version"] != "2.4.0" || ev["prompt"] != true {
		t.Errorf("offered: %v", ev)
	}
	if n := atomic.LoadInt32(&srv.hits); n != 3 {
		t.Errorf("asked %d times, want 3 (two busy answers, then the file)", n)
	}
	if st := app.store.Get(); st.UpdateSeen != "2.4.0" || time.Since(st.UpdateChecked) > time.Minute {
		t.Errorf("remembered %+v", st)
	}
	app.Shutdown()
}

func TestSwapPortableExe(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "HELINK.exe")
	os.WriteFile(exe, []byte("MZ old"), 0o755)
	zipPath := filepath.Join(dir, "u.zip")
	// Not a program inside: refused, nothing changed.
	os.WriteFile(zipPath, portableZip([]byte("<html>not a program</html>")), 0o644)
	if err := swapPortableExe(exe, zipPath, func(string) error { return nil }); err == nil {
		t.Error("a zip without a program was accepted")
	}
	// The new one does not start: the old one is put back.
	os.WriteFile(zipPath, portableZip([]byte("MZ new")), 0o644)
	if err := swapPortableExe(exe, zipPath, func(string) error { return errors.New("blocked") }); err == nil {
		t.Error("a failed start was ignored")
	}
	if b, _ := os.ReadFile(exe); string(b) != "MZ old" {
		t.Errorf("after a failed start: %q", b)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 2 {
		t.Errorf("left behind: %v", entries)
	}
	var started string
	if err := swapPortableExe(exe, zipPath, func(p string) error { started = p; return nil }); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "MZ new" || started != exe {
		t.Errorf("after the swap: %q, started %q", b, started)
	}
}
