package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Settings is everything the app remembers between runs.
type Settings struct {
	SaveDir      string    `json:"saveDir,omitempty"`
	YtdlpVersion string    `json:"ytdlpVersion,omitempty"`
	YtdlpChecked time.Time `json:"ytdlpChecked,omitempty"`
	DenoVersion  string    `json:"denoVersion,omitempty"`
	DenoChecked  time.Time `json:"denoChecked,omitempty"`
	FfmpegSource string    `json:"ffmpegSource,omitempty"`
	LastKind     string    `json:"lastKind,omitempty"`
	Lang         string    `json:"lang,omitempty"` // "ar" or "en"

	// The download manager (HELINK 2). Switches are named for the change from what
	// most people want, so an empty file means the usual behaviour.
	MaxParallel int `json:"maxParallel,omitempty"` // downloads at the same time (0: 3)
	Conns       int `json:"conns,omitempty"`       // connections per file (0: 8)
	// NoCategoryDirs: everything in the save folder itself, not in a folder per kind of file
	// (Video, Music, Pictures, Programs…; the usual since 2.2, an option before).
	NoCategoryDirs bool     `json:"noCategoryDirs,omitempty"`
	NoCatch        bool     `json:"noCatch,omitempty"`     // the browsers keep their downloads
	NoDialog       bool     `json:"noDialog,omitempty"`    // a download from the browser starts without asking
	NoAutostart    bool     `json:"noAutostart,omitempty"` // HELINK does not start with Windows
	NoTray         bool     `json:"noTray,omitempty"`      // closing the window quits instead of staying near the clock
	NoNotify       bool     `json:"noNotify,omitempty"`    // no message when a download finishes
	NoButtons      bool     `json:"noButtons,omitempty"`   // no download button on videos in web pages
	CatchTypes     []string `json:"catchTypes,omitempty"`  // the kinds of files taken from the browsers (nil: defaultCatchTypes)
	CatchMinKB     int      `json:"catchMinKB,omitempty"`  // smaller files stay with the browser
	SkipSites      []string `json:"skipSites,omitempty"`   // sites whose downloads stay with the browser
	ButtonSkip     []string `json:"buttonSkip,omitempty"`  // sites without the button on videos
	IntroShown     bool     `json:"introShown,omitempty"`  // "Add HELINK to your browser" was shown once, at the first start

	// Updates (update.go).
	UpdateChecked     time.Time `json:"updateChecked,omitempty"`     // when GitHub last answered
	UpdateSeen        string    `json:"updateSeen,omitempty"`        // the newest version it named
	UpdateSnoozed     string    `json:"updateSnoozed,omitempty"`     // "Not now" for this version…
	UpdateSnoozeUntil time.Time `json:"updateSnoozeUntil,omitempty"` // …until then
	// While HELINK replaces itself: the downloads to continue, and from which version to which.
	ResumeAfterUpdate []string `json:"resumeAfterUpdate,omitempty"`
	UpdatingFrom      string   `json:"updatingFrom,omitempty"`
	UpdatingTo        string   `json:"updatingTo,omitempty"`
}

// The usual values of the settings above.
const (
	defaultParallel = 3
	defaultFileConn = 8
)

// defaultCatchTypes are the files taken from the browsers, as Internet Download Manager does.
var defaultCatchTypes = []string{
	"3gp", "7z", "aac", "ace", "aif", "apk", "arj", "asf", "avi", "bin", "bz2", "dmg", "exe", "flac", "flv", "gz",
	"gzip", "img", "iso", "lzh", "m4a", "m4v", "mkv", "mov", "mp3", "mp4", "mpeg", "mpg", "msi", "msu", "ogg", "ogv",
	"opus", "pdf", "qt", "rar", "rm", "rmvb", "sit", "sitx", "tar", "tgz", "torrent", "ts", "wav", "webm", "wma",
	"wmv", "xz", "z", "zip", "zst", "docx", "xlsx", "pptx", "doc", "xls", "ppt", "epub", "deb", "rpm", "appx",
	"msix", "cab", "jar", "vob", "mts", "m2ts",
}

func (s Settings) parallel() int {
	if s.MaxParallel <= 0 {
		return defaultParallel
	}
	return min(s.MaxParallel, 10)
}

func (s Settings) conns() int {
	if s.Conns <= 0 {
		return defaultFileConn
	}
	return min(s.Conns, maxConns)
}

func (s Settings) catchTypes() []string {
	if s.CatchTypes == nil {
		return defaultCatchTypes
	}
	return s.CatchTypes
}

type Store struct {
	path string
	mu   sync.Mutex
	s    Settings
}

func OpenStore(path string) *Store {
	st := &Store{path: path}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &st.s)
	}
	return st
}

func (st *Store) Get() Settings {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.s
}

// Update changes the settings and writes them to disk (write-then-rename, so a crash
// never leaves a half-written file).
func (st *Store) Update(f func(s *Settings)) {
	st.mu.Lock()
	defer st.mu.Unlock()
	f(&st.s)
	b, err := json.MarshalIndent(st.s, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(st.path), 0o755)
	tmp := st.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, st.path)
}
