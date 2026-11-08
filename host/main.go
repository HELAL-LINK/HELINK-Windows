// helink-host is HELINK's helper for the browsers: the HELINK extension cannot talk to
// programs itself, so the browser starts this small program ("native messaging"),
// hands it the extension's message on its input and reads the answer from its output.
// The helper passes the message to the running HELINK (starting it in the background
// when it is not running) and gives back HELINK's answer.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"nazzelha/ipc"
)

const hostVersion = "2.2.1"

func main() {
	// A last word for the browser if anything goes very wrong.
	defer func() {
		if r := recover(); r != nil {
			b, _ := json.Marshal(map[string]any{"ok": false, "error": "host: " + fmt.Sprint(r)})
			_ = ipc.WriteMessage(os.Stdout, b)
		}
	}()
	dir := dataDir()
	for {
		msg, err := ipc.ReadMessage(os.Stdin)
		if err != nil {
			return // the browser closed the pipe: done
		}
		touchSeen(dir)
		_ = ipc.WriteMessage(os.Stdout, answer(dir, msg))
	}
}

// touchSeen notes that the extension talked to HELINK (HELINK says so in its window,
// even when it was closed at that time). At most once a minute.
func touchSeen(dir string) {
	p := filepath.Join(dir, "seen.json")
	if fi, err := os.Stat(p); err == nil && time.Since(fi.ModTime()) < time.Minute {
		return
	}
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(p, []byte(fmt.Sprintf(`{"at":%d}`, time.Now().UnixMilli())), 0o644)
}

// dataDir is HELINK's own folder (%LOCALAPPDATA%\HELINK).
func dataDir() string {
	if d := os.Getenv("HELINK_DATA"); d != "" {
		return d
	}
	if d := os.Getenv("LOCALAPPDATA"); d != "" {
		return filepath.Join(d, "HELINK")
	}
	d, _ := os.UserCacheDir()
	return filepath.Join(d, "HELINK")
}

func fail(code string) []byte {
	b, _ := json.Marshal(map[string]any{"ok": false, "error": code})
	return b
}

// answer handles one message of the extension.
func answer(dir string, msg []byte) []byte {
	var m struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(msg, &m) != nil {
		return fail("bad_message")
	}
	switch m.Type {
	case "ping":
		// Is the helper installed? (asked by the extension's settings page)
		b, _ := json.Marshal(map[string]any{"ok": true, "host": hostVersion})
		return b
	case "hello":
		// The extension asks how to behave. With HELINK closed, its settings are read
		// from the file it keeps for the extension: no need to start it for that.
		if resp, err := call(dir, msg, 10*time.Second); err == nil {
			return resp
		}
		b, _ := json.Marshal(map[string]any{"ok": true, "running": false, "settings": extSettings(dir)})
		return b
	}
	timeout := 2 * time.Minute
	resp, err := call(dir, msg, timeout)
	if err == nil {
		return resp
	}
	// Not running: start it (hidden near the clock) and ask again.
	if err := startApp(dir); err != nil {
		return fail("app_not_found")
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(250 * time.Millisecond)
		if resp, err := call(dir, msg, timeout); err == nil {
			return resp
		}
	}
	return fail("app_not_running")
}

// call passes a message to the running app.
func call(dir string, msg []byte, timeout time.Duration) ([]byte, error) {
	info, err := ipc.ReadInfo(dir)
	if err != nil {
		return nil, err
	}
	if !processAlive(info.PID) {
		return nil, errors.New("not running")
	}
	allowForeground(info.PID)
	resp, err := ipc.Call(info, ipc.Request{Type: "ext", Data: msg, PID: os.Getpid()}, timeout)
	if err != nil {
		return nil, err
	}
	if len(resp) > ipc.MaxToBrowser {
		return fail("answer_too_long"), nil
	}
	return resp, nil
}

// appPath is where HELINK is (app.json, written by HELINK at each start), else "".
func appPath(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, "app.json"))
	if err != nil {
		return ""
	}
	var a struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(b, &a) != nil {
		return ""
	}
	if fi, err := os.Stat(a.Path); err != nil || fi.IsDir() {
		return ""
	}
	return a.Path
}

// extSettings is what HELINK last wrote for the extension (ext.json).
func extSettings(dir string) json.RawMessage {
	f, err := os.Open(filepath.Join(dir, "ext.json"))
	if err != nil {
		return json.RawMessage("null")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 512<<10))
	if err != nil || !json.Valid(b) {
		return json.RawMessage("null")
	}
	return json.RawMessage(b)
}
