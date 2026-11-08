//go:build !windows

package main

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProductSite(t *testing.T) {
	for u, want := range map[string]string{
		"https://www.amazon.eg/-/en/Anker-Charger/dp/B0C1234567/ref=sr_1_1": prodAmazon,
		"https://amazon.ae/dp/B0C1234567":                                    prodAmazon,
		"https://amzn.eu/d/abc":                                              prodAmazon,
		"https://www.alibaba.com/product-detail/USB-C-Cable_1600123456789.html": prodAlibaba,
		"https://ar.aliexpress.com/item/1005006123456789.html":                  prodAliExpress,
		"https://www.noon.com/egypt-en/charger/N53412345A/p/":                   prodNoon,
		"https://www.temu.com/eg/usb-cable-g-601099512345678.html":              prodTemu,
		"https://aws.amazonaws.com/x":                                           "",
		"https://www.youtube.com/watch?v=x":                                     "",
	} {
		if got := productSite(u); got != want {
			t.Errorf("productSite(%s) = %q, want %q", u, got, want)
		}
	}
	for u, want := range map[string]bool{
		"https://www.amazon.eg/-/en/Anker-Charger/dp/B0C1234567/ref=sr_1_1":   true,
		"https://www.amazon.eg/s?k=charger":                                   false,
		"https://www.alibaba.com/product-detail/USB-C-Cable_1600123456789.html": true,
		"https://www.alibaba.com/":                                            false,
		"https://ar.aliexpress.com/item/1005006123456789.html":                  true,
		"https://www.noon.com/egypt-en/charger/N53412345A/p/":                   true,
		"https://www.noon.com/egypt-en/search?q=x":                              false,
		"https://www.temu.com/eg/usb-cable-g-601099512345678.html":              true,
		"https://www.temu.com/eg":                                               false,
	} {
		if got := isProductPage(u); got != want {
			t.Errorf("isProductPage(%s) = %v", u, got)
		}
	}
}

func TestProductAmazon(t *testing.T) {
	doc := `<html><head><title>Amazon.eg: Anker Charger</title></head><body>
<span id="productTitle" class="a-size-large">   Anker 735 Charger (Nano II 65W) &amp; Cable   </span>
<img id="landingImage" data-a-dynamic-image="{&quot;https://m.media-amazon.com/images/I/61abcDEF12L._AC_SX300_SY300_QL70_ML2_.jpg&quot;:[300,300]}">
<script type="text/javascript">
P.when('A').register("ImageBlockATF", function(A){
  var data = {
  'colorImages': { 'initial': [{"hiRes":"https:\/\/m.media-amazon.com\/images\/I\/61abcDEF12L._AC_SL1500_.jpg","thumb":"https://m.media-amazon.com/images/I/41x._AC_US40_.jpg","large":"https://m.media-amazon.com/images/I/41abcDEF12L._AC_.jpg","main":{}},
     {"hiRes":null,"large":"https://m.media-amazon.com/images/I/51Zz9+YyQ1L._AC_.jpg"},
     {"hiRes":"https://m.media-amazon.com/images/I/71Qq%2BRr2SL._AC_SL1500_.jpg","large":"x"}]},
  'videos': [{"title":"Anker 735","url":"https://m.media-amazon.com/images/S/vse-vms-transcoding-artifact-eu-west-1-prod/1a2b/default.jobtemplate.hls.m3u8","slateUrl":"https://m.media-amazon.com/images/I/81slate.jpg"}]
  };
});
</script>
<div id="similarities"><img src="https://m.media-amazon.com/images/I/OTHERPRODUCT._AC_UL320_.jpg"></div>
</body></html>`
	p := ReadProductPage(prodAmazon, "https://www.amazon.eg/dp/B0C1234567", doc)
	if p.Title != "Anker 735 Charger (Nano II 65W) & Cable" {
		t.Errorf("title %q", p.Title)
	}
	var imgs, vids []string
	for _, m := range p.Media {
		if m.Kind == "image" {
			imgs = append(imgs, m.URL)
		} else {
			vids = append(vids, m.URL)
		}
	}
	want := []string{
		"https://m.media-amazon.com/images/I/61abcDEF12L.jpg",
		"https://m.media-amazon.com/images/I/41abcDEF12L.jpg",
		"https://m.media-amazon.com/images/I/51Zz9+YyQ1L.jpg",
		"https://m.media-amazon.com/images/I/71Qq%2BRr2SL.jpg",
	}
	if strings.Join(imgs, "\n") != strings.Join(want, "\n") {
		t.Errorf("images:\n%s", strings.Join(imgs, "\n"))
	}
	if len(vids) != 1 || !strings.HasSuffix(vids[0], "hls.m3u8") {
		t.Errorf("videos %v", vids)
	}
	if p.Media[0].Kind != "video" {
		t.Errorf("videos first: %v", p.Media[0])
	}
}

func TestProductAliExpress(t *testing.T) {
	doc := `<html><head><meta property="og:title" content="65W GaN Charger USB C - AliExpress 44"></head><body>
<script>window.runParams = {data: {"imageModule":{"imagePathList":["https://ae01.alicdn.com/kf/S1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6dQ.jpg","https://ae01.alicdn.com/kf/S9f8e7d6c5b4a3f2e1d0c9b8a7f6e5d4cW.jpg"]},
"skuModule":{"productSKUPropertyList":[{"skuPropertyValues":[{"skuPropertyImagePath":"https://ae01.alicdn.com/kf/Sabcdefabcdefabcdefabcdefabcdef01X.jpg_640x640.jpg"}]}]},
"videoModule":{"videoUid":"Ab1Cd2","videoId":123456789}}};</script>
<div class="recommend"><img src="https://ae01.alicdn.com/kf/SOTHERother0123456789abcdef.jpg_220x220.jpg"></div>
</body></html>`
	p := ReadProductPage(prodAliExpress, "https://ar.aliexpress.com/item/1.html", doc)
	if p.Title != "65W GaN Charger USB C" {
		t.Errorf("title %q", p.Title)
	}
	got := []string{}
	for _, m := range p.Media {
		got = append(got, m.Kind+" "+m.URL)
	}
	want := []string{
		"video https://video.aliexpress-media.com/play/u/ae_sg_item/Ab1Cd2/p/1/e/6/t/10301/123456789.mp4",
		"image https://ae01.alicdn.com/kf/S1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6dQ.jpg",
		"image https://ae01.alicdn.com/kf/S9f8e7d6c5b4a3f2e1d0c9b8a7f6e5d4cW.jpg",
		"image https://ae01.alicdn.com/kf/Sabcdefabcdefabcdefabcdefabcdef01X.jpg",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("media:\n%s", strings.Join(got, "\n"))
	}
}

func TestProductAlibabaNoonTemu(t *testing.T) {
	ali := `<title>Fast Charging Cable - Buy Cable Product on Alibaba.com</title>
<script>window.detailData = {"globalData":{"product":{"mediaItems":[{"type":"image","imageUrl":{"big":"//s.alicdn.com/@sc04/kf/H1234567890abcdef1234567890abcdefA.jpg_720x720q50.jpg"}},
{"type":"video","videoUrl":"https://cloud.video.taobao.com/play/u/2200000/p/1/e/6/t/10301/400000000001.mp4"},
{"type":"image","imageUrl":{"big":"//s.alicdn.com/@sc04/kf/Hfedcba0987654321fedcba0987654321B.png_720x720.png"}}]}}};</script>`
	p := ReadProductPage(prodAlibaba, "https://www.alibaba.com/product-detail/x_1.html", ali)
	got := []string{}
	for _, m := range p.Media {
		got = append(got, m.Kind+" "+m.URL)
	}
	want := []string{
		"video https://cloud.video.taobao.com/play/u/2200000/p/1/e/6/t/10301/400000000001.mp4",
		"image https://s.alicdn.com/@sc04/kf/H1234567890abcdef1234567890abcdefA.jpg",
		"image https://s.alicdn.com/@sc04/kf/Hfedcba0987654321fedcba0987654321B.png",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("alibaba:\n%s", strings.Join(got, "\n"))
	}
	if p.Title != "Fast Charging Cable - Buy Cable Product on Alibaba.com" && p.Title != "Fast Charging Cable - Buy Cable Product on" {
		t.Logf("alibaba title %q", p.Title)
	}

	noon := `<title>Anker Charger | noon Egypt</title><script id="__NEXT_DATA__" type="application/json">{"props":{"pageProps":{"catalog":{"product":{"product_title":"Anker Charger",
"image_keys":["pnsku/N53412345A/45/_/1700000000/abc123de-0001","pnsku/N53412345A/45/_/1700000000/abc123de-0002"]}},
"related":[{"image_keys":["pnsku/NOTHIS/45/_/1/zzz"]}]}}}</script>`
	p = ReadProductPage(prodNoon, "https://www.noon.com/egypt-en/x/N53412345A/p/", noon)
	if len(p.Media) != 2 || p.Media[0].URL != "https://f.nooncdn.com/p/pnsku/N53412345A/45/_/1700000000/abc123de-0001.jpg?format=jpg&width=original" {
		t.Errorf("noon: %+v", p.Media)
	}
	if p.Title != "Anker Charger" {
		t.Errorf("noon title %q", p.Title)
	}

	temu := `<title>USB C Cable - Temu Egypt</title><script>window.rawData={"store":{"goods":{"gallery":[{"url":"https://img.kwcdn.com/product/fancy/aaaa-1111.jpg?imageView2/2/w/800/q/70/format/webp","width":800},
{"url":"https://img.kwcdn.com/product/fancy/bbbb-2222.jpg","width":800},{"videoUrl":"https://goods-vod.kwcdn.com/goods-video/cccc.mp4"}]},
"recommend":[{"thumbUrl":"https://img.kwcdn.com/product/fancy/OTHER-9999.jpg"}]}};</script>`
	p = ReadProductPage(prodTemu, "https://www.temu.com/eg/x-g-601099512345678.html", temu)
	got = got[:0]
	for _, m := range p.Media {
		got = append(got, m.Kind+" "+m.URL)
	}
	want = []string{
		"video https://goods-vod.kwcdn.com/goods-video/cccc.mp4",
		"image https://img.kwcdn.com/product/fancy/aaaa-1111.jpg",
		"image https://img.kwcdn.com/product/fancy/bbbb-2222.jpg",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("temu:\n%s", strings.Join(got, "\n"))
	}
	if p.Title != "USB C Cable" {
		t.Errorf("temu title %q", p.Title)
	}
	// Any page: its JSON-LD product's pictures.
	ld := `<script type="application/ld+json">{"@context":"https://schema.org","@type":"Product","name":"X","image":["https://cdn.example/1.jpg","https://cdn.example/2.jpg"]}</script>`
	p = ReadProductPage(prodNoon, "https://www.noon.com/x/p/", ld)
	if len(p.Media) != 2 || p.Media[1].URL != "https://cdn.example/2.jpg" {
		t.Errorf("ld: %+v", p.Media)
	}
	if !productCheckPage("<title>Robot Check</title> Enter the characters you see below") || productCheckPage(noon) {
		t.Error("check page")
	}
}

// The product window: a pasted link read by HELINK, the choice downloaded into a folder
// named after the product, in Pictures.
func TestProductDownloadE2E(t *testing.T) {
	jpg := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, []byte(strings.Repeat("p", 3000))...)
	mp4 := append([]byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p'}, []byte(strings.Repeat("v", 5000))...)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, ".jpg"):
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(jpg)
		case strings.HasSuffix(r.URL.Path, ".mp4"):
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write(mp4)
		default:
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, `<title>Test Charger | noon</title><script type="application/ld+json">{"@type":"Product","image":["`+srv.URL+`/a.jpg","`+srv.URL+`/b.jpg"]}</script><video src="`+srv.URL+`/v.mp4"></video>`)
		}
	}))
	defer srv.Close()
	app, events, _ := fileApp(t, "")
	// The page as the extension sends it (the same reading as for a pasted link).
	doc, _, err := fetchProductPage(app.ctx, srv.URL+"/item")
	if err != nil {
		t.Fatal(err)
	}
	r := app.readProductHTML(prodNoon, srv.URL+"/item", "", doc, FileSource{UserAgent: "test"})
	if r["found"] != 3 {
		t.Fatalf("found %v", r)
	}
	ev := events.waitFor(t, "product", "ready", 5*time.Second)
	items := ev["items"].([]any)
	if len(items) != 3 || items[0].(map[string]any)["kind"] != "video" || ev["title"] != "Test Charger" {
		t.Fatalf("event %v", ev)
	}
	id := int(ev["id"].(float64))
	res := app.ProductDownload(id, []int{0, 1, 2})
	if res["added"] != 3 {
		t.Fatalf("download %v", res)
	}
	folder := res["folder"].(string)
	if filepath.Base(folder) != "Test Charger" || filepath.Base(filepath.Dir(folder)) != categoryFolder(catPictures) {
		t.Fatalf("folder %s", folder)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		a, _ := os.ReadFile(filepath.Join(folder, "01.jpg"))
		b, _ := os.ReadFile(filepath.Join(folder, "02.jpg"))
		v, _ := os.ReadFile(filepath.Join(folder, tr("فيديو 1", "video 1")+".mp4"))
		if len(a) == len(jpg) && len(b) == len(jpg) && len(v) == len(mp4) {
			break
		}
		if time.Now().After(deadline) {
			list, _ := os.ReadDir(folder)
			t.Fatalf("files not there: %v", list)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestClipPreviewPick(t *testing.T) {
	info := `{"duration": 212, "formats": [
 {"format_id":"sb0","url":"https://i.ytimg.com/sb/x","protocol":"mhtml","ext":"mhtml","vcodec":"none","acodec":"none"},
 {"format_id":"140","url":"https://rr1.googlevideo.com/a?itag=140","protocol":"https","ext":"m4a","vcodec":"none","acodec":"mp4a.40.2","abr":129},
 {"format_id":"251","url":"https://rr1.googlevideo.com/a?itag=251","protocol":"https","ext":"webm","vcodec":"none","acodec":"opus","abr":140},
 {"format_id":"134","url":"https://rr1.googlevideo.com/v?itag=134","protocol":"https","ext":"mp4","vcodec":"avc1.4d401e","acodec":"none","height":360},
 {"format_id":"135","url":"https://rr1.googlevideo.com/v?itag=135","protocol":"https","ext":"mp4","vcodec":"avc1.4d401f","acodec":"none","height":480},
 {"format_id":"137","url":"https://rr1.googlevideo.com/v?itag=137","protocol":"https","ext":"mp4","vcodec":"avc1.640028","acodec":"none","height":1080},
 {"format_id":"hls-720","url":"https://x/m.m3u8","protocol":"m3u8_native","ext":"mp4","vcodec":"avc1","acodec":"mp4a","height":720},
 {"format_id":"18","url":"https://rr1.googlevideo.com/v?itag=18","protocol":"https","ext":"mp4","vcodec":"avc1.42001E","acodec":"mp4a.40.2","height":360,"http_headers":{"User-Agent":"UA-1"}}]}`
	p, err := pickPreview([]byte(info))
	if err != nil {
		t.Fatal(err)
	}
	// The 360p file with sound is near enough to the 480p picture alone: one file is simpler.
	if p.Kind != "av" || p.Video.FormatID != "18" || p.Duration != 212 {
		t.Errorf("preview %+v %+v", p, p.Video)
	}
	h := headersOf(p.Video, []Cookie{{Name: "a", Value: "1", Domain: ".googlevideo.com"}, {Name: "b", Value: "2", Domain: "other.com"}}, "https://page")
	if h.Get("User-Agent") != "UA-1" || h.Get("Cookie") != "a=1" || h.Get("Referer") != "https://page" {
		t.Errorf("headers %v", h)
	}
	// Without a file with sound: the picture and the sound, played together.
	split := strings.Replace(info, `"acodec":"mp4a.40.2","height":360,"http_headers"`, `"acodec":"none","height":240,"http_headers"`, 1)
	p, _ = pickPreview([]byte(split))
	if p.Kind != "split" || p.Video.FormatID != "135" || p.Audio.FormatID != "140" {
		t.Errorf("split %+v", p)
	}
	// Sound only.
	p, _ = pickPreview([]byte(`{"duration":30,"formats":[{"format_id":"a","url":"https://s/a.mp3","ext":"mp3","vcodec":"none","acodec":"mp3","abr":128}]}`))
	if p.Kind != "audio" || p.Audio.FormatID != "a" {
		t.Errorf("audio %+v", p)
	}
	// A direct file described at the top.
	p, _ = pickPreview([]byte(`{"duration":5,"url":"https://s/v.mp4","ext":"mp4"}`))
	if p.Kind != "av" || p.Video.URL != "https://s/v.mp4" {
		t.Errorf("direct %+v", p)
	}
	if _, err := pickPreview([]byte(`{"formats":[{"url":"https://x/m.m3u8","protocol":"m3u8_native","ext":"mp4","vcodec":"avc1","acodec":"mp4a"}]}`)); err != errNoPreview {
		t.Errorf("stream only: %v", err)
	}
}

func TestPreviewServer(t *testing.T) {
	data := []byte(strings.Repeat("0123456789", 1000))
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Need") != "yes" {
			http.Error(w, "no", http.StatusForbidden)
			return
		}
		http.ServeContent(w, r, "v.mp4", time.Time{}, strings.NewReader(string(data)))
	}))
	defer up.Close()
	var ps previewServer
	defer ps.stop()
	u, err := ps.add("clip", up.URL+"/v.mp4", http.Header{"X-Need": {"yes"}})
	if err != nil || !strings.HasPrefix(u, "http://127.0.0.1:") {
		t.Fatal(u, err)
	}
	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Set("Range", "bytes=5000-5009")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 206 || string(b) != "0123456789" || resp.Header.Get("Content-Range") != "bytes 5000-5009/10000" {
		t.Errorf("range: %d %q %v", resp.StatusCode, b, resp.Header)
	}
	ps.drop("clip")
	resp, _ = http.Get(u)
	if resp.StatusCode != 404 {
		t.Errorf("dropped: %d", resp.StatusCode)
	}
	resp.Body.Close()
	if r, _ := http.Get(strings.Replace(u, "/m/", "/m/x", 1)); r.StatusCode != 404 {
		t.Errorf("unknown token: %d", r.StatusCode)
	}
}

func TestFfmpegTimeAndCRLines(t *testing.T) {
	if s, ok := parseFfmpegTime("frame=  50 fps=0.0 q=-1.0 size=  256kB time=00:01:02.50 bitrate= 33.5kbits/s speed=12x"); !ok || s != 62.5 {
		t.Errorf("time %v %v", s, ok)
	}
	if _, ok := parseFfmpegTime("time=N/A bitrate=N/A"); ok {
		t.Error("N/A")
	}
	br := bufio.NewReader(strings.NewReader("a\rb\nc"))
	var got []string
	for {
		l, err := readLineCR(br)
		if l != "" {
			got = append(got, l)
		}
		if err != nil {
			break
		}
	}
	if strings.Join(got, "|") != "a\r|b\n|c" {
		t.Errorf("lines %q", got)
	}
	if HumanClock(62.9) != "1:02" || HumanClock(3725) != "1:02:05" || clipNamePart(80, 125) != tr("مقطع ", "part ")+"1.20-2.05" {
		t.Error("clock")
	}
}

// A part of a video: played through HELINK's server, cut by yt-dlp and ffmpeg as it is
// downloaded (the real tools, against the local media).
func TestClipEndToEnd(t *testing.T) {
	if _, err := os.Stat(e2eMedia); err != nil {
		t.Skip("local media not prepared")
	}
	srv := newMediaServer(e2eMedia, 50_000_000)
	defer srv.Close()
	app, em, _, save := e2eApp(t, srv, nil)
	defer app.Shutdown()
	app.Init()
	app.Fetch("https://youtu.be/TESTVID0001")
	info := em.waitFor(t, "info", "done", 60*time.Second)
	_ = info
	pv := app.ClipPreview(0)
	if pv["kind"] == "none" || pv["kind"] == nil {
		t.Fatalf("preview %v", pv)
	}
	play, _ := pv["video"].(string)
	if play == "" {
		play, _ = pv["audio"].(string)
	}
	req, _ := http.NewRequest("GET", play, nil)
	req.Header.Set("Range", "bytes=0-99")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 206 || len(b) != 100 {
		t.Fatalf("preview through HELINK's server: %d, %d bytes", resp.StatusCode, len(b))
	}
	if r := app.ClipDownload(0, "v360", 3, 3.5, false); r["error"] == nil {
		t.Fatalf("a part under a second: %v", r)
	}
	r := app.ClipDownload(0, "v360", 2, 4.75, false)
	id, _ := r["id"].(string)
	if id == "" {
		t.Fatalf("clip %v", r)
	}
	task := em.waitTask(t, id, stateDone, 120*time.Second)
	path, _ := task["result"].(map[string]any)["path"].(string)
	if !strings.Contains(filepath.Base(path), tr("مقطع 0.02-0.04", "part 0.02-0.04")) || !strings.HasSuffix(path, ".mp4") {
		t.Errorf("name %s", path)
	}
	if !strings.HasPrefix(path, save) {
		t.Errorf("where %s", path)
	}
	d := ffprobe(t, path, "format=duration")
	var sec float64
	fmt.Sscan(d, &sec)
	if sec < 2.5 || sec > 3.1 {
		t.Errorf("length %v (want about 2.75 s)", d)
	}
	if v := ffprobe(t, path, "stream=codec_type"); !strings.Contains(v, "video") || !strings.Contains(v, "audio") {
		t.Errorf("streams %s", v)
	}
	if tk := taskIn(app, id); tk["clip"] != "0:02 – 0:04" {
		t.Errorf("list shows %v", tk["clip"])
	}
	// The sound of a part, as MP3.
	r = app.ClipDownload(0, "mp3-128", 1, 4, false)
	id, _ = r["id"].(string)
	task = em.waitTask(t, id, stateDone, 120*time.Second)
	mp3, _ := task["result"].(map[string]any)["path"].(string)
	fmt.Sscan(ffprobe(t, mp3, "format=duration"), &sec)
	if !strings.HasSuffix(mp3, ".mp3") || sec < 2.8 || sec > 3.3 {
		t.Errorf("mp3 %s %v", mp3, sec)
	}
}
