//go:build !windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// (Linux: for the tests.)

func allowForeground(int) {}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

// startApp runs $HELINK_APP_CMD (the tests' stand-in for HELINK.exe).
func startApp(dir string) error {
	c := os.Getenv("HELINK_APP_CMD")
	if c == "" {
		return errors.New("no app")
	}
	cmd := exec.Command("/bin/sh", "-c", c)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
