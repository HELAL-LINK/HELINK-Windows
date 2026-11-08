package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Supported sites
// ---------------------------------------------------------------------------

const (
	siteYouTube   = "youtube"
	siteFacebook  = "facebook"
	siteInstagram = "instagram"
	siteTikTok    = "tiktok"
	siteX         = "x"
)

// siteNames are the names people use for each site, in Arabic and in English.
var siteNames = map[string][2]string{
	siteYouTube:   {"يوتيوب", "YouTube"},
	siteFacebook:  {"فيسبوك", "Facebook"},
	siteInstagram: {"إنستجرام", "Instagram"},
	siteTikTok:    {"تيك توك", "TikTok"},
	siteX:         {"إكس", "X"},
}

// siteName is the site's name in the language in use: one of the sites, a web
// address's host ("dl.example.com"), or "الموقع" / "the site" when unknown.
func siteName(id string) string {
	if n, ok := siteNames[id]; ok {
		return tr(n[0], n[1])
	}
	if strings.Contains(id, ".") {
		return strings.TrimPrefix(id, "www.")
	}
	return tr("الموقع", "the site")
}

// hostOf is the host of a web address, for the messages ("" if there is none).
func hostOf(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

var schemeLinkRe = regexp.MustCompile(`(?i)https?://[^\s<>"'،]+`)

// firstWebAddress finds the first http(s) address inside pasted text.
func firstWebAddress(text string) string {
	for _, m := range append(schemeLinkRe.FindAllString(text, 5), anyLinkRe.FindAllString(text, 20)...) {
		m = strings.TrimRight(m, `.,;:!?)]}»"'`+"،؛؟")
		if !strings.HasPrefix(strings.ToLower(m), "http") {
			m = "https://" + m
		}
		if u, err := url.Parse(m); err == nil && u.Host != "" && (u.Scheme == "http" || u.Scheme == "https") {
			return u.String()
		}
	}
	return ""
}

// lazySiteName is a site written in the language in use whenever it is sent to the page.
type lazySiteName string

func (s lazySiteName) MarshalJSON() ([]byte, error) { return json.Marshal(siteName(string(s))) }

// siteFromExtractor maps yt-dlp's extractor name to one of our sites. A tweet that
// only links to a YouTube video comes back from the YouTube extractor, for example.
func siteFromExtractor(key, fallback string) string {
	k := strings.ToLower(key)
	switch {
	case strings.HasPrefix(k, "youtube"):
		return siteYouTube
	case strings.HasPrefix(k, "facebook"):
		return siteFacebook
	case strings.HasPrefix(k, "instagram"):
		return siteInstagram
	case strings.HasPrefix(k, "tiktok") || strings.HasPrefix(k, "vm.tiktok"):
		return siteTikTok
	case strings.HasPrefix(k, "twitter"):
		return siteX
	}
	return fallback
}

// ---------------------------------------------------------------------------
// Links
// ---------------------------------------------------------------------------

// LinkKind tells what a pasted link points to.
type LinkKind string

const (
	LinkVideo    LinkKind = "video"
	LinkPlaylist LinkKind = "playlist" // a YouTube playlist
	LinkProfile  LinkKind = "profile"  // a channel, page or account, not one video
	LinkLive     LinkKind = "live"     // a live stream page that is not a video yet
	LinkShort    LinkKind = "short"    // a short or share link we open first to find the real one
	LinkOther    LinkKind = "other"    // a page we don't recognise; yt-dlp decides
)

type Link struct {
	URL  string
	Site string
	Kind LinkKind
}

var (
	// Anything that looks like a web address, with or without https://
	anyLinkRe = regexp.MustCompile(`(?i)(?:https?://)?(?:[a-z0-9](?:[a-z0-9-]*[a-z0-9])?\.)+[a-z]{2,}(?::\d+)?(?:/[^\s<>"'،]*)?`)
	videoIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
	digitsRe  = regexp.MustCompile(`^\d+$`)
	tiktokVRe = regexp.MustCompile(`^(\d+)(?:\.html)?$`)
)

// LinkResult explains why no supported link was found.
type LinkResult int

const (
	LinkFound       LinkResult = iota
	LinkNone                   // no web address at all
	LinkUnsupported            // a web address, but not from a site we support
	LinkInvalid                // a supported site, but not a video page (home page, broken link)
)

func hostSite(host string) string {
	h := strings.TrimPrefix(strings.ToLower(host), "www.")
	is := func(domains ...string) bool {
		for _, d := range domains {
			if h == d || strings.HasSuffix(h, "."+d) {
				return true
			}
		}
		return false
	}
	switch {
	case is("youtube.com", "youtu.be", "youtube-nocookie.com"):
		return siteYouTube
	case is("facebook.com", "fb.watch", "fb.com", "fb.me"):
		return siteFacebook
	case is("instagram.com", "instagr.am"):
		return siteInstagram
	case is("tiktok.com"):
		return siteTikTok
	case is("x.com", "twitter.com", "t.co", "fxtwitter.com", "vxtwitter.com", "fixupx.com", "fixvx.com"):
		return siteX
	}
	return ""
}

// ParseLink finds the first supported link inside arbitrary pasted text (people often
// paste a whole WhatsApp message) and returns it cleaned up.
func ParseLink(text string) (Link, LinkResult) {
	result := LinkNone
	for _, m := range anyLinkRe.FindAllString(text, 20) {
		m = strings.TrimRight(m, `.,;:!?)]}»"'`+"،؛؟")
		if !strings.HasPrefix(strings.ToLower(m), "http") {
			m = "https://" + m
		}
		u, err := url.Parse(m)
		if err != nil || u.Host == "" {
			continue
		}
		site := hostSite(u.Hostname())
		if site == "" {
			if result == LinkNone {
				result = LinkUnsupported
			}
			continue
		}
		u.Scheme = "https"
		u.Fragment = ""
		var link Link
		var ok bool
		switch site {
		case siteYouTube:
			link, ok = parseYouTube(u)
		case siteFacebook:
			link, ok = parseFacebook(u)
		case siteInstagram:
			link, ok = parseInstagram(u)
		case siteTikTok:
			link, ok = parseTikTok(u)
		case siteX:
			link, ok = parseX(u)
		}
		if ok {
			link.Site = site
			return link, LinkFound
		}
		result = LinkInvalid
	}
	return Link{}, result
}

func pathParts(u *url.URL) []string {
	p := strings.Trim(u.Path, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

func parseYouTube(u *url.URL) (Link, bool) {
	host := strings.ToLower(u.Hostname())
	parts := pathParts(u)
	path := strings.Join(parts, "/")
	q := u.Query()
	switch {
	case host == "youtu.be":
		if len(parts) >= 1 && videoIDRe.MatchString(parts[0]) {
			return Link{URL: u.String(), Kind: LinkVideo}, true
		}
		return Link{}, false
	case path == "watch" || path == "watch_popup":
		if videoIDRe.MatchString(q.Get("v")) {
			return Link{URL: u.String(), Kind: LinkVideo}, true
		}
		return Link{}, false
	case len(parts) >= 2 && (parts[0] == "shorts" || parts[0] == "live" || parts[0] == "embed" || parts[0] == "v" || parts[0] == "e"):
		if videoIDRe.MatchString(parts[1]) {
			return Link{URL: u.String(), Kind: LinkVideo}, true
		}
		return Link{}, false
	case len(parts) >= 2 && parts[0] == "clip":
		return Link{URL: u.String(), Kind: LinkVideo}, true
	case path == "playlist":
		return Link{URL: u.String(), Kind: LinkPlaylist}, true
	case len(parts) > 0 && (strings.HasPrefix(parts[0], "@") || parts[0] == "channel" || parts[0] == "c" || parts[0] == "user"):
		return Link{URL: u.String(), Kind: LinkProfile}, true
	case path == "":
		return Link{}, false
	}
	return Link{URL: u.String(), Kind: LinkOther}, true
}

// Facebook paths that look like a page name but are not one.
var fbNotProfile = map[string]bool{"photo": true, "photos": true, "groups": true, "events": true, "stories": true,
	"live": true, "videos": true, "video": true, "watchparty": true, "media": true, "permalink": true, "posts": true}

func parseFacebook(u *url.URL) (Link, bool) {
	host := strings.ToLower(u.Hostname())
	parts := pathParts(u)
	if host == "fb.watch" || host == "fb.me" {
		if len(parts) == 0 {
			return Link{}, false
		}
		return Link{URL: u.String(), Kind: LinkShort}, true
	}
	if host == "fb.com" || host == "www.fb.com" {
		u.Host = "www.facebook.com"
	}
	if len(parts) == 0 {
		return Link{}, false
	}
	q := u.Query()
	first := strings.ToLower(parts[0])
	switch {
	case first == "share":
		return Link{URL: u.String(), Kind: LinkShort}, true
	case first == "reel" && len(parts) >= 2:
		return Link{URL: u.String(), Kind: LinkVideo}, true
	case first == "watch":
		if q.Get("v") != "" {
			return Link{URL: u.String(), Kind: LinkVideo}, true
		}
		return Link{}, false
	case first == "login" || first == "login.php" || first == "home.php" || first == "reels" || first == "marketplace" || first == "gaming":
		return Link{}, false
	case first == "profile.php" || first == "people":
		return Link{URL: u.String(), Kind: LinkProfile}, true
	case fbNotProfile[first]:
		return Link{URL: u.String(), Kind: LinkOther}, true
	case len(parts) >= 3 && strings.ToLower(parts[1]) == "videos":
		return Link{URL: u.String(), Kind: LinkVideo}, true
	case len(parts) == 1 && !strings.Contains(first, ".php"):
		return Link{URL: u.String(), Kind: LinkProfile}, true // a page or a person
	case len(parts) == 2 && (parts[1] == "videos" || parts[1] == "reels" || parts[1] == "photos"):
		return Link{URL: u.String(), Kind: LinkProfile}, true
	}
	return Link{URL: u.String(), Kind: LinkOther}, true
}

func parseInstagram(u *url.URL) (Link, bool) {
	u.Host = "www.instagram.com"
	parts := pathParts(u)
	if len(parts) == 0 {
		return Link{}, false
	}
	first := strings.ToLower(parts[0])
	isPost := func(seg string) bool {
		s := strings.ToLower(seg)
		return s == "p" || s == "reel" || s == "reels" || s == "tv"
	}
	switch {
	case first == "share":
		return Link{URL: u.String(), Kind: LinkShort}, true
	case first == "reels" && len(parts) >= 2 && strings.ToLower(parts[1]) == "audio":
		return Link{URL: u.String(), Kind: LinkProfile}, true
	case isPost(first) && len(parts) >= 2:
		return Link{URL: u.String(), Kind: LinkVideo}, true
	case len(parts) >= 3 && isPost(parts[1]):
		return Link{URL: u.String(), Kind: LinkVideo}, true // instagram.com/<user>/reel/<code>
	case first == "stories":
		return Link{URL: u.String(), Kind: LinkOther}, true
	case first == "accounts" || first == "explore" || first == "direct" || isPost(first):
		return Link{}, false
	case len(parts) <= 2:
		return Link{URL: u.String(), Kind: LinkProfile}, true
	}
	return Link{URL: u.String(), Kind: LinkOther}, true
}

func parseTikTok(u *url.URL) (Link, bool) {
	host := strings.ToLower(u.Hostname())
	parts := pathParts(u)
	if host == "vm.tiktok.com" || host == "vt.tiktok.com" {
		if len(parts) == 0 {
			return Link{}, false
		}
		return Link{URL: u.String(), Kind: LinkShort}, true
	}
	// yt-dlp only recognises www.tiktok.com.
	u.Host = "www.tiktok.com"
	if len(parts) == 0 {
		return Link{}, false
	}
	first := strings.ToLower(parts[0])
	video := func(id string) (Link, bool) {
		user := "@"
		if strings.HasPrefix(parts[0], "@") {
			user = parts[0]
		}
		return Link{URL: "https://www.tiktok.com/" + user + "/video/" + id, Kind: LinkVideo}, true
	}
	switch {
	case first == "t" && len(parts) >= 2:
		return Link{URL: u.String(), Kind: LinkShort}, true
	case strings.HasPrefix(first, "@") && len(parts) >= 3 && (parts[1] == "video" || parts[1] == "photo") && digitsRe.MatchString(parts[2]):
		// Photo posts are read through the video page: that gives their music.
		return video(parts[2])
	case strings.HasPrefix(first, "@") && len(parts) >= 2 && parts[1] == "live":
		return Link{URL: u.String(), Kind: LinkLive}, true
	case strings.HasPrefix(first, "@") && len(parts) <= 2:
		return Link{URL: u.String(), Kind: LinkProfile}, true
	case (first == "v" || first == "embed") && len(parts) >= 2:
		last := parts[len(parts)-1]
		if m := tiktokVRe.FindStringSubmatch(last); m != nil {
			return video(m[1])
		}
		return Link{}, false
	case (first == "share" || first == "video") && len(parts) >= 2:
		last := parts[len(parts)-1]
		if digitsRe.MatchString(last) {
			return video(last)
		}
		return Link{}, false
	case first == "music" || first == "tag" || first == "discover" || first == "explore" || first == "foryou" || first == "following" || first == "live":
		return Link{URL: u.String(), Kind: LinkProfile}, true
	}
	return Link{URL: u.String(), Kind: LinkOther}, true
}

var xStatusRe = regexp.MustCompile(`(?i)^/(?:[^/]+/status(?:es)?|i/(?:web/)?status|statuses)/(\d+)(/video/\d+)?`)

func parseX(u *url.URL) (Link, bool) {
	host := strings.ToLower(u.Hostname())
	parts := pathParts(u)
	if host == "t.co" {
		if len(parts) == 0 {
			return Link{}, false
		}
		return Link{URL: u.String(), Kind: LinkOther}, true
	}
	if len(parts) == 0 {
		return Link{}, false
	}
	if m := xStatusRe.FindStringSubmatch(u.Path); m != nil {
		// A link to one photo of the post would fail; ask for the whole post instead.
		path := strings.TrimSuffix(u.Path[:len(m[0])], "/")
		return Link{URL: "https://x.com" + path, Kind: LinkVideo}, true
	}
	first := strings.ToLower(parts[0])
	switch {
	case first == "i" && len(parts) >= 3 && (parts[1] == "spaces" || parts[1] == "broadcasts"):
		return Link{URL: "https://x.com" + u.Path, Kind: LinkOther}, true
	case first == "i" || first == "home" || first == "explore" || first == "search" || first == "notifications" || first == "messages" || first == "settings" || first == "login":
		return Link{}, false
	case len(parts) <= 2:
		return Link{URL: "https://x.com/" + parts[0], Kind: LinkProfile}, true
	}
	return Link{URL: "https://x.com" + u.Path, Kind: LinkOther}, true
}

// ---------------------------------------------------------------------------
// Short links
// ---------------------------------------------------------------------------

var (
	ogURLRe        = regexp.MustCompile(`(?i)<meta[^>]+property=["']og:url["'][^>]+content=["']([^"']+)["']`)
	ogURLRe2       = regexp.MustCompile(`(?i)<meta[^>]+content=["']([^"']+)["'][^>]+property=["']og:url["']`)
	canonicalURLRe = regexp.MustCompile(`(?i)<link[^>]+rel=["']canonical["'][^>]+href=["']([^"']+)["']`)
)

// resolveShortLink opens a short or share link (fb.watch/…, vm.tiktok.com/…,
// facebook.com/share/…) and returns the address of the real post.
func resolveShortLink(ctx context.Context, client *http.Client, raw string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", raw, nil)
	if err != nil {
		return "", err
	}
	// Sites answer link-preview robots with a plain redirect or a page that names the post.
	req.Header.Set("User-Agent", "facebookexternalhit/1.1")
	req.Header.Set("Accept-Language", "en-US,en;q=0.8")
	// Stop as soon as a redirect names the post: no need to load the post's heavy page.
	found := ""
	c := *client
	c.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		if l, res := ParseLink(r.URL.String()); res == LinkFound && l.Kind != LinkShort {
			found = r.URL.String()
			return http.ErrUseLastResponse
		}
		return nil
	}
	resp, err := c.Do(req)
	if found != "" {
		if resp != nil {
			resp.Body.Close()
		}
		return found, nil
	}
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	final := resp.Request.URL.String()
	if l, res := ParseLink(final); res == LinkFound && l.Kind != LinkShort {
		return final, nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	for _, re := range []*regexp.Regexp{ogURLRe, ogURLRe2, canonicalURLRe} {
		if m := re.FindSubmatch(body); m != nil {
			u := strings.ReplaceAll(string(m[1]), "&amp;", "&")
			if l, res := ParseLink(u); res == LinkFound && l.Kind != LinkShort {
				return u, nil
			}
		}
	}
	return final, nil
}

// ---------------------------------------------------------------------------
// Thumbnails
// ---------------------------------------------------------------------------

const browserUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36"

func siteReferer(site string) string {
	switch site {
	case siteTikTok:
		return "https://www.tiktok.com/"
	case siteInstagram:
		return "https://www.instagram.com/"
	case siteFacebook:
		return "https://www.facebook.com/"
	case siteX:
		return "https://x.com/"
	}
	return ""
}

// fetchImageDataURL downloads a small picture and returns it as a data: address
// the page can show directly.
func fetchImageDataURL(ctx context.Context, client *http.Client, src, referer string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", src, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Accept", "image/avif,image/webp,image/apng,image/*,*/*;q=0.8")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", &httpStatusError{resp.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20+1))
	if err != nil {
		return "", err
	}
	if len(body) > 4<<20 || len(body) == 0 {
		return "", errors.New("picture too large or empty")
	}
	ct := strings.TrimSpace(strings.SplitN(resp.Header.Get("Content-Type"), ";", 2)[0])
	if !strings.HasPrefix(ct, "image/") {
		ct = http.DetectContentType(body)
	}
	if !strings.HasPrefix(ct, "image/") {
		return "", errors.New("not a picture: " + ct)
	}
	return "data:" + ct + ";base64," + base64.StdEncoding.EncodeToString(body), nil
}
