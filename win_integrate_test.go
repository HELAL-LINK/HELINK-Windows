//go:build windows

package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows/registry"

	"nazzelha/ipc"
)

func TestWinRegistration(t *testing.T) {
	data := t.TempDir()
	host := filepath.Join(data, "host", "helink-host.exe")
	registerHosts(data, host)
	for _, base := range append(append([]string{}, chromiumHostKeys...), firefoxHostKeys...) {
		k, err := registry.OpenKey(registry.CURRENT_USER, base+`\`+nmHostName, registry.QUERY_VALUE)
		if err != nil {
			t.Fatalf("%s: %v", base, err)
		}
		v, _, err := k.GetStringValue("")
		k.Close()
		want := filepath.Join(data, "nm", "chrome.json")
		if strings.Contains(base, "Mozilla") || strings.Contains(base, "Waterfox") || strings.Contains(base, "LibreWolf") {
			want = filepath.Join(data, "nm", "firefox.json")
		}
		if err != nil || v != want {
			t.Errorf("%s = %q %v", base, v, err)
		}
	}
	var chrome, firefox map[string]any
	b, _ := os.ReadFile(filepath.Join(data, "nm", "chrome.json"))
	json.Unmarshal(b, &chrome)
	b, _ = os.ReadFile(filepath.Join(data, "nm", "firefox.json"))
	json.Unmarshal(b, &firefox)
	if chrome["name"] != nmHostName || chrome["path"] != host || chrome["type"] != "stdio" ||
		chrome["allowed_origins"].([]any)[0] != "chrome-extension://bmkpbbkgkcbhaidhefjkhanimkfaefon/" {
		t.Errorf("chrome manifest %v", chrome)
	}
	if firefox["allowed_extensions"].([]any)[0] != firefoxAddonID || firefox["path"] != host {
		t.Errorf("firefox manifest %v", firefox)
	}

	exe := `C:\Program Files\HELINK\HELINK.exe`
	setAutostart(exe, true)
	k, _ := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Run`, registry.QUERY_VALUE)
	v, _, err := k.GetStringValue(runValueName)
	k.Close()
	if err != nil || v != `"C:\Program Files\HELINK\HELINK.exe" --background` {
		t.Errorf("autostart %q %v", v, err)
	}
	setAutostart(exe, false)
	k, _ = registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Run`, registry.QUERY_VALUE)
	_, _, err = k.GetStringValue(runValueName)
	k.Close()
	if err == nil {
		t.Error("autostart still set")
	}

	// The uninstaller undoes it all, and leaves the user's own files.
	setAutostart(exe, true)
	_ = os.WriteFile(filepath.Join(data, "settings.json"), []byte("{}"), 0o644)
	unregister(data)
	for _, base := range append(append([]string{}, chromiumHostKeys...), firefoxHostKeys...) {
		if k, err := registry.OpenKey(registry.CURRENT_USER, base+`\`+nmHostName, registry.QUERY_VALUE); err == nil {
			k.Close()
			t.Errorf("%s still registered", base)
		}
	}
	k, _ = registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Run`, registry.QUERY_VALUE)
	_, _, err = k.GetStringValue(runValueName)
	k.Close()
	if err == nil {
		t.Error("autostart still set after unregister")
	}
	if pathExists(filepath.Join(data, "nm")) || !pathExists(filepath.Join(data, "settings.json")) {
		t.Error("unregister: wrong files removed")
	}
}

func TestWinExtensionFiles(t *testing.T) {
	data := t.TempDir()
	old := extChromiumZip
	defer func() { extChromiumZip = old }()
	extChromiumZip = zipOf(t, map[string][]byte{"manifest.json": []byte(`{"v":1}`), "js/old.js": []byte("old")})
	extractExtension(data)
	dir := chromiumExtFolder(data)
	if b, _ := os.ReadFile(filepath.Join(dir, "js", "old.js")); string(b) != "old" {
		t.Fatalf("not unpacked: %q", b)
	}
	// A new version replaces the files and removes the ones it no longer has.
	extChromiumZip = zipOf(t, map[string][]byte{"manifest.json": []byte(`{"v":2}`), "js/new.js": []byte("new")})
	_ = os.WriteFile(filepath.Join(dir, ".helink-version"), []byte("0.9"), 0o644)
	extractExtension(data)
	if b, _ := os.ReadFile(filepath.Join(dir, "manifest.json")); string(b) != `{"v":2}` {
		t.Errorf("manifest %q", b)
	}
	if pathExists(filepath.Join(dir, "js", "old.js")) || !pathExists(filepath.Join(dir, "js", "new.js")) {
		t.Error("old files kept or new ones missing")
	}
}

func TestWinTrash(t *testing.T) {
	p := filepath.Join(t.TempDir(), "gone.txt")
	os.WriteFile(p, []byte("x"), 0o644)
	if err := moveToTrash(p); err != nil {
		t.Fatal(err)
	}
	if pathExists(p) {
		t.Error("file still there")
	}
}

// The browsers' helper, built for Windows (HELINK_HOST), talks to this process's HELINK.
func TestWinHost(t *testing.T) {
	hostExePath := os.Getenv("HELINK_HOST")
	if hostExePath == "" {
		t.Skip("HELINK_HOST not set")
	}
	data := t.TempDir()
	em := &chanEmitter{ch: make(chan string, 1000)}
	app := NewApp(em, newPaths(data))
	defer app.Shutdown()
	ln, err := app.ListenForBrowsers()
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	fs := newFileServer(512 << 10)
	srv := httptest.NewServer(fs)
	defer srv.Close()
	cmd := exec.Command(hostExePath, "chrome-extension://bmkpbbkgkcbhaidhefjkhanimkfaefon/", "--parent-window=0")
	cmd.Env = append(os.Environ(), "HELINK_DATA="+data)
	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Wait()
	defer in.Close()
	ask := func(m map[string]any) map[string]any {
		b, _ := json.Marshal(m)
		if err := ipc.WriteMessage(in, b); err != nil {
			t.Fatal(err)
		}
		done := make(chan []byte, 1)
		go func() {
			b, _ := ipc.ReadMessage(out)
			done <- b
		}()
		select {
		case b := <-done:
			var r map[string]any
			json.Unmarshal(b, &r)
			return r
		case <-time.After(60 * time.Second):
			t.Fatal("no answer")
			return nil
		}
	}
	if r := ask(map[string]any{"type": "ping"}); r["ok"] != true {
		t.Fatalf("ping %v", r)
	}
	if r := ask(map[string]any{"type": "hello"}); r["ok"] != true || r["running"] != true {
		t.Fatalf("hello %v", r)
	}
	if r := ask(map[string]any{"type": "download", "url": srv.URL + "/setup.exe", "filename": "setup.exe"}); r["ok"] != true {
		t.Fatalf("download %v", r)
	}
}

// The icon near the clock: Windows accepts its data (the size of the structure matters).
func TestWinTray(t *testing.T) {
	if unsafe.Sizeof(notifyIconData{}) != 976 {
		t.Fatalf("NOTIFYICONDATAW is %d bytes, Windows expects 976", unsafe.Sizeof(notifyIconData{}))
	}
	if unsafe.Sizeof(shFileOp{}) != 56 {
		t.Fatalf("SHFILEOPSTRUCTW is %d bytes, Windows expects 56", unsafe.Sizeof(shFileOp{}))
	}
	cls := utf16("STATIC")
	hwnd, _, _ := user32.NewProc("CreateWindowExW").Call(0, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(utf16("t"))),
		0, 0, 0, 10, 10, 0, 0, 0, 0)
	if hwnd == 0 {
		t.Skip("no window here")
	}
	tr := newTray(hwnd)
	n := tr.base()
	n.UFlags = nifTip
	copyUTF16(n.SzTip[:], "HELINK test")
	if r, _, _ := procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&n))); r == 0 {
		t.Error("Windows refused to change the icon")
	}
	tr.setTip("HELINK – 1 download, 50%")
	tr.balloon("اكتمل التنزيل", "ملف.zip")
	tr.remove()
}
