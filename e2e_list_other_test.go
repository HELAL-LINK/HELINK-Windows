//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A whole course at once: the list is read, three of its videos are picked, and each is
// downloaded (the real yt-dlp and ffmpeg, the local video for each) into one folder named
// after the list, numbered in its order.
func TestEndToEndPlaylist(t *testing.T) {
	if _, err := os.Stat(e2eMedia); err != nil {
		t.Skip("local media not prepared")
	}
	srv := newMediaServer(e2eMedia, 6_000_000)
	defer srv.Close()
	app, em, _, save := e2eApp(t, srv, nil)
	defer setLang("ar")
	wd, _ := os.Getwd()
	t.Setenv("NAZZELHA_DEV_LISTJSON", filepath.Join(wd, "testdata", "youtube_playlist_flat.json"))

	r := app.AddLink("شوف الكورس ده https://www.youtube.com/playlist?list=PLtest0001course")
	if r["kind"] != "media" {
		t.Fatalf("add %v", r)
	}
	seq := r["seq"].(int)
	ev := em.waitFor(t, "info", "list", 30*time.Second)
	if int(ev["seq"].(float64)) != seq {
		t.Fatalf("seq %v want %d", ev["seq"], seq)
	}
	list := ev["list"].(map[string]any)
	if list["title"] != "دورة بايثون للمبتدئين: من الصفر" || list["count"] != 4.0 || len(list["entries"].([]any)) != 8 {
		t.Fatalf("list %v", list)
	}
	if f := app.Init().Fetch; f == nil || f.Status != "list" || f.List == nil {
		t.Errorf("a page that missed the list can catch up: %+v", f)
	}

	// Three videos, 720p at most; the private one asked for is left out.
	res := app.DownloadList(seq, []int{1, 3, 4, 8}, "v720", false)
	if res["error"] != nil || res["added"] != 3 || res["skipped"] != 0 {
		t.Fatalf("download list %v", res)
	}
	dir := filepath.Join(save, "Video", "دورة بايثون للمبتدئين - من الصفر")
	if res["folder"] != folderLabel(dir) {
		t.Errorf("folder %v, want %v", res["folder"], folderLabel(dir))
	}
	// Asked again: nothing twice.
	if again := app.DownloadList(seq, []int{1, 4, 8}, "v720", false); again["added"] != 0 || again["skipped"] != 3 {
		t.Errorf("again %v", again)
	}
	ids := res["ids"].([]string)
	wantNames := []string{"01 - الدرس 1 - المقدمة - لماذا بايثون؟.mp4", "04 - الدرس 4 - المتغيرات والأنواع.mp4",
		"08 - Lesson 8 - Loops in Python (for - while).mp4"}
	// They run at the same time: wait until all are done.
	done := map[string]map[string]any{}
	deadline := time.Now().Add(120 * time.Second)
	for len(done) < len(ids) {
		name, m := em.next(t, deadline)
		if name != "task" {
			continue
		}
		task, _ := m["task"].(map[string]any)
		if task == nil {
			continue
		}
		switch task["state"] {
		case stateDone:
			done[task["id"].(string)] = task
		case stateError:
			t.Fatalf("download error: %v", task["error"])
		}
	}
	for i, id := range ids {
		task := done[id]
		res := task["result"].(map[string]any)
		p := res["path"].(string)
		if filepath.Dir(p) != dir || filepath.Base(p) != wantNames[i] {
			t.Errorf("video %d saved as %s", i, p)
		}
		if task["list"] != "دورة بايثون للمبتدئين: من الصفر" || task["label"] != "720p" {
			t.Errorf("task %d: list %v label %v", i, task["list"], task["label"])
		}
		if got := ffprobe(t, p, "stream=codec_name,height"); got != "h264,720,aac" {
			t.Errorf("video %d streams %s", i, got)
		}
	}
	// The same list as MP3, without its music: another folder, another kind.
	res2 := app.DownloadList(seq, []int{2}, "mp3-128", true)
	if res2["added"] != 1 {
		t.Fatalf("mp3 %v", res2)
	}
	id := res2["ids"].([]string)[0]
	for _, m := range app.TaskList() {
		if m["id"] == id {
			if m["kind"] != "audio" || m["noMusic"] != true || !strings.HasPrefix(m["saveDir"].(string), filepath.Join(save, "Music")) {
				t.Errorf("mp3 task %v", m)
			}
		}
	}
	app.Delete(id)
	// A key that is not offered, or a list that is not the one on screen.
	if r := app.DownloadList(seq, []int{1}, "v999", false); r["error"] == nil {
		t.Error("an unknown quality should fail")
	}
	if r := app.DownloadList(seq+5, []int{1}, "v720", false); r["error"] == nil {
		t.Error("another list should fail")
	}

	// Pasted again: what is in the downloads is marked (the window leaves it unticked), by kind;
	// a video whose file was deleted since can be downloaded again.
	if err := os.Remove(done[ids[2]]["result"].(map[string]any)["path"].(string)); err != nil {
		t.Fatal(err)
	}
	mp3 := app.DownloadList(seq, []int{4}, "mp3-128", false) // still downloading when the list is read again
	r2 := app.AddLink("https://www.youtube.com/playlist?list=PLtest0001course")
	ev2 := em.waitFor(t, "info", "list", 30*time.Second)
	if int(ev2["seq"].(float64)) != r2["seq"].(int) {
		t.Fatalf("seq %v want %v", ev2["seq"], r2["seq"])
	}
	marks := map[int]string{}
	for _, e := range ev2["list"].(map[string]any)["entries"].([]any) {
		m := e.(map[string]any)
		if h, ok := m["have"]; ok {
			marks[int(m["n"].(float64))] = jsonOf(h)
		}
	}
	if jsonOf(marks) != `{"1":"[\"video\"]","4":"[\"video\",\"audio\"]"}` {
		t.Errorf("have %s", jsonOf(marks))
	}
	for _, id := range mp3["ids"].([]string) {
		app.Delete(id)
	}
	app.Shutdown()
}
