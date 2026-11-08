package main

// Removing the music from a recording: a download with "without music" turned on (the
// video or the MP3 is saved without its music), a finished download of the list, or files
// chosen on the computer (a copy "name (without music)" is made next to each one; the file
// itself stays as it is).
//
// ffmpeg decodes the sound to 44.1 kHz stereo, the music remover (mdxremover.go) keeps
// the voices, and ffmpeg writes them in the file's own format: a video keeps its picture
// as it is (copied, not encoded again), its subtitles and chapters; an MP3 its cover and
// its tags. One file at a time and below normal priority, as it takes all the cores. The
// first time, the remover itself is downloaded (about 64 MB, septool.go).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Stages of removing the music, shared with the page.
const (
	stageSepWait  = "sepwait"  // waiting while another file's music is removed
	stageSepModel = "sepmodel" // downloading the music remover (the first time only)
	stageSeparate = "separate" // removing the music
)

// noMusicSuffix ends the name of a file without its music.
func noMusicSuffix() string { return tr(" (بدون موسيقى)", " (no music)") }

// ffFile names a file for ffmpeg: without "file:", a name like "a:b.mp3" would be taken
// for a protocol.
func ffFile(p string) string { return "file:" + p }

// ---------------------------------------------------------------------------
// What a file holds
// ---------------------------------------------------------------------------

type mediaProbe struct {
	format        string  // ffmpeg's names of the container ("mov,mp4,m4a,3gp,3g2,mj2", "mp3"…)
	duration      float64 // seconds, 0 if unknown
	start         float64 // when the file's first stream starts
	video         bool    // a moving picture (not only a cover)
	cover         int     // the stream of a cover picture (JPEG or PNG), -1 if none
	audio         bool
	acodec        string
	abitrate      int64 // bits per second of the sound, 0 if unknown
	channels      int
	astart        float64
	title         string // the title tag, "" if none
	titleOfStream bool   // it is a tag of the sound (Ogg) rather than of the file
	subs          bool
}

type ffprobeJSON struct {
	Streams []struct {
		Index       int               `json:"index"`
		CodecType   string            `json:"codec_type"`
		CodecName   string            `json:"codec_name"`
		BitRate     string            `json:"bit_rate"`
		Channels    int               `json:"channels"`
		StartTime   string            `json:"start_time"`
		Duration    string            `json:"duration"`
		Disposition map[string]int    `json:"disposition"`
		Tags        map[string]string `json:"tags"`
	} `json:"streams"`
	Format struct {
		FormatName string            `json:"format_name"`
		Duration   string            `json:"duration"`
		StartTime  string            `json:"start_time"`
		BitRate    string            `json:"bit_rate"`
		Tags       map[string]string `json:"tags"`
	} `json:"format"`
}

func tagOf(tags map[string]string, name string) string {
	for k, v := range tags {
		if strings.EqualFold(k, name) {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func numOf(s string) float64 {
	v, ok := parseNum(s)
	if !ok {
		return 0
	}
	return v
}

func parseProbe(b []byte) (*mediaProbe, error) {
	var j ffprobeJSON
	if err := json.Unmarshal(b, &j); err != nil {
		return nil, err
	}
	p := &mediaProbe{format: j.Format.FormatName, cover: -1, start: numOf(j.Format.StartTime)}
	for _, s := range j.Streams {
		switch s.CodecType {
		case "video":
			if s.Disposition["attached_pic"] == 1 || s.Disposition["timed_thumbnails"] == 1 {
				if p.cover < 0 && (s.CodecName == "mjpeg" || s.CodecName == "png") {
					p.cover = s.Index
				}
				continue
			}
			p.video = true
		case "audio":
			if p.audio {
				continue // the first sound track is the one used
			}
			p.audio, p.acodec, p.channels = true, s.CodecName, s.Channels
			p.abitrate = int64(numOf(s.BitRate))
			p.astart = numOf(s.StartTime)
			p.duration = numOf(s.Duration)
			if t := tagOf(s.Tags, "title"); t != "" {
				p.title, p.titleOfStream = t, true
			}
		case "subtitle":
			p.subs = true
		}
	}
	if p.duration <= 0 {
		p.duration = numOf(j.Format.Duration)
	}
	if t := tagOf(j.Format.Tags, "title"); t != "" {
		p.title, p.titleOfStream = t, false
	}
	if p.audio && p.abitrate <= 0 && !p.video {
		p.abitrate = int64(numOf(j.Format.BitRate)) // a sound file: its own rate
	}
	return p, nil
}

func probeMedia(ctx context.Context, tools *Tools, path string) (*mediaProbe, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	res, err := runTool(ctx, tools.FfprobeExe(), []string{"-v", "error", "-hide_banner", "-print_format", "json",
		"-show_format", "-show_streams", "-i", ffFile(path)}, tools.Env(), true, nil)
	if err != nil {
		if errors.Is(err, errCancelled) {
			return nil, err
		}
		return nil, fmt.Errorf("%v: %s", err, tailLines(res.Log, 8))
	}
	return parseProbe(res.Stdout)
}

// ---------------------------------------------------------------------------
// The result's format
// ---------------------------------------------------------------------------

// sepPlan is how the result is written.
type sepPlan struct {
	video         bool
	ext           string   // of the result
	muxer         string   // ffmpeg's name of its container
	codec         []string // how its sound is encoded
	rate          int64    // about how many bits per second the sound takes (for the space it needs)
	mono          bool
	cover         bool    // keep the cover picture (sound files)
	subs          bool    // copy the subtitles (a video in the same kind of file)
	attach        bool    // copy the attached fonts (Matroska)
	title         string  // the new title tag ("" for none)
	titleOfStream bool    //
	offset        float64 // a video's sound starts this many seconds after its picture
}

func clampRate(br, lo, hi, def int64) int64 {
	if br <= 0 {
		return def
	}
	return max(lo, min(hi, br))
}

func kbps(br int64) string { return strconv.FormatInt((br+500)/1000, 10) + "k" }

// mp3Rate is the MP3 rate for a sound of br bits per second: the standard one at or just
// above it.
func mp3Rate(br int64) int64 {
	r := clampRate(br, 64000, 320000, 192000)
	for _, s := range []int64{64, 80, 96, 112, 128, 160, 192, 224, 256, 320} {
		if s*1000*103/100 >= r {
			return s * 1000
		}
	}
	return 320000
}

func (pl *sepPlan) useMP3(br int64) {
	pl.rate = mp3Rate(br)
	pl.codec = []string{"-c:a", "libmp3lame", "-b:a", kbps(pl.rate)}
}

func (pl *sepPlan) useAAC(br int64) {
	pl.rate = clampRate(br, 128000, 256000, 160000)
	pl.codec = []string{"-c:a", "aac", "-b:a", kbps(pl.rate)}
}

func (pl *sepPlan) useOpus(br int64) {
	pl.rate = clampRate(br, 64000, 256000, 128000)
	pl.codec = []string{"-c:a", "libopus", "-b:a", kbps(pl.rate)}
}

func (pl *sepPlan) useVorbis(br int64) {
	pl.rate = clampRate(br, 64000, 320000, 160000)
	pl.codec = []string{"-c:a", "libvorbis", "-b:a", kbps(pl.rate)}
}

func (pl *sepPlan) useLossless(codec string, rate int64) {
	pl.codec, pl.rate = []string{"-c:a", codec}, rate
}

// sameFamily keeps the kind of sound of a Matroska file when it can.
func (pl *sepPlan) sameFamily(p *mediaProbe) {
	switch p.acodec {
	case "opus":
		pl.useOpus(p.abitrate)
	case "vorbis":
		pl.useVorbis(p.abitrate)
	case "flac":
		pl.useLossless("flac", 900000)
	default:
		pl.useAAC(p.abitrate)
	}
}

// planFor chooses the result's format: the file's own (by its extension), MP3 for kinds
// ffmpeg cannot write well, and MP3 at mp3Rate ("192K") for a download in MP3.
func planFor(p *mediaProbe, ext, mp3 string) sepPlan {
	ext = strings.ToLower(ext)
	pl := sepPlan{ext: ext, mono: p.channels == 1}
	if p.title != "" && !strings.HasSuffix(p.title, strings.TrimSpace(noMusicSuffix())) {
		pl.title, pl.titleOfStream = p.title+noMusicSuffix(), p.titleOfStream
	}
	if mp3 != "" {
		rate, _ := strconv.ParseInt(strings.TrimSuffix(strings.ToUpper(mp3), "K"), 10, 64)
		pl.ext, pl.muxer = "mp3", "mp3"
		pl.useMP3(rate * 1000)
		pl.cover = true
		pl.titleOfStream = false
		return pl
	}
	if p.video {
		pl.video = true
		if off := p.astart - p.start; off > 0.005 && off < 60 {
			pl.offset = off
		}
		switch ext {
		case "mp4", "m4v":
			pl.muxer, pl.subs = "mp4", true
			pl.useAAC(p.abitrate)
		case "mov":
			pl.muxer, pl.subs = "mov", true
			pl.useAAC(p.abitrate)
		case "mkv":
			pl.muxer, pl.subs, pl.attach = "matroska", true, true
			pl.sameFamily(p)
		case "webm":
			pl.muxer, pl.subs = "webm", true
			pl.useOpus(p.abitrate)
		case "avi":
			pl.muxer = "avi"
			pl.useMP3(p.abitrate)
		default:
			pl.ext, pl.muxer, pl.subs = "mkv", "matroska", true
			pl.sameFamily(p)
		}
		pl.titleOfStream = false
		return pl
	}
	switch ext {
	case "mp3":
		pl.muxer, pl.cover = "mp3", true
		pl.useMP3(p.abitrate)
	case "m4a", "m4b":
		pl.muxer, pl.cover = "ipod", true
		pl.useAAC(p.abitrate)
	case "mp4", "m4v", "mov", "3gp":
		pl.ext, pl.muxer, pl.cover = "m4a", "ipod", true
		pl.useAAC(p.abitrate)
	case "aac":
		pl.muxer = "adts"
		pl.useAAC(p.abitrate)
	case "ogg", "oga", "opus":
		pl.muxer = "ogg"
		if p.acodec == "opus" || ext == "opus" {
			pl.useOpus(p.abitrate)
		} else {
			pl.useVorbis(p.abitrate)
		}
	case "webm":
		pl.muxer = "webm"
		pl.useOpus(p.abitrate)
	case "mka":
		pl.muxer = "matroska"
		pl.sameFamily(p)
	case "flac":
		pl.muxer, pl.cover = "flac", true
		pl.useLossless("flac", 900000)
	case "wav":
		pl.muxer = "wav"
		pl.useLossless("pcm_s16le", 1411200)
	case "aif", "aiff", "aifc":
		pl.ext, pl.muxer = "aiff", "aiff"
		pl.useLossless("pcm_s16be", 1411200)
	default: // wma, ape, amr, ac3…
		pl.ext, pl.muxer, pl.cover = "mp3", "mp3", true
		pl.useMP3(p.abitrate)
	}
	if pl.muxer != "ogg" && pl.muxer != "matroska" && pl.muxer != "webm" {
		pl.titleOfStream = false
	}
	return pl
}

// encodeArgs: ffmpeg's options to write the voices that come on its input. A video's
// sound is written on its own (into dst, a Matroska file) and joined with the picture
// afterwards.
func encodeArgs(p *mediaProbe, pl sepPlan, src, dst string) []string {
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error",
		"-f", "f32le", "-ar", strconv.Itoa(mrRate), "-ac", "2", "-i", "pipe:0"}
	if pl.video {
		args = append(args, "-map", "0:a:0")
		args = append(args, pl.codec...)
		if pl.mono {
			args = append(args, "-ac", "1")
		}
		return append(args, "-f", "matroska", "-y", ffFile(dst))
	}
	args = append(args, "-i", ffFile(src), "-map", "0:a:0")
	if pl.cover && p.cover >= 0 {
		args = append(args, "-map", "1:"+strconv.Itoa(p.cover), "-c:v", "copy", "-disposition:v:0", "attached_pic")
	}
	args = append(args, pl.codec...)
	if pl.mono {
		args = append(args, "-ac", "1")
	}
	args = append(args, "-map_metadata", "1", "-map_metadata:s:a:0", "1:s:a:0", "-map_chapters", "1")
	if pl.title != "" {
		if pl.titleOfStream {
			args = append(args, "-metadata:s:a:0", "title="+pl.title)
		} else {
			args = append(args, "-metadata", "title="+pl.title)
		}
	}
	if pl.muxer == "mp3" {
		args = append(args, "-id3v2_version", "3")
	}
	return append(args, "-f", pl.muxer, "-y", ffFile(dst))
}

// muxArgs: ffmpeg's options to join a video's picture (and subtitles, chapters, tags) with
// its new sound, both copied as they are.
func muxArgs(pl sepPlan, src, voice, dst string) []string {
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-progress", "pipe:1", "-nostats",
		"-i", ffFile(src)}
	if pl.offset > 0 {
		args = append(args, "-itsoffset", strconv.FormatFloat(pl.offset, 'f', 3, 64))
	}
	args = append(args, "-i", ffFile(voice), "-map", "0:V", "-map", "1:a:0")
	if pl.subs {
		args = append(args, "-map", "0:s?")
	}
	if pl.attach {
		args = append(args, "-map", "0:t?")
	}
	args = append(args, "-c", "copy", "-map_metadata", "0", "-map_metadata:s:a:0", "0:s:a:0", "-map_chapters", "0",
		"-disposition:a:0", "default")
	if pl.title != "" {
		args = append(args, "-metadata", "title="+pl.title)
	}
	return append(args, "-f", pl.muxer, "-y", ffFile(dst))
}

// spaceNeeded is about how much the result takes while it is made (a video: its copy and
// its sound on its own).
func spaceNeeded(p *mediaProbe, pl sepPlan, srcSize int64) int64 {
	if p.duration <= 0 {
		return 0
	}
	rate := pl.rate
	if pl.mono {
		rate = rate * 6 / 10
	}
	sound := int64(p.duration*float64(rate)/8) + 1<<20
	need := sound + 20<<20
	if pl.video {
		need += srcSize + sound
	}
	return need
}

// ---------------------------------------------------------------------------
// Running ffmpeg on a pipe
// ---------------------------------------------------------------------------

// ffRun is one ffmpeg of the pipeline, stopped with its children.
type ffRun struct {
	cmd     *exec.Cmd
	tree    *processTree
	log     *lineRing
	stdin   io.WriteCloser
	stdout  io.ReadCloser
	errDone chan struct{}
	killed  atomic.Bool
}

func startFF(exe string, args, env []string, wantIn, wantOut bool) (*ffRun, error) {
	cmd := exec.Command(exe, args...)
	cmd.Env = env
	prepareCmd(cmd)
	r := &ffRun{cmd: cmd, log: &lineRing{max: 40}, errDone: make(chan struct{})}
	var err error
	if wantIn {
		if r.stdin, err = cmd.StdinPipe(); err != nil {
			return nil, err
		}
	}
	if wantOut {
		if r.stdout, err = cmd.StdoutPipe(); err != nil {
			return nil, err
		}
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	r.tree = attachProcessTree(cmd)
	go func() {
		defer close(r.errDone)
		buf := make([]byte, 4096)
		var line []byte
		for {
			n, err := stderr.Read(buf)
			for _, c := range buf[:n] {
				if c == '\n' || c == '\r' {
					if l := strings.TrimSpace(string(line)); l != "" {
						r.log.add(l)
					}
					line = line[:0]
				} else if len(line) < 4000 {
					line = append(line, c)
				}
			}
			if err != nil {
				if l := strings.TrimSpace(string(line)); l != "" {
					r.log.add(l)
				}
				return
			}
		}
	}()
	return r, nil
}

func (r *ffRun) kill() {
	r.killed.Store(true)
	r.tree.kill()
}

func (r *ffRun) wait() error {
	<-r.errDone
	err := r.cmd.Wait()
	r.tree.release()
	return err
}

// readAhead reads a few MB ahead of the music remover, so ffmpeg decodes the next part
// while the remover works on this one.
type readAhead struct {
	ch   chan []byte
	cur  []byte
	err  error
	quit chan struct{}
	done chan struct{}
	once sync.Once
}

func newReadAhead(r io.Reader, chunks, size int) *readAhead {
	ra := &readAhead{ch: make(chan []byte, chunks), quit: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(ra.done)
		defer close(ra.ch)
		for {
			// Whole chunks (a pipe gives a few KB at a time).
			b := make([]byte, size)
			n, err := io.ReadFull(r, b)
			if n > 0 {
				select {
				case ra.ch <- b[:n]:
				case <-ra.quit:
					return
				}
			}
			if err != nil {
				if err != io.EOF && err != io.ErrUnexpectedEOF {
					ra.err = err
				}
				return
			}
		}
	}()
	return ra
}

func (ra *readAhead) Read(p []byte) (int, error) {
	for len(ra.cur) == 0 {
		b, ok := <-ra.ch
		if !ok {
			<-ra.done
			if ra.err != nil {
				return 0, ra.err
			}
			return 0, io.EOF
		}
		ra.cur = b
	}
	n := copy(p, ra.cur)
	ra.cur = ra.cur[n:]
	return n, nil
}

// stop ends reading (the source is killed or done).
func (ra *readAhead) stop() {
	ra.once.Do(func() { close(ra.quit) })
	<-ra.done
}

// writeBehind lets ffmpeg encode the voices while the remover works on the next part.
type writeBehind struct {
	ch   chan []byte
	done chan struct{}
	w    io.WriteCloser
	mu   sync.Mutex
	err  error
}

func newWriteBehind(w io.WriteCloser, chunks int) *writeBehind {
	wb := &writeBehind{ch: make(chan []byte, chunks), done: make(chan struct{}), w: w}
	go func() {
		defer close(wb.done)
		for b := range wb.ch {
			if wb.failed() != nil {
				continue // ffmpeg stopped: let the remover find out, and drop the rest
			}
			if _, err := w.Write(b); err != nil {
				wb.mu.Lock()
				wb.err = err
				wb.mu.Unlock()
			}
		}
	}()
	return wb
}

func (wb *writeBehind) failed() error {
	wb.mu.Lock()
	defer wb.mu.Unlock()
	return wb.err
}

func (wb *writeBehind) Write(p []byte) (int, error) {
	if err := wb.failed(); err != nil {
		return 0, err
	}
	wb.ch <- append([]byte(nil), p...)
	return len(p), nil
}

// Close waits for what is left to be written and ends ffmpeg's input.
func (wb *writeBehind) Close() error {
	close(wb.ch)
	<-wb.done
	_ = wb.w.Close()
	return wb.failed()
}

// ffError puts what ffmpeg said in the user's words: no space, no permission, or code.
func ffError(code, logText, folder string) *UserError {
	l := strings.ToLower(logText)
	switch {
	case isNoSpace(errors.New(l)) || strings.Contains(l, "errno 28") || strings.Contains(l, "disk full"):
		code = "disk"
	case strings.Contains(l, "permission denied") || strings.Contains(l, "access is denied"):
		code = "permission"
	}
	e := NewUserError(code)
	e.Details = tailLines(logText, 15)
	if code == "disk" || code == "permission" {
		e.Folder = folder
	}
	return e
}

// separateStream decodes src's sound, removes the music and writes the result into dst
// (pl), giving the samples done so far to progress. It returns the samples written.
func (a *App) separateStream(ctx context.Context, mr *mdxRemover, src string, p *mediaProbe, pl sepPlan, dst, folder string,
	progress func(int64)) (int64, *UserError) {
	ff, env := a.tools.FfmpegExe(), a.tools.Env()
	dec, err := startFF(ff, []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-i", ffFile(src),
		"-map", "0:a:0", "-vn", "-sn", "-dn", "-f", "f32le", "-acodec", "pcm_f32le",
		"-ar", strconv.Itoa(mrRate), "-ac", "2", "pipe:1"}, env, false, true)
	if err != nil {
		e := NewUserError("ffmpeg")
		e.Details = err.Error()
		return 0, e
	}
	enc, err := startFF(ff, encodeArgs(p, pl, src, dst), env, true, false)
	if err != nil {
		dec.kill()
		_ = dec.wait()
		e := NewUserError("ffmpeg")
		e.Details = err.Error()
		return 0, e
	}
	stop := context.AfterFunc(ctx, func() {
		dec.kill()
		enc.kill()
	})
	defer stop()
	in := newReadAhead(dec.stdout, 64, 1<<16)
	out := newWriteBehind(enc.stdin, 64)
	n, rerr := mr.run(ctx, in, out, progress)
	werr := out.Close()
	if rerr != nil || werr != nil {
		dec.kill() // the rest is not wanted
	}
	in.stop()
	decErr := dec.wait()
	if rerr != nil {
		enc.kill() // half a result
	}
	encErr := enc.wait()
	switch {
	case ctx.Err() != nil:
		return n, nil
	case rerr != nil && errors.Is(rerr, errCutSample):
		e := NewUserError("sep_unreadable")
		e.Details = tailLines(dec.log.String(), 10)
		return n, e
	case decErr != nil && !dec.killed.Load():
		log.Printf("decoding %s: %v: %s", src, decErr, dec.log.String())
		e := NewUserError("sep_unreadable")
		e.Details = tailLines(dec.log.String(), 10)
		return n, e
	case rerr != nil || werr != nil || encErr != nil:
		log.Printf("writing %s: %v %v %v: %s", dst, rerr, werr, encErr, enc.log.String())
		text := enc.log.String()
		if text == "" {
			text = fmt.Sprint(rerr, werr, encErr)
		}
		return n, ffError("sep_failed", text, folder)
	case n == 0:
		e := NewUserError("sep_unreadable")
		e.Details = "no sound decoded\n" + tailLines(dec.log.String(), 10)
		return n, e
	}
	return n, nil
}

// ---------------------------------------------------------------------------
// One file
// ---------------------------------------------------------------------------

// sepJob is a file whose music is removed.
type sepJob struct {
	src     string // the file
	work    string // the folder the result is made in (removed with the task's temporary folder)
	base    string // the result's name, without extension
	saveDir string // where the result goes afterwards (for the space it needs)
	mp3     string // "192K": the result is an MP3 at that rate (a download in MP3)
}

// sepProgress shows the stage and its share done (eta in seconds, -1 if unknown).
func (a *App) sepProgress(t *DownloadTask, stage string, percent, eta float64) {
	a.mu.Lock()
	if t.cancel == nil {
		a.mu.Unlock()
		return
	}
	t.Stage, t.Percent, t.ETA, t.Speed = stage, math.Max(0, math.Min(100, percent)), eta, 0
	t.Done, t.Total, t.PartsCount, t.PartIndex = 0, 0, 0, 0
	a.mu.Unlock()
	a.emitTask("update", t, false)
	a.saveTasks(false)
}

// sepTurn waits until no other file's music is being removed. The task no longer takes a
// download's place meanwhile.
func (a *App) sepTurn(ctx context.Context, t *DownloadTask) (release func(), err error) {
	a.mu.Lock()
	t.sepPhase = true
	a.mu.Unlock()
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		a.schedule()
	}()
	select {
	case a.sepSem <- struct{}{}:
	default:
		a.sepProgress(t, stageSepWait, 0, -1)
		select {
		case a.sepSem <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return func() { <-a.sepSem }, nil
}

// setupSep downloads the music remover, showing it as the task's stage.
func (a *App) setupSep(ctx context.Context, t *DownloadTask) *UserError {
	a.sepProgress(t, stageSepModel, 0, -1)
	var mu sync.Mutex
	var last time.Time
	var first int64 = -1
	start := time.Now()
	err := a.sep.install(ctx, func(done, total int64) {
		mu.Lock()
		defer mu.Unlock()
		if first < 0 {
			first = done // continued from an earlier try
		}
		if time.Since(last) < 250*time.Millisecond && done < total {
			return
		}
		last = time.Now()
		speed, eta := 0.0, -1.0
		if el := time.Since(start).Seconds(); el > 0.5 && done > first {
			speed = float64(done-first) / el
			eta = float64(total-done) / speed
		}
		a.mu.Lock()
		if t.cancel != nil {
			t.Stage, t.Speed, t.ETA = stageSepModel, speed, eta
			t.Done, t.Total = done, total
			if total > 0 {
				t.Percent = math.Min(100, float64(done)*100/float64(total))
			}
		}
		a.mu.Unlock()
		a.emitTask("update", t, false)
	})
	if ctx.Err() != nil {
		return nil
	}
	if err != nil {
		log.Printf("setting up the music remover: %v", err)
		code := "sep_setup"
		var de *DownloadError
		msg := strings.ToLower(err.Error())
		switch {
		case errors.As(err, &de) && de.Code == "disk", isNoSpace(err):
			code = "sep_disk"
		case strings.Contains(msg, "x509") || strings.Contains(msg, "certificate"):
			code = "clock"
		}
		e := NewUserError(code)
		e.Details = err.Error()
		return e
	}
	a.emit.Emit("sep", map[string]any{"ready": true})
	return nil
}

// removeMusic makes, in j.work, a copy of j.src without its music, named j.base and the
// format's extension, and returns its path. It waits its turn, and sets the remover up the
// first time. (path "" and no error: stopped.)
func (a *App) removeMusic(ctx context.Context, t *DownloadTask, j sepJob) (string, *UserError) {
	if !a.tools.devMode && (!fileOK(a.tools.FfmpegExe()) || !fileOK(a.tools.FfprobeExe())) {
		return "", NewUserError("tools_missing")
	}
	fi, err := os.Stat(j.src)
	if err != nil || fi.IsDir() {
		e := NewUserError("file_missing")
		if err != nil {
			e.Details = err.Error()
		}
		return "", e
	}
	p, err := probeMedia(ctx, a.tools, j.src)
	if ctx.Err() != nil {
		return "", nil
	}
	if err != nil {
		log.Printf("reading %s: %v", j.src, err)
		e := NewUserError("sep_unreadable")
		e.Details = err.Error()
		return "", e
	}
	if !p.audio {
		return "", NewUserError("sep_no_audio")
	}
	pl := planFor(p, extOf(j.src), j.mp3)
	a.mu.Lock()
	if t.isSep() {
		// What the file turned out to be.
		t.Kind, t.Category = "audio", catMusic
		if pl.video {
			t.Kind, t.Category = "video", catVideo
		}
	}
	a.mu.Unlock()
	if need := spaceNeeded(p, pl, fi.Size()); need > 0 {
		if free, ok := freeSpace(j.saveDir); ok && free < need {
			e := NewUserError("disk")
			e.Need, e.Free, e.Folder = need, free, j.saveDir
			return "", e
		}
	}
	if err := os.MkdirAll(j.work, 0o755); err != nil {
		e := NewUserError("permission")
		e.Details, e.Folder = err.Error(), j.saveDir
		return "", e
	}

	release, err := a.sepTurn(ctx, t)
	if err != nil {
		return "", nil
	}
	defer func() {
		release()
		a.sep.drop()
		debug.FreeOSMemory() // about 100 MB, back to Windows
	}()
	if !a.sep.ready() {
		if e := a.setupSep(ctx, t); e != nil || ctx.Err() != nil {
			return "", e
		}
	}
	model, err := a.sep.load()
	if err != nil {
		log.Printf("music remover: %v", err)
		e := NewUserError("sep_setup")
		e.Details = err.Error()
		return "", e
	}
	return a.separate(ctx, t, j, p, pl, model)
}

func (a *App) separate(ctx context.Context, t *DownloadTask, j sepJob, p *mediaProbe, pl sepPlan, model *sepModel) (string, *UserError) {
	out := filepath.Join(j.work, j.base+"."+pl.ext)
	sound := out
	if pl.video {
		sound = filepath.Join(j.work, "voice.mka")
	}
	_ = os.Remove(out)
	_ = os.Remove(sound)
	if sepThreadHook != nil {
		defer sepThreadHook()() // the chunks are read and written on this thread
	}
	net, err := loadMdxNet(model, sepSpec, sepThreads())
	if err != nil {
		log.Printf("music remover: %v", err)
		a.sep.forget()
		e := NewUserError("sep_setup")
		e.Details = err.Error()
		return "", e
	}
	mr := newMdxRemover(net, sepSpec)
	total := int64(math.Round(p.duration * mrRate))
	start := time.Now()
	var last time.Time
	a.sepProgress(t, stageSeparate, 0, -1)
	n, e := a.separateStream(ctx, mr, j.src, p, pl, sound, j.saveDir, func(done int64) {
		if total <= 0 || time.Since(last) < 250*time.Millisecond {
			return
		}
		last = time.Now()
		share := math.Min(0.995, float64(done)/float64(total))
		eta := -1.0
		if share > 0.01 {
			eta = time.Since(start).Seconds() * (1 - share) / share
		}
		a.sepProgress(t, stageSeparate, share*100, eta)
	})
	if ctx.Err() != nil || e != nil {
		_ = os.Remove(sound)
		return "", e
	}
	log.Printf("music removed from %s: %.1f s of sound in %.1f s (%s, %d threads)", filepath.Base(j.src),
		float64(n)/mrRate, time.Since(start).Seconds(), mdxKernel, sepThreads())
	if !pl.video {
		if !fileOK(out) {
			return "", NewUserError("sep_failed")
		}
		return out, nil
	}
	defer os.Remove(sound)
	return a.joinPicture(ctx, t, j, p, pl, sound)
}

// joinPicture puts the new sound with the video's picture (both copied). When the file's
// own kind refuses something (a subtitle, a kind of picture), it is tried without the
// subtitles, then as an MKV, which takes anything.
func (a *App) joinPicture(ctx context.Context, t *DownloadTask, j sepJob, p *mediaProbe, pl sepPlan, sound string) (string, *UserError) {
	tries := []sepPlan{pl}
	if pl.subs || pl.attach {
		bare := pl
		bare.subs, bare.attach = false, false
		tries = append(tries, bare)
	}
	if pl.muxer != "matroska" {
		mkv := pl
		mkv.ext, mkv.muxer, mkv.subs, mkv.attach = "mkv", "matroska", false, false
		tries = append(tries, mkv)
	}
	a.sepProgress(t, stageMerge, 0, -1)
	var lastLog string
	for _, try := range tries {
		dst := filepath.Join(j.work, j.base+"."+try.ext)
		_ = os.Remove(dst)
		var mu sync.Mutex
		var last time.Time
		res, err := runTool(ctx, a.tools.FfmpegExe(), muxArgs(try, j.src, sound, dst), a.tools.Env(), false, func(line string, isErr bool) {
			v, ok := strings.CutPrefix(line, "out_time_us=")
			if isErr || !ok || p.duration <= 0 {
				return
			}
			us, ok := parseNum(v)
			mu.Lock()
			defer mu.Unlock()
			if !ok || time.Since(last) < 250*time.Millisecond {
				return
			}
			last = time.Now()
			a.sepProgress(t, stageMerge, us/1e4/p.duration, -1)
		})
		if ctx.Err() != nil {
			_ = os.Remove(dst)
			return "", nil
		}
		if err == nil && fileOK(dst) {
			return dst, nil
		}
		_ = os.Remove(dst)
		log.Printf("joining %s as %s: %v: %s", filepath.Base(j.src), try.muxer, err, res.Log)
		lastLog = res.Log
		if e := ffError("sep_failed", res.Log, j.saveDir); e.Code != "sep_failed" {
			return "", e // no space, no permission: another kind of file would not help
		}
	}
	return "", ffError("sep_failed", lastLog, j.saveDir)
}
