//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// Non-Windows builds exist only for tests and the development preview.

func prepareCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

type processTree struct{ cmd *exec.Cmd }

func attachProcessTree(cmd *exec.Cmd) *processTree { return &processTree{cmd: cmd} }

func (t *processTree) kill() {
	if t.cmd.Process != nil {
		syscall.Kill(-t.cmd.Process.Pid, syscall.SIGKILL)
	}
}

func (t *processTree) release() {}
