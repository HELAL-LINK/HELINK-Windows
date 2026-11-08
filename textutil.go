package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

// ---------------------------------------------------------------------------
// File names
// ---------------------------------------------------------------------------

var reservedNames = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// SafeFileBase turns a video title into a Windows-safe file name (without extension).
func SafeFileBase(title string, maxRunes int) string {
	return safeBaseOr(title, maxRunes, tr("فيديو", "video"))
}

// SafeName is SafeFileBase for any file: "download" when nothing usable is left.
func SafeName(name string, maxRunes int) string {
	return safeBaseOr(name, maxRunes, "download")
}

func safeBaseOr(title string, maxRunes int, fallback string) string {
	var b strings.Builder
	for _, r := range title {
		switch {
		case r == '/' || r == '\\' || r == '|':
			b.WriteString(" - ")
		case r == ':':
			b.WriteString(" - ")
		case r == '"':
			b.WriteRune('\'')
		case r == '<' || r == '>' || r == '?' || r == '*':
			b.WriteRune(' ')
		case r < 0x20 || r == 0x7f:
			b.WriteRune(' ')
		// Invisible direction marks and zero-width characters confuse file names.
		case (r >= 0x200B && r <= 0x200F) || (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069) || r == 0xFEFF:
			// drop
		case unicode.IsSpace(r):
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	s := strings.Join(strings.Fields(b.String()), " ")
	// Collapse the " - - " sequences produced by replacements.
	for strings.Contains(s, "- -") {
		s = strings.ReplaceAll(s, "- -", "-")
	}
	if maxRunes > 0 {
		r := []rune(s)
		if len(r) > maxRunes {
			s = strings.TrimSpace(string(r[:maxRunes]))
		}
	}
	s = strings.Trim(s, " .-")
	if s == "" {
		s = fallback
	}
	if reservedNames[strings.ToUpper(strings.SplitN(s, ".", 2)[0])] {
		s = "_" + s
	}
	return s
}

// UniquePath returns dir/base.ext, or dir/base (2).ext ... if that file already exists.
func UniquePath(dir, base, ext string) string {
	p := filepath.Join(dir, base+"."+ext)
	if !pathExists(p) {
		return p
	}
	for i := 2; i < 1000; i++ {
		p = filepath.Join(dir, fmt.Sprintf("%s (%d).%s", base, i, ext))
		if !pathExists(p) {
			return p
		}
	}
	return p
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// EscapeOutputTemplate makes literal text safe inside a yt-dlp output template.
func EscapeOutputTemplate(s string) string {
	return strings.ReplaceAll(s, "%", "%%")
}

// ---------------------------------------------------------------------------
// Sizes, speeds and times in words (Arabic or English, see i18n.go)
// ---------------------------------------------------------------------------

const (
	kib = 1024.0
	mib = 1024.0 * 1024
	gib = 1024.0 * 1024 * 1024
)

func trimFloat(v float64, decimals int) string {
	s := strconv.FormatFloat(v, 'f', decimals, 64)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}

// HumanSize: "850 كيلوبايت" / "850 KB", "6.2 ميجابايت" / "6.2 MB", "85 ميجابايت", "1.4 جيجابايت".
func HumanSize(b int64) string {
	f := float64(b)
	switch {
	case b <= 0:
		return ""
	case f < mib:
		return trimFloat(math.Max(1, math.Round(f/kib)), 0) + tr(" كيلوبايت", " KB")
	case f < 10*mib:
		return trimFloat(f/mib, 1) + tr(" ميجابايت", " MB")
	case f < 1000*mib:
		return trimFloat(math.Round(f/mib), 0) + tr(" ميجابايت", " MB")
	default:
		return trimFloat(f/gib, 1) + tr(" جيجابايت", " GB")
	}
}

// HumanSpeed: "2.3 ميجابايت/ث" / "2.3 MB/s".
func HumanSpeed(bytesPerSec float64) string {
	if bytesPerSec <= 0 || math.IsNaN(bytesPerSec) || math.IsInf(bytesPerSec, 0) {
		return ""
	}
	if bytesPerSec < mib {
		return trimFloat(math.Max(1, math.Round(bytesPerSec/kib)), 0) + tr(" كيلوبايت/ث", " KB/s")
	}
	return trimFloat(bytesPerSec/mib, 1) + tr(" ميجابايت/ث", " MB/s")
}

// HumanDuration: "4:05", "12:34", "1:02:03".
func HumanDuration(sec float64) string {
	if sec <= 0 || math.IsNaN(sec) {
		return ""
	}
	s := int64(math.Round(sec))
	h, m, ss := s/3600, (s%3600)/60, s%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, ss)
	}
	return fmt.Sprintf("%d:%02d", m, ss)
}

// Minutes after "نحو": دقيقة، دقيقتين، 3 دقائق، 15 دقيقة.
func minutesAr(n int) string {
	switch {
	case n == 1:
		return "دقيقة"
	case n == 2:
		return "دقيقتين"
	case n >= 3 && n <= 10:
		return fmt.Sprintf("%d دقائق", n)
	default:
		return fmt.Sprintf("%d دقيقة", n)
	}
}

func hoursAr(n int) string {
	switch {
	case n == 1:
		return "ساعة"
	case n == 2:
		return "ساعتين"
	case n >= 3 && n <= 10:
		return fmt.Sprintf("%d ساعات", n)
	default:
		return fmt.Sprintf("%d ساعة", n)
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// HumanETA: "متبقٍ بضع ثوانٍ", "متبقٍ أقل من دقيقة", "متبقٍ نحو 5 دقائق", "متبقٍ نحو ساعة و20 دقيقة";
// in English "A few seconds left", "About 5 minutes left", "About 1 hour 20 min left".
func HumanETA(sec float64) string {
	if sec < 0 || math.IsNaN(sec) || math.IsInf(sec, 0) {
		return ""
	}
	en := isEnglish()
	switch {
	case sec < 10:
		return tr("متبقٍ بضع ثوانٍ", "A few seconds left")
	case sec < 60:
		return tr("متبقٍ أقل من دقيقة", "Less than a minute left")
	case sec < 3600:
		m := int(math.Round(sec / 60))
		if en {
			return "About " + plural(m, "minute", "minutes") + " left"
		}
		return "متبقٍ نحو " + minutesAr(m)
	}
	total := int(math.Round(sec / 60))
	h, m := total/60, total%60
	m = int(math.Round(float64(m)/5) * 5)
	if m == 60 {
		h, m = h+1, 0
	}
	if en {
		s := "About " + plural(h, "hour", "hours")
		if m > 0 {
			s += fmt.Sprintf(" %d min", m)
		}
		return s + " left"
	}
	s := "متبقٍ نحو " + hoursAr(h)
	if m > 0 {
		s += " و" + minutesAr(m)
	}
	return s
}
