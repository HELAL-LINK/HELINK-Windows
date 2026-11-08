package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestBuildList(t *testing.T) {
	defer setLang("ar")
	setLang("ar")
	b, err := os.ReadFile("testdata/youtube_playlist_flat.json")
	if err != nil {
		t.Fatal(err)
	}
	l, err := BuildList(b)
	if err != nil {
		t.Fatal(err)
	}
	if l.Title != "دورة بايثون للمبتدئين: من الصفر" || l.Channel != "قناة البرمجة" || l.ID != "PLtest0001course" || len(l.Entries) != 8 {
		t.Fatalf("list %+v", l)
	}
	var avail []int
	for _, e := range l.Entries {
		if e.Available {
			avail = append(avail, e.N)
		}
	}
	// Private, deleted, members-only and not-yet-shown videos are listed but cannot be picked.
	if got := jsonOf(avail); got != "[1,2,4,8]" {
		t.Errorf("available %v", got)
	}
	e := l.Entries[0]
	if e.N != 1 || e.ID != "aaaaaaaaaa1" || e.DurationText != "12:34" || e.Thumbnail != "https://i.ytimg.com/vi/aaaaaaaaaa1/mqdefault.jpg" ||
		e.Channel != "قناة البرمجة" {
		t.Errorf("first %+v", e)
	}
	if l.Entries[3].N != 4 || l.Entries[3].DurationText != "21:06" {
		t.Errorf("fourth %+v", l.Entries[3])
	}
	// What the page gets: counts and total length of what can be downloaded, the qualities.
	var page struct {
		Count        int
		DurationText string
		Options      []ListOption
		Entries      []ListEntry
	}
	js, _ := json.Marshal(l)
	json.Unmarshal(js, &page)
	if page.Count != 4 || page.DurationText != "1:12:46" || len(page.Options) != 7 {
		t.Errorf("page %s", js)
	}
	keys := []string{}
	for _, o := range page.Options {
		keys = append(keys, o.Key)
		if o.Key == "v720" && (!o.Recommended || o.Label != "720p" || o.Desc != "جودة جيدة بحجم مناسب") {
			t.Errorf("720p %+v", o)
		}
	}
	if strings.Join(keys, ",") != "v1080,v720,v480,v360,mp3-320,mp3-192,mp3-128" {
		t.Errorf("keys %v", keys)
	}
	// A list with nothing to download.
	if _, err := BuildList([]byte(`{"_type":"playlist","title":"x","entries":[{"_type":"url","id":"bbbbbbbbbb1","title":"[Private video]"}]}`)); err == nil {
		t.Error("an empty list should fail")
	} else if ie, ok := err.(*InfoError); !ok || ie.Code != "list_empty" {
		t.Errorf("empty list: %v", err)
	}
}

func jsonOf(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestYoutubeListOf(t *testing.T) {
	for in, want := range map[string]string{
		"https://www.youtube.com/watch?v=aaaaaaaaaa1&list=PLtest0001course&index=2": "https://www.youtube.com/playlist?list=PLtest0001course",
		"https://youtu.be/aaaaaaaaaa1?list=PLtest0001course":                        "https://www.youtube.com/playlist?list=PLtest0001course",
		"https://m.youtube.com/watch?v=aaaaaaaaaa1&list=OLAK5uy_abc":                "https://www.youtube.com/playlist?list=OLAK5uy_abc",
		"https://www.youtube.com/watch?v=aaaaaaaaaa1&list=RDaaaaaaaaaa1":            "", // a mix: endless, made for one person
		"https://www.youtube.com/watch?v=aaaaaaaaaa1&list=WL":                       "", // Watch later
		"https://www.youtube.com/watch?v=aaaaaaaaaa1&list=LL":                       "",
		"https://www.youtube.com/watch?v=aaaaaaaaaa1":                               "",
		"https://www.youtube.com/watch?v=aaaaaaaaaa1&list=PL<script>":               "",
		"https://www.tiktok.com/@a/video/1?list=PLx":                                "",
	} {
		if got := youtubeListOf(in); got != want {
			t.Errorf("%s: %q, want %q", in, got, want)
		}
	}
}

func TestYoutubeVideoID(t *testing.T) {
	for in, want := range map[string]string{
		"https://www.youtube.com/watch?v=aaaaaaaaaa1":                       "aaaaaaaaaa1",
		"https://www.youtube.com/watch?v=aaaaaaaaaa1&list=PLtest0001course": "aaaaaaaaaa1",
		"https://m.youtube.com/watch?v=aaaaaaaaaa1&t=30s":                   "aaaaaaaaaa1",
		"https://youtu.be/aaaaaaaaaa1?si=x":                                 "aaaaaaaaaa1",
		"https://www.youtube.com/shorts/aaaaaaaaaa1":                        "aaaaaaaaaa1",
		"https://www.youtube.com/live/aaaaaaaaaa1?feature=share":            "aaaaaaaaaa1",
		"https://www.youtube.com/playlist?list=PLtest0001course":            "",
		"https://www.youtube.com/watch?v=short":                             "",
		"https://www.youtube.com/@prog":                                     "",
		"https://www.tiktok.com/@a/video/1234567890123456789":               "",
		"https://example.com/watch?v=aaaaaaaaaa1":                           "",
	} {
		if got := youtubeVideoID(in); got != want {
			t.Errorf("%s: %q, want %q", in, got, want)
		}
	}
}

func TestListLinks(t *testing.T) {
	for in, want := range map[string]string{
		"https://www.youtube.com/playlist?list=PLtest0001course": "playlist",
		"https://www.youtube.com/@prog":                          "channel",
		"https://www.youtube.com/@prog/videos":                   "channel",
		"https://www.youtube.com/watch?v=aaaaaaaaaa1&list=PLx":   "",
		"https://www.instagram.com/someone/":                     "profile",
	} {
		if _, code := linkToRead(in); code != want {
			t.Errorf("%s: %q, want %q", in, code, want)
		}
	}
	for key, want := range map[string]string{
		"v720":    "video|720p|bv*+ba/b|res:720,vcodec:h264,acodec:aac|",
		"v1080":   "video|1080p|bv*+ba/b|res:1080,vcodec:h264,acodec:aac|",
		"mp3-192": "audio|192k|ba/b|acodec:aac|192K",
	} {
		kind, label, f, s, q, ok := listFormat(key)
		if got := strings.Join([]string{kind, label, f, s, q}, "|"); !ok || got != want {
			t.Errorf("%s: %s", key, got)
		}
	}
	for _, key := range []string{"v721", "mp3-100", "vbest", "", "720"} {
		if _, _, _, _, _, ok := listFormat(key); ok {
			t.Errorf("%q accepted", key)
		}
	}
}
