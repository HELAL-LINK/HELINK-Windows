//go:build !windows

package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResolveShortLink(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/redirect", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "facebookexternalhit/1.1" {
			t.Errorf("user agent %q", r.Header.Get("User-Agent"))
		}
		http.Redirect(w, r, "/hop", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/hop", func(w http.ResponseWriter, r *http.Request) {
		// The real post: the client must stop here without connecting to TikTok.
		http.Redirect(w, r, "https://www.tiktok.com/@mona/photo/7400000000000000001?_r=1", http.StatusFound)
	})
	mux.HandleFunc("/page", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><head><meta property="og:title" content="x"><meta property="og:url" content="https://www.facebook.com/MahmoudKitchen/videos/1234567890/"></head></html>`)
	})
	mux.HandleFunc("/canonical", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<link rel="canonical" href="https://www.instagram.com/reel/C1abcdEFGhi/">`)
	})
	mux.HandleFunc("/nothing", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "<html></html>") })
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cases := map[string]string{
		"/redirect":  "https://www.tiktok.com/@mona/photo/7400000000000000001?_r=1",
		"/page":      "https://www.facebook.com/MahmoudKitchen/videos/1234567890/",
		"/canonical": "https://www.instagram.com/reel/C1abcdEFGhi/",
		"/nothing":   srv.URL + "/nothing",
	}
	for path, want := range cases {
		got, err := resolveShortLink(context.Background(), srv.Client(), srv.URL+path)
		if err != nil || got != want {
			t.Errorf("%s: %q, %v; want %q", path, got, err, want)
		}
	}
	// The photo post found behind the short link is read through its video page.
	l, _ := ParseLink(cases["/redirect"])
	if l.URL != "https://www.tiktok.com/@mona/video/7400000000000000001" || l.Kind != LinkVideo {
		t.Errorf("resolved link %+v", l)
	}
}
