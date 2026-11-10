package main

// One file from a web address, the way download managers do it: the file is cut into
// parts that download over several connections at once (HTTP Range requests) and are
// written in place into one ".part" file. When a connection finishes its part, it takes
// half of the biggest part still running, so every connection stays busy to the end.
// Where each part stands is saved next to the file every two seconds (after the bytes it
// counts are on the disk): a download that was paused, or stopped by closing the app or
// turning the computer off, continues where it was.

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"mime"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

const (
	defaultConns  = 8
	maxConns      = 16
	minSegment    = 1 << 20   // a file is not cut into first parts smaller than this
	minSplit      = 256 << 10 // a running part of unknown speed is cut in two only if twice this is left
	minPiece      = 64 << 10  // no part is cut into pieces smaller than this
	partFileName  = "data.part"
	stateFileName = "state.json"
	fallbackUA    = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36"
)

// Waits (variables, so the tests can shorten them).
var (
	idleTimeout   = 45 * time.Second // a connection that brings nothing for this long is opened again
	stallTimeout  = 3 * time.Minute  // nothing at all on any connection for this long: give up for now
	stateInterval = 2 * time.Second  // how often where the parts stand is saved
	retryBase     = time.Second      // the first wait before asking again (it doubles, up to 16 times)
	connSpacing   = 150 * time.Millisecond
	progressEvery = 500 * time.Millisecond
)

// Cookie is one of the browser's cookies for the download (sent only where it belongs).
type Cookie struct {
	Name     string  `json:"name"`
	Value    string  `json:"value"`
	Domain   string  `json:"domain"`
	Path     string  `json:"path,omitempty"`
	Secure   bool    `json:"secure,omitempty"`
	HostOnly bool    `json:"hostOnly,omitempty"`
	Expires  float64 `json:"expirationDate,omitempty"` // seconds since 1970; 0 for a session cookie
}

// FileSource is where a file comes from and how to ask for it like the browser did.
type FileSource struct {
	URL       string   `json:"url"`
	FinalURL  string   `json:"finalUrl,omitempty"` // where the browser ended up after redirects
	Referer   string   `json:"referer,omitempty"`
	UserAgent string   `json:"userAgent,omitempty"`
	Cookies   []Cookie `json:"cookies,omitempty"`
}

// FileInfo is what the server said about the file.
type FileInfo struct {
	Name        string `json:"name"`
	Size        int64  `json:"size"` // -1 when the server does not say
	Resumable   bool   `json:"resumable"`
	ContentType string `json:"contentType,omitempty"`
	FinalURL    string `json:"finalUrl,omitempty"`
}

// FileProgress is the state of a running file download.
type FileProgress struct {
	Done, Total int64   // Total is -1 while unknown
	Speed       float64 // bytes per second
	ETA         float64 // seconds; -1 while unknown
	Conns       int     // connections receiving right now
	Resumable   bool
	Segments    [][3]int64 // start, end, bytes done: for the picture of the parts
}

// DownloadError says why a file could not be downloaded, as a code the app puts in words:
// network, tls, forbidden, gone, server, page_not_file, disk, permission, changed,
// noparts, badurl, unknown.
type DownloadError struct {
	Code       string
	Status     int // the HTTP status, if any
	Err        error
	Need, Free int64 // for "disk": the bytes the file needs and the bytes free
}

func (e *DownloadError) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("%s (HTTP %d): %v", e.Code, e.Status, e.Err)
	}
	return fmt.Sprintf("%s: %v", e.Code, e.Err)
}

func (e *DownloadError) Unwrap() error { return e.Err }

func dlErr(code string, status int, format string, args ...any) *DownloadError {
	return &DownloadError{Code: code, Status: status, Err: fmt.Errorf(format, args...)}
}

// errCode is the DownloadError code of err ("" for none).
func errCode(err error) string {
	var de *DownloadError
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}

// statusError turns a refusing answer into a DownloadError.
func statusError(code int) *DownloadError {
	switch {
	case code == 401 || code == 403:
		return dlErr("forbidden", code, "the server refused")
	case code == 404 || code == 410:
		return dlErr("gone", code, "not on the server")
	case code == 416:
		return dlErr("changed", code, "that part is not there any more")
	case code == 408 || code == 425 || code == 429 || code >= 500:
		return dlErr("server", code, "the server is busy")
	default:
		return dlErr("unknown", code, "unexpected answer")
	}
}

func netError(err error) *DownloadError {
	var cv *tls.CertificateVerificationError
	if errors.As(err, &cv) {
		return &DownloadError{Code: "tls", Err: err}
	}
	return &DownloadError{Code: "network", Err: err}
}

// fileError says whether writing failed for lack of space or for another reason.
func fileError(err error) *DownloadError {
	m := strings.ToLower(err.Error())
	if strings.Contains(m, "space") || strings.Contains(m, "disk is full") || strings.Contains(m, "disk full") {
		return &DownloadError{Code: "disk", Err: err}
	}
	return &DownloadError{Code: "permission", Err: err}
}

// ---------------------------------------------------------------------------
// Asking for the file like the browser
// ---------------------------------------------------------------------------

// matches says whether the browser would send the cookie with a request to u.
func (c Cookie) matches(u *url.URL, now time.Time) bool {
	if c.Name == "" || strings.ContainsAny(c.Name, "=;, \t\r\n") || strings.ContainsAny(c.Value, ";\r\n") {
		return false
	}
	if c.Expires > 0 && float64(now.Unix()) > c.Expires {
		return false
	}
	if c.Secure && u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	d := strings.ToLower(strings.TrimPrefix(c.Domain, "."))
	if d == "" {
		return false
	}
	if c.HostOnly {
		if host != d {
			return false
		}
	} else if host != d && !strings.HasSuffix(host, "."+d) {
		return false
	}
	p := c.Path
	if p == "" {
		p = "/"
	}
	rp := u.EscapedPath()
	if rp == "" {
		rp = "/"
	}
	return rp == p || strings.HasPrefix(rp, strings.TrimSuffix(p, "/")+"/")
}

// fetcher asks for a file as the browser that found it would: its User-Agent, Referer and
// cookies (each sent only where it belongs, and exactly as it is), plus any cookie a
// server sets on the way through redirects.
type fetcher struct {
	src     FileSource
	browser []Cookie
	jar     *cookiejar.Jar
	hc      *http.Client
}

// downloadTransport opens one real connection per request (HTTP/1.1): several
// connections are what makes a download in parts fast; HTTP/2 would put them all on one.
var downloadTransport = func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = systemProxy
	t.ForceAttemptHTTP2 = false
	t.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	// HTTP/1.1 only, and said so to the server: since Go 1.24 a transport cloned from the
	// default one still offers HTTP/2 when the connection is opened (ALPN "h2"), and a server
	// that takes it (Amazon's, AliExpress's and Alibaba's pictures, most big sites) then answers
	// in HTTP/2, which this transport can't read ("malformed HTTP response \x00\x00…").
	var p http.Protocols
	p.SetHTTP1(true)
	t.Protocols = &p
	t.TLSClientConfig = &tls.Config{NextProtos: []string{"http/1.1"}}
	t.DialContext = (&net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	t.TLSHandshakeTimeout = 20 * time.Second
	t.ResponseHeaderTimeout = 45 * time.Second
	t.MaxIdleConnsPerHost = maxConns
	t.IdleConnTimeout = 60 * time.Second
	t.DisableCompression = true // the bytes as the server keeps them: ranges count those
	return t
}()

func newFetcher(src FileSource) *fetcher {
	jar, _ := cookiejar.New(nil)
	bc := append([]Cookie(nil), src.Cookies...)
	// Longer paths first, as browsers send them.
	sort.SliceStable(bc, func(a, b int) bool { return len(bc[a].Path) > len(bc[b].Path) })
	return &fetcher{src: src, browser: bc, jar: jar, hc: &http.Client{
		Transport: downloadTransport,
		// Redirects are followed in get, to send each address its own cookies.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (f *fetcher) cookieHeader(u *url.URL) string {
	var parts []string
	set := map[string]bool{}
	for _, c := range f.jar.Cookies(u) {
		set[c.Name] = true
		parts = append(parts, c.Name+"="+c.Value)
	}
	now := time.Now()
	for _, c := range f.browser {
		// A cookie a server set on the way replaces the browser's of that name.
		if !set[c.Name] && c.matches(u, now) {
			parts = append(parts, c.Name+"="+c.Value)
		}
	}
	return strings.Join(parts, "; ")
}

// get asks for target (following redirects) with the extra headers.
func (f *fetcher) get(ctx context.Context, target string, extra http.Header) (*http.Response, error) {
	cur := target
	for hop := 0; ; hop++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, cur, nil)
		if err != nil || (req.URL.Scheme != "http" && req.URL.Scheme != "https") || req.URL.Host == "" {
			return nil, dlErr("badurl", 0, "not a web address: %q", cur)
		}
		ua := f.src.UserAgent
		if ua == "" {
			ua = fallbackUA
		}
		req.Header.Set("User-Agent", ua)
		req.Header.Set("Accept", "*/*")
		req.Header.Set("Accept-Encoding", "identity")
		if f.src.Referer != "" {
			req.Header.Set("Referer", f.src.Referer)
		}
		for k, v := range extra {
			req.Header[k] = v
		}
		if c := f.cookieHeader(req.URL); c != "" {
			req.Header.Set("Cookie", c)
		}
		resp, err := f.hc.Do(req)
		if err != nil {
			return nil, netError(err)
		}
		if rc := resp.Cookies(); len(rc) > 0 {
			f.jar.SetCookies(req.URL, rc)
		}
		switch resp.StatusCode {
		case 301, 302, 303, 307, 308:
			loc, lerr := resp.Location()
			drainClose(resp)
			if lerr != nil {
				return nil, dlErr("server", resp.StatusCode, "a redirect without an address")
			}
			if hop >= 15 {
				return nil, dlErr("server", resp.StatusCode, "too many redirects")
			}
			cur = loc.String()
			continue
		}
		return resp, nil
	}
}

func drainClose(r *http.Response) {
	if r == nil || r.Body == nil {
		return
	}
	_, _ = io.CopyN(io.Discard, r.Body, 64<<10)
	r.Body.Close()
}

// answer is a server's answer whose body brings bytes [start, end) of the file
// (end -1: until the body ends).
type answer struct {
	resp       *http.Response
	cancel     context.CancelFunc
	start, end int64
}

func (a *answer) close() {
	if a != nil {
		a.resp.Body.Close()
		a.cancel()
	}
}

// ---------------------------------------------------------------------------
// File names
// ---------------------------------------------------------------------------

// nameFromDisposition reads the file name in a Content-Disposition header ("" if none).
func nameFromDisposition(h string) string {
	if strings.TrimSpace(h) == "" {
		return ""
	}
	if _, params, err := mime.ParseMediaType(h); err == nil {
		if n := params["filename"]; n != "" {
			// (ParseMediaType has already decoded a filename*= value; browsers also undo
			// %-escapes in a plain filename=.)
			if !strings.Contains(strings.ToLower(h), "filename*=") {
				n = unescapeName(n)
			}
			return decodeName(n)
		}
	}
	// Servers write all sorts of things: look for filename*= and filename= by hand.
	lower := strings.ToLower(h)
	if i := strings.Index(lower, "filename*="); i >= 0 {
		v := strings.TrimSpace(h[i+len("filename*="):])
		if j := strings.IndexByte(v, ';'); j >= 0 {
			v = v[:j]
		}
		v = strings.Trim(v, `"' `)
		if k := strings.Index(v, "''"); k >= 0 {
			v = v[k+2:]
		}
		if d, err := url.PathUnescape(v); err == nil && d != "" {
			return decodeName(d)
		}
	}
	for from := 0; ; {
		i := strings.Index(lower[from:], "filename=")
		if i < 0 {
			return ""
		}
		v := strings.TrimSpace(h[from+i+len("filename="):])
		from += i + len("filename=")
		if strings.HasPrefix(v, `"`) {
			if j := strings.IndexByte(v[1:], '"'); j >= 0 {
				v = v[1 : j+1]
			} else {
				v = v[1:]
			}
		} else if j := strings.IndexByte(v, ';'); j >= 0 {
			v = v[:j]
		}
		if v = unescapeName(strings.TrimSpace(v)); v != "" {
			return decodeName(v)
		}
	}
}

// unescapeName undoes %-escapes when they make sense (UTF-8 text).
func unescapeName(v string) string {
	if strings.Contains(v, "%") {
		if d, err := url.PathUnescape(v); err == nil && utf8.ValidString(d) {
			return d
		}
	}
	return v
}

// decodeName undoes the encodings servers use in names: "=?UTF-8?B?...?=" and Latin-1.
func decodeName(s string) string {
	if strings.Contains(s, "=?") {
		if d, err := new(mime.WordDecoder).DecodeHeader(s); err == nil {
			s = d
		}
	}
	if !utf8.ValidString(s) {
		r := make([]rune, 0, len(s))
		for i := 0; i < len(s); i++ {
			r = append(r, rune(s[i]))
		}
		s = string(r)
	}
	// Only the name, never a folder.
	s = strings.ReplaceAll(s, "\\", "/")
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		s = s[i+1:]
	}
	return strings.TrimSpace(s)
}

// nameFromURL is the last part of the address's path ("" if there is none).
func nameFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	p, err := url.PathUnescape(u.EscapedPath())
	if err != nil {
		p = u.Path
	}
	base := path.Base(p)
	if base == "." || base == "/" || base == "" {
		return ""
	}
	return decodeName(base)
}

var extByMime = map[string]string{
	"application/zip": "zip", "application/x-zip-compressed": "zip", "application/x-rar-compressed": "rar",
	"application/vnd.rar": "rar", "application/x-7z-compressed": "7z", "application/gzip": "gz",
	"application/x-gzip": "gz", "application/x-tar": "tar", "application/pdf": "pdf",
	"application/x-msdownload": "exe", "application/x-msdos-program": "exe", "application/x-msi": "msi",
	"application/vnd.android.package-archive": "apk", "application/x-iso9660-image": "iso",
	"application/msword": "doc", "application/vnd.ms-excel": "xls", "application/vnd.ms-powerpoint": "ppt",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document":   "docx",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         "xlsx",
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": "pptx",
	"video/mp4": "mp4", "video/webm": "webm", "video/x-matroska": "mkv", "video/quicktime": "mov",
	"video/x-msvideo": "avi", "video/x-flv": "flv", "video/3gpp": "3gp", "video/mp2t": "ts",
	"audio/mpeg": "mp3", "audio/mp4": "m4a", "audio/aac": "aac", "audio/ogg": "ogg", "audio/wav": "wav",
	"audio/x-wav": "wav", "audio/flac": "flac", "audio/opus": "opus", "image/jpeg": "jpg", "image/png": "png",
	"image/gif": "gif", "image/webp": "webp", "image/svg+xml": "svg", "text/plain": "txt", "text/csv": "csv",
	"application/json": "json", "application/epub+zip": "epub", "application/x-bittorrent": "torrent",
}

// splitName gives a file name's base and extension (without the dot, as written).
func splitName(name string) (string, string) {
	ext := filepath.Ext(name)
	if len(ext) < 2 || len(ext) > 12 || strings.ContainsAny(ext, " ()[]") {
		return name, ""
	}
	return strings.TrimSuffix(name, ext), ext[1:]
}

// extOf is a file name's extension in lower case ("" if none).
func extOf(name string) string {
	_, ext := splitName(name)
	return strings.ToLower(ext)
}

// cleanFileName makes a name safe for Windows and not too long, keeping its extension
// (from the content type when the name has none).
func cleanFileName(name, contentType string) string {
	base, ext := splitName(strings.TrimSpace(strings.ReplaceAll(name, "\x00", "")))
	if ext == "" && contentType != "" {
		mt, _, _ := mime.ParseMediaType(contentType)
		ext = extByMime[strings.ToLower(mt)]
	}
	b := SafeName(base, 150)
	if ext == "" {
		return b
	}
	return b + "." + SafeName(ext, 12)
}

// ---------------------------------------------------------------------------
// Where the parts stand (saved next to the file)
// ---------------------------------------------------------------------------

type segState struct {
	Start int64 `json:"s"`
	End   int64 `json:"e"`
	Done  int64 `json:"d"`
}

type dlState struct {
	Version     int        `json:"v"`
	URL         string     `json:"url"`
	FinalURL    string     `json:"finalUrl,omitempty"`
	Size        int64      `json:"size"`
	Resumable   bool       `json:"resumable"`
	ETag        string     `json:"etag,omitempty"`
	LastMod     string     `json:"lastModified,omitempty"`
	Name        string     `json:"name"`
	ContentType string     `json:"contentType,omitempty"`
	Segments    []segState `json:"segments,omitempty"`
}

func freshState(u string) dlState { return dlState{Version: 1, URL: u, Size: -1} }

// valid says whether the parts cover the file exactly once.
func (st *dlState) valid() bool {
	if st.Version != 1 || !st.Resumable || st.Size <= 0 || len(st.Segments) == 0 {
		return false
	}
	at := int64(0)
	for _, s := range st.Segments {
		if s.Start != at || s.End <= s.Start || s.Done < 0 || s.Done > s.End-s.Start {
			return false
		}
		at = s.End
	}
	return at == st.Size
}

func loadState(dir string) (*dlState, bool) {
	b, err := os.ReadFile(filepath.Join(dir, stateFileName))
	if err != nil {
		return nil, false
	}
	var st dlState
	if json.Unmarshal(b, &st) != nil || !st.valid() {
		return nil, false
	}
	return &st, true
}

func saveState(dir string, st *dlState) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	p := filepath.Join(dir, stateFileName)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// StateDone reads how much of a paused download is on the disk (for the list).
func StateDone(dir string) (done, size int64, ok bool) {
	st, ok := loadState(dir)
	if !ok {
		return 0, 0, false
	}
	for _, s := range st.Segments {
		done += s.Done
	}
	return done, st.Size, true
}

// ---------------------------------------------------------------------------
// The download
// ---------------------------------------------------------------------------

type seg struct {
	mu    sync.Mutex // held while bytes are written into the part and while it is cut
	start int64
	end   atomic.Int64 // exclusive; it moves back when the part is cut in two
	done  atomic.Int64 // bytes written from start
	owner int          // the connection on it, or -1 (under FileDownload.mu)
	since atomic.Int64 // when its current answer started to arrive (unix nanoseconds), for its speed
	from  atomic.Int64 // done at that time
}

// timeLeft is how long the part's connection needs for the rest, judged by its speed so
// far (ok false while that is not known yet).
func (s *seg) timeLeft(now int64) (float64, bool) {
	t0 := s.since.Load()
	if t0 == 0 || now-t0 < int64(300*time.Millisecond) {
		return 0, false
	}
	speed := float64(s.done.Load()-s.from.Load()) / (float64(now-t0) / 1e9)
	if speed <= 0 {
		return math.Inf(1), true
	}
	return float64(s.remaining()) / speed, true
}

func (s *seg) remaining() int64 { return s.end.Load() - s.start - s.done.Load() }

// FileDownload downloads one file into Dir (data.part, state.json).
type FileDownload struct {
	Src        FileSource
	Dir        string
	Name       string // the name the browser or the user chose; "" lets the server decide
	Conns      int
	OnInfo     func(FileInfo)     // once the server has answered
	OnProgress func(FileProgress) // twice a second

	f       *fetcher
	file    *os.File
	mu      sync.Mutex
	st      dlState // under mu (the parts are in segs)
	segs    []*seg  // under mu
	fatal   error   // under mu: what ends the download
	refusal error   // under mu: the last refusal a connection got
	netErr  error   // under mu: the last broken connection
	ckMu    sync.Mutex
	active  atomic.Int32 // connections receiving right now
	got     atomic.Int64 // bytes received in this run (for the speed)
	last    atomic.Int64 // when a byte last arrived (unix nanoseconds)
	ranged  atomic.Bool  // the server has answered a request for a part with that part
	stopCh  chan struct{}
	stop    *sync.Once
}

// Info returns what the server said about the file.
func (d *FileDownload) Info() FileInfo {
	d.mu.Lock()
	defer d.mu.Unlock()
	return FileInfo{Name: d.st.Name, Size: d.st.Size, Resumable: d.st.Resumable, ContentType: d.st.ContentType, FinalURL: d.st.FinalURL}
}

func (d *FileDownload) fail(err error) {
	d.mu.Lock()
	if d.fatal == nil {
		d.fatal = err
	}
	d.mu.Unlock()
	d.stop.Do(func() { close(d.stopCh) })
}

// Run downloads until the file is complete (it returns the finished data's path, still
// in Dir; Info has the name for it), ctx is cancelled (a pause: everything is kept for
// the next Run), or something fails.
func (d *FileDownload) Run(ctx context.Context) (string, error) {
	d.Conns = max(1, min(maxConns, d.Conns))
	if err := os.MkdirAll(d.Dir, 0o755); err != nil {
		return "", &DownloadError{Code: "permission", Err: err}
	}
	for round := 0; ; round++ {
		p, err := d.run(ctx)
		if round == 0 && errCode(err) == "changed" && ctx.Err() == nil {
			// The file changed on the server while it was downloading: once more from the start.
			log.Printf("file download %s: changed on the server, starting over", d.Src.URL)
			_ = os.Remove(filepath.Join(d.Dir, stateFileName))
			_ = os.Remove(filepath.Join(d.Dir, partFileName))
			continue
		}
		return p, err
	}
}

func (d *FileDownload) run(ctx context.Context) (string, error) {
	d.f = newFetcher(d.Src)
	d.stopCh, d.stop = make(chan struct{}), new(sync.Once)
	d.mu.Lock()
	d.segs, d.fatal, d.refusal, d.netErr = nil, nil, nil, nil
	d.st = freshState(d.Src.URL)
	d.mu.Unlock()
	d.active.Store(0)
	d.got.Store(0)
	d.ranged.Store(false)
	partPath := filepath.Join(d.Dir, partFileName)
	if st, ok := loadState(d.Dir); ok {
		if fi, err := os.Stat(partPath); err == nil && fi.Size() == st.Size {
			d.mu.Lock()
			d.st = *st
			d.mu.Unlock()
		}
	}

	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	go func() {
		select {
		case <-d.stopCh:
			cancelRun()
		case <-runCtx.Done():
		}
	}()

	first, err := d.firstRequest(runCtx)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", err
	}
	defer first.close()
	resume := len(d.st.Segments) > 0
	if d.OnInfo != nil {
		d.OnInfo(d.Info())
	}

	flags := os.O_RDWR | os.O_CREATE
	if !resume {
		flags |= os.O_TRUNC
		_ = os.Remove(filepath.Join(d.Dir, stateFileName))
	}
	f, err := os.OpenFile(partPath, flags, 0o644)
	if err != nil {
		return "", fileError(err)
	}
	d.file = f
	defer f.Close()
	if size := d.st.Size; !resume && size > 0 {
		if free, ok := freeSpace(d.Dir); ok && free < size+(32<<20) {
			return "", &DownloadError{Code: "disk", Err: fmt.Errorf("the file needs %d bytes, %d are free", size, free),
				Need: size + (32 << 20), Free: free}
		}
		makeSparse(f)
		if err := f.Truncate(size); err != nil {
			return "", fileError(err)
		}
	}
	if d.st.Resumable && !d.ranged.Load() && !d.verifyRanges(runCtx) {
		// The server said it gives parts but answered with the whole file, and it does not
		// give a part when asked: one connection, then.
		d.mu.Lock()
		d.st.Resumable = false
		d.mu.Unlock()
	}
	if d.st.Resumable {
		err = d.runParts(runCtx, first)
	} else {
		err = d.runSingle(runCtx, first)
	}
	d.checkpoint()
	d.mu.Lock()
	if d.fatal != nil {
		err = d.fatal
	}
	d.mu.Unlock()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		return "", err
	}
	if err := f.Sync(); err != nil {
		return "", fileError(err)
	}
	if fi, serr := f.Stat(); serr != nil || fi.Size() != d.st.Size {
		return "", dlErr("unknown", 0, "the file on the disk does not have the size the server said")
	}
	unsparse(f)
	return partPath, nil
}

// firstRequest asks for the file (from the start, or from the first missing byte when
// continuing) and learns its size, name and whether it can be downloaded in parts. The
// answer's body is kept: it becomes the first connection.
func (d *FileDownload) firstRequest(ctx context.Context) (*answer, error) {
	resume := len(d.st.Segments) > 0
	from, to := int64(0), int64(-1) // to: the last byte to ask for (-1: to the end)
	if resume {
		from = -1
		for _, s := range d.st.Segments {
			if s.Done < s.End-s.Start {
				from, to = s.Start+s.Done, s.End-1 // only the part it continues: nothing is sent for nothing
				break
			}
		}
		if from < 0 {
			from, to = 0, 0 // everything is there; the answer only confirms it is the same file
		}
	}
	targets := []string{d.Src.URL}
	if d.Src.FinalURL != "" && d.Src.FinalURL != d.Src.URL {
		targets = append(targets, d.Src.FinalURL)
	}
	ti, useRange := 0, true
	var lastErr error
	for attempt := 0; attempt < 6; attempt++ {
		h := http.Header{}
		if useRange {
			rng := "bytes=" + strconv.FormatInt(from, 10) + "-"
			if to >= 0 {
				rng += strconv.FormatInt(to, 10)
			}
			h.Set("Range", rng)
			if resume && d.st.URL == d.Src.URL {
				if d.st.ETag != "" {
					h.Set("If-Range", d.st.ETag)
				} else if d.st.LastMod != "" {
					h.Set("If-Range", d.st.LastMod)
				}
			}
		}
		rctx, cancel := context.WithCancel(ctx)
		resp, err := d.f.get(rctx, targets[ti], h)
		if err != nil {
			cancel()
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = err
			if c := errCode(err); c != "network" && c != "server" {
				return nil, err
			}
			if !sleepCtx(ctx, backoff(attempt)) {
				return nil, ctx.Err()
			}
			continue
		}
		code := resp.StatusCode
		if code == http.StatusOK || code == http.StatusPartialContent {
			a, err := d.learn(resp, cancel, from)
			if err != nil {
				resp.Body.Close()
				cancel()
				switch c := errCode(err); {
				case c == "changed" && resume, c == "norange" && resume:
					return d.restart(ctx)
				case c == "norange" && useRange:
					useRange = false // the whole file, then
					continue
				}
				return nil, err
			}
			return a, nil
		}
		drainClose(resp)
		cancel()
		if code == http.StatusRequestedRangeNotSatisfiable {
			if resume {
				return d.restart(ctx)
			}
			if useRange { // an empty file, or a server that does not like ranges
				useRange = false
				continue
			}
		}
		e := statusError(code)
		lastErr = e
		switch e.Code {
		case "forbidden", "gone":
			// The browser's final address may still work when the first one does not.
			if ti+1 < len(targets) {
				ti++
				continue
			}
			return nil, e
		case "server":
			if !sleepCtx(ctx, backoff(attempt)) {
				return nil, ctx.Err()
			}
			continue
		default:
			// Some servers refuse a Range they do not understand: once more without it.
			if useRange && !resume && code >= 400 && code < 500 {
				useRange = false
				continue
			}
			return nil, e
		}
	}
	if lastErr == nil {
		lastErr = dlErr("network", 0, "no answer")
	}
	return nil, lastErr
}

// verifyRanges asks for one byte in the middle of the file: whether the server really
// gives parts.
func (d *FileDownload) verifyRanges(ctx context.Context) bool {
	d.mu.Lock()
	target, size := d.st.FinalURL, d.st.Size
	d.mu.Unlock()
	if target == "" {
		target = d.Src.URL
	}
	at := size / 2
	h := http.Header{}
	h.Set("Range", "bytes="+strconv.FormatInt(at, 10)+"-"+strconv.FormatInt(at, 10))
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := d.f.get(rctx, target, h)
	if err != nil {
		return false
	}
	defer drainClose(resp)
	start, _, total, ok := parseContentRange(resp.Header.Get("Content-Range"))
	if resp.StatusCode == http.StatusPartialContent && ok && start == at && total == size {
		d.ranged.Store(true)
		return true
	}
	return false
}

// restart forgets what was downloaded and asks again from the start.
func (d *FileDownload) restart(ctx context.Context) (*answer, error) {
	log.Printf("file download %s: not the same file any more, starting over", d.Src.URL)
	d.mu.Lock()
	d.st = freshState(d.Src.URL)
	d.mu.Unlock()
	_ = os.Remove(filepath.Join(d.Dir, stateFileName))
	return d.firstRequest(ctx)
}

func strongTag(etag string) string {
	if strings.HasPrefix(etag, "W/") {
		return "" // a weak tag cannot say a part belongs to the same file
	}
	return etag
}

// learn reads the first answer: size, name, whether parts work, and (when continuing)
// whether it is still the same file.
func (d *FileDownload) learn(resp *http.Response, cancel context.CancelFunc, from int64) (*answer, error) {
	resume := len(d.st.Segments) > 0
	h := resp.Header
	ct := h.Get("Content-Type")
	size, resumable := int64(-1), false
	a := &answer{resp: resp, cancel: cancel, start: from, end: -1}
	if resp.StatusCode == http.StatusPartialContent {
		start, last, total, ok := parseContentRange(h.Get("Content-Range"))
		if !ok || start != from || total < 0 {
			// A part that was not asked for, or of a file whose size the server does not say.
			return nil, dlErr("norange", resp.StatusCode, "unexpected Content-Range %q", h.Get("Content-Range"))
		}
		size, resumable, a.end = total, total > 0, last+1
		d.ranged.Store(true)
	} else {
		if from > 0 {
			// Asked for the rest, got everything: the server cannot continue this file.
			return nil, dlErr("changed", resp.StatusCode, "the server sent the whole file again")
		}
		if resp.ContentLength >= 0 {
			size = resp.ContentLength
			a.end = size
		}
		resumable = size > 0 && strings.EqualFold(strings.TrimSpace(h.Get("Accept-Ranges")), "bytes")
	}
	etag, lastMod := strongTag(h.Get("ETag")), h.Get("Last-Modified")
	final := resp.Request.URL.String()
	if resume {
		same := size == d.st.Size
		// The same address must still have the same file; a new address for it (the old
		// link expired) needs only the same size.
		if same && d.st.URL == d.Src.URL {
			if d.st.ETag != "" && etag != "" {
				same = etag == d.st.ETag
			} else if d.st.ETag == "" && d.st.LastMod != "" && lastMod != "" {
				same = lastMod == d.st.LastMod
			}
		}
		if !same || !resumable {
			return nil, dlErr("changed", resp.StatusCode, "not the same file")
		}
		d.mu.Lock()
		d.st.URL, d.st.FinalURL = d.Src.URL, final
		if etag != "" {
			d.st.ETag = etag
		}
		if lastMod != "" {
			d.st.LastMod = lastMod
		}
		d.mu.Unlock()
		return a, nil
	}
	// A web page where a file was expected: the site wants a sign-in, or the link expired.
	disp := h.Get("Content-Disposition")
	mt, _, _ := mime.ParseMediaType(ct)
	if (mt == "text/html" || mt == "application/xhtml+xml") && !strings.HasPrefix(strings.ToLower(strings.TrimSpace(disp)), "attachment") {
		wantExt, urlExt := extOf(d.Name), extOf(nameFromURL(d.Src.URL))
		if wantExt != "htm" && wantExt != "html" && urlExt != "htm" && urlExt != "html" {
			return nil, dlErr("page_not_file", resp.StatusCode, "got a web page (%s)", ct)
		}
	}
	name := d.Name
	if name == "" {
		name = nameFromDisposition(disp)
	}
	if name == "" {
		name = nameFromURL(final)
	}
	if name == "" {
		name = nameFromURL(d.Src.URL)
	}
	d.mu.Lock()
	d.st = dlState{Version: 1, URL: d.Src.URL, FinalURL: final, Size: size, Resumable: resumable,
		ETag: etag, LastMod: lastMod, Name: cleanFileName(name, ct), ContentType: ct}
	d.mu.Unlock()
	return a, nil
}

// parseContentRange reads "bytes 100-199/1000" (total -1 for "*").
func parseContentRange(v string) (start, last, total int64, ok bool) {
	v = strings.TrimSpace(v)
	if len(v) < 6 || !strings.EqualFold(v[:6], "bytes ") {
		return 0, 0, 0, false
	}
	rng, tot, found := strings.Cut(strings.TrimSpace(v[6:]), "/")
	if !found {
		return 0, 0, 0, false
	}
	a, b, found := strings.Cut(rng, "-")
	if !found {
		return 0, 0, 0, false
	}
	s, err1 := strconv.ParseInt(strings.TrimSpace(a), 10, 64)
	l, err2 := strconv.ParseInt(strings.TrimSpace(b), 10, 64)
	if err1 != nil || err2 != nil || s < 0 || l < s {
		return 0, 0, 0, false
	}
	if strings.TrimSpace(tot) == "*" {
		return s, l, -1, true
	}
	t, err := strconv.ParseInt(strings.TrimSpace(tot), 10, 64)
	if err != nil || t <= l {
		return 0, 0, 0, false
	}
	return s, l, t, true
}

func backoff(attempt int) time.Duration {
	return retryBase << min(max(attempt, 0), 4) // 1, 2, 4, 8, 16 s
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// snapshot is the state with the parts as they are now (a finished part and the part
// right after it are one part).
func (d *FileDownload) snapshot() *dlState {
	d.mu.Lock()
	st := d.st
	parts := make([]segState, 0, len(d.segs))
	for _, s := range d.segs {
		end := s.end.Load()
		parts = append(parts, segState{Start: s.start, End: end, Done: min(s.done.Load(), end-s.start)})
	}
	d.mu.Unlock()
	sort.Slice(parts, func(a, b int) bool { return parts[a].Start < parts[b].Start })
	var out []segState
	for _, p := range parts {
		if n := len(out); n > 0 && out[n-1].Done == out[n-1].End-out[n-1].Start && out[n-1].End == p.Start {
			out[n-1].End = p.End
			out[n-1].Done += p.Done
			continue
		}
		out = append(out, p)
	}
	st.Segments = out
	return &st
}

// checkpoint saves where the parts stand, after making sure the bytes it counts are on
// the disk (so a power cut can never leave the state ahead of the file).
func (d *FileDownload) checkpoint() {
	d.ckMu.Lock()
	defer d.ckMu.Unlock()
	st := d.snapshot()
	if !st.Resumable || !st.valid() {
		return
	}
	if d.file != nil && d.file.Sync() != nil {
		return
	}
	if err := saveState(d.Dir, st); err != nil {
		log.Printf("file download: saving the state: %v", err)
	}
}

func (d *FileDownload) progress() FileProgress {
	st := d.snapshot()
	p := FileProgress{Total: st.Size, ETA: -1, Resumable: st.Resumable, Conns: int(d.active.Load())}
	for _, s := range st.Segments {
		p.Done += s.Done
		if st.Size > 0 {
			p.Segments = append(p.Segments, [3]int64{s.Start, s.End, s.Done})
		}
	}
	return p
}

// watch reports the progress twice a second, saves the state, and gives up when nothing
// at all has arrived for stallTimeout. It returns the function that stops it.
func (d *FileDownload) watch(ctx context.Context) func() {
	quit := make(chan struct{})
	var wg sync.WaitGroup
	d.last.Store(time.Now().UnixNano())
	wg.Add(2)
	go func() {
		defer wg.Done()
		tick := time.NewTicker(progressEvery)
		defer tick.Stop()
		var speed float64
		lastGot, lastAt := d.got.Load(), time.Now()
		report := func() {
			if d.OnProgress != nil {
				p := d.progress()
				p.Speed = speed
				if speed > 1 && p.Total > 0 {
					p.ETA = float64(p.Total-p.Done) / speed
				}
				d.OnProgress(p)
			}
		}
		for {
			select {
			case <-ctx.Done():
				report()
				return
			case <-quit:
				report()
				return
			case now := <-tick.C:
				got := d.got.Load()
				if dt := now.Sub(lastAt).Seconds(); dt > 0 {
					inst := float64(got-lastGot) / dt
					if speed == 0 {
						speed = inst
					} else {
						speed = 0.7*speed + 0.3*inst
					}
				}
				lastGot, lastAt = got, now
				if now.Sub(time.Unix(0, d.last.Load())) > stallTimeout {
					d.fail(dlErr("network", 0, "nothing arrived for %s", stallTimeout))
					return
				}
				report()
			}
		}
	}()
	go func() {
		defer wg.Done()
		tick := time.NewTicker(stateInterval)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-quit:
				return
			case <-tick.C:
				d.checkpoint()
			}
		}
	}()
	return func() {
		close(quit)
		wg.Wait()
	}
}

// put writes bytes that continue part s, only as many as still belong to it (it may have
// been cut meanwhile).
func (d *FileDownload) put(s *seg, b []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	pos := s.start + s.done.Load()
	if lim := s.end.Load() - pos; int64(len(b)) > lim {
		if lim <= 0 {
			return nil
		}
		b = b[:lim]
	}
	if _, err := d.file.WriteAt(b, pos); err != nil {
		return fileError(err)
	}
	n := int64(len(b))
	s.done.Add(n)
	d.got.Add(n)
	d.last.Store(time.Now().UnixNano())
	return nil
}

var bufPool = sync.Pool{New: func() any { b := make([]byte, 128<<10); return &b }}

var errShort = errors.New("the connection ended early")

// copyBody writes an answer into its part until the part is complete (it may have been
// cut shorter meanwhile), the answer ends, or ctx is cancelled. It closes the answer.
func (d *FileDownload) copyBody(ctx context.Context, a *answer, s *seg, sizeKnown bool) error {
	defer a.close()
	bp := bufPool.Get().(*[]byte)
	defer bufPool.Put(bp)
	buf := *bp
	// A connection that stops bringing anything is closed (and opened again).
	idle := time.AfterFunc(idleTimeout, a.cancel)
	defer idle.Stop()
	body := a.resp.Body
	s.from.Store(s.done.Load())
	s.since.Store(time.Now().UnixNano())
	defer s.since.Store(0)
	for {
		pos := s.start + s.done.Load()
		left := s.end.Load() - pos
		if left <= 0 {
			if a.end >= 0 && pos == a.end {
				// The answer is used up: reading its end lets the connection serve again.
				_, _ = body.Read(buf[:1])
			}
			return nil
		}
		n, err := body.Read(buf[:min(int64(len(buf)), left)])
		if n > 0 {
			idle.Reset(idleTimeout)
			if werr := d.put(s, buf[:n]); werr != nil {
				d.fail(werr)
				return werr
			}
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err == io.EOF {
				if !sizeKnown || s.remaining() <= 0 {
					return nil
				}
				return &DownloadError{Code: "network", Err: errShort}
			}
			return netError(err)
		}
	}
}

// runSingle downloads over one connection: the server does not give parts (or does not
// say the size). A pause then means starting over next time.
func (d *FileDownload) runSingle(ctx context.Context, first *answer) error {
	size := d.st.Size
	s := &seg{start: 0, owner: 0}
	if size >= 0 {
		s.end.Store(size)
	} else {
		s.end.Store(math.MaxInt64 / 2)
	}
	d.mu.Lock()
	d.segs = []*seg{s}
	d.mu.Unlock()
	stop := d.watch(ctx)
	defer stop()
	d.active.Add(1)
	err := d.copyBody(ctx, first, s, size >= 0)
	d.active.Add(-1)
	if err != nil {
		return err
	}
	if size < 0 {
		dn := s.done.Load()
		d.mu.Lock()
		d.st.Size = dn
		s.end.Store(dn)
		d.mu.Unlock()
	}
	return nil
}

// runParts downloads the parts over several connections.
func (d *FileDownload) runParts(ctx context.Context, first *answer) error {
	size := d.st.Size
	d.mu.Lock()
	if len(d.st.Segments) > 0 {
		for _, p := range d.st.Segments {
			s := &seg{start: p.Start, owner: -1}
			s.end.Store(p.End)
			s.done.Store(p.Done)
			d.segs = append(d.segs, s)
		}
	} else {
		// One part per connection, none smaller than minSegment.
		n := max(1, min(int64(d.Conns), size/minSegment))
		step := size / n
		for i := int64(0); i < n; i++ {
			s := &seg{start: i * step, owner: -1}
			if i == n-1 {
				s.end.Store(size)
			} else {
				s.end.Store((i + 1) * step)
			}
			d.segs = append(d.segs, s)
		}
	}
	d.mu.Unlock()
	if d.complete() {
		return nil
	}
	stop := d.watch(ctx)
	var wg sync.WaitGroup
	// The first answer starts at the first missing byte: it serves the part holding it.
	s0 := d.claimAt(first.start, 0)
	wg.Add(1)
	if s0 == nil {
		first.close()
		go d.worker(ctx, &wg, 0, nil, nil)
	} else {
		go d.worker(ctx, &wg, 0, s0, first)
	}
	for i := 1; i < d.Conns; i++ {
		// Not all at once: a polite start.
		if !sleepCtx(ctx, connSpacing) || d.complete() {
			break
		}
		wg.Add(1)
		go d.worker(ctx, &wg, i, nil, nil)
	}
	wg.Wait()
	stop()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	d.mu.Lock()
	fatal, refusal, netErr := d.fatal, d.refusal, d.netErr
	d.mu.Unlock()
	if fatal != nil {
		return fatal
	}
	if !d.complete() {
		if refusal != nil {
			return refusal
		}
		if netErr != nil {
			return netErr
		}
		return dlErr("network", 0, "parts are missing")
	}
	return nil
}

func (d *FileDownload) complete() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, s := range d.segs {
		if s.remaining() > 0 {
			return false
		}
	}
	return true
}

// claimAt gives connection id the part that continues at byte pos.
func (d *FileDownload) claimAt(pos int64, id int) *seg {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, s := range d.segs {
		if s.owner < 0 && s.remaining() > 0 && s.start+s.done.Load() == pos {
			s.owner = id
			return s
		}
	}
	return nil
}

// claim gives connection id something to do: a part nobody downloads (the earliest), or
// else the second half of the running part that will take longest. With nothing worth
// doing, later says whether a running part may become worth cutting.
func (d *FileDownload) claim(id int) (s *seg, later bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var free *seg
	for _, s := range d.segs {
		if s.owner < 0 && s.remaining() > 0 && (free == nil || s.start < free.start) {
			free = s
		}
	}
	if free != nil {
		free.owner = id
		return free, false
	}
	// The running part that will take longest: worth cutting if it needs more than a
	// second more (or, while its speed is not known, if it is big).
	now := time.Now().UnixNano()
	var big *seg
	bigScore := 0.0
	for _, s := range d.segs {
		left := s.remaining()
		if s.owner < 0 || left < 2*minPiece {
			continue
		}
		later = true
		score, known := s.timeLeft(now)
		if !known {
			if left < 2*minSplit {
				continue
			}
			score = float64(left) / float64(1<<20) // as if 1 MB/s
		} else if score < 1 {
			continue
		}
		if score > bigScore {
			big, bigScore = s, score
		}
	}
	if big == nil {
		return nil, later
	}
	big.mu.Lock()
	pos, end := big.start+big.done.Load(), big.end.Load()
	mid := pos + (end-pos)/2
	mid -= mid % 4096 // whole blocks
	if end-pos < 2*minPiece || mid <= pos {
		big.mu.Unlock()
		return nil, later
	}
	big.end.Store(mid)
	big.mu.Unlock()
	ns := &seg{start: mid, owner: id}
	ns.end.Store(end)
	d.segs = append(d.segs, ns)
	return ns, false
}

func (d *FileDownload) release(s *seg) {
	d.mu.Lock()
	s.owner = -1
	d.mu.Unlock()
}

func (d *FileDownload) note(err error, refusal bool) {
	d.mu.Lock()
	if refusal {
		d.refusal = err
	} else {
		d.netErr = err
	}
	d.mu.Unlock()
}

// worker is one connection: it downloads parts until none is left.
func (d *FileDownload) worker(ctx context.Context, wg *sync.WaitGroup, id int, s *seg, a *answer) {
	defer wg.Done()
	defer func() {
		if a != nil {
			a.close()
		}
		if s != nil {
			d.release(s)
		}
	}()
	fails := 0
	for ctx.Err() == nil {
		if s == nil {
			var later bool
			if s, later = d.claim(id); s == nil {
				// Wait while a running part may still need help.
				if !later || !sleepCtx(ctx, 200*time.Millisecond) {
					return
				}
				continue
			}
		}
		if a == nil {
			var err error
			a, err = d.requestPart(ctx, s)
			if err != nil {
				a = nil
				if ctx.Err() != nil {
					return
				}
				switch errCode(err) {
				case "changed":
					d.fail(err)
					return
				case "forbidden", "gone", "noparts", "page_not_file", "badurl", "tls", "unknown":
					// This connection was refused (servers limit how many, links expire):
					// the others go on, and take its part.
					d.note(err, true)
					return
				case "server":
					fails++
					d.release(s)
					s = nil
					if fails >= 3 && d.active.Load() > 0 {
						return // the server wants fewer connections
					}
				default:
					fails++
					d.note(err, false)
				}
				if !sleepCtx(ctx, backoff(fails)) {
					return
				}
				continue
			}
			if a == nil { // the part was finished meanwhile
				d.release(s)
				s = nil
				continue
			}
		}
		d.active.Add(1)
		before := s.done.Load()
		err := d.copyBody(ctx, a, s, true)
		d.active.Add(-1)
		a = nil
		if err == nil {
			d.release(s)
			s = nil
			fails = 0
			continue
		}
		if ctx.Err() != nil {
			return
		}
		if c := errCode(err); c == "disk" || c == "permission" {
			return
		}
		if s.done.Load() > before {
			fails = 0 // it broke off after bringing something (servers cut long answers): ask for the rest at once
			continue
		}
		fails++
		d.note(err, false)
		if !sleepCtx(ctx, backoff(min(fails, 4))) {
			return
		}
	}
}

// requestPart asks for the rest of part s. (nil, nil): nothing is left of it.
func (d *FileDownload) requestPart(ctx context.Context, s *seg) (*answer, error) {
	from, end := s.start+s.done.Load(), s.end.Load()
	if from >= end {
		return nil, nil
	}
	d.mu.Lock()
	target, tag, lastMod, size := d.st.FinalURL, d.st.ETag, d.st.LastMod, d.st.Size
	d.mu.Unlock()
	if target == "" {
		target = d.Src.URL
	}
	h := http.Header{}
	h.Set("Range", "bytes="+strconv.FormatInt(from, 10)+"-"+strconv.FormatInt(end-1, 10))
	if tag != "" {
		h.Set("If-Range", tag)
	} else if lastMod != "" {
		h.Set("If-Range", lastMod)
	}
	rctx, cancel := context.WithCancel(ctx)
	resp, err := d.f.get(rctx, target, h)
	if err != nil {
		cancel()
		return nil, err
	}
	switch resp.StatusCode {
	case http.StatusPartialContent:
		start, last, total, ok := parseContentRange(resp.Header.Get("Content-Range"))
		etag, lm := strongTag(resp.Header.Get("ETag")), resp.Header.Get("Last-Modified")
		if (ok && total >= 0 && total != size) || (tag != "" && etag != "" && etag != tag) ||
			(tag == "" && lastMod != "" && lm != "" && lm != lastMod) {
			resp.Body.Close()
			cancel()
			return nil, dlErr("changed", resp.StatusCode, "the file changed on the server")
		}
		if !ok || start != from {
			resp.Body.Close()
			cancel()
			return nil, dlErr("noparts", resp.StatusCode, "a part that was not asked for: %q", resp.Header.Get("Content-Range"))
		}
		d.ranged.Store(true)
		if f := resp.Request.URL.String(); f != target {
			d.mu.Lock()
			d.st.FinalURL = f
			d.mu.Unlock()
		}
		return &answer{resp: resp, cancel: cancel, start: from, end: last + 1}, nil
	case http.StatusOK:
		if d.ranged.Load() {
			// Parts worked before, so the whole file instead of a part means it changed.
			resp.Body.Close()
			cancel()
			return nil, dlErr("changed", resp.StatusCode, "the file changed on the server")
		}
		if from == 0 && resp.ContentLength == size {
			return &answer{resp: resp, cancel: cancel, start: 0, end: size}, nil
		}
		resp.Body.Close()
		cancel()
		// The server said it gives parts, but it does not: one connection, and no pausing.
		d.mu.Lock()
		d.st.Resumable = false
		d.mu.Unlock()
		return nil, dlErr("noparts", resp.StatusCode, "the server sent the whole file instead of a part")
	default:
		drainClose(resp)
		cancel()
		e := statusError(resp.StatusCode)
		if (e.Code == "forbidden" || e.Code == "gone") && target != d.Src.URL {
			// The address the server sent us to may have expired: the original gives a new one.
			d.mu.Lock()
			if d.st.FinalURL == target {
				d.st.FinalURL = ""
			}
			d.mu.Unlock()
			return nil, dlErr("network", resp.StatusCode, "the forwarded address expired")
		}
		return nil, e
	}
}

// ---------------------------------------------------------------------------
// Asking about a link (for the "new download" window)
// ---------------------------------------------------------------------------

// ProbeFile asks the server about a link without downloading it.
func ProbeFile(ctx context.Context, src FileSource, wantName string) (FileInfo, error) {
	d := &FileDownload{Src: src, Name: wantName, f: newFetcher(src), st: freshState(src.URL),
		stopCh: make(chan struct{}), stop: new(sync.Once)}
	a, err := d.firstRequest(ctx)
	if err != nil {
		return FileInfo{}, err
	}
	a.close()
	return d.Info(), nil
}
