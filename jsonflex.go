package main

import (
	"bytes"
	"encoding/json"
	"strconv"
)

// yt-dlp's JSON comes from many different sites, and a field that is a number on one
// site can be a string on another. These types accept whatever arrives instead of
// failing the whole video over one odd field.

// jstr is a string; null becomes "" and numbers or booleans become their text.
type jstr string

func (s *jstr) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		*s = ""
		return nil
	}
	if b[0] == '"' {
		var v string
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		*s = jstr(v)
		return nil
	}
	if b[0] == '{' || b[0] == '[' {
		*s = ""
		return nil
	}
	*s = jstr(b)
	return nil
}

// jnum is a number; null, empty or unreadable values become 0.
type jnum float64

func (n *jnum) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	*n = 0
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	if b[0] == '"' {
		var v string
		if json.Unmarshal(b, &v) == nil {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				*n = jnum(f)
			}
		}
		return nil
	}
	if f, err := strconv.ParseFloat(string(b), 64); err == nil {
		*n = jnum(f)
	}
	return nil
}

// jbool is true only for JSON true (or the strings "true"/"1").
type jbool bool

func (v *jbool) UnmarshalJSON(b []byte) error {
	s := string(bytes.TrimSpace(b))
	*v = s == "true" || s == `"true"` || s == "1" || s == `"1"`
	return nil
}

func isJSONNull(b []byte) bool {
	b = bytes.TrimSpace(b)
	return len(b) == 0 || string(b) == "null"
}
