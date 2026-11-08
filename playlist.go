package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Courses and playlists (YouTube): the whole list is read at once, titles and lengths only
// (yt-dlp's --flat-playlist: one request for every hundred videos), the user ticks the videos
// and picks one quality for all, and each video becomes a download of its own, numbered in
// the list's order ("01 - …") in a folder named after the list. A video's own qualities are
// read when its download starts; the quality chosen is a ceiling ("720p": the best up to
// 720p, as the single videos' sort order does).

// maxListEntries is as much of a list as is shown (YouTube lists go up to 5000 videos).
const maxListEntries = 1000

// ListEntry is one video of a list.
type ListEntry struct {
	N            int     `json:"n"` // its place in the list (1-based)
	ID           string  `json:"id"`
	Title        string  `json:"title"`
	Channel      string  `json:"channel,omitempty"`
	Duration     float64 `json:"duration"`
	DurationText string  `json:"durationText"`
	Thumbnail    string  `json:"thumbnail"`
	Available    bool    `json:"available"` // false: private, deleted, or not out yet
	// Have: in the downloads already, as "video" and/or "audio" (downloaded, its file still
	// there, or not finished yet). The window leaves these unticked, so a course pasted again
	// downloads only its new lessons.
	Have []string `json:"have,omitempty"`
}

// ListInfo is a list as read.
type ListInfo struct {
	ID      string
	Title   string
	Channel string
	URL     string
	Entries []ListEntry
	More    bool // the list goes on after maxListEntries
}

// ListOption is a quality offered for a whole list.
type ListOption struct {
	Key         string `json:"key"`
	Kind        string `json:"kind"`
	Label       string `json:"label"`
	Tag         string `json:"tag,omitempty"`
	Desc        string `json:"desc"`
	Recommended bool   `json:"recommended"`
	PerMinute   int64  `json:"perMinute"` // about how many bytes a minute of video takes
}

// MarshalJSON writes the list for the page, with its qualities in the language in use.
func (l *ListInfo) MarshalJSON() ([]byte, error) {
	count, total := 0, 0.0
	for _, e := range l.Entries {
		if e.Available {
			count++
			total += e.Duration
		}
	}
	entries := l.Entries
	if entries == nil {
		entries = []ListEntry{}
	}
	return json.Marshal(map[string]any{"id": l.ID, "title": l.Title, "channel": l.Channel, "url": l.URL,
		"entries": entries, "count": count, "duration": total, "durationText": HumanDuration(total), "more": l.More,
		"options": listOptions()})
}

// Rough sizes of a minute of a YouTube video at each ceiling (picture and sound), for the
// estimate shown before a list is downloaded. Lessons with slides take much less.
var listPerMinute = map[int]int64{1080: 20 << 20, 720: 11 << 20, 480: 6 << 20, 360: 4 << 20}

func listOptions() []ListOption {
	var out []ListOption
	for _, q := range []int{1080, 720, 480, 360} {
		label, tag, desc := qualityWords(q)
		out = append(out, ListOption{Key: fmt.Sprintf("v%d", q), Kind: "video", Label: label, Tag: tag, Desc: descText(desc),
			Recommended: q == 720, PerMinute: listPerMinute[q]})
	}
	for _, kbps := range []int{320, 192, 128} {
		out = append(out, ListOption{Key: fmt.Sprintf("mp3-%d", kbps), Kind: "audio", Label: fmt.Sprintf("%dk", kbps), Tag: "MP3",
			Desc: descText(fmt.Sprintf("a%d", kbps)), Recommended: kbps == 192, PerMinute: int64(kbps) * 1000 / 8 * 60})
	}
	return out
}

// listFormat is how yt-dlp is asked for a list's quality key ("v720", "mp3-192").
func listFormat(key string) (kind, label, formatSpec, sortSpec, audioQuality string, ok bool) {
	if q, err := strconv.Atoi(strings.TrimPrefix(key, "v")); err == nil && strings.HasPrefix(key, "v") {
		if _, known := listPerMinute[q]; known {
			label, _, _ = qualityWords(q)
			return "video", label, "bv*+ba/b", fmt.Sprintf("res:%d,vcodec:h264,acodec:aac", q), "", true
		}
	}
	if k, err := strconv.Atoi(strings.TrimPrefix(key, "mp3-")); err == nil && strings.HasPrefix(key, "mp3-") && (k == 320 || k == 192 || k == 128) {
		return "audio", fmt.Sprintf("%dk", k), "ba/b", "acodec:aac", fmt.Sprintf("%dK", k), true
	}
	return "", "", "", "", "", false
}

// listParts: what a video of the list downloads, for its progress bar (the picture's size is
// learned when it starts; YouTube's sound is about 130 kb/s).
func listParts(kind string, duration float64) []dlPart {
	sound := int64(duration * 130_000 / 8)
	if kind == "audio" {
		return []dlPart{{IsAudio: true, Bytes: sound}}
	}
	return []dlPart{{}, {IsAudio: true, Bytes: sound}}
}

// The subset of yt-dlp's answer for a list read with --flat-playlist.
type ytFlatList struct {
	Type     jstr              `json:"_type"`
	ID       jstr              `json:"id"`
	Title    jstr              `json:"title"`
	Uploader jstr              `json:"uploader"`
	Channel  jstr              `json:"channel"`
	Entries  []json.RawMessage `json:"entries"`
}

type ytFlatEntry struct {
	Type         jstr `json:"_type"`
	IEKey        jstr `json:"ie_key"`
	ID           jstr `json:"id"`
	Title        jstr `json:"title"`
	Duration     jnum `json:"duration"`
	Channel      jstr `json:"channel"`
	Uploader     jstr `json:"uploader"`
	Availability jstr `json:"availability"`
	LiveStatus   jstr `json:"live_status"`
}

var unavailableTitles = []string{"[private video]", "[deleted video]", "[unavailable video]", "[video unavailable]"}

// BuildList reads yt-dlp's answer for a list.
func BuildList(data []byte) (*ListInfo, error) {
	var info ytFlatList
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, err
	}
	if info.Type != "playlist" && len(info.Entries) == 0 {
		return nil, &InfoError{"novideo"}
	}
	l := &ListInfo{ID: string(info.ID), Title: strings.TrimSpace(string(info.Title)), Channel: string(info.Channel)}
	if l.Channel == "" {
		l.Channel = string(info.Uploader)
	}
	n, usable := 0, 0
	for _, raw := range info.Entries {
		if isJSONNull(raw) {
			n++
			continue
		}
		var e ytFlatEntry
		if json.Unmarshal(raw, &e) != nil {
			n++
			continue
		}
		if strings.EqualFold(string(e.IEKey), "YoutubeTab") || e.Type == "playlist" {
			continue // a list inside the list (a channel's tabs): not a video
		}
		n++
		id := string(e.ID)
		if !videoIDRe.MatchString(id) {
			continue
		}
		if len(l.Entries) >= maxListEntries {
			l.More = true
			break
		}
		title := strings.TrimSpace(string(e.Title))
		available := true
		for _, u := range unavailableTitles {
			if strings.EqualFold(title, u) {
				available = false
			}
		}
		switch string(e.Availability) {
		case "private", "premium_only", "subscriber_only", "needs_auth":
			available = false
		}
		switch string(e.LiveStatus) {
		case "is_live", "is_upcoming":
			available = false
		}
		if title == "" {
			title = tr("فيديو ", "Video ") + strconv.Itoa(n)
		}
		ch := string(e.Channel)
		if ch == "" {
			ch = string(e.Uploader)
		}
		d := float64(e.Duration)
		l.Entries = append(l.Entries, ListEntry{N: n, ID: id, Title: title, Channel: ch, Duration: d, DurationText: HumanDuration(d),
			Thumbnail: "https://i.ytimg.com/vi/" + id + "/mqdefault.jpg", Available: available})
		if available {
			usable++
		}
	}
	if usable == 0 {
		return nil, &InfoError{"list_empty"}
	}
	if l.Title == "" {
		l.Title = tr("قائمة تشغيل", "Playlist")
	}
	return l, nil
}

var listIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{2,80}$`)

// youtubeListOf is the list a YouTube video link was opened from ("&list=…"), as the link to the
// whole list; "" when there is none, or for the lists YouTube makes on its own (mixes, Watch
// later, Liked videos), which belong to one person or never end.
func youtubeListOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || hostSite(u.Hostname()) != siteYouTube {
		return ""
	}
	id := u.Query().Get("list")
	if !listIDRe.MatchString(id) {
		return ""
	}
	up := strings.ToUpper(id)
	if strings.HasPrefix(up, "RD") || up == "WL" || up == "LL" || up == "LM" || strings.HasPrefix(up, "UL") {
		return ""
	}
	return "https://www.youtube.com/playlist?list=" + id
}

// youtubeVideoID is the video a YouTube link is for ("" when it is not a video's link).
func youtubeVideoID(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || hostSite(u.Hostname()) != siteYouTube {
		return ""
	}
	parts := pathParts(u)
	id := ""
	switch {
	case strings.EqualFold(strings.TrimPrefix(u.Hostname(), "www."), "youtu.be"):
		if len(parts) >= 1 {
			id = parts[0]
		}
	case len(parts) == 1 && (parts[0] == "watch" || parts[0] == "watch_popup"):
		id = u.Query().Get("v")
	case len(parts) >= 2 && (parts[0] == "shorts" || parts[0] == "live" || parts[0] == "embed" || parts[0] == "v" || parts[0] == "e"):
		id = parts[1]
	}
	if !videoIDRe.MatchString(id) {
		return ""
	}
	return id
}

// markHave notes which videos of the list are in the downloads already (ListEntry.Have).
func (a *App) markHave(l *ListInfo) {
	type had struct{ id, kind, path string }
	var tasks []had
	a.mu.Lock()
	for _, t := range a.tasks {
		if t.Type != "" || (t.Kind != "video" && t.Kind != "audio") {
			continue
		}
		id := youtubeVideoID(t.URL)
		if id == "" {
			continue
		}
		p := ""
		if t.finished() {
			p = t.Path
			if p == "" {
				continue
			}
		}
		tasks = append(tasks, had{id, t.Kind, p})
	}
	a.mu.Unlock()
	if len(tasks) == 0 {
		return
	}
	inList := map[string]bool{}
	for _, e := range l.Entries {
		inList[e.ID] = true
	}
	have := map[string]map[string]bool{}
	for _, t := range tasks {
		if !inList[t.id] || have[t.id][t.kind] {
			continue
		}
		if t.path != "" && !fileOK(t.path) {
			continue // downloaded, then deleted or moved: it can be downloaded again
		}
		if have[t.id] == nil {
			have[t.id] = map[string]bool{}
		}
		have[t.id][t.kind] = true
	}
	for i := range l.Entries {
		e := &l.Entries[i]
		e.Have = nil
		for _, k := range []string{"video", "audio"} {
			if have[e.ID][k] {
				e.Have = append(e.Have, k)
			}
		}
	}
}

// fetchedList is the list on screen, and the reading it came from.
type fetchedList struct {
	seq  int
	info *ListInfo
}

// runFetchList reads a list (the "info" event says "list").
func (a *App) runFetchList(ctx context.Context, seq int, link Link) {
	l, uerr := a.readList(ctx, link, func() {
		a.emit.Emit("info", newInfoEvent(seq, "updating", siteYouTube, nil, nil))
	})
	if ctx.Err() != nil {
		return
	}
	if uerr != nil {
		a.fetchEnded(seq, newInfoEvent(seq, "error", siteYouTube, nil, uerr))
		return
	}
	a.markHave(l)
	ev := newInfoEvent(seq, "list", siteYouTube, nil, nil)
	ev.List = l
	a.mu.Lock()
	if seq != a.fetchSeq {
		a.mu.Unlock()
		return
	}
	a.curList = &fetchedList{seq: seq, info: l}
	a.lastFetchEnd = ev.copy()
	a.mu.Unlock()
	a.emit.Emit("info", ev)
}

// readList asks yt-dlp for a list, updating it once when YouTube changed something.
func (a *App) readList(ctx context.Context, link Link, onRepair func()) (*ListInfo, *UserError) {
	if len(a.tools.Missing()) > 0 {
		return nil, NewUserError("tools_missing").Localize(siteYouTube)
	}
	repaired := false
	for {
		data, logText, uerr := a.fetchListJSON(ctx, link.URL)
		if ctx.Err() != nil {
			return nil, nil
		}
		if uerr == nil {
			l, err := BuildList(data)
			if err == nil {
				l.URL = link.URL
				return l, nil
			}
			var ie *InfoError
			if errors.As(err, &ie) {
				uerr = NewUserError(ie.Code)
				uerr.Details = tailLines(logText, 25)
			} else {
				uerr = NewUserError("unknown")
				uerr.Details = "bad json: " + err.Error()
			}
		}
		if !repaired && (uerr.Outdated || uerr.JSRuntime) {
			repaired = true
			if onRepair != nil {
				onRepair()
			}
			if a.repair(ctx, uerr) {
				continue
			}
		}
		return nil, uerr.Localize(siteYouTube)
	}
}

func (a *App) fetchListJSON(ctx context.Context, link string) ([]byte, string, *UserError) {
	if f := devListJSON(a.tools); f != "" {
		if b, err := os.ReadFile(f); err == nil {
			return b, "", nil
		}
	}
	release := a.tools.Acquire()
	args := append(a.tools.YtdlpArgs(a.paths.Cache), "-J", "--flat-playlist", "--yes-playlist", "--", link)
	res, err := runTool(ctx, a.tools.YtdlpExe(), args, a.tools.Env(), true, nil)
	release()
	if errors.Is(err, errCancelled) {
		return nil, "", nil
	}
	out := bytes.TrimSpace(res.Stdout)
	if len(out) > 0 && out[0] == '{' && json.Valid(out) {
		return out, res.Log, nil
	}
	if err != nil && strings.TrimSpace(res.Log) == "" {
		return nil, "", a.brokenTool(err)
	}
	return nil, res.Log, ClassifyFor(res.Log, siteYouTube)
}

// DownloadList adds the videos picks (their places in the list, 1-based) of the list on screen
// to the downloads, all in quality key ("v720", "mp3-192"), without their music if noMusic.
// It returns {"ids", "added", "skipped" (already in the list), "folder"} or {"error"}.
func (a *App) DownloadList(seq int, picks []int, key string, noMusic bool) map[string]any {
	a.mu.Lock()
	fl := a.curList
	a.mu.Unlock()
	if fl == nil || fl.seq != seq {
		return map[string]any{"error": NewUserError("unknown")}
	}
	kind, label, formatSpec, sortSpec, audioQuality, ok := listFormat(key)
	if !ok {
		return map[string]any{"error": NewUserError("format")}
	}
	chosen := map[int]bool{}
	for _, p := range picks {
		chosen[p] = true
	}
	var entries []ListEntry
	last := 0
	for _, e := range fl.info.Entries {
		last = max(last, e.N)
		if chosen[e.N] && e.Available {
			entries = append(entries, e)
		}
	}
	if len(entries) == 0 {
		return map[string]any{"error": NewUserError("list_empty")}
	}
	a.store.Update(func(s *Settings) { s.LastKind = kind })
	cat := catVideo
	if kind == "audio" {
		cat = catMusic
	}
	parent := a.saveDirFor(cat)
	dir := filepath.Join(parent, safeBaseOr(fl.info.Title, min(60, fileBaseRoom(parent)), tr("قائمة تشغيل", "Playlist")))
	digits := max(2, len(strconv.Itoa(last)))
	room := max(fileBaseRoom(dir)-digits-3, 24)
	now := time.Now().UnixMilli()
	var added []*DownloadTask
	skipped := 0
	a.mu.Lock()
	for i, e := range entries {
		u := "https://www.youtube.com/watch?v=" + e.ID
		dup := false
		for _, x := range a.tasks {
			if !x.finished() && x.URL == u && x.Key == key && x.NoMusic == noMusic {
				dup = true
				break
			}
		}
		if dup {
			skipped++
			continue
		}
		base := fmt.Sprintf("%0*d - %s", digits, e.N, SafeFileBase(e.Title, room))
		if noMusic {
			base += noMusicSuffix()
		}
		ch := e.Channel
		if ch == "" {
			ch = fl.info.Channel
		}
		t := &DownloadTask{URL: u, Site: siteYouTube, Title: e.Title, Channel: ch, DurationText: e.DurationText,
			Key: key, Kind: kind, Label: label, FormatSpec: formatSpec, SortSpec: sortSpec, AudioQuality: audioQuality,
			Parts: listParts(kind, e.Duration), Base: base, SaveDir: dir, CreatedAt: now + int64(i), QueuedAt: now + int64(i),
			State: stateQueued, ETA: -1, thumb: e.Thumbnail, Category: cat, NoMusic: noMusic, List: fl.info.Title}
		t.ID = a.newID()
		a.tasks = append(a.tasks, t)
		added = append(added, t)
	}
	dropped := a.trimFinishedLocked()
	a.mu.Unlock()
	for _, d := range dropped {
		a.forgetTask(d)
	}
	ids := []string{}
	for _, t := range added {
		a.dl.writeThumb(t.ID, t.thumb)
		a.emitTask("add", t, true)
		ids = append(ids, t.ID)
	}
	if len(added) > 0 {
		a.saveTasksSoon()
		a.wg.Add(1)
		go func() {
			defer a.wg.Done()
			a.schedule()
		}()
	}
	return map[string]any{"ids": ids, "added": len(added), "skipped": skipped, "folder": folderLabel(dir)}
}
