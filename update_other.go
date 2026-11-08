//go:build !windows

package main

import (
	"errors"
	"os"
	"sync"
)

// Outside Windows (tests and the development preview) HELINK updates the program named by
// HELINK_TEST_EXE the way the portable copy does (HELINK_TEST_UPDATE_KIND=setup: the way the
// installed one does), and records what it would start instead of starting it.
var (
	updateLaunchMu sync.Mutex
	updateLaunched []string
)

func updateTarget() (updateTargetInfo, error) {
	exe := os.Getenv("HELINK_TEST_EXE")
	if exe == "" {
		return updateTargetInfo{}, errors.New("no program to update here")
	}
	kind := "portable"
	if os.Getenv("HELINK_TEST_UPDATE_KIND") == "setup" {
		kind = "setup"
	}
	return updateTargetInfo{Kind: kind, Exe: exe}, nil
}

func applyUpdate(t updateTargetInfo, file string) error {
	record := func(s string) error {
		updateLaunchMu.Lock()
		updateLaunched = append(updateLaunched, s)
		updateLaunchMu.Unlock()
		return nil
	}
	if t.Kind == "setup" {
		return record(file + " /S /RELAUNCH")
	}
	return swapPortableExe(t.Exe, file, func(exe string) error { return record(exe + " --updated") })
}

func waitForUpdatedFrom() {}
