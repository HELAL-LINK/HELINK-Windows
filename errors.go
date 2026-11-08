package main

import (
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"
)

// UserError is what the user sees when something goes wrong: a plain message in Modern
// Standard Arabic or English, no jargon. The words are written when the error is shown
// (Title, Message), so a message already on screen or saved with a download follows a
// change of language.
type UserError struct {
	Code    string
	Details string
	// Retry: offer a "try again" button.
	Retry bool
	// Action hints the UI about the best next step: "", "newLink", "chooseFolder", "chooseQuality", "setup".
	Action string
	// Outdated: a newer yt-dlp might fix it, so the app updates and retries once on its own.
	Outdated bool
	// JSRuntime: the YouTube helper (Deno) looks broken or too old.
	JSRuntime bool
	// Site the message talks about ("" for "the site").
	Site string
	// For "disk": bytes the file needs and bytes free (-1 when unknown), and the folder.
	Need, Free int64
	Folder     string
}

func (e *UserError) Error() string { return e.Code + ": " + e.Message() }

// MarshalJSON writes the error for the page, in the language in use.
func (e UserError) MarshalJSON() ([]byte, error) {
	type out struct {
		Code    string `json:"code"`
		Title   string `json:"title"`
		Message string `json:"message"`
		Details string `json:"details,omitempty"`
		Retry   bool   `json:"retry"`
		Action  string `json:"action,omitempty"`
	}
	return json.Marshal(out{Code: e.Code, Title: e.Title(), Message: e.Message(), Details: e.Details, Retry: e.Retry, Action: e.Action})
}

func (e *UserError) Title() string {
	t := errorTexts(e.Code)
	if isEnglish() {
		return e.fill(t[2])
	}
	return e.fill(t[0])
}

func (e *UserError) Message() string {
	if e.Code == "disk" && e.Need > 0 && e.Free >= 0 {
		where := tr("مكان الحفظ", "the save folder")
		if e.Folder != "" {
			// Isolated, so a name that mixes Arabic and Latin letters stays in one piece.
			where = "\u2068" + folderLabel(e.Folder) + "\u2069"
		}
		if isEnglish() {
			return "This file needs about " + HumanSize(e.Need) + " of free space, but only " + HumanSize(e.Free) +
				" is free in " + where + ". Choose another place with more space, or delete some files and try again."
		}
		return "يحتاج الملف إلى نحو " + HumanSize(e.Need) + " من المساحة الفارغة، والمتاح في «" + where + "» " + HumanSize(e.Free) +
			" فقط. اختر مكانًا آخر فيه مساحة، أو احذف بعض الملفات ثم أعد المحاولة."
	}
	t := errorTexts(e.Code)
	if isEnglish() {
		return e.fill(t[3])
	}
	return e.fill(t[1])
}

// fill puts the site's name in: {site}, or {Site} at the start of an English sentence.
func (e *UserError) fill(s string) string {
	name := siteName(e.Site)
	if _, known := siteNames[e.Site]; !known && strings.Contains(e.Site, ".") {
		name = "\u2068" + name + "\u2069" // a web address keeps its order inside Arabic text
	}
	capName := name
	if r, n := utf8.DecodeRuneInString(name); n > 0 {
		capName = string(unicode.ToUpper(r)) + name[n:]
	}
	return strings.NewReplacer("{Site}", capName, "{site}", name).Replace(s)
}

// Localize sets the site the message is about ("تعذّر الوصول إلى يوتيوب").
func (e *UserError) Localize(site string) *UserError {
	e.Site = site
	return e
}

type errRule struct {
	code    string
	needles []string
}

// Order matters: the first rule that matches wins.
var errRules = []errRule{
	// A share link that ended on a login page.
	{"login", []string{"facebook.com/login", "instagram.com/accounts/login", "tiktok.com/login"}},
	{"invalid_url", []string{"is not a valid url", "unsupported url"}},
	{"novideo", []string{"no video could be found in this tweet", "there is no video in this post", "is not a video", "no video in this post"}},
	{"private", []string{"private video", "this video is private", "video is private", "protected tweet",
		"registered users who follow", "account is private", "do not have permission to view this post"}},
	{"age", []string{"confirm your age", "age-restricted", "age restricted", "inappropriate for some users"}},
	{"members", []string{"members-only", "members only", "join this channel", "channel's members", "available to this channel"}},
	{"premium", []string{"music premium", "youtube premium", "premium members"}},
	{"upcoming", []string{"premieres in", "will begin in", "live event will begin", "premiere will begin", "this live event will",
		"has not yet started", "not started yet"}},
	{"geo", []string{"not available in your country", "geo restrict", "geo-restrict", "in your country",
		"not made this video available in your", "ip address is blocked"}},
	{"drm", []string{"drm protected", "is drm", "drm-protected"}},
	{"bot", []string{"not a bot", "confirm you’re not", "confirm you're not", "unusual traffic"}},
	{"ig_limit", []string{"rate-limit reached or login required", "rate-limit for accessing posts anonymously",
		"instagram sent an empty media response"}},
	{"ratelimit", []string{"http error 429", "too many requests", "rate-limit", "rate limit", "ratelimit"}},
	{"login", []string{"--cookies", "login required", "log in to continue", "you must log in", "requires authentication",
		"requiring login", "log in for access", "only available for registered users"}},
	{"clock", []string{"certificate verify failed", "certificate_verify_failed", "certificate has expired", "certificate is not yet valid", "not yet valid"}},
	{"disk", []string{"no space left", "errno 28", "not enough space", "disk full", "winerror 112", "there is not enough space", "disk quota exceeded"}},
	{"permission", []string{"permission denied", "errno 13", "access is denied", "read-only file system", "errno 30",
		"winerror 5]", "winerror 5)", "being used by another process", "winerror 32"}},
	{"noaudio", []string{"does not contain any stream", "unable to obtain file audio codec", "matches no streams"}},
	{"unavailable", []string{"video unavailable", "this video is unavailable", "this video has been removed", "has been terminated",
		"no longer available", "does not exist", "removed for violating", "copyright", "video is not available", "content isn't available",
		"requested tweet is unavailable", "facebook said", "video not available", "post isn't available", "is unavailable",
		"broadcast no longer exists", "space not found", "livestream has ended"}},
	{"ffmpeg", []string{"ffmpeg not found", "ffprobe and ffmpeg not found", "ffmpeg is not installed", "ffmpeg could not be found", "ffmpeg exited with code"}},
	{"jsruntime", []string{"javascript runtime", "js runtime", "jsc", "challenge solving failed", "challenge solver", "deno"}},
	{"network", []string{"getaddrinfo failed", "could not resolve host", "name or service not known", "failed to resolve",
		"temporary failure in name resolution", "nodename nor servname", "no address associated with hostname",
		"network is unreachable", "no route to host", "timed out", "timeout", "winerror 10060", "winerror 10061", "winerror 10065",
		"winerror 11001", "winerror 10051", "winerror 10054", "connection refused", "connection reset", "connection aborted",
		"software caused connection abort", "broken pipe", "eof occurred in violation of protocol",
		"remote end closed connection", "unable to connect", "failed to establish a new connection", "max retries exceeded",
		"network error", "curl: (6)", "curl: (7)", "curl: (28)", "curl: (35)", "curl: (56)", "transporterror", "incompleteread"}},
	{"forbidden", []string{"http error 403", "403: forbidden", "403 forbidden"}},
	{"format", []string{"requested format is not available", "no video formats found", "format is not available"}},
	{"extractor", []string{"unable to extract", "please report this issue", "unable to download api page", "unable to download webpage",
		"failed to parse", "cannot parse data", "guest token", "while querying api", "unexpected response from webpage",
		"unable to solve js challenge"}},
}

// ClassifyFor maps yt-dlp's error output to a friendly message about the given site.
func ClassifyFor(output, site string) *UserError {
	e := ClassifyOutput(output)
	if site != siteYouTube {
		switch {
		case e.Code == "jsruntime":
			// The YouTube helper is not involved on other sites.
			d := e.Details
			e = NewUserError("extractor")
			e.Details = d
		case e.Code == "format" && strings.Contains(strings.ToLower(output), "no video formats found"):
			d := e.Details
			e = NewUserError("novideo")
			e.Details = d
		}
	}
	return e.Localize(site)
}

// ClassifyOutput maps yt-dlp's error output to a friendly message.
func ClassifyOutput(output string) *UserError {
	lower := strings.ToLower(output)
	// Prefer what yt-dlp itself calls an ERROR; warnings are only a fallback.
	errLines := []string{}
	for _, l := range strings.Split(lower, "\n") {
		if strings.Contains(l, "error") {
			errLines = append(errLines, l)
		}
	}
	scopes := []string{strings.Join(errLines, "\n"), lower}
	for _, scope := range scopes {
		if strings.TrimSpace(scope) == "" {
			continue
		}
		for _, r := range errRules {
			for _, n := range r.needles {
				if strings.Contains(scope, n) {
					e := NewUserError(r.code)
					e.Details = tailLines(output, 25)
					return e
				}
			}
		}
	}
	e := NewUserError("unknown")
	e.Details = tailLines(output, 25)
	return e
}

func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// NewUserError makes the error for a known code (an unknown code becomes "unknown").
func NewUserError(code string) *UserError {
	if code == "not_youtube" {
		code = "unsupported_site"
	}
	if _, ok := errTexts[code]; !ok {
		code = "unknown"
	}
	e := &UserError{Code: code, Retry: true, Need: -1, Free: -1}
	switch code {
	case "invalid_url", "unsupported_site", "playlist", "profile", "novideo", "private", "login", "age", "members",
		"premium", "geo", "drm", "unavailable", "nothing_found", "channel", "list_empty":
		e.Retry, e.Action = false, "newLink"
	case "update_none", "update_manual":
		e.Retry = false
	case "page_not_file":
		e.Retry, e.Action = false, "openPage"
	case "noaudio":
		e.Retry, e.Action = false, "chooseQuality"
	case "upcoming", "live":
		e.Action = "newLink"
	case "disk", "permission":
		e.Action = "chooseFolder"
	case "folder_missing":
		e.Retry, e.Action = false, "chooseFolder"
	case "sep_no_audio", "sep_unreadable":
		e.Retry = false
	case "format":
		e.Action = "chooseQuality"
	case "ffmpeg", "tools_missing":
		e.Action = "setup"
	}
	switch code {
	case "jsruntime":
		e.JSRuntime, e.Outdated = true, true
	case "forbidden", "format", "noformats", "extractor", "unknown":
		e.Outdated = true
	}
	return e
}

// errorTexts returns {Arabic title, Arabic message, English title, English message}.
func errorTexts(code string) [4]string {
	if t, ok := errTexts[code]; ok {
		return t
	}
	return errTexts["unknown"]
}

var errTexts = map[string][4]string{
	"invalid_url": {"هذا الرابط غير صالح",
		"هذا ليس رابط فيديو أو ملف. افتح الفيديو أو الملف وانسخ رابطه من شريط العنوان أو من زر «مشاركة»، ثم الصقه هنا.",
		"This link doesn't work",
		"This isn't a link to a video or a file. Open it, copy its link from the address bar or the Share button, and paste it here."},
	"nothing_found": {"لا يوجد ما يمكن تنزيله هنا",
		"لم يجد HELINK ملفًا أو فيديو في هذا الرابط. تأكد من الرابط، أو افتح الصفحة في المتصفح واضغط زر HELINK الذي يظهر على الفيديو.",
		"Nothing to download here",
		"HELINK found no file or video at this link. Check the link, or open the page in your browser and click the HELINK button that appears on the video."},
	"link_expired": {"انتهت صلاحية رابط التنزيل",
		"رفض {site} إكمال التنزيل، وغالبًا انتهت صلاحية الرابط. ابدأ تنزيل الملف مرة أخرى من الموقع في المتصفح، وسيُكمل HELINK من حيث توقف.",
		"The download link expired",
		"{Site} refused to continue the download; the link has probably expired. Start downloading the file again from the website in your browser, and HELINK will continue where it stopped."},
	"file_gone": {"الملف غير موجود",
		"لم يعد هذا الملف موجودًا على {site}، أو تغيّر رابطه. افتح الرابط في المتصفح للتأكد من أنه يعمل.",
		"File not found",
		"This file is no longer on {site}, or its link changed. Open the link in your browser to check that it works."},
	"server_busy": {"لا يستجيب {site} الآن",
		"الموقع مشغول أو متوقف مؤقتًا. حاول مرة أخرى بعد قليل.",
		"{Site} isn't answering",
		"The site is busy or down for a while. Try again in a little while."},
	"page_not_file": {"هذا الرابط لصفحة وليس لملف",
		"فتح الرابط صفحة ويب بدلًا من الملف، وغالبًا يطلب الموقع تسجيل الدخول أو انتهت صلاحية الرابط. افتحه في المتصفح وابدأ التنزيل من هناك، وسيلتقطه HELINK.",
		"This link opens a page, not a file",
		"The link opened a web page instead of the file; the site probably wants you to sign in, or the link expired. Open it in your browser and start the download there, and HELINK will take it."},
	"file_changed": {"تغيّر الملف على الموقع",
		"تغيّر الملف على {site} أثناء تنزيله. حاول مرة أخرى ليُنزَّل من البداية.",
		"The file changed on the site",
		"The file changed on {site} while it was downloading. Try again to download it from the start."},
	"unsupported_site": {"لا يوجد ما يمكن تنزيله هنا",
		"لم يجد HELINK ملفًا أو فيديو في هذا الرابط. تأكد من الرابط، أو افتح الصفحة في المتصفح واضغط زر HELINK الذي يظهر على الفيديو.",
		"Nothing to download here",
		"HELINK found no file or video at this link. Check the link, or open the page in your browser and click the HELINK button that appears on the video."},
	"playlist": {"هذا رابط قائمة تشغيل",
		"لتنزيل القائمة كاملة، الصق رابطها في HELINK من «إضافة رابط». ولتنزيل فيديو واحد منها، افتحه وانسخ رابطه هو.",
		"This is a playlist link",
		"To download the whole list, paste its link in HELINK with Add a link. To download one of its videos, open it and copy its own link."},
	"channel": {"هذا رابط قناة كاملة",
		"افتح قائمة التشغيل أو الكورس الذي تريده في القناة وانسخ رابطه، أو افتح الفيديو نفسه وانسخ رابطه.",
		"This is a whole channel",
		"Open the playlist or course you want on the channel and copy its link, or open the video itself and copy its link."},
	"list_empty": {"لا توجد فيديوهات يمكن تنزيلها في هذه القائمة",
		"القائمة فارغة، أو كل فيديوهاتها خاصة أو محذوفة.",
		"No videos to download in this list",
		"The list is empty, or all its videos are private or removed."},
	"update_check": {"تعذّر التحقق من التحديثات",
		"تأكد من اتصالك بالإنترنت، ثم حاول مرة أخرى.",
		"Couldn't check for updates",
		"Check your internet connection, then try again."},
	"update_verify": {"ملف التحديث غير سليم",
		"لم يطابق ملف التحديث النسخة الأصلية، لذلك لم يُثبَّت حفاظًا على جهازك. حاول مرة أخرى لاحقًا.",
		"The update file isn't right",
		"The update didn't match the original, so it wasn't installed, to keep your computer safe. Try again later."},
	"update_network": {"تعذّر تنزيل التحديث",
		"انقطع الاتصال أثناء تنزيل التحديث. تأكد من اتصالك بالإنترنت ثم حاول مرة أخرى، وسيُكمل التنزيل من حيث توقف.",
		"Couldn't download the update",
		"The connection dropped while the update was downloading. Check your internet connection and try again; it continues where it stopped."},
	"update_disk": {"المساحة غير كافية للتحديث",
		"لا توجد مساحة كافية على القرص C لتنزيل التحديث. احذف بعض الملفات، ثم حاول مرة أخرى.",
		"Not enough space for the update",
		"There isn't enough space on drive C to download the update. Delete some files, then try again."},
	"update_failed": {"تعذّر تثبيت التحديث",
		"حدثت مشكلة أثناء تثبيت التحديث. حاول مرة أخرى، أو نزّل الإصدار الجديد من صفحة HELINK وثبّته.",
		"Couldn't install the update",
		"Something went wrong while installing the update. Try again, or download the new version from HELINK's page and install it."},
	"update_manual": {"لا يمكن تحديث هذه النسخة تلقائيًا",
		"HELINK موجود في مجلد لا يسمح ويندوز بتغييره. نزّل الإصدار الجديد من صفحة HELINK وثبّته.",
		"This copy can't update itself",
		"HELINK is in a folder Windows won't let it change. Download the new version from HELINK's page and install it."},
	"update_none": {"لا يوجد تحديث الآن",
		"لديك أحدث إصدار من HELINK.",
		"No update right now",
		"You have the latest version of HELINK."},
	"profile": {"هذا رابط حساب وليس فيديو",
		"هذا الرابط لصفحة أو لحساب كامل. افتح الفيديو نفسه وانسخ رابطه.",
		"This is a profile, not a video",
		"This link is for a page or a whole account. Open the video itself and copy its link."},
	"novideo": {"لا يحتوي هذا المنشور على فيديو",
		"هذا الرابط لمنشور يضم صورًا أو نصًا فقط. إذا كان المنشور يحتوي على فيديو، فافتح الفيديو نفسه وانسخ رابطه.",
		"This post has no video",
		"This link is for a post with only photos or text. If the post has a video, open the video itself and copy its link."},
	"noaudio": {"هذا الفيديو بلا صوت",
		"لذلك لا يمكن تنزيله بصيغة MP3، لكن يمكنك تنزيله فيديو بدون صوت.",
		"This video has no sound",
		"So it can't be saved as MP3, but you can download it as a video without sound."},
	"private": {"هذا الفيديو خاص",
		"جعله صاحبه متاحًا له ولمن يختارهم فقط، لذلك لا يمكن تنزيله.",
		"This video is private",
		"Its owner shared it only with people they chose, so it can't be downloaded."},
	"login": {"يتطلب هذا الفيديو تسجيل الدخول",
		"لا يسمح {site} بتنزيل هذا الفيديو دون حساب. يُنزّل التطبيق الفيديوهات التي يستطيع أي شخص مشاهدتها دون تسجيل الدخول.",
		"This video requires signing in",
		"{Site} doesn't allow downloading this video without an account. The app downloads videos that anyone can watch without signing in."},
	"ig_limit": {"يرفض إنستجرام إتاحة الفيديو الآن",
		"يحدث هذا إذا كان الحساب خاصًا، أو عندما يطلب إنستجرام تسجيل الدخول بعد طلبات كثيرة متتالية. إذا كان الحساب عامًا، فانتظر ربع ساعة ثم حاول مرة أخرى.",
		"Instagram won't share the video right now",
		"This happens when the account is private, or when Instagram asks for a sign-in after many requests in a row. If the account is public, wait 15 minutes and try again."},
	"age": {"يتطلب هذا الفيديو تسجيل الدخول",
		"يطلب {site} التحقق من عمر المشاهد لهذا الفيديو، لذلك لا يمكن تنزيله دون حساب.",
		"This video requires signing in",
		"{Site} needs to check the viewer's age for this video, so it can't be downloaded without an account."},
	"members": {"هذا الفيديو للمشتركين فقط",
		"هذا الفيديو متاح لأعضاء القناة المشتركين فقط، لذلك لا يمكن تنزيله.",
		"This video is for members only",
		"This video is only for the channel's paying members, so it can't be downloaded."},
	"premium": {"هذا الفيديو لمشتركي بريميوم",
		"هذا الفيديو متاح لمشتركي يوتيوب بريميوم فقط، لذلك لا يمكن تنزيله.",
		"This video is for Premium members",
		"This video is only for YouTube Premium members, so it can't be downloaded."},
	"upcoming": {"لم يُعرض الفيديو بعد",
		"لم يبدأ عرض هذا الفيديو حتى الآن. انتظر حتى يبدأ ثم حاول مرة أخرى.",
		"This video isn't out yet",
		"This video hasn't premiered yet. Wait until it starts, then try again."},
	"live": {"هذا بث مباشر جارٍ",
		"لا يمكن تنزيل البث المباشر أثناء عرضه. انتظر حتى ينتهي البث ثم حاول مرة أخرى.",
		"This is a live stream",
		"A live stream can't be downloaded while it's on. Wait until it ends, then try again."},
	"geo": {"الفيديو غير متاح في بلدك",
		"منع صاحب الفيديو أو {site} عرضه في بلدك، لذلك لا يمكن تنزيله من هنا.",
		"This video isn't available in your country",
		"The owner or {site} blocked it in your country, so it can't be downloaded here."},
	"drm": {"هذا الفيديو محمي",
		"هذا الفيديو محمي ضد النسخ (مثل الأفلام المدفوعة)، ولا يمكن تنزيله.",
		"This video is protected",
		"This video has copy protection (like paid movies) and can't be downloaded."},
	"unavailable": {"الفيديو غير موجود",
		"حُذف هذا الفيديو أو أصبح غير متاح على {site}. تأكد أولًا من أنه يعمل لديك.",
		"Video not found",
		"This video was removed or isn't available on {site}. Check that it opens for you first."},
	"bot": {"يريد {site} التأكد من أنك لست روبوتًا",
		"أوقف {site} هذا الطلب مؤقتًا. انتظر قليلًا ثم حاول مرة أخرى، وإذا استمرت المشكلة فجرّب الاتصال بشبكة إنترنت أخرى.",
		"{Site} wants to check you're not a robot",
		"{Site} paused this request for now. Wait a little and try again. If it keeps happening, try another internet connection."},
	"ratelimit": {"يطلب {site} الانتظار قليلًا",
		"أُرسلت طلبات كثيرة متتالية فأوقفها {site} مؤقتًا. انتظر ربع ساعة ثم حاول مرة أخرى.",
		"{Site} asks us to wait a bit",
		"Too many requests in a row, so {site} paused them for a while. Wait 15 minutes and try again."},
	"clock": {"توجد مشكلة في تاريخ الكمبيوتر",
		"غالبًا تاريخ الكمبيوتر أو ساعته غير صحيح، وهذا يمنع الاتصال الآمن بالمواقع. اضبط التاريخ والساعة تلقائيًا من إعدادات ويندوز، ثم حاول مرة أخرى.",
		"There's a problem with the computer's date",
		"The computer's date or time is probably wrong, which blocks secure connections to websites. Set the date and time automatically in Windows Settings, then try again."},
	"network": {"تعذّر الوصول إلى {site}",
		"تأكد من اتصالك بالإنترنت، ثم حاول مرة أخرى.",
		"Can't reach {site}",
		"Check your internet connection, then try again."},
	"disk": {"المساحة غير كافية",
		"المساحة الفارغة في مكان الحفظ لا تكفي. اختر مكانًا آخر فيه مساحة، أو اختر جودة أقل.",
		"Not enough space",
		"There isn't enough free space where files are saved. Choose another place with more space, or a lower quality."},
	"permission": {"تعذّر الحفظ في هذا المكان",
		"لم يسمح ويندوز بالحفظ في هذا المجلد. اختر مكانًا آخر مثل «التنزيلات» أو «سطح المكتب».",
		"Can't save in this place",
		"Windows didn't allow saving in this folder. Choose another place, like Downloads or Desktop."},
	"ffmpeg": {"جزء من التطبيق مفقود",
		"بعض أدوات التطبيق مفقودة أو تالفة، وغالبًا حذفها برنامج الحماية. ستُجهَّز من جديد خلال دقائق.",
		"Part of the app is missing",
		"Some of the app's tools are missing or damaged, often removed by an antivirus. They'll be set up again in a few minutes."},
	"tools_missing": {"جزء من التطبيق مفقود",
		"بعض أدوات التطبيق مفقودة أو تالفة، وغالبًا حذفها برنامج الحماية. ستُجهَّز من جديد خلال دقائق.",
		"Part of the app is missing",
		"Some of the app's tools are missing or damaged, often removed by an antivirus. They'll be set up again in a few minutes."},
	"jsruntime": {"مساعد يوتيوب يحتاج إلى تحديث",
		"مساعد يوتيوب في التطبيق لا يعمل جيدًا. حاول مرة أخرى وسنحاول تحديثه.",
		"The YouTube helper needs an update",
		"The app's YouTube helper isn't working well. Try again and we'll try to update it."},
	"forbidden": {"رفض {site} التنزيل",
		"رفض {site} هذا الطلب، وغالبًا سيحل التحديث المشكلة. حاول مرة أخرى.",
		"{Site} refused the download",
		"{Site} refused this request. An update will probably fix it, so try again."},
	"format": {"هذه الجودة غير متاحة",
		"لم تعد الجودة التي اخترتها متاحة. حاول مرة أخرى أو اختر جودة أخرى.",
		"This quality isn't available",
		"The quality you chose is no longer available. Try again or choose another quality."},
	"noformats": {"لا توجد جودات لهذا الفيديو",
		"لم يُرجع {site} أي جودة لهذا الفيديو. حاول مرة أخرى بعد قليل.",
		"No qualities found for this video",
		"{Site} didn't return any quality for this video. Try again in a little while."},
	"extractor": {"غيّر {site} طريقة عمله",
		"يغيّر {site} طريقة عمله من وقت لآخر. سيُحدَّث التطبيق، فحاول مرة أخرى.",
		"{Site} changed something",
		"{Site} changes how it works from time to time. The app will update, so try again."},
	"setup_failed": {"لم يكتمل التجهيز",
		"غالبًا انقطع الإنترنت أثناء تنزيل أدوات التطبيق. تأكد من اتصالك بالإنترنت، ثم اضغط «إعادة المحاولة» ليُستأنف التنزيل من حيث توقف.",
		"Setup didn't finish",
		"The internet probably dropped while the app's tools were downloading. Check your connection, then click Try again to continue where it stopped."},
	"setup_blocked": {"برنامج الحماية منع إحدى الأدوات",
		"نُزّلت إحدى أدوات التطبيق لكنها لا تعمل، وغالبًا أوقفها برنامج الحماية (مكافح الفيروسات). حاول مرة أخرى، وإذا استمرت المشكلة فأضف التطبيق إلى الاستثناءات في برنامج الحماية.",
		"Your antivirus blocked a tool",
		"One of the app's tools was downloaded but won't run, probably stopped by your antivirus. Try again; if it keeps happening, add the app to your antivirus exceptions."},
	"folder_missing": {"مجلد الحفظ غير موجود",
		"المجلد الذي اخترته للحفظ غير موجود (ربما فُصلت ذاكرة USB). اختر مكانًا آخر.",
		"Save folder not found",
		"The folder you chose for saving isn't there (maybe a USB drive was removed). Choose another place."},
	"expired": {"انتهت مدة هذه القائمة",
		"اضغط زر التنزيل على الفيديو مرة أخرى لتظهر الجودات من جديد.",
		"This menu has expired",
		"Click the download button on the video again to see its qualities."},
	"sep_setup": {"تعذّر تجهيز إزالة الموسيقى",
		"يُنزِّل HELINK أداة إزالة الموسيقى مرة واحدة فقط (نحو 64 ميجابايت)، لكن تنزيلها لم يكتمل. تأكد من اتصالك بالإنترنت، ثم حاول مرة أخرى.",
		"Couldn't set up music removal",
		"HELINK downloads its music remover only once (about 64 MB), but the download didn't finish. Check your internet connection, then try again."},
	"sep_disk": {"المساحة غير كافية",
		"لا توجد مساحة كافية على القرص C لتجهيز أداة إزالة الموسيقى (تحتاج إلى نحو 110 ميجابايت). احذف بعض الملفات، ثم حاول مرة أخرى.",
		"Not enough space",
		"There isn't enough space on drive C to set up the music remover (it needs about 110 MB). Delete some files, then try again."},
	"sep_no_audio": {"لا يوجد صوت في هذا الملف",
		"هذا الملف بلا صوت، لذلك لا توجد فيه موسيقى لإزالتها.",
		"This file has no sound",
		"This file has no sound, so there's no music to remove."},
	"sep_unreadable": {"تعذّرت قراءة هذا الملف",
		"قد يكون الملف تالفًا، أو ليس ملف صوت أو فيديو. جرّب ملفًا آخر.",
		"Can't read this file",
		"The file may be damaged, or it isn't an audio or video file. Try another file."},
	"sep_failed": {"تعذّرت إزالة الموسيقى",
		"حدثت مشكلة أثناء إزالة الموسيقى. حاول مرة أخرى، وإذا استمرت المشكلة فأرسل التفاصيل إلى فريق الدعم.",
		"Couldn't remove the music",
		"Something went wrong while removing the music. Try again; if it keeps happening, send the details to support."},
	"file_missing": {"الملف غير موجود",
		"لم يعد الملف الأصلي في مكانه؛ ربما نُقل أو حُذف، أو فُصلت ذاكرة USB التي عليها. أعده إلى مكانه ثم حاول مرة أخرى، أو أضفه من مكانه الجديد.",
		"File not found",
		"The original file is no longer where it was: it may have been moved or deleted, or it's on a USB drive that was removed. Put it back and try again, or add it from its new place."},
	"unknown": {"حدثت مشكلة",
		"تعذّر الإكمال هذه المرة. حاول مرة أخرى، وإذا استمرت المشكلة فأرسل التفاصيل إلى فريق الدعم.",
		"Something went wrong",
		"We couldn't finish this time. Try again; if it keeps happening, send the details to support."},
}
