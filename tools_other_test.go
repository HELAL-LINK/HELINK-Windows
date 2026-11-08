//go:build !windows

package main

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate}
		h.SetMode(0o755)
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(content))
	}
	zw.Close()
	return buf.Bytes()
}

func TestToolsInstallAndUpdate(t *testing.T) {
	ytZip := makeZip(t, map[string]string{
		"yt-dlp":                     "#!/bin/sh\necho 2026.10.10.233141\n",
		`_internal\base_library.zip`: "x", // PowerShell-made zips may use backslashes
	})
	ytZip2 := makeZip(t, map[string]string{"yt-dlp": "#!/bin/sh\necho 2026.10.12.000000\n"})
	denoZip := makeZip(t, map[string]string{"deno": "#!/bin/sh\necho 'deno 2.9.7 (stable, release, x86_64-pc-windows-msvc)'\n"})
	big := strings.Repeat("A", 300_000)
	ffZip := makeZip(t, map[string]string{
		"ffmpeg-master-latest-win64-gpl-shared/bin/ffmpeg":  "#!/bin/sh\necho ffmpeg version N-1\n#" + big,
		"ffmpeg-master-latest-win64-gpl-shared/bin/ffprobe": "#!/bin/sh\necho ffprobe version N-1\n",
		"ffmpeg-master-latest-win64-gpl-shared/bin/ffplay":  "#!/bin/sh\necho no\n",
		"ffmpeg-master-latest-win64-gpl-shared/doc/x.html":  "doc",
		"../../evil": "zip slip",
	})

	var ffRequests, brokeOnce atomic.Int32
	latestTag := "2026.10.10.233141"
	mux := http.NewServeMux()
	mux.HandleFunc("/nightly/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/yt-dlp/yt-dlp-nightly-builds/releases/tag/"+latestTag, http.StatusFound)
	})
	mux.HandleFunc("/nightly/download/", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "2026.10.12") {
			w.Write(ytZip2)
			return
		}
		w.Write(ytZip)
	})
	mux.HandleFunc("/deno/latest.txt", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "v2.9.7\n") })
	mux.HandleFunc("/deno/gh/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }) // forces the CDN fallback
	mux.HandleFunc("/deno/cdn/", func(w http.ResponseWriter, r *http.Request) { w.Write(denoZip) })
	mux.HandleFunc("/ff.zip", func(w http.ResponseWriter, r *http.Request) {
		ffRequests.Add(1)
		start := 0
		if rg := r.Header.Get("Range"); rg != "" {
			start, _ = strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(rg, "bytes="), "-"))
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(ffZip)-1, len(ffZip)))
			w.Header().Set("Content-Length", strconv.Itoa(len(ffZip)-start))
			w.WriteHeader(http.StatusPartialContent)
			w.Write(ffZip[start:])
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(ffZip)))
		if brokeOnce.Add(1) == 1 {
			// First attempt: the connection dies halfway.
			w.Write(ffZip[:len(ffZip)/2])
			if hj, ok := w.(http.Hijacker); ok {
				w.(http.Flusher).Flush()
				c, _, _ := hj.Hijack()
				c.Close()
			}
			return
		}
		w.Write(ffZip)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ytdlpNightlyLatest = srv.URL + "/nightly/latest"
	ytdlpNightlyAsset = srv.URL + "/nightly/download/%s/yt-dlp_win.zip"
	ytdlpFallbackURLs = nil
	denoLatestTxt = srv.URL + "/deno/latest.txt"
	denoAssetGitHub = srv.URL + "/deno/gh/%s/deno.zip"
	denoAssetCDN = srv.URL + "/deno/cdn/%s/deno.zip"
	denoFallbackURL = srv.URL + "/deno/gh/latest"
	ffmpegURLs = []string{srv.URL + "/ff.zip"}

	root := t.TempDir()
	store := OpenStore(filepath.Join(root, "settings.json"))
	tl := NewTools(filepath.Join(root, "tools"), store)
	if len(tl.Missing()) != 3 {
		t.Fatalf("missing %v", tl.Missing())
	}
	var last SetupProgress
	var steps []string
	err := tl.Install(context.Background(), func(p SetupProgress) {
		last = p
		if len(steps) == 0 || steps[len(steps)-1] != p.Tool+":"+p.Phase {
			steps = append(steps, p.Tool+":"+p.Phase)
		}
	})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if m := tl.Missing(); len(m) != 0 {
		t.Fatalf("still missing %v", m)
	}
	if last.Phase != "done" || last.Percent < 99.9 {
		t.Errorf("last progress %+v", last)
	}
	t.Logf("steps: %v", steps)
	if ffRequests.Load() < 2 {
		t.Errorf("expected a resumed ffmpeg download, got %d requests", ffRequests.Load())
	}
	if fileOK(filepath.Join(tl.FfmpegDir(), "ffplay")) || fileOK(filepath.Join(root, "evil")) {
		t.Error("extracted files that should be skipped")
	}
	if !fileOK(filepath.Join(tl.dir(toolYtdlp), "_internal", "base_library.zip")) {
		t.Error("backslash zip entry not extracted into its folder")
	}
	st := store.Get()
	if st.YtdlpVersion != "2026.10.10.233141" || st.DenoVersion != "v2.9.7" {
		t.Errorf("versions %+v", st)
	}
	if _, err := os.Stat(filepath.Join(root, "tools", "yt-dlp.new")); err == nil {
		t.Error("yt-dlp.new should have been swapped in")
	}

	// Update: same version → nothing to do.
	store.Update(func(s *Settings) { s.YtdlpChecked = time.Time{} })
	if up, err := tl.UpdateYtdlp(context.Background(), time.Hour); up || err != nil {
		t.Errorf("update same version: %v %v", up, err)
	}
	// New nightly appears → downloaded, swapped before the next run.
	latestTag = "2026.10.12.000000"
	store.Update(func(s *Settings) { s.YtdlpChecked = time.Time{} })
	if up, err := tl.UpdateYtdlp(context.Background(), time.Hour); !up || err != nil {
		t.Fatalf("update new version: %v %v", up, err)
	}
	release := tl.Acquire()
	out, _ := quickRun(tl.YtdlpExe(), 10*time.Second, "--version")
	release()
	if out != "2026.10.12.000000" {
		t.Errorf("after update yt-dlp says %q", out)
	}
	// Recently checked → no network at all.
	if up, _ := tl.UpdateYtdlp(context.Background(), time.Hour); up {
		t.Error("should not update twice")
	}
	// A broken yt-dlp is removed so setup reinstalls it.
	tl.MarkBroken(toolYtdlp)
	if m := tl.Missing(); len(m) != 1 || m[0] != toolYtdlp {
		t.Errorf("after MarkBroken missing=%v", m)
	}
}
