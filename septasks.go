package main

// The music remover in the downloads list: downloads "without music", and files of the
// computer (chosen in HELINK, or a finished download) of which a copy without the music is
// made next to them.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// sepFileTypes are offered first when choosing files to remove the music from.
const sepFileTypes = "*.mp3;*.m4a;*.aac;*.wav;*.flac;*.ogg;*.oga;*.opus;*.wma;*.aif;*.aiff;*.ape;*.amr;*.mka;" +
	"*.mp4;*.m4v;*.mkv;*.webm;*.avi;*.mov;*.wmv;*.flv;*.3gp;*.mpg;*.mpeg;*.ts;*.mts;*.m2ts;*.vob;*.ogv;*.asf"

// downloadedMark, in a download's temporary folder, names the file yt-dlp finished: after
// a pause, only the music is left to remove.
const downloadedMark = "downloaded.txt"

func markDownloaded(job, path string) {
	if rel, err := filepath.Rel(job, path); err == nil && !strings.HasPrefix(rel, "..") {
		_ = os.WriteFile(filepath.Join(job, downloadedMark), []byte(filepath.ToSlash(rel)), 0o644)
	}
}

// downloadedFile is the file a download without music finished before it was paused ("" if none).
func downloadedFile(job string) string {
	b, err := os.ReadFile(filepath.Join(job, downloadedMark))
	if err != nil {
		return ""
	}
	rel := filepath.FromSlash(strings.TrimSpace(string(b)))
	if rel == "" || filepath.IsAbs(rel) || strings.HasPrefix(rel, "..") {
		return ""
	}
	p := filepath.Join(job, rel)
	if !fileOK(p) {
		return ""
	}
	return p
}

// onlyFileIn is the one file in dir ("" if there are none or several).
func onlyFileIn(dir string) string {
	entries, _ := os.ReadDir(dir)
	found := ""
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if found != "" {
			return ""
		}
		found = filepath.Join(dir, e.Name())
	}
	return found
}

// finishNoMusic removes the music from what a download without music downloaded, and
// saves the result.
func (a *App) finishNoMusic(ctx context.Context, t *DownloadTask, file, job, saveDir string) outcome {
	a.mu.Lock()
	base, kind, quality, site := t.Base, t.Kind, t.AudioQuality, t.Site
	a.mu.Unlock()
	mp3 := ""
	if kind == "audio" {
		if mp3 = quality; mp3 == "" {
			mp3 = "192K"
		}
	}
	work := filepath.Join(job, "sep")
	path, e := a.removeMusic(ctx, t, sepJob{src: file, work: work, base: base, saveDir: saveDir, mp3: mp3})
	if ctx.Err() != nil || e != nil {
		_ = os.RemoveAll(work) // it starts over next time; the download itself stays
	}
	if ctx.Err() != nil {
		return outcome{stopped: true}
	}
	if e != nil {
		switch {
		case e.Code == "sep_no_audio" && kind == "video":
			// A video without sound has no music either: it is saved as it is.
			return a.saveFile(ctx, t, file, saveDir, NewProgressTracker(kind, nil))
		case e.Code == "file_missing":
			// The download vanished (an antivirus?): trying again downloads it again.
			_ = os.Remove(filepath.Join(job, downloadedMark))
		}
		return outcome{err: e.Localize(site)}
	}
	return a.saveFile(ctx, t, path, saveDir, NewProgressTracker(kind, nil))
}

// runSepTask makes the copy without music of a file of the computer.
func (a *App) runSepTask(ctx context.Context, t *DownloadTask) outcome {
	a.mu.Lock()
	src, base, saveDir, cat := t.SrcPath, t.Base, t.SaveDir, t.Category
	a.mu.Unlock()
	if !fileOK(src) {
		return outcome{err: NewUserError("file_missing")}
	}
	// The copy goes next to the file; where it cannot (a CD, a folder of Windows), where
	// downloads go.
	if saveDir == "" || !folderReachable(saveDir) || !dirWritable(saveDir) {
		saveDir = a.saveDirFor(cat)
		a.mu.Lock()
		t.SaveDir = saveDir
		a.mu.Unlock()
	}
	if err := os.MkdirAll(saveDir, 0o755); err != nil || !dirWritable(saveDir) {
		e := NewUserError("permission")
		e.Folder = saveDir
		return outcome{err: e}
	}
	a.mu.Lock()
	job := jobDirOf(t)
	a.mu.Unlock()
	if err := os.MkdirAll(job, 0o755); err != nil {
		e := NewUserError("permission")
		e.Details, e.Folder = err.Error(), saveDir
		return outcome{err: e}
	}
	hideFolder(filepath.Dir(job))
	path, e := a.removeMusic(ctx, t, sepJob{src: src, work: job, base: base, saveDir: saveDir})
	if ctx.Err() != nil || e != nil {
		removeJob(job) // nothing in it is worth keeping: it starts over next time
	}
	if ctx.Err() != nil {
		return outcome{stopped: true}
	}
	if e != nil {
		return outcome{err: e}
	}
	a.mu.Lock()
	kind := t.Kind
	a.mu.Unlock()
	return a.saveFile(ctx, t, path, saveDir, NewProgressTracker(kind, nil))
}

// unfinishedSep finds the entry already removing the music of that file. Call with the lock held.
func (a *App) unfinishedSepLocked(path string) *DownloadTask {
	for _, t := range a.tasks {
		if t.isSep() && !t.finished() && strings.EqualFold(filepath.Clean(t.SrcPath), path) {
			return t
		}
	}
	return nil
}

// AddSepFiles adds files of the computer to the list, to make of each a copy without its
// music (next to it, "name (without music)"; the file itself stays as it is). The music
// is removed from one file at a time, in the order added. It returns {"ids": […],
// "skipped": n}: folders, empty files and files that are gone are skipped; a file already
// in the list is continued instead of added twice.
func (a *App) AddSepFiles(paths []string) map[string]any {
	ids := []string{}
	skipped := 0
	var added, again []*DownloadTask
	now := time.Now().UnixMilli()
	a.mu.Lock()
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			skipped++
			continue
		}
		p = filepath.Clean(p)
		fi, err := os.Stat(p)
		if err != nil || fi.IsDir() || fi.Size() == 0 {
			skipped++
			continue
		}
		if t := a.unfinishedSepLocked(p); t != nil {
			ids = append(ids, t.ID)
			again = append(again, t)
			continue
		}
		name := filepath.Base(p)
		base, ext := splitName(name)
		if base == "" {
			base = name
		}
		cat, kind := catMusic, "audio"
		if categoryOfExt[strings.ToLower(ext)] == catVideo {
			cat, kind = catVideo, "video"
		}
		t := &DownloadTask{Type: typeSep, SrcPath: p, Title: name, Kind: kind, Base: base + noMusicSuffix(),
			SaveDir: filepath.Dir(p), Category: cat, NoMusic: true, SizeBytes: fi.Size(),
			CreatedAt: now, QueuedAt: now, State: stateQueued, ETA: -1}
		t.ID = a.newID()
		a.tasks = append(a.tasks, t)
		added = append(added, t)
		ids = append(ids, t.ID)
	}
	dropped := a.trimFinishedLocked()
	a.mu.Unlock()
	for _, d := range dropped {
		a.forgetTask(d)
	}
	for _, t := range again {
		a.Resume(t.ID)
	}
	if len(added) > 0 {
		a.saveTasksSoon()
		for _, t := range added {
			a.emitTask("add", t, true)
		}
		a.wg.Add(1)
		go func() {
			defer a.wg.Done()
			a.schedule()
		}()
	}
	return map[string]any{"ids": ids, "skipped": skipped}
}

// RemoveMusicFrom makes a copy without music of a finished download (next to it). It
// returns {"id": …} or {"error": …}.
func (a *App) RemoveMusicFrom(id string) map[string]any {
	a.mu.Lock()
	path := ""
	if t := a.find(id); t != nil && t.finished() {
		path = t.Path
	}
	a.mu.Unlock()
	if path == "" {
		return map[string]any{"error": NewUserError("unknown")}
	}
	if !fileOK(path) {
		return map[string]any{"error": NewUserError("file_missing")}
	}
	ids, _ := a.AddSepFiles([]string{path})["ids"].([]string)
	if len(ids) == 0 {
		return map[string]any{"error": NewUserError("file_missing")}
	}
	return map[string]any{"id": ids[0]}
}
