package main

// The download manager's settings and the commands that act on the whole list.

import (
	"log"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
)

// SettingsView is what the settings window shows (switches named for what they turn on).
type SettingsView struct {
	SaveDir      string   `json:"saveDir"`
	SaveDirLabel string   `json:"saveDirLabel"`
	MaxParallel  int      `json:"maxParallel"`
	Conns        int      `json:"conns"`
	CategoryDirs bool     `json:"categoryDirs"`
	Catch        bool     `json:"catch"`
	Dialog       bool     `json:"dialog"`
	Autostart    bool     `json:"autostart"`
	Tray         bool     `json:"tray"`
	Notify       bool     `json:"notify"`
	Buttons      bool     `json:"buttons"`
	CatchTypes   []string `json:"catchTypes"`
	DefaultTypes []string `json:"defaultTypes"`
	CatchMinKB   int      `json:"catchMinKB"`
	SkipSites    []string `json:"skipSites"`
	ButtonSkip   []string `json:"buttonSkip"`
	Lang         string   `json:"lang"`
	IntroShown   bool     `json:"introShown"`
}

func (a *App) settingsView() SettingsView {
	s := a.store.Get()
	dir := a.SaveDir()
	nz := func(l []string) []string {
		if l == nil {
			return []string{}
		}
		return l
	}
	return SettingsView{SaveDir: dir, SaveDirLabel: folderLabel(dir), MaxParallel: s.parallel(), Conns: s.conns(),
		CategoryDirs: !s.NoCategoryDirs, Catch: !s.NoCatch, Dialog: !s.NoDialog, Autostart: !s.NoAutostart, Tray: !s.NoTray,
		Notify: !s.NoNotify, Buttons: !s.NoButtons, CatchTypes: s.catchTypes(), DefaultTypes: defaultCatchTypes,
		CatchMinKB: s.CatchMinKB, SkipSites: nz(s.SkipSites), ButtonSkip: nz(s.ButtonSkip), Lang: curLang(),
		IntroShown: s.IntroShown}
}

// GetSettings is what the settings window shows.
func (a *App) GetSettings() SettingsView { return a.settingsView() }

// ChangeSettings changes the settings named in ch (as the settings window sends them)
// and returns them all.
func (a *App) ChangeSettings(ch map[string]any) SettingsView {
	a.store.Update(func(s *Settings) {
		for k, v := range ch {
			switch k {
			case "maxParallel":
				if n, ok := intOf(v); ok {
					s.MaxParallel = max(1, min(10, n))
				}
			case "conns":
				if n, ok := intOf(v); ok {
					s.Conns = max(1, min(maxConns, n))
				}
			case "categoryDirs":
				s.NoCategoryDirs = !boolOf(v)
			case "catch":
				s.NoCatch = !boolOf(v)
			case "dialog":
				s.NoDialog = !boolOf(v)
			case "autostart":
				s.NoAutostart = !boolOf(v)
			case "tray":
				s.NoTray = !boolOf(v)
			case "notify":
				s.NoNotify = !boolOf(v)
			case "buttons":
				s.NoButtons = !boolOf(v)
			case "catchTypes":
				if l, ok := v.([]any); ok {
					s.CatchTypes = cleanExts(l)
				} else if v == nil {
					s.CatchTypes = nil // the usual list again
				}
			case "catchMinKB":
				if n, ok := intOf(v); ok {
					s.CatchMinKB = max(0, min(1<<20, n))
				}
			case "skipSites":
				s.SkipSites = cleanHosts(v)
			case "buttonSkip":
				s.ButtonSkip = cleanHosts(v)
			case "introShown":
				s.IntroShown = boolOf(v)
			}
		}
	})
	st := a.store.Get()
	a.writeExtFile()
	if a.SettingsChanged != nil {
		a.SettingsChanged(st)
	}
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		a.schedule() // more downloads may run at once now
	}()
	return a.settingsView()
}

func intOf(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	}
	return 0, false
}

func boolOf(v any) bool {
	b, _ := v.(bool)
	return b
}

// cleanExts turns what the user typed ("MP4", ".zip ", "r0*") into a list of extensions.
func cleanExts(l []any) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, x := range l {
		s, _ := x.(string)
		for _, e := range strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == ',' || r == ';' || r == '\n' }) {
			e = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(e), "."))
			if e == "" || len(e) > 12 || seen[e] {
				continue
			}
			seen[e] = true
			out = append(out, e)
		}
	}
	sort.Strings(out)
	return out
}

// cleanHosts turns what the user typed ("https://www.Example.com/page", "example.org")
// into a list of sites.
func cleanHosts(v any) []string {
	l, _ := v.([]any)
	seen := map[string]bool{}
	var out []string
	for _, x := range l {
		s, _ := x.(string)
		for _, h := range strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == ',' || r == ';' || r == '\n' }) {
			h = strings.ToLower(strings.TrimSpace(h))
			if !strings.Contains(h, "://") {
				h = "https://" + h
			}
			u, err := url.Parse(h)
			if err != nil || u.Hostname() == "" {
				continue
			}
			host := strings.TrimPrefix(u.Hostname(), "www.")
			if !seen[host] {
				seen[host] = true
				out = append(out, host)
			}
		}
	}
	return out
}

// ResumeAll starts again every paused or failed download.
func (a *App) ResumeAll() {
	var ids []string
	a.mu.Lock()
	for _, t := range a.tasks {
		if t.State == statePaused || t.State == stateError {
			ids = append(ids, t.ID)
		}
	}
	a.mu.Unlock()
	for _, id := range ids {
		a.Resume(id)
	}
}

// Remove deletes downloads from the list (as Delete does); withFile also puts finished
// files in the Recycle Bin.
func (a *App) Remove(ids []string, withFile bool) {
	for _, id := range ids {
		if withFile {
			a.mu.Lock()
			path := ""
			if t := a.find(id); t != nil && t.finished() {
				path = t.Path
			}
			a.mu.Unlock()
			if path != "" && pathExists(path) {
				if err := moveToTrash(path); err != nil {
					log.Printf("deleting %s: %v", path, err)
				}
			}
		}
		a.Delete(id)
	}
}

// FinishedPath says whether path is a file that a download in the list saved, or a file
// chosen to remove its music from (the page may only open those).
func (a *App) FinishedPath(path string) bool {
	if path == "" {
		return false
	}
	clean := filepath.Clean(path)
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, t := range a.tasks {
		if (t.finished() && strings.EqualFold(filepath.Clean(t.Path), clean)) ||
			(t.isSep() && strings.EqualFold(filepath.Clean(t.SrcPath), clean)) {
			return fileOK(clean) || pathExists(clean)
		}
	}
	return false
}
