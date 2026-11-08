//go:build !windows

package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWritePreview builds preview.html (the real page plus a fake Windows side, ui/mock.js)
// for screenshots and browser tests. Run with NAZZELHA_PREVIEW=/path/to/preview.html and
// NAZZELHA_PREVIEW_DIR=<folder with thumb*.jpg>.
func TestWritePreview(t *testing.T) {
	out := os.Getenv("NAZZELHA_PREVIEW")
	if out == "" {
		t.Skip("set NAZZELHA_PREVIEW")
	}
	dir := os.Getenv("NAZZELHA_PREVIEW_DIR")
	img := func(name string) string {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return ""
		}
		return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(b)
	}
	type post struct {
		Site     string       `json:"site"`
		SiteName lazySiteName `json:"siteName"`
		Items    []Item       `json:"items"`
	}
	build := func(fixture, link string) post {
		b, err := os.ReadFile(filepath.Join("testdata", fixture))
		if err != nil {
			t.Fatal(err)
		}
		site, items, err := BuildPost(b, link)
		if err != nil {
			t.Fatal(err)
		}
		return post{Site: site, SiteName: lazySiteName(site), Items: items}
	}
	yt := build("youtube_like.json", siteYouTube)
	yt.Items[0].Meta.Title = "إزاي تعمل كيكة البرتقال في البيت بأسهل طريقة ومن غير فرن"
	yt.Items[0].Meta.Channel = "مطبخ ست الكل"
	yt.Items[0].Meta.DurationText = "12:48"
	yt.Items[0].Meta.Thumbnail = img("thumb.jpg")
	tt := build("tiktok.json", siteTikTok)
	tt.Items[0].Meta.Thumbnail = img("thumb_tall.jpg")
	x := build("x_multi.json", siteX)
	x.Items[0].Meta.Thumbnail = img("thumb2.jpg")
	x.Items[1].Meta.Thumbnail = img("thumb.jpg")
	ig := build("instagram_carousel.json", siteInstagram)
	ig.Items[0].Meta.Thumbnail = img("thumb_tall.jpg")
	ig.Items[1].Meta.Thumbnail = img("thumb2.jpg")
	posts := map[string]*post{"youtube": &yt, "tiktok": &tt, "x": &x, "instagram": &ig}
	ar, _ := json.Marshal(posts)

	// The same posts for the English preview: written in English, with English titles.
	setLang("en")
	defer setLang("ar")
	yt.Items[0].Meta.Title = "How to make orange cake at home, no oven needed"
	yt.Items[0].Meta.Channel = "Home Kitchen"
	tt.Items[0].Meta.Title = "The best video you'll see today 😂"
	for i := range x.Items {
		x.Items[i].Meta.Title = "Amazing goal in the 90th minute"
	}
	for i := range ig.Items {
		ig.Items[i].Meta.Title = "Instagram - sara.cooks"
	}
	en, _ := json.Marshal(posts)
	setLang("ar")

	data := `{"posts":` + string(ar) + `,"postsEn":` + string(en) + `}`
	mock, err := os.ReadFile("ui/mock.js")
	if err != nil {
		t.Fatal(err)
	}
	html := buildHTML()
	marker := "<script>\n(() => {\n'use strict';"
	if !strings.Contains(html, marker) {
		t.Fatal("page script marker not found")
	}
	inject := "<script>window.__nzPreviewData=" + data + ";</script>\n<script>" + string(mock) + "</script>\n" + marker
	html = strings.Replace(html, marker, inject, 1)
	if err := os.WriteFile(out, []byte(html), 0o644); err != nil {
		t.Fatal(err)
	}
}
