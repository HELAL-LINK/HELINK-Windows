package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// HELINK updates itself from its page on GitHub (github.com/HELAL-LINK/HELINK-Windows).
// latest.json there names the newest version, what is new in it, and its two files (the
// installer, and HELINK.exe alone in a zip) with their sizes and SHA-256, signed with HELINK's
// key (Ed25519: only the public half is in the program, so a changed or forged file is
// ignored). HELINK reads it a little after it starts, again every 12 hours while it runs, and
// every few minutes while there is no internet. When a newer version is there it says so, and
// it updates only when asked: "Update now", or "Not now" (asked again a day later). The
// installed copy runs the new installer quietly; the portable copy puts the new HELINK.exe in
// place of its own. The new HELINK opens by itself and continues the downloads that were
// running.

// updateManifestURLs: GitHub itself, then a copy of the same repository on jsDelivr's network
// (for a network that cannot reach GitHub's files).
var updateManifestURLs = []string{
	"https://raw.githubusercontent.com/HELAL-LINK/HELINK-Windows/main/latest.json",
	"https://cdn.jsdelivr.net/gh/HELAL-LINK/HELINK-Windows@main/latest.json",
	// HELINK's first page, kept in case it comes back (its files are signed with the same key).
	"https://raw.githubusercontent.com/helink-updates/HELINK-Windows/main/latest.json",
}

// updatePageURL is where people download HELINK themselves.
const updatePageURL = "https://github.com/HELAL-LINK/HELINK-Windows"

// updateAppID is what a manifest for this program says it is for.
const updateAppID = "helink-windows"

// updatePublicKey checks latest.json's signature. The private half stays with whoever publishes
// HELINK (devtools/updsign).
var updatePublicKey = ed25519.PublicKey(mustBase64("zVqJUl6UkCskdHwrssheiCMOtoAbKql+6/3bozJX2NQ="))

// When HELINK looks (variables, so the tests can make it quick).
var (
	updateFirstDelay   = 20 * time.Second
	updateEvery        = 12 * time.Hour
	updateRetryOffline = 2 * time.Minute // while there is no internet
	updateRetryLater   = 15 * time.Minute
	updateSnooze       = 24 * time.Hour // "Not now"
	updateTick         = time.Minute    // how often the clock is looked at (sleep and wake-up included)
)

func mustBase64(s string) []byte {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

// updateFile is one file of a version: where to get it, and how to recognise it.
type updateFile struct {
	URLs   []string `json:"urls"`
	Size   int64    `json:"size"`
	SHA256 string   `json:"sha256"`
}

// updateManifest is what latest.json says (inside its signature).
type updateManifest struct {
	App      string              `json:"app"`
	Version  string              `json:"version"`
	Date     string              `json:"date,omitempty"`
	Notes    map[string][]string `json:"notes,omitempty"` // "ar", "en"
	Setup    *updateFile         `json:"setup,omitempty"`
	Portable *updateFile         `json:"portable,omitempty"`
}

// signedManifest is latest.json itself: the manifest's exact bytes (base64) and their signature.
type signedManifest struct {
	Manifest string `json:"manifest"`
	Sig      string `json:"sig"`
}

var errBadManifest = errors.New("update manifest: bad signature or content")

// parseSignedManifest checks the signature and the content.
func parseSignedManifest(data []byte, key ed25519.PublicKey) (*updateManifest, error) {
	var s signedManifest
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("%w: %v", errBadManifest, err)
	}
	body, err1 := base64.StdEncoding.DecodeString(s.Manifest)
	sig, err2 := base64.StdEncoding.DecodeString(s.Sig)
	if err1 != nil || err2 != nil || len(sig) != ed25519.SignatureSize || !ed25519.Verify(key, body, sig) {
		return nil, errBadManifest
	}
	var m updateManifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("%w: %v", errBadManifest, err)
	}
	if m.App != updateAppID || versionParts(m.Version) == nil {
		return nil, errBadManifest
	}
	for _, f := range []*updateFile{m.Setup, m.Portable} {
		if f == nil {
			continue
		}
		if len(f.URLs) == 0 || f.Size <= 0 || len(f.SHA256) != 64 {
			return nil, errBadManifest
		}
		for _, u := range f.URLs {
			if !strings.HasPrefix(u, "https://") {
				return nil, errBadManifest
			}
		}
	}
	return &m, nil
}

// versionParts reads "2.3.0" (nil when it is not a version).
func versionParts(v string) []int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if v == "" {
		return nil
	}
	var out []int
	for _, p := range strings.Split(v, ".") {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil
		}
		out = append(out, n)
	}
	return out
}

// newerVersion tells whether version a comes after version b ("2.10.0" after "2.9.1").
func newerVersion(a, b string) bool {
	pa, pb := versionParts(a), versionParts(b)
	if pa == nil || pb == nil {
		return false
	}
	for i := 0; i < max(len(pa), len(pb)); i++ {
		x, y := 0, 0
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			return x > y
		}
	}
	return false
}

// updateTargetInfo is the copy of HELINK that is updated: installed (the installer replaces
// it) or portable (its HELINK.exe is replaced), and its program file.
type updateTargetInfo struct {
	Kind string // "setup" | "portable"
	Exe  string
}

// updater is where the update stands.
type updater struct {
	mu        sync.Mutex
	phase     string // "" | "checking" | "available" | "downloading" | "installing" | "error" | "latest"
	latest    *updateManifest
	done      int64
	total     int64
	err       *UserError // with phase "error": what failed
	failedIn  string     // "check" or "install"
	checkedAt time.Time  // the last answer from GitHub
	running   bool       // the background checks were started
	cancel    context.CancelFunc
	wake      chan struct{}
	prompted  string // the version the window was asked to show (once per run)
}

func newUpdater() *updater { return &updater{wake: make(chan struct{}, 1)} }

// StartUpdates starts the background checks (the window's code calls it once).
func (a *App) StartUpdates() {
	u := a.upd
	u.mu.Lock()
	if u.running {
		u.mu.Unlock()
		return
	}
	u.running = true
	u.mu.Unlock()
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				log.Printf("update checks: %v", r)
			}
		}()
		a.updateLoop()
	}()
}

// updateLoop looks for a new version: soon after the start, then every 12 hours, and every few
// minutes while there is no internet. The wall clock decides (a computer that slept for a day
// looks again when it wakes).
func (a *App) updateLoop() {
	next := time.Now().Add(updateFirstDelay)
	tick := time.NewTicker(updateTick)
	defer tick.Stop()
	for {
		wait := time.Until(next)
		if wait < 0 {
			wait = 0
		}
		timer := time.NewTimer(min(wait, updateTick))
		select {
		case <-a.ctx.Done():
			timer.Stop()
			return
		case <-a.upd.wake:
			next = time.Now()
		case <-timer.C:
		case <-tick.C:
		}
		timer.Stop()
		if time.Now().Before(next) {
			continue
		}
		err := a.checkUpdate(a.ctx, false)
		switch {
		case a.ctx.Err() != nil:
			return
		case err == nil:
			next = time.Now().Add(updateEvery)
		case isNetworkTrouble(err):
			next = time.Now().Add(updateRetryOffline)
		default:
			next = time.Now().Add(updateRetryLater)
		}
	}
}

// isNetworkTrouble: no internet (or GitHub unreachable), as opposed to a bad answer.
func isNetworkTrouble(err error) bool {
	if err == nil || errors.Is(err, errBadManifest) {
		return false
	}
	var he *httpStatusError
	return !errors.As(err, &he) || he.code >= 500 || he.code == 429 || he.code == 408
}

// CheckUpdate looks for a new version now ("Check for updates" in About). The answer comes
// as an "update" event.
func (a *App) CheckUpdate() map[string]any {
	u := a.upd
	u.mu.Lock()
	busy := u.phase == "downloading" || u.phase == "installing" || u.phase == "checking"
	u.mu.Unlock()
	if !busy {
		a.wg.Add(1)
		go func() {
			defer a.wg.Done()
			defer func() {
				if r := recover(); r != nil {
					log.Printf("update check: %v", r)
				}
			}()
			_ = a.checkUpdate(a.ctx, true)
		}()
	}
	return a.UpdateInfo()
}

// fetchManifest reads latest.json from the first address that answers with a valid one.
func fetchManifest(ctx context.Context, client *http.Client, urls []string) (*updateManifest, error) {
	var firstErr error
	for _, u := range urls {
		m, err := fetchManifestFrom(ctx, client, u)
		if err == nil {
			return m, nil
		}
		log.Printf("update manifest %s: %v", u, err)
		if firstErr == nil {
			firstErr = err
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, firstErr
}

func fetchManifestFrom(ctx context.Context, client *http.Client, u string) (*updateManifest, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &httpStatusError{resp.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if err != nil {
		return nil, err
	}
	return parseSignedManifest(body, updatePublicKey)
}

// checkUpdate asks GitHub once. manual: the user asked (the answer is shown even when there is
// nothing new, and a failure is said).
func (a *App) checkUpdate(ctx context.Context, manual bool) error {
	u := a.upd
	u.mu.Lock()
	if u.phase == "downloading" || u.phase == "installing" {
		u.mu.Unlock()
		return nil
	}
	before := u.phase
	if manual {
		u.phase, u.err = "checking", nil
	}
	u.mu.Unlock()
	if manual {
		a.emitUpdate(false)
	}
	m, err := fetchManifest(ctx, a.tools.client, updateManifestURLs)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	u.mu.Lock()
	if u.phase == "downloading" || u.phase == "installing" {
		u.mu.Unlock()
		return err
	}
	if err != nil {
		if manual {
			if u.latest != nil {
				// A new version is known already: it stays offered.
				u.phase = "available"
			} else {
				code := "update_check"
				if errors.Is(err, errBadManifest) {
					code = "update_verify"
				}
				u.phase, u.err, u.failedIn = "error", NewUserError(code), "check"
				u.err.Details = err.Error()
			}
		}
		u.mu.Unlock()
		if manual {
			a.emitUpdate(false)
		}
		return err
	}
	u.checkedAt = time.Now()
	newer := newerVersion(m.Version, appVersion)
	if newer {
		u.latest, u.phase, u.err = m, "available", nil
	} else {
		u.latest, u.phase, u.err = nil, "latest", nil
	}
	u.mu.Unlock()
	a.store.Update(func(s *Settings) {
		s.UpdateChecked = time.Now()
		s.UpdateSeen = m.Version
	})
	if newer {
		log.Printf("update: version %s is available (this is %s)", m.Version, appVersion)
		a.emitUpdate(a.shouldPrompt(m.Version))
	} else if manual || before == "available" || before == "error" {
		a.emitUpdate(false)
	}
	return nil
}

// shouldPrompt: the window opens the update's window for this version, unless the user said
// "Not now" less than a day ago (or it was shown already while HELINK runs).
func (a *App) shouldPrompt(version string) bool {
	st := a.store.Get()
	if st.UpdateSnoozed == version && time.Now().Before(st.UpdateSnoozeUntil) {
		return false
	}
	u := a.upd
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.prompted == version {
		return false
	}
	u.prompted = version
	return true
}

// UpdateLater is "Not now": the update stays offered (the button in the toolbar, About), and
// HELINK asks again in a day.
func (a *App) UpdateLater() {
	u := a.upd
	u.mu.Lock()
	v := ""
	if u.latest != nil {
		v = u.latest.Version
	}
	u.mu.Unlock()
	if v == "" {
		return
	}
	a.store.Update(func(s *Settings) {
		s.UpdateSnoozed, s.UpdateSnoozeUntil = v, time.Now().Add(updateSnooze)
	})
	a.emitUpdate(false)
}

// UpdateInfo is where the update stands, for the page.
func (a *App) UpdateInfo() map[string]any { return a.updateView(false) }

func (a *App) updateView(prompt bool) map[string]any {
	u := a.upd
	u.mu.Lock()
	defer u.mu.Unlock()
	m := map[string]any{"state": u.phase, "current": appVersion, "page": updatePageURL, "prompt": prompt}
	if !u.checkedAt.IsZero() {
		m["checkedAt"] = u.checkedAt.UnixMilli()
	}
	if l := u.latest; l != nil {
		m["version"] = l.Version
		notes := l.Notes["ar"]
		if isEnglish() || len(notes) == 0 {
			if en := l.Notes["en"]; len(en) > 0 {
				notes = en
			}
		}
		if notes == nil {
			notes = []string{}
		}
		m["notes"] = notes
		if f := a.updateFileFor(l); f != nil {
			m["sizeText"] = HumanSize(f.Size)
		}
		st := a.store.Get()
		m["snoozed"] = st.UpdateSnoozed == l.Version && time.Now().Before(st.UpdateSnoozeUntil)
	}
	if u.phase == "downloading" {
		m["done"], m["total"] = u.done, u.total
		m["doneText"], m["totalText"] = HumanSize(u.done), HumanSize(u.total)
		if u.total > 0 {
			m["percent"] = float64(u.done) * 100 / float64(u.total)
		}
	}
	if u.err != nil {
		m["error"] = *u.err
		m["failedIn"] = u.failedIn
	}
	return m
}

func (a *App) emitUpdate(prompt bool) { a.emit.Emit("update", a.updateView(prompt)) }

// updateFileFor is the file this copy of HELINK needs (call with any lock; it only reads).
func (a *App) updateFileFor(m *updateManifest) *updateFile {
	t, err := updateTarget()
	if err != nil {
		return nil
	}
	if t.Kind == "portable" {
		return m.Portable
	}
	return m.Setup
}

// UpdateNow downloads the new version, checks it, and puts it in place: HELINK closes and the
// new one opens. Progress comes as "update" events.
func (a *App) UpdateNow() map[string]any {
	u := a.upd
	u.mu.Lock()
	if u.phase == "downloading" || u.phase == "installing" {
		u.mu.Unlock()
		return map[string]any{"ok": true}
	}
	m := u.latest
	if m == nil {
		u.mu.Unlock()
		return map[string]any{"error": NewUserError("update_none")}
	}
	target, terr := updateTarget()
	var f *updateFile
	if terr == nil {
		f = m.Setup
		if target.Kind == "portable" {
			f = m.Portable
		}
	}
	if terr != nil || f == nil {
		e := NewUserError("update_manual")
		if terr != nil {
			e.Details = terr.Error()
		}
		u.phase, u.err, u.failedIn = "error", e, "install"
		u.mu.Unlock()
		a.emitUpdate(false)
		return map[string]any{"error": e}
	}
	ctx, cancel := context.WithCancel(a.ctx)
	u.phase, u.err, u.cancel, u.done, u.total = "downloading", nil, cancel, 0, f.Size
	u.mu.Unlock()
	a.emitUpdate(false)
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		defer cancel()
		defer func() {
			if r := recover(); r != nil {
				e := panicError("update", r)
				e.Code = "update_failed"
				a.updateFailed(e)
			}
		}()
		a.runUpdate(ctx, m, target, f)
	}()
	return map[string]any{"ok": true}
}

// CancelUpdate stops downloading the update (what was downloaded is kept for next time).
func (a *App) CancelUpdate() {
	u := a.upd
	u.mu.Lock()
	if u.phase == "downloading" && u.cancel != nil {
		u.cancel()
	}
	u.mu.Unlock()
}

func (a *App) updateFailed(e *UserError) {
	u := a.upd
	u.mu.Lock()
	u.phase, u.err, u.failedIn, u.cancel = "error", e, "install", nil
	u.mu.Unlock()
	a.emitUpdate(false)
}

var safeAssetRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,80}$`)

// updateDir holds the downloaded update until HELINK has been replaced.
func (a *App) updateDir() string { return filepath.Join(a.paths.Data, "update") }

func (a *App) runUpdate(ctx context.Context, m *updateManifest, target updateTargetInfo, f *updateFile) {
	name := path.Base(f.URLs[0])
	if !safeAssetRe.MatchString(name) {
		name = "HELINK-" + m.Version + "-update"
	}
	dir := a.updateDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		e := NewUserError("update_failed")
		e.Details = err.Error()
		a.updateFailed(e)
		return
	}
	dest := filepath.Join(dir, name)
	if !fileMatches(dest, f) {
		_ = os.Remove(dest)
		if free, ok := freeSpace(dir); ok && free < 3*f.Size+30<<20 {
			e := NewUserError("update_disk")
			e.Need, e.Free = 3*f.Size+30<<20, free
			a.updateFailed(e)
			return
		}
		var last time.Time
		err := a.tools.download(ctx, f.URLs, dest, func(done, total int64) {
			if total <= 0 {
				total = f.Size
			}
			u := a.upd
			u.mu.Lock()
			u.done, u.total = done, total
			u.mu.Unlock()
			if time.Since(last) >= 200*time.Millisecond || done == total {
				last = time.Now()
				a.emitUpdate(false)
			}
		})
		if ctx.Err() != nil {
			// Cancelled (or HELINK closes): what was downloaded stays for the next try.
			u := a.upd
			u.mu.Lock()
			u.phase, u.cancel = "available", nil
			u.mu.Unlock()
			a.emitUpdate(false)
			return
		}
		if err != nil {
			e := NewUserError("update_network")
			e.Details = err.Error()
			a.updateFailed(e)
			return
		}
	}
	if !fileMatches(dest, f) {
		_ = os.Remove(dest)
		e := NewUserError("update_verify")
		e.Details = "size or SHA-256 differ from latest.json"
		a.updateFailed(e)
		return
	}
	u := a.upd
	u.mu.Lock()
	u.phase, u.cancel = "installing", nil
	u.done = u.total
	u.mu.Unlock()
	a.emitUpdate(false)

	// The downloads running now continue in the new version.
	var resume []string
	a.mu.Lock()
	for _, t := range a.tasks {
		if t.active() {
			resume = append(resume, t.ID)
		}
	}
	a.mu.Unlock()
	a.store.Update(func(s *Settings) {
		s.ResumeAfterUpdate, s.UpdatingFrom, s.UpdatingTo = resume, appVersion, m.Version
	})
	log.Printf("update: installing %s (%s) from %s", m.Version, target.Kind, dest)
	if err := applyUpdate(target, dest); err != nil {
		log.Printf("update: %v", err)
		a.store.Update(func(s *Settings) { s.ResumeAfterUpdate, s.UpdatingFrom, s.UpdatingTo = nil, "", "" })
		code := "update_failed"
		if errors.Is(err, os.ErrPermission) {
			code = "update_manual"
		}
		e := NewUserError(code)
		e.Details = err.Error()
		a.updateFailed(e)
		return
	}
	// The new HELINK takes over: this one closes (its downloads pause, and continue there).
	if a.Quit != nil {
		a.Quit()
	}
}

// fileMatches: the file is there with the size and SHA-256 the manifest gives.
func fileMatches(p string, f *updateFile) bool {
	fi, err := os.Stat(p)
	if err != nil || fi.Size() != f.Size {
		return false
	}
	in, err := os.Open(p)
	if err != nil {
		return false
	}
	defer in.Close()
	h := sha256.New()
	if _, err := io.Copy(h, in); err != nil {
		return false
	}
	return strings.EqualFold(hex.EncodeToString(h.Sum(nil)), f.SHA256)
}

// extractUpdateExe takes HELINK.exe out of the portable zip into dest.
func extractUpdateExe(zipPath, dest string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, f := range zr.File {
		if !strings.EqualFold(path.Base(strings.ReplaceAll(f.Name, "\\", "/")), "HELINK.exe") || f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		defer rc.Close()
		tmp := dest + ".tmp"
		out, err := os.Create(tmp)
		if err != nil {
			return err
		}
		head := make([]byte, 2)
		n, _ := io.ReadFull(rc, head)
		if n != 2 || !bytes.Equal(head, []byte("MZ")) {
			out.Close()
			os.Remove(tmp)
			return errors.New("HELINK.exe in the update is not a program")
		}
		if _, err := out.Write(head); err == nil {
			_, err = io.Copy(out, rc)
		}
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(tmp)
			return err
		}
		_ = os.Remove(dest)
		return os.Rename(tmp, dest)
	}
	return errors.New("no HELINK.exe in the update")
}

// swapPortableExe puts the new HELINK.exe (from the portable zip) in place of the running one:
// Windows lets a running program be renamed, not overwritten. The old one stays as
// HELINK.exe.old until the new one has started (and removes it); start runs the new one. On any
// failure the old one is put back.
func swapPortableExe(exe, zipPath string, start func(exe string) error) error {
	newExe := exe + ".new"
	if err := extractUpdateExe(zipPath, newExe); err != nil {
		return err
	}
	old := exe + ".old"
	_ = os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		_ = os.Remove(newExe)
		return err
	}
	if err := os.Rename(newExe, exe); err != nil {
		_ = os.Rename(old, exe)
		_ = os.Remove(newExe)
		return err
	}
	if err := start(exe); err != nil {
		_ = os.Rename(exe, newExe)
		_ = os.Rename(old, exe)
		_ = os.Remove(newExe)
		return err
	}
	return nil
}

// afterUpdate runs when HELINK starts: it continues the downloads an update stopped, tells
// whether the update went through, and cleans what it left. It returns the version updated to
// ("" if none) and whether an update was tried but this is still the old version.
func (a *App) afterUpdate() (updated string, failed bool) {
	st := a.store.Get()
	if st.UpdatingTo == "" && len(st.ResumeAfterUpdate) == 0 {
		return "", false
	}
	switch {
	case st.UpdatingTo == appVersion:
		updated = appVersion
		log.Printf("update: now %s (was %s)", appVersion, st.UpdatingFrom)
	case st.UpdatingTo != "" && st.UpdatingFrom == appVersion:
		failed = true
		log.Printf("update to %s did not happen", st.UpdatingTo)
	}
	resume := map[string]bool{}
	for _, id := range st.ResumeAfterUpdate {
		resume[id] = true
	}
	n := 0
	a.mu.Lock()
	now := time.Now().UnixMilli()
	for _, t := range a.tasks {
		if resume[t.ID] && t.State == statePaused {
			t.State, t.Stage, t.QueuedAt = stateQueued, "", now+int64(n)
			n++
		}
	}
	a.mu.Unlock()
	a.store.Update(func(s *Settings) { s.ResumeAfterUpdate, s.UpdatingFrom, s.UpdatingTo = nil, "", "" })
	if updated != "" {
		if dir := a.updateDir(); os.RemoveAll(dir) != nil {
			// The installer that opened this HELINK may still be closing (its file, its folder):
			// a little later then.
			go func() {
				for i := 0; i < 12; i++ {
					time.Sleep(10 * time.Second)
					if os.RemoveAll(dir) == nil {
						return
					}
				}
			}()
		}
		a.store.Update(func(s *Settings) { s.UpdateSnoozed, s.UpdateSnoozeUntil = "", time.Time{} })
		if t, err := updateTarget(); err == nil && t.Kind == "portable" {
			_ = os.Remove(t.Exe + ".old") // the portable copy's previous HELINK.exe
		}
	}
	if n > 0 {
		a.saveTasks(true)
	}
	return updated, failed
}
