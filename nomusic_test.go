package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSepProbeAndPlans(t *testing.T) {
	defer setLang("ar")
	setLang("ar")
	// An MP3 with a cover and tags.
	p, err := parseProbe([]byte(`{"streams":[
		{"index":0,"codec_type":"audio","codec_name":"mp3","bit_rate":"191873","channels":2,"start_time":"0.025057","duration":"30.04","disposition":{"attached_pic":0},"tags":{}},
		{"index":1,"codec_type":"video","codec_name":"png","disposition":{"attached_pic":1}}],
		"format":{"format_name":"mp3","duration":"30.041","start_time":"0.025057","bit_rate":"193000","tags":{"Title":"أغنية","artist":"فنان"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if p.video || !p.audio || p.cover != 1 || p.acodec != "mp3" || p.abitrate != 191873 || p.duration != 30.04 || p.title != "أغنية" || p.titleOfStream {
		t.Fatalf("mp3 probe %+v", p)
	}
	pl := planFor(p, "MP3", "")
	if pl.video || pl.ext != "mp3" || pl.muxer != "mp3" || strings.Join(pl.codec, " ") != "-c:a libmp3lame -b:a 192k" ||
		!pl.cover || pl.title != "أغنية (بدون موسيقى)" || pl.mono {
		t.Fatalf("mp3 plan %+v", pl)
	}
	args := strings.Join(encodeArgs(p, pl, `C:\a\song.mp3`, `C:\a\out.mp3`), " ")
	for _, want := range []string{"-f f32le -ar 44100 -ac 2 -i pipe:0 -i file:C:\\a\\song.mp3 -map 0:a:0 -map 1:1 -c:v copy -disposition:v:0 attached_pic",
		"-map_metadata 1 -map_metadata:s:a:0 1:s:a:0", "-metadata title=أغنية (بدون موسيقى)", "-id3v2_version 3 -f mp3 -y file:C:\\a\\out.mp3"} {
		if !strings.Contains(args, want) {
			t.Errorf("mp3 args lack %q: %s", want, args)
		}
	}
	// A download in MP3 from what yt-dlp gave (an Opus WebM), at the chosen rate.
	pl = planFor(&mediaProbe{audio: true, acodec: "opus", abitrate: 130000, cover: -1, title: "x", titleOfStream: true}, "webm", "128K")
	if pl.ext != "mp3" || strings.Join(pl.codec, " ") != "-c:a libmp3lame -b:a 128k" || pl.titleOfStream || pl.title != "x (بدون موسيقى)" {
		t.Fatalf("download plan %+v", pl)
	}
	// MP3 rates snap to the standard ones.
	for in, want := range map[int64]int64{0: 192000, 32000: 64000, 129523: 128000, 135000: 160000, 192000: 192000, 245000: 256000, 999000: 320000} {
		if got := mp3Rate(in); got != want {
			t.Errorf("mp3Rate(%d) = %d, want %d", in, got, want)
		}
	}
	// Ogg keeps its title on the sound; a title already ending so is left alone.
	pl = planFor(&mediaProbe{audio: true, acodec: "vorbis", channels: 1, cover: -1, title: "حديث", titleOfStream: true}, "ogg", "")
	if pl.muxer != "ogg" || pl.codec[1] != "libvorbis" || !pl.mono || !pl.titleOfStream || pl.title != "حديث (بدون موسيقى)" {
		t.Fatalf("ogg plan %+v", pl)
	}
	if args := strings.Join(encodeArgs(&mediaProbe{cover: -1}, pl, "a", "b"), " "); !strings.Contains(args, "-ac 1") ||
		!strings.Contains(args, "-metadata:s:a:0 title=حديث (بدون موسيقى)") {
		t.Errorf("ogg args %s", args)
	}
	if pl := planFor(&mediaProbe{audio: true, title: "حديث (بدون موسيقى)", cover: -1}, "flac", ""); pl.title != "" || pl.codec[1] != "flac" {
		t.Errorf("title twice: %+v", pl)
	}
	// Kinds ffmpeg writes badly become MP3; sound-only MP4 becomes M4A.
	for ext, want := range map[string]string{"wma": "mp3/mp3/libmp3lame", "ape": "mp3/mp3/libmp3lame", "m4a": "m4a/ipod/aac",
		"mp4": "m4a/ipod/aac", "aac": "aac/adts/aac", "opus": "opus/ogg/libopus", "wav": "wav/wav/pcm_s16le",
		"aif": "aiff/aiff/pcm_s16be", "webm": "webm/webm/libopus", "mka": "mka/matroska/aac"} {
		pl := planFor(&mediaProbe{audio: true, cover: -1}, ext, "")
		if got := pl.ext + "/" + pl.muxer + "/" + pl.codec[1]; got != want {
			t.Errorf("%s: %s, want %s", ext, got, want)
		}
	}

	// A video: its own kind of file, the sound joined afterwards; others become MKV.
	v, _ := parseProbe([]byte(`{"streams":[
		{"index":0,"codec_type":"video","codec_name":"h264","start_time":"0","disposition":{"attached_pic":0}},
		{"index":1,"codec_type":"audio","codec_name":"aac","bit_rate":"128000","channels":2,"start_time":"0.5","disposition":{}},
		{"index":2,"codec_type":"subtitle","codec_name":"mov_text"}],
		"format":{"format_name":"mov,mp4","duration":"10","start_time":"0","tags":{"title":"فيلم"}}}`))
	if !v.video || !v.subs || v.duration != 10 || v.astart != 0.5 {
		t.Fatalf("video probe %+v", v)
	}
	for ext, want := range map[string]string{"mp4": "mp4/mp4/aac", "M4V": "m4v/mp4/aac", "mov": "mov/mov/aac", "mkv": "mkv/matroska/aac",
		"webm": "webm/webm/libopus", "avi": "avi/avi/libmp3lame", "flv": "mkv/matroska/aac", "ts": "mkv/matroska/aac"} {
		pl := planFor(v, ext, "")
		if got := pl.ext + "/" + pl.muxer + "/" + pl.codec[1]; got != want || !pl.video || pl.offset != 0.5 {
			t.Errorf("video %s: %s offset %v, want %s", ext, got, pl.offset, want)
		}
	}
	vpl := planFor(v, "mp4", "")
	if args := strings.Join(encodeArgs(v, vpl, "in.mp4", "voice.mka"), " "); !strings.HasSuffix(args, "-map 0:a:0 -c:a aac -b:a 128k -f matroska -y file:voice.mka") {
		t.Errorf("voice args %s", args)
	}
	if args := strings.Join(muxArgs(vpl, "in.mp4", "voice.mka", "out.mp4"), " "); !strings.Contains(args,
		"-i file:in.mp4 -itsoffset 0.500 -i file:voice.mka -map 0:V -map 1:a:0 -map 0:s? -c copy -map_metadata 0 -map_metadata:s:a:0 0:s:a:0 -map_chapters 0") ||
		!strings.HasSuffix(args, "-metadata title=فيلم (بدون موسيقى) -f mp4 -y file:out.mp4") {
		t.Errorf("mux args %s", args)
	}
	// An MKV keeps Opus, and its fonts.
	if pl := planFor(&mediaProbe{video: true, audio: true, acodec: "opus", cover: -1}, "mkv", ""); pl.codec[1] != "libopus" || !pl.attach {
		t.Errorf("mkv plan %+v", pl)
	}
	// The space a video takes while it is made: its copy and its sound on its own.
	if n := spaceNeeded(v, vpl, 100<<20); n < 100<<20+2*(10*128000/8) || n > 100<<20+40<<20 {
		t.Errorf("space %d", n)
	}
	if n := spaceNeeded(&mediaProbe{}, vpl, 1<<30); n != 0 {
		t.Errorf("unknown length: %d", n)
	}
	setLang("en")
	if noMusicSuffix() != " (no music)" {
		t.Error(noMusicSuffix())
	}
}

func TestSepTaskStore(t *testing.T) {
	// A file of the computer is kept in the list like any download.
	desk := filepath.Join(string(filepath.Separator)+"Users", "a", "Desktop")
	tk := &DownloadTask{ID: "d1", Type: typeSep, SrcPath: filepath.Join(desk, "درس.mp4"), Title: "درس.mp4", Kind: "video",
		Base: "درس (بدون موسيقى)", SaveDir: desk, Category: catVideo, NoMusic: true, State: statePaused}
	back := taskFromStore(tk.toStore())
	if !back.valid() || !back.isSep() || back.SrcPath != tk.SrcPath || !back.NoMusic || back.labelText() != "MP4" {
		t.Fatalf("stored %+v", back)
	}
	if m := back.toJSON(false); m["srcName"] != "درس.mp4" || m["srcFolder"] != desk || m["noMusic"] != true || m["type"] != typeSep {
		t.Errorf("page %v", m)
	}
	tk.SrcPath = ""
	if tk.valid() {
		t.Error("an entry without its file must be dropped")
	}
	if (&DownloadTask{ID: "x", Type: "future", URL: "u", Kind: "video", Base: "b", State: stateDone}).valid() {
		t.Error("an entry of a newer HELINK must be dropped")
	}
	if !(&DownloadTask{ID: "x", URL: "u", Kind: "video", Base: "b", State: stateDone, NoMusic: true}).valid() {
		t.Error("a video without music")
	}
}
