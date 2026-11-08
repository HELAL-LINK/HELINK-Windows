package main

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
)

// The subset of yt-dlp's info JSON that we need.
type ytInfo struct {
	Type          jstr              `json:"_type"`
	ID            jstr              `json:"id"`
	Title         jstr              `json:"title"`
	FullTitle     jstr              `json:"fulltitle"`
	Description   jstr              `json:"description"`
	Uploader      jstr              `json:"uploader"`
	Channel       jstr              `json:"channel"`
	Duration      jnum              `json:"duration"`
	Thumbnail     jstr              `json:"thumbnail"`
	Thumbnails    []ytThumb         `json:"thumbnails"`
	LiveStatus    jstr              `json:"live_status"`
	IsLive        jbool             `json:"is_live"`
	WebpageURL    jstr              `json:"webpage_url"`
	ExtractorKey  jstr              `json:"extractor_key"`
	PlaylistIndex jnum              `json:"playlist_index"`
	Formats       []ytFormat        `json:"formats"`
	Entries       []json.RawMessage `json:"entries"`
}

type ytThumb struct {
	ID     jstr `json:"id"`
	URL    jstr `json:"url"`
	Width  jnum `json:"width"`
	Height jnum `json:"height"`
}

type ytFormat struct {
	FormatID           jstr            `json:"format_id"`
	Ext                jstr            `json:"ext"`
	VCodec             jstr            `json:"vcodec"`
	ACodec             jstr            `json:"acodec"`
	Width              jnum            `json:"width"`
	Height             jnum            `json:"height"`
	FPS                jnum            `json:"fps"`
	TBR                jnum            `json:"tbr"`
	ABR                jnum            `json:"abr"`
	Filesize           jnum            `json:"filesize"`
	FilesizeApprox     jnum            `json:"filesize_approx"`
	FormatNote         jstr            `json:"format_note"`
	Protocol           jstr            `json:"protocol"`
	DynamicRange       jstr            `json:"dynamic_range"`
	Language           jstr            `json:"language"`
	LanguagePreference *float64        `json:"language_preference"`
	HasDRM             json.RawMessage `json:"has_drm"`
}

func (f *ytFormat) id() string     { return string(f.FormatID) }
func (f *ytFormat) vcodec() string { return strings.ToLower(string(f.VCodec)) }
func (f *ytFormat) acodec() string { return strings.ToLower(string(f.ACodec)) }
func (f *ytFormat) note() string   { return strings.ToLower(string(f.FormatNote)) }
func (f *ytFormat) height() float64 {
	return float64(f.Height)
}

var audioOnlyExts = map[string]bool{"m4a": true, "mp3": true, "opus": true, "ogg": true, "oga": true, "aac": true,
	"wav": true, "weba": true, "flac": true, "mka": true}

// hasVideo / hasAudio follow yt-dlp's convention: the string "none" means "definitely absent",
// an empty value means "unknown", which yt-dlp itself treats as present.
func (f *ytFormat) hasVideo() bool {
	v := f.vcodec()
	if v == "none" {
		return false
	}
	if v != "" || f.Width > 0 || f.Height > 0 {
		return true
	}
	return !audioOnlyExts[strings.ToLower(string(f.Ext))]
}

func (f *ytFormat) hasAudio() bool { return f.acodec() != "none" }

func (f *ytFormat) drm() bool {
	s := strings.TrimSpace(string(f.HasDRM))
	return s == "true" || s == `"maybe"`
}

func (f *ytFormat) isUnwanted() bool {
	n := f.note()
	id := strings.ToLower(f.id())
	return f.Ext == "mhtml" || f.Protocol == "mhtml" || strings.Contains(n, "storyboard") ||
		f.drm() || strings.Contains(n, "premium") ||
		strings.HasSuffix(id, "-sr") || strings.Contains(n, "ai-upscaled") ||
		strings.Contains(n, "missing pot") || strings.Contains(n, "unplayable") ||
		strings.Contains(f.vcodec(), "bytevc2")
}

func (f *ytFormat) watermarked() bool { return strings.Contains(f.note(), "watermark") }

// size returns the best known size of the format in bytes (0 if unknown).
func (f *ytFormat) size(duration float64) float64 {
	if f.Filesize > 0 {
		return float64(f.Filesize)
	}
	if f.FilesizeApprox > 0 {
		return float64(f.FilesizeApprox)
	}
	if f.TBR > 0 && duration > 0 {
		return float64(f.TBR) * 1000 / 8 * duration
	}
	return 0
}

func protoRank(p jstr) int {
	switch s := string(p); {
	case s == "https" || s == "http" || s == "":
		return 0
	case strings.HasPrefix(s, "m3u8"):
		return 1
	default:
		return 2
	}
}

// Codec preference for plain Windows playback and for copying to phones/TVs (WhatsApp too):
// H.264 first, then VP9, AV1, HEVC, HDR, and unknown codecs last.
func videoCodecRank(f *ytFormat) int {
	dr := strings.ToUpper(string(f.DynamicRange))
	v := f.vcodec()
	if (dr != "" && dr != "SDR") || strings.HasPrefix(v, "vp09.02") || strings.Contains(f.note(), "hdr") {
		return 4
	}
	switch {
	case strings.HasPrefix(v, "avc") || strings.HasPrefix(v, "h264"):
		return 0
	case strings.HasPrefix(v, "vp9") || strings.HasPrefix(v, "vp09"):
		return 1
	case strings.HasPrefix(v, "av01") || strings.HasPrefix(v, "av1"):
		return 2
	case strings.HasPrefix(v, "h265") || strings.HasPrefix(v, "hevc") || strings.HasPrefix(v, "hvc1") ||
		strings.HasPrefix(v, "hev1") || strings.HasPrefix(v, "bytevc1"):
		return 3
	default:
		return 5
	}
}

func compatNote(rank int) string {
	switch rank {
	case 1, 2:
		return tr("قد لا يعمل على الهواتف والشاشات القديمة", "May not play on older phones and TVs")
	case 3:
		return tr("قد لا يعمل على بعض الأجهزة", "May not play on some devices")
	case 4:
		return tr("فيديو HDR، قد تبدو ألوانه غريبة على الشاشات العادية", "HDR video: colors may look odd on regular screens")
	}
	return ""
}

// descText says what a quality is good for, in the language in use (id: q720, a192, vbest…).
func descText(id string) string {
	t, ok := map[string][2]string{
		"q4320": {"أعلى جودة ممكنة، والحجم ضخم جدًا", "Highest possible quality, huge file"},
		"q2160": {"جودة فائقة، والحجم كبير جدًا", "Top quality, very large file"},
		"q1440": {"جودة عالية جدًا، والحجم كبير", "Very high quality, large file"},
		"q1080": {"جودة عالية للشاشات الكبيرة", "High quality for big screens"},
		"q720":  {"جودة جيدة بحجم مناسب", "Good quality, reasonable size"},
		"q480":  {"جودة متوسطة بحجم أصغر", "Medium quality, smaller file"},
		"q360":  {"جودة عادية تناسب الهاتف", "Standard quality, fine for phones"},
		"q240":  {"جودة منخفضة بحجم صغير", "Low quality, small file"},
		"qlow":  {"أقل جودة وأصغر حجم", "Lowest quality, smallest file"},
		"vbest": {"أفضل جودة متاحة", "Best available quality"},
		"vhd":   {"جودة عالية", "High quality"},
		"vsd":   {"جودة عادية بحجم أصغر", "Standard quality, smaller file"},
		"a320":  {"أعلى جودة للصوت، والحجم أكبر", "Best sound, larger file"},
		"a192":  {"جودة جيدة بحجم مناسب", "Good quality, reasonable size"},
		"a128":  {"حجم صغير يوفّر المساحة", "Small file that saves space"},
	}[id]
	if !ok {
		return ""
	}
	return tr(t[0], t[1])
}

func audioCodecRank(f *ytFormat) int {
	a := f.acodec()
	switch {
	case strings.HasPrefix(a, "mp4a") || strings.HasPrefix(a, "aac") || f.Ext == "m4a":
		return 0
	case strings.HasPrefix(a, "opus"):
		return 1
	default:
		return 2
	}
}

// langPref mirrors yt-dlp: original track 10, default 5, other tracks -1, descriptive audio -10.
func langPref(f *ytFormat) float64 {
	if f.LanguagePreference == nil {
		return -1
	}
	return *f.LanguagePreference
}

func isDRC(f *ytFormat) bool {
	return strings.Contains(strings.ToLower(f.id()), "drc") || strings.Contains(string(f.FormatNote), "DRC")
}

var noteHeightRe = regexp.MustCompile(`(\d{3,4})p`)

var qualityLadder = []int{144, 240, 360, 480, 720, 1080, 1440, 2160, 4320}

// qualityClass returns the "720" in "720p" for a video format, following the site's own label
// when present. For vertical videos (TikTok, Reels, Shorts) the short side counts.
func qualityClass(f *ytFormat) int {
	if m := noteHeightRe.FindStringSubmatch(string(f.FormatNote)); m != nil {
		var n int
		fmt.Sscan(m[1], &n)
		if n >= 100 {
			return snapToLadder(n)
		}
	}
	w, h := float64(f.Width), float64(f.Height)
	switch {
	case w > 0 && h > 0:
		return snapToLadder(int(math.Min(w, h)))
	case h > 0:
		return snapToLadder(int(h))
	}
	return 0
}

func snapToLadder(n int) int {
	// Anything far below 144p is not worth offering.
	if n < 100 {
		return 0
	}
	best, bestDiff := 0, math.MaxFloat64
	for _, l := range qualityLadder {
		d := math.Abs(float64(n - l))
		if d < bestDiff {
			best, bestDiff = l, d
		}
	}
	return best
}

// ---------------------------------------------------------------------------
// What the UI shows
// ---------------------------------------------------------------------------

type VideoMeta struct {
	ID           string
	Title        string
	Channel      string
	Duration     float64
	DurationText string
	Thumbnail    string
	Site         string
}

// MarshalJSON writes the site's name in the language in use.
func (m VideoMeta) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"id": m.ID, "title": m.Title, "channel": m.Channel, "duration": m.Duration,
		"durationText": m.DurationText, "thumbnail": m.Thumbnail, "site": m.Site, "siteName": siteName(m.Site)})
}

type QualityOption struct {
	Key         string
	Kind        string // "video" | "audio"
	Label       string
	Tag         string
	DescID      string // what the quality is good for, as a code written in the language in use when shown
	CompatRank  int    // videoCodecRank of the file, for the note about devices that may not play it
	Height      int
	SizeBytes   int64
	Recommended bool

	FormatSpec   string   // not sent to the page
	SortSpec     string   //
	AudioQuality string   // for MP3: "320K", "192K", "128K"
	Parts        []dlPart //
}

// LabelText is the label shown: "720p", "4K", "192k"; "Video" for a file of unknown size of picture.
func (o QualityOption) LabelText() string {
	if o.Key == "vbest" {
		return tr("فيديو", "Video")
	}
	return o.Label
}

// MarshalJSON writes what the page shows, in the language in use.
func (o QualityOption) MarshalJSON() ([]byte, error) {
	m := map[string]any{"key": o.Key, "kind": o.Kind, "label": o.LabelText(), "tag": o.Tag, "desc": descText(o.DescID),
		"height": o.Height, "sizeBytes": o.SizeBytes, "sizeText": HumanSize(o.SizeBytes), "recommended": o.Recommended}
	if n := compatNote(o.CompatRank); n != "" {
		m["compatNote"] = n
	}
	return json.Marshal(m)
}

// dlPart describes one file yt-dlp will download for an option (video, audio or both in one).
type dlPart struct {
	FormatID string
	Bytes    int64
	IsAudio  bool
}

// Item is one video of a post. Most links have one; a post on X or an Instagram
// carousel can have several.
type Item struct {
	Index    int             `json:"index"` // position inside the post (1-based); 0 for a plain video
	Meta     VideoMeta       `json:"meta"`
	Options  []QualityOption `json:"options"`
	Selected string          `json:"selected"`
	raw      []byte
}

// InfoError is a problem with the video itself (not a crash), shown to the user as-is.
type InfoError struct{ Code string }

func (e *InfoError) Error() string { return "info: " + e.Code }

func parseInfo(data []byte) (*ytInfo, error) {
	var info ytInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

func pickThumbnail(info *ytInfo, site string) string {
	if site == siteYouTube {
		best := ""
		bestW := math.MaxFloat64
		for _, t := range info.Thumbnails {
			w, h := float64(t.Width), float64(t.Height)
			if t.URL == "" || w <= 0 || h <= 0 || strings.Contains(string(t.URL), ".webp") {
				continue
			}
			if ratio := w / h; w >= 320 && ratio > 1.7 && ratio < 1.8 && w < bestW {
				best, bestW = string(t.URL), w
			}
		}
		if best != "" {
			return best
		}
		if videoIDRe.MatchString(string(info.ID)) {
			return "https://i.ytimg.com/vi/" + string(info.ID) + "/mqdefault.jpg"
		}
		return string(info.Thumbnail)
	}
	// Other sites: the smallest picture that is still sharp enough, never an animated cover.
	best := ""
	bestW := math.MaxFloat64
	for _, t := range info.Thumbnails {
		if t.URL == "" || strings.Contains(strings.ToLower(string(t.ID)), "dynamic") {
			continue
		}
		if w := float64(t.Width); w >= 320 && w < bestW {
			best, bestW = string(t.URL), w
		}
	}
	if best != "" {
		return best
	}
	if info.Thumbnail != "" {
		return string(info.Thumbnail)
	}
	for i := len(info.Thumbnails) - 1; i >= 0; i-- {
		t := info.Thumbnails[i]
		if t.URL != "" && !strings.Contains(strings.ToLower(string(t.ID)), "dynamic") {
			return string(t.URL)
		}
	}
	return ""
}

var (
	urlInTextRe  = regexp.MustCompile(`https?://\S+`)
	hashtagRe    = regexp.MustCompile(`(?:^|\s)#[^\s#\d][^\s#]*`) // not "#2", the number of a video in a post
	byUserRe     = regexp.MustCompile(`^(?:Video|Post|Reel) by (.+)$`)
	postNumberRe = regexp.MustCompile(`\s+#\d+$`)
	fbCountsRe   = regexp.MustCompile(`(?i)\d[\d.,]*\s*[KMB]?\s+(?:views|reactions|comments|shares|plays)\b`)
)

// cleanTitle makes social-media captions usable as a title and a file name: no links,
// no wall of hashtags, no "1.2K views | Facebook" noise.
func cleanTitle(site, title, uploader string) string {
	t := strings.TrimSpace(title)
	if site == siteYouTube {
		return t
	}
	t = urlInTextRe.ReplaceAllString(t, " ")
	if m := byUserRe.FindStringSubmatch(strings.TrimSpace(t)); m != nil {
		return siteName(site) + " - " + strings.TrimSpace(m[1])
	}
	if site == siteFacebook {
		var keep []string
		for _, part := range strings.Split(t, "|") {
			p := strings.TrimSpace(part)
			if p == "" || strings.EqualFold(p, "facebook") || strings.HasPrefix(p, "By ") || fbCountsRe.MatchString(p) {
				continue
			}
			keep = append(keep, p)
		}
		t = strings.Join(keep, " - ")
	}
	if noTags := hashtagRe.ReplaceAllString(t, " "); len([]rune(strings.TrimSpace(noTags))) >= 3 {
		t = noTags
	}
	// "#2" is how yt-dlp numbers the videos of one post; the app shows its own numbers.
	t = postNumberRe.ReplaceAllString(t, "")
	t = strings.Join(strings.Fields(t), " ")
	t = strings.Trim(t, " -|·•…")
	if len([]rune(t)) < 2 {
		if uploader != "" {
			return siteName(site) + " - " + uploader
		}
		return tr("فيديو من "+siteName(site), siteName(site)+" video")
	}
	return t
}

// hasWords tells whether a caption says something besides links and hashtags.
func hasWords(s string) bool {
	s = urlInTextRe.ReplaceAllString(s, " ")
	s = hashtagRe.ReplaceAllString(s, " ")
	return len([]rune(strings.Join(strings.Fields(s), " "))) >= 3
}

// firstLine returns the first line of a caption, cut at a word if it is long.
func firstLine(s string, max int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	cut := string(r[:max])
	if i := strings.LastIndex(cut, " "); i > max/2 {
		cut = cut[:i]
	}
	return strings.TrimSpace(cut) + "…"
}

func buildMeta(info *ytInfo, site string) VideoMeta {
	meta := VideoMeta{ID: string(info.ID), Site: site}
	channel := string(info.Channel)
	if channel == "" {
		channel = string(info.Uploader)
	}
	meta.Channel = channel
	title := string(info.Title)
	if title == "" {
		title = string(info.FullTitle)
	}
	// Instagram calls every video "Video by <user>"; the caption says much more.
	if site == siteInstagram && byUserRe.MatchString(strings.TrimSpace(title)) {
		if c := firstLine(string(info.Description), 90); hasWords(c) {
			title = c
		}
	}
	meta.Title = cleanTitle(site, title, channel)
	// X puts the author's name in front of the post's text; the name is already shown under it.
	if site == siteX && channel != "" {
		if rest := strings.TrimPrefix(meta.Title, channel+" - "); rest != meta.Title && len([]rune(rest)) >= 2 {
			meta.Title = rest
		}
	}
	meta.Duration = float64(info.Duration)
	meta.DurationText = HumanDuration(meta.Duration)
	meta.Thumbnail = pickThumbnail(info, site)
	return meta
}

func recommendedKey(opts []QualityOption, kind string) string {
	for _, o := range opts {
		if o.Recommended && (kind == "" || o.Kind == kind) {
			return o.Key
		}
	}
	for _, o := range opts {
		if kind == "" || o.Kind == kind {
			return o.Key
		}
	}
	return ""
}

// BuildPost reads yt-dlp's answer for a link: one video, or several for a post that has more.
func BuildPost(data []byte, linkSite string) (string, []Item, error) {
	info, err := parseInfo(data)
	if err != nil {
		return linkSite, nil, err
	}
	site := siteFromExtractor(string(info.ExtractorKey), linkSite)
	isList := info.Type == "playlist" || info.Type == "multi_video" || len(info.Entries) > 0
	if !isList {
		meta, opts, err := BuildOptions(info, site)
		if err != nil {
			return site, nil, err
		}
		return site, []Item{{Meta: meta, Options: opts, Selected: recommendedKey(opts, ""), raw: data}}, nil
	}
	if site == siteYouTube {
		return site, nil, &InfoError{"playlist"}
	}
	var items []Item
	refs, live := 0, 0
	for i, raw := range info.Entries {
		if isJSONNull(raw) {
			continue // a photo of the post, or an entry that failed
		}
		e, err := parseInfo(raw)
		if err != nil {
			continue
		}
		if e.Type == "url" || e.Type == "url_transparent" {
			refs++
			continue
		}
		meta, opts, err := BuildOptions(e, siteFromExtractor(string(e.ExtractorKey), site))
		if ie, ok := err.(*InfoError); ok && ie.Code == "live" {
			live++
		}
		if err != nil || len(opts) == 0 {
			continue
		}
		idx := int(e.PlaylistIndex)
		if idx <= 0 {
			idx = i + 1
		}
		items = append(items, Item{Index: idx, Meta: meta, Options: opts, Selected: recommendedKey(opts, ""), raw: raw})
		if len(items) >= 30 {
			break
		}
	}
	if len(items) == 0 {
		switch {
		case live > 0:
			return site, nil, &InfoError{"live"}
		case refs > 0:
			return site, nil, &InfoError{"profile"}
		}
		return site, nil, &InfoError{"novideo"}
	}
	return site, items, nil
}

// BuildOptions turns yt-dlp's format list into the short list of choices the user sees.
func BuildOptions(info *ytInfo, site string) (VideoMeta, []QualityOption, error) {
	meta := buildMeta(info, site)
	duration := meta.Duration

	switch info.LiveStatus {
	case "is_live":
		return meta, nil, &InfoError{"live"}
	case "is_upcoming":
		return meta, nil, &InfoError{"upcoming"}
	}
	if info.IsLive {
		return meta, nil, &InfoError{"live"}
	}

	var audios, videos []*ytFormat
	for i := range info.Formats {
		f := &info.Formats[i]
		if f.FormatID == "" || f.isUnwanted() {
			continue
		}
		switch {
		case f.hasVideo():
			videos = append(videos, f)
		case f.hasAudio():
			audios = append(audios, f)
		}
	}
	// TikTok's watermarked copy only if there is nothing else.
	if clean := filterFormats(videos, func(f *ytFormat) bool { return !f.watermarked() }); len(clean) > 0 {
		videos = clean
	}
	// TikTok's separate "music" track is the song used, not necessarily what the video sounds
	// like; its videos always carry their own sound.
	if site == siteTikTok && len(filterFormats(videos, (*ytFormat).hasAudio)) > 0 {
		audios = nil
	}

	// Best audio: original language track, not DRC, plain https, AAC preferred (plays everywhere in MP4).
	sort.SliceStable(audios, func(i, j int) bool {
		a, b := audios[i], audios[j]
		if la, lb := langPref(a), langPref(b); la != lb {
			return la > lb
		}
		if da, db := isDRC(a), isDRC(b); da != db {
			return !da
		}
		if pa, pb := protoRank(a.Protocol), protoRank(b.Protocol); pa != pb {
			return pa < pb
		}
		if ca, cb := audioCodecRank(a), audioCodecRank(b); ca != cb {
			return ca < cb
		}
		return math.Max(float64(a.ABR), float64(a.TBR)) > math.Max(float64(b.ABR), float64(b.TBR))
	})
	var bestAudio *ytFormat
	if len(audios) > 0 {
		bestAudio = audios[0]
	}

	// A video with no sound at all (a GIF on X, for example): offer the picture alone and no MP3.
	silent := bestAudio == nil && len(filterFormats(videos, (*ytFormat).hasAudio)) == 0
	if site == siteX && bestAudio == nil && duration == 0 && len(videos) > 0 &&
		len(filterFormats(videos, func(f *ytFormat) bool { return f.id() != "http" })) == 0 {
		silent = true // X serves GIFs as one soundless MP4 named "http"
	}

	byQ := map[int][]*ytFormat{}
	var unknown []*ytFormat
	for _, f := range videos {
		if q := qualityClass(f); q > 0 {
			byQ[q] = append(byQ[q], f)
		} else {
			unknown = append(unknown, f)
		}
	}
	var qs []int
	for q := range byQ {
		qs = append(qs, q)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(qs)))

	var opts []QualityOption
	for _, q := range qs {
		cands := usableVideos(byQ[q], bestAudio, silent)
		if len(cands) == 0 {
			continue
		}
		sortVideos(cands)
		o := videoOption(cands[0], bestAudio, silent, duration)
		o.Key, o.Height = fmt.Sprintf("v%d", q), q
		o.Label, o.Tag, o.DescID = qualityWords(q)
		o.SortSpec = fmt.Sprintf("res:%d,vcodec:h264,acodec:aac", q)
		opts = append(opts, o)
	}

	// Formats without a known size of picture (Facebook's "hd" and "sd" files, or a single
	// unnamed file) are only offered when nothing better is known.
	if len(opts) == 0 {
		cands := usableVideos(unknown, bestAudio, silent)
		sortVideos(cands)
		var hd, sd, other *ytFormat
		for _, f := range cands {
			switch strings.ToLower(f.id()) {
			case "hd":
				if hd == nil {
					hd = f
				}
			case "sd":
				if sd == nil {
					sd = f
				}
			default:
				if other == nil {
					other = f
				}
			}
		}
		if hd == nil && sd == nil && other != nil {
			o := videoOption(other, bestAudio, silent, duration)
			o.Key, o.Label, o.DescID = "vbest", "Video", "vbest"
			opts = append(opts, o)
		}
		if hd != nil {
			o := videoOption(hd, bestAudio, silent, duration)
			o.Key, o.Label, o.Tag, o.DescID = "vhd", "HD", "", "vhd"
			opts = append(opts, o)
		}
		if sd != nil {
			o := videoOption(sd, bestAudio, silent, duration)
			o.Key, o.Label, o.Tag, o.DescID = "vsd", "SD", "", "vsd"
			opts = append(opts, o)
		}
	}

	if len(opts) == 0 && (bestAudio == nil || silent) {
		return meta, nil, &InfoError{"noformats"}
	}

	// Recommended: 720p, otherwise the closest quality below it, otherwise the lowest available.
	rec := -1
	for i, o := range opts {
		if o.Height > 0 && o.Height <= 720 {
			rec = i
			break
		}
	}
	if rec == -1 && len(opts) > 0 {
		rec = len(opts) - 1
		if opts[0].Height == 0 {
			rec = 0 // HD before SD
		}
	}
	if rec >= 0 {
		opts[rec].Recommended = true
	}

	if !silent {
		opts = append(opts, audioOptions(opts, rec, bestAudio, duration)...)
	}
	return meta, opts, nil
}

func filterFormats(in []*ytFormat, keep func(*ytFormat) bool) []*ytFormat {
	var out []*ytFormat
	for _, f := range in {
		if keep(f) {
			out = append(out, f)
		}
	}
	return out
}

// usableVideos keeps the formats that give a complete file: with sound, or without sound
// when a separate sound track can be added (or when the video has no sound at all).
func usableVideos(cands []*ytFormat, bestAudio *ytFormat, silent bool) []*ytFormat {
	if bestAudio != nil || silent {
		return append([]*ytFormat(nil), cands...)
	}
	return filterFormats(cands, (*ytFormat).hasAudio)
}

func sortVideos(cands []*ytFormat) {
	group := func(f *ytFormat) int {
		combined := f.hasAudio()
		p := protoRank(f.Protocol)
		switch {
		case !combined && p == 0:
			return 0 // separate video over https (merged with audio afterwards)
		case combined && p == 0:
			return 1 // ready-made video+audio file (e.g. 360p "18")
		case !combined:
			return 2
		default:
			return 3 // pre-merged HLS
		}
	}
	isDamaged := func(f *ytFormat) bool { return strings.Contains(f.note(), "damaged") }
	sort.SliceStable(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		// A file whose codec is unknown might be one Windows cannot play (X serves some as HEVC).
		if ka, kb := a.vcodec() != "", b.vcodec() != ""; ka != kb {
			return ka
		}
		if ga, gb := group(a), group(b); ga != gb {
			return ga < gb
		}
		if da, db := isDamaged(a), isDamaged(b); da != db {
			return !da
		}
		if ca, cb := videoCodecRank(a), videoCodecRank(b); ca != cb {
			return ca < cb
		}
		if a.FPS != b.FPS {
			return a.FPS > b.FPS
		}
		return a.TBR > b.TBR
	})
}

func videoOption(v, bestAudio *ytFormat, silent bool, duration float64) QualityOption {
	o := QualityOption{Kind: "video"}
	vSize := int64(v.size(duration))
	switch {
	case v.hasAudio() || silent:
		o.FormatSpec = v.id() + "/b"
		if silent {
			o.FormatSpec = v.id() + "/bv*/b"
		}
		o.SizeBytes = vSize
		o.Parts = []dlPart{{FormatID: v.id(), Bytes: vSize}}
	default:
		aSize := int64(bestAudio.size(duration))
		o.FormatSpec = v.id() + "+" + bestAudio.id() + "/bv*+ba/b"
		if vSize > 0 && aSize > 0 {
			o.SizeBytes = vSize + aSize
		}
		o.Parts = []dlPart{{FormatID: v.id(), Bytes: vSize}, {FormatID: bestAudio.id(), Bytes: aSize, IsAudio: true}}
	}
	if o.CompatRank = videoCodecRank(v); o.CompatRank > 4 {
		o.CompatRank = 0
	}
	return o
}

// audioOptions offers MP3 in three sizes. The sound comes from the separate sound track
// when there is one, otherwise from the recommended video file.
func audioOptions(videoOpts []QualityOption, rec int, bestAudio *ytFormat, duration float64) []QualityOption {
	var src string
	var srcSize int64
	spec := "ba/b"
	switch {
	case bestAudio != nil:
		src, srcSize = bestAudio.id(), int64(bestAudio.size(duration))
		spec = src + "/ba/b"
	case rec >= 0 && len(videoOpts[rec].Parts) == 1:
		src, srcSize = videoOpts[rec].Parts[0].FormatID, videoOpts[rec].Parts[0].Bytes
		spec = src + "/b"
	}
	var out []QualityOption
	for _, kbps := range []int{320, 192, 128} {
		o := QualityOption{
			Key: fmt.Sprintf("mp3-%d", kbps), Kind: "audio",
			Label: fmt.Sprintf("%dk", kbps), Tag: "MP3", DescID: fmt.Sprintf("a%d", kbps),
			Recommended:  kbps == 192,
			FormatSpec:   spec,
			SortSpec:     "acodec:aac",
			AudioQuality: fmt.Sprintf("%dK", kbps),
			Parts:        []dlPart{{FormatID: src, Bytes: srcSize, IsAudio: true}},
		}
		if duration > 0 {
			o.SizeBytes = int64(duration * float64(kbps) * 1000 / 8)
		}
		out = append(out, o)
	}
	return out
}

// qualityWords returns the label, the small tag and the description's code for a quality.
func qualityWords(q int) (label, tag, descID string) {
	switch {
	case q >= 4320:
		return "8K", fmt.Sprintf("%dp", q), "q4320"
	case q >= 2160:
		return "4K", "2160p", "q2160"
	case q >= 1440:
		return "2K", "1440p", "q1440"
	case q >= 1080:
		return "1080p", "Full HD", "q1080"
	case q >= 720:
		return "720p", "HD", "q720"
	case q >= 480:
		return "480p", "", "q480"
	case q >= 360:
		return "360p", "", "q360"
	case q >= 240:
		return "240p", "", "q240"
	default:
		return fmt.Sprintf("%dp", q), "", "qlow"
	}
}
