//go:build windows

package main

// HELINK in Windows: starting with Windows (hidden near the clock), the browsers' helper
// (helink-host.exe, registered for each browser so the extension can reach HELINK), and
// the extension's own files, ready to be added to the browsers.

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows/registry"
)

const (
	nmHostName     = "com.helaltech.helink"
	firefoxAddonID = "helink@helaltech.com"
	runValueName   = "HELINK"
)

// chromiumExtensionIDs are the extension's IDs in the Chromium browsers: the one its key
// gives when it is added from its folder, then the stores' (once it is published there:
// build.sh adds the IDs listed in extension/store-ids.txt through storeExtensionIDs).
var chromiumExtensionIDs = append([]string{"bmkpbbkgkcbhaidhefjkhanimkfaefon"}, storeIDs()...)

// storeExtensionIDs is set when HELINK is built: "id1,id2" (-ldflags -X main.storeExtensionIDs=…).
var storeExtensionIDs string

func storeIDs() []string {
	var ids []string
	for _, id := range strings.Split(storeExtensionIDs, ",") {
		if id = strings.TrimSpace(id); len(id) == 32 {
			ids = append(ids, id)
		}
	}
	return ids
}

// Where each browser looks for helpers (under HKEY_CURRENT_USER).
var (
	chromiumHostKeys = []string{
		`Software\Google\Chrome\NativeMessagingHosts`,
		`Software\Chromium\NativeMessagingHosts`,
		`Software\Microsoft\Edge\NativeMessagingHosts`,
		`Software\BraveSoftware\Brave-Browser\NativeMessagingHosts`,
		`Software\Vivaldi\NativeMessagingHosts`,
		`Software\Opera Software\NativeMessagingHosts`,
		`Software\Yandex\YandexBrowser\NativeMessagingHosts`,
	}
	firefoxHostKeys = []string{
		`Software\Mozilla\NativeMessagingHosts`,
		`Software\Waterfox\NativeMessagingHosts`,
		`Software\LibreWolf\NativeMessagingHosts`,
	}
)

func hostDir(data string) string { return filepath.Join(data, "host") }
func extDir(data string) string  { return filepath.Join(data, "extension") }

// chromiumExtFolder is the folder the Chromium browsers load the extension from.
func chromiumExtFolder(data string) string { return filepath.Join(extDir(data), "chromium") }

// firefoxXPI is the extension's package for Firefox.
func firefoxXPI(data string) string { return filepath.Join(extDir(data), "helink-firefox.xpi") }

// integrate puts the helper and the extension in place and registers them, every start
// (so they follow HELINK when it is moved or updated).
func integrate(paths Paths, s Settings) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("integration: %v", r)
		}
	}()
	exe, err := os.Executable()
	if err != nil {
		log.Printf("integration: %v", err)
		return
	}
	// Where HELINK is, for the helper to start it.
	b, _ := json.Marshal(map[string]string{"path": exe, "version": appVersion})
	writeIfChanged(filepath.Join(paths.Data, "app.json"), b)

	host := filepath.Join(hostDir(paths.Data), "helink-host.exe")
	if len(hostExe) > 0 {
		if err := writeIfChanged(host, hostExe); err != nil {
			// A browser may be running the old one right now: it is replaced next time.
			log.Printf("helper: %v", err)
		}
	}
	registerHosts(paths.Data, host)
	setAutostart(exe, !s.NoAutostart)
	extractExtension(paths.Data)
}

// writeIfChanged writes a file only when its content differs (through a temporary file).
func writeIfChanged(p string, b []byte) error {
	if old, err := os.ReadFile(p); err == nil && bytes.Equal(old, b) {
		return nil
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	tmp := p + ".new"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// registerHosts writes the helper's manifests and tells every browser where they are.
func registerHosts(data, host string) {
	var origins []string
	for _, id := range chromiumExtensionIDs {
		origins = append(origins, "chrome-extension://"+id+"/")
	}
	desc := "HELINK download manager"
	chrome, _ := json.MarshalIndent(map[string]any{"name": nmHostName, "description": desc, "path": host,
		"type": "stdio", "allowed_origins": origins}, "", "  ")
	firefox, _ := json.MarshalIndent(map[string]any{"name": nmHostName, "description": desc, "path": host,
		"type": "stdio", "allowed_extensions": []string{firefoxAddonID}}, "", "  ")
	dir := filepath.Join(data, "nm")
	cm, fm := filepath.Join(dir, "chrome.json"), filepath.Join(dir, "firefox.json")
	if err := writeIfChanged(cm, chrome); err != nil {
		log.Printf("helper manifest: %v", err)
	}
	if err := writeIfChanged(fm, firefox); err != nil {
		log.Printf("helper manifest: %v", err)
	}
	set := func(base, manifest string) {
		k, _, err := registry.CreateKey(registry.CURRENT_USER, base+`\`+nmHostName, registry.SET_VALUE|registry.QUERY_VALUE)
		if err != nil {
			log.Printf("registry %s: %v", base, err)
			return
		}
		defer k.Close()
		if v, _, err := k.GetStringValue(""); err != nil || v != manifest {
			_ = k.SetStringValue("", manifest)
		}
	}
	for _, k := range chromiumHostKeys {
		set(k, cm)
	}
	for _, k := range firefoxHostKeys {
		set(k, fm)
	}
}

// unregister undoes integrate (for the uninstaller): HELINK no longer starts with Windows,
// the browsers no longer find its helper, and its helper and extension files go.
func unregister(data string) {
	if k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Run`, registry.SET_VALUE); err == nil {
		_ = k.DeleteValue(runValueName)
		k.Close()
	}
	for _, base := range append(append([]string{}, chromiumHostKeys...), firefoxHostKeys...) {
		_ = registry.DeleteKey(registry.CURRENT_USER, base+`\`+nmHostName)
	}
	for _, p := range []string{hostDir(data), extDir(data), filepath.Join(data, "nm"), filepath.Join(data, "app.json"),
		filepath.Join(data, "ext.json"), filepath.Join(data, "seen.json")} {
		_ = os.RemoveAll(p)
	}
}

// setAutostart makes HELINK start with Windows, hidden near the clock (or not).
func setAutostart(exe string, on bool) {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Run`, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		log.Printf("autostart: %v", err)
		return
	}
	defer k.Close()
	if !on {
		_ = k.DeleteValue(runValueName)
		return
	}
	want := `"` + exe + `" --background`
	if v, _, err := k.GetStringValue(runValueName); err != nil || v != want {
		if err := k.SetStringValue(runValueName, want); err != nil {
			log.Printf("autostart: %v", err)
		}
	}
}

// extractExtension unpacks the extension for the Chromium browsers (they load it from
// that folder, and read it again from there after an update) and writes Firefox's package.
func extractExtension(data string) {
	if len(extChromiumZip) > 0 {
		dir := chromiumExtFolder(data)
		stamp := filepath.Join(dir, ".helink-version")
		if old, _ := os.ReadFile(stamp); string(old) != appVersion {
			if err := unzipTo(extChromiumZip, dir); err != nil {
				log.Printf("extension: %v", err)
			} else {
				_ = os.WriteFile(stamp, []byte(appVersion), 0o644)
			}
		}
	}
	if len(extFirefoxXPI) > 0 {
		if err := writeIfChanged(firefoxXPI(data), extFirefoxXPI); err != nil {
			log.Printf("firefox package: %v", err)
		}
	}
}

func unzipTo(z []byte, dir string) error {
	r, err := zip.NewReader(bytes.NewReader(z), int64(len(z)))
	if err != nil {
		return err
	}
	// Files of an older version that are not in this one go.
	keep := map[string]bool{".helink-version": true}
	for _, f := range r.File {
		keep[filepath.FromSlash(f.Name)] = true
	}
	for _, f := range r.File {
		name := filepath.FromSlash(f.Name)
		if strings.Contains(name, "..") {
			continue
		}
		p := filepath.Join(dir, name)
		if f.FileInfo().IsDir() {
			_ = os.MkdirAll(p, 0o755)
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return err
		}
		if err := writeIfChanged(p, b); err != nil {
			return err
		}
	}
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if rel, err := filepath.Rel(dir, p); err == nil && !keep[rel] {
			_ = os.Remove(p)
		}
		return nil
	})
	return nil
}

// ---------------------------------------------------------------------------
// The browsers on this computer (for "add HELINK to your browser")
// ---------------------------------------------------------------------------

type browserApp struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Kind    string `json:"kind"` // "chromium" or "firefox"
	Exe     string `json:"-"`
	Page    string `json:"page"`    // its extensions page
	Running bool   `json:"running"` // (not used yet)
}

var knownBrowsers = []struct {
	id, name, kind, exe, page string
	paths                     []string // %VAR%-relative places, when not in App Paths
}{
	{"chrome", "Google Chrome", "chromium", "chrome.exe", "chrome://extensions",
		[]string{`%ProgramFiles%\Google\Chrome\Application\chrome.exe`, `%ProgramFiles(x86)%\Google\Chrome\Application\chrome.exe`, `%LOCALAPPDATA%\Google\Chrome\Application\chrome.exe`}},
	{"edge", "Microsoft Edge", "chromium", "msedge.exe", "edge://extensions",
		[]string{`%ProgramFiles(x86)%\Microsoft\Edge\Application\msedge.exe`, `%ProgramFiles%\Microsoft\Edge\Application\msedge.exe`}},
	{"brave", "Brave", "chromium", "brave.exe", "brave://extensions",
		[]string{`%ProgramFiles%\BraveSoftware\Brave-Browser\Application\brave.exe`, `%LOCALAPPDATA%\BraveSoftware\Brave-Browser\Application\brave.exe`}},
	{"opera", "Opera", "chromium", "opera.exe", "opera://extensions",
		[]string{`%LOCALAPPDATA%\Programs\Opera\opera.exe`, `%LOCALAPPDATA%\Programs\Opera\launcher.exe`, `%ProgramFiles%\Opera\launcher.exe`}},
	{"operagx", "Opera GX", "chromium", "", "opera://extensions",
		[]string{`%LOCALAPPDATA%\Programs\Opera GX\opera.exe`, `%LOCALAPPDATA%\Programs\Opera GX\launcher.exe`}},
	{"vivaldi", "Vivaldi", "chromium", "vivaldi.exe", "vivaldi://extensions",
		[]string{`%LOCALAPPDATA%\Vivaldi\Application\vivaldi.exe`, `%ProgramFiles%\Vivaldi\Application\vivaldi.exe`}},
	{"yandex", "Yandex Browser", "chromium", "", "browser://extensions",
		[]string{`%LOCALAPPDATA%\Yandex\YandexBrowser\Application\browser.exe`}},
	{"firefox", "Mozilla Firefox", "firefox", "firefox.exe", "about:addons",
		[]string{`%ProgramFiles%\Mozilla Firefox\firefox.exe`, `%ProgramFiles(x86)%\Mozilla Firefox\firefox.exe`, `%LOCALAPPDATA%\Mozilla Firefox\firefox.exe`}},
}

func appPath(exe string) string {
	if exe == "" {
		return ""
	}
	for _, root := range []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE} {
		k, err := registry.OpenKey(root, `Software\Microsoft\Windows\CurrentVersion\App Paths\`+exe, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		v, _, err := k.GetStringValue("")
		k.Close()
		if v = strings.Trim(v, `"`); err == nil && fileOK(v) {
			return v
		}
	}
	return ""
}

// installedBrowsers lists the browsers found on this computer.
func installedBrowsers() []browserApp {
	var out []browserApp
	for _, b := range knownBrowsers {
		exe := appPath(b.exe)
		for _, p := range b.paths {
			if exe != "" {
				break
			}
			if p = os.ExpandEnv(strings.NewReplacer("%ProgramFiles%", "${ProgramFiles}", "%ProgramFiles(x86)%", "${ProgramFiles(x86)}",
				"%LOCALAPPDATA%", "${LOCALAPPDATA}").Replace(p)); fileOK(p) {
				exe = p
			}
		}
		if exe != "" {
			out = append(out, browserApp{ID: b.id, Name: b.name, Kind: b.kind, Exe: exe, Page: b.page})
		}
	}
	return out
}

// openBrowserPage opens a browser on its extensions page (a page that cannot be opened
// from a link: the browser is started with it).
func openBrowserPage(id, page string) bool {
	for _, b := range installedBrowsers() {
		if b.ID != id {
			continue
		}
		if page == "" {
			page = b.Page
		}
		cmd := exec.Command(b.Exe, page)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: false}
		if err := cmd.Start(); err != nil {
			log.Printf("opening %s: %v", b.Name, err)
			return false
		}
		_ = cmd.Process.Release()
		return true
	}
	return false
}
