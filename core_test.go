package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseLink(t *testing.T) {
	cases := []struct {
		in   string
		want string
		site string
		kind LinkKind
		res  LinkResult
	}{
		// YouTube
		{"https://www.youtube.com/watch?v=dQw4w9WgXcQ", "https://www.youtube.com/watch?v=dQw4w9WgXcQ", siteYouTube, LinkVideo, LinkFound},
		{"شوف الفيديو ده https://youtu.be/dQw4w9WgXcQ?si=abc123 جامد", "https://youtu.be/dQw4w9WgXcQ?si=abc123", siteYouTube, LinkVideo, LinkFound},
		{"youtube.com/shorts/dQw4w9WgXcQ", "https://youtube.com/shorts/dQw4w9WgXcQ", siteYouTube, LinkVideo, LinkFound},
		{"https://m.youtube.com/watch?v=dQw4w9WgXcQ&list=PL1234&index=2", "https://m.youtube.com/watch?v=dQw4w9WgXcQ&list=PL1234&index=2", siteYouTube, LinkVideo, LinkFound},
		{"https://music.youtube.com/watch?v=dQw4w9WgXcQ&feature=share", "https://music.youtube.com/watch?v=dQw4w9WgXcQ&feature=share", siteYouTube, LinkVideo, LinkFound},
		{"(https://youtu.be/dQw4w9WgXcQ).", "https://youtu.be/dQw4w9WgXcQ", siteYouTube, LinkVideo, LinkFound},
		{"https://www.youtube.com/playlist?list=PL123", "https://www.youtube.com/playlist?list=PL123", siteYouTube, LinkPlaylist, LinkFound},
		{"https://www.youtube.com/@SomeChannel", "https://www.youtube.com/@SomeChannel", siteYouTube, LinkProfile, LinkFound},
		{"HTTP://WWW.YOUTUBE.COM/watch?v=dQw4w9WgXcQ", "https://WWW.YOUTUBE.COM/watch?v=dQw4w9WgXcQ", siteYouTube, LinkVideo, LinkFound},
		{"https://www.youtube.com/watch?v=short", "", "", "", LinkInvalid},
		{"https://www.youtube.com/", "", "", "", LinkInvalid},
		// Facebook
		{"https://www.facebook.com/watch/?v=1234567890", "https://www.facebook.com/watch/?v=1234567890", siteFacebook, LinkVideo, LinkFound},
		{"https://www.facebook.com/reel/1234567890?mibextid=abc", "https://www.facebook.com/reel/1234567890?mibextid=abc", siteFacebook, LinkVideo, LinkFound},
		{"https://m.facebook.com/MahmoudKitchen/videos/1234567890/", "https://m.facebook.com/MahmoudKitchen/videos/1234567890/", siteFacebook, LinkVideo, LinkFound},
		{"https://fb.watch/abCDef12/", "https://fb.watch/abCDef12/", siteFacebook, LinkShort, LinkFound},
		{"https://www.facebook.com/share/v/1AbCdEfGh/", "https://www.facebook.com/share/v/1AbCdEfGh/", siteFacebook, LinkShort, LinkFound},
		{"https://www.facebook.com/share/r/1AbCdEfGh/", "https://www.facebook.com/share/r/1AbCdEfGh/", siteFacebook, LinkShort, LinkFound},
		{"https://www.facebook.com/story.php?story_fbid=123&id=456", "https://www.facebook.com/story.php?story_fbid=123&id=456", siteFacebook, LinkOther, LinkFound},
		{"https://www.facebook.com/MahmoudKitchen", "https://www.facebook.com/MahmoudKitchen", siteFacebook, LinkProfile, LinkFound},
		{"https://www.facebook.com/photo/?fbid=123&set=a.456", "https://www.facebook.com/photo/?fbid=123&set=a.456", siteFacebook, LinkOther, LinkFound},
		{"https://www.facebook.com/groups/123/permalink/456/", "https://www.facebook.com/groups/123/permalink/456/", siteFacebook, LinkOther, LinkFound},
		{"https://web.facebook.com/", "", "", "", LinkInvalid},
		// Instagram
		{"https://www.instagram.com/reel/C1abcdEFGhi/?igsh=xyz", "https://www.instagram.com/reel/C1abcdEFGhi/?igsh=xyz", siteInstagram, LinkVideo, LinkFound},
		{"instagram.com/p/C1abcdEFGhi", "https://www.instagram.com/p/C1abcdEFGhi", siteInstagram, LinkVideo, LinkFound},
		{"https://www.instagram.com/sara.cooks/reel/C1abcdEFGhi/", "https://www.instagram.com/sara.cooks/reel/C1abcdEFGhi/", siteInstagram, LinkVideo, LinkFound},
		{"https://www.instagram.com/share/reel/BAabc123/", "https://www.instagram.com/share/reel/BAabc123/", siteInstagram, LinkShort, LinkFound},
		{"https://www.instagram.com/sara.cooks/", "https://www.instagram.com/sara.cooks/", siteInstagram, LinkProfile, LinkFound},
		{"https://www.instagram.com/reels/", "", "", "", LinkInvalid},
		// TikTok
		{"https://www.tiktok.com/@ahmed.ali/video/7412345678901234567?is_from_webapp=1", "https://www.tiktok.com/@ahmed.ali/video/7412345678901234567", siteTikTok, LinkVideo, LinkFound},
		{"https://tiktok.com/@ahmed.ali/video/7412345678901234567", "https://www.tiktok.com/@ahmed.ali/video/7412345678901234567", siteTikTok, LinkVideo, LinkFound},
		{"https://www.tiktok.com/@mona/photo/7400000000000000001", "https://www.tiktok.com/@mona/video/7400000000000000001", siteTikTok, LinkVideo, LinkFound},
		{"https://m.tiktok.com/v/7412345678901234567.html", "https://www.tiktok.com/@/video/7412345678901234567", siteTikTok, LinkVideo, LinkFound},
		{"https://vm.tiktok.com/ZMabc123/", "https://vm.tiktok.com/ZMabc123/", siteTikTok, LinkShort, LinkFound},
		{"https://www.tiktok.com/t/ZTRabc123/", "https://www.tiktok.com/t/ZTRabc123/", siteTikTok, LinkShort, LinkFound},
		{"https://www.tiktok.com/@ahmed.ali", "https://www.tiktok.com/@ahmed.ali", siteTikTok, LinkProfile, LinkFound},
		{"https://www.tiktok.com/@ahmed.ali/live", "https://www.tiktok.com/@ahmed.ali/live", siteTikTok, LinkLive, LinkFound},
		// X / Twitter
		{"https://x.com/egfootball/status/1840000000000000000?s=46&t=abc", "https://x.com/egfootball/status/1840000000000000000", siteX, LinkVideo, LinkFound},
		{"https://twitter.com/egfootball/status/1840000000000000000", "https://x.com/egfootball/status/1840000000000000000", siteX, LinkVideo, LinkFound},
		{"https://mobile.twitter.com/i/web/status/1840000000000000000", "https://x.com/i/web/status/1840000000000000000", siteX, LinkVideo, LinkFound},
		{"https://x.com/egfootball/status/1840000000000000000/photo/1", "https://x.com/egfootball/status/1840000000000000000", siteX, LinkVideo, LinkFound},
		{"https://x.com/egfootball/status/1840000000000000000/video/2", "https://x.com/egfootball/status/1840000000000000000/video/2", siteX, LinkVideo, LinkFound},
		{"https://fxtwitter.com/egfootball/status/1840000000000000000", "https://x.com/egfootball/status/1840000000000000000", siteX, LinkVideo, LinkFound},
		{"https://t.co/AbCdEf123", "https://t.co/AbCdEf123", siteX, LinkOther, LinkFound},
		{"https://x.com/egfootball", "https://x.com/egfootball", siteX, LinkProfile, LinkFound},
		{"https://x.com/home", "", "", "", LinkInvalid},
		// Others
		{"https://www.dailymotion.com/video/x8abc", "", "", "", LinkUnsupported},
		{"مرحبا", "", "", "", LinkNone},
		{"ابعتلي على ali@gmail.com وبعدين https://vm.tiktok.com/ZMabc123/", "https://vm.tiktok.com/ZMabc123/", siteTikTok, LinkShort, LinkFound},
	}
	for _, c := range cases {
		got, res := ParseLink(c.in)
		if res != c.res || got.URL != c.want || got.Site != c.site || got.Kind != c.kind {
			t.Errorf("ParseLink(%q) = %+v, %v; want %q %q %q %v", c.in, got, res, c.want, c.site, c.kind, c.res)
		}
	}
}

func TestSafeFileBase(t *testing.T) {
	cases := map[string]string{
		`أغنية "تجربة" | الحلقة 1: 100% كوميدي? <نسخة> *جديدة*`: `أغنية 'تجربة' - الحلقة 1 - 100% كوميدي نسخة جديدة`,
		"CON":                      "_CON",
		"  ...  ":                  "فيديو",
		"a/b\\c":                   "a - b - c",
		"\u202bنص\u202c مع علامات": "نص مع علامات",
		"trailing dots...":         "trailing dots",
	}
	for in, want := range cases {
		if got := SafeFileBase(in, 90); got != want {
			t.Errorf("SafeFileBase(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("ب", 200)
	if got := SafeFileBase(long, 50); len([]rune(got)) != 50 {
		t.Errorf("long title not cut: %d runes", len([]rune(got)))
	}
}

func TestUniquePath(t *testing.T) {
	dir := t.TempDir()
	p1 := UniquePath(dir, "فيديو - 720p", "mp4")
	if filepath.Base(p1) != "فيديو - 720p.mp4" {
		t.Fatal(p1)
	}
	os.WriteFile(p1, []byte("x"), 0o644)
	p2 := UniquePath(dir, "فيديو - 720p", "mp4")
	if filepath.Base(p2) != "فيديو - 720p (2).mp4" {
		t.Fatal(p2)
	}
}

func TestHumanText(t *testing.T) {
	sizes := map[int64]string{
		500 * 1024:             "500 كيلوبايت",
		6_500_000:              "6.2 ميجابايت",
		85 * 1024 * 1024:       "85 ميجابايت",
		1503238553:             "1.4 جيجابايت",
		2 * 1024 * 1024 * 1024: "2 جيجابايت",
	}
	for in, want := range sizes {
		if got := HumanSize(in); got != want {
			t.Errorf("HumanSize(%d) = %q, want %q", in, got, want)
		}
	}
	etas := map[float64]string{
		5:    "متبقٍ بضع ثوانٍ",
		40:   "متبقٍ أقل من دقيقة",
		70:   "متبقٍ نحو دقيقة",
		125:  "متبقٍ نحو دقيقتين",
		300:  "متبقٍ نحو 5 دقائق",
		900:  "متبقٍ نحو 15 دقيقة",
		3700: "متبقٍ نحو ساعة",
		4800: "متبقٍ نحو ساعة و20 دقيقة",
		7300: "متبقٍ نحو ساعتين",
	}
	for in, want := range etas {
		if got := HumanETA(in); got != want {
			t.Errorf("HumanETA(%v) = %q, want %q", in, got, want)
		}
	}
	if got := HumanDuration(3723); got != "1:02:03" {
		t.Error(got)
	}
	if got := HumanDuration(245); got != "4:05" {
		t.Error(got)
	}
	if got := HumanSpeed(2.3 * 1024 * 1024); got != "2.3 ميجابايت/ث" {
		t.Error(got)
	}
	// Exactly halfway values round to even, like the phone app.
	if got := HumanSize(int64(6.25 * 1024 * 1024)); got != "6.2 ميجابايت" {
		t.Error(got)
	}
	setLang("en")
	defer setLang("ar")
	en := map[string]string{
		HumanSize(85 * 1024 * 1024):   "85 MB",
		HumanSize(500 * 1024):         "500 KB",
		HumanSize(1503238553):         "1.4 GB",
		HumanSpeed(2.3 * 1024 * 1024): "2.3 MB/s",
		HumanETA(5):                   "A few seconds left",
		HumanETA(70):                  "About 1 minute left",
		HumanETA(300):                 "About 5 minutes left",
		HumanETA(4800):                "About 1 hour 20 min left",
		HumanETA(7300):                "About 2 hours left",
		siteName(siteTikTok):          "TikTok",
		siteName("other"):             "the site",
		SafeFileBase("...", 90):       "video",
	}
	for got, want := range en {
		if got != want {
			t.Errorf("English: got %q, want %q", got, want)
		}
	}
	setLang("ar")
	if siteName(siteTikTok) != "تيك توك" || SafeFileBase("...", 90) != "فيديو" {
		t.Error("back to Arabic")
	}
}

// Words are written when shown: an error or a download already created follows a change
// of language, also after being saved and read back.
func TestLanguage(t *testing.T) {
	defer setLang("ar")
	e := NewUserError("network").Localize(siteInstagram)
	if e.Title() != "تعذّر الوصول إلى إنستجرام" {
		t.Errorf("network title ar %q", e.Title())
	}
	d := NewUserError("disk")
	d.Need, d.Free, d.Folder = 300<<20, 100<<20, defaultSaveDir()
	if m := d.Message(); !strings.Contains(m, "300 ميجابايت") || !strings.Contains(m, "100 ميجابايت") || !strings.Contains(m, "التنزيلات ← HELINK") {
		t.Errorf("disk message ar %q", m)
	}
	task := &DownloadTask{ID: "x", URL: "https://youtu.be/x", Site: siteYouTube, Kind: "video", Key: "vbest", Label: "Video",
		Base: "b", SizeBytes: 69 << 20, State: stateRunning, Done: 31 << 20, Total: 69 << 20, Speed: 2.4 * 1024 * 1024, ETA: 40}
	j := func() map[string]any {
		b, _ := json.Marshal(task.toJSON(false))
		var m map[string]any
		json.Unmarshal(b, &m)
		return m
	}
	ar := j()
	if got := fmt.Sprint(ar["sizeText"], "|", ar["doneText"], "|", ar["label"], "|", ar["siteName"]); got != "69 ميجابايت|31 ميجابايت|فيديو|يوتيوب" {
		t.Errorf("task in Arabic: %s", got)
	}
	setLang("en")
	if e.Title() != "Can't reach Instagram" {
		t.Errorf("same error in English: %q", e.Title())
	}
	if got := NewUserError("extractor").Localize("").Title(); got != "The site changed something" {
		t.Errorf("capitalised site: %q", got)
	}
	if m := d.Message(); !strings.HasPrefix(m, "This file needs about 300 MB") || !strings.Contains(m, "100 MB") || !strings.Contains(m, "Downloads › HELINK") {
		t.Errorf("disk message en %q", m)
	}
	en := j()
	if got := fmt.Sprint(en["sizeText"], "|", en["doneText"], "|", en["speedText"], "|", en["etaText"], "|", en["label"], "|", en["siteName"]); got != "69 MB|31 MB|2.4 MB/s|Less than a minute left|Video|YouTube" {
		t.Errorf("same task in English: %s", got)
	}
	task.State, task.Err = stateError, NewUserError("network").Localize(siteYouTube)
	b := serializeTasks([]*DownloadTask{task})
	var l storedList
	if err := json.Unmarshal(b, &l); err != nil || len(l.Tasks) != 1 {
		t.Fatalf("stored list %s", b)
	}
	back := taskFromStore(l.Tasks[0])
	title := func() string {
		b, _ := json.Marshal(back.toJSON(false))
		var m struct {
			Error struct{ Title string } `json:"error"`
		}
		json.Unmarshal(b, &m)
		return m.Error.Title
	}
	if got := title(); got != "Can't reach YouTube" {
		t.Errorf("saved error in English: %q", got)
	}
	setLang("ar")
	if got := title(); got != "تعذّر الوصول إلى يوتيوب" {
		t.Errorf("saved error back in Arabic: %q", got)
	}
	for code := range errTexts {
		for _, lang := range []string{"ar", "en"} {
			setLang(lang)
			u := NewUserError(code).Localize(siteX)
			if u.Code != code || u.Title() == "" || u.Message() == "" || strings.Contains(u.Title()+u.Message(), "{") {
				t.Errorf("%s %s: %q / %q", code, lang, u.Title(), u.Message())
			}
		}
	}
	// Qualities follow the language too.
	setLang("en")
	_, opts, err := BuildOptions(loadFixture(t, "youtube_like.json"), siteYouTube)
	if err != nil {
		t.Fatal(err)
	}
	o, _ := json.Marshal(optByKey(opts, "v720"))
	if !strings.Contains(string(o), `"desc":"Good quality, reasonable size"`) || !strings.Contains(string(o), " MB") {
		t.Errorf("720p in English: %s", o)
	}
	setLang("ar")
	o, _ = json.Marshal(optByKey(opts, "v720"))
	if !strings.Contains(string(o), `"desc":"جودة جيدة بحجم مناسب"`) || !strings.Contains(string(o), " ميجابايت") {
		t.Errorf("720p in Arabic: %s", o)
	}
}

func loadFixture(t *testing.T, name string) *ytInfo {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	info, err := parseInfo(b)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func optByKey(opts []QualityOption, k string) *QualityOption {
	for i := range opts {
		if opts[i].Key == k {
			return &opts[i]
		}
	}
	return nil
}

func keysOf(opts []QualityOption) string {
	var keys []string
	for _, o := range opts {
		keys = append(keys, o.Key)
	}
	return strings.Join(keys, " ")
}

func TestBuildOptionsYouTube(t *testing.T) {
	meta, opts, err := BuildOptions(loadFixture(t, "youtube_like.json"), siteYouTube)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Thumbnail != "https://i.ytimg.com/vi/abcdefghijk/mqdefault.jpg" {
		t.Errorf("thumbnail %q", meta.Thumbnail)
	}
	if meta.DurationText != "5:00" || meta.Channel != "قناة القناة" || siteName(meta.Site) != "يوتيوب" {
		t.Errorf("meta %+v", meta)
	}
	want := "v2160 v1440 v1080 v720 v480 v360 v240 v144 mp3-320 mp3-192 mp3-128"
	if got := keysOf(opts); got != want {
		t.Fatalf("keys = %s, want %s", got, want)
	}
	check := func(key, spec string) {
		o := optByKey(opts, key)
		if o == nil || o.FormatSpec != spec {
			t.Errorf("%s spec = %+v, want %q", key, o, spec)
		}
	}
	// H.264 with the original-language AAC track (not dubbed, not DRC).
	check("v1080", "137+140-1/bv*+ba/b")
	check("v720", "136+140-1/bv*+ba/b")
	check("v360", "134+140-1/bv*+ba/b")
	// No H.264 at 1440p/2160p: VP9 SDR beats AV1 and HDR.
	check("v1440", "271+140-1/bv*+ba/b")
	check("v2160", "313+140-1/bv*+ba/b")
	check("mp3-192", "140-1/ba/b")
	if o := optByKey(opts, "v720"); !o.Recommended || compatNote(o.CompatRank) != "" || o.Label != "720p" || o.Tag != "HD" {
		t.Errorf("720 option %+v", o)
	}
	if o := optByKey(opts, "v2160"); compatNote(o.CompatRank) == "" || o.Label != "4K" {
		t.Errorf("2160 option %+v", o)
	}
	o := optByKey(opts, "v1080")
	wantSize := int64(3500*1000/8*300) + int64(129*1000/8*300)
	if o.SizeBytes != wantSize || HumanSize(o.SizeBytes) == "" {
		t.Errorf("1080 size %d, want %d", o.SizeBytes, wantSize)
	}
	if len(o.Parts) != 2 || o.Parts[0].FormatID != "137" || !o.Parts[1].IsAudio {
		t.Errorf("parts %+v", o.Parts)
	}
	if o.SortSpec != "res:1080,vcodec:h264,acodec:aac" {
		t.Error(o.SortSpec)
	}
	a := optByKey(opts, "mp3-192")
	if !a.Recommended || a.Kind != "audio" || a.AudioQuality != "192K" || a.SizeBytes != 300*192000/8 || a.Label != "192k" {
		t.Errorf("mp3 option %+v", a)
	}
	if b := optByKey(opts, "mp3-320"); b.Recommended || b.AudioQuality != "320K" {
		t.Errorf("mp3-320 %+v", b)
	}
}

func TestBuildOptionsShortAndDegraded(t *testing.T) {
	_, opts, err := BuildOptions(loadFixture(t, "short.json"), siteYouTube)
	if err != nil {
		t.Fatal(err)
	}
	if o := optByKey(opts, "v1080"); o == nil || o.FormatSpec != "137+140/bv*+ba/b" || o.Label != "1080p" {
		t.Errorf("short 1080: %+v", o)
	}
	if optByKey(opts, "v1920") != nil {
		t.Error("vertical video classified by its long side")
	}

	_, opts, err = BuildOptions(loadFixture(t, "only18.json"), siteYouTube)
	if err != nil {
		t.Fatal(err)
	}
	if keysOf(opts) != "v360 mp3-320 mp3-192 mp3-128" || opts[0].FormatSpec != "18/b" || !opts[0].Recommended {
		t.Fatalf("only18 opts: %+v", opts)
	}
	if a := optByKey(opts, "mp3-192"); a.FormatSpec != "18/b" || a.Parts[0].FormatID != "18" {
		t.Errorf("audio from combined: %+v", a)
	}

	for name, code := range map[string]string{"playlist.json": "playlist", "live.json": "live"} {
		b, _ := os.ReadFile(filepath.Join("testdata", name))
		_, _, err := BuildPost(b, siteYouTube)
		if ie, ok := err.(*InfoError); !ok || ie.Code != code {
			t.Errorf("%s: err %v, want %s", name, err, code)
		}
	}
}

func buildFixture(t *testing.T, name, linkSite string) (string, []Item) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	site, items, err := BuildPost(b, linkSite)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return site, items
}

func TestBuildPostSocial(t *testing.T) {
	// TikTok: H.264 preferred, HEVC only where nothing else exists, no watermark, sound from the video.
	site, items := buildFixture(t, "tiktok.json", siteTikTok)
	it := items[0]
	if site != siteTikTok || len(items) != 1 || it.Index != 0 {
		t.Fatalf("tiktok %s %+v", site, items)
	}
	if got := keysOf(it.Options); got != "v1080 v720 v480 mp3-320 mp3-192 mp3-128" {
		t.Errorf("tiktok keys %s", got)
	}
	if o := optByKey(it.Options, "v720"); o.FormatSpec != "h264_720p_912345-0/b" || !o.Recommended || o.SizeBytes != 2850000 {
		t.Errorf("tiktok 720 %+v", o)
	}
	if o := optByKey(it.Options, "v1080"); o.FormatSpec != "bytevc1_1080p_1512345-0/b" || compatNote(o.CompatRank) == "" {
		t.Errorf("tiktok 1080 %+v", o)
	}
	if o := optByKey(it.Options, "mp3-192"); o.FormatSpec != "h264_720p_912345-0/b" {
		t.Errorf("tiktok mp3 should come from the video, got %+v", o)
	}
	if it.Meta.Title != "ده أحلى فيديو هتشوفه النهارده 😂" || it.Meta.Thumbnail != "https://p16.tiktokcdn.com/origin.jpeg" ||
		siteName(it.Meta.Site) != "تيك توك" || it.Meta.Channel != "Ahmed Ali" {
		t.Errorf("tiktok meta %+v", it.Meta)
	}

	// TikTok photo post: MP3 only.
	_, items = buildFixture(t, "tiktok_photo.json", siteTikTok)
	if got := keysOf(items[0].Options); got != "mp3-320 mp3-192 mp3-128" {
		t.Errorf("photo keys %s", got)
	}
	if o := optByKey(items[0].Options, "mp3-192"); o.FormatSpec != "audio/ba/b" || !o.Recommended {
		t.Errorf("photo mp3 %+v", o)
	}

	// Instagram reel: DASH picture + DASH sound.
	_, items = buildFixture(t, "instagram_reel.json", siteInstagram)
	if got := keysOf(items[0].Options); got != "v1080 v720 v480 mp3-320 mp3-192 mp3-128" {
		t.Errorf("reel keys %s", got)
	}
	if o := optByKey(items[0].Options, "v720"); o.FormatSpec != "dash-720v+dash-audio/bv*+ba/b" {
		t.Errorf("reel 720 %+v", o)
	}
	if o := optByKey(items[0].Options, "v480"); o.FormatSpec != "102/b" {
		t.Errorf("reel 480 %+v", o)
	}
	if items[0].Meta.Title != "إنستجرام - sara.cooks" || items[0].Meta.Thumbnail != "https://scontent.cdninstagram.com/t320.jpg" || items[0].Meta.Channel != "sara.cooks" {
		t.Errorf("reel meta %+v", items[0].Meta)
	}

	// Instagram carousel with a photo in the middle: two videos, original positions kept.
	_, items = buildFixture(t, "instagram_carousel.json", siteInstagram)
	if len(items) != 2 || items[0].Index != 1 || items[1].Index != 3 || len(items[1].raw) == 0 {
		t.Fatalf("carousel %+v", items)
	}

	// X: HLS (known H.264) over the plain MP4 of unknown codec, sound from the HLS audio.
	_, items = buildFixture(t, "x_single.json", siteX)
	if got := keysOf(items[0].Options); got != "v720 v360 v240 mp3-320 mp3-192 mp3-128" {
		t.Errorf("x keys %s", got)
	}
	if o := optByKey(items[0].Options, "v720"); o.FormatSpec != "hls-2176+hls-audio-128000-Audio/bv*+ba/b" || !o.Recommended {
		t.Errorf("x 720 %+v", o)
	}
	if items[0].Meta.Title != "هدف رائع في الدقيقة 90" {
		t.Errorf("x title %q", items[0].Meta.Title)
	}
	site, items = buildFixture(t, "x_multi.json", siteX)
	if site != siteX || len(items) != 2 || items[0].Index != 1 || items[1].Index != 2 {
		t.Fatalf("x multi %+v", items)
	}
	if items[1].Meta.Title != "هدف رائع في الدقيقة 90" {
		t.Errorf("x multi title %q", items[1].Meta.Title)
	}

	// X GIF: picture only, no MP3.
	_, items = buildFixture(t, "x_gif.json", siteX)
	if got := keysOf(items[0].Options); got != "v240" || items[0].Options[0].FormatSpec != "http/bv*/b" {
		t.Errorf("gif %+v", items[0].Options)
	}

	// Facebook: DASH qualities win over the unsized "hd"/"sd" files; caption cleaned.
	_, items = buildFixture(t, "facebook_dash.json", siteFacebook)
	if got := keysOf(items[0].Options); got != "v720 v360 mp3-320 mp3-192 mp3-128" {
		t.Errorf("fb keys %s", got)
	}
	if o := optByKey(items[0].Options, "v720"); o.FormatSpec != "1111v+3333a/bv*+ba/b" {
		t.Errorf("fb 720 %+v", o)
	}
	if items[0].Meta.Title != "وصفة الكشري المصري الأصلية في البيت" {
		t.Errorf("fb title %q", items[0].Meta.Title)
	}
	// Facebook with only "hd" and "sd".
	_, items = buildFixture(t, "facebook_sdhd.json", siteFacebook)
	if got := keysOf(items[0].Options); got != "vhd vsd mp3-320 mp3-192 mp3-128" {
		t.Errorf("fb sdhd keys %s", got)
	}
	if o := optByKey(items[0].Options, "vhd"); o.FormatSpec != "hd/b" || !o.Recommended {
		t.Errorf("fb hd %+v", o)
	}
	if o := optByKey(items[0].Options, "mp3-192"); o.FormatSpec != "hd/b" {
		t.Errorf("fb mp3 %+v", o)
	}
	if items[0].Meta.Title != "فيسبوك - Cairo News" {
		t.Errorf("fb fallback title %q", items[0].Meta.Title)
	}
}

func TestCaptionTitles(t *testing.T) {
	info := &ytInfo{Title: "Video by sara.cooks", Channel: "sara.cooks",
		Description: "أسهل طريقة لعمل البسبوسة في البيت 😍 #حلويات #رمضان\n\nالمقادير: ..."}
	if m := buildMeta(info, siteInstagram); m.Title != "أسهل طريقة لعمل البسبوسة في البيت 😍" {
		t.Errorf("caption title %q", m.Title)
	}
	info.Description = "#reels #explore"
	if m := buildMeta(info, siteInstagram); m.Title != "إنستجرام - sara.cooks" {
		t.Errorf("hashtag-only caption %q", m.Title)
	}
	long := strings.Repeat("كلمة ", 40)
	if got := firstLine(long, 90); len([]rune(got)) > 91 || !strings.HasSuffix(got, "…") {
		t.Errorf("firstLine %q", got)
	}
	x := &ytInfo{Title: "Egypt Football - Egypt Football", Uploader: "Egypt Football"}
	if m := buildMeta(x, siteX); m.Title != "Egypt Football" {
		t.Errorf("x self title %q", m.Title)
	}
}

func TestLenientJSON(t *testing.T) {
	// A number where a string is expected (and the reverse) must not break the whole video.
	data := []byte(`{"id": 12345, "title": "x", "duration": "61.5", "uploader": null, "is_live": false,
		"formats": [{"format_id": 18, "width": "640", "height": 360, "vcodec": "avc1", "acodec": "mp4a", "tbr": null}]}`)
	_, items, err := BuildPost(data, siteFacebook)
	if err != nil {
		t.Fatal(err)
	}
	if items[0].Meta.ID != "12345" || items[0].Meta.Duration != 61.5 || items[0].Options[0].FormatSpec != "18/b" {
		t.Errorf("lenient %+v", items[0])
	}
}

func TestProgressParsingAndTracker(t *testing.T) {
	lines := []string{
		"NZLP|downloading|1024|3020458|NA|NA|NA|136",
		"NZLP|downloading|1566989|3020458|NA|1573699.32|0|136",
		"NZLP|finished|3020458|3020458|NA|1463961.02|NA|136",
		"NZLP|downloading|1024|NA|129542.5|45638.7|2|140",
		"NZLP|finished|129542|129542|NA|1592634.85|NA|140",
	}
	tr := NewProgressTracker("video", []dlPart{{FormatID: "136", Bytes: 3_000_000}, {FormatID: "140", Bytes: 130_000, IsAudio: true}})
	var last Overall
	var percents []float64
	for _, l := range lines {
		d, ok := parseDownloadLine(l)
		if !ok {
			t.Fatalf("not parsed: %s", l)
		}
		last = tr.OnDownload(d)
		percents = append(percents, last.Percent)
	}
	for i := 1; i < len(percents); i++ {
		if percents[i] < percents[i-1]-0.01 {
			t.Errorf("progress went backwards: %v", percents)
		}
	}
	if last.Percent != 100 || last.Stage != stageAudio || last.PartIndex != 2 {
		t.Errorf("final %+v", last)
	}
	if p := percents[1]; p < 49 || p > 53 {
		t.Errorf("midway percent %v", p)
	}
	st, pp, ok := parsePostLine("NZLPP|started|Merger")
	if !ok || st != "started" || pp != "Merger" {
		t.Error("post line")
	}
	if o := tr.OnPost(st, pp); o.Stage != stageMerge || o.Percent != -1 {
		t.Errorf("post %+v", o)
	}
	if p, ok := parseDoneLine("NZLDONE|C:\\Users\\Ali\\Downloads\\اسم الملف 100% - 720p.mp4"); !ok || !strings.HasSuffix(p, "720p.mp4") {
		t.Error("done line")
	}
}

func TestClassifyOutput(t *testing.T) {
	cases := map[string]string{
		"ERROR: [generic] 'not a url' is not a valid URL":                                                                       "invalid_url",
		"ERROR: [youtube] abc: Private video. Sign in if you've been granted access to this video":                              "private",
		"ERROR: [youtube] abc: Sign in to confirm your age. This video may be inappropriate for some users.":                    "age",
		"ERROR: [youtube] abc: Sign in to confirm you’re not a bot. Use --cookies-from-browser or --cookies":                    "bot",
		"ERROR: [youtube] abc: Video unavailable. This video has been removed by the uploader":                                  "unavailable",
		"ERROR: [youtube] abc: Join this channel to get access to members-only content like this video":                         "members",
		"ERROR: [youtube] abc: Unable to download API page: <urlopen error [WinError 11001] getaddrinfo failed> (caused by...)": "network",
		"ERROR: unable to download video data: HTTP Error 403: Forbidden":                                                       "forbidden",
		"ERROR: unable to download video data: HTTP Error 429: Too Many Requests":                                               "ratelimit",
		"ERROR: [Errno 28] No space left on device":                                                                             "disk",
		"ERROR: unable to open for writing: [Errno 13] Permission denied: 'C:\\x'":                                              "permission",
		"ERROR: [youtube] abc: Requested format is not available. Use --list-formats for a list of available formats":           "format",
		"ERROR: [youtube] abc: The uploader has not made this video available in your country":                                  "geo",
		"ERROR: [youtube] abc: This live event will begin in 3 hours.":                                                          "upcoming",
		"WARNING: something odd\nERROR: [youtube] abc: Unable to extract yt initial data; please report this issue":             "extractor",
		"ERROR: certificate verify failed: certificate is not yet valid":                                                        "clock",
		// Other sites
		"ERROR: [twitter] 1840: No video could be found in this tweet":                                                                      "novideo",
		"ERROR: [Instagram] C1abc: There is no video in this post":                                                                          "novideo",
		"ERROR: [twitter] 1840: NSFW tweet requires authentication. Use --cookies, --cookies-from-browser, --username and --password":       "login",
		"ERROR: [twitter] 1840: You are not authorized to view this protected tweet. Use --cookies, --cookies-from-browser":                 "private",
		"ERROR: [twitter] 1840: Requested tweet is unavailable":                                                                             "unavailable",
		"ERROR: [Instagram] C1abc: Requested content is not available, rate-limit reached or login required. Use --cookies":                 "ig_limit",
		"ERROR: [Instagram] C1abc: Instagram sent an empty media response. Check if this post is accessible in your browser. Use --cookies": "ig_limit",
		"ERROR: [Instagram] C1abc: This content is only available for registered users who follow this account. Use --cookies":              "private",
		"ERROR: [TikTok] 741: You do not have permission to view this post. Log into an account that has access. Use --cookies":             "private",
		"ERROR: [TikTok] 741: This post may not be comfortable for some audiences. Log in for access. Use --cookies-from-browser":           "login",
		"ERROR: [TikTok] 741: Your IP address is blocked from accessing this post":                                                          "geo",
		"ERROR: [TikTok] 741: Video not available, status code 10231":                                                                       "unavailable",
		"ERROR: [facebook] 123: The video is not available, Facebook said: \"This content isn't available right now\"":                      "unavailable",
		"ERROR: [facebook] 123: Cannot parse data; please report this issue on  https://github.com/yt-dlp/yt-dlp/issues":                    "extractor",
		"ERROR: Unsupported URL: https://www.facebook.com/login/?next=https%3A%2F%2Fwww.facebook.com%2Fshare%2Fv%2F1Ab":                     "login",
		"ERROR: Postprocessing: audio conversion failed: Output file #0 does not contain any stream":                                        "noaudio",
		"something completely unexpected": "unknown",
	}
	for in, want := range cases {
		if got := ClassifyOutput(in); got.Code != want {
			t.Errorf("ClassifyOutput(%q) = %s, want %s", in, got.Code, want)
		}
	}
	if e := ClassifyOutput("ERROR: HTTP Error 403: Forbidden"); !e.Outdated {
		t.Error("403 should trigger an update")
	}
	if e := NewUserError("private"); e.Retry || e.Title() == "" || e.Message() == "" {
		t.Errorf("private %+v", e)
	}
	// Site names in messages.
	if e := ClassifyFor("ERROR: [Instagram] x: Unable to download webpage: <urlopen error [WinError 11001] getaddrinfo failed>", siteInstagram); e.Title() != "تعذّر الوصول إلى إنستجرام" {
		t.Errorf("network title %q", e.Title())
	}
	if e := ClassifyFor("ERROR: x: Unable to extract data", siteTikTok); e.Code != "extractor" || e.Title() != "غيّر تيك توك طريقة عمله" {
		t.Errorf("extractor %+v", e)
	}
	if e := NewUserError("network").Localize(""); e.Title() != "تعذّر الوصول إلى الموقع" {
		t.Errorf("generic site %q", e.Title())
	}
	if e := ClassifyFor("ERROR: [TikTok] 741: Requested format is not available. No video formats found!", siteTikTok); e.Code != "novideo" {
		t.Errorf("no formats on social %+v", e)
	}
	if e := ClassifyFor("ERROR: [youtube] abc: no supported javascript runtime, deno", siteFacebook); e.Code != "extractor" || e.JSRuntime {
		t.Errorf("js runtime on other site %+v", e)
	}
	for _, code := range []string{"login", "novideo", "noaudio", "profile", "unsupported_site", "ig_limit"} {
		e := NewUserError(code).Localize(siteX)
		if e.Code == "unknown" || strings.Contains(e.Title()+e.Message(), "{site") {
			t.Errorf("%s: %+v", code, e)
		}
	}
}
