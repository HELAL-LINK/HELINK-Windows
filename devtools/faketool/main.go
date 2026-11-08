// faketool imitates yt-dlp, deno and ffmpeg for Windows tests (run under Wine).
// Its behaviour depends on the name it is started as.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func main() {
	name := strings.ToLower(strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe"))
	args := os.Args[1:]
	has := func(a string) bool {
		for _, x := range args {
			if x == a {
				return true
			}
		}
		return false
	}
	switch {
	case strings.HasPrefix(name, "deno"):
		fmt.Println("deno 2.9.7 (stable, release, x86_64-pc-windows-msvc)")
		return
	case strings.HasPrefix(name, "ffmpeg"), strings.HasPrefix(name, "ffprobe"):
		fmt.Println("ffmpeg version N-fake Copyright (c) 2000-2026 the FFmpeg developers")
		return
	}
	// yt-dlp
	if has("--version") {
		fmt.Println("2026.10.10.233141")
		return
	}
	if len(args) > 0 && args[0] == "child-sleep" {
		time.Sleep(120 * time.Second)
		return
	}
	if has("-J") {
		if strings.Contains(os.Getenv("FAKE_FAIL"), "fetch") {
			fmt.Fprintln(os.Stderr, "WARNING: [youtube] something")
			fmt.Fprintln(os.Stderr, "ERROR: [youtube] abcdefghijk: Video unavailable. This video has been removed by the uploader")
			os.Exit(1)
		}
		b, err := os.ReadFile(os.Getenv("FAKE_INFO_JSON"))
		if err != nil {
			fmt.Fprintln(os.Stderr, "ERROR: no fake json:", err)
			os.Exit(1)
		}
		os.Stdout.Write(b)
		os.Stdout.Write([]byte("\r\n"))
		return
	}
	// download
	var home, temp, tmpl, format string
	audio := has("-x")
	for i := 0; i < len(args)-1; i++ {
		switch args[i] {
		case "-P":
			v := args[i+1]
			if strings.HasPrefix(v, "home:") {
				home = strings.TrimPrefix(v, "home:")
			} else if strings.HasPrefix(v, "temp:") {
				temp = strings.TrimPrefix(v, "temp:")
			}
		case "-o":
			tmpl = args[i+1]
		case "-f":
			format = args[i+1]
		}
	}
	if os.Getenv("FAKE_SPAWN") == "1" {
		c := exec.Command(os.Args[0], "child-sleep")
		if err := c.Start(); err == nil {
			fmt.Printf("CHILDPID|%d\r\n", c.Process.Pid)
		}
	}
	slow, _ := strconv.Atoi(os.Getenv("FAKE_SLOW_MS"))
	if slow == 0 {
		slow = 20
	}
	ids := strings.Split(strings.SplitN(format, "/", 2)[0], "+")
	sizes := []int{3000000, 130000}
	for pi, id := range ids {
		total := sizes[min(pi, 1)]
		for d := 0; d <= total; d += total / 5 {
			fmt.Printf("NZLP|downloading|%d|%d|NA|1500000.5|1|%s\r\n", d, total, id)
			time.Sleep(time.Duration(slow) * time.Millisecond)
		}
		fmt.Printf("NZLP|finished|%d|%d|NA|1500000.5|NA|%s\r\n", total, total, id)
		if temp != "" {
			os.WriteFile(filepath.Join(temp, "part-"+id), []byte("x"), 0o644)
		}
	}
	if strings.Contains(os.Getenv("FAKE_FAIL"), "download") {
		fmt.Fprintln(os.Stderr, "ERROR: unable to download video data: HTTP Error 403: Forbidden")
		os.Exit(1)
	}
	ext := "mp4"
	pp := []string{"Merger", "VideoRemuxer", "MoveFiles"}
	if audio {
		ext = "mp3"
		pp = []string{"ExtractAudio", "Metadata", "MoveFiles"}
	}
	for _, p := range pp {
		fmt.Fprintf(os.Stderr, "NZLPP|started|%s\r\n", p)
		time.Sleep(time.Duration(slow) * time.Millisecond)
		fmt.Fprintf(os.Stderr, "NZLPP|finished|%s\r\n", p)
	}
	name2 := strings.ReplaceAll(strings.ReplaceAll(tmpl, "%(ext)s", ext), "%%", "%")
	final := filepath.Join(home, name2)
	if err := os.WriteFile(final, []byte("fake media"), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "ERROR: unable to open for writing:", err)
		os.Exit(1)
	}
	fmt.Printf("NZLDONE|%s\r\n", final)
}
