//go:build !windows

package main

// Removing the music with the real ffmpeg (from PATH) and the real model:
//   HELINK_SEP_ARCHIVES=dir with Kim_Vocal_2.onnx (see mdx_test.go)
//   HELINK_SEP_REF=dir with mix.f32 (30 s of a song) and mix_ref_voice.f32 (its voices, by
//     the reference pipeline)

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type probed struct {
	Streams []struct {
		Index       int               `json:"index"`
		CodecName   string            `json:"codec_name"`
		CodecType   string            `json:"codec_type"`
		Channels    int               `json:"channels"`
		BitRate     string            `json:"bit_rate"`
		NbFrames    string            `json:"nb_frames"`
		Disposition map[string]int    `json:"disposition"`
		Tags        map[string]string `json:"tags"`
	} `json:"streams"`
	Format struct {
		Duration string            `json:"duration"`
		BitRate  string            `json:"bit_rate"`
		Tags     map[string]string `json:"tags"`
	} `json:"format"`
	Chapters []any `json:"chapters"`
}

func probeFile(t *testing.T, p string) probed {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error", "-print_format", "json", "-show_format", "-show_streams",
		"-show_chapters", "file:"+p).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", p, err)
	}
	var r probed
	json.Unmarshal(out, &r)
	return r
}

// codecs: "h264,aac,mov_text" (covers marked "png*").
func (p probed) codecs() string {
	var l []string
	for _, s := range p.Streams {
		c := s.CodecName
		if s.Disposition["attached_pic"] == 1 {
			c += "*"
		}
		l = append(l, c)
	}
	return strings.Join(l, ",")
}

func (p probed) duration() float64 { v, _ := parseNum(p.Format.Duration); return v }

func sha(t *testing.T, p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func ffmpegMake(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("ffmpeg", append([]string{"-nostdin", "-hide_banner", "-loglevel", "error", "-y"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg %v: %v\n%s", args, err, out)
	}
}

// sepMedia makes the test files from the reference mix (30 s of song) in dir.
func sepMedia(t *testing.T, dir, ref string) {
	t.Helper()
	mix := filepath.Join(ref, "mix.f32")
	raw := []string{"-f", "f32le", "-ar", "44100", "-ac", "2", "-i", mix}
	raw5 := []string{"-f", "f32le", "-ar", "44100", "-ac", "2", "-t", "5", "-i", mix}
	video := []string{"-f", "lavfi", "-i", "testsrc=s=320x240:r=25:d=5"}
	f := func(n string) string { return "file:" + filepath.Join(dir, n) }
	cover := filepath.Join(dir, "cover.png")
	ffmpegMake(t, "-f", "lavfi", "-i", "color=c=red:s=200x200:d=1", "-frames:v", "1", "file:"+cover)
	ffmpegMake(t, append(raw, "-i", "file:"+cover, "-map", "0:a", "-map", "1:v", "-c:a", "libmp3lame", "-b:a", "192k",
		"-c:v", "copy", "-disposition:v:0", "attached_pic", "-metadata", "title=أغنية تجربة", "-metadata", "artist=فنان",
		"-id3v2_version", "3", f("أغنية.mp3"))...)
	os.Remove(cover)
	ffmpegMake(t, append(raw, "-c:a", "pcm_f32le", f("ref.wav"))...)
	srt := filepath.Join(dir, "subs.srt")
	os.WriteFile(srt, []byte("1\n00:00:00,500 --> 00:00:02,000\nمرحبا\n\n2\n00:00:03,000 --> 00:00:04,500\nHello\n"), 0o644)
	meta := filepath.Join(dir, "meta.txt")
	os.WriteFile(meta, []byte(";FFMETADATA1\ntitle=فيلم\n[CHAPTER]\nTIMEBASE=1/1000\nSTART=0\nEND=2500\ntitle=One\n"+
		"[CHAPTER]\nTIMEBASE=1/1000\nSTART=2500\nEND=5000\ntitle=Two\n"), 0o644)
	ffmpegMake(t, append(append(video, raw5...), "-i", "file:"+srt, "-i", "file:"+meta, "-map", "0:v", "-map", "1:a", "-map", "2:s",
		"-map_metadata", "3", "-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac", "-b:a", "128k", "-c:s", "mov_text",
		"-metadata:s:a:0", "language=ara", f("درس.mp4"))...)
	ffmpegMake(t, append(append(video, raw5...), "-i", "file:"+srt, "-map", "0:v", "-map", "1:a", "-map", "2:s",
		"-c:v", "libx264", "-preset", "ultrafast", "-c:a", "libopus", "-b:a", "96k", "-c:s", "srt", f("clip.mkv"))...)
	os.Remove(srt)
	os.Remove(meta)
	ffmpegMake(t, append(append(video, raw5...), "-c:v", "libvpx", "-b:v", "300k", "-c:a", "libopus", f("clip.webm"))...)
	ffmpegMake(t, append(append(video, raw5...), "-c:v", "mpeg4", "-c:a", "libmp3lame", f("clip.avi"))...)
	ffmpegMake(t, append(append(video, raw5...), "-c:v", "flv1", "-c:a", "libmp3lame", f("old.flv"))...)
	ffmpegMake(t, append(raw5, "-ac", "1", "-c:a", "libvorbis", "-metadata", "title=حديث", f("حديث.ogg"))...)
	ffmpegMake(t, append(video, "-c:v", "libx264", "-preset", "ultrafast", f("silent.mp4"))...)
	os.WriteFile(filepath.Join(dir, "fake.mp3"), []byte(strings.Repeat("not a song\n", 500)), 0o644)
}

// sepFinal waits until every id is done or failed, checking meanwhile that the music is
// removed from one file at a time. It returns the last state of each.
func sepFinal(t *testing.T, em *recEmitter, ids []string, timeout time.Duration) (map[string]map[string]any, map[string]bool) {
	t.Helper()
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	final := map[string]map[string]any{}
	stages := map[string]bool{}
	busy := map[string]bool{} // removing the music right now
	deadline := time.Now().Add(timeout)
	for len(final) < len(want) {
		name, m := em.next(t, deadline)
		if name != "task" {
			continue
		}
		task, _ := m["task"].(map[string]any)
		id, _ := task["id"].(string)
		if task == nil || !want[id] {
			continue
		}
		st, _ := task["state"].(string)
		stage, _ := task["stage"].(string)
		if st == stateRunning {
			stages[stage] = true
		}
		if st == stateRunning && (stage == stageSeparate || stage == stageSepModel || stage == stageMerge) {
			busy[id] = true
		} else {
			delete(busy, id)
		}
		if len(busy) > 1 {
			t.Fatalf("the music is removed from %d files at once", len(busy))
		}
		if st == stateDone || st == stateError {
			final[id] = task
		}
	}
	return final, stages
}

func TestNoMusicFiles(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	ref, archives := os.Getenv("HELINK_SEP_REF"), os.Getenv("HELINK_SEP_ARCHIVES")
	if ref == "" || archives == "" {
		t.Skip("set HELINK_SEP_REF and HELINK_SEP_ARCHIVES")
	}
	defer setLang("ar")
	setLang("ar")
	tmp := t.TempDir()
	desk := filepath.Join(tmp, "Desktop", "ملفاتي")
	os.MkdirAll(desk, 0o755)
	sepMedia(t, desk, ref)
	before := map[string]string{}
	entries, _ := os.ReadDir(desk)
	var files []string
	for _, e := range entries {
		p := filepath.Join(desk, e.Name())
		before[p] = sha(t, p)
		files = append(files, p)
	}

	// The remover is downloaded the first time: from a local server here (the first
	// address is gone, as if it moved).
	srv := httptest.NewServer(http.FileServer(http.Dir(archives)))
	defer srv.Close()
	old := sepSources
	defer func() { sepSources = old }()
	sepSources = []sepSource{old[0]}
	sepSources[0].urls = []string{srv.URL + "/gone/" + old[0].file, srv.URL + "/" + old[0].file}
	// HELINK 2.1's remover is there: it goes.
	oldSep := filepath.Join(tmp, "data", "tools", "sep")
	os.MkdirAll(oldSep, 0o755)
	os.WriteFile(filepath.Join(oldSep, "spleeter-2stems.hlw"), []byte("old"), 0o644)

	t.Setenv("NAZZELHA_DEV_TOOLS", "1")
	t.Setenv("NAZZELHA_SAVE", filepath.Join(tmp, "Downloads"))
	em := newRec()
	app := NewApp(em, newPaths(filepath.Join(tmp, "data")))
	defer app.Shutdown()
	if r := app.Init(); r.SepReady || r.SepSizeText != "64 ميجابايت" {
		t.Fatalf("not set up yet: %v %q", r.SepReady, r.SepSizeText)
	}
	vanish := filepath.Join(desk, "vanish.mp3")
	copyFile(filepath.Join(desk, "أغنية.mp3"), vanish)
	r := app.AddSepFiles(append(append([]string{}, files...), vanish, desk, filepath.Join(desk, "nothing.mp3"), ""))
	ids, _ := r["ids"].([]string)
	if len(ids) != len(files)+1 || r["skipped"] != 3 {
		t.Fatalf("added %v", r)
	}
	os.Remove(vanish)
	// The same file again is the same entry.
	if again := app.AddSepFiles([]string{files[0]}); again["ids"].([]string)[0] != ids[0] {
		t.Errorf("added twice: %v", again)
	}
	start := time.Now()
	final, stages := sepFinal(t, em, ids, 4*time.Minute)
	t.Logf("%d files in %s", len(ids), time.Since(start).Round(time.Millisecond))
	if !stages[stageSepModel] || !stages[stageSeparate] || !stages[stageMerge] || !stages[stageSave] || stages[stageSepWait] {
		t.Errorf("stages %v", stages)
	}
	if r := app.Init(); !r.SepReady {
		t.Error("set up now")
	}
	if _, err := os.Stat(filepath.Join(oldSep, "spleeter-2stems.hlw")); err == nil {
		t.Error("the old remover is still there")
	}
	byName := map[string]map[string]any{}
	for i, id := range ids {
		name := "vanish.mp3"
		if i < len(files) {
			name = filepath.Base(files[i])
		}
		byName[name] = final[id]
	}
	result := func(name string) (string, probed) {
		t.Helper()
		task := byName[name]
		if task["state"] != stateDone {
			t.Fatalf("%s: %v %v", name, task["state"], task["error"])
		}
		res := task["result"].(map[string]any)
		p := res["path"].(string)
		if filepath.Dir(p) != desk {
			t.Errorf("%s saved in %s, not next to it", name, filepath.Dir(p))
		}
		if task["noMusic"] != true || task["type"] != typeSep {
			t.Errorf("%s entry %v", name, task)
		}
		return filepath.Base(p), probeFile(t, p)
	}
	near := func(a, b float64) bool { return math.Abs(a-b) < 0.15 }

	n, p := result("أغنية.mp3")
	if n != "أغنية (بدون موسيقى).mp3" || p.codecs() != "mp3,png*" || p.Format.Tags["title"] != "أغنية تجربة (بدون موسيقى)" ||
		p.Format.Tags["artist"] != "فنان" || !near(p.duration(), 30) || p.Streams[0].BitRate != "192000" {
		t.Errorf("mp3: %s %s %v %.2f %s", n, p.codecs(), p.Format.Tags, p.duration(), p.Streams[0].BitRate)
	}
	if byName["أغنية.mp3"]["kind"] != "audio" || byName["أغنية.mp3"]["category"] != catMusic {
		t.Errorf("mp3 kind %v", byName["أغنية.mp3"])
	}
	n, p = result("ref.wav")
	if n != "ref (بدون موسيقى).wav" || p.codecs() != "pcm_s16le" || p.Streams[0].Channels != 2 {
		t.Errorf("wav: %s %s", n, p.codecs())
	}
	// The voices themselves, against the reference (numpy + ONNX Runtime).
	out, err := exec.Command("ffmpeg", "-v", "error", "-i", "file:"+filepath.Join(desk, n), "-f", "f32le", "-").Output()
	if err != nil {
		t.Fatal(err)
	}
	got := make([]float32, len(out)/4)
	for i := range got {
		got[i] = math.Float32frombits(binary.LittleEndian.Uint32(out[4*i:]))
	}
	want := readF32(t, filepath.Join(ref, "mix_ref_voice.f32"))
	if len(got) != len(want) {
		t.Fatalf("voices: %d values, want %d", len(got), len(want))
	}
	if _, _, snr := compareF32(got, want); snr < 60 {
		t.Errorf("voices differ from the reference: %.1f dB", snr)
	} else {
		t.Logf("voices against the reference: %.1f dB", snr)
	}
	n, p = result("درس.mp4")
	if n != "درس (بدون موسيقى).mp4" || !strings.HasPrefix(p.codecs(), "h264,aac,mov_text") || p.Streams[0].NbFrames != "125" ||
		p.Streams[1].Tags["language"] != "ara" || len(p.Chapters) != 2 || !near(p.duration(), 5) || p.Format.Tags["title"] != "فيلم (بدون موسيقى)" {
		t.Errorf("mp4: %s %s frames %s %v chapters %d %.2f %v", n, p.codecs(), p.Streams[0].NbFrames, p.Streams[1].Tags,
			len(p.Chapters), p.duration(), p.Format.Tags)
	}
	if byName["درس.mp4"]["kind"] != "video" || byName["درس.mp4"]["label"] != "MP4" {
		t.Errorf("mp4 entry %v", byName["درس.mp4"])
	}
	if n, p = result("clip.mkv"); n != "clip (بدون موسيقى).mkv" || p.codecs() != "h264,opus,subrip" || !near(p.duration(), 5) {
		t.Errorf("mkv: %s %s %.2f", n, p.codecs(), p.duration())
	}
	if n, p = result("clip.webm"); n != "clip (بدون موسيقى).webm" || p.codecs() != "vp8,opus" {
		t.Errorf("webm: %s %s", n, p.codecs())
	}
	if n, p = result("clip.avi"); n != "clip (بدون موسيقى).avi" || p.codecs() != "mpeg4,mp3" {
		t.Errorf("avi: %s %s", n, p.codecs())
	}
	if n, p = result("old.flv"); n != "old (بدون موسيقى).mkv" || p.codecs() != "flv1,aac" || !near(p.duration(), 5) {
		t.Errorf("flv: %s %s", n, p.codecs())
	}
	if n, p = result("حديث.ogg"); n != "حديث (بدون موسيقى).ogg" || p.codecs() != "vorbis" || p.Streams[0].Channels != 1 ||
		p.Streams[0].Tags["title"]+p.Format.Tags["title"] != "حديث (بدون موسيقى)" {
		t.Errorf("ogg: %s %s %d %v %v", n, p.codecs(), p.Streams[0].Channels, p.Streams[0].Tags, p.Format.Tags)
	}
	for name, code := range map[string]string{"silent.mp4": "sep_no_audio", "fake.mp3": "sep_unreadable", "vanish.mp3": "file_missing"} {
		task := byName[name]
		e, _ := task["error"].(map[string]any)
		if task["state"] != stateError || e == nil || e["code"] != code {
			t.Errorf("%s: %v %v, want %s", name, task["state"], e, code)
		}
	}
	if e := byName["silent.mp4"]["error"].(map[string]any); e["retry"] != false || e["title"] != "لا يوجد صوت في هذا الملف" {
		t.Errorf("no sound: %v", e)
	}
	// The files themselves are as they were, and nothing is left behind.
	for p, s := range before {
		if sha(t, p) != s {
			t.Errorf("%s changed", p)
		}
	}
	if _, err := os.Stat(filepath.Join(desk, tempFolderName)); err == nil {
		t.Error("temporary folder left behind")
	}
	if fileOK(filepath.Join(tmp, "data", "tools", "sep", old[0].file)) {
		t.Error("the downloaded archive is kept")
	}
	if processesUsing(desk) {
		t.Error("ffmpeg still running")
	}
	// A finished download (here: one of the results) gives a copy next to it.
	r2 := app.RemoveMusicFrom(byName["أغنية.mp3"]["id"].(string))
	id2, _ := r2["id"].(string)
	if id2 == "" {
		t.Fatalf("from the list: %v", r2)
	}
	d := em.waitTask(t, id2, stateDone, time.Minute)
	if p := d["result"].(map[string]any)["path"].(string); filepath.Base(p) != "أغنية (بدون موسيقى) (بدون موسيقى).mp3" {
		t.Errorf("from the list: %s", p)
	}
	if r := app.RemoveMusicFrom("nobody"); r["error"] == nil {
		t.Error("no such entry")
	}
}

// Stopping: a pause keeps the entry (it starts over when resumed), a delete removes it and
// what it made; a file whose music is being removed does not hold up the downloads.
func TestNoMusicPauseDelete(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	ref := os.Getenv("HELINK_SEP_REF")
	if ref == "" || os.Getenv("HELINK_SEP_ARCHIVES") == "" {
		t.Skip("set HELINK_SEP_REF and HELINK_SEP_ARCHIVES")
	}
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "music")
	os.MkdirAll(dir, 0o755)
	long := filepath.Join(dir, "long.flac")
	// Two minutes of song, so it is still running when stopped.
	mix := filepath.Join(ref, "mix.f32")
	ffmpegMake(t, "-stream_loop", "3", "-f", "f32le", "-ar", "44100", "-ac", "2", "-i", mix, "-c:a", "flac", "file:"+long)
	other := filepath.Join(dir, "other.flac")
	copyFile(long, other)
	t.Setenv("NAZZELHA_DEV_TOOLS", "1")
	t.Setenv("NAZZELHA_SAVE", filepath.Join(tmp, "Downloads"))
	em := newRec()
	data := filepath.Join(tmp, "data")
	installTestModel(t, filepath.Join(data, "tools"))
	app := NewApp(em, newPaths(data))
	defer app.Shutdown()
	ids := app.AddSepFiles([]string{long, other})["ids"].([]string)
	waitStage := func(id, stage string) {
		t.Helper()
		deadline := time.Now().Add(time.Minute)
		for {
			task := em.waitTask(t, id, stateRunning, time.Until(deadline))
			if task["stage"] == stage && (stage != stageSeparate || task["percent"].(float64) > 0) {
				return
			}
		}
	}
	waitStage(ids[0], stageSeparate)
	if task := taskIn(app, ids[1]); task["state"] != stateQueued {
		t.Errorf("the second waits its turn: %v", task["state"])
	}
	app.Pause(ids[0])
	em.waitTask(t, ids[0], statePaused, 30*time.Second)
	// The second starts as soon as the first stopped; delete it in the middle.
	waitStage(ids[1], stageSeparate)
	app.Delete(ids[1])
	em.waitTask(t, ids[1], "removed", 30*time.Second)
	time.Sleep(300 * time.Millisecond)
	if processesUsing(dir) {
		t.Error("ffmpeg still running after delete")
	}
	app.Resume(ids[0])
	d := em.waitTask(t, ids[0], stateDone, 3*time.Minute)
	p := d["result"].(map[string]any)["path"].(string)
	if filepath.Base(p) != "long (بدون موسيقى).flac" {
		t.Errorf("result %s", p)
	}
	if v := probeFile(t, p); v.codecs() != "flac" || math.Abs(v.duration()-120) > 0.2 {
		t.Errorf("result %s %.2f", v.codecs(), v.duration())
	}
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if strings.Join(names, "|") != "long (بدون موسيقى).flac|long.flac|other.flac" {
		t.Errorf("folder: %v", names)
	}
	// Restarted after Windows was closed in the middle: it waits paused in the list.
	ids2 := app.AddSepFiles([]string{other})["ids"].([]string)
	waitStage(ids2[0], stageSeparate)
	app.Shutdown()
	app2 := NewApp(newRec(), newPaths(data))
	defer app2.Shutdown()
	if task := taskIn(app2, ids2[0]); task == nil || task["state"] != statePaused || task["type"] != typeSep || task["noMusic"] != true {
		t.Errorf("after closing: %v", task)
	}
}

// Downloads without music, through the real yt-dlp: an MP3 encoded once at the chosen
// rate, a video with its picture copied. While its music waits (or is removed), a download
// no longer takes a download's place; paused there, it resumes without downloading again.
func TestEndToEndNoMusic(t *testing.T) {
	if _, err := os.Stat(e2eMedia); err != nil {
		t.Skip("local media not prepared")
	}
	if os.Getenv("HELINK_SEP_ARCHIVES") == "" {
		t.Skip("set HELINK_SEP_ARCHIVES")
	}
	srv := newMediaServer(e2eMedia, 3_000_000)
	defer srv.Close()
	app, em, tmp, save := e2eApp(t, srv, nil)
	defer setLang("ar")
	defer app.Shutdown()
	installTestModel(t, filepath.Join(tmp, "data", "tools"))
	app.Fetch("https://youtu.be/TESTVID0001")
	em.waitFor(t, "info", "done", 60*time.Second)

	id1, _ := app.DownloadWith(0, "mp3-128", true)["id"].(string)
	seen := map[string]bool{}
	var done map[string]any
	deadline := time.Now().Add(90 * time.Second)
	for done == nil {
		name, m := em.next(t, deadline)
		task, _ := m["task"].(map[string]any)
		if name != "task" || task == nil || task["id"] != id1 {
			continue
		}
		switch task["state"] {
		case stateRunning:
			seen[task["stage"].(string)] = true
		case stateDone:
			done = task
		case stateError:
			t.Fatalf("mp3 without music: %v", task["error"])
		}
	}
	if !seen[stageAudio] || !seen[stageSeparate] || !seen[stageSave] || seen[stageConvert] {
		t.Errorf("stages %v (the sound is encoded once, after the music is removed)", seen)
	}
	p1 := done["result"].(map[string]any)["path"].(string)
	if filepath.Dir(p1) != filepath.Join(save, "Music") || !strings.HasSuffix(p1, " (بدون موسيقى).mp3") || done["noMusic"] != true {
		t.Errorf("mp3 without music: %s %v", p1, done["noMusic"])
	}
	pr := probeFile(t, p1)
	if pr.codecs() != "mp3" || pr.Streams[0].BitRate != "128000" || !strings.HasSuffix(pr.Format.Tags["title"], " (بدون موسيقى)") ||
		math.Abs(pr.duration()-8) > 0.2 {
		t.Errorf("mp3 %s %s %v %.2f", pr.codecs(), pr.Streams[0].BitRate, pr.Format.Tags, pr.duration())
	}

	// One download at a time, and the music remover kept busy.
	app.ChangeSettings(map[string]any{"maxParallel": 1})
	app.sepSem <- struct{}{}
	idV, _ := app.DownloadWith(0, "v360", true)["id"].(string)
	if again, _ := app.DownloadWith(0, "v360", true)["id"].(string); again != idV {
		t.Errorf("the same download twice: %s %s", again, idV)
	}
	for {
		if task := em.waitTask(t, idV, stateRunning, 60*time.Second); task["stage"] == stageSepWait {
			break
		}
	}
	idN, _ := app.Download(0, "v360")["id"].(string)
	if idN == idV || idN == "" {
		t.Fatalf("with and without music are two downloads: %s %s", idN, idV)
	}
	em.waitTask(t, idN, stateDone, 60*time.Second)
	if task := taskIn(app, idV); task["state"] != stateRunning || task["stage"] != stageSepWait {
		t.Errorf("still waiting: %v %v", task["state"], task["stage"])
	}
	app.Pause(idV)
	em.waitTask(t, idV, statePaused, 30*time.Second)
	<-app.sepSem
	srv.reset()
	app.Resume(idV)
	d := em.waitTask(t, idV, stateDone, 90*time.Second)
	srv.mu.Lock()
	served := append([]string(nil), srv.served...)
	srv.mu.Unlock()
	if len(served) != 0 {
		t.Errorf("downloaded again after the pause: %v", served)
	}
	pv := d["result"].(map[string]any)["path"].(string)
	if !strings.HasSuffix(pv, " - 360p (بدون موسيقى).mp4") {
		t.Errorf("video without music: %s", pv)
	}
	if v := probeFile(t, pv); v.codecs() != "h264,aac" || v.Streams[0].NbFrames != "240" || math.Abs(v.duration()-8) > 0.2 {
		t.Errorf("video %s frames %s %.2f", v.codecs(), v.Streams[0].NbFrames, v.duration())
	}
	if filepath.Dir(pv) != filepath.Join(save, "Video") {
		t.Errorf("not in the Video folder: %s", pv)
	}
	for _, kind := range []string{"Video", "Music"} {
		if _, err := os.Stat(filepath.Join(save, kind, tempFolderName)); err == nil {
			t.Errorf("temporary folder left behind in %s", kind)
		}
	}
}
