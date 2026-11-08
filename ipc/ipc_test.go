package ipc

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestCallAndRefuse(t *testing.T) {
	dir := t.TempDir()
	s, err := Listen(dir, "9.9", func(req Request) any {
		return map[string]any{"ok": true, "type": req.Type, "data": req.Data, "pid": req.PID}
	})
	if err != nil {
		t.Fatal(err)
	}
	info, err := ReadInfo(dir)
	if err != nil || info.PID != os.Getpid() || info.Version != "9.9" || len(info.Token) != 48 {
		t.Fatalf("info %+v %v", info, err)
	}
	if fi, _ := os.Stat(InfoPath(dir)); fi.Mode().Perm() != 0o600 {
		t.Errorf("ipc.json mode %v", fi.Mode())
	}
	resp, err := Call(info, Request{Type: "ext", Data: json.RawMessage(`{"type":"hello","x":"شكرا"}`), PID: 42}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(resp, &m)
	if m["ok"] != true || m["type"] != "ext" || m["pid"] != 42.0 || m["data"].(map[string]any)["x"] != "شكرا" {
		t.Fatalf("answer %s", resp)
	}
	bad := info
	bad.Token = "nope"
	resp, err = Call(bad, Request{Type: "show"}, 5*time.Second)
	if err != nil || !bytes.Contains(resp, []byte("refused")) {
		t.Fatalf("wrong secret: %s %v", resp, err)
	}
	// A big message passes.
	big := bytes.Repeat([]byte("a"), 3<<20)
	data, _ := json.Marshal(map[string]string{"big": string(big)})
	if _, err := Call(info, Request{Type: "ext", Data: data}, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := os.Stat(InfoPath(dir)); !os.IsNotExist(err) {
		t.Errorf("ipc.json left: %v", err)
	}
	if _, err := Call(info, Request{Type: "show"}, time.Second); err == nil {
		t.Error("closed server answered")
	}
}

func TestFraming(t *testing.T) {
	var b bytes.Buffer
	for _, m := range []string{`{"a":1}`, `{}`, `{"t":"مرحبا"}`} {
		if err := WriteMessage(&b, []byte(m)); err != nil {
			t.Fatal(err)
		}
	}
	if b.Bytes()[0] != 7 || b.Bytes()[1] != 0 {
		t.Fatalf("length is not little-endian: %v", b.Bytes()[:4])
	}
	for _, want := range []string{`{"a":1}`, `{}`, `{"t":"مرحبا"}`} {
		got, err := ReadMessage(&b)
		if err != nil || string(got) != want {
			t.Fatalf("got %q %v", got, err)
		}
	}
	if _, err := ReadMessage(&b); err == nil {
		t.Fatal("read past the end")
	}
	if err := WriteMessage(&b, make([]byte, MaxToBrowser+1)); err == nil {
		t.Fatal("wrote too long an answer")
	}
	b.Reset()
	b.Write([]byte{0xff, 0xff, 0xff, 0x7f})
	if _, err := ReadMessage(&b); err == nil {
		t.Fatal("accepted a huge length")
	}
}
