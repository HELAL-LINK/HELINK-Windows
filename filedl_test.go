package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fileServer serves one file the way different real servers do.
type fileServer struct {
	mu          sync.Mutex
	data        []byte
	etag        string
	ranges      bool  // gives parts
	advertise   bool  // says Accept-Ranges even when it does not give parts
	rate        int   // bytes per second per connection (0: as fast as possible)
	slowFirst   int   // bytes per second for answers starting at byte 0 (0: rate)
	capRange    int64 // most bytes in one answer (0: no limit)
	dropAfter   int64 // cut the connection after this many bytes of an answer (0: never)
	maxConns    int   // answers at once; more get limitCode
	limitCode   int
	noLength    bool // no Content-Length
	contentType string
	disposition string
	check       func(r *http.Request) int // a status to refuse with, or 0
	hang        bool                      // send the headers, then nothing

	active, maxActive, requests int
	served                      atomic.Int64
	ranges206                   int
}

func newFileServer(size int) *fileServer {
	b := make([]byte, size)
	_, _ = rand.Read(b)
	return &fileServer{data: b, etag: `"v1"`, ranges: true, limitCode: 503}
}

// set changes the server's behaviour while it serves.
func (s *fileServer) set(f func()) {
	s.mu.Lock()
	f()
	s.mu.Unlock()
}

func (s *fileServer) setData(b []byte, etag string) {
	s.mu.Lock()
	s.data, s.etag = b, etag
	s.mu.Unlock()
}

func (s *fileServer) stats() (requests, maxActive int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests, s.maxActive
}

func (s *fileServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests++
	s.active++
	if s.active > s.maxActive {
		s.maxActive = s.active
	}
	act := s.active
	data, etag, check, rate, slowFirst := s.data, s.etag, s.check, s.rate, s.slowFirst
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.active--
		s.mu.Unlock()
	}()
	if s.maxConns > 0 && act > s.maxConns {
		w.WriteHeader(s.limitCode)
		return
	}
	if check != nil {
		if code := check(r); code != 0 {
			w.WriteHeader(code)
			return
		}
	}
	h := w.Header()
	if s.contentType != "" {
		h.Set("Content-Type", s.contentType)
	} else {
		h.Set("Content-Type", "application/octet-stream")
	}
	if s.disposition != "" {
		h.Set("Content-Disposition", s.disposition)
	}
	if etag != "" {
		h.Set("ETag", etag)
	}
	n := int64(len(data))
	start, end, status := int64(0), n-1, 200
	if s.ranges || s.advertise {
		h.Set("Accept-Ranges", "bytes")
	}
	if rg := r.Header.Get("Range"); s.ranges && rg != "" {
		ir := r.Header.Get("If-Range")
		if ir == "" || ir == etag {
			spec := strings.TrimPrefix(rg, "bytes=")
			a, b, _ := strings.Cut(spec, "-")
			ai, err := strconv.ParseInt(a, 10, 64)
			if err != nil || ai >= n {
				h.Set("Content-Range", fmt.Sprintf("bytes */%d", n))
				w.WriteHeader(416)
				return
			}
			start = ai
			if b != "" {
				if bi, err := strconv.ParseInt(b, 10, 64); err == nil && bi < end {
					end = bi
				}
			}
			if s.capRange > 0 && end-start+1 > s.capRange {
				end = start + s.capRange - 1
			}
			h.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, n))
			status = 206
			s.mu.Lock()
			s.ranges206++
			s.mu.Unlock()
		}
	}
	if !s.noLength {
		h.Set("Content-Length", strconv.FormatInt(end-start+1, 10))
	}
	w.WriteHeader(status)
	if s.hang {
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		return
	}
	if start == 0 && slowFirst > 0 {
		rate = slowFirst
	}
	const chunk = 16 << 10
	sent := int64(0)
	t0 := time.Now()
	for p := start; p <= end; {
		q := min(p+chunk, end+1)
		if s.dropAfter > 0 && sent+(q-p) > s.dropAfter {
			q = p + (s.dropAfter - sent)
			if q > p {
				_, _ = w.Write(data[p:q])
				s.served.Add(q - p)
			}
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler) // cut the connection
		}
		if _, err := w.Write(data[p:q]); err != nil {
			return
		}
		s.served.Add(q - p)
		sent += q - p
		p = q
		if rate > 0 {
			w.(http.Flusher).Flush()
			if wait := time.Duration(float64(sent)/float64(rate)*float64(time.Second)) - time.Since(t0); wait > 0 {
				select {
				case <-time.After(wait):
				case <-r.Context().Done():
					return
				}
			}
		}
	}
}

// fastWaits makes the download's waits short for the tests.
func fastWaits(t *testing.T) {
	old := []time.Duration{idleTimeout, stallTimeout, stateInterval, retryBase, connSpacing, progressEvery}
	idleTimeout, stallTimeout, stateInterval, retryBase, connSpacing, progressEvery = 2*time.Second, 6*time.Second, 100*time.Millisecond, 20*time.Millisecond, 5*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() {
		idleTimeout, stallTimeout, stateInterval, retryBase, connSpacing, progressEvery = old[0], old[1], old[2], old[3], old[4], old[5]
	})
}

func runFile(t *testing.T, d *FileDownload, timeout time.Duration) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return d.Run(ctx)
}

func checkFile(t *testing.T, p string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("file content differs: %d bytes, want %d", len(got), len(want))
	}
}

func TestFileInParts(t *testing.T) {
	fastWaits(t)
	fs := newFileServer(6<<20 + 12345)
	fs.set(func() { fs.rate = 3 << 20 })
	srv := httptest.NewServer(fs)
	defer srv.Close()
	var maxConns atomic.Int32
	var infos []FileInfo
	d := &FileDownload{Src: FileSource{URL: srv.URL + "/files/big%20file.bin?x=1"}, Dir: t.TempDir(), Conns: 8,
		OnInfo: func(i FileInfo) { infos = append(infos, i) },
		OnProgress: func(p FileProgress) {
			if int32(p.Conns) > maxConns.Load() {
				maxConns.Store(int32(p.Conns))
			}
			if p.Done > p.Total || p.Total != int64(len(fs.data)) {
				t.Errorf("progress %d of %d", p.Done, p.Total)
			}
		}}
	start := time.Now()
	p, err := runFile(t, d, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	checkFile(t, p, fs.data)
	_, maxActive := fs.stats()
	if maxActive < 4 || maxConns.Load() < 3 {
		t.Fatalf("connections: server saw %d at once, progress %d", maxActive, maxConns.Load())
	}
	if len(infos) != 1 || infos[0].Name != "big file.bin" || !infos[0].Resumable || infos[0].Size != int64(len(fs.data)) {
		t.Fatalf("info %+v", infos)
	}
	// One connection at 3 MB/s would need more than 2 s.
	if el := time.Since(start); el > 1800*time.Millisecond {
		t.Fatalf("took %s", el)
	}
	if served := fs.served.Load(); served > int64(len(fs.data))*11/10 {
		t.Fatalf("served %d bytes for %d", served, len(fs.data))
	}
}

func TestFileWithoutRanges(t *testing.T) {
	fastWaits(t)
	for _, advertise := range []bool{false, true} {
		fs := newFileServer(3<<20 + 7)
		fs.ranges, fs.advertise = false, advertise
		srv := httptest.NewServer(fs)
		var info FileInfo
		d := &FileDownload{Src: FileSource{URL: srv.URL + "/a.zip"}, Dir: t.TempDir(), Conns: 8, OnInfo: func(i FileInfo) { info = i }}
		p, err := runFile(t, d, 30*time.Second)
		srv.Close()
		if err != nil {
			t.Fatal(err)
		}
		checkFile(t, p, fs.data)
		req, maxActive := fs.stats()
		want := 1
		if advertise {
			want = 2 // the answer said it gives parts: one more question to be sure
		}
		if req != want || maxActive > 2 || d.Info().Resumable {
			t.Fatalf("advertise %v: %d requests, %d at once, info %+v / %+v", advertise, req, maxActive, info, d.Info())
		}
	}
}

func TestFileRedirectsCookiesHeaders(t *testing.T) {
	fastWaits(t)
	fs := newFileServer(2<<20 + 99)
	fs.disposition = `attachment; filename="fallback.bin"; filename*=UTF-8''%D9%85%D9%84%D9%81%20%D8%B9%D8%B1%D8%A8%D9%8A.pdf`
	var seen sync.Map
	fs.check = func(r *http.Request) int {
		c := r.Header.Get("Cookie")
		seen.Store(c, true)
		if r.Header.Get("User-Agent") != "TestBrowser/1.0" || r.Header.Get("Referer") != "https://site.example/page" {
			return 403
		}
		if !strings.Contains(c, "auth=x y\"z") || !strings.Contains(c, "sess=s1") || strings.Contains(c, "other=") || strings.Contains(c, "deep=") {
			return 403
		}
		return 0
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/mid", http.StatusFound)
	})
	mux.HandleFunc("/mid", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "sess", Value: "s1", Path: "/"})
		http.Redirect(w, r, "/dl/file", http.StatusTemporaryRedirect)
	})
	mux.Handle("/dl/", fs)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	host := strings.Split(strings.TrimPrefix(srv.URL, "http://"), ":")[0]
	src := FileSource{URL: srv.URL + "/start", UserAgent: "TestBrowser/1.0", Referer: "https://site.example/page",
		Cookies: []Cookie{
			{Name: "auth", Value: `x y"z`, Domain: host, HostOnly: true, Path: "/"},
			{Name: "other", Value: "1", Domain: ".example.com", Path: "/"},
			{Name: "deep", Value: "1", Domain: host, Path: "/elsewhere"},
			{Name: "old", Value: "1", Domain: host, Expires: 1000},
			{Name: "sec", Value: "1", Domain: host, Secure: true},
		}}
	info, err := ProbeFile(context.Background(), src, "")
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "ملف عربي.pdf" || info.Size != int64(len(fs.data)) || !info.Resumable || !strings.HasSuffix(info.FinalURL, "/dl/file") {
		t.Fatalf("probe %+v", info)
	}
	d := &FileDownload{Src: src, Dir: t.TempDir(), Conns: 4}
	p, err := runFile(t, d, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	checkFile(t, p, fs.data)
	seen.Range(func(k, _ any) bool {
		if c := k.(string); strings.Contains(c, "old=") || strings.Contains(c, "sec=") {
			t.Errorf("sent a cookie it should not: %q", c)
		}
		return true
	})
	// The browser's name wins over the server's.
	d2 := &FileDownload{Src: src, Dir: t.TempDir(), Name: "my:name?.pdf", Conns: 2}
	if _, err := runFile(t, d2, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if n := d2.Info().Name; n != "my - name.pdf" {
		t.Fatalf("name %q", n)
	}
}

func TestFileNames(t *testing.T) {
	cases := []struct{ disp, want string }{
		{`attachment; filename="report 2024.pdf"`, "report 2024.pdf"},
		{`attachment; filename=plain.zip`, "plain.zip"},
		{`attachment; filename*=UTF-8''%E2%82%AC%20rates.xlsx`, "€ rates.xlsx"},
		{`attachment; filename="a.txt"; filename*=utf-8''b%C3%A4.txt`, "bä.txt"},
		{`inline; filename="=?UTF-8?B?2YXZhNmBLmRvY3g=?="`, "ملف.docx"},
		{`attachment; filename="..\..\evil.exe"`, "evil.exe"},
		{`attachment;filename=no%20quotes.rar;`, "no quotes.rar"},
		{"attachment; filename=\"caf\xe9.txt\"", "café.txt"},
		{`attachment`, ""},
		{``, ""},
	}
	for _, c := range cases {
		if got := nameFromDisposition(c.disp); got != c.want {
			t.Errorf("%q: got %q, want %q", c.disp, got, c.want)
		}
	}
	clean := []struct{ name, ct, want string }{
		{"video", "video/mp4", "video.mp4"},
		{"setup.EXE", "", "setup.EXE"},
		{"con.txt", "", "_con.txt"},
		{"a<b>c|d.zip", "", "a b c - d.zip"},
		{"", "application/pdf", "download.pdf"},
		{"...", "", "download"},
		{"archive.tar.gz", "application/gzip", "archive.tar.gz"},
		{"name (1)", "text/plain; charset=utf-8", "name (1).txt"},
	}
	for _, c := range clean {
		if got := cleanFileName(c.name, c.ct); got != c.want {
			t.Errorf("clean %q %q: got %q, want %q", c.name, c.ct, got, c.want)
		}
	}
	if n := nameFromURL("https://x.example/dir/My%20File.iso?token=1#frag"); n != "My File.iso" {
		t.Errorf("url name %q", n)
	}
	if n := nameFromURL("https://x.example/"); n != "" {
		t.Errorf("url name %q", n)
	}
}

func TestParseContentRange(t *testing.T) {
	cases := []struct {
		in          string
		s, l, total int64
		ok          bool
	}{
		{"bytes 0-99/1000", 0, 99, 1000, true},
		{"bytes 100-199/*", 100, 199, -1, true},
		{"Bytes 5-5/6", 5, 5, 6, true},
		{"bytes */1000", 0, 0, 0, false},
		{"bytes 10-5/100", 0, 0, 0, false},
		{"bytes 0-99/50", 0, 0, 0, false},
		{"items 0-1/2", 0, 0, 0, false},
		{"", 0, 0, 0, false},
	}
	for _, c := range cases {
		s, l, total, ok := parseContentRange(c.in)
		if ok != c.ok || (ok && (s != c.s || l != c.l || total != c.total)) {
			t.Errorf("%q: %d %d %d %v", c.in, s, l, total, ok)
		}
	}
}

// pauseWhen runs the download until cond says so, then pauses it.
func pauseWhen(t *testing.T, d *FileDownload, cond func(FileProgress) bool) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var once sync.Once
	d.OnProgress = func(p FileProgress) {
		if cond(p) {
			once.Do(cancel)
		}
	}
	_, err := d.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("paused download ended with %v", err)
	}
	d.OnProgress = nil
}

func TestFilePauseAndContinue(t *testing.T) {
	fastWaits(t)
	fs := newFileServer(8<<20 + 3)
	fs.set(func() { fs.rate = 2 << 20 })
	srv := httptest.NewServer(fs)
	defer srv.Close()
	dir := t.TempDir()
	src := FileSource{URL: srv.URL + "/f.bin"}
	pauseWhen(t, &FileDownload{Src: src, Dir: dir, Conns: 4}, func(p FileProgress) bool { return p.Done > 3<<20 })
	done, size, ok := StateDone(dir)
	if !ok || done < 1<<20 || size != int64(len(fs.data)) {
		t.Fatalf("saved state: %d of %d, %v", done, size, ok)
	}
	time.Sleep(300 * time.Millisecond) // the server stops sending what nobody reads any more
	before := fs.served.Load()
	// A new download, as after closing the app.
	d := &FileDownload{Src: src, Dir: dir, Conns: 4}
	p, err := runFile(t, d, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	checkFile(t, p, fs.data)
	if again := fs.served.Load() - before; again > int64(len(fs.data))-done+(1<<20) {
		t.Fatalf("continued with %d bytes; %d were done", again, done)
	}
}

func TestFileChangedBetweenSessions(t *testing.T) {
	fastWaits(t)
	for _, sameSize := range []bool{true, false} {
		fs := newFileServer(6 << 20)
		fs.set(func() { fs.rate = 2 << 20 })
		srv := httptest.NewServer(fs)
		dir := t.TempDir()
		src := FileSource{URL: srv.URL + "/f.bin"}
		pauseWhen(t, &FileDownload{Src: src, Dir: dir, Conns: 4}, func(p FileProgress) bool { return p.Done > 2<<20 })
		n := len(fs.data)
		if !sameSize {
			n += 1000
		}
		nb := make([]byte, n)
		_, _ = rand.Read(nb)
		fs.setData(nb, `"v2"`)
		fs.set(func() { fs.rate = 0 })
		p, err := runFile(t, &FileDownload{Src: src, Dir: dir, Conns: 4}, 30*time.Second)
		srv.Close()
		if err != nil {
			t.Fatal(err)
		}
		checkFile(t, p, nb)
	}
}

func TestFileChangedWhileDownloading(t *testing.T) {
	fastWaits(t)
	fs := newFileServer(6 << 20)
	fs.set(func() { fs.rate = 2 << 20 })
	srv := httptest.NewServer(fs)
	defer srv.Close()
	nb := make([]byte, len(fs.data))
	_, _ = rand.Read(nb)
	old := fs.data
	var once sync.Once
	d := &FileDownload{Src: FileSource{URL: srv.URL + "/f.bin"}, Dir: t.TempDir(), Conns: 4,
		OnProgress: func(p FileProgress) {
			if p.Done > 1<<20 {
				once.Do(func() { fs.setData(nb, `"v2"`) })
			}
		}}
	p, err := runFile(t, d, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	// Either version, whole: never a mix of the two.
	got, _ := os.ReadFile(p)
	if !bytes.Equal(got, nb) && !bytes.Equal(got, old) {
		t.Fatal("the file mixes the old and the new version")
	}
}

func TestFileRefusals(t *testing.T) {
	fastWaits(t)
	page := newFileServer(5000)
	page.contentType = "text/html; charset=utf-8"
	srv := httptest.NewServer(page)
	defer srv.Close()
	_, err := runFile(t, &FileDownload{Src: FileSource{URL: srv.URL + "/download?id=5"}, Dir: t.TempDir()}, 10*time.Second)
	if errCode(err) != "page_not_file" {
		t.Fatalf("html: %v", err)
	}
	// An .html file asked for by name is fine.
	if _, err := runFile(t, &FileDownload{Src: FileSource{URL: srv.URL + "/saved.html"}, Dir: t.TempDir()}, 10*time.Second); err != nil {
		t.Fatalf("html file: %v", err)
	}
	for code, want := range map[int]string{403: "forbidden", 401: "forbidden", 404: "gone", 410: "gone"} {
		fs := newFileServer(100)
		fs.check = func(*http.Request) int { return code }
		s := httptest.NewServer(fs)
		_, err := runFile(t, &FileDownload{Src: FileSource{URL: s.URL + "/x"}, Dir: t.TempDir()}, 10*time.Second)
		s.Close()
		if errCode(err) != want {
			t.Errorf("%d: %v", code, err)
		}
	}
	_, err = runFile(t, &FileDownload{Src: FileSource{URL: "ftp://example.com/x"}, Dir: t.TempDir()}, 10*time.Second)
	if errCode(err) != "badurl" {
		t.Fatalf("ftp: %v", err)
	}
	// Nobody listening.
	l := httptest.NewServer(http.NotFoundHandler())
	addr := l.URL
	l.Close()
	start := time.Now()
	_, err = runFile(t, &FileDownload{Src: FileSource{URL: addr + "/x"}, Dir: t.TempDir()}, 10*time.Second)
	if errCode(err) != "network" || time.Since(start) > 5*time.Second {
		t.Fatalf("no server: %v after %s", err, time.Since(start))
	}
}

func TestFileBrokenConnections(t *testing.T) {
	fastWaits(t)
	fs := newFileServer(5<<20 + 77)
	fs.dropAfter = 300 << 10
	srv := httptest.NewServer(fs)
	defer srv.Close()
	p, err := runFile(t, &FileDownload{Src: FileSource{URL: srv.URL + "/f"}, Dir: t.TempDir(), Conns: 6}, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	checkFile(t, p, fs.data)
}

func TestFileShortAnswers(t *testing.T) {
	fastWaits(t)
	fs := newFileServer(4<<20 + 5)
	fs.capRange = 200 << 10
	srv := httptest.NewServer(fs)
	defer srv.Close()
	start := time.Now()
	p, err := runFile(t, &FileDownload{Src: FileSource{URL: srv.URL + "/f"}, Dir: t.TempDir(), Conns: 4}, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	checkFile(t, p, fs.data)
	if el := time.Since(start); el > 3*time.Second {
		t.Fatalf("took %s", el)
	}
}

func TestFileUnknownSizeAndEmpty(t *testing.T) {
	fastWaits(t)
	fs := newFileServer(1<<20 + 3)
	fs.ranges, fs.noLength = false, true
	srv := httptest.NewServer(fs)
	d := &FileDownload{Src: FileSource{URL: srv.URL + "/stream.ts"}, Dir: t.TempDir(), Conns: 8}
	p, err := runFile(t, d, 30*time.Second)
	srv.Close()
	if err != nil {
		t.Fatal(err)
	}
	checkFile(t, p, fs.data)
	if d.Info().Size != int64(len(fs.data)) {
		t.Fatalf("size %d", d.Info().Size)
	}
	for _, ranges := range []bool{true, false} {
		empty := newFileServer(0)
		empty.ranges = ranges
		s := httptest.NewServer(empty)
		p, err := runFile(t, &FileDownload{Src: FileSource{URL: s.URL + "/empty.txt"}, Dir: t.TempDir()}, 10*time.Second)
		s.Close()
		if err != nil {
			t.Fatalf("empty (ranges %v): %v", ranges, err)
		}
		checkFile(t, p, nil)
	}
}

func TestFileConnectionLimits(t *testing.T) {
	fastWaits(t)
	for _, code := range []int{503, 429, 403} {
		fs := newFileServer(3<<20 + 1)
		fs.set(func() { fs.rate = 4 << 20 })
		fs.maxConns, fs.limitCode = 2, code
		srv := httptest.NewServer(fs)
		p, err := runFile(t, &FileDownload{Src: FileSource{URL: srv.URL + "/f"}, Dir: t.TempDir(), Conns: 8}, 30*time.Second)
		srv.Close()
		if err != nil {
			t.Fatalf("limit %d: %v", code, err)
		}
		checkFile(t, p, fs.data)
	}
}

func TestFileSplitsSlowParts(t *testing.T) {
	fastWaits(t)
	fs := newFileServer(4 << 20)
	fs.rate, fs.slowFirst = 8<<20, 150<<10 // the connection from byte 0 is very slow
	srv := httptest.NewServer(fs)
	defer srv.Close()
	start := time.Now()
	p, err := runFile(t, &FileDownload{Src: FileSource{URL: srv.URL + "/f"}, Dir: t.TempDir(), Conns: 4}, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	checkFile(t, p, fs.data)
	// Alone, the slow connection would need 7 s for its first megabyte.
	req, _ := fs.stats()
	if el := time.Since(start); el > 4*time.Second || req <= 5 {
		t.Fatalf("took %s with %d requests", el, req)
	}
}

func TestFileExpiringAddresses(t *testing.T) {
	fastWaits(t)
	fs := newFileServer(6 << 20)
	fs.set(func() { fs.rate = 2 << 20 })
	var token atomic.Int64
	fs.check = func(r *http.Request) int {
		if r.URL.Query().Get("t") != strconv.FormatInt(token.Load(), 10) {
			return 403 // only the newest address works
		}
		return 0
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/get", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/signed?t="+strconv.FormatInt(token.Add(1), 10), http.StatusFound)
	})
	mux.Handle("/signed", fs)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	dir := t.TempDir()
	src := FileSource{URL: srv.URL + "/get"}
	pauseWhen(t, &FileDownload{Src: src, Dir: dir, Conns: 3}, func(p FileProgress) bool { return p.Done > 2<<20 })
	// The saved state has an address that no longer works.
	token.Add(1)
	// Expire the address once more while it downloads.
	var once sync.Once
	d := &FileDownload{Src: src, Dir: dir, Conns: 3, OnProgress: func(p FileProgress) {
		if p.Done > 4<<20 {
			once.Do(func() { token.Add(1) })
		}
	}}
	p, err := runFile(t, d, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	checkFile(t, p, fs.data)
}

func TestFileNewAddressContinues(t *testing.T) {
	fastWaits(t)
	fs := newFileServer(6 << 20)
	fs.set(func() { fs.rate = 2 << 20 })
	srv := httptest.NewServer(fs)
	defer srv.Close()
	dir := t.TempDir()
	pauseWhen(t, &FileDownload{Src: FileSource{URL: srv.URL + "/old-link"}, Dir: dir, Conns: 4}, func(p FileProgress) bool { return p.Done > 3<<20 })
	done, _, _ := StateDone(dir)
	fs.setData(fs.data, `"another-server-tag"`)
	time.Sleep(300 * time.Millisecond) // the server stops sending what nobody reads any more
	before := fs.served.Load()
	fs.set(func() { fs.rate = 0 })
	p, err := runFile(t, &FileDownload{Src: FileSource{URL: srv.URL + "/new-link"}, Dir: dir, Conns: 4}, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	checkFile(t, p, fs.data)
	if again := fs.served.Load() - before; again > int64(len(fs.data))-done+(1<<20) {
		t.Fatalf("started over: %d bytes again, %d were done", again, done)
	}
}

func TestFileStalls(t *testing.T) {
	fastWaits(t)
	idleTimeout, stallTimeout = 300*time.Millisecond, 1500*time.Millisecond
	fs := newFileServer(3 << 20)
	fs.hang = true
	srv := httptest.NewServer(fs)
	defer srv.Close()
	start := time.Now()
	_, err := runFile(t, &FileDownload{Src: FileSource{URL: srv.URL + "/f"}, Dir: t.TempDir(), Conns: 4}, 20*time.Second)
	if errCode(err) != "network" || time.Since(start) > 6*time.Second {
		t.Fatalf("%v after %s", err, time.Since(start))
	}
}

func TestFileStateFile(t *testing.T) {
	dir := t.TempDir()
	st := &dlState{Version: 1, URL: "u", Size: 100, Resumable: true, Segments: []segState{{0, 40, 40}, {40, 100, 10}}}
	if err := saveState(dir, st); err != nil {
		t.Fatal(err)
	}
	if done, size, ok := StateDone(dir); !ok || done != 50 || size != 100 {
		t.Fatalf("%d %d %v", done, size, ok)
	}
	for _, bad := range [][]segState{{{0, 40, 40}, {50, 100, 0}}, {{0, 40, 41}, {40, 100, 0}}, {{0, 90, 0}}, {{0, 60, 0}, {40, 100, 0}}} {
		st.Segments = bad
		_ = saveState(dir, st)
		if _, ok := loadState(dir); ok {
			t.Errorf("accepted %v", bad)
		}
	}
	_ = os.WriteFile(filepath.Join(dir, stateFileName), []byte("{broken"), 0o644)
	if _, ok := loadState(dir); ok {
		t.Error("accepted broken json")
	}
}

func TestCookieMatching(t *testing.T) {
	now := time.Now()
	cases := []struct {
		c    Cookie
		url  string
		want bool
	}{
		{Cookie{Name: "a", Value: "1", Domain: ".example.com"}, "https://www.example.com/x", true},
		{Cookie{Name: "a", Value: "1", Domain: "example.com"}, "https://example.com/", true},
		{Cookie{Name: "a", Value: "1", Domain: "example.com", HostOnly: true}, "https://www.example.com/", false},
		{Cookie{Name: "a", Value: "1", Domain: ".example.com"}, "https://badexample.com/", false},
		{Cookie{Name: "a", Value: "1", Domain: "example.com", Path: "/docs"}, "https://example.com/docs/a", true},
		{Cookie{Name: "a", Value: "1", Domain: "example.com", Path: "/docs"}, "https://example.com/docsx", false},
		{Cookie{Name: "a", Value: "1", Domain: "example.com", Secure: true}, "http://example.com/", false},
		{Cookie{Name: "a", Value: "1", Domain: "example.com", Expires: float64(now.Unix() - 10)}, "https://example.com/", false},
		{Cookie{Name: "a", Value: "1", Domain: "example.com", Expires: float64(now.Unix() + 1000)}, "https://example.com/", true},
		{Cookie{Name: "a b", Value: "1", Domain: "example.com"}, "https://example.com/", false},
	}
	for _, c := range cases {
		pu, _ := url.Parse(c.url)
		if got := c.c.matches(pu, now); got != c.want {
			t.Errorf("%+v on %s: %v", c.c, c.url, got)
		}
	}
}
