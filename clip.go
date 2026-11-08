package main

// A part of a video (since 2.4, as in HELINK for Android 1.7): the window plays the video,
// the user sets where the part starts and ends on its timeline and plays the part, then
// downloads it as a video or as MP3. yt-dlp cuts it as it downloads (--download-sections,
// app.go). The window plays the video through a small server of HELINK's on 127.0.0.1,
// which asks the site with the headers and cookies it needs (and lets the player jump
// anywhere in the file).

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// The preview: which of the site's files the window plays
// ---------------------------------------------------------------------------

// rawFormat is a format as yt-dlp describes it in its info JSON.
type rawFormat struct {
	FormatID    string            `json:"format_id"`
	URL         string            `json:"url"`
	Protocol    string            `json:"protocol"`
	Ext         string            `json:"ext"`
	VCodec      string            `json:"vcodec"`
	ACodec      string            `json:"acodec"`
	Height      float64           `json:"height"`
	TBR         float64           `json:"tbr"`
	ABR         float64           `json:"abr"`
	HTTPHeaders map[string]string `json:"http_headers"`
	Cookies     string            `json:"cookies"`
}

// clipPreview is what the window plays: one file with picture and sound ("av"), a picture
// file and a sound file played together ("split"), or the sound only ("audio").
type clipPreview struct {
	Kind     string
	Video    *rawFormat
	Audio    *rawFormat
	Duration float64
}

func (f *rawFormat) hasVideo() bool { return f.VCodec != "" && f.VCodec != "none" }
func (f *rawFormat) hasAudio() bool { return f.ACodec != "" && f.ACodec != "none" }

// playable: a whole file the window's player opens (not a stream of pieces).
func (f *rawFormat) playable() bool {
	if !strings.HasPrefix(f.URL, "https://") && !strings.HasPrefix(f.URL, "http://") {
		return false
	}
	if f.Protocol != "" && f.Protocol != "https" && f.Protocol != "http" {
		return false
	}
	switch strings.ToLower(f.Ext) {
	case "mp4", "m4a", "m4v", "webm", "mov", "mp3":
		return true
	}
	return false
}

// videoScore: an H.264 picture of 360p to 720p, nearest to 480p, is best (light and sharp
// enough to find a moment); higher is better.
func videoScore(f *rawFormat) float64 {
	h := f.Height
	if h <= 0 {
		h = 360
	}
	s := 1000 - math.Abs(h-480)
	if h > 720 {
		s -= 600
	}
	if strings.HasPrefix(f.VCodec, "avc") || strings.HasPrefix(f.VCodec, "h264") {
		s += 150
	}
	if strings.EqualFold(f.Ext, "mp4") {
		s += 50
	}
	return s
}

func audioScore(f *rawFormat) float64 {
	s := math.Min(f.ABR, 160)
	if strings.EqualFold(f.Ext, "m4a") || strings.HasPrefix(f.ACodec, "mp4a") {
		s += 40
	}
	return s
}

// pickPreview chooses the files to play from a video's info JSON.
func pickPreview(info []byte) (*clipPreview, error) {
	var v struct {
		Duration float64     `json:"duration"`
		Formats  []rawFormat `json:"formats"`
		rawFormat
	}
	if err := json.Unmarshal(info, &v); err != nil {
		return nil, err
	}
	formats := v.Formats
	if len(formats) == 0 && v.URL != "" {
		formats = []rawFormat{v.rawFormat} // a single file, described at the top
	}
	var av, vid, aud *rawFormat
	for i := range formats {
		f := &formats[i]
		if !f.playable() {
			continue
		}
		switch {
		case f.hasVideo() && f.hasAudio():
			if av == nil || videoScore(f) > videoScore(av) {
				av = f
			}
		case f.hasVideo():
			if vid == nil || videoScore(f) > videoScore(vid) {
				vid = f
			}
		case f.hasAudio():
			if aud == nil || audioScore(f) > audioScore(aud) {
				aud = f
			}
		case f.VCodec == "" && f.ACodec == "":
			// A file yt-dlp knows nothing about (a direct MP4): played as it is.
			if av == nil {
				av = f
			}
		}
	}
	p := &clipPreview{Duration: v.Duration}
	switch {
	case av != nil && (vid == nil || videoScore(av) >= videoScore(vid)-200):
		p.Kind, p.Video = "av", av
	case vid != nil && aud != nil:
		p.Kind, p.Video, p.Audio = "split", vid, aud
	case av != nil:
		p.Kind, p.Video = "av", av
	case aud != nil:
		p.Kind, p.Audio = "audio", aud
	case vid != nil:
		p.Kind, p.Video = "av", vid // a picture without sound
	default:
		return nil, errNoPreview
	}
	return p, nil
}

type previewError string

func (e previewError) Error() string { return string(e) }

const errNoPreview = previewError("no playable file")

// headersOf are the headers a format is asked with (yt-dlp's, the site's cookies).
func headersOf(f *rawFormat, cookies []Cookie, page string) http.Header {
	h := http.Header{}
	for k, v := range f.HTTPHeaders {
		h.Set(k, v)
	}
	if h.Get("User-Agent") == "" {
		h.Set("User-Agent", desktopUA)
	}
	c := f.Cookies
	if c == "" && len(cookies) > 0 {
		c = cookieHeaderFor(cookies, f.URL)
	}
	if c != "" {
		h.Set("Cookie", c)
	}
	if h.Get("Referer") == "" && page != "" {
		h.Set("Referer", page)
	}
	return h
}

// cookieHeaderFor is a Cookie header with the cookies that belong to an address.
func cookieHeaderFor(cookies []Cookie, u string) string {
	host := hostOf(u)
	var parts []string
	for _, c := range cookies {
		d := strings.TrimPrefix(strings.ToLower(c.Domain), ".")
		if d == "" || host == d || strings.HasSuffix(host, "."+d) {
			parts = append(parts, c.Name+"="+c.Value)
		}
	}
	return strings.Join(parts, "; ")
}

// ---------------------------------------------------------------------------
// The local server
// ---------------------------------------------------------------------------

type previewSource struct {
	group   string
	url     string
	headers http.Header
	at      time.Time
}

// previewServer serves the site's files to the window's player at
// http://127.0.0.1:<port>/m/<token> (the token: 32 random characters).
type previewServer struct {
	mu      sync.Mutex
	ln      net.Listener
	srv     *http.Server
	sources map[string]*previewSource
	client  *http.Client
}

func (p *previewServer) start() error {
	if p.ln != nil {
		return nil
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	p.ln = ln
	p.sources = map[string]*previewSource{}
	p.client = &http.Client{Timeout: 0, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment,
		ResponseHeaderTimeout: 30 * time.Second, IdleConnTimeout: 60 * time.Second}}
	p.srv = &http.Server{Handler: http.HandlerFunc(p.serve), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = p.srv.Serve(ln) }()
	return nil
}

// add makes a file playable; its address for the window.
func (p *previewServer) add(group, url string, h http.Header) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.start(); err != nil {
		return "", err
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	tok := hex.EncodeToString(b)
	p.sources[tok] = &previewSource{group: group, url: url, headers: h, at: time.Now()}
	if len(p.sources) > 200 {
		var oldest string
		for k, s := range p.sources {
			if oldest == "" || s.at.Before(p.sources[oldest].at) {
				oldest = k
			}
		}
		delete(p.sources, oldest)
	}
	return "http://" + p.ln.Addr().String() + "/m/" + tok, nil
}

// drop forgets a group's files (the clip window closed).
func (p *previewServer) drop(group string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, s := range p.sources {
		if s.group == group {
			delete(p.sources, k)
		}
	}
}

func (p *previewServer) stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.srv != nil {
		_ = p.srv.Close()
		p.srv, p.ln = nil, nil
	}
}

func (p *previewServer) serve(w http.ResponseWriter, r *http.Request) {
	tok := strings.TrimPrefix(r.URL.Path, "/m/")
	p.mu.Lock()
	src := p.sources[tok]
	p.mu.Unlock()
	if src == nil || (r.Method != "GET" && r.Method != "HEAD") {
		http.NotFound(w, r)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, src.url, nil)
	if err != nil {
		http.Error(w, "bad", http.StatusBadGateway)
		return
	}
	for k, v := range src.headers {
		req.Header[k] = v
	}
	if rg := r.Header.Get("Range"); rg != "" {
		req.Header.Set("Range", rg)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		if r.Context().Err() == nil {
			log.Printf("preview: %v", err)
		}
		http.Error(w, "upstream", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for _, k := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "Last-Modified", "ETag"} {
		if v := resp.Header.Get(k); v != "" {
			w.Header().Set(k, v)
		}
	}
	if w.Header().Get("Accept-Ranges") == "" {
		w.Header().Set("Accept-Ranges", "bytes")
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(resp.StatusCode)
	if r.Method == "GET" {
		_, _ = io.Copy(w, resp.Body)
	}
}

// ---------------------------------------------------------------------------
// The app
// ---------------------------------------------------------------------------

// ClipPreview prepares the video on screen (one of its items) for the clip window: what it
// plays ({kind, video, audio}: addresses on HELINK's server), the video's length, title and
// picture.
func (a *App) ClipPreview(item int) map[string]any {
	a.mu.Lock()
	f := a.cur
	a.mu.Unlock()
	if f == nil || item < 0 || item >= len(f.items) {
		return map[string]any{"error": NewUserError("unknown")}
	}
	it := f.items[item]
	out := map[string]any{"item": item, "title": it.Meta.Title, "duration": it.Meta.Duration,
		"durationText": it.Meta.DurationText, "thumbnail": it.Meta.Thumbnail, "site": f.site, "siteName": siteName(f.site)}
	b, err := os.ReadFile(it.infoPath)
	if err != nil {
		out["kind"] = "none"
		return out
	}
	pv, err := pickPreview(b)
	if err != nil {
		out["kind"] = "none"
		return out
	}
	if pv.Duration > 0 && it.Meta.Duration <= 0 {
		out["duration"] = pv.Duration
	}
	a.previews.drop("clip")
	cookies := f.opts.cookies
	if f.site == siteYouTube {
		cookies = nil // never YouTube's
	}
	out["kind"] = pv.Kind
	if pv.Video != nil {
		u, err := a.previews.add("clip", pv.Video.URL, headersOf(pv.Video, cookies, f.page))
		if err != nil {
			out["kind"] = "none"
			return out
		}
		out["video"] = u
	}
	if pv.Audio != nil {
		u, err := a.previews.add("clip", pv.Audio.URL, headersOf(pv.Audio, cookies, f.page))
		if err != nil {
			out["kind"] = "none"
			return out
		}
		out["audio"] = u
	}
	return out
}

// ClipClosed forgets the clip window's files.
func (a *App) ClipClosed() { a.previews.drop("clip") }

// ClipDownload downloads the part from start to end (seconds) of the video on screen, in
// the quality chosen (its key: a video quality, or MP3).
func (a *App) ClipDownload(item int, key string, start, end float64, noMusic bool) map[string]any {
	a.mu.Lock()
	f := a.cur
	a.mu.Unlock()
	if f == nil || item < 0 || item >= len(f.items) {
		return map[string]any{"error": NewUserError("unknown")}
	}
	dur := f.items[item].Meta.Duration
	start = math.Max(0, math.Round(start*10)/10)
	end = math.Round(end*10) / 10
	if dur > 0 {
		end = math.Min(end, dur)
	}
	if end-start < 1 {
		return map[string]any{"error": NewUserError("clip_range")}
	}
	a.store.Update(func(s *Settings) { s.ClipKind = kindOfKey(f.items[item].Options, key) })
	return a.downloadTo(f, item, key, noMusic, dlTarget{clipStart: start, clipEnd: end})
}

func kindOfKey(opts []QualityOption, key string) string {
	for _, o := range opts {
		if o.Key == key {
			return o.Kind
		}
	}
	return ""
}
