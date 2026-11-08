//go:build windows

package main

// Removing the music on Windows (under Wine here) with the real ffmpeg.exe: pipes, job
// objects, thread priority, Arabic names. HELINK_SEP_DATA is an app folder prepared as
// HELINK sets it up (tools\ffmpeg with the FFmpeg build it installs, tools\sep with the
// remover); HELINK_SEP_MEDIA holds the test files (made by sepMedia in the Linux tests).

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// running counts the programs of that name (ffmpeg.exe) running now.
func running(name string) int {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return -1
	}
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	n := 0
	for err := windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if strings.EqualFold(windows.UTF16ToString(e.ExeFile[:]), name) {
			n++
		}
	}
	return n
}

func TestWinThreadPriority(t *testing.T) {
	get := func() int32 {
		r, _, _ := procGetThreadPriority.Call(uintptr(windows.CurrentThread()))
		return int32(r)
	}
	restore := lowerThreadPriority()
	if p := get(); p != -1 {
		t.Errorf("while removing music: priority %d, want -1 (below normal)", p)
	}
	restore()
	if p := get(); p != 0 {
		t.Errorf("after: priority %d, want 0", p)
	}
}

func TestWinNoMusic(t *testing.T) {
	data, media := os.Getenv("HELINK_SEP_DATA"), os.Getenv("HELINK_SEP_MEDIA")
	if data == "" || media == "" {
		t.Skip("set HELINK_SEP_DATA and HELINK_SEP_MEDIA")
	}
	for _, f := range []string{"downloads.json", "settings.json"} {
		os.Remove(filepath.Join(data, f))
	}
	dir := filepath.Join(t.TempDir(), "ملفاتي")
	os.MkdirAll(dir, 0o755)
	for _, n := range []string{"أغنية.mp3", "درس.mp4", "حديث.ogg", "long.flac"} {
		if err := copyFile(filepath.Join(media, n), filepath.Join(dir, n)); err != nil {
			t.Fatal(err)
		}
	}
	em := &chanEmitter{ch: make(chan string, 100000)}
	app := NewApp(em, newPaths(data))
	defer app.Shutdown()
	if r := app.Init(); !r.SepReady {
		t.Fatal("the remover is in place")
	}
	ids := app.AddSepFiles([]string{filepath.Join(dir, "أغنية.mp3"), filepath.Join(dir, "درس.mp4"), filepath.Join(dir, "حديث.ogg")})["ids"].([]string)
	if len(ids) != 3 {
		t.Fatalf("added %v", ids)
	}
	start := time.Now()
	paths := map[string]string{}
	for _, id := range ids {
		d := em.waitTask(t, id, stateDone, 3*time.Minute)
		paths[d["title"].(string)] = d["result"].(map[string]any)["path"].(string)
	}
	t.Logf("3 files in %s", time.Since(start).Round(time.Millisecond))
	probe := func(p string) (codecs string, tags map[string]string) {
		t.Helper()
		out, err := exec.Command(filepath.Join(data, "tools", "ffmpeg", "ffprobe.exe"), "-v", "error", "-print_format", "json",
			"-show_format", "-show_streams", "file:"+p).Output()
		if err != nil {
			t.Fatalf("ffprobe %s: %v", p, err)
		}
		var r struct {
			Streams []struct {
				CodecName   string         `json:"codec_name"`
				Disposition map[string]int `json:"disposition"`
			} `json:"streams"`
			Format struct {
				Tags map[string]string `json:"tags"`
			} `json:"format"`
		}
		json.Unmarshal(out, &r)
		var l []string
		for _, s := range r.Streams {
			c := s.CodecName
			if s.Disposition["attached_pic"] == 1 {
				c += "*"
			}
			l = append(l, c)
		}
		return strings.Join(l, ","), r.Format.Tags
	}
	for title, want := range map[string]string{"أغنية.mp3": "أغنية (بدون موسيقى).mp3|mp3,png*", "درس.mp4": "درس (بدون موسيقى).mp4|h264,aac,mov_text",
		"حديث.ogg": "حديث (بدون موسيقى).ogg|vorbis"} {
		p := paths[title]
		c, tags := probe(p)
		if got := filepath.Base(p) + "|" + c; !strings.HasPrefix(got, want) || filepath.Dir(p) != dir {
			t.Errorf("%s: %s in %s, want %s", title, got, filepath.Dir(p), want)
		}
		if title == "أغنية.mp3" && tags["title"] != "أغنية تجربة (بدون موسيقى)" {
			t.Errorf("mp3 tags %v", tags)
		}
	}

	// Deleted in the middle: ffmpeg is stopped with its job object, nothing is left.
	id := app.AddSepFiles([]string{filepath.Join(dir, "long.flac")})["ids"].([]string)[0]
	for {
		task := em.waitTask(t, id, stateRunning, time.Minute)
		if task["stage"] == stageSeparate && task["percent"].(float64) > 0 {
			break
		}
	}
	if n := running("ffmpeg.exe"); n != 2 {
		t.Errorf("%d ffmpeg running while removing (the decoder and the encoder)", n)
	}
	app.Delete(id)
	em.waitTask(t, id, "removed", 30*time.Second)
	time.Sleep(500 * time.Millisecond)
	if n := running("ffmpeg.exe"); n != 0 {
		t.Errorf("%d ffmpeg still running after delete", n)
	}
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 7 {
		t.Errorf("folder: %v", names)
	}
	if _, err := os.Stat(filepath.Join(dir, tempFolderName)); err == nil {
		t.Error("temporary folder left behind")
	}
}
