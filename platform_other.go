//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"syscall"
)

// Non-Windows builds exist only for tests and the development preview.

func dataBaseDir() string {
	if d := os.Getenv("NAZZELHA_DATA"); d != "" {
		return d
	}
	d, err := os.UserCacheDir()
	if err != nil {
		d = os.TempDir()
	}
	return filepath.Join(d, "nazzelha-dev")
}

func defaultSaveDir() string {
	if d := os.Getenv("NAZZELHA_SAVE"); d != "" {
		return d
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, "Downloads", "HELINK")
}

func folderLabel(dir string) string {
	clean := filepath.Clean(dir)
	if clean == filepath.Clean(defaultSaveDir()) {
		return tr("التنزيلات ← HELINK", "Downloads › HELINK")
	}
	if parent := filepath.Dir(clean); parent != clean && isCategoryFolder(filepath.Base(clean)) {
		if pl := folderLabel(parent); pl != "/" && pl != "." {
			// A kind's folder in the save folder. The mark keeps the order of Latin names
			// following each other in an Arabic window.
			return pl + tr(" \u200f← ", " › ") + filepath.Base(clean)
		}
	}
	return filepath.Base(dir)
}

func freeSpace(dir string) (int64, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, false
	}
	return int64(st.Bavail) * int64(st.Bsize), true
}

func hideFolder(string) {}

func dirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".helink-check-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}

// Development preview: use yt-dlp and ffmpeg from PATH, optionally with a local info JSON
// instead of asking YouTube.
func devToolsEnabled() bool { return os.Getenv("NAZZELHA_DEV_TOOLS") == "1" }

// devListJSON: a list's answer read from a file instead of asking YouTube (development).
func devListJSON(t *Tools) string {
	if t.devMode {
		return os.Getenv("NAZZELHA_DEV_LISTJSON")
	}
	return ""
}

func devInfoJSON(t *Tools) string {
	if t.devMode {
		return os.Getenv("NAZZELHA_DEV_INFOJSON")
	}
	return ""
}

// moveToTrash deletes the file (the development preview has no Recycle Bin).
func moveToTrash(path string) error { return os.Remove(path) }
