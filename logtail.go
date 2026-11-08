package main

import (
	"io"
	"os"
	"strings"
)

// logTail returns the last lines of the app's log, for "copy details for support".
func logTail(path string, lines int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	const maxBytes = 24 << 10
	if fi, err := f.Stat(); err == nil && fi.Size() > maxBytes {
		_, _ = f.Seek(fi.Size()-maxBytes, io.SeekStart)
	}
	b, _ := io.ReadAll(f)
	return tailLines(strings.ToValidUTF8(string(b), ""), lines)
}
