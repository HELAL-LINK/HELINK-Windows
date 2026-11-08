package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var errCancelled = errors.New("cancelled")

// RunResult collects what a yt-dlp run printed.
type RunResult struct {
	Stdout   []byte // only filled when captureStdout is set
	Log      string // last lines of stdout+stderr, for error messages
	ExitCode int
}

// lineRing keeps the last N lines of output.
type lineRing struct {
	mu    sync.Mutex
	lines []string
	max   int
}

func (r *lineRing) add(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(s) > 2000 {
		s = s[:2000] + "…"
	}
	r.lines = append(r.lines, s)
	if len(r.lines) > r.max {
		r.lines = r.lines[len(r.lines)-r.max:]
	}
}

func (r *lineRing) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.lines, "\n")
}

// runTool starts exe with args, streams every output line to onLine, and waits.
// Cancelling ctx kills the whole process tree.
func runTool(ctx context.Context, exe string, args []string, env []string, captureStdout bool, onLine func(line string, fromStderr bool)) (RunResult, error) {
	cmd := exec.Command(exe, args...)
	cmd.Env = env
	prepareCmd(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return RunResult{}, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return RunResult{}, err
	}
	if err := cmd.Start(); err != nil {
		return RunResult{}, err
	}
	tree := attachProcessTree(cmd)
	defer tree.release()

	ring := &lineRing{max: 60}
	var captured bytes.Buffer
	var wg sync.WaitGroup
	readLines := func(rd io.Reader, isErr bool) {
		defer wg.Done()
		br := bufio.NewReaderSize(rd, 64*1024)
		for {
			line, err := br.ReadString('\n')
			if len(line) > 0 {
				if captureStdout && !isErr {
					captured.WriteString(line)
				} else {
					l := strings.TrimRight(line, "\r\n")
					if l != "" {
						if !strings.HasPrefix(l, tagDownload) {
							ring.add(l)
						}
						if onLine != nil {
							onLine(l, isErr)
						}
					}
				}
			}
			if err != nil {
				return
			}
		}
	}
	wg.Add(2)
	go readLines(stdout, false)
	go readLines(stderr, true)

	waitDone := make(chan struct{})
	var killed atomic.Bool
	go func() {
		select {
		case <-ctx.Done():
			killed.Store(true)
			tree.kill()
		case <-waitDone:
		}
	}()

	wg.Wait()
	werr := cmd.Wait()
	close(waitDone)

	res := RunResult{Log: ring.String()}
	if captureStdout {
		res.Stdout = captured.Bytes()
	}
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	if killed.Load() || ctx.Err() != nil {
		return res, errCancelled
	}
	if werr != nil {
		return res, werr
	}
	return res, nil
}

// toolEnv builds the environment for yt-dlp and friends.
func toolEnv(extraPath ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		k := strings.ToUpper(strings.SplitN(kv, "=", 2)[0])
		switch k {
		// A Python installed on the PC must not leak into yt-dlp's own bundled Python.
		case "PYTHONHOME", "PYTHONPATH", "PYTHONSTARTUP", "PYTHONUTF8", "PYTHONIOENCODING":
			continue
		case "PATH":
			continue
		}
		env = append(env, kv)
	}
	path := strings.Join(append(extraPath, os.Getenv("PATH")), string(os.PathListSeparator))
	env = append(env, "PATH="+path, "PYTHONUTF8=1", "PYTHONIOENCODING=utf-8")
	return env
}

// quickRun runs a short command (like "--version") and returns its trimmed output.
func quickRun(exe string, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var out strings.Builder
	var mu sync.Mutex
	_, err := runTool(ctx, exe, args, toolEnv(), false, func(l string, _ bool) {
		mu.Lock()
		if out.Len() < 4000 {
			out.WriteString(l + "\n")
		}
		mu.Unlock()
	})
	if errors.Is(err, errCancelled) && ctx.Err() == context.DeadlineExceeded {
		return out.String(), context.DeadlineExceeded
	}
	return strings.TrimSpace(out.String()), err
}
