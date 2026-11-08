package main

// Product pictures and videos (since 2.4): the photos and videos of a product on Amazon,
// Alibaba, AliExpress, Noon or Temu, at their full size, for a trader who studies a
// product. The page is read either by HELINK (a pasted link: the page as these sites give
// it to a program, which some of them refuse) or by the extension, from the page open in
// the browser (always works: it is the page the user sees). The user picks among them in
// the "product" window; they are downloaded into a folder named after the product, in
// Pictures.

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Product sites.
const (
	prodAmazon     = "amazon"
	prodAlibaba    = "alibaba"
	prodAliExpress = "aliexpress"
	prodNoon       = "noon"
	prodTemu       = "temu"
)

var productSiteNames = map[string][2]string{
	prodAmazon:     {"أمازون", "Amazon"},
	prodAlibaba:    {"علي بابا", "Alibaba"},
	prodAliExpress: {"علي إكسبريس", "AliExpress"},
	prodNoon:       {"نون", "Noon"},
	prodTemu:       {"تيمو", "Temu"},
}

// productSite is the product site of a page's address ("" for any other page).
func productSite(u string) string {
	h := hostOf(u)
	if h == "" {
		return ""
	}
	has := func(name string) bool {
		// amazon.eg, www.amazon.ae, smile.amazon.com, amazon.co.uk… (not amazonaws.com)
		for _, part := range strings.Split(h, ".") {
			if part == name {
				return true
			}
		}
		return false
	}
	switch {
	case has("amazon") || h == "amzn.eu" || h == "amzn.to" || h == "a.co":
		return prodAmazon
	case has("aliexpress"):
		return prodAliExpress
	case has("alibaba"):
		return prodAlibaba
	case has("noon"):
		return prodNoon
	case has("temu"):
		return prodTemu
	}
	return ""
}

// isProductPage says whether an address on a product site is a product's own page (not
// the home page or a search): the window offers its pictures then.
func isProductPage(u string) bool {
	site := productSite(u)
	p, err := url.Parse(u)
	if err != nil || site == "" {
		return false
	}
	path := strings.ToLower(p.Path)
	switch site {
	case prodAmazon:
		return strings.Contains(path, "/dp/") || strings.Contains(path, "/gp/product/") || strings.Contains(path, "/gp/aw/d/") ||
			p.Host == "amzn.eu" || p.Host == "amzn.to" || p.Host == "a.co"
	case prodAlibaba:
		return strings.Contains(path, "/product-detail/") || strings.Contains(path, "/product/")
	case prodAliExpress:
		return strings.Contains(path, "/item/") || strings.HasPrefix(p.Host, "a.aliexpress") || strings.HasPrefix(p.Host, "s.click.aliexpress")
	case prodNoon:
		return strings.Contains(path, "/p/") || regexp.MustCompile(`/n[0-9a-z]{8,}[a-z]?/p`).MatchString(path)
	case prodTemu:
		return regexp.MustCompile(`-g-\d+\.html`).MatchString(path) || strings.Contains(path, "goods.html") || strings.Contains(path, "/goods")
	}
	return false
}

// ProductMedia is one picture or video of a product.
type ProductMedia struct {
	Kind string `json:"kind"` // image, video
	URL  string `json:"url"`
}

// ProductPage is what was found on a product's page.
type ProductPage struct {
	Site  string         `json:"site"`
	Page  string         `json:"page"`
	Title string         `json:"title"`
	Media []ProductMedia `json:"media"`
}

// ---------------------------------------------------------------------------
// Reading a page
// ---------------------------------------------------------------------------

var (
	reJSONEsc     = strings.NewReplacer(`/`, "/", `/`, "/", `\/`, "/", `&`, "&", `=`, "=", `=`, "=", `:`, ":", `:`, ":")
	reTitleTag    = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	reOGTitle     = regexp.MustCompile(`(?is)<meta[^>]+property=["']og:title["'][^>]+content=["']([^"']+)["']`)
	reOGTitle2    = regexp.MustCompile(`(?is)<meta[^>]+content=["']([^"']+)["'][^>]+property=["']og:title["']`)
	reOGImage     = regexp.MustCompile(`(?is)<meta[^>]+property=["']og:image(?::url)?["'][^>]+content=["']([^"']+)["']`)
	reLDJSON      = regexp.MustCompile(`(?is)<script[^>]+type=["']application/ld\+json["'][^>]*>(.*?)</script>`)
	reVideoSrc    = regexp.MustCompile(`(?is)<(?:video|source)[^>]+src=["'](https?://[^"']+)["']`)
	reAnyMP4      = regexp.MustCompile(`https?://[^"'\s<>\\]+?\.(?:mp4|m3u8)(?:\?[^"'\s<>\\]*)?`)
	reAmzTitle    = regexp.MustCompile(`(?is)<span[^>]+id=["']productTitle["'][^>]*>(.*?)</span>`)
	reAmzHiRes    = regexp.MustCompile(`"(?:hiRes|large|mainUrl)"\s*:\s*"(https://[^"]+)"`)
	reAmzDynamic  = regexp.MustCompile(`data-a-dynamic-image=["']\{([^}]+)\}["']`)
	reAmzImage    = regexp.MustCompile(`https://(?:[a-z0-9-]+\.)*(?:media-amazon|ssl-images-amazon)\.com/images/I/([A-Za-z0-9+%-]{6,})(?:\.[A-Za-z0-9_,%-]+)*\.(jpg|jpeg|png|webp|gif)`)
	reAmzVideo    = regexp.MustCompile(`https://m\.media-amazon\.com/images/S/vse-vms-transcoding-artifact[^"'\s<>\\]+?\.(?:mp4|m3u8)`)
	reAliImage    = regexp.MustCompile(`(?:https?:)?//(?:ae\d*|ae-pic-[a-z0-9]+|s|sc\d*|img|i\d*)\.(?:alicdn\.com|aliexpress-media\.com)/(?:@sc\d+/)?kf/([A-Za-z0-9_-]+)\.(jpg|jpeg|png|webp)`)
	reAliList     = regexp.MustCompile(`"imagePathList"\s*:\s*\[([^\]]*)\]`)
	reAliSku      = regexp.MustCompile(`"(?:skuPropertyImagePath|skuImage|skuPropertyImage)"\s*:\s*"([^"]+)"`)
	reAliVideo    = regexp.MustCompile(`https?://(?:[a-z0-9-]+\.)*(?:aliexpress-media\.com|alicdn\.com|taobao\.com|alibaba\.com|alibabausercontent\.com)/[^"'\s<>\\]+?\.mp4(?:\?[^"'\s<>\\]*)?`)
	reAliVideoIDs = regexp.MustCompile(`"videoUid"\s*:\s*"([A-Za-z0-9_-]+)"[^{}]{0,400}?"videoId"\s*:\s*"?(\d+)"?|"videoId"\s*:\s*"?(\d+)"?[^{}]{0,400}?"videoUid"\s*:\s*"([A-Za-z0-9_-]+)"`)
	reAlibabaImgs = regexp.MustCompile(`"(?:mediaItems|productImages|images|imageList|productImage)"\s*:\s*[\[{]`)
	reNoonKeys    = regexp.MustCompile(`"image_keys"\s*:\s*\[([^\]]*)\]`)
	reNoonImage   = regexp.MustCompile(`https?://(?:[a-z0-9-]+\.)?nooncdn\.com/p/([^"'\s?<>\\]+?)\.(jpg|jpeg|png|webp|avif)`)
	reTemuGallery = regexp.MustCompile(`"(?:gallery|galleryList|detailGallery|mainGallery)"\s*:\s*\[`)
	reTemuImage   = regexp.MustCompile(`https?://img(?:-[a-z0-9]+)?\.kwcdn\.com/(?:product|goods)/[^"'\s<>\\?]+?\.(?:jpg|jpeg|png|webp)`)
	reTemuVideo   = regexp.MustCompile(`https?://[a-z0-9-]+\.kwcdn\.com/[^"'\s<>\\]+?\.mp4(?:\?[^"'\s<>\\]*)?`)
)

// unescapePage makes the addresses written inside a page's scripts (JSON: "\/", "/")
// and attributes ("&amp;") plain.
func unescapePage(s string) string {
	return strings.ReplaceAll(reJSONEsc.Replace(s), "&amp;", "&")
}

// ReadProductPage finds the product's title, pictures and videos in its page.
func ReadProductPage(site, page, doc string) ProductPage {
	text := unescapePage(doc)
	p := ProductPage{Site: site, Page: page, Title: productTitle(site, doc)}
	var images, videos []string
	switch site {
	case prodAmazon:
		images, videos = amazonMedia(text)
	case prodAliExpress:
		images, videos = aliexpressMedia(text)
	case prodAlibaba:
		images, videos = alibabaMedia(text)
	case prodNoon:
		images, videos = noonMedia(text)
	case prodTemu:
		images, videos = temuMedia(text)
	}
	if len(images) == 0 {
		images = ldImages(doc)
	}
	if len(images) == 0 {
		for _, m := range reOGImage.FindAllStringSubmatch(doc, -1) {
			images = append(images, unescapePage(m[1]))
		}
	}
	if len(videos) == 0 {
		for _, m := range reVideoSrc.FindAllStringSubmatch(doc, -1) {
			if u := unescapePage(m[1]); isProductVideoURL(u) {
				videos = append(videos, u)
			}
		}
	}
	seen := map[string]bool{}
	add := func(kind, u string) {
		u = strings.TrimSpace(u)
		if strings.HasPrefix(u, "//") {
			u = "https:" + u
		}
		if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") || seen[u] {
			return
		}
		seen[u] = true
		p.Media = append(p.Media, ProductMedia{Kind: kind, URL: u})
	}
	for _, u := range videos {
		add("video", u)
	}
	for _, u := range images {
		add("image", u)
	}
	if len(p.Media) > 200 {
		p.Media = p.Media[:200]
	}
	return p
}

func isProductVideoURL(u string) bool {
	l := strings.ToLower(u)
	return strings.Contains(l, ".mp4") || strings.Contains(l, ".m3u8")
}

// productTitle is the product's name: the site's own title element, else og:title, else
// <title> without the site's name.
func productTitle(site, doc string) string {
	clean := func(s string) string {
		s = html.UnescapeString(regexp.MustCompile(`(?s)<[^>]+>`).ReplaceAllString(s, " "))
		return strings.Join(strings.Fields(s), " ")
	}
	if site == prodAmazon {
		if m := reAmzTitle.FindStringSubmatch(doc); m != nil {
			if t := clean(m[1]); t != "" {
				return t
			}
		}
	}
	for _, re := range []*regexp.Regexp{reOGTitle, reOGTitle2, reTitleTag} {
		if m := re.FindStringSubmatch(doc); m != nil {
			t := clean(m[1])
			// "Name : Amazon.eg: Electronics", "Name - AliExpress 44", "Name | noon"
			for _, sep := range []string{" : Amazon", ": Amazon", " | Amazon", " - AliExpress", " | AliExpress", " - Alibaba.com", " | Alibaba",
				" | noon", " | Noon", " - Temu", " | Temu", "Amazon.com: ", "Amazon.eg: ", "Amazon.ae: ", "Amazon.sa: "} {
				if i := strings.Index(t, sep); i > 0 {
					t = t[:i]
				} else if i == 0 {
					t = t[len(sep):]
				}
			}
			if t = strings.TrimSpace(t); t != "" {
				return t
			}
		}
	}
	return ""
}

// ldImages are the "image" of the Product described in the page's JSON-LD.
func ldImages(doc string) []string {
	var out []string
	var walk func(v any, inProduct bool)
	walk = func(v any, inProduct bool) {
		switch x := v.(type) {
		case map[string]any:
			t, _ := x["@type"].(string)
			isProd := inProduct || strings.EqualFold(t, "Product")
			if isProd {
				switch im := x["image"].(type) {
				case string:
					out = append(out, im)
				case []any:
					for _, e := range im {
						switch y := e.(type) {
						case string:
							out = append(out, y)
						case map[string]any:
							if u, ok := y["url"].(string); ok {
								out = append(out, u)
							}
						}
					}
				case map[string]any:
					if u, ok := im["url"].(string); ok {
						out = append(out, u)
					}
				}
			}
			for k, e := range x {
				if k != "image" {
					walk(e, false)
				}
			}
		case []any:
			for _, e := range x {
				walk(e, inProduct)
			}
		}
	}
	for _, m := range reLDJSON.FindAllStringSubmatch(doc, -1) {
		var v any
		if json.Unmarshal([]byte(html.UnescapeString(strings.TrimSpace(m[1]))), &v) == nil {
			walk(v, false)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// The sites
// ---------------------------------------------------------------------------

// amazonMedia: the image block's pictures ("hiRes", else "large", for every colour), at
// their original size (the address without its size part), and its videos.
func amazonMedia(text string) (images, videos []string) {
	orig := func(u string) string {
		m := reAmzImage.FindStringSubmatch(u)
		if m == nil {
			return ""
		}
		return "https://m.media-amazon.com/images/I/" + m[1] + "." + m[2]
	}
	seen := map[string]bool{}
	addImg := func(u string) {
		if o := orig(u); o != "" && !seen[o] {
			seen[o] = true
			images = append(images, o)
		}
	}
	for _, m := range reAmzHiRes.FindAllStringSubmatch(text, -1) {
		addImg(m[1])
	}
	if len(images) == 0 {
		// The main picture's sizes, in its attribute.
		for _, m := range reAmzDynamic.FindAllStringSubmatch(text, -1) {
			for _, u := range reAmzImage.FindAllString(html.UnescapeString(m[1]), -1) {
				addImg(u)
			}
		}
	}
	vs := map[string]bool{}
	for _, u := range reAmzVideo.FindAllString(text, -1) {
		if !vs[u] {
			vs[u] = true
			videos = append(videos, u)
		}
	}
	// A video in both forms: the MP4 is enough.
	videos = preferMP4(videos)
	return images, videos
}

func preferMP4(videos []string) []string {
	hasMP4 := false
	for _, v := range videos {
		if strings.Contains(strings.ToLower(v), ".mp4") {
			hasMP4 = true
		}
	}
	if !hasMP4 {
		return videos
	}
	var out []string
	for _, v := range videos {
		if strings.Contains(strings.ToLower(v), ".mp4") {
			out = append(out, v)
		}
	}
	return out
}

// aliOrig is an alicdn picture at its original size: ".../kf/<id>.jpg".
func aliOrig(u string) string {
	m := reAliImage.FindStringSubmatch(u)
	if m == nil {
		return ""
	}
	host := "ae01.alicdn.com"
	if strings.Contains(u, "s.alicdn.com") || strings.Contains(u, "//sc") {
		// Alibaba's own pictures.
		at := ""
		if i := strings.Index(u, "/@sc"); i >= 0 {
			if j := strings.Index(u[i+1:], "/"); j > 0 {
				at = u[i : i+1+j]
			}
		}
		return "https://s.alicdn.com" + at + "/kf/" + m[1] + "." + m[2]
	}
	if i := strings.Index(u, "aliexpress-media.com"); i >= 0 {
		start := strings.Index(u, "//") + 2
		host = u[start : i+len("aliexpress-media.com")]
	}
	return "https://" + host + "/kf/" + m[1] + "." + m[2]
}

// aliexpressMedia: the gallery's list of pictures ("imagePathList"), the pictures of the
// variants, and the video.
func aliexpressMedia(text string) (images, videos []string) {
	seen := map[string]bool{}
	add := func(u string) {
		if o := aliOrig(u); o != "" && !seen[o] {
			seen[o] = true
			images = append(images, o)
		}
	}
	if m := reAliList.FindStringSubmatch(text); m != nil {
		for _, u := range reAliImage.FindAllString(m[1], -1) {
			add(u)
		}
	}
	for _, m := range reAliSku.FindAllStringSubmatch(text, -1) {
		add(m[1])
	}
	if len(images) == 0 {
		// The page as shown (the gallery's pictures, made small).
		for _, u := range reAliImage.FindAllString(galleryPart(text), -1) {
			add(u)
		}
	}
	vs := map[string]bool{}
	for _, u := range reAliVideo.FindAllString(text, -1) {
		if !vs[u] {
			vs[u] = true
			videos = append(videos, u)
		}
	}
	if len(videos) == 0 {
		for _, m := range reAliVideoIDs.FindAllStringSubmatch(text, -1) {
			uid, id := m[1], m[2]
			if uid == "" {
				uid, id = m[4], m[3]
			}
			u := fmt.Sprintf("https://video.aliexpress-media.com/play/u/ae_sg_item/%s/p/1/e/6/t/10301/%s.mp4", uid, id)
			if !vs[u] {
				vs[u] = true
				videos = append(videos, u)
			}
		}
	}
	return images, videos
}

// galleryPart is the beginning of a page, where its product's pictures are (before the
// lists of other products further down), at most 600 kB.
func galleryPart(text string) string {
	if len(text) > 600<<10 {
		return text[:600<<10]
	}
	return text
}

// alibabaMedia: the product's pictures (from its data, else the gallery) and videos.
func alibabaMedia(text string) (images, videos []string) {
	seen := map[string]bool{}
	add := func(u string) {
		if o := aliOrig(u); o != "" && !seen[o] {
			seen[o] = true
			images = append(images, o)
		}
	}
	// The product's own data: the first list of pictures after one of its keys.
	if loc := reAlibabaImgs.FindStringIndex(text); loc != nil {
		end := loc[1] + 20000
		if end > len(text) {
			end = len(text)
		}
		for _, u := range reAliImage.FindAllString(text[loc[0]:end], -1) {
			add(u)
		}
	}
	if len(images) == 0 {
		for _, u := range reAliImage.FindAllString(galleryPart(text), -1) {
			add(u)
		}
	}
	vs := map[string]bool{}
	for _, u := range reAliVideo.FindAllString(text, -1) {
		if !vs[u] {
			vs[u] = true
			videos = append(videos, u)
		}
	}
	return images, videos
}

// noonMedia: the product's "image_keys" (the first list: the product shown), at their
// original size.
func noonMedia(text string) (images, videos []string) {
	seen := map[string]bool{}
	add := func(key, ext string) {
		key = strings.Trim(key, `" `)
		if key == "" {
			return
		}
		if ext == "" || ext == "avif" || ext == "webp" {
			ext = "jpg"
		}
		u := "https://f.nooncdn.com/p/" + key + "." + ext + "?format=" + ext + "&width=original"
		if !seen[key] {
			seen[key] = true
			images = append(images, u)
		}
	}
	if m := reNoonKeys.FindStringSubmatch(text); m != nil {
		for _, k := range strings.Split(m[1], ",") {
			add(k, "jpg")
		}
	}
	if len(images) == 0 {
		for _, m := range reNoonImage.FindAllStringSubmatch(galleryPart(text), -1) {
			if strings.HasPrefix(m[1], "pnsku/") || strings.HasPrefix(m[1], "v") {
				add(m[1], m[2])
			}
		}
	}
	for _, u := range reAnyMP4.FindAllString(text, -1) {
		if strings.Contains(u, "nooncdn.com") {
			videos = append(videos, u)
		}
	}
	return images, videos
}

// temuMedia: the gallery's pictures (without their size part), and the videos.
func temuMedia(text string) (images, videos []string) {
	seen := map[string]bool{}
	add := func(u string) {
		if i := strings.IndexAny(u, "?"); i > 0 {
			u = u[:i]
		}
		if !seen[u] {
			seen[u] = true
			images = append(images, u)
		}
	}
	if loc := reTemuGallery.FindStringIndex(text); loc != nil {
		end := loc[1] + 30000
		if end > len(text) {
			end = len(text)
		}
		part := text[loc[1]:end]
		// The gallery's list only: up to its closing bracket.
		depth := 1
		for i := 0; i < len(part); i++ {
			if part[i] == '[' {
				depth++
			} else if part[i] == ']' {
				depth--
				if depth == 0 {
					part = part[:i]
					break
				}
			}
		}
		for _, u := range reTemuImage.FindAllString(part, -1) {
			add(u)
		}
	}
	if len(images) == 0 {
		for _, u := range reTemuImage.FindAllString(galleryPart(text), -1) {
			add(u)
		}
	}
	vs := map[string]bool{}
	for _, u := range reTemuVideo.FindAllString(text, -1) {
		if !vs[u] {
			vs[u] = true
			videos = append(videos, u)
		}
	}
	return images, videos
}

// ---------------------------------------------------------------------------
// The app: reading, choosing, downloading
// ---------------------------------------------------------------------------

// productRead is a product found, kept for its download.
type productRead struct {
	id      int
	page    ProductPage
	src     FileSource // how the site is asked (the browser's cookies and User-Agent)
	created time.Time
}

type productStore struct {
	mu   sync.Mutex
	seq  int
	list map[int]*productRead
}

func (s *productStore) put(p *productRead) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.list == nil {
		s.list = map[int]*productRead{}
	}
	s.seq++
	p.id = s.seq
	s.list[p.id] = p
	for id := range s.list {
		if id < s.seq-10 {
			delete(s.list, id)
		}
	}
	return p.id
}

func (s *productStore) get(id int) *productRead {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.list[id]
}

// productEvent is what the "product" window shows.
func productEvent(p *productRead) map[string]any {
	names := productSiteNames[p.page.Site]
	site := names[1]
	if curLang() == "ar" {
		site = names[0]
	}
	items := make([]map[string]any, 0, len(p.page.Media))
	for i, m := range p.page.Media {
		items = append(items, map[string]any{"i": i, "kind": m.Kind, "url": m.URL, "stream": strings.Contains(strings.ToLower(m.URL), ".m3u8")})
	}
	return map[string]any{"id": p.id, "status": "ready", "site": p.page.Site, "siteName": site, "title": p.page.Title,
		"page": p.page.Page, "items": items}
}

// readProductHTML reads a product page sent by the extension (the page the user sees).
func (a *App) readProductHTML(site, page, title, doc string, src FileSource) map[string]any {
	pp := ReadProductPage(site, page, doc)
	if title != "" && pp.Title == "" {
		pp.Title = title
	}
	p := &productRead{page: pp, src: src, created: time.Now()}
	a.products.put(p)
	if len(pp.Media) == 0 {
		a.emit.Emit("product", map[string]any{"id": p.id, "status": "error", "site": site, "page": page,
			"error": NewUserError("product_none")})
		return map[string]any{"ok": true, "found": 0}
	}
	a.emit.Emit("product", productEvent(p))
	return map[string]any{"ok": true, "found": len(pp.Media)}
}

// ReadProductLink reads a pasted product link: HELINK asks for the page itself, as a
// browser does. Some sites answer a program with a check page instead: the window then
// says to use the extension's button on the page in the browser. The answer comes as a
// "product" event (status "loading", then "ready" or "error").
func (a *App) ReadProductLink(u string) map[string]any {
	site := productSite(u)
	if site == "" {
		return map[string]any{"error": NewUserError("invalid_url")}
	}
	p := &productRead{page: ProductPage{Site: site, Page: u}, src: FileSource{URL: u, UserAgent: desktopUA}, created: time.Now()}
	id := a.products.put(p)
	a.emit.Emit("product", map[string]any{"id": id, "status": "loading", "site": site, "page": u})
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				a.emit.Emit("product", map[string]any{"id": id, "status": "error", "site": site, "page": u, "error": panicError("product", r)})
			}
		}()
		ctx, cancel := context.WithTimeout(a.ctx, 40*time.Second)
		defer cancel()
		doc, final, err := fetchProductPage(ctx, u)
		if a.ctx.Err() != nil {
			return
		}
		if err != nil {
			log.Printf("product %s: %v", u, err)
			code := "product_blocked"
			if errCode(err) == "network" {
				code = "network"
			}
			a.emit.Emit("product", map[string]any{"id": id, "status": "error", "site": site, "page": u, "error": NewUserError(code)})
			return
		}
		pp := ReadProductPage(site, final, doc)
		p.page = pp
		if len(pp.Media) == 0 {
			code := "product_none"
			if productCheckPage(doc) {
				code = "product_blocked"
			}
			a.emit.Emit("product", map[string]any{"id": id, "status": "error", "site": site, "page": u, "error": NewUserError(code)})
			return
		}
		a.emit.Emit("product", productEvent(p))
	}()
	return map[string]any{"kind": "product", "id": id}
}

// desktopUA is a usual browser's User-Agent (the product sites answer it with the page).
const desktopUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"

// productCheckPage says whether a page is a site's check for robots ("Robot Check",
// a captcha) rather than the product.
func productCheckPage(doc string) bool {
	l := strings.ToLower(doc)
	for _, s := range []string{"captcha", "robot check", "api-services-support@amazon.com", "punish", "slide to verify", "x5secdata", "/_____tmd_____/"} {
		if strings.Contains(l, s) {
			return true
		}
	}
	return false
}

var productHTTP = &http.Client{Timeout: 35 * time.Second}

// fetchProductPage asks for a product page as a browser would (at most 12 MB).
func fetchProductPage(ctx context.Context, u string) (doc, final string, err error) {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", desktopUA)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9,ar;q=0.8")
	req.Header.Set("Upgrade-Insecure-Requests", "1")
	resp, err := productHTTP.Do(req)
	if err != nil {
		return "", "", netError(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 12<<20))
	if err != nil {
		return "", "", netError(err)
	}
	if resp.StatusCode >= 400 {
		return string(b), resp.Request.URL.String(), fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return string(b), resp.Request.URL.String(), nil
}

// ProductDownload downloads the pictures and videos chosen (their numbers in the event)
// into a folder named after the product, in Pictures: "01.jpg", "02.jpg"… and "video 1.mp4"
// (a stream, ".m3u8", is read by yt-dlp first). It answers how many were added, and the
// folder.
func (a *App) ProductDownload(id int, picks []int) map[string]any {
	p := a.products.get(id)
	if p == nil {
		return map[string]any{"error": NewUserError("unknown")}
	}
	sort.Ints(picks)
	title := p.page.Title
	if title == "" {
		names := productSiteNames[p.page.Site]
		title = tr("منتج من ", "Product from ") + tr(names[0], names[1])
	}
	root := a.saveDirFor(catPictures)
	folder := filepath.Join(root, SafeFileBase(title, 80))
	if err := os.MkdirAll(folder, 0o755); err != nil {
		return map[string]any{"error": fileUserError(err, "", folder)}
	}
	img, vid, added := 0, 0, 0
	for _, i := range picks {
		if i < 0 || i >= len(p.page.Media) {
			continue
		}
		m := p.page.Media[i]
		src := FileSource{URL: m.URL, Referer: p.page.Page, UserAgent: p.src.UserAgent, Cookies: p.src.Cookies}
		if src.UserAgent == "" {
			src.UserAgent = desktopUA
		}
		if m.Kind == "video" {
			vid++
			name := fmt.Sprintf(tr("فيديو %d", "video %d"), vid)
			if strings.Contains(strings.ToLower(m.URL), ".m3u8") {
				a.productStream(m.URL, src, folder, name, p.page.Page)
			} else {
				a.addFileTask(src, p.page.Page, name+".mp4", catVideo, folder, FileInfo{Size: -1}, true)
			}
			added++
			continue
		}
		img++
		ext := strings.ToLower(extOf(nameFromURL(m.URL)))
		switch ext {
		case "jpg", "jpeg", "png", "webp", "gif", "avif":
		default:
			ext = "jpg"
		}
		if ext == "jpeg" {
			ext = "jpg"
		}
		a.addFileTask(src, p.page.Page, fmt.Sprintf("%02d.%s", img, ext), catPictures, folder, FileInfo{Size: -1}, true)
		added++
	}
	if added == 0 {
		return map[string]any{"error": NewUserError("unknown")}
	}
	return map[string]any{"added": added, "folder": folder}
}

// productStream downloads a product's video given as a stream (Amazon): yt-dlp reads it,
// and its best quality goes into the product's folder.
func (a *App) productStream(u string, src FileSource, folder, name, page string) {
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		defer func() { _ = recover() }()
		ctx, cancel := context.WithTimeout(a.ctx, 2*time.Minute)
		defer cancel()
		link, _ := linkToRead(u)
		opts := fetchOpts{cookies: src.Cookies, referer: page, userAgent: src.UserAgent, page: page}
		f, _, fail := a.readPost(ctx, 0, link, opts, nil)
		if fail != nil || f == nil || len(f.items) == 0 {
			log.Printf("product video %s: not read", u)
			return
		}
		best := ""
		for _, o := range f.items[0].Options {
			if o.Kind == "video" {
				best = o.Key // the options come best first
				break
			}
		}
		if best == "" {
			return
		}
		a.downloadInto(f, 0, best, folder, name)
	}()
}
