//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// appFolder is the name of the app's own folder in %LOCALAPPDATA% and of the folder in
// Downloads where files are saved.
const appFolder = "HELINK"

func dataBaseDir() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		if d, err := os.UserCacheDir(); err == nil {
			base = d
		} else {
			base = os.TempDir()
		}
	}
	dir := filepath.Join(base, appFolder)
	// The app was called Helal up to version 1.1 and kept its tools in "Nazzelha": take
	// them over instead of downloading them again.
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if fi, err := os.Stat(filepath.Join(base, "Nazzelha")); err == nil && fi.IsDir() {
			_ = os.Rename(filepath.Join(base, "Nazzelha"), dir)
		}
	}
	return dir
}

func knownFolder(id *windows.KNOWNFOLDERID) string {
	p, err := windows.KnownFolderPath(id, 0)
	if err != nil {
		return ""
	}
	return p
}

func downloadsFolder() string {
	if p := knownFolder(windows.FOLDERID_Downloads); p != "" {
		return p
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, "Downloads")
	}
	return os.TempDir()
}

// defaultSaveDir is Downloads\HELINK (made when the first file is saved).
func defaultSaveDir() string { return filepath.Join(downloadsFolder(), appFolder) }

func sameDir(a, b string) bool {
	return a != "" && b != "" && strings.EqualFold(strings.TrimRight(filepath.Clean(a), `\`), strings.TrimRight(filepath.Clean(b), `\`))
}

// folderLabel names a folder the way people know it, in the language in use.
func folderLabel(dir string) string {
	clean := strings.TrimRight(filepath.Clean(dir), `\`)
	if l, ok := knownFolderLabel(clean); ok {
		return l
	}
	name := filepath.Base(clean)
	if parent := filepath.Dir(clean); !sameDir(parent, clean) && isCategoryFolder(name) {
		if pl := folderLabel(parent); pl != `\` && pl != "." {
			// A kind's folder in the save folder. The mark keeps the order of Latin names
			// following each other in an Arabic window.
			return pl + tr(" \u200f← ", " › ") + name
		}
	}
	if sameDir(filepath.Dir(clean), downloadsFolder()) {
		return tr("التنزيلات ← ", "Downloads › ") + name
	}
	if vol := filepath.VolumeName(dir); len(vol) == 2 {
		return name + tr(" (القرص ", " (drive ") + strings.ToUpper(vol[:1]) + ")"
	}
	return name
}

// knownFolderLabel names HELINK's folder, the folders of Windows and the drives.
func knownFolderLabel(clean string) (string, bool) {
	if sameDir(clean, defaultSaveDir()) {
		return tr("التنزيلات ← HELINK", "Downloads › HELINK"), true
	}
	known := []struct {
		id     *windows.KNOWNFOLDERID
		ar, en string
	}{
		{windows.FOLDERID_Downloads, "التنزيلات (Downloads)", "Downloads"},
		{windows.FOLDERID_Desktop, "سطح المكتب (Desktop)", "Desktop"},
		{windows.FOLDERID_Videos, "الفيديوهات (Videos)", "Videos"},
		{windows.FOLDERID_Music, "الموسيقى (Music)", "Music"},
		{windows.FOLDERID_Documents, "المستندات (Documents)", "Documents"},
	}
	for _, k := range known {
		if p := knownFolder(k.id); sameDir(p, clean) {
			return tr(k.ar, k.en), true
		}
	}
	if vol := filepath.VolumeName(clean); len(vol) == 2 && strings.EqualFold(clean, vol) {
		return tr("القرص ", "Drive ") + strings.ToUpper(vol[:1]), true
	}
	return "", false
}

func freeSpace(dir string) (int64, bool) {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, false
	}
	var free, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &free, &total, &totalFree); err != nil {
		return 0, false
	}
	return int64(free), true
}

func hideFolder(dir string) {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return
	}
	if attrs, err := windows.GetFileAttributes(p); err == nil {
		_ = windows.SetFileAttributes(p, attrs|windows.FILE_ATTRIBUTE_HIDDEN)
	}
}

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

func devToolsEnabled() bool     { return false }
func devInfoJSON(*Tools) string { return "" }
func devListJSON(*Tools) string { return "" }

var procSHFileOperationW = windows.NewLazySystemDLL("shell32.dll").NewProc("SHFileOperationW")

type shFileOp struct {
	hwnd          uintptr
	wFunc         uint32
	pFrom         *uint16
	pTo           *uint16
	fFlags        uint16
	aborted       int32
	nameMappings  uintptr
	progressTitle *uint16
}

// moveToTrash puts a file in the Recycle Bin (so "delete the file too" can be undone).
func moveToTrash(path string) error {
	from, err := windows.UTF16FromString(path)
	if err != nil {
		return err
	}
	from = append(from, 0) // the list of files ends with two zeros
	op := shFileOp{wFunc: 3 /* FO_DELETE */, pFrom: &from[0],
		fFlags: 0x40 | 0x10 | 0x4 | 0x400 /* ALLOWUNDO, NOCONFIRMATION, SILENT, NOERRORUI */}
	if r, _, _ := procSHFileOperationW.Call(uintptr(unsafe.Pointer(&op))); r != 0 {
		// No Recycle Bin on that drive (a USB stick?), or another problem: delete it.
		return os.Remove(path)
	}
	if _, err := os.Stat(path); err == nil {
		return os.Remove(path)
	}
	return nil
}
