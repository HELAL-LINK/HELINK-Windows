package main

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const userAgent = "HELINK/1.3 (+Windows desktop app)"

// Download locations. yt-dlp's nightly channel is the one its authors recommend for
// regular users because YouTube changes often; stable is the fallback.
var (
	ytdlpNightlyLatest = "https://github.com/yt-dlp/yt-dlp-nightly-builds/releases/latest"
	ytdlpNightlyAsset  = "https://github.com/yt-dlp/yt-dlp-nightly-builds/releases/download/%s/yt-dlp_win.zip"
	ytdlpFallbackURLs  = []string{
		"https://github.com/yt-dlp/yt-dlp-nightly-builds/releases/latest/download/yt-dlp_win.zip",
		"https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp_win.zip",
	}
	denoLatestTxt   = "https://dl.deno.land/release-latest.txt"
	denoAssetGitHub = "https://github.com/denoland/deno/releases/download/%s/deno-x86_64-pc-windows-msvc.zip"
	denoAssetCDN    = "https://dl.deno.land/release/%s/deno-x86_64-pc-windows-msvc.zip"
	denoFallbackURL = "https://github.com/denoland/deno/releases/latest/download/deno-x86_64-pc-windows-msvc.zip"
	ffmpegURLs      = []string{
		"https://github.com/yt-dlp/FFmpeg-Builds/releases/download/latest/ffmpeg-master-latest-win64-gpl-shared.zip",
		"https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/ffmpeg-master-latest-win64-gpl-shared.zip",
	}
)

// Rough sizes, used for the progress bar until the real sizes are known.
const (
	estYtdlp  = 18 << 20
	estDeno   = 45 << 20
	estFfmpeg = 83 << 20
)

type toolID string

const (
	toolYtdlp  toolID = "ytdlp"
	toolDeno   toolID = "deno"
	toolFfmpeg toolID = "ffmpeg"
)

// SetupProgress is sent to the UI while the tools download.
type SetupProgress struct {
	Phase     string  `json:"phase"` // "download" | "extract" | "verify" | "done"
	Step      int     `json:"step"`  // 1-based
	Steps     int     `json:"steps"`
	Tool      string  `json:"tool"`
	Percent   float64 `json:"percent"` // overall
	DoneText  string  `json:"doneText"`
	TotalText string  `json:"totalText"`
}

// Tools manages the helper programs: yt-dlp, ffmpeg and Deno.
type Tools struct {
	root   string
	store  *Store
	client *http.Client

	mu      sync.Mutex
	running int  // yt-dlp processes currently running
	pending bool // a new yt-dlp is ready in yt-dlp.new, waiting for the swap

	installMu sync.Mutex // one install/update at a time
	devMode   bool       // use tools from PATH (development preview only)
}

func NewTools(root string, store *Store) *Tools {
	tr := &http.Transport{
		Proxy:                 systemProxy,
		TLSHandshakeTimeout:   25 * time.Second,
		ResponseHeaderTimeout: 40 * time.Second,
		IdleConnTimeout:       60 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	t := &Tools{root: root, store: store, client: &http.Client{Transport: tr}}
	if _, err := os.Stat(filepath.Join(root, "yt-dlp.new", exeName("yt-dlp"))); err == nil {
		t.pending = true
	}
	return t
}

func exeName(n string) string {
	if runtime.GOOS == "windows" {
		return n + ".exe"
	}
	return n
}

func (t *Tools) dir(id toolID) string {
	switch id {
	case toolYtdlp:
		return filepath.Join(t.root, "yt-dlp")
	case toolDeno:
		return filepath.Join(t.root, "deno")
	default:
		return filepath.Join(t.root, "ffmpeg")
	}
}

func (t *Tools) YtdlpExe() string {
	if t.devMode {
		return "yt-dlp"
	}
	return filepath.Join(t.dir(toolYtdlp), exeName("yt-dlp"))
}
func (t *Tools) DenoExe() string   { return filepath.Join(t.dir(toolDeno), exeName("deno")) }
func (t *Tools) FfmpegDir() string { return t.dir(toolFfmpeg) }

// FfmpegExe and FfprobeExe are run directly to remove the music.
func (t *Tools) FfmpegExe() string {
	if t.devMode {
		return "ffmpeg"
	}
	return filepath.Join(t.FfmpegDir(), exeName("ffmpeg"))
}

func (t *Tools) FfprobeExe() string {
	if t.devMode {
		return "ffprobe"
	}
	return filepath.Join(t.FfmpegDir(), exeName("ffprobe"))
}

func fileOK(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir() && fi.Size() > 0
}

// Missing lists the tools that are not installed (or were removed, e.g. by an antivirus).
func (t *Tools) Missing() []toolID {
	if t.devMode {
		return nil
	}
	var m []toolID
	if !fileOK(t.YtdlpExe()) && !fileOK(filepath.Join(t.root, "yt-dlp.new", exeName("yt-dlp"))) {
		m = append(m, toolYtdlp)
	}
	if !fileOK(t.DenoExe()) {
		m = append(m, toolDeno)
	}
	if !fileOK(filepath.Join(t.FfmpegDir(), exeName("ffmpeg"))) || !fileOK(filepath.Join(t.FfmpegDir(), exeName("ffprobe"))) {
		m = append(m, toolFfmpeg)
	}
	return m
}

// Acquire must wrap every yt-dlp run. Before the first run it puts a freshly
// downloaded yt-dlp in place (swapping is impossible while yt-dlp.exe is running).
func (t *Tools) Acquire() (release func()) {
	t.mu.Lock()
	if t.running == 0 && t.pending {
		if err := swapDir(filepath.Join(t.root, "yt-dlp.new"), t.dir(toolYtdlp)); err == nil {
			t.pending = false
		}
	}
	t.running++
	t.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			t.mu.Lock()
			t.running--
			t.mu.Unlock()
		})
	}
}

// MarkBroken removes a tool that fails to start, so the next setup downloads it again.
func (t *Tools) MarkBroken(id toolID) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.running > 0 || t.devMode {
		return
	}
	_ = os.RemoveAll(t.dir(id))
	if id == toolYtdlp {
		_ = os.RemoveAll(filepath.Join(t.root, "yt-dlp.new"))
		t.pending = false
	}
}

// YtdlpArgs are the options used for every yt-dlp run.
func (t *Tools) YtdlpArgs(cacheDir string) []string {
	args := []string{"--ignore-config", "--no-color", "--encoding", "utf-8", "--socket-timeout", "30"}
	if cacheDir != "" {
		args = append(args, "--cache-dir", cacheDir)
	}
	if !t.devMode {
		args = append(args, "--ffmpeg-location", t.FfmpegDir(), "--js-runtimes", "deno:"+t.DenoExe())
	}
	return args
}

func (t *Tools) Env() []string {
	if t.devMode {
		return toolEnv()
	}
	return toolEnv(t.dir(toolDeno), t.FfmpegDir())
}

// ---------------------------------------------------------------------------
// Installing
// ---------------------------------------------------------------------------

type setupReporter struct {
	mu       sync.Mutex
	steps    []toolID
	totals   map[toolID]int64
	done     map[toolID]int64
	step     int
	tool     toolID
	phase    string
	callback func(SetupProgress)
	last     time.Time
}

func (r *setupReporter) set(step int, tool toolID, phase string) {
	r.mu.Lock()
	r.step, r.tool, r.phase = step, tool, phase
	r.mu.Unlock()
	r.emit(true)
}

func (r *setupReporter) bytes(tool toolID, done, total int64) {
	r.mu.Lock()
	r.done[tool] = done
	if total > 0 {
		r.totals[tool] = total
	}
	r.mu.Unlock()
	r.emit(false)
}

func (r *setupReporter) emit(force bool) {
	r.mu.Lock()
	if !force && time.Since(r.last) < 150*time.Millisecond {
		r.mu.Unlock()
		return
	}
	r.last = time.Now()
	var done, total int64
	for _, s := range r.steps {
		total += r.totals[s]
		d := r.done[s]
		if d > r.totals[s] {
			d = r.totals[s]
		}
		done += d
	}
	p := SetupProgress{Phase: r.phase, Step: r.step, Steps: len(r.steps), Tool: string(r.tool),
		DoneText: HumanSize(done), TotalText: HumanSize(total)}
	if total > 0 {
		p.Percent = float64(done) / float64(total) * 100
	}
	cb := r.callback
	r.mu.Unlock()
	if cb != nil {
		cb(p)
	}
}

// Install downloads whatever is missing. It is safe to call again after a failure:
// partly downloaded files are resumed.
func (t *Tools) Install(ctx context.Context, onProgress func(SetupProgress)) error {
	t.installMu.Lock()
	defer t.installMu.Unlock()
	missing := t.Missing()
	if len(missing) == 0 {
		return nil
	}
	est := map[toolID]int64{toolYtdlp: estYtdlp, toolDeno: estDeno, toolFfmpeg: estFfmpeg}
	rep := &setupReporter{steps: missing, totals: map[toolID]int64{}, done: map[toolID]int64{}, callback: onProgress}
	for _, m := range missing {
		rep.totals[m] = est[m]
	}
	for i, id := range missing {
		rep.set(i+1, id, "download")
		var err error
		switch id {
		case toolYtdlp:
			err = t.installYtdlp(ctx, func(d, tot int64) { rep.bytes(id, d, tot) }, func(ph string) { rep.set(i+1, id, ph) })
			if err == nil {
				t.mu.Lock()
				if t.running == 0 && t.pending {
					if swapDir(filepath.Join(t.root, "yt-dlp.new"), t.dir(toolYtdlp)) == nil {
						t.pending = false
					}
				}
				t.mu.Unlock()
			}
		case toolDeno:
			err = t.installDeno(ctx, func(d, tot int64) { rep.bytes(id, d, tot) }, func(ph string) { rep.set(i+1, id, ph) })
		case toolFfmpeg:
			err = t.installFfmpeg(ctx, func(d, tot int64) { rep.bytes(id, d, tot) }, func(ph string) { rep.set(i+1, id, ph) })
		}
		if err != nil {
			return err
		}
		rep.mu.Lock()
		rep.done[id] = rep.totals[id]
		rep.mu.Unlock()
	}
	rep.set(len(missing), missing[len(missing)-1], "done")
	return nil
}

var errToolBlocked = errors.New("tool downloaded but does not run")

func (t *Tools) dlDir() string { return filepath.Join(t.root, "_downloads") }

func (t *Tools) installYtdlp(ctx context.Context, onBytes func(int64, int64), onPhase func(string)) error {
	tag, _ := t.latestTag(ctx, ytdlpNightlyLatest)
	urls := ytdlpFallbackURLs
	if tag != "" {
		urls = append([]string{fmt.Sprintf(ytdlpNightlyAsset, tag)}, urls...)
	}
	zipPath := filepath.Join(t.dlDir(), "yt-dlp_win.zip")
	if err := t.download(ctx, urls, zipPath, onBytes); err != nil {
		return err
	}
	onPhase("extract")
	// Unpack into a staging folder first: yt-dlp.new is only replaced once the new copy works.
	staging := filepath.Join(t.root, "yt-dlp.staging")
	_ = os.RemoveAll(staging)
	if err := extractZip(zipPath, staging, func(name string) (string, bool) { return name, true }); err != nil {
		os.Remove(zipPath)
		return err
	}
	if err := hoistDirContaining(staging, exeName("yt-dlp")); err != nil {
		os.Remove(zipPath)
		return err
	}
	onPhase("verify")
	ver, err := quickRun(filepath.Join(staging, exeName("yt-dlp")), 3*time.Minute, "--version")
	if err != nil || ver == "" {
		_ = os.RemoveAll(staging)
		return fmt.Errorf("%w: yt-dlp: %v %s", errToolBlocked, err, ver)
	}
	os.Remove(zipPath)
	if tag == "" {
		tag = lastLine(ver)
	}
	newDir := filepath.Join(t.root, "yt-dlp.new")
	t.mu.Lock()
	_ = os.RemoveAll(newDir)
	err = renameRetry(staging, newDir)
	if err == nil {
		t.pending = true
	}
	t.mu.Unlock()
	if err != nil {
		return err
	}
	t.store.Update(func(s *Settings) { s.YtdlpVersion = tag; s.YtdlpChecked = time.Now() })
	return nil
}

var denoVerRe = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

func (t *Tools) latestDenoVersion(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", denoLatestTxt, nil)
	req.Header.Set("User-Agent", userAgent)
	resp, err := t.client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 100))
	v := strings.TrimSpace(string(b))
	if resp.StatusCode != 200 || !denoVerRe.MatchString(v) {
		return ""
	}
	return v
}

func (t *Tools) installDeno(ctx context.Context, onBytes func(int64, int64), onPhase func(string)) error {
	ver := t.latestDenoVersion(ctx)
	var urls []string
	if ver != "" {
		urls = append(urls, fmt.Sprintf(denoAssetGitHub, ver), fmt.Sprintf(denoAssetCDN, ver))
	}
	urls = append(urls, denoFallbackURL)
	zipPath := filepath.Join(t.dlDir(), "deno.zip")
	if err := t.download(ctx, urls, zipPath, onBytes); err != nil {
		return err
	}
	onPhase("extract")
	newDir := t.dir(toolDeno) + ".new"
	_ = os.RemoveAll(newDir)
	err := extractZip(zipPath, newDir, func(name string) (string, bool) {
		if strings.EqualFold(path.Base(name), exeName("deno")) {
			return exeName("deno"), true
		}
		return "", false
	})
	if err == nil && !fileOK(filepath.Join(newDir, exeName("deno"))) {
		err = errors.New("deno.exe not found in archive")
	}
	if err != nil {
		os.Remove(zipPath)
		return err
	}
	onPhase("verify")
	out, err := quickRun(filepath.Join(newDir, exeName("deno")), 2*time.Minute, "--version")
	if err != nil || !strings.Contains(strings.ToLower(out), "deno") {
		_ = os.RemoveAll(newDir)
		return fmt.Errorf("%w: deno: %v %s", errToolBlocked, err, out)
	}
	os.Remove(zipPath)
	if err := t.swapWhenIdle(newDir, t.dir(toolDeno)); err != nil {
		return err
	}
	if ver == "" {
		if m := regexp.MustCompile(`deno (\d+\.\d+\.\d+)`).FindStringSubmatch(out); m != nil {
			ver = "v" + m[1]
		}
	}
	t.store.Update(func(s *Settings) { s.DenoVersion = ver; s.DenoChecked = time.Now() })
	return nil
}

func (t *Tools) installFfmpeg(ctx context.Context, onBytes func(int64, int64), onPhase func(string)) error {
	zipPath := filepath.Join(t.dlDir(), "ffmpeg.zip")
	if err := t.download(ctx, ffmpegURLs, zipPath, onBytes); err != nil {
		return err
	}
	onPhase("extract")
	newDir := t.dir(toolFfmpeg) + ".new"
	_ = os.RemoveAll(newDir)
	err := extractZip(zipPath, newDir, func(name string) (string, bool) {
		dir, base := path.Split(name)
		lb := strings.ToLower(base)
		if !strings.HasSuffix(strings.ToLower(strings.TrimSuffix(dir, "/")), "bin") {
			return "", false
		}
		if lb == exeName("ffplay") || !(strings.HasSuffix(lb, ".exe") || strings.HasSuffix(lb, ".dll") || runtime.GOOS != "windows") {
			return "", false
		}
		return base, true
	})
	if err == nil && (!fileOK(filepath.Join(newDir, exeName("ffmpeg"))) || !fileOK(filepath.Join(newDir, exeName("ffprobe")))) {
		err = errors.New("ffmpeg not found in archive")
	}
	if err != nil {
		os.Remove(zipPath)
		return err
	}
	onPhase("verify")
	out, err := quickRun(filepath.Join(newDir, exeName("ffmpeg")), 2*time.Minute, "-hide_banner", "-version")
	if err != nil || !strings.Contains(strings.ToLower(out), "ffmpeg") {
		_ = os.RemoveAll(newDir)
		return fmt.Errorf("%w: ffmpeg: %v %s", errToolBlocked, err, out)
	}
	os.Remove(zipPath)
	return t.swapWhenIdle(newDir, t.dir(toolFfmpeg))
}

// swapWhenIdle replaces finalDir with newDir, waiting for running yt-dlp processes to end.
func (t *Tools) swapWhenIdle(newDir, finalDir string) error {
	for i := 0; ; i++ {
		t.mu.Lock()
		if t.running == 0 {
			err := swapDir(newDir, finalDir)
			t.mu.Unlock()
			return err
		}
		t.mu.Unlock()
		time.Sleep(time.Second)
		if i > 3600 {
			return errors.New("tools busy")
		}
	}
}

// UpdateYtdlp checks GitHub for a newer yt-dlp and downloads it in the background.
// The new version is put in place before the next yt-dlp run.
func (t *Tools) UpdateYtdlp(ctx context.Context, minAge time.Duration) (bool, error) {
	if t.devMode {
		return false, nil
	}
	st := t.store.Get()
	if time.Since(st.YtdlpChecked) < minAge {
		return false, nil
	}
	if !t.installMu.TryLock() {
		return false, nil
	}
	defer t.installMu.Unlock()
	tag, err := t.latestTag(ctx, ytdlpNightlyLatest)
	if err != nil || tag == "" {
		return false, err
	}
	if tag == st.YtdlpVersion {
		t.store.Update(func(s *Settings) { s.YtdlpChecked = time.Now() })
		return false, nil
	}
	if err := t.installYtdlp(ctx, func(int64, int64) {}, func(string) {}); err != nil {
		return false, err
	}
	return true, nil
}

// RepairDeno re-installs Deno when yt-dlp says the JavaScript runtime is not working.
func (t *Tools) RepairDeno(ctx context.Context, minAge time.Duration) (bool, error) {
	if t.devMode {
		return false, nil
	}
	st := t.store.Get()
	if time.Since(st.DenoChecked) < minAge {
		return false, nil
	}
	if !t.installMu.TryLock() {
		return false, nil
	}
	defer t.installMu.Unlock()
	latest := t.latestDenoVersion(ctx)
	if latest != "" && latest == st.DenoVersion {
		if out, err := quickRun(t.DenoExe(), time.Minute, "--version"); err == nil && strings.Contains(out, "deno") {
			t.store.Update(func(s *Settings) { s.DenoChecked = time.Now() })
			return false, nil
		}
	}
	if err := t.installDeno(ctx, func(int64, int64) {}, func(string) {}); err != nil {
		return false, err
	}
	return true, nil
}

// latestTag follows GitHub's ".../releases/latest" redirect and returns the tag name.
func (t *Tools) latestTag(ctx context.Context, latestURL string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	c := *t.client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, _ := http.NewRequestWithContext(ctx, "GET", latestURL, nil)
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	if i := strings.Index(loc, "/releases/tag/"); i >= 0 {
		tag := strings.Trim(loc[i+len("/releases/tag/"):], "/")
		if tag != "" && !strings.ContainsAny(tag, "?#/") {
			return tag, nil
		}
	}
	return "", fmt.Errorf("no release tag (status %d)", resp.StatusCode)
}

// ---------------------------------------------------------------------------
// HTTP download with resume
// ---------------------------------------------------------------------------

type httpStatusError struct{ code int }

func (e *httpStatusError) Error() string { return "HTTP " + strconv.Itoa(e.code) }

func (t *Tools) download(ctx context.Context, urls []string, dest string, onBytes func(int64, int64)) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	var lastErr error
	for _, u := range urls {
		for attempt := 0; attempt < 4; attempt++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			err := t.downloadOnce(ctx, u, dest, onBytes)
			if err == nil {
				return nil
			}
			lastErr = err
			var he *httpStatusError
			if errors.As(err, &he) && he.code >= 400 && he.code < 500 && he.code != 408 && he.code != 429 {
				break // this address will not work; try the next one
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(1+attempt*2) * time.Second):
			}
		}
	}
	return lastErr
}

var contentRangeRe = regexp.MustCompile(`bytes (\d+)-(\d+)/(\d+|\*)`)

func (t *Tools) downloadOnce(ctx context.Context, url, dest string, onBytes func(int64, int64)) error {
	part := dest + ".part"
	var offset int64
	if fi, err := os.Stat(part); err == nil {
		offset = fi.Size()
	}
	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, "GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	total := int64(-1)
	flags := os.O_CREATE | os.O_WRONLY
	switch resp.StatusCode {
	case http.StatusOK:
		offset = 0
		flags |= os.O_TRUNC
		total = resp.ContentLength
	case http.StatusPartialContent:
		m := contentRangeRe.FindStringSubmatch(resp.Header.Get("Content-Range"))
		if m == nil {
			os.Remove(part)
			return errors.New("bad content-range")
		}
		start, _ := strconv.ParseInt(m[1], 10, 64)
		if start != offset {
			os.Remove(part)
			return errors.New("server resumed at the wrong place")
		}
		if m[3] != "*" {
			total, _ = strconv.ParseInt(m[3], 10, 64)
		}
		flags |= os.O_APPEND
	case http.StatusRequestedRangeNotSatisfiable:
		os.Remove(part)
		return errors.New("range not satisfiable")
	default:
		return &httpStatusError{resp.StatusCode}
	}

	f, err := os.OpenFile(part, flags, 0o644)
	if err != nil {
		return err
	}
	// Abort if the connection stalls: no data for 45 seconds.
	idle := time.AfterFunc(45*time.Second, cancel)
	defer idle.Stop()

	buf := make([]byte, 256<<10)
	done := offset
	onBytes(done, total)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			idle.Reset(45 * time.Second)
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				return werr
			}
			done += int64(n)
			onBytes(done, total)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			return rerr
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	if total > 0 && done != total {
		return fmt.Errorf("incomplete download: %d of %d", done, total)
	}
	_ = os.Remove(dest)
	return renameRetry(part, dest)
}

// ---------------------------------------------------------------------------
// Zip helpers
// ---------------------------------------------------------------------------

// extractZip unpacks the entries accepted by pick (which returns the relative destination).
func extractZip(zipPath, destDir string, pick func(name string) (string, bool)) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer zr.Close()
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}
	for _, f := range zr.File {
		name := strings.ReplaceAll(f.Name, `\`, "/")
		if f.FileInfo().IsDir() || strings.HasSuffix(name, "/") {
			continue
		}
		rel, ok := pick(name)
		if !ok {
			continue
		}
		rel = path.Clean("/" + strings.ReplaceAll(rel, `\`, "/"))[1:]
		if rel == "" || strings.HasPrefix(rel, "../") || strings.Contains(rel, ":") {
			continue
		}
		target := filepath.Join(destDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := writeZipEntry(f, target); err != nil {
			return err
		}
	}
	return nil
}

func writeZipEntry(f *zip.File, target string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, rc); err != nil { // zip checks the CRC at the end
		out.Close()
		return err
	}
	return out.Close()
}

// hoistDirContaining makes sure exe sits directly in dir (some archives nest a folder).
func hoistDirContaining(dir, exe string) error {
	if fileOK(filepath.Join(dir, exe)) {
		return nil
	}
	var found string
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.EqualFold(d.Name(), exe) && found == "" {
			found = filepath.Dir(p)
		}
		return nil
	})
	if found == "" {
		return fmt.Errorf("%s not found in archive", exe)
	}
	tmp := dir + ".hoist"
	_ = os.RemoveAll(tmp)
	if err := renameRetry(found, tmp); err != nil {
		return err
	}
	_ = os.RemoveAll(dir)
	return renameRetry(tmp, dir)
}

func swapDir(newDir, finalDir string) error {
	if _, err := os.Stat(newDir); err != nil {
		return err
	}
	old := finalDir + ".old"
	_ = os.RemoveAll(old)
	if _, err := os.Stat(finalDir); err == nil {
		if err := renameRetry(finalDir, old); err != nil {
			return err
		}
	}
	if err := renameRetry(newDir, finalDir); err != nil {
		_ = renameRetry(old, finalDir)
		return err
	}
	_ = os.RemoveAll(old)
	return nil
}

// renameRetry retries for a few seconds: right after a file is written, Windows Defender
// often keeps it open while scanning, which makes renaming fail briefly.
func renameRetry(from, to string) error {
	var err error
	for i := 0; i < 20; i++ {
		if err = os.Rename(from, to); err == nil {
			return nil
		}
		time.Sleep(time.Duration(200+100*i) * time.Millisecond)
	}
	return err
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		return strings.TrimSpace(s[i+1:])
	}
	return s
}
