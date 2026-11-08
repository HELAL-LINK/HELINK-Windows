package main

// The quality menu in the browser: the button on a video asks for its qualities
// ("qualities"), the page shows them, and the one chosen is downloaded right away ("get"),
// HELINK showing its progress. The video is read as for the window, but kept apart from
// the post shown there: a reading is kept 15 minutes for the click that follows, and
// asking again for the same address meanwhile waits for the same reading (the button asks
// as soon as the mouse is on it).

import (
	"context"
	"fmt"
	"time"
)

const (
	browserPostTTL  = 15 * time.Minute
	browserReadWait = 100 * time.Second // the helper gives up on an answer after 2 minutes
)

// browserPost is a video read for the browser's menu.
type browserPost struct {
	id     string
	target string
	f      *fetched
	fail   *readFailure
	at     time.Time // when the reading ended
	done   chan struct{}
}

// linkToRead is the link behind what was given, or the code of why it is not read.
func linkToRead(input string) (Link, string) {
	link, res := ParseLink(input)
	switch res {
	case LinkUnsupported, LinkNone:
		// Another site: yt-dlp knows more than a thousand, and finds videos in plain pages.
		u := firstWebAddress(input)
		if u == "" {
			return link, "invalid_url"
		}
		link = Link{URL: u, Site: hostOf(u), Kind: LinkOther}
	case LinkInvalid:
		return link, "invalid_url"
	}
	switch link.Kind {
	case LinkPlaylist:
		return link, "playlist"
	case LinkProfile:
		if link.Site == siteYouTube {
			return link, "channel"
		}
		return link, "profile"
	case LinkLive:
		return link, "live"
	}
	return link, ""
}

func browserFailure(e *UserError) map[string]any {
	return map[string]any{"ok": false, "error": e.Code, "title": e.Title(), "message": e.Message(), "retry": e.Retry}
}

// pruneBrowserPostsLocked forgets the readings older than browserPostTTL (their files are
// removed by pruneInfoFiles). Call with the lock held.
func (a *App) pruneBrowserPostsLocked() {
	for id, bp := range a.bposts {
		select {
		case <-bp.done:
			if time.Since(bp.at) > browserPostTTL {
				delete(a.bposts, id)
				if a.bpostByURL[bp.target] == bp {
					delete(a.bpostByURL, bp.target)
				}
			}
		default:
		}
	}
}

// BrowserQualities reads a video for the browser's menu and answers with its qualities:
// {"ok": true, "id", "site", "siteName", "items": [{"title", "channel", "duration",
// "options": [{"key", "kind", "label", "tag", "sizeBytes", "recommended"}]}], "sepReady"},
// or {"ok": false, "error", "title", "message"}.
func (a *App) BrowserQualities(v BrowserVideo) map[string]any {
	target := v.PostURL
	if target == "" {
		target = v.PageURL
	}
	if target == "" && len(v.Media) > 0 {
		target = v.Media[0].URL
	}
	link, code := linkToRead(target)
	if code != "" {
		return browserFailure(NewUserError(code).Localize(link.Site))
	}
	opts := fetchOpts{cookies: v.Cookies, referer: v.PageURL, userAgent: v.UserAgent, page: v.PageURL, media: v.Media}
	a.mu.Lock()
	if a.bposts == nil {
		a.bposts, a.bpostByURL = map[string]*browserPost{}, map[string]*browserPost{}
	}
	a.pruneBrowserPostsLocked()
	bp := a.bpostByURL[link.URL]
	if bp != nil {
		select {
		case <-bp.done:
			if bp.fail != nil {
				bp = nil // it failed: read it again
			}
		default:
		}
	}
	if bp == nil {
		a.bpostSeq++
		bp = &browserPost{id: fmt.Sprintf("b%d", a.bpostSeq), target: link.URL, done: make(chan struct{})}
		a.bposts[bp.id] = bp
		a.bpostByURL[link.URL] = bp
		a.wg.Add(1)
		go func() {
			defer a.wg.Done()
			ctx, cancel := context.WithTimeout(a.ctx, 3*time.Minute)
			defer cancel()
			var f *fetched
			var fail *readFailure
			func() {
				defer func() {
					if r := recover(); r != nil {
						fail = &readFailure{site: link.Site, err: panicError("browser read", r)}
					}
				}()
				f, _, fail = a.readPost(ctx, 0, link, opts, nil)
			}()
			if f == nil && fail == nil { // stopped: HELINK is closing, or it took too long
				fail = &readFailure{site: link.Site, err: NewUserError("network").Localize(link.Site)}
			}
			a.mu.Lock()
			bp.f, bp.fail, bp.at = f, fail, time.Now()
			a.mu.Unlock()
			close(bp.done)
			a.pruneInfoFiles()
		}()
	}
	a.mu.Unlock()
	select {
	case <-bp.done:
	case <-time.After(browserReadWait):
		return browserFailure(NewUserError("network").Localize(link.Site))
	case <-a.ctx.Done():
		return map[string]any{"ok": false, "error": "closing"}
	}
	a.mu.Lock()
	f, fail := bp.f, bp.fail
	a.mu.Unlock()
	if fail != nil {
		return browserFailure(fail.err)
	}
	items := make([]map[string]any, 0, len(f.items))
	for _, it := range f.items {
		opts := make([]map[string]any, 0, len(it.Options))
		for _, o := range it.Options {
			opts = append(opts, map[string]any{"key": o.Key, "kind": o.Kind, "label": o.Label, "tag": o.Tag,
				"sizeBytes": o.SizeBytes, "recommended": o.Recommended})
		}
		items = append(items, map[string]any{"title": it.Meta.Title, "channel": it.Meta.Channel,
			"duration": it.Meta.DurationText, "options": opts})
	}
	return map[string]any{"ok": true, "id": bp.id, "site": f.site, "siteName": siteName(f.site), "items": items,
		"sepReady": a.sep.ready()}
}

// BrowserGet downloads quality key of video item (0-based) of a post read for the
// browser's menu, the music removed if noMusic, and shows its progress. It answers
// {"ok": true, "taskId", "existing"} or {"ok": false, "error", ...}.
func (a *App) BrowserGet(id string, item int, key string, noMusic bool) map[string]any {
	a.mu.Lock()
	bp := a.bposts[id]
	var f *fetched
	if bp != nil {
		f = bp.f
	}
	a.mu.Unlock()
	if f == nil {
		return browserFailure(NewUserError("expired"))
	}
	r := a.downloadFrom(f, item, key, noMusic)
	if e, ok := r["error"].(*UserError); ok {
		return browserFailure(e)
	}
	tid, _ := r["id"].(string)
	if tid == "" {
		return browserFailure(NewUserError("unknown"))
	}
	a.emit.Emit("browserTask", map[string]any{"id": tid})
	a.show("task")
	return map[string]any{"ok": true, "taskId": tid, "existing": r["existing"] == true}
}
