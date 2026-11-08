package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Download states.
const (
	stateQueued  = "queued"  // waiting for a free slot
	stateRunning = "running" //
	statePaused  = "paused"  // stopped by the user (or the app was closed); the part downloaded is kept
	stateError   = "error"   // failed; "try again" continues from what was downloaded
	stateDone    = "done"    //
)

// DownloadTask is one entry of the downloads list: what to download (fixed when it is
// added), where it stands, and the finished file. Saved in downloads.json, so a paused or
// interrupted download is still there, with its progress, after the app was closed.
type DownloadTask struct {
	// ---- what to download
	ID           string
	Type         string // "" for a video or its sound (through yt-dlp), typeFile for a file from a web address, typeSep for removing the music from a file on the computer
	URL          string // the post's address, after opening short links; for a file, its address
	Site         string // one of the sites, or the web address's host ("dl.example.com")
	Index        int    // the video's position in the post for yt-dlp (1-based); 0 for a plain video
	Title        string
	Channel      string
	DurationText string
	Key          string
	Kind         string // "video" | "audio"
	Label        string
	SizeBytes    int64
	FormatSpec   string
	SortSpec     string
	AudioQuality string
	Parts        []dlPart
	Base         string  // the file name without extension
	SaveDir      string  // where the file goes; its temporary folder is inside it
	Category     string  // video, music, programs, documents, compressed, other
	Page         string  // the page it was found on, if any
	NoMusic      bool    // the music is removed (a video or MP3 "without music"; always for typeSep)
	List         string  // the playlist (course) it was added from, if any
	SrcPath      string  // typeSep: the file whose music is removed (it stays as it is)
	ClipStart    float64 // a part of the video only (ClipEnd > 0): from ClipStart to ClipEnd seconds
	ClipEnd      float64
	// ---- for a file: how to ask for it (the browser's cookies are kept apart, see cookieFile)
	Src        FileSource
	FileName   string // the name it gets (with its extension)
	Resumable  bool   // the server gives parts: a pause keeps what was downloaded
	FetchedAt  int64  // when the video was read, in ms (the saved links expire after a few hours)
	CreatedAt  int64
	QueuedAt   int64
	FinishedAt int64

	// ---- where it stands
	State      string
	Stage      string  // while running: preparing, updating, video, audio, merge, convert, finish, save, sepwait, sepmodel, separate
	Percent    float64 // 0..100
	Done       int64   // bytes downloaded of the whole
	Total      int64
	Speed      float64 // bytes per second while running
	ETA        float64 // seconds left while running, -1 if unknown
	PartsCount int
	PartIndex  int
	Err        *UserError

	// ---- the finished file
	Path     string
	FileSize int64

	// ---- only while the app runs (not saved)
	conns           int        // a file's connections receiving right now
	segments        [][3]int64 // a file's parts (start, end, bytes done)
	thumb           string
	rev             int64              // grows with every change sent to the page (events can arrive out of order)
	cancel          context.CancelFunc // set while a download process belongs to this task
	stopReason      string             // "pause" or "delete" while the process is being stopped
	resumeAfterStop bool               // "resume" was pressed while it was still stopping
	adding          bool               // just added: its folder is being prepared, it waits until then
	sepPhase        bool               // removing the music (or waiting its turn): it no longer takes a download's place
}

// typeFile marks a download of a file (not a video read through yt-dlp); typeSep, a file on
// the computer whose music is removed (nomusic.go).
const (
	typeFile = "file"
	typeSep  = "sep"
)

func (t *DownloadTask) active() bool   { return t.State == stateQueued || t.State == stateRunning }
func (t *DownloadTask) finished() bool { return t.State == stateDone }
func (t *DownloadTask) isFile() bool   { return t.Type == typeFile }
func (t *DownloadTask) isSep() bool    { return t.Type == typeSep }

// apply takes the latest progress of the running download.
func (t *DownloadTask) apply(o Overall) {
	t.Stage = o.Stage
	t.PartsCount, t.PartIndex = o.PartsCount, o.PartIndex
	t.Speed, t.ETA = o.Speed, o.ETA
	if o.Percent >= 0 {
		t.Percent = o.Percent
		if o.Total > 0 {
			t.Done, t.Total = o.Done, o.Total
		}
	} else {
		// A final step (joining, converting, saving): the download itself is complete.
		t.Percent = 100
	}
}

// labelText is "720p", "4K", "192k"; "Video" for a file of unknown size of picture; a
// file's extension ("ZIP").
func (t *DownloadTask) labelText() string {
	if t.isFile() {
		_, ext := splitName(t.FileName)
		return strings.ToUpper(ext)
	}
	if t.isSep() {
		_, ext := splitName(t.Title)
		return strings.ToUpper(ext)
	}
	if t.Key == "vbest" {
		return tr("فيديو", "Video")
	}
	return t.Label
}

// picture compacts the parts of a file for the page: at most 48, merged where needed.
func picture(segs [][3]int64, total int64) [][3]int64 {
	if total <= 0 || len(segs) <= 48 {
		return segs
	}
	out := make([][3]int64, 0, 48)
	per := (len(segs) + 47) / 48
	for i := 0; i < len(segs); i += per {
		j := min(i+per, len(segs))
		m := [3]int64{segs[i][0], segs[j-1][1], 0}
		for _, s := range segs[i:j] {
			m[2] += s[2]
		}
		out = append(out, m)
	}
	return out
}

// toJSON is what the page shows, in the language in use. The picture is sent only when
// asked (it can be large). Call with the app's lock held.
func (t *DownloadTask) toJSON(withThumb bool) map[string]any {
	m := map[string]any{
		"id": t.ID, "type": t.Type, "title": t.Title, "channel": t.Channel, "durationText": t.DurationText, "rev": t.rev,
		"url": t.URL, "site": t.Site, "siteName": siteName(t.Site), "kind": t.Kind, "key": t.Key,
		"label": t.labelText(), "sizeText": HumanSize(t.SizeBytes), "state": t.State, "stage": t.Stage,
		"percent": t.Percent, "doneText": "", "totalText": HumanSize(t.Total), "speedText": "", "etaText": "",
		"partsCount": t.PartsCount, "partIndex": t.PartIndex, "createdAt": t.CreatedAt,
		"category": t.Category, "page": t.Page, "saveDir": t.SaveDir, "folderLabel": folderLabel(t.SaveDir),
		"size": t.SizeBytes, "done": t.Done, "total": t.Total, "noMusic": t.NoMusic,
	}
	if t.List != "" {
		m["list"] = t.List
	}
	if t.ClipEnd > 0 {
		m["clip"] = HumanClock(t.ClipStart) + " – " + HumanClock(t.ClipEnd)
	}
	if t.isSep() {
		m["srcPath"], m["srcName"], m["srcFolder"] = t.SrcPath, filepath.Base(t.SrcPath), filepath.Dir(t.SrcPath)
		m["srcFolderLabel"] = folderLabel(filepath.Dir(t.SrcPath))
	}
	if t.Total > 0 || (t.isFile() && t.Done > 0) {
		m["doneText"] = HumanSize(t.Done)
	}
	if t.isFile() {
		m["fileName"] = t.FileName
		m["resumable"] = t.Resumable
		m["referer"] = t.Src.Referer
		if t.Total <= 0 {
			m["sizeText"], m["totalText"] = "", ""
		}
		if t.State == stateRunning {
			m["conns"] = t.conns
			if len(t.segments) > 0 {
				m["segments"] = picture(t.segments, t.Total)
			}
		}
	}
	if t.State == stateRunning {
		m["speedText"] = HumanSpeed(t.Speed)
		m["speed"] = t.Speed
		m["eta"] = t.ETA
		if t.ETA >= 0 {
			m["etaText"] = HumanETA(t.ETA)
		}
	}
	if t.Err != nil && t.State == stateError {
		m["error"] = *t.Err
	}
	if t.State == stateDone {
		m["result"] = map[string]any{"path": t.Path, "name": filepath.Base(t.Path), "folder": filepath.Dir(t.Path),
			"folderLabel": folderLabel(filepath.Dir(t.Path)), "sizeText": HumanSize(t.FileSize), "kind": t.Kind}
		m["finishedAt"] = t.FinishedAt
	}
	if t.stopReason != "" {
		m["stopping"] = t.stopReason
	}
	if withThumb && t.thumb != "" {
		m["thumb"] = t.thumb
	}
	return m
}

// ---------------------------------------------------------------------------
// On disk
// ---------------------------------------------------------------------------

// storedError keeps the code, not the words: the message is written in the language in
// use when shown.
type storedError struct {
	Code    string `json:"code"`
	Details string `json:"details,omitempty"`
	Action  string `json:"action,omitempty"`
	Retry   bool   `json:"retry"`
	Site    string `json:"site,omitempty"`
	Need    int64  `json:"need"`
	Free    int64  `json:"free"`
	Folder  string `json:"folder,omitempty"`
}

type storedPart struct {
	FormatID string `json:"formatId"`
	Bytes    int64  `json:"bytes"`
	Audio    bool   `json:"audio"`
}

type storedTask struct {
	ID           string       `json:"id"`
	Type         string       `json:"type,omitempty"`
	URL          string       `json:"url"`
	Site         string       `json:"site"`
	Index        int          `json:"index"`
	Title        string       `json:"title"`
	Channel      string       `json:"channel"`
	DurationText string       `json:"durationText"`
	Key          string       `json:"key"`
	Kind         string       `json:"kind"`
	Label        string       `json:"label"`
	SizeBytes    int64        `json:"sizeBytes"`
	FormatSpec   string       `json:"formatSpec"`
	SortSpec     string       `json:"sortSpec"`
	AudioQuality string       `json:"audioQuality"`
	Parts        []storedPart `json:"parts"`
	Base         string       `json:"base"`
	SaveDir      string       `json:"saveDir"`
	FetchedAt    int64        `json:"fetchedAt"`
	CreatedAt    int64        `json:"createdAt"`
	QueuedAt     int64        `json:"queuedAt"`
	FinishedAt   int64        `json:"finishedAt"`
	State        string       `json:"state"`
	Percent      float64      `json:"percent"`
	Done         int64        `json:"done"`
	Total        int64        `json:"total"`
	PartsCount   int          `json:"partsCount"`
	PartIndex    int          `json:"partIndex"`
	Error        *storedError `json:"error,omitempty"`
	Path         string       `json:"path,omitempty"`
	FileSize     int64        `json:"fileSize,omitempty"`
	Category     string       `json:"category,omitempty"`
	Page         string       `json:"page,omitempty"`
	Src          *FileSource  `json:"src,omitempty"` // without the cookies
	FileName     string       `json:"fileName,omitempty"`
	Resumable    bool         `json:"resumable,omitempty"`
	NoMusic      bool         `json:"noMusic,omitempty"`
	SrcPath      string       `json:"srcPath,omitempty"`
	List         string       `json:"list,omitempty"`
	ClipStart    float64      `json:"clipStart,omitempty"`
	ClipEnd      float64      `json:"clipEnd,omitempty"`
}

type storedList struct {
	Version int          `json:"version"`
	Tasks   []storedTask `json:"tasks"`
}

func (t *DownloadTask) toStore() storedTask {
	s := storedTask{ID: t.ID, Type: t.Type, URL: t.URL, Site: t.Site, Index: t.Index, Title: t.Title, Channel: t.Channel,
		DurationText: t.DurationText, Key: t.Key, Kind: t.Kind, Label: t.Label, SizeBytes: t.SizeBytes,
		FormatSpec: t.FormatSpec, SortSpec: t.SortSpec, AudioQuality: t.AudioQuality, Base: t.Base, SaveDir: t.SaveDir,
		FetchedAt: t.FetchedAt, CreatedAt: t.CreatedAt, QueuedAt: t.QueuedAt, FinishedAt: t.FinishedAt, State: t.State,
		Percent: t.Percent, Done: t.Done, Total: t.Total, PartsCount: t.PartsCount, PartIndex: t.PartIndex,
		Category: t.Category, Page: t.Page, FileName: t.FileName, Resumable: t.Resumable, NoMusic: t.NoMusic, SrcPath: t.SrcPath,
		List: t.List, ClipStart: t.ClipStart, ClipEnd: t.ClipEnd}
	if t.isFile() || t.Src.Referer != "" || t.Src.UserAgent != "" {
		src := t.Src
		src.Cookies = nil
		s.Src = &src
	}
	for _, p := range t.Parts {
		s.Parts = append(s.Parts, storedPart{FormatID: p.FormatID, Bytes: p.Bytes, Audio: p.IsAudio})
	}
	if t.Err != nil {
		s.Error = &storedError{Code: t.Err.Code, Details: t.Err.Details, Action: t.Err.Action, Retry: t.Err.Retry,
			Site: t.Err.Site, Need: t.Err.Need, Free: t.Err.Free, Folder: t.Err.Folder}
	}
	if t.State == stateDone {
		s.Path, s.FileSize = t.Path, t.FileSize
	}
	return s
}

func taskFromStore(s storedTask) *DownloadTask {
	t := &DownloadTask{ID: s.ID, Type: s.Type, URL: s.URL, Site: s.Site, Index: s.Index, Title: s.Title, Channel: s.Channel,
		DurationText: s.DurationText, Key: s.Key, Kind: s.Kind, Label: s.Label, SizeBytes: s.SizeBytes,
		FormatSpec: s.FormatSpec, SortSpec: s.SortSpec, AudioQuality: s.AudioQuality, Base: s.Base, SaveDir: s.SaveDir,
		FetchedAt: s.FetchedAt, CreatedAt: s.CreatedAt, QueuedAt: s.QueuedAt, FinishedAt: s.FinishedAt, State: s.State,
		Percent: s.Percent, Done: s.Done, Total: s.Total, PartsCount: s.PartsCount, PartIndex: s.PartIndex,
		Path: s.Path, FileSize: s.FileSize, ETA: -1, Category: s.Category, Page: s.Page, FileName: s.FileName,
		Resumable: s.Resumable, NoMusic: s.NoMusic, SrcPath: s.SrcPath, List: s.List, ClipStart: s.ClipStart, ClipEnd: s.ClipEnd}
	if s.Src != nil {
		t.Src = *s.Src
	}
	for _, p := range s.Parts {
		t.Parts = append(t.Parts, dlPart{FormatID: p.FormatID, Bytes: p.Bytes, IsAudio: p.Audio})
	}
	if e := s.Error; e != nil {
		ue := NewUserError(e.Code)
		ue.Details, ue.Action, ue.Retry, ue.Site, ue.Need, ue.Free, ue.Folder = e.Details, e.Action, e.Retry, e.Site, e.Need, e.Free, e.Folder
		if ue.Site == "" {
			ue.Site = t.Site
		}
		t.Err = ue
	}
	return t
}

func (t *DownloadTask) valid() bool {
	if t.ID == "" || t.Kind == "" || t.Base == "" {
		return false
	}
	switch t.Type {
	case "":
		if t.URL == "" {
			return false
		}
	case typeFile:
		if t.URL == "" || t.Src.URL == "" {
			return false
		}
	case typeSep:
		if t.SrcPath == "" {
			return false
		}
	default:
		return false // made by a newer HELINK
	}
	switch t.State {
	case stateQueued, stateRunning, statePaused, stateError, stateDone:
		return true
	}
	return false
}

// taskStore is the downloads list on disk (downloads.json in the app's folder), plus the
// pictures shown next to each entry (one small file each, so the list stays light).
// Written to a temporary file and renamed, so a crash never leaves a half-written list.
type taskStore struct {
	file   string
	thumbs string

	mu      sync.Mutex
	written int64 // sequence of the newest text on disk
}

func newTaskStore(file, thumbs string) *taskStore { return &taskStore{file: file, thumbs: thumbs} }

func (s *taskStore) load() []*DownloadTask {
	b, err := os.ReadFile(s.file)
	if err != nil {
		return nil
	}
	var l storedList
	if err := json.Unmarshal(b, &l); err != nil {
		log.Printf("downloads list unreadable, starting fresh: %v", err)
		return nil
	}
	var out []*DownloadTask
	for _, st := range l.Tasks {
		t := taskFromStore(st)
		if !t.valid() {
			continue
		}
		t.thumb = s.readThumb(t.ID)
		out = append(out, t)
	}
	return out
}

// serialize is called under the app's lock (fast); write happens outside it.
func serializeTasks(tasks []*DownloadTask) []byte {
	l := storedList{Version: 1, Tasks: []storedTask{}}
	for _, t := range tasks {
		l.Tasks = append(l.Tasks, t.toStore())
	}
	b, _ := json.Marshal(l)
	return b
}

// write saves the list; an older text that arrives late never replaces a newer one.
func (s *taskStore) write(text []byte, seq int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if seq < s.written {
		return
	}
	s.written = seq
	_ = os.MkdirAll(filepath.Dir(s.file), 0o755)
	tmp := s.file + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		log.Printf("saving the downloads list: %v", err)
		return
	}
	_, err = f.Write(text)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		log.Printf("saving the downloads list: %v", err)
		return
	}
	for i := 0; ; i++ {
		if err = os.Rename(tmp, s.file); err == nil || i >= 10 {
			break
		}
		// An antivirus may hold the file for a moment.
		time.Sleep(time.Duration(50+50*i) * time.Millisecond)
	}
	if err != nil {
		log.Printf("cannot save the downloads list: %v", err)
	}
}

func (s *taskStore) thumbFile(id string) string { return filepath.Join(s.thumbs, id+".txt") }

func (s *taskStore) writeThumb(id, thumb string) {
	if thumb == "" || len(thumb) > 6<<20 {
		return
	}
	_ = os.MkdirAll(s.thumbs, 0o755)
	if err := os.WriteFile(s.thumbFile(id), []byte(thumb), 0o644); err != nil {
		log.Printf("picture of %s: %v", id, err)
	}
}

func (s *taskStore) readThumb(id string) string {
	b, err := os.ReadFile(s.thumbFile(id))
	if err != nil {
		return ""
	}
	return string(b)
}

func (s *taskStore) deleteThumb(id string) { _ = os.Remove(s.thumbFile(id)) }

// pruneThumbs removes pictures of entries that are gone.
func (s *taskStore) pruneThumbs(ids map[string]bool) {
	entries, _ := os.ReadDir(s.thumbs)
	for _, e := range entries {
		n := e.Name()
		if strings.HasSuffix(n, ".txt") && !ids[strings.TrimSuffix(n, ".txt")] {
			_ = os.Remove(filepath.Join(s.thumbs, n))
		}
	}
}
