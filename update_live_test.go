package main

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

// TestLiveUpdate checks what is published on GitHub the way HELINK sees it: latest.json read
// from its address and checked with the key in this program, naming this version; each file
// downloaded from its address and checked (size, SHA-256); the portable zip holding a
// HELINK.exe. Run after devtools/publish_update.sh:
//
//	HELINK_LIVE_UPDATE=1 go test -count=1 -run TestLiveUpdate -v .
func TestLiveUpdate(t *testing.T) {
	if os.Getenv("HELINK_LIVE_UPDATE") == "" {
		t.Skip("HELINK_LIVE_UPDATE=1 checks what is published")
	}
	tools := NewTools(t.TempDir(), nil)
	ctx := context.Background()
	m, err := fetchManifestFrom(ctx, tools.client, updateManifestURLs[0])
	if err != nil {
		t.Fatalf("%s: %v", updateManifestURLs[0], err)
	}
	t.Logf("latest.json: %s (%s), ar %q, en %q", m.Version, m.Date, m.Notes["ar"], m.Notes["en"])
	if m.Version != appVersion {
		t.Errorf("published %s, this is %s", m.Version, appVersion)
	}
	if len(m.Notes["ar"]) == 0 || len(m.Notes["en"]) == 0 {
		t.Error("no notes")
	}
	for _, u := range updateManifestURLs[1:] {
		if m2, err := fetchManifestFrom(ctx, tools.client, u); err != nil {
			t.Logf("the copy at %s cannot be read from here: %v", u, err)
		} else if m2.Version != m.Version {
			t.Logf("the copy at %s still says %s (it follows GitHub within hours)", u, m2.Version)
		}
	}
	dir := t.TempDir()
	var zipPath string
	for _, f := range []*updateFile{m.Setup, m.Portable} {
		if f == nil {
			t.Fatal("a file is missing from latest.json")
		}
		dest := filepath.Join(dir, path.Base(f.URLs[0]))
		if err := tools.download(ctx, f.URLs[:1], dest, func(int64, int64) {}); err != nil {
			t.Fatalf("%s: %v", f.URLs[0], err)
		}
		if !fileMatches(dest, f) {
			t.Errorf("%s does not match latest.json", f.URLs[0])
		}
		t.Logf("%s: %d bytes, SHA-256 %s: matches", f.URLs[0], f.Size, f.SHA256)
		if strings.HasSuffix(dest, ".zip") {
			zipPath = dest
		}
	}
	if zipPath == "" {
		t.Fatal("no portable zip")
	}
	if err := extractUpdateExe(zipPath, filepath.Join(dir, "HELINK.exe")); err != nil {
		t.Errorf("portable zip: %v", err)
	}
}
