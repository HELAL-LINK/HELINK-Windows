package main

import (
	"math"
	"strconv"
	"strings"
)

// yt-dlp is asked to print machine-readable lines with these prefixes.
const (
	tagDownload = "NZLP|"
	tagPost     = "NZLPP|"
	tagDone     = "NZLDONE|"
)

// Progress templates passed to yt-dlp; they produce the lines parsed below.
var (
	progressTemplateDownload = "download:" + tagDownload +
		"%(progress.status)s|%(progress.downloaded_bytes)s|%(progress.total_bytes)s|" +
		"%(progress.total_bytes_estimate)s|%(progress.speed)s|%(progress.eta)s|%(info.format_id)s"
	progressTemplatePost = "postprocess:" + tagPost + "%(progress.status)s|%(progress.postprocessor)s"
	printDoneTemplate    = "after_move:" + tagDone + "%(filepath)s"
)

type dlLine struct {
	Status     string
	Downloaded float64
	Total      float64 // exact or estimated, 0 if unknown
	Speed      float64
	ETA        float64 // -1 if unknown
	FormatID   string
}

func parseNum(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" || s == "NA" || s == "None" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

func parseDownloadLine(line string) (dlLine, bool) {
	if !strings.HasPrefix(line, tagDownload) {
		return dlLine{}, false
	}
	f := strings.Split(strings.TrimPrefix(line, tagDownload), "|")
	if len(f) < 7 {
		return dlLine{}, false
	}
	d := dlLine{Status: f[0], FormatID: strings.TrimSpace(strings.Join(f[6:], "|")), ETA: -1}
	d.Downloaded, _ = parseNum(f[1])
	if t, ok := parseNum(f[2]); ok {
		d.Total = t
	} else if t, ok := parseNum(f[3]); ok {
		d.Total = t
	}
	d.Speed, _ = parseNum(f[4])
	if e, ok := parseNum(f[5]); ok {
		d.ETA = e
	}
	return d, true
}

// parsePostLine returns ("started"|"finished", postprocessor name).
func parsePostLine(line string) (status, pp string, ok bool) {
	if !strings.HasPrefix(line, tagPost) {
		return "", "", false
	}
	f := strings.SplitN(strings.TrimPrefix(line, tagPost), "|", 2)
	if len(f) != 2 {
		return "", "", false
	}
	return strings.TrimSpace(f[0]), strings.TrimSpace(f[1]), true
}

func parseDoneLine(line string) (string, bool) {
	if !strings.HasPrefix(line, tagDone) {
		return "", false
	}
	p := strings.TrimSpace(strings.TrimPrefix(line, tagDone))
	return p, p != "" && p != "NA"
}

// Stage names shared with the UI.
const (
	stageVideo   = "video"   // downloading the picture (or a ready-made file)
	stageAudio   = "audio"   // downloading the sound
	stageMerge   = "merge"   // joining sound and picture
	stageConvert = "convert" // converting to MP3
	stageFinish  = "finish"  // other quick final steps
	stageSave    = "save"    // moving the file to its folder
)

func stageForPostprocessor(pp string) string {
	switch {
	case pp == "Merger":
		return stageMerge
	case pp == "ExtractAudio":
		return stageConvert
	case pp == "MoveFiles":
		return stageSave
	default:
		return stageFinish
	}
}

// Overall is the progress of a running download: numbers only; the words are written
// when the download is shown (see DownloadTask).
type Overall struct {
	Stage      string
	Percent    float64 // 0..100, -1 while a final step runs with no measurable progress
	Done       int64
	Total      int64
	Speed      float64 // bytes per second, 0 if unknown
	ETA        float64 // seconds left, -1 if unknown
	PartsCount int
	PartIndex  int // 1-based
}

type trackedPart struct {
	id       string
	isAudio  bool
	expected float64
	total    float64 // learned from yt-dlp
	done     float64
	finished bool
}

// ProgressTracker merges the per-file progress of yt-dlp into one bar.
type ProgressTracker struct {
	parts   []*trackedPart
	kind    string // "video" | "audio"
	current *trackedPart
	stage   string
	speed   float64
}

func NewProgressTracker(kind string, parts []dlPart) *ProgressTracker {
	t := &ProgressTracker{kind: kind, stage: stageVideo}
	if kind == "audio" {
		t.stage = stageAudio
	}
	for _, p := range parts {
		t.parts = append(t.parts, &trackedPart{id: p.FormatID, isAudio: p.IsAudio, expected: float64(p.Bytes)})
	}
	return t
}

func (t *ProgressTracker) part(id string) *trackedPart {
	for _, p := range t.parts {
		if p.id == id {
			return p
		}
	}
	// yt-dlp used a fallback format: take the first part that has not started yet.
	for _, p := range t.parts {
		if !p.finished && p.done == 0 && p != t.current {
			p.id = id
			return p
		}
	}
	p := &trackedPart{id: id}
	t.parts = append(t.parts, p)
	return p
}

// OnDownload feeds one parsed download line and returns the merged progress.
func (t *ProgressTracker) OnDownload(d dlLine) Overall {
	p := t.part(d.FormatID)
	t.current = p
	if d.Total > 0 {
		p.total = d.Total
	}
	if d.Downloaded > 0 {
		p.done = d.Downloaded
	}
	if d.Status == "finished" {
		p.finished = true
		if p.total == 0 {
			p.total = p.done
		}
		p.done = p.total
	}
	if d.Speed > 0 {
		t.speed = d.Speed
	}
	if t.kind == "audio" || p.isAudio {
		t.stage = stageAudio
	} else {
		t.stage = stageVideo
	}
	return t.snapshot()
}

// OnPost records the start of a post-processing step.
func (t *ProgressTracker) OnPost(status, pp string) Overall {
	if status == "started" {
		t.stage = stageForPostprocessor(pp)
	}
	o := t.snapshot()
	o.Percent = -1
	o.Speed, o.ETA = 0, -1
	return o
}

func (t *ProgressTracker) snapshot() Overall {
	var done, total float64
	idx := 0
	for i, p := range t.parts {
		size := p.total
		if size == 0 {
			size = p.expected
		}
		if size == 0 && p.done > 0 {
			size = p.done
		}
		total += size
		if p.finished {
			done += size
		} else {
			done += math.Min(p.done, size)
		}
		if p == t.current {
			idx = i + 1
		}
	}
	o := Overall{Stage: t.stage, PartsCount: len(t.parts), PartIndex: idx, ETA: -1}
	if total > 0 {
		o.Percent = math.Min(100, math.Max(0, done/total*100))
		o.Done, o.Total = int64(done), int64(total)
	}
	if t.speed > 0 {
		o.Speed = t.speed
		if total > done {
			o.ETA = (total - done) / t.speed
		}
	}
	return o
}
