package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
)

// appVersion is this HELINK's version (a variable only so the update tests can play an older or newer one).
var appVersion = "2.4.1"

const (
	// Finished downloads kept in the list (the files themselves stay in their folder).
	keepFinished = 500
	// The hidden folder, inside the save folder, that holds unfinished downloads.
	tempFolderName = ".helink-temp"
	// The same folder of the app's earlier versions (Helal), cleaned when found.
	oldTempFolderName = ".nazzelha-temp"
)

// Emitter sends an event to the user interface.
type Emitter interface {
	Emit(event string, payload any)
}

type Paths struct {
	Data  string // %LOCALAPPDATA%\HELINK
	Tools string
	Work  string
	Cache string
}

func newPaths(base string) Paths {
	p := Paths{
		Data:  base,
		Tools: filepath.Join(base, "tools"),
		Work:  filepath.Join(base, "work"),
		Cache: filepath.Join(base, "cache"),
	}
	for _, d := range []string{p.Data, p.Tools, p.Work, p.Cache} {
		_ = os.MkdirAll(d, 0o755)
	}
	return p
}

type fetched struct {
	seq   int
	url   string // the post's address, after opening short links
	site  string
	at    time.Time
	items []fetchedItem
	opts  fetchOpts
	page  string
}

// fetchOpts make a video found in a browser be read the way the browser read it: its
// cookies (never for YouTube), the page as Referer, the browser's User-Agent.
type fetchOpts struct {
	cookies   []Cookie
	referer   string
	userAgent string
	page      string         // the page the video was found on
	media     []BrowserMedia // what the page loaded, offered when reading the page finds nothing
}

type fetchedItem struct {
	Item
	infoPath string // what yt-dlp found, saved for the download step
}

// App holds the state behind the window. Its public methods are called from the UI
// thread and must return quickly; slow work runs in goroutines and reports via events
// ("setup", "info", "thumb", "task").
type App struct {
	emit  Emitter
	paths Paths
	store *Store
	tools *Tools
	dl    *taskStore

	mu           sync.Mutex
	setupBusy    bool
	setupCancel  context.CancelFunc
	fetchSeq     int
	fetchCancel  context.CancelFunc
	cur          *fetched
	bgCancel     context.CancelFunc
	lastInfo     *infoEvent // the post on screen, so the page can show it again in another language
	lastFetchEnd *infoEvent // how the latest link reading ended, for a page that wants to catch up
	shuttingDown bool

	tasks          []*DownloadTask         // the downloads list, oldest first
	pending        map[string]*pendingFile // files shown in the "new download" window
	pendSeq        int
	readOpts       map[int]fetchOpts       // how the latest readings from a browser were asked for
	bposts         map[string]*browserPost // videos read for the browser's menu, by id (browserread.go)
	bpostByURL     map[string]*browserPost // the same, by the address read
	bpostSeq       int
	extSeen        time.Time // when the browser extension last asked something
	storeSeq       int64
	lastStoreWrite time.Time
	idSeq          int
	saveMu         sync.Mutex    // one finished file is put in its folder at a time (unique names)
	sep            *sepTool      // the music remover
	sepSem         chan struct{} // the music is removed from one file at a time
	upd            *updater      // HELINK's own updates (update.go)
	justUpdated    string        // the version HELINK was just updated to (said once)
	updateDidFail  bool          // an update was tried, but this is still the old version
	curList        *fetchedList  // the playlist on screen (playlist.go)
	products       productStore  // products read for their pictures (product.go)
	previews       previewServer // plays the videos in the clip window (clip.go)

	wg           sync.WaitGroup // background work
	saves        sync.WaitGroup // list writes started from the UI thread
	shutdownOnce sync.Once

	ctx    context.Context // cancelled when the window closes
	cancel context.CancelFunc

	// Show brings the window to the front for something the browser sent ("file" for the
	// "new download" window, "video" for a video's qualities). Set by the window's code.
	Show func(reason string)
	// SettingsChanged lets the window's code follow a change (start with Windows…).
	SettingsChanged func(Settings)
	// Quit closes HELINK (asked by the installer before an update). Set by the window's code.
	Quit func()
}

func NewApp(emit Emitter, paths Paths) *App {
	store := OpenStore(filepath.Join(paths.Data, "settings.json"))
	setLang(store.Get().Lang)
	a := &App{emit: emit, paths: paths, store: store, tools: NewTools(paths.Tools, store),
		dl: newTaskStore(filepath.Join(paths.Data, "downloads.json"), filepath.Join(paths.Work, "thumbs"))}
	a.tools.devMode = devToolsEnabled()
	a.sep, a.sepSem = newSepTool(paths.Tools, a.tools), make(chan struct{}, 1)
	a.upd = newUpdater()
	a.ctx, a.cancel = context.WithCancel(context.Background())
	changed := false
	for _, t := range a.dl.load() {
		if t.active() {
			// The app was closed in the middle: what was downloaded stays, and the user
			// resumes it from the list.
			t.State, t.Stage = statePaused, ""
			changed = true
		}
		a.tasks = append(a.tasks, t)
	}
	if changed {
		a.saveTasks(true)
	}
	// HELINK was just replaced by a newer version: the downloads it stopped continue.
	a.justUpdated, a.updateDidFail = a.afterUpdate()
	if a.ActiveCount() > 0 {
		a.wg.Add(1)
		go func() {
			defer a.wg.Done()
			a.schedule()
		}()
	}
	return a
}

// ---------------------------------------------------------------------------
// Start-up
// ---------------------------------------------------------------------------

type InitResult struct {
	Screen        string           `json:"screen"` // "setup" | "ready"
	Platform      string           `json:"platform"`
	SaveDir       string           `json:"saveDir"`
	SaveDirLabel  string           `json:"saveDirLabel"`
	Version       string           `json:"version"`
	Lang          string           `json:"lang"`
	SetupSizeText string           `json:"setupSizeText"`
	Missing       []string         `json:"missing"`
	Busy          string           `json:"busy"`     // "" or "setup"
	LastKind      string           `json:"lastKind"` // "video" or "audio": what the user downloaded last time
	Parallel      int              `json:"parallel"`
	Restore       map[string]any   `json:"restore"`         // {"info": the post on screen}
	Fetch         *infoEvent       `json:"fetch,omitempty"` // how the latest link reading ended
	Tasks         []map[string]any `json:"tasks"`           // the downloads list, newest first
	Settings      SettingsView     `json:"settings"`
	Pending       []map[string]any `json:"pending"`                // files waiting in the "new download" window
	SepReady      bool             `json:"sepReady"`               // the music remover is set up
	SepSizeText   string           `json:"sepSizeText"`            // what setting it up downloads
	Update        map[string]any   `json:"update"`                 // where HELINK's own update stands
	Updated       string           `json:"updated,omitempty"`      // HELINK was just updated to this version
	UpdateFailed  bool             `json:"updateFailed,omitempty"` // an update was tried and did not happen
}

func (a *App) Init() InitResult {
	dir := a.SaveDir()
	lastKind := a.store.Get().LastKind
	if lastKind != "audio" {
		lastKind = "video"
	}
	r := InitResult{Screen: "ready", Platform: "windows", SaveDir: dir, SaveDirLabel: folderLabel(dir), Version: appVersion,
		Lang: curLang(), LastKind: lastKind, Missing: []string{}, Parallel: a.store.Get().parallel()}
	if missing := a.tools.Missing(); len(missing) > 0 {
		r.Screen = "setup"
		var total int64
		for _, m := range missing {
			r.Missing = append(r.Missing, string(m))
			switch m {
			case toolYtdlp:
				total += estYtdlp
			case toolDeno:
				total += estDeno
			case toolFfmpeg:
				total += estFfmpeg
			}
		}
		r.SetupSizeText = HumanSize(total)
	}
	a.mu.Lock()
	if a.setupBusy {
		r.Busy = "setup"
	}
	r.Restore = map[string]any{"info": nil}
	if a.lastInfo != nil {
		r.Restore["info"] = a.lastInfo.copy()
	}
	if a.lastFetchEnd != nil {
		r.Fetch = a.lastFetchEnd.copy()
	}
	r.Tasks = a.taskListLocked()
	a.mu.Unlock()
	r.Settings = a.settingsView()
	r.Pending = a.PendingFiles()
	r.SepReady, r.SepSizeText = a.sep.ready(), HumanSize(sepDownloadSize())
	r.Update = a.UpdateInfo()
	a.mu.Lock()
	r.Updated, r.UpdateFailed = a.justUpdated, a.updateDidFail
	a.justUpdated, a.updateDidFail = "", false // said once
	a.mu.Unlock()
	return r
}

// Lang is "ar" (Modern Standard Arabic, the default) or "en".
func (a *App) Lang() string { return curLang() }

// SetLang changes the language of everything the app writes, and remembers it.
func (a *App) SetLang(lang string) {
	setLang(lang)
	a.store.Update(func(s *Settings) { s.Lang = curLang() })
	a.writeExtFile()
}

// StartMaintenance cleans leftovers and looks for a newer yt-dlp in the background.
func (a *App) StartMaintenance() {
	ctx, cancel := context.WithCancel(context.Background())
	a.mu.Lock()
	if a.bgCancel != nil {
		a.bgCancel()
	}
	a.bgCancel = cancel
	a.mu.Unlock()
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				panicError("maintenance", r)
			}
		}()
		a.cleanLeftovers()
		a.sep.removeOld()
		if len(a.tools.Missing()) == 0 {
			if updated, err := a.tools.UpdateYtdlp(ctx, 72*time.Hour); err != nil {
				log.Printf("background update: %v", err)
			} else if updated {
				log.Printf("yt-dlp updated to %s", a.store.Get().YtdlpVersion)
			}
		}
	}()
}

func (a *App) cleanLeftovers() {
	entries, _ := os.ReadDir(a.paths.Work)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > 24*time.Hour {
			_ = os.Remove(filepath.Join(a.paths.Work, e.Name()))
		}
	}
	_ = os.RemoveAll(filepath.Join(a.paths.Tools, "_downloads", "stale"))

	// Temporary folders belong to entries of the list; anything else is a leftover (an
	// earlier version of the app, or a crash while deleting).
	ids := map[string]bool{}
	roots := map[string]bool{a.SaveDir(): true, defaultSaveDir(): true, filepath.Dir(defaultSaveDir()): true}
	a.mu.Lock()
	for _, t := range a.tasks {
		ids[t.ID] = true
		if t.SaveDir != "" {
			roots[t.SaveDir] = true
		}
	}
	a.mu.Unlock()
	for dir := range roots {
		root := filepath.Join(dir, tempFolderName)
		jobs, _ := os.ReadDir(root)
		for _, j := range jobs {
			if !ids[j.Name()] && time.Since(newestIn(filepath.Join(root, j.Name()))) > time.Hour {
				_ = os.RemoveAll(filepath.Join(root, j.Name()))
			}
		}
		_ = os.Remove(root) // only if empty
		old := filepath.Join(dir, oldTempFolderName)
		if olds, err := os.ReadDir(old); err == nil {
			for _, j := range olds {
				if time.Since(newestIn(filepath.Join(old, j.Name()))) > time.Hour {
					_ = os.RemoveAll(filepath.Join(old, j.Name()))
				}
			}
			_ = os.Remove(old)
		}
	}
	a.dl.pruneThumbs(ids)
	a.pruneCookies(ids)
}

// newestIn is the time of the newest change inside a folder.
func newestIn(dir string) time.Time {
	var t time.Time
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if info, err := d.Info(); err == nil && info.ModTime().After(t) {
			t = info.ModTime()
		}
		return nil
	})
	return t
}

func treeSize(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}

// ---------------------------------------------------------------------------
// Settings
// ---------------------------------------------------------------------------

// SaveDir is where new downloads go: the folder the user chose, or Downloads\HELINK.
func (a *App) SaveDir() string {
	if d := a.store.Get().SaveDir; d != "" {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			return d
		}
	}
	return defaultSaveDir()
}

type FolderResult struct {
	SaveDir      string `json:"saveDir"`
	SaveDirLabel string `json:"saveDirLabel"`
	Changed      bool   `json:"changed"`
	Error        string `json:"error,omitempty"`
	TaskID       string `json:"taskId,omitempty"`
}

func (a *App) SetSaveDir(dir string) FolderResult {
	if dir == "" {
		d := a.SaveDir()
		return FolderResult{SaveDir: d, SaveDirLabel: folderLabel(d)}
	}
	if !dirWritable(dir) {
		d := a.SaveDir()
		return FolderResult{SaveDir: d, SaveDirLabel: folderLabel(d),
			Error: tr("تعذّر الحفظ في هذا المجلد. اختر مجلدًا آخر مثل «التنزيلات» أو «سطح المكتب».",
				"Can't save in this folder. Choose another one, like Downloads or Desktop.")}
	}
	a.store.Update(func(s *Settings) { s.SaveDir = dir })
	return FolderResult{SaveDir: dir, SaveDirLabel: folderLabel(dir)}
}

// Busy reports work that would stop if the window closed now.
func (a *App) Busy() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.setupBusy || a.activeCountLocked() > 0
}

func (a *App) SetupBusy() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.setupBusy
}

func (a *App) CancelSetup() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.setupCancel != nil {
		a.setupCancel()
	}
}

// panicError turns a crash inside a background job into an error message instead of
// closing the whole program. Call it with the value of recover().
func panicError(what string, r any) *UserError {
	log.Printf("panic in %s: %v\n%s", what, r, debug.Stack())
	e := NewUserError("unknown")
	e.Details = fmt.Sprint(r)
	return e
}

// Shutdown stops everything: running downloads are paused, so they can be resumed the
// next time the app opens.
func (a *App) Shutdown() { a.shutdownOnce.Do(a.shutdown) }

func (a *App) shutdown() {
	a.previews.stop()
	a.cancel()
	var stop []context.CancelFunc
	a.mu.Lock()
	a.shuttingDown = true
	for _, c := range []context.CancelFunc{a.setupCancel, a.fetchCancel, a.bgCancel} {
		if c != nil {
			stop = append(stop, c)
		}
	}
	for _, t := range a.tasks {
		if t.cancel != nil {
			if t.stopReason == "" {
				t.stopReason = "pause"
			}
			stop = append(stop, t.cancel)
		} else if t.State == stateQueued {
			t.State = statePaused
		}
	}
	a.mu.Unlock()
	for _, c := range stop {
		c()
	}
	a.saveTasks(true)
	done := make(chan struct{})
	go func() { a.wg.Wait(); a.saves.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(6 * time.Second):
	}
	a.saveTasks(true)
}

// ---------------------------------------------------------------------------
// First-run setup
// ---------------------------------------------------------------------------

func (a *App) StartSetup() {
	a.mu.Lock()
	if a.setupBusy {
		a.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.setupBusy, a.setupCancel = true, cancel
	a.mu.Unlock()

	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		defer cancel()
		defer func() {
			if r := recover(); r != nil {
				e := panicError("setup", r)
				e.Code = "setup_failed"
				a.mu.Lock()
				a.setupBusy, a.setupCancel = false, nil
				a.mu.Unlock()
				a.emit.Emit("setup", map[string]any{"phase": "error", "error": e})
			}
		}()
		err := a.tools.Install(ctx, func(p SetupProgress) { a.emit.Emit("setup", p) })
		a.mu.Lock()
		a.setupBusy, a.setupCancel = false, nil
		a.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.Printf("setup failed: %v", err)
			code := "setup_failed"
			msg := strings.ToLower(err.Error())
			switch {
			case errors.Is(err, errToolBlocked):
				code = "setup_blocked"
			case strings.Contains(msg, "x509") || strings.Contains(msg, "certificate"):
				code = "clock" // a wrong PC clock breaks secure downloads
			case strings.Contains(msg, "no space") || strings.Contains(msg, "not enough space"):
				code = "disk"
			}
			ue := NewUserError(code)
			ue.Details = err.Error()
			if code == "disk" {
				ue.Action = "" // the tools go to the app's own folder, not the save folder
			}
			a.emit.Emit("setup", map[string]any{"phase": "error", "error": ue})
			return
		}
		a.emit.Emit("setup", SetupProgress{Phase: "done", Percent: 100})
		a.StartMaintenance()
	}()
}

// ---------------------------------------------------------------------------
// Reading the video and its qualities
// ---------------------------------------------------------------------------

type infoEvent struct {
	Seq      int              `json:"seq"`
	Status   string           `json:"status"` // loading | updating | done | error
	Site     string           `json:"site,omitempty"`
	SiteName lazySiteName     `json:"siteName,omitempty"`
	Items    []Item           `json:"items,omitempty"`
	Error    *UserError       `json:"error,omitempty"`
	Media    []map[string]any `json:"media,omitempty"`   // with an error: what the page loaded, to download instead
	List     *ListInfo        `json:"list,omitempty"`    // status "list": a playlist (playlist.go)
	ListURL  string           `json:"listUrl,omitempty"` // a YouTube video opened from a list: the whole list
}

func newInfoEvent(seq int, status, site string, items []Item, e *UserError) *infoEvent {
	return &infoEvent{Seq: seq, Status: status, Site: site, SiteName: lazySiteName(site), Items: items, Error: e}
}

// copy is safe to write out after the lock is released.
func (e *infoEvent) copy() *infoEvent {
	c := *e
	c.Items = append([]Item(nil), e.Items...)
	return &c
}

// Fetch starts reading the video behind a pasted link. Results arrive as "info" events.
func (a *App) Fetch(input string) int { return a.fetch(input, fetchOpts{}, nil) }

// fetch is Fetch with the browser's way of reading the page; before (if any) learns the
// number of the reading before its first event is sent.
func (a *App) fetch(input string, opts fetchOpts, before func(seq int)) int {
	a.mu.Lock()
	if a.fetchCancel != nil {
		a.fetchCancel()
	}
	a.fetchSeq++
	seq := a.fetchSeq
	ctx, cancel := context.WithCancel(context.Background())
	a.fetchCancel = cancel
	a.mu.Unlock()
	a.rememberReadOpts(seq, opts)
	if before != nil {
		before(seq)
	}

	link, code := linkToRead(input)
	isList := false
	if code == "playlist" && link.Site == siteYouTube {
		code, isList = "", true // a course or playlist: all of it at once (playlist.go)
	}
	if code != "" {
		cancel()
		a.fetchEnded(seq, newInfoEvent(seq, "error", link.Site, nil, NewUserError(code).Localize(link.Site)))
		return seq
	}
	a.emit.Emit("info", newInfoEvent(seq, "loading", link.Site, nil, nil))
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		defer cancel()
		defer func() {
			if r := recover(); r != nil && ctx.Err() == nil {
				a.fetchEnded(seq, newInfoEvent(seq, "error", link.Site, nil, panicError("fetch", r)))
			}
		}()
		if isList {
			a.runFetchList(ctx, seq, link)
			return
		}
		a.runFetch(ctx, seq, link, opts)
	}()
	return seq
}

// CancelFetch stops reading a link. forget also drops the video shown (the user went home).
func (a *App) CancelFetch(forget bool) {
	a.mu.Lock()
	if a.fetchCancel != nil {
		a.fetchCancel()
		a.fetchCancel = nil
	}
	a.fetchSeq++
	if forget {
		a.lastInfo = nil
	}
	a.mu.Unlock()
}

func (a *App) fetchEnded(seq int, ev *infoEvent) {
	a.mu.Lock()
	if seq == a.fetchSeq {
		a.lastFetchEnd = ev
	}
	a.mu.Unlock()
	a.emit.Emit("info", ev)
}

func (a *App) runFetch(ctx context.Context, seq int, link Link, opts fetchOpts) {
	f, items, uerr := a.readPost(ctx, seq, link, opts, func(site string) {
		a.emit.Emit("info", newInfoEvent(seq, "updating", site, nil, nil))
	})
	if ctx.Err() != nil {
		f.removeFiles()
		return
	}
	if uerr != nil {
		ev := newInfoEvent(seq, "error", uerr.site, nil, uerr.err)
		ev.Media = mediaChoices(opts.media)
		a.fetchEnded(seq, ev)
		return
	}
	ev := newInfoEvent(seq, "done", f.site, items, nil)
	if f.site == siteYouTube {
		ev.ListURL = youtubeListOf(link.URL)
	}
	a.mu.Lock()
	if seq != a.fetchSeq {
		a.mu.Unlock()
		f.removeFiles()
		return
	}
	a.cur = f
	a.lastInfo = ev.copy()
	a.lastFetchEnd = ev.copy()
	a.mu.Unlock()
	a.pruneInfoFiles()
	a.emit.Emit("info", ev)
	if f.site != siteYouTube {
		a.fetchThumbnails(f)
	}
}

// readFailure is why a post could not be read, and the site it is shown for.
type readFailure struct {
	site string
	err  *UserError // localized
}

// removeFiles deletes what yt-dlp found for the post (nil: nothing).
func (f *fetched) removeFiles() {
	if f == nil {
		return
	}
	for _, it := range f.items {
		if it.infoPath != "" {
			_ = os.Remove(it.infoPath)
		}
	}
}

// readPost reads the post behind a link with yt-dlp (short links opened first, the tools
// repaired once if they are out of date), and keeps what yt-dlp found for each video so
// its download starts right away. onRepair (if set) learns that the tools are being
// repaired. Stopped (ctx): nothing, and no failure.
func (a *App) readPost(ctx context.Context, seq int, link Link, opts fetchOpts, onRepair func(site string)) (*fetched, []Item, *readFailure) {
	site := link.Site
	if _, known := siteNames[site]; known {
		// The sites with a reader of their own: it sets the headers that site needs (a
		// browser's User-Agent or Referer can make YouTube answer differently).
		opts.referer, opts.userAgent = "", ""
	}
	if site == siteYouTube {
		opts.cookies = nil // YouTube is read without an account, always
	}
	fail := func(e *UserError) *readFailure {
		if ctx.Err() != nil {
			return nil
		}
		if _, known := siteNames[site]; !known {
			// A page of another site: "no video here" says it better than yt-dlp's reasons.
			switch e.Code {
			case "unsupported_site", "invalid_url", "novideo", "extractor", "unknown", "noformats", "format", "profile", "playlist":
				d := e.Details
				e = NewUserError("nothing_found")
				e.Details = d
			}
		}
		return &readFailure{site: site, err: e.Localize(site)}
	}
	if len(a.tools.Missing()) > 0 {
		return nil, nil, fail(NewUserError("tools_missing"))
	}
	target := link.URL
	if link.Kind == LinkShort {
		// fb.watch/…, vm.tiktok.com/…, facebook.com/share/…: find the real post first.
		resolved, err := resolveShortLink(ctx, a.tools.client, link.URL)
		if ctx.Err() != nil {
			return nil, nil, nil
		}
		if err != nil {
			log.Printf("short link %s: %v", link.URL, err)
		} else if l2, r := ParseLink(resolved); r == LinkFound && l2.Kind != LinkShort {
			switch l2.Kind {
			case LinkProfile:
				return nil, nil, fail(NewUserError("profile"))
			case LinkLive:
				return nil, nil, fail(NewUserError("live"))
			case LinkPlaylist:
				return nil, nil, fail(NewUserError("playlist"))
			}
			target = l2.URL
		}
	}
	repaired := false
	for {
		data, logText, uerr := a.fetchJSON(ctx, target, site, opts)
		if ctx.Err() != nil {
			return nil, nil, nil
		}
		var items []Item
		if uerr == nil {
			var err error
			site, items, err = BuildPost(data, link.Site)
			if err != nil {
				var ie *InfoError
				if errors.As(err, &ie) {
					uerr = NewUserError(ie.Code)
					uerr.Details = tailLines(logText, 25)
				} else {
					uerr = NewUserError("unknown")
					uerr.Details = "bad json: " + err.Error()
				}
			}
		}
		// The YouTube helper warned that it could not solve YouTube's challenge:
		// qualities may be missing, so try repairing once.
		if uerr == nil && site == siteYouTube && !repaired && jsTrouble(logText) && !hasQualityAbove(items[0].Options, 360) {
			uerr = NewUserError("jsruntime")
			uerr.Details = tailLines(logText, 25)
		}
		if uerr != nil {
			if !repaired && (uerr.Outdated || uerr.JSRuntime) {
				repaired = true
				if onRepair != nil {
					onRepair(site)
				}
				if a.repair(ctx, uerr) || uerr.Code == "jsruntime" {
					continue
				}
			}
			if uerr.Code == "jsruntime" && len(items) > 0 {
				uerr = nil // we still have something to offer
			} else {
				return nil, nil, fail(uerr)
			}
		}

		// Keep what yt-dlp found for each video, so the download starts right away.
		stamp := time.Now().UnixNano()
		fitems := make([]fetchedItem, 0, len(items))
		for i, it := range items {
			p := filepath.Join(a.paths.Work, fmt.Sprintf("info-%d-%d-%d.json", stamp, seq, i))
			if err := os.WriteFile(p, it.raw, 0o644); err != nil {
				p = ""
			}
			items[i].raw = nil // free the memory
			fitems = append(fitems, fetchedItem{Item: items[i], infoPath: p})
		}
		return &fetched{seq: seq, url: target, site: site, at: time.Now(), items: fitems, opts: opts, page: opts.page}, items, nil
	}
}

// pruneInfoFiles removes what yt-dlp found for older links (a YouTube answer can take a
// few MB), keeping the post on screen. Each download keeps its own copy.
func (a *App) pruneInfoFiles() {
	keep := map[string]bool{}
	a.mu.Lock()
	posts := []*fetched{a.cur}
	for _, bp := range a.bposts {
		posts = append(posts, bp.f)
	}
	for _, f := range posts {
		if f != nil {
			for _, it := range f.items {
				keep[filepath.Base(it.infoPath)] = true
			}
		}
	}
	a.mu.Unlock()
	entries, _ := os.ReadDir(a.paths.Work)
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasPrefix(n, "info-") || !strings.HasSuffix(n, ".json") || keep[n] {
			continue
		}
		// A minute's grace: a link being read right now has written its files but is not "cur" yet.
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > time.Minute {
			_ = os.Remove(filepath.Join(a.paths.Work, n))
		}
	}
}

// fetchThumbnails loads the pictures of social-media posts here instead of in the page:
// Instagram and TikTok refuse to show them inside other programs.
func (a *App) fetchThumbnails(f *fetched) {
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				log.Printf("thumbnails: %v", r)
			}
		}()
		for i, it := range f.items {
			if i >= 12 {
				return
			}
			src := it.Meta.Thumbnail
			if src == "" || strings.HasPrefix(src, "data:") {
				continue
			}
			a.mu.Lock()
			stale := f.seq != a.fetchSeq
			a.mu.Unlock()
			if stale || a.ctx.Err() != nil {
				return
			}
			data, err := fetchImageDataURL(a.ctx, a.tools.client, src, siteReferer(f.site))
			if err != nil {
				log.Printf("thumbnail %d: %v", i, err)
				continue
			}
			a.mu.Lock()
			f.items[i].Meta.Thumbnail = data // so the page shows it when it draws the post again
			if a.lastInfo != nil && a.lastInfo.Seq == f.seq && i < len(a.lastInfo.Items) {
				a.lastInfo.Items[i].Meta.Thumbnail = data
			}
			a.mu.Unlock()
			a.emit.Emit("thumb", map[string]any{"seq": f.seq, "index": i, "src": data})
			a.thumbArrived(f.url, it.Index, data)
		}
	}()
}

// thumbArrived gives the picture to downloads added before it arrived.
func (a *App) thumbArrived(url string, index int, data string) {
	var got []*DownloadTask
	a.mu.Lock()
	for _, t := range a.tasks {
		if t.URL == url && t.Index == index && !strings.HasPrefix(t.thumb, "data:") {
			got = append(got, t)
		}
	}
	a.mu.Unlock()
	if len(got) == 0 {
		return
	}
	small := smallPicture(data)
	for _, t := range got {
		a.mu.Lock()
		t.thumb = small
		a.mu.Unlock()
		a.dl.writeThumb(t.ID, small)
		a.emitTask("update", t, true)
	}
}

func jsTrouble(logText string) bool {
	l := strings.ToLower(logText)
	return strings.Contains(l, "challenge solving failed") || strings.Contains(l, "javascript runtime") ||
		strings.Contains(l, "js challenge") || strings.Contains(l, "no supported javascript")
}

func hasQualityAbove(opts []QualityOption, h int) bool {
	for _, o := range opts {
		if o.Kind == "video" && o.Height > h {
			return true
		}
	}
	return false
}

func (a *App) fetchJSON(ctx context.Context, link, site string, opts fetchOpts) ([]byte, string, *UserError) {
	release := a.tools.Acquire()
	args := append(a.tools.YtdlpArgs(a.paths.Cache), "-J", "--no-playlist", "--flat-playlist")
	cookies := ""
	if len(opts.cookies) > 0 {
		cookies = filepath.Join(a.paths.Work, fmt.Sprintf("cookies-read-%d.txt", time.Now().UnixNano()))
		if err := writeNetscapeCookies(cookies, opts.cookies); err == nil {
			args = append(args, "--cookies", cookies)
			defer os.Remove(cookies)
		}
	}
	args = append(args, browserArgs(opts.referer, opts.userAgent)...)
	args = append(args, "--", link)
	if f := devInfoJSON(a.tools); f != "" {
		args = append(a.tools.YtdlpArgs(a.paths.Cache), "-J", "--no-clean-info-json", "--load-info-json", f)
	}
	res, err := runTool(ctx, a.tools.YtdlpExe(), args, a.tools.Env(), true, nil)
	release()
	if errors.Is(err, errCancelled) {
		return nil, "", nil
	}
	out := bytes.TrimSpace(res.Stdout)
	// A post can come back with some parts failed (the photos of an Instagram carousel):
	// yt-dlp then still prints what it found, but ends with an error code.
	if len(out) > 0 && out[0] == '{' && json.Valid(out) {
		return out, res.Log, nil
	}
	if err != nil && strings.TrimSpace(res.Log) == "" {
		return nil, "", a.brokenTool(err)
	}
	return nil, res.Log, ClassifyFor(res.Log, site)
}

// brokenTool handles yt-dlp failing to start at all (deleted or blocked by an antivirus):
// it is removed so that the setup screen downloads a fresh copy.
func (a *App) brokenTool(err error) *UserError {
	log.Printf("yt-dlp did not start: %v", err)
	a.tools.MarkBroken(toolYtdlp)
	e := NewUserError("tools_missing")
	e.Details = err.Error()
	return e
}

// repair updates yt-dlp and/or Deno. It returns true when something changed.
func (a *App) repair(ctx context.Context, uerr *UserError) bool {
	changed := false
	if uerr.JSRuntime {
		if ok, err := a.tools.RepairDeno(ctx, 6*time.Hour); err != nil {
			log.Printf("repair deno: %v", err)
		} else if ok {
			changed = true
		}
	}
	if uerr.Outdated || uerr.JSRuntime {
		if ok, err := a.tools.UpdateYtdlp(ctx, 15*time.Minute); err != nil {
			log.Printf("update yt-dlp: %v", err)
		} else if ok {
			changed = true
		}
	}
	return changed
}

// ---------------------------------------------------------------------------
// The downloads list
// ---------------------------------------------------------------------------

func (a *App) find(id string) *DownloadTask {
	for _, t := range a.tasks {
		if t.ID == id {
			return t
		}
	}
	return nil
}

func (a *App) newID() string {
	a.idSeq++
	return "d" + strconv.FormatInt(time.Now().UnixMilli(), 36) + strconv.FormatInt(int64(a.idSeq), 36)
}

// jobDir is the task's temporary folder, inside the folder it saves to (call with the lock held).
func jobDirOf(t *DownloadTask) string { return filepath.Join(t.SaveDir, tempFolderName, t.ID) }

func (a *App) activeCountLocked() int {
	n := 0
	for _, t := range a.tasks {
		if t.active() {
			n++
		}
	}
	return n
}

// ActiveCount is the number of downloads that run or wait.
func (a *App) ActiveCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.activeCountLocked()
}

// TaskList is the whole list for the page, newest first, with pictures.
func (a *App) TaskList() []map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.taskListLocked()
}

func (a *App) taskListLocked() []map[string]any {
	out := []map[string]any{}
	for i := len(a.tasks) - 1; i >= 0; i-- {
		out = append(out, a.tasks[i].toJSON(true))
	}
	return out
}

// Progress sums up the running downloads for the taskbar button: how many run or wait,
// and the share downloaded (-1 when nothing can be measured yet).
func (a *App) Progress() (active int, percent float64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var sum float64
	running := 0
	for _, t := range a.tasks {
		if t.active() {
			active++
		}
		if t.State == stateRunning {
			running++
			sum += t.Percent
		}
	}
	if running == 0 || sum <= 0 {
		return active, -1
	}
	return active, sum / float64(running)
}

type taskEvent struct {
	Op   string         `json:"op"` // add | update | remove
	Task map[string]any `json:"task,omitempty"`
	ID   string         `json:"id,omitempty"`
}

func (a *App) emitTask(op string, t *DownloadTask, withThumb bool) {
	a.mu.Lock()
	t.rev++
	ev := taskEvent{Op: op, Task: t.toJSON(withThumb)}
	a.mu.Unlock()
	a.emit.Emit("task", ev)
}

func (a *App) emitRemoved(id string) { a.emit.Emit("task", taskEvent{Op: "remove", ID: id}) }

// forgetTask cleans up after an entry left the list (it is not running).
func (a *App) forgetTask(t *DownloadTask) {
	a.dl.deleteThumb(t.ID)
	a.dropCookies(t.ID)
	a.emitRemoved(t.ID)
}

// saveTasks writes the list now (force) or at most every few seconds (progress).
func (a *App) saveTasks(force bool) {
	a.mu.Lock()
	if !force && time.Since(a.lastStoreWrite) < 3*time.Second {
		a.mu.Unlock()
		return
	}
	a.lastStoreWrite = time.Now()
	text := serializeTasks(a.tasks)
	a.storeSeq++
	seq := a.storeSeq
	a.mu.Unlock()
	a.dl.write(text, seq)
}

// saveTasksSoon writes the list without making the window wait.
func (a *App) saveTasksSoon() {
	a.saves.Add(1)
	go func() {
		defer a.saves.Done()
		a.saveTasks(true)
	}()
}

// trimFinishedLocked keeps the newest finished downloads in the list.
func (a *App) trimFinishedLocked() []*DownloadTask {
	var drop []*DownloadTask
	finished := 0
	for i := len(a.tasks) - 1; i >= 0; i-- {
		if t := a.tasks[i]; t.finished() {
			finished++
			if finished > keepFinished {
				drop = append(drop, t)
			}
		}
	}
	a.removeLocked(drop...)
	return drop
}

func (a *App) removeLocked(drop ...*DownloadTask) {
	if len(drop) == 0 {
		return
	}
	gone := map[*DownloadTask]bool{}
	for _, t := range drop {
		gone[t] = true
	}
	kept := a.tasks[:0]
	for _, t := range a.tasks {
		if !gone[t] {
			kept = append(kept, t)
		}
	}
	for i := len(kept); i < len(a.tasks); i++ {
		a.tasks[i] = nil
	}
	a.tasks = kept
}

// fileBaseRoom keeps paths short: yt-dlp's Python can trip over paths longer than
// Windows' classic limit of 260 characters.
func fileBaseRoom(saveDir string) int {
	room := 200 - len([]rune(saveDir)) - 60
	return max(min(room, 90), 24)
}

// Download adds video number item (0-based, in the order shown) of the post on screen in
// the chosen quality to the downloads. It returns {"id": …} (with "existing": true when
// that download was already in the list), or {"error": …}.
func (a *App) Download(item int, key string) map[string]any { return a.DownloadWith(item, key, false) }

// DownloadWith is Download, with the music removed (noMusic) from the video or the MP3.
func (a *App) DownloadWith(item int, key string, noMusic bool) map[string]any {
	a.mu.Lock()
	f := a.cur
	a.mu.Unlock()
	return a.downloadFrom(f, item, key, noMusic)
}

// downloadFrom is DownloadWith for the post f (the one on screen, or one read for the
// browser's menu).
func (a *App) downloadFrom(f *fetched, item int, key string, noMusic bool) map[string]any {
	return a.downloadTo(f, item, key, noMusic, dlTarget{})
}

// downloadInto downloads a video into a folder of its own, under a name of its own (a
// product's video: product.go).
func (a *App) downloadInto(f *fetched, item int, key, dir, name string) map[string]any {
	return a.downloadTo(f, item, key, false, dlTarget{dir: dir, name: name})
}

// dlTarget is where a download goes when not where its kind goes (zero: as usual), and
// the part of the video to keep (clipEnd > 0: only from clipStart to clipEnd seconds).
type dlTarget struct {
	dir, name          string
	clipStart, clipEnd float64
}

func (a *App) downloadTo(f *fetched, item int, key string, noMusic bool, tg dlTarget) map[string]any {
	a.mu.Lock()
	var opt *QualityOption
	var it *fetchedItem
	if f != nil && item >= 0 && item < len(f.items) {
		it = &f.items[item]
		for i := range it.Options {
			if it.Options[i].Key == key {
				o := it.Options[i]
				opt = &o
			}
		}
	}
	var existing *DownloadTask
	existingState := ""
	if opt != nil {
		for _, x := range a.tasks {
			if !x.finished() && x.URL == f.url && x.Index == it.Index && x.Key == key && x.NoMusic == noMusic &&
				x.ClipStart == tg.clipStart && x.ClipEnd == tg.clipEnd && (tg.dir == "" || x.SaveDir == tg.dir) {
				existing, existingState = x, x.State
			}
		}
	}
	var meta VideoMeta
	if it != nil {
		meta = it.Meta
	}
	a.mu.Unlock()
	if opt == nil {
		return map[string]any{"error": NewUserError("format").Localize("")}
	}
	if existing != nil {
		// Already in the list: continue it instead of downloading it twice.
		if existingState == statePaused || existingState == stateError {
			a.Resume(existing.ID)
		}
		return map[string]any{"id": existing.ID, "existing": true}
	}
	a.store.Update(func(s *Settings) { s.LastKind = opt.Kind })

	cat := catVideo
	if opt.Kind == "audio" {
		cat = catMusic
	}
	saveDir := a.saveDirFor(cat)
	if tg.dir != "" {
		saveDir = tg.dir
	}
	title := meta.Title
	if len(f.items) > 1 {
		title += fmt.Sprintf(" - %d", item+1) // several videos in one post
	}
	if tg.clipEnd > 0 {
		title += " - " + clipNamePart(tg.clipStart, tg.clipEnd)
	}
	base := SafeFileBase(title, fileBaseRoom(saveDir))
	if opt.Kind == "video" {
		base += " - " + opt.LabelText()
	}
	if noMusic {
		base += noMusicSuffix()
	}
	if tg.name != "" {
		base = SafeFileBase(tg.name, fileBaseRoom(saveDir))
	}
	now := time.Now().UnixMilli()
	t := &DownloadTask{URL: f.url, Site: f.site, Index: it.Index, Title: meta.Title, Channel: meta.Channel,
		DurationText: meta.DurationText, Key: opt.Key, Kind: opt.Kind, Label: opt.Label, SizeBytes: opt.SizeBytes,
		FormatSpec: opt.FormatSpec, SortSpec: opt.SortSpec, AudioQuality: opt.AudioQuality,
		Parts: append([]dlPart(nil), opt.Parts...), Base: base, SaveDir: saveDir, FetchedAt: f.at.UnixMilli(),
		CreatedAt: now, QueuedAt: now, State: stateQueued, ETA: -1, thumb: meta.Thumbnail, adding: true,
		Category: cat, Page: f.page, Src: FileSource{Referer: f.opts.referer, UserAgent: f.opts.userAgent}, NoMusic: noMusic,
		ClipStart: tg.clipStart, ClipEnd: tg.clipEnd}
	if tg.clipEnd > 0 {
		// The part only: its length, and its share of the sizes.
		share := (tg.clipEnd - tg.clipStart) / math.Max(meta.Duration, tg.clipEnd)
		if share > 0 && share < 1 {
			t.SizeBytes = int64(float64(t.SizeBytes) * share)
			for i := range t.Parts {
				t.Parts[i].Bytes = int64(float64(t.Parts[i].Bytes) * share)
			}
		}
		t.DurationText = clockText(tg.clipEnd - tg.clipStart)
	}
	info := it.infoPath
	a.mu.Lock()
	t.ID = a.newID()
	a.tasks = append(a.tasks, t)
	dropped := a.trimFinishedLocked()
	a.mu.Unlock()
	a.keepCookies(t.ID, f.opts.cookies)
	for _, d := range dropped {
		a.forgetTask(d)
	}
	a.saveTasksSoon()
	a.emitTask("add", t, true)

	// Off the window's thread: the button answers at once.
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				panicError("add", r)
				a.mu.Lock()
				t.adding = false
				a.mu.Unlock()
				a.schedule()
			}
		}()
		// The temporary folder keeps the partly downloaded files until the download is
		// finished or deleted, and what yt-dlp found, so the download starts without
		// asking the site again (renamed when complete, so it is never read half-copied).
		a.mu.Lock()
		job := jobDirOf(t)
		a.mu.Unlock()
		if err := os.MkdirAll(job, 0o755); err == nil {
			hideFolder(filepath.Dir(job))
			if info != "" && fileOK(info) {
				tmp := filepath.Join(job, "info.json.tmp")
				if err := copyFile(info, tmp); err == nil {
					if err := os.Rename(tmp, filepath.Join(job, "info.json")); err != nil {
						_ = os.Remove(tmp)
					}
				} else {
					_ = os.Remove(tmp)
					log.Printf("keeping the video's info: %v", err)
				}
			}
		}
		// A small copy of the picture for the list (the full one can be large).
		small := meta.Thumbnail
		if strings.HasPrefix(small, "data:") {
			small = smallPicture(small)
		}
		a.mu.Lock()
		t.thumb, t.adding = small, false
		a.mu.Unlock()
		a.dl.writeThumb(t.ID, small)
		a.schedule()
	}()
	return map[string]any{"id": t.ID}
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(to)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// schedule starts waiting downloads while fewer than the settings allow run.
func (a *App) schedule() {
	type start struct {
		t   *DownloadTask
		ctx context.Context
	}
	var starts []start
	a.mu.Lock()
	if a.shuttingDown {
		a.mu.Unlock()
		return
	}
	// Two lanes: the downloads (as many as the settings allow), and removing the music
	// (one file at a time; a download whose music is being removed no longer takes a
	// download's place).
	downloads, seps := 0, 0
	for _, t := range a.tasks {
		if t.State == stateRunning {
			if t.isSep() || t.sepPhase {
				seps++
			} else {
				downloads++
			}
		}
	}
	// First come, first served: one whose folder is still being prepared starts as soon
	// as it is ready (its preparation calls schedule again).
	next := func(sep bool) *DownloadTask {
		var n *DownloadTask
		for _, t := range a.tasks {
			if t.State == stateQueued && t.isSep() == sep && (n == nil || t.QueuedAt < n.QueuedAt) {
				n = t
			}
		}
		if n == nil || n.adding {
			return nil
		}
		return n
	}
	begin := func(t *DownloadTask) {
		ctx, cancel := context.WithCancel(context.Background())
		t.State, t.Stage, t.Err, t.sepPhase = stateRunning, "preparing", nil, false
		t.cancel, t.stopReason, t.resumeAfterStop = cancel, "", false
		t.Speed, t.ETA = 0, -1
		starts = append(starts, start{t, ctx})
	}
	for limit := a.store.Get().parallel(); downloads < limit; downloads++ {
		t := next(false)
		if t == nil {
			break
		}
		begin(t)
	}
	if seps == 0 {
		if t := next(true); t != nil {
			begin(t)
		}
	}
	if len(starts) > 0 {
		a.wg.Add(len(starts))
	}
	a.mu.Unlock()
	for _, s := range starts {
		a.emitTask("update", s.t, false)
		go func(t *DownloadTask, ctx context.Context) {
			defer a.wg.Done()
			var o outcome
			func() {
				defer func() {
					if r := recover(); r != nil {
						o = outcome{err: panicError("download", r)}
					}
				}()
				o = a.runTask(ctx, t)
			}()
			a.finishTask(t, ctx, o)
		}(s.t, s.ctx)
	}
	if len(starts) > 0 {
		a.saveTasks(true)
	}
}

// Pause stops a download from pulling data; what it downloaded is kept.
func (a *App) Pause(id string) {
	var stop context.CancelFunc
	save := false
	a.mu.Lock()
	t := a.find(id)
	switch {
	case t == nil:
		a.mu.Unlock()
		return
	case t.State == stateQueued:
		t.State, t.Stage = statePaused, ""
		save = true
	case t.State == stateRunning && t.cancel != nil:
		if t.stopReason == "" {
			t.stopReason = "pause"
		}
		t.resumeAfterStop = false
		stop = t.cancel
	default:
		a.mu.Unlock()
		return
	}
	a.mu.Unlock()
	a.emitTask("update", t, false)
	if stop != nil {
		stop() // the download goroutine then marks it paused
	}
	if save {
		a.saveTasksSoon()
	}
}

// PauseAll pauses everything that runs or waits.
func (a *App) PauseAll() {
	var ids []string
	a.mu.Lock()
	for _, t := range a.tasks {
		if t.active() {
			ids = append(ids, t.ID)
		}
	}
	a.mu.Unlock()
	for _, id := range ids {
		a.Pause(id)
	}
}

// Resume continues a paused or failed download from where it stopped.
func (a *App) Resume(id string) {
	a.mu.Lock()
	t := a.find(id)
	switch {
	case t == nil:
		a.mu.Unlock()
		return
	case t.cancel != nil:
		// Still stopping: it continues as soon as it has stopped.
		if t.stopReason != "pause" {
			a.mu.Unlock()
			return
		}
		t.resumeAfterStop = true
	case t.State == statePaused || t.State == stateError:
		t.State, t.Err, t.Stage = stateQueued, nil, ""
		t.QueuedAt = time.Now().UnixMilli()
	default:
		a.mu.Unlock()
		return
	}
	a.mu.Unlock()
	a.saveTasksSoon()
	a.emitTask("update", t, false)
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		a.schedule()
	}()
}

// MoveTask makes a stopped download save to another folder (after "choose another
// place"), carrying what it downloaded along when the folder is on the same drive,
// and resumes it.
func (a *App) MoveTask(id, dir string) {
	a.mu.Lock()
	t := a.find(id)
	if t == nil || t.cancel != nil || t.finished() || strings.EqualFold(filepath.Clean(t.SaveDir), filepath.Clean(dir)) {
		a.mu.Unlock()
		if t != nil {
			a.Resume(id)
		}
		return
	}
	if t.State == stateQueued {
		t.State = statePaused // keep it from starting while its files move; Resume queues it again
	}
	oldJob := jobDirOf(t)
	t.SaveDir = dir
	newJob := jobDirOf(t)
	a.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(newJob), 0o755); err == nil {
		hideFolder(filepath.Dir(newJob))
		if err := os.Rename(oldJob, newJob); err != nil {
			// Another drive: start over there, and free the space taken here.
			_ = os.RemoveAll(oldJob)
		}
		_ = os.Remove(filepath.Dir(oldJob))
	}
	a.Resume(id)
}

// MoveTaskToSaveDir moves a stopped download to where downloads of its kind go now (after
// the save folder changed), and continues it.
func (a *App) MoveTaskToSaveDir(id string) {
	a.mu.Lock()
	t := a.find(id)
	cat := ""
	if t != nil {
		cat = taskCategory(t)
	}
	a.mu.Unlock()
	if t != nil {
		a.MoveTask(id, a.saveDirFor(cat))
	}
}

// Delete cancels a download and deletes what it downloaded so far. For a finished
// download it only removes the entry from the list (the file stays in its folder).
func (a *App) Delete(id string) {
	a.mu.Lock()
	t := a.find(id)
	if t == nil {
		a.mu.Unlock()
		return
	}
	if t.cancel != nil {
		t.stopReason, t.resumeAfterStop = "delete", false
		stop := t.cancel
		a.mu.Unlock()
		a.emitTask("update", t, false)
		stop() // the download goroutine removes it once the program has stopped
		return
	}
	a.removeLocked(t)
	job := jobDirOf(t)
	done := t.finished()
	a.mu.Unlock()
	a.dl.deleteThumb(t.ID)
	a.dropCookies(t.ID)
	a.saveTasksSoon()
	a.emitRemoved(t.ID)
	if !done {
		a.wg.Add(1)
		go func() {
			defer a.wg.Done()
			removeJob(job)
		}()
	}
}

// removeJob deletes a task's temporary folder, and the parent folder if it is now empty.
func removeJob(job string) {
	for i := 0; i < 5; i++ {
		if err := os.RemoveAll(job); err == nil {
			break
		}
		time.Sleep(time.Duration(300*(i+1)) * time.Millisecond) // a file still held for a moment
	}
	_ = os.Remove(filepath.Dir(job))
}

// ClearFinished removes the finished downloads from the list (the files stay).
func (a *App) ClearFinished() {
	var drop []*DownloadTask
	a.mu.Lock()
	for _, t := range a.tasks {
		if t.finished() {
			drop = append(drop, t)
		}
	}
	a.removeLocked(drop...)
	a.mu.Unlock()
	if len(drop) == 0 {
		return
	}
	a.saveTasksSoon()
	for _, t := range drop {
		a.dl.deleteThumb(t.ID)
		a.emitRemoved(t.ID)
	}
}

// outcome is how one run of a download ended.
type outcome struct {
	path    string // the finished file
	size    int64
	err     *UserError
	stopped bool
}

func (a *App) finishTask(t *DownloadTask, ctx context.Context, o outcome) {
	removed, deleteJob := false, false
	var dropped []*DownloadTask
	a.mu.Lock()
	cancel := t.cancel
	t.cancel = nil
	reason := t.stopReason
	t.stopReason = ""
	job := jobDirOf(t)
	switch {
	case reason == "delete":
		a.removeLocked(t)
		removed, deleteJob = true, true
	case o.path != "":
		t.State, t.Path, t.FileSize = stateDone, o.path, o.size
		t.FinishedAt = time.Now().UnixMilli()
		t.Percent = 100
		deleteJob = true
		dropped = a.trimFinishedLocked()
	case o.stopped || o.err == nil || reason == "pause" || ctx.Err() != nil:
		if t.resumeAfterStop && !a.shuttingDown {
			t.State, t.QueuedAt = stateQueued, time.Now().UnixMilli()
		} else {
			t.State = statePaused
		}
	default:
		t.State, t.Err = stateError, o.err
	}
	t.resumeAfterStop, t.sepPhase = false, false
	t.Stage, t.Speed, t.ETA = "", 0, -1
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if deleteJob {
		removeJob(job)
		a.dropCookies(t.ID)
	}
	if removed {
		a.dl.deleteThumb(t.ID)
	}
	for _, d := range dropped {
		a.dl.deleteThumb(d.ID)
		a.dropCookies(d.ID)
	}
	a.saveTasks(true)
	if removed {
		a.emitRemoved(t.ID)
	} else {
		a.emitTask("update", t, false)
	}
	for _, d := range dropped {
		a.emitRemoved(d.ID)
	}
	a.schedule()
}

// ---------------------------------------------------------------------------
// Running one download
// ---------------------------------------------------------------------------

func (a *App) setStage(t *DownloadTask, stage string) {
	a.mu.Lock()
	t.Stage = stage
	a.mu.Unlock()
	a.emitTask("update", t, false)
}

// dropHalfConverted: a conversion to MP3 that was stopped half-way leaves a short
// "name.mp3" next to the downloaded sound; yt-dlp would take it for the finished file, so
// it is removed and the conversion runs again.
func dropHalfConverted(tmp, base string) {
	mp3 := base + ".mp3"
	entries, _ := os.ReadDir(tmp)
	source := false
	for _, e := range entries {
		n := e.Name()
		if !strings.HasPrefix(n, base+".") || n == mp3 {
			continue
		}
		rest := n[len(base)+1:]
		if strings.HasPrefix(rest, "temp.") || strings.HasPrefix(rest, "orig.") {
			_ = os.Remove(filepath.Join(tmp, n))
			continue
		}
		if !strings.HasSuffix(n, ".part") && !strings.HasSuffix(n, ".ytdl") && !strings.Contains(rest, ".part-Frag") {
			source = true
		}
	}
	if source {
		_ = os.Remove(filepath.Join(tmp, mp3))
	}
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func (a *App) runTask(ctx context.Context, t *DownloadTask) outcome {
	fail := func(e *UserError) outcome { return outcome{err: e} }
	a.mu.Lock()
	isFile, isSep := t.isFile(), t.isSep()
	a.mu.Unlock()
	if isFile {
		return a.runFileTask(ctx, t)
	}
	if isSep {
		return a.runSepTask(ctx, t)
	}
	if len(a.tools.Missing()) > 0 {
		return fail(NewUserError("tools_missing"))
	}
	saveDir, uerr := a.prepareSaveDir(t)
	if uerr != nil {
		return fail(uerr)
	}
	a.mu.Lock()
	kind, base, site, sizeBytes, noMusic := t.Kind, t.Base, t.Site, t.SizeBytes, t.NoMusic
	parts, fetchedAt := t.Parts, time.UnixMilli(t.FetchedAt)
	a.mu.Unlock()
	tempRoot := filepath.Join(saveDir, tempFolderName)
	job := filepath.Join(tempRoot, t.ID)
	out, tmp := filepath.Join(job, "out"), filepath.Join(job, "tmp")
	for _, d := range []string{out, tmp} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			e := NewUserError("permission")
			e.Details = err.Error()
			return fail(e)
		}
	}
	hideFolder(tempRoot)
	if noMusic {
		// Downloaded before a pause: only the music is left to remove.
		if p := downloadedFile(job); p != "" {
			return a.finishNoMusic(ctx, t, p, job, saveDir)
		}
	}
	if sizeBytes > 0 {
		// The parts and the joined file; what was downloaded before a pause already counts.
		need := sizeBytes*21/10 + 50<<20 - treeSize(job)
		if noMusic && kind == "video" {
			need += sizeBytes // the copy without music
		}
		if free, ok := freeSpace(saveDir); ok && free < need {
			e := NewUserError("disk")
			e.Need, e.Free, e.Folder = need, free, saveDir
			return fail(e)
		}
	}
	if kind == "audio" {
		dropHalfConverted(tmp, base)
	}

	ext := "mp4"
	if kind == "audio" && !noMusic {
		ext = "mp3"
	}
	info := filepath.Join(job, "info.json")
	useInfo := fileOK(info) && time.Since(fetchedAt) < 3*time.Hour
	retried := false
	for {
		tracker := NewProgressTracker(kind, parts)
		infoArg := ""
		if useInfo {
			infoArg = info
		}
		path, logText, err := a.ytdlpDownload(ctx, t, infoArg, out, tmp, tracker)
		if ctx.Err() != nil {
			return outcome{stopped: true}
		}
		if err == nil {
			if path == "" || !fileOK(path) {
				path = filepath.Join(out, base+"."+ext)
			}
			if !fileOK(path) && noMusic {
				path = onlyFileIn(out) // the sound keeps the extension it came with
			}
			if !fileOK(path) {
				e := NewUserError("unknown")
				e.Details = tailLines(logText, 25)
				return fail(e.Localize(site))
			}
			if noMusic {
				markDownloaded(job, path)
				return a.finishNoMusic(ctx, t, path, job, saveDir)
			}
			return a.saveFile(ctx, t, path, saveDir, tracker)
		}
		if strings.TrimSpace(logText) == "" {
			return fail(a.brokenTool(err))
		}
		uerr := ClassifyFor(logText, site)
		log.Printf("download failed (%s): %v", uerr.Code, err)
		if uerr.Code == "disk" || uerr.Code == "permission" {
			uerr.Folder = saveDir
		}
		if !retried {
			retry := false
			switch uerr.Code {
			case "forbidden", "format", "unknown", "extractor", "network":
				// The links saved from the first look may have expired: ask the site again.
				// The part already downloaded is kept and continued.
				if useInfo {
					useInfo, retry = false, true
					_ = os.Remove(info)
				}
			}
			if uerr.Outdated || uerr.JSRuntime {
				a.setStage(t, "updating")
				if a.repair(ctx, uerr) {
					useInfo, retry = false, true
				}
			}
			if retry && ctx.Err() == nil {
				retried = true
				continue
			}
		}
		if ctx.Err() != nil {
			return outcome{stopped: true}
		}
		return fail(uerr)
	}
}

func (a *App) sendProgress(t *DownloadTask, o Overall) {
	a.mu.Lock()
	if t.cancel == nil {
		a.mu.Unlock()
		return // already finished
	}
	t.apply(o)
	a.mu.Unlock()
	a.emitTask("update", t, false)
	a.saveTasks(false)
}

// saveFile puts the finished file in its folder under a name no other file has.
func (a *App) saveFile(ctx context.Context, t *DownloadTask, file, saveDir string, tracker *ProgressTracker) outcome {
	o := tracker.OnPost("started", "MoveFiles")
	a.sendProgress(t, o)
	ext := strings.TrimPrefix(filepath.Ext(file), ".")
	base := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
	a.saveMu.Lock()
	defer a.saveMu.Unlock()
	dest := UniquePath(saveDir, base, ext)
	err := moveFile(ctx, file, dest)
	if err != nil {
		if ctx.Err() != nil {
			_ = os.Remove(dest)
			return outcome{stopped: true}
		}
		msg := strings.ToLower(err.Error())
		code := "permission"
		if strings.Contains(msg, "space") || strings.Contains(msg, "disk is full") {
			code = "disk"
		}
		e := NewUserError(code)
		e.Details, e.Folder = err.Error(), saveDir
		log.Printf("save failed: %v", err)
		return outcome{err: e.Localize(t.Site)}
	}
	var size int64
	if fi, err := os.Stat(dest); err == nil {
		size = fi.Size()
	}
	return outcome{path: dest, size: size}
}

// moveFile renames a file (instant on the same drive), copying it when that fails.
func moveFile(ctx context.Context, from, to string) error {
	var err error
	for i := 0; i < 6; i++ {
		if err = os.Rename(from, to); err == nil {
			return nil
		}
		if _, serr := os.Stat(from); serr != nil {
			return err
		}
		// An antivirus often keeps a new file open for a moment.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(150*(i+1)) * time.Millisecond):
		}
	}
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	buf := make([]byte, 1<<20)
	for {
		if ctx.Err() != nil {
			out.Close()
			os.Remove(to)
			return ctx.Err()
		}
		n, rerr := in.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				out.Close()
				os.Remove(to)
				return werr
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			out.Close()
			os.Remove(to)
			return rerr
		}
	}
	if err := out.Close(); err != nil {
		os.Remove(to)
		return err
	}
	in.Close()
	_ = os.Remove(from)
	return nil
}

func (a *App) ytdlpDownload(ctx context.Context, t *DownloadTask, info, out, tmp string, tracker *ProgressTracker) (string, string, error) {
	release := a.tools.Acquire()
	defer release()

	a.mu.Lock()
	kind, base, formatSpec, sortSpec, audioQuality, url, index := t.Kind, t.Base, t.FormatSpec, t.SortSpec, t.AudioQuality, t.URL, t.Index
	id, site, src, noMusic := t.ID, t.Site, t.Src, t.NoMusic
	clipStart, clipEnd := t.ClipStart, t.ClipEnd
	a.mu.Unlock()
	args := a.tools.YtdlpArgs(a.paths.Cache)
	args = append(args,
		"--newline", "--progress", "--progress-delta", "0.25",
		"--progress-template", progressTemplateDownload,
		"--progress-template", progressTemplatePost,
		"--print", printDoneTemplate,
		"--no-mtime", "--no-playlist",
		// The connection drops now and then: wait a little longer before giving up.
		"--retries", "15", "--fragment-retries", "15",
		"--retry-sleep", "http:exp=1:16", "--retry-sleep", "fragment:exp=1:16",
		"-f", formatSpec,
		"-P", "home:"+out, "-P", "temp:"+tmp,
		"-o", EscapeOutputTemplate(base)+".%(ext)s",
	)
	if sortSpec != "" {
		args = append(args, "-S", sortSpec)
	}
	if clipEnd > 0 {
		// A part only: ffmpeg cuts it from the site's files as it downloads, at exact times
		// (the cut is encoded again around it, quickly).
		args = append(args, "--download-sections", fmt.Sprintf("*%.3f-%.3f", clipStart, clipEnd), "--force-keyframes-at-cuts")
		if kind == "video" {
			args = append(args, "--downloader-args", "ffmpeg_o:-preset veryfast -crf 21")
		}
	}
	// A video found in the browser: its cookies (never YouTube's) and its page as Referer.
	if c := a.cookiesOf(id); len(c) > 0 && site != siteYouTube {
		p := strings.TrimSuffix(a.cookiePath(id), ".json") + ".txt"
		if err := writeNetscapeCookies(p, c); err == nil {
			args = append(args, "--cookies", p)
		}
	}
	args = append(args, browserArgs(src.Referer, src.UserAgent)...)
	switch {
	case kind == "audio" && noMusic:
		// The sound as it comes: it becomes an MP3 once, after the music is removed.
		args = append(args, "--embed-metadata")
	case kind == "audio":
		q := audioQuality
		if q == "" {
			q = "192K"
		}
		args = append(args, "-x", "--audio-format", "mp3", "--audio-quality", q, "--embed-metadata")
	default:
		args = append(args, "--merge-output-format", "mp4", "--remux-video", "mp4")
	}
	switch {
	case info != "":
		args = append(args, "--load-info-json", info)
	case index > 0:
		// One video of a post with several: ask for that one only.
		args = append(args, "--playlist-items", strconv.Itoa(index), "--", url)
	case devInfoJSON(a.tools) != "":
		args = append(args, "--load-info-json", devInfoJSON(a.tools)) // development: the local video
	default:
		args = append(args, "--", url)
	}

	var mu sync.Mutex
	var finalPath string
	var lastSent time.Time
	lastStage := ""
	send := func(o Overall, force bool) {
		mu.Lock()
		if !force && o.Stage == lastStage && time.Since(lastSent) < 250*time.Millisecond {
			mu.Unlock()
			return
		}
		lastSent, lastStage = time.Now(), o.Stage
		mu.Unlock()
		a.sendProgress(t, o)
	}
	res, err := runTool(ctx, a.tools.YtdlpExe(), args, a.tools.Env(), false, func(line string, _ bool) {
		if d, ok := parseDownloadLine(line); ok {
			mu.Lock()
			o := tracker.OnDownload(d)
			mu.Unlock()
			send(o, d.Status == "finished")
			return
		}
		if st, pp, ok := parsePostLine(line); ok {
			mu.Lock()
			o := tracker.OnPost(st, pp)
			mu.Unlock()
			if st == "started" {
				send(o, true)
			}
			return
		}
		if p, ok := parseDoneLine(line); ok {
			mu.Lock()
			finalPath = p
			mu.Unlock()
			return
		}
		if clipEnd > 0 {
			// ffmpeg cutting the part: how far it is, in the part's time.
			if sec, ok := parseFfmpegTime(line); ok {
				pct := math.Min(99, 100*sec/math.Max(0.5, clipEnd-clipStart))
				send(Overall{Stage: "clip", Percent: pct, ETA: -1}, false)
			}
		}
	})
	mu.Lock()
	defer mu.Unlock()
	if err != nil {
		return "", res.Log, err
	}
	return finalPath, res.Log, nil
}
