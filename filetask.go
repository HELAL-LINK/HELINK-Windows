package main

// Files from web addresses, next to the videos: added from a link pasted in the app or
// taken over from a browser, shown first in the "new download" window (name, size, kind,
// folder), then downloaded in parts (filedl.go) through the same list and queue as the
// videos.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"mime"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Kinds of files
// ---------------------------------------------------------------------------

const (
	catVideo      = "video"
	catMusic      = "music"
	catPictures   = "pictures"
	catPrograms   = "programs"
	catDocuments  = "documents"
	catCompressed = "compressed"
	catOther      = "other"
)

var categories = []string{catVideo, catMusic, catPictures, catPrograms, catDocuments, catCompressed, catOther}

var categoryOfExt = map[string]string{}

func init() {
	for cat, exts := range map[string]string{
		catVideo:      "mp4 mkv webm avi mov wmv flv m4v 3gp 3g2 mpg mpeg ts m2ts mts vob ogv asf rm rmvb qt f4v",
		catMusic:      "mp3 m4a aac wav flac ogg oga opus wma aif aiff ape mka mid midi amr",
		catPictures:   "jpg jpeg jfif png gif webp bmp tif tiff heic heif avif svg ico psd raw cr2 nef arw dng",
		catPrograms:   "exe msi msu msix appx appxbundle msixbundle apk xapk dmg pkg deb rpm bat cmd jar",
		catDocuments:  "pdf doc docx xls xlsx ppt pptx pps ppsx txt rtf odt ods odp epub csv md mobi azw3 djvu chm xps",
		catCompressed: "zip rar 7z gz gzip tar bz2 xz tgz tbz2 iso img cab zst lz lzh arj ace z sit sitx",
	} {
		for _, e := range strings.Fields(exts) {
			categoryOfExt[e] = cat
		}
	}
}

func validCategory(c string) bool {
	for _, x := range categories {
		if x == c {
			return true
		}
	}
	return false
}

// categoryOf is the kind of a file, by its extension (or else its content type).
func categoryOf(name, contentType string) string {
	if c, ok := categoryOfExt[extOf(name)]; ok {
		return c
	}
	mt, _, _ := mime.ParseMediaType(contentType)
	switch {
	case strings.HasPrefix(mt, "video/"):
		return catVideo
	case strings.HasPrefix(mt, "audio/"):
		return catMusic
	case strings.HasPrefix(mt, "image/"):
		return catPictures
	}
	return catOther
}

// categoryFolder is the folder of a kind of file, inside the save folder (English names,
// as Internet Download Manager's, so they stay when the language changes).
func categoryFolder(cat string) string {
	switch cat {
	case catVideo:
		return "Video"
	case catMusic:
		return "Music"
	case catPictures:
		return "Pictures"
	case catPrograms:
		return "Programs"
	case catDocuments:
		return "Documents"
	case catCompressed:
		return "Compressed"
	}
	return "Other"
}

// taskCategory is the kind of a download, also for those listed before kinds were kept.
// Call with the lock held.
func taskCategory(t *DownloadTask) string {
	switch {
	case validCategory(t.Category):
		return t.Category
	case t.Kind == "video":
		return catVideo
	case t.Kind == "audio":
		return catMusic
	}
	return categoryOf(t.FileName, "")
}

// isCategoryFolder reports whether name is that of a kind's folder.
func isCategoryFolder(name string) bool {
	for _, c := range categories {
		if strings.EqualFold(name, categoryFolder(c)) {
			return true
		}
	}
	return false
}

// saveDirFor is where a new download of that kind goes.
func (a *App) saveDirFor(cat string) string {
	dir := a.SaveDir()
	if !a.store.Get().NoCategoryDirs {
		dir = filepath.Join(dir, categoryFolder(cat))
	}
	return dir
}

// uniqueFilePath is dir/name, or "name (2).ext"… when that file already exists.
func uniqueFilePath(dir, name string) string {
	base, ext := splitName(name)
	if ext != "" && base != "" {
		return UniquePath(dir, base, ext)
	}
	p := filepath.Join(dir, name)
	for i := 2; pathExists(p) && i < 1000; i++ {
		p = filepath.Join(dir, fmt.Sprintf("%s (%d)", name, i))
	}
	return p
}

// ---------------------------------------------------------------------------
// The browser's cookies for a download (kept in the app's own folder, never in the list)
// ---------------------------------------------------------------------------

func (a *App) cookiePath(id string) string { return filepath.Join(a.paths.Work, "cookies", id+".json") }

func (a *App) keepCookies(id string, c []Cookie) {
	p := a.cookiePath(id)
	if len(c) == 0 {
		_ = os.Remove(p)
		return
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	b, _ := json.Marshal(c)
	if err := os.WriteFile(p, b, 0o600); err != nil {
		log.Printf("keeping the cookies of %s: %v", id, err)
	}
}

func (a *App) cookiesOf(id string) []Cookie {
	b, err := os.ReadFile(a.cookiePath(id))
	if err != nil {
		return nil
	}
	var c []Cookie
	_ = json.Unmarshal(b, &c)
	return c
}

func (a *App) dropCookies(id string) {
	_ = os.Remove(a.cookiePath(id))
	_ = os.Remove(strings.TrimSuffix(a.cookiePath(id), ".json") + ".txt")
}

// pruneCookies removes the cookies of downloads that are gone.
func (a *App) pruneCookies(ids map[string]bool) {
	dir := filepath.Join(a.paths.Work, "cookies")
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		n := e.Name()
		id := strings.TrimSuffix(strings.TrimSuffix(n, ".json"), ".txt")
		if !ids[id] {
			_ = os.Remove(filepath.Join(dir, n))
		}
	}
}

// writeNetscapeCookies writes cookies in the format yt-dlp reads (--cookies).
func writeNetscapeCookies(path string, cookies []Cookie) error {
	var b strings.Builder
	b.WriteString("# Netscape HTTP Cookie File\n")
	for _, c := range cookies {
		if c.Name == "" || strings.ContainsAny(c.Name+c.Value+c.Domain+c.Path, "\t\r\n") {
			continue
		}
		domain, sub := strings.ToLower(c.Domain), "TRUE"
		if c.HostOnly {
			domain, sub = strings.TrimPrefix(domain, "."), "FALSE"
		} else if !strings.HasPrefix(domain, ".") {
			domain = "." + domain
		}
		p := c.Path
		if p == "" {
			p = "/"
		}
		secure := "FALSE"
		if c.Secure {
			secure = "TRUE"
		}
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n", domain, sub, p, secure, int64(c.Expires), c.Name, c.Value)
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

// browserArgs makes yt-dlp ask like the browser did (only for sites it has no reader of
// its own for: a site's reader sets what that site needs).
func browserArgs(referer, userAgent string) []string {
	var args []string
	if referer != "" {
		args = append(args, "--add-headers", "Referer:"+referer)
	}
	if userAgent != "" {
		args = append(args, "--add-headers", "User-Agent:"+userAgent)
	}
	return args
}

// ---------------------------------------------------------------------------
// The "new download" window
// ---------------------------------------------------------------------------

// pendingFile is a file shown in the "new download" window, waiting for Start, Later
// or Cancel.
type pendingFile struct {
	id   string
	src  FileSource
	page string
	from string // "browser" or "link"
	info FileInfo
	at   time.Time
}

func (a *App) addPending(src FileSource, page, from string) *pendingFile {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pending == nil {
		a.pending = map[string]*pendingFile{}
	}
	for id, p := range a.pending {
		if time.Since(p.at) > 6*time.Hour {
			delete(a.pending, id)
		}
	}
	a.pendSeq++
	p := &pendingFile{id: "p" + strconv.Itoa(a.pendSeq), src: src, page: page, from: from, at: time.Now()}
	a.pending[p.id] = p
	return p
}

func (a *App) takePending(id string) *pendingFile {
	a.mu.Lock()
	defer a.mu.Unlock()
	p := a.pending[id]
	delete(a.pending, id)
	return p
}

func (a *App) putPending(p *pendingFile) {
	a.mu.Lock()
	a.pending[p.id] = p
	a.mu.Unlock()
}

// PendingFiles lists the files waiting in the "new download" window (for a page that
// opens late, or again).
func (a *App) PendingFiles() []map[string]any {
	a.mu.Lock()
	var ps []*pendingFile
	for _, p := range a.pending {
		if p.info.Name != "" {
			ps = append(ps, p)
		}
	}
	a.mu.Unlock()
	out := []map[string]any{}
	for _, p := range ps {
		out = append(out, a.pendingEvent(p))
	}
	return out
}

// pendingEvent is what the "new download" window shows about a file.
func (a *App) pendingEvent(p *pendingFile) map[string]any {
	a.mu.Lock()
	info, src, page, from, id, at := p.info, p.src, p.page, p.from, p.id, p.at
	a.mu.Unlock()
	cat := categoryOf(info.Name, info.ContentType)
	folders := map[string]any{}
	for _, c := range categories {
		d := a.saveDirFor(c)
		folders[c] = map[string]string{"folder": d, "label": folderLabel(d)}
	}
	dir := a.saveDirFor(cat)
	m := map[string]any{"pid": id, "status": "ready", "from": from, "name": info.Name, "size": info.Size,
		"sizeText": "", "resumable": info.Resumable, "category": cat, "folder": dir, "folderLabel": folderLabel(dir),
		"folders": folders, "url": src.URL, "host": siteName(hostOf(src.URL)), "page": page, "at": at.UnixMilli()}
	if info.Size >= 0 {
		m["sizeText"] = HumanSize(info.Size)
	}
	if t := a.unfinishedSame(info.Name, info.Size); t != nil {
		a.mu.Lock()
		m["existing"] = map[string]any{"id": t.ID, "percent": t.Percent, "doneText": HumanSize(t.Done), "state": t.State}
		a.mu.Unlock()
	}
	return m
}

// unfinishedSame finds an unfinished download of the same file (same name and size):
// the new link can continue it, as when a link expired.
func (a *App) unfinishedSame(name string, size int64) *DownloadTask {
	if name == "" {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := len(a.tasks) - 1; i >= 0; i-- {
		t := a.tasks[i]
		if t.isFile() && !t.finished() && strings.EqualFold(t.FileName, name) && (size < 0 || t.Total == size) {
			return t
		}
	}
	return nil
}

// FileChoice is what the user chose in the "new download" window.
type FileChoice struct {
	Name     string `json:"name"`
	Category string `json:"category"`
	Folder   string `json:"folder"`   // "" for where that kind of file goes
	Start    bool   `json:"start"`    // false: "later" (in the list, not started)
	Continue string `json:"continue"` // an unfinished download of the same file, to continue with this link
}

// ConfirmFile adds the file of the "new download" window to the list. It returns
// {"id": …} or {"error": …} (the window then stays).
func (a *App) ConfirmFile(pid string, c FileChoice) map[string]any {
	p := a.takePending(pid)
	if p == nil {
		return map[string]any{"error": NewUserError("unknown")}
	}
	if c.Continue != "" && a.continueFile(c.Continue, p) {
		return map[string]any{"id": c.Continue, "existing": true}
	}
	a.mu.Lock()
	info := p.info
	a.mu.Unlock()
	name := info.Name
	if n := strings.TrimSpace(c.Name); n != "" {
		name = cleanFileName(decodeName(n), info.ContentType)
	}
	if name == "" {
		name = cleanFileName("", info.ContentType)
	}
	cat := c.Category
	if !validCategory(cat) {
		cat = categoryOf(name, info.ContentType)
	}
	dir := strings.TrimSpace(c.Folder)
	if dir == "" {
		dir = a.saveDirFor(cat)
	} else if err := os.MkdirAll(dir, 0o755); err != nil || !dirWritable(dir) {
		a.putPending(p)
		e := NewUserError("permission")
		e.Folder = dir
		return map[string]any{"error": e}
	}
	t := a.addFileTask(p.src, p.page, name, cat, dir, info, c.Start)
	return map[string]any{"id": t.ID}
}

// CancelFile closes the "new download" window without downloading.
func (a *App) CancelFile(pid string) { a.takePending(pid) }

// addFileTask puts a file in the list (started, or paused for "later").
func (a *App) addFileTask(src FileSource, page, name, cat, dir string, info FileInfo, start bool) *DownloadTask {
	base, _ := splitName(name)
	if base == "" {
		base = name
	}
	cookies := src.Cookies
	src.Cookies = nil
	if src.FinalURL == "" {
		src.FinalURL = info.FinalURL
	}
	now := time.Now().UnixMilli()
	t := &DownloadTask{Type: typeFile, URL: src.URL, Site: hostOf(src.URL), Title: name, Kind: typeFile, Base: base,
		SaveDir: dir, Category: cat, Page: page, Src: src, FileName: name, Resumable: info.Resumable,
		CreatedAt: now, QueuedAt: now, State: statePaused, ETA: -1}
	if info.Size >= 0 {
		t.SizeBytes, t.Total = info.Size, info.Size
	}
	if start {
		t.State = stateQueued
	}
	a.mu.Lock()
	t.ID = a.newID()
	a.tasks = append(a.tasks, t)
	dropped := a.trimFinishedLocked()
	a.mu.Unlock()
	a.keepCookies(t.ID, cookies)
	for _, d := range dropped {
		a.forgetTask(d)
	}
	a.saveTasksSoon()
	a.emitTask("add", t, true)
	if start {
		a.wg.Add(1)
		go func() {
			defer a.wg.Done()
			a.schedule()
		}()
	}
	return t
}

// continueFile gives an unfinished download of the same file the new link (and its
// cookies), and continues it from where it stopped.
func (a *App) continueFile(id string, p *pendingFile) bool {
	a.mu.Lock()
	t := a.find(id)
	if t == nil || !t.isFile() || t.finished() {
		a.mu.Unlock()
		return false
	}
	running := t.cancel != nil
	if !running {
		src := p.src
		src.Cookies = nil
		if src.FinalURL == "" {
			src.FinalURL = p.info.FinalURL
		}
		t.Src, t.URL, t.Site = src, src.URL, hostOf(src.URL)
		if p.page != "" {
			t.Page = p.page
		}
	}
	a.mu.Unlock()
	if !running {
		a.keepCookies(id, p.src.Cookies)
		a.Resume(id)
	}
	return true
}

// ---------------------------------------------------------------------------
// "Add a link"
// ---------------------------------------------------------------------------

// AddLink reads what the user pasted in "add a link". A video on one of the sites is read
// as before ({"kind": "media", "seq": …}, then "info" events). Any other web address is
// asked about first ({"kind": "file", "pid": …}, then a "file" event): a file opens the
// "new download" window ("ready"); a web page is searched for videos ("media", with the
// seq of the "info" events that follow); else "error".
func (a *App) AddLink(input string) map[string]any {
	return a.addLink(input, FileSource{}, "", "link")
}

// addLink is AddLink for a link found in a browser (src: how the browser asks for it,
// without its URL; page: where it was).
func (a *App) addLink(input string, src FileSource, page, from string) map[string]any {
	opts := fetchOpts{cookies: src.Cookies, referer: src.Referer, userAgent: src.UserAgent, page: page}
	link, res := ParseLink(input)
	switch res {
	case LinkFound:
		return map[string]any{"kind": "media", "seq": a.fetch(input, opts, nil)}
	case LinkInvalid:
		return map[string]any{"error": NewUserError("invalid_url").Localize(link.Site)}
	}
	u := firstWebAddress(input)
	if u == "" {
		return map[string]any{"error": NewUserError("invalid_url")}
	}
	src.URL = u
	p := a.addPending(src, page, from)
	// The window says "checking the link…" until the answer (this event reaches the page
	// before the answer of nz_addLink, and before the "ready" below).
	a.emit.Emit("file", map[string]any{"pid": p.id, "status": "checking", "url": u, "from": from})
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				a.takePending(p.id)
				a.emit.Emit("file", map[string]any{"pid": p.id, "status": "error", "url": u, "from": from, "error": panicError("probe", r)})
			}
		}()
		ctx, cancel := context.WithTimeout(a.ctx, 45*time.Second)
		defer cancel()
		info, err := ProbeFile(ctx, p.src, "")
		if a.ctx.Err() != nil {
			return
		}
		if err == nil {
			a.mu.Lock()
			p.info = info
			a.mu.Unlock()
			if from == "browser" && a.store.Get().NoDialog {
				// The user asked not to be asked about downloads from the browser.
				if r := a.ConfirmFile(p.id, FileChoice{Start: true}); r["error"] == nil {
					a.emit.Emit("file", map[string]any{"pid": p.id, "status": "started", "id": r["id"]})
					return
				}
				a.putPending(p)
			}
			a.emit.Emit("file", a.pendingEvent(p))
			return
		}
		a.takePending(p.id)
		if errCode(err) == "page_not_file" {
			// A web page: it may show videos (yt-dlp reads more than a thousand sites).
			a.fetch(u, opts, func(seq int) {
				a.emit.Emit("file", map[string]any{"pid": p.id, "status": "media", "seq": seq, "url": u, "from": from})
			})
			return
		}
		log.Printf("link %s: %v", u, err)
		a.emit.Emit("file", map[string]any{"pid": p.id, "status": "error", "url": u, "from": from, "error": fileUserError(err, hostOf(u), "")})
	}()
	return map[string]any{"kind": "file", "pid": p.id}
}

// fileUserError puts a file download's failure in the user's words.
func fileUserError(err error, site, folder string) *UserError {
	code := "unknown"
	var de *DownloadError
	if errors.As(err, &de) {
		switch de.Code {
		case "network", "noparts":
			code = "network"
		case "tls":
			code = "clock"
		case "forbidden":
			code = "link_expired"
		case "gone":
			code = "file_gone"
		case "server":
			code = "server_busy"
		case "page_not_file":
			code = "page_not_file"
		case "disk":
			code = "disk"
		case "permission":
			code = "permission"
		case "badurl":
			code = "invalid_url"
		case "changed":
			code = "file_changed"
		}
	}
	e := NewUserError(code)
	e.Details = err.Error()
	if de != nil && code == "disk" && de.Need > 0 {
		e.Need, e.Free = de.Need, de.Free
	}
	if code == "disk" || code == "permission" {
		e.Folder = folder
	}
	return e.Localize(site)
}

// ---------------------------------------------------------------------------
// Running a file download
// ---------------------------------------------------------------------------

// folderReachable says whether a folder exists or can be made: its drive is there (not a
// USB drive taken out, or a network folder off line).
func folderReachable(dir string) bool {
	for d := filepath.Clean(dir); ; {
		if isDir(d) {
			return true
		}
		parent := filepath.Dir(d)
		if parent == d {
			return false
		}
		d = parent
	}
}

// prepareSaveDir makes sure the task's folder can take its file. When the folder chosen
// at the start cannot be reached (a USB drive taken out?), the file goes where new
// downloads go; a folder that was deleted is made again.
func (a *App) prepareSaveDir(t *DownloadTask) (string, *UserError) {
	a.mu.Lock()
	saveDir := t.SaveDir
	a.mu.Unlock()
	if saveDir == "" || !folderReachable(saveDir) {
		saveDir = a.SaveDir()
		a.mu.Lock()
		t.SaveDir = saveDir
		a.mu.Unlock()
	}
	if err := os.MkdirAll(saveDir, 0o755); err != nil {
		e := NewUserError("folder_missing")
		e.Details = err.Error()
		return "", e
	}
	if !dirWritable(saveDir) {
		e := NewUserError("permission")
		e.Folder = saveDir
		return "", e
	}
	return saveDir, nil
}

func (a *App) runFileTask(ctx context.Context, t *DownloadTask) outcome {
	saveDir, uerr := a.prepareSaveDir(t)
	if uerr != nil {
		return outcome{err: uerr}
	}
	a.mu.Lock()
	src, name, id, site := t.Src, t.FileName, t.ID, t.Site
	job := jobDirOf(t)
	a.mu.Unlock()
	if err := os.MkdirAll(job, 0o755); err != nil {
		return outcome{err: fileUserError(&DownloadError{Code: "permission", Err: err}, site, saveDir)}
	}
	hideFolder(filepath.Dir(job))
	src.Cookies = a.cookiesOf(id)
	d := &FileDownload{Src: src, Dir: job, Name: name, Conns: a.store.Get().conns(),
		OnInfo: func(fi FileInfo) {
			a.mu.Lock()
			if t.FileName == "" && fi.Name != "" {
				t.FileName, t.Title = fi.Name, fi.Name
				if b, _ := splitName(fi.Name); b != "" {
					t.Base = b
				}
			}
			t.Resumable = fi.Resumable
			if fi.Size >= 0 {
				t.Total, t.SizeBytes = fi.Size, fi.Size
			}
			if fi.FinalURL != "" {
				t.Src.FinalURL = fi.FinalURL
			}
			t.Stage = "download"
			a.mu.Unlock()
			a.emitTask("update", t, false)
		},
		OnProgress: func(p FileProgress) {
			a.mu.Lock()
			if t.cancel == nil {
				a.mu.Unlock()
				return
			}
			t.Done, t.Speed, t.ETA, t.conns, t.segments = p.Done, p.Speed, p.ETA, p.Conns, p.Segments
			if p.Total > 0 {
				t.Total = p.Total
				t.Percent = math.Min(100, float64(p.Done)*100/float64(p.Total))
			}
			t.Stage = "download"
			a.mu.Unlock()
			a.emitTask("update", t, false)
			a.saveTasks(false)
		}}
	path, err := d.Run(ctx)
	if ctx.Err() != nil {
		return outcome{stopped: true}
	}
	if err != nil {
		log.Printf("file download %s failed: %v", id, err)
		return outcome{err: fileUserError(err, site, saveDir)}
	}
	a.mu.Lock()
	name = t.FileName
	t.Stage, t.Percent, t.conns = "save", 100, 0
	a.mu.Unlock()
	if name == "" {
		name = d.Info().Name
	}
	a.emitTask("update", t, false)
	a.saveMu.Lock()
	dest := uniqueFilePath(saveDir, name)
	err = moveFile(ctx, path, dest)
	a.saveMu.Unlock()
	if err != nil {
		if ctx.Err() != nil {
			_ = os.Remove(dest)
			return outcome{stopped: true}
		}
		log.Printf("saving %s: %v", id, err)
		return outcome{err: fileUserError(fileError(err), site, saveDir)}
	}
	var size int64
	if fi, err := os.Stat(dest); err == nil {
		size = fi.Size()
	}
	return outcome{path: dest, size: size}
}
