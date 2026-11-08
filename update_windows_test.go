//go:build windows

package main

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows/registry"
)

// This test program also plays the programs an update starts (the installer, the new
// HELINK.exe): started with HELINK_WIN_HELPER=<file>|<milliseconds>, it notes how it was
// started (its file and arguments) in that file, waits that long, and ends.
func TestMain(m *testing.M) {
	if spec := os.Getenv("HELINK_WIN_HELPER"); spec != "" {
		os.Exit(winHelper(spec))
	}
	os.Exit(m.Run())
}

func winHelper(spec string) int {
	file, ms, _ := strings.Cut(spec, "|")
	exe, _ := os.Executable()
	f, err := os.OpenFile(file, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return 3
	}
	fmt.Fprintf(f, "%s\t%s\n", exe, strings.Join(os.Args[1:], " "))
	f.Close()
	if n, err := strconv.Atoi(ms); err == nil {
		time.Sleep(time.Duration(n) * time.Millisecond)
	}
	return 0
}

// helperLines waits until the helpers wrote n lines in file.
func helperLines(t *testing.T, file string, n int) []string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		b, _ := os.ReadFile(file)
		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		if len(b) > 0 && len(lines) >= n {
			return lines
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d programs started: %q", len(b), n, b)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// removeWhenFree removes a program once it no longer runs (Windows keeps it while it does).
func removeWhenFree(t *testing.T, p string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		err := os.Remove(p)
		if err == nil || errors.Is(err, os.ErrNotExist) {
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("%s still in use: %v", p, err)
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func copyTestFile(t *testing.T, from, to string) []byte {
	t.Helper()
	b, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, b, 0o755); err != nil {
		t.Fatal(err)
	}
	return b
}

// The installed copy is recognised by the uninstaller beside it, or by the folder the installer
// noted; anything else is a portable copy.
func TestWinUpdateTarget(t *testing.T) {
	exe, _ := os.Executable()
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	dir := filepath.Dir(exe)
	// The installer's note: kept as it was (a HELINK may be installed on this computer).
	_, err := registry.OpenKey(registry.CURRENT_USER, `Software\HELINK`, registry.QUERY_VALUE)
	keyWas := err == nil
	k, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\HELINK`, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	old, _, oldErr := k.GetStringValue("InstallDir")
	defer func() {
		if oldErr == nil {
			k.SetStringValue("InstallDir", old)
		} else {
			k.DeleteValue("InstallDir")
		}
		k.Close()
		if !keyWas {
			registry.DeleteKey(registry.CURRENT_USER, `Software\HELINK`)
		}
	}()
	k.DeleteValue("InstallDir")

	if got, err := updateTarget(); err != nil || got.Kind != "portable" || !strings.EqualFold(got.Exe, exe) {
		t.Errorf("alone: %+v %v", got, err)
	}
	un := filepath.Join(dir, "Uninstall HELINK.exe")
	if _, err := os.Stat(un); err == nil {
		t.Fatalf("%s is there already", un)
	}
	os.WriteFile(un, []byte("MZ"), 0o644)
	got, err := updateTarget()
	os.Remove(un)
	if err != nil || got.Kind != "setup" {
		t.Errorf("beside its uninstaller: %+v %v", got, err)
	}
	k.SetStringValue("InstallDir", dir+`\`)
	if got, err := updateTarget(); err != nil || got.Kind != "setup" {
		t.Errorf("where the installer put it: %+v %v", got, err)
	}
	k.SetStringValue("InstallDir", filepath.Join(dir, "elsewhere"))
	if got, err := updateTarget(); err != nil || got.Kind != "portable" {
		t.Errorf("another folder installed: %+v %v", got, err)
	}
}

// The installer is started on its own, quietly, told to open HELINK again.
func TestWinApplySetup(t *testing.T) {
	exe, _ := os.Executable()
	dir := t.TempDir()
	setup := filepath.Join(dir, "HELINK-9.9.9-Setup.exe")
	copyTestFile(t, exe, setup)
	marker := filepath.Join(dir, "started.txt")
	t.Setenv("HELINK_WIN_HELPER", marker+"|0")
	if err := applyUpdate(updateTargetInfo{Kind: "setup", Exe: `C:\nowhere\HELINK.exe`}, setup); err != nil {
		t.Fatal(err)
	}
	line := helperLines(t, marker, 1)[0]
	if !strings.EqualFold(strings.Split(line, "\t")[0], setup) || !strings.HasSuffix(line, "\t/S /RELAUNCH") {
		t.Errorf("started as %q", line)
	}
	removeWhenFree(t, setup)
}

// A portable copy: the new HELINK.exe takes the running one's place (renamed .old), and is
// started, told which HELINK it replaces; the one that runs goes on running meanwhile.
func TestWinApplyPortable(t *testing.T) {
	exe, _ := os.Executable()
	dir := t.TempDir()
	cur := filepath.Join(dir, "HELINK.exe")
	body := copyTestFile(t, exe, cur)
	newBody := append(append([]byte{}, body...), []byte("HELINK 9.9.9")...)
	zipPath := filepath.Join(dir, "HELINK-9.9.9-portable.zip")
	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	for name, b := range map[string][]byte{"HELINK-9.9.9-portable/HELINK-guide-en.txt": []byte("guide"),
		"HELINK-9.9.9-portable/HELINK.exe": newBody} {
		w, _ := zw.Create(name)
		w.Write(b)
	}
	zw.Close()
	os.WriteFile(zipPath, zb.Bytes(), 0o644)

	marker := filepath.Join(dir, "started.txt")
	t.Setenv("HELINK_WIN_HELPER", marker+"|2500")
	running := exec.Command(cur)
	running.Dir = dir
	if err := running.Start(); err != nil {
		t.Fatal(err)
	}
	helperLines(t, marker, 1)
	t.Setenv("HELINK_WIN_HELPER", marker+"|0")
	if err := applyUpdate(updateTargetInfo{Kind: "portable", Exe: cur}, zipPath); err != nil {
		t.Fatal(err)
	}
	lines := helperLines(t, marker, 2)
	if want := "\t--updated=" + strconv.Itoa(os.Getpid()); !strings.EqualFold(strings.Split(lines[1], "\t")[0], cur) || !strings.HasSuffix(lines[1], want) {
		t.Errorf("the new one started as %q", lines[1])
	}
	if b, _ := os.ReadFile(cur); !bytes.Equal(b, newBody) {
		t.Error("HELINK.exe is not the new one")
	}
	if b, _ := os.ReadFile(cur + ".old"); !bytes.Equal(b, body) {
		t.Error("HELINK.exe.old is not the previous one")
	}
	if _, err := os.Stat(cur + ".new"); err == nil {
		t.Error("HELINK.exe.new left behind")
	}
	running.Wait()
	removeWhenFree(t, cur)
	removeWhenFree(t, cur+".old")
}

// The new HELINK waits for the one it replaces to close, at most 30 seconds.
func TestWinWaitForUpdatedFrom(t *testing.T) {
	exe, _ := os.Executable()
	marker := filepath.Join(t.TempDir(), "started.txt")
	t.Setenv("HELINK_WIN_HELPER", marker+"|1500")
	cmd := exec.Command(exe)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := cmd.Process
	helperLines(t, marker, 1)
	args := os.Args
	defer func() { os.Args = args }()
	os.Args = []string{exe, "--updated=" + strconv.Itoa(p.Pid)}
	start := time.Now()
	waitForUpdatedFrom()
	if d := time.Since(start); d < 300*time.Millisecond || d > 20*time.Second {
		t.Errorf("waited %v", d)
	}
	p.Wait()
	for _, a := range []string{"--updated=" + strconv.Itoa(p.Pid), "--updated", "--updated=x", "--background"} {
		os.Args = []string{exe, a}
		start = time.Now()
		waitForUpdatedFrom()
		if d := time.Since(start); d > 3*time.Second {
			t.Errorf("%s: waited %v", a, d)
		}
	}
}
