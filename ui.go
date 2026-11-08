package main

import (
	_ "embed"
	"encoding/base64"
	"strings"
)

//go:embed ui/index.html
var indexHTML string

//go:embed ui/fonts/alexandria-arabic-wght-normal.woff2
var fontAlexandriaArabic []byte

//go:embed ui/fonts/alexandria-latin-wght-normal.woff2
var fontAlexandriaLatin []byte

// The owner's photo on the About page.
//
//go:embed ui/owner.jpg
var ownerPhoto []byte

const (
	rangeArabic = "U+0600-06FF,U+0750-077F,U+0870-088E,U+0890-0891,U+0897-08E1,U+08E3-08FF,U+200C-200E,U+2010-2011,U+204F,U+2E41,U+FB50-FDFF,U+FE70-FE74,U+FE76-FEFC"
	rangeLatin  = "U+0000-00FF,U+0131,U+0152-0153,U+02BB-02BC,U+02C6,U+02DA,U+02DC,U+0304,U+0308,U+0329,U+2000-206F,U+20AC,U+2122,U+2190-2193,U+2212,U+2215,U+FEFF,U+FFFD"
)

func fontFace(family, weight, unicodeRange string, data []byte) string {
	return `@font-face{font-family:"` + family + `";font-style:normal;font-display:block;font-weight:` + weight +
		`;src:url(data:font/woff2;base64,` + base64.StdEncoding.EncodeToString(data) + `) format("woff2");unicode-range:` +
		unicodeRange + `;}`
}

// buildHTML returns the whole interface as one self-contained page (fonts and the About
// photo included), so the app works offline and loads instantly.
func buildHTML() string {
	css := fontFace("Alexandria", "100 900", rangeArabic, fontAlexandriaArabic) + "\n" +
		fontFace("Alexandria", "100 900", rangeLatin, fontAlexandriaLatin)
	html := strings.Replace(indexHTML, "/*@FONTS@*/", css, 1)
	return strings.Replace(html, "@OWNER_PHOTO@", "data:image/jpeg;base64,"+base64.StdEncoding.EncodeToString(ownerPhoto), 1)
}
