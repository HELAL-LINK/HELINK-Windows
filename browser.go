package main

// What the HELINK extension asks, through helink-host and the ipc connection: how to
// behave ("hello"), a download the browser started ("download"), a link or a video the
// user chose in a page ("link", "video"), a video's qualities for the menu on its button
// and the one chosen ("qualities", "get": browserread.go), and its settings page
// ("getSettings", "setSettings").

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"nazzelha/ipc"
)

// BrowserFile is a download a browser started, as the extension reports it.
type BrowserFile struct {
	URL       string   `json:"url"`
	FinalURL  string   `json:"finalUrl"`
	Referrer  string   `json:"referrer"`
	Filename  string   `json:"filename"`
	Mime      string   `json:"mime"`
	Size      int64    `json:"size"`
	Cookies   []Cookie `json:"cookies"`
	UserAgent string   `json:"userAgent"`
	PageURL   string   `json:"pageUrl"`
}

// BrowserMedia is a sound or video address a page loaded (seen by the extension).
type BrowserMedia struct {
	URL  string `json:"url"`
	Kind string `json:"kind"` // video, audio, hls, dash
	Mime string `json:"mime,omitempty"`
	Size int64  `json:"size,omitempty"`
}

// BrowserVideo is a video the user asked for in a page (its button, or the menu).
type BrowserVideo struct {
	PageURL   string         `json:"pageUrl"`
	PostURL   string         `json:"postUrl"` // the extension's guess at the post's own address
	Title     string         `json:"title"`
	Media     []BrowserMedia `json:"media"`
	Cookies   []Cookie       `json:"cookies"`
	UserAgent string         `json:"userAgent"`
	Ext       string         `json:"ext"` // the extension's version (since 2.2)
}

func (a *App) show(reason string) {
	if a.Show != nil {
		a.Show(reason)
	}
}

// extView is how the extension behaves; also kept in ext.json, for the helper to answer
// when HELINK is closed.
func (a *App) extView() map[string]any {
	s := a.store.Get()
	nz := func(l []string) []string {
		if l == nil {
			return []string{}
		}
		return l
	}
	return map[string]any{"catch": !s.NoCatch, "dialog": !s.NoDialog, "buttons": !s.NoButtons,
		"catchTypes": s.catchTypes(), "catchMinKB": s.CatchMinKB, "skipSites": nz(s.SkipSites),
		"buttonSkip": nz(s.ButtonSkip), "lang": curLang(), "version": appVersion}
}

func (a *App) writeExtFile() {
	b, _ := json.Marshal(a.extView())
	p := filepath.Join(a.paths.Data, "ext.json")
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err == nil {
		_ = os.Rename(tmp, p)
	}
}

// ListenForBrowsers starts answering the browsers' helper. Call Close on the result when
// the app quits.
func (a *App) ListenForBrowsers() (*ipc.Server, error) {
	a.writeExtFile()
	return ipc.Listen(a.paths.Data, appVersion, func(req ipc.Request) any {
		switch req.Type {
		case "ping":
			return map[string]any{"ok": true, "version": appVersion}
		case "show":
			a.show("open")
			return map[string]any{"ok": true}
		case "quit":
			// The installer replaces HELINK.exe: close (downloads pause, and continue next time).
			if a.Quit != nil {
				a.Quit()
			}
			return map[string]any{"ok": true}
		case "ext":
			return a.ExtMessage(req.Data)
		}
		return map[string]any{"ok": false, "error": "unknown_request"}
	})
}

// ExtSeen is when the extension last talked to HELINK (zero: never). The helper notes it
// in seen.json too, also while HELINK is closed.
func (a *App) ExtSeen() time.Time {
	a.mu.Lock()
	t := a.extSeen
	a.mu.Unlock()
	if fi, err := os.Stat(filepath.Join(a.paths.Data, "seen.json")); err == nil && fi.ModTime().After(t) {
		t = fi.ModTime()
	}
	return t
}

// ExtMessage answers one message of the extension.
func (a *App) ExtMessage(data []byte) map[string]any {
	var m struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(data, &m) != nil {
		return map[string]any{"ok": false, "error": "bad_message"}
	}
	a.mu.Lock()
	a.extSeen = time.Now()
	a.mu.Unlock()
	switch m.Type {
	case "hello", "getSettings":
		return map[string]any{"ok": true, "running": true, "settings": a.extView()}
	case "download":
		var b BrowserFile
		if json.Unmarshal(data, &b) != nil || b.URL == "" {
			return map[string]any{"ok": false, "error": "bad_message"}
		}
		taken, reason := a.TakeBrowserDownload(a.ctx, b)
		if !taken {
			return map[string]any{"ok": false, "reason": reason}
		}
		// For the card the extension shows in the page: the file's name, and whether
		// HELINK's "new download" window opened (else the download started right away).
		return map[string]any{"ok": true, "name": reason, "dialog": !a.store.Get().NoDialog}
	case "link":
		var l struct {
			URL       string   `json:"url"`
			Referrer  string   `json:"referrer"`
			UserAgent string   `json:"userAgent"`
			PageURL   string   `json:"pageUrl"`
			Cookies   []Cookie `json:"cookies"`
		}
		if json.Unmarshal(data, &l) != nil || l.URL == "" {
			return map[string]any{"ok": false, "error": "bad_message"}
		}
		r := a.addLink(l.URL, FileSource{Referer: l.Referrer, UserAgent: l.UserAgent, Cookies: l.Cookies}, l.PageURL, "browser")
		if e, ok := r["error"]; ok {
			return map[string]any{"ok": false, "error": e}
		}
		if r["kind"] == "media" {
			a.emit.Emit("browserVideo", map[string]any{"seq": r["seq"], "page": l.PageURL})
			a.show("video")
		} else {
			a.show("file")
		}
		return map[string]any{"ok": true}
	case "video":
		var v BrowserVideo
		if json.Unmarshal(data, &v) != nil || (v.PageURL == "" && v.PostURL == "" && len(v.Media) == 0) {
			return map[string]any{"ok": false, "error": "bad_message"}
		}
		a.ReadBrowserVideo(v)
		return map[string]any{"ok": true}
	case "qualities": // the button's menu: the video's qualities, shown in the page
		var v BrowserVideo
		if json.Unmarshal(data, &v) != nil || (v.PageURL == "" && v.PostURL == "" && len(v.Media) == 0) {
			return map[string]any{"ok": false, "error": "bad_message"}
		}
		return a.BrowserQualities(v)
	case "get": // a quality chosen in that menu
		var g struct {
			ID      string `json:"id"`
			Item    int    `json:"item"`
			Key     string `json:"key"`
			NoMusic bool   `json:"noMusic"`
		}
		if json.Unmarshal(data, &g) != nil || g.ID == "" || g.Key == "" {
			return map[string]any{"ok": false, "error": "bad_message"}
		}
		return a.BrowserGet(g.ID, g.Item, g.Key, g.NoMusic)
	case "product": // a product's page open in the browser: its pictures and videos (product.go)
		var pr struct {
			PageURL   string   `json:"pageUrl"`
			Title     string   `json:"title"`
			HTML      string   `json:"html"`
			Cookies   []Cookie `json:"cookies"`
			UserAgent string   `json:"userAgent"`
		}
		if json.Unmarshal(data, &pr) != nil || pr.PageURL == "" || pr.HTML == "" {
			return map[string]any{"ok": false, "error": "bad_message"}
		}
		site := productSite(pr.PageURL)
		if site == "" {
			return map[string]any{"ok": false, "error": "not_product"}
		}
		r := a.readProductHTML(site, pr.PageURL, pr.Title, pr.HTML, FileSource{Referer: pr.PageURL, UserAgent: pr.UserAgent, Cookies: pr.Cookies})
		a.show("product")
		return r
	case "show":
		a.show("open")
		return map[string]any{"ok": true}
	case "setSettings":
		var s struct {
			Settings map[string]any `json:"settings"`
		}
		_ = json.Unmarshal(data, &s)
		ch := map[string]any{}
		for _, k := range []string{"catch", "dialog", "buttons", "catchTypes", "catchMinKB", "skipSites", "buttonSkip"} {
			if v, ok := s.Settings[k]; ok {
				ch[k] = v
			}
		}
		a.ChangeSettings(ch)
		a.emit.Emit("settings", a.settingsView())
		return map[string]any{"ok": true, "settings": a.extView()}
	}
	return map[string]any{"ok": false, "error": "unknown_type"}
}

// TakeBrowserDownload answers a browser that started a download: HELINK takes it when
// the file answers here as well (the browser then cancels its own) and shows the "new
// download" window, or starts it right away when the user chose not to be asked.
// Otherwise the browser keeps it (taken false, and why, for the extension's log). When
// taken, reason is the file's name.
func (a *App) TakeBrowserDownload(ctx context.Context, b BrowserFile) (taken bool, reason string) {
	if a.store.Get().NoCatch {
		return false, "off"
	}
	src := FileSource{URL: b.URL, FinalURL: b.FinalURL, Referer: b.Referrer, UserAgent: b.UserAgent, Cookies: b.Cookies}
	name := ""
	if n := decodeName(strings.TrimSpace(b.Filename)); n != "" {
		name = cleanFileName(n, b.Mime)
	}
	pctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	info, err := ProbeFile(pctx, src, name)
	if err != nil {
		log.Printf("browser download %s stays with the browser: %v", b.URL, err)
		return false, errCode(err)
	}
	if b.Size > 0 && info.Size > 0 && b.Size != info.Size {
		// Not what the browser was getting.
		log.Printf("browser download %s stays with the browser: %d bytes there, %d here", b.URL, b.Size, info.Size)
		return false, "size"
	}
	p := a.addPending(src, b.PageURL, "browser")
	a.mu.Lock()
	p.info = info
	a.mu.Unlock()
	if a.store.Get().NoDialog {
		if r := a.ConfirmFile(p.id, FileChoice{Start: true}); r["error"] != nil {
			return false, "folder"
		}
		return true, info.Name
	}
	a.emit.Emit("file", a.pendingEvent(p))
	a.show("file")
	return true, info.Name
}

// ReadBrowserVideo reads a video the user asked for in the browser: the post's own page
// first (the sites' readers know all its qualities), else what the page showed. The
// qualities arrive as "info" events, after a "browserVideo" event with their number.
func (a *App) ReadBrowserVideo(v BrowserVideo) int {
	target := v.PostURL
	if target == "" {
		target = v.PageURL
	}
	if target == "" && len(v.Media) > 0 {
		target = v.Media[0].URL
	}
	opts := fetchOpts{cookies: v.Cookies, referer: v.PageURL, userAgent: v.UserAgent, page: v.PageURL, media: v.Media}
	seq := a.fetch(target, opts, func(seq int) {
		// An extension older than 2.2 still runs in the browser (it was not restarted since
		// HELINK was updated): its button sends the video here instead of showing its menu.
		a.emit.Emit("browserVideo", map[string]any{"seq": seq, "title": v.Title, "page": v.PageURL, "oldExt": v.Ext == ""})
	})
	a.show("video")
	return seq
}

// mediaChoices describes what a page loaded, for the window to offer when reading the
// page found nothing.
func mediaChoices(media []BrowserMedia) []map[string]any {
	var out []map[string]any
	for i, m := range media {
		if i >= 8 {
			break
		}
		label := strings.ToUpper(extOf(nameFromURL(m.URL)))
		switch m.Kind {
		case "hls", "dash":
			label = tr("بث", "Stream")
		case "":
			if label == "" {
				label = tr("ملف", "File")
			}
		}
		c := map[string]any{"index": i, "kind": m.Kind, "label": label, "host": siteName(hostOf(m.URL))}
		if m.Size > 0 {
			c["sizeText"] = HumanSize(m.Size)
		}
		out = append(out, c)
	}
	return out
}

// UseMedia downloads what a page loaded, after reading the page found nothing: a stream
// is read by yt-dlp ("media"), a file opens the "new download" window ("file").
func (a *App) UseMedia(seq, index int) map[string]any {
	a.mu.Lock()
	opts, ok := a.readOpts[seq]
	a.mu.Unlock()
	if !ok || index < 0 || index >= len(opts.media) {
		return map[string]any{"error": NewUserError("unknown")}
	}
	m := opts.media[index]
	lower := strings.ToLower(m.URL)
	if m.Kind == "hls" || m.Kind == "dash" || strings.Contains(lower, ".m3u8") || strings.Contains(lower, ".mpd") {
		o := opts
		o.media = nil
		return map[string]any{"kind": "media", "seq": a.fetch(m.URL, o, nil)}
	}
	src := FileSource{Referer: opts.referer, UserAgent: opts.userAgent, Cookies: opts.cookies}
	if src.Referer == "" {
		src.Referer = opts.page
	}
	return a.addLink(m.URL, src, opts.page, "browser")
}

// rememberReadOpts keeps how a reading was asked for (for UseMedia), for the latest few.
func (a *App) rememberReadOpts(seq int, opts fetchOpts) {
	if len(opts.media) == 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.readOpts == nil {
		a.readOpts = map[int]fetchOpts{}
	}
	a.readOpts[seq] = opts
	for s := range a.readOpts {
		if s < seq-20 {
			delete(a.readOpts, s)
		}
	}
}
