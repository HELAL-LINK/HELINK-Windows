package main

import "sync/atomic"

// The language the app speaks: Modern Standard Arabic (the default) or English. Texts
// are written when they are shown, so switching the language changes everything at
// once, downloads already in the list included.

var english atomic.Bool

// setLang switches to English for "en" and to Arabic for anything else.
func setLang(lang string) { english.Store(lang == "en") }

// curLang is "ar" or "en".
func curLang() string {
	if english.Load() {
		return "en"
	}
	return "ar"
}

func isEnglish() bool { return english.Load() }

// tr returns the text in the language in use.
func tr(ar, en string) string {
	if english.Load() {
		return en
	}
	return ar
}
