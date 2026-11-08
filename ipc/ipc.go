// Package ipc is how the browser's helper (helink-host.exe, started by the browser for
// the HELINK extension) talks to the running HELINK: one JSON line over a connection to
// 127.0.0.1, on a port the app picks and writes, with a secret, in a file only the user
// can read. It also has the browsers' "native messaging" framing.
package ipc

import (
	"bufio"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"
)

// Info is where the running app listens (ipc.json in the app's folder).
type Info struct {
	Port    int    `json:"port"`
	Token   string `json:"token"`
	PID     int    `json:"pid"`
	Version string `json:"version"`
}

// InfoPath is the file that tells where the app listens.
func InfoPath(dataDir string) string { return filepath.Join(dataDir, "ipc.json") }

// ReadInfo reads where the app listens (an error when it never ran).
func ReadInfo(dataDir string) (Info, error) {
	var in Info
	b, err := os.ReadFile(InfoPath(dataDir))
	if err != nil {
		return in, err
	}
	if err := json.Unmarshal(b, &in); err != nil || in.Port <= 0 || in.Token == "" {
		return in, errors.New("ipc.json is not valid")
	}
	return in, nil
}

// NewToken is a random secret.
func NewToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Request is one question to the app.
type Request struct {
	Token string          `json:"token"`
	Type  string          `json:"type"`           // "show", "ext" (a message of the extension), "ping"
	Data  json.RawMessage `json:"data,omitempty"` // for "ext": the extension's message as it is
	PID   int             `json:"pid,omitempty"`  // the asking process (it may give the app the right to come to the front)
}

// Handler answers a request; the answer is sent as JSON.
type Handler func(req Request) any

// Server listens for requests on 127.0.0.1.
type Server struct {
	ln    net.Listener
	token string
	h     Handler
	file  string
}

// Listen starts the server and writes ipc.json in dataDir.
func Listen(dataDir, version string, h Handler) (*Server, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &Server{ln: ln, token: NewToken(), h: h, file: InfoPath(dataDir)}
	info := Info{Port: ln.Addr().(*net.TCPAddr).Port, Token: s.token, PID: os.Getpid(), Version: version}
	b, _ := json.Marshal(info)
	_ = os.MkdirAll(dataDir, 0o700)
	tmp := s.file + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	if err := os.Rename(tmp, s.file); err != nil {
		ln.Close()
		return nil, err
	}
	go s.serve()
	return s, nil
}

// Close stops listening and removes ipc.json (if it is still this server's).
func (s *Server) Close() {
	s.ln.Close()
	if in, err := ReadInfo(filepath.Dir(s.file)); err == nil && in.Token == s.token {
		_ = os.Remove(s.file)
	}
}

func (s *Server) serve() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return
		}
		go s.handle(c)
	}
}

func (s *Server) handle(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(2 * time.Minute))
	r := bufio.NewReaderSize(c, 64<<10)
	line, err := readLine(r, 32<<20)
	if err != nil {
		return
	}
	var req Request
	if json.Unmarshal(line, &req) != nil || req.Token != s.token {
		_, _ = c.Write([]byte(`{"ok":false,"error":"refused"}` + "\n"))
		return
	}
	var resp any
	func() {
		defer func() {
			if r := recover(); r != nil {
				resp = map[string]any{"ok": false, "error": fmt.Sprint(r)}
			}
		}()
		resp = s.h(req)
	}()
	b, err := json.Marshal(resp)
	if err != nil {
		b = []byte(`{"ok":false,"error":"bad answer"}`)
	}
	_, _ = c.Write(append(b, '\n'))
}

func readLine(r *bufio.Reader, max int) ([]byte, error) {
	var out []byte
	for {
		chunk, isPrefix, err := r.ReadLine()
		if err != nil {
			return nil, err
		}
		out = append(out, chunk...)
		if len(out) > max {
			return nil, errors.New("too long")
		}
		if !isPrefix {
			return out, nil
		}
	}
}

// Call sends one request to the app and returns its answer.
func Call(info Info, req Request, timeout time.Duration) (json.RawMessage, error) {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", info.Port), 3*time.Second)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(timeout))
	req.Token = info.Token
	b, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if _, err := c.Write(append(b, '\n')); err != nil {
		return nil, err
	}
	line, err := readLine(bufio.NewReaderSize(c, 64<<10), 32<<20)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(line), nil
}

// ---------------------------------------------------------------------------
// Native messaging: each message is its length (4 bytes, little-endian) and UTF-8 JSON.
// ---------------------------------------------------------------------------

// MaxToHost is the most a message from the browser may hold here; MaxToBrowser is the
// browsers' limit for an answer (1 MB).
const (
	MaxToHost    = 64 << 20
	MaxToBrowser = 1 << 20
)

// ReadMessage reads one message from the browser (io.EOF when it closed the pipe).
func ReadMessage(r io.Reader) ([]byte, error) {
	var n uint32
	if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
		return nil, err
	}
	if n > MaxToHost {
		return nil, fmt.Errorf("message of %d bytes is too long", n)
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, err
	}
	return b, nil
}

// WriteMessage writes one message for the browser.
func WriteMessage(w io.Writer, b []byte) error {
	if len(b) > MaxToBrowser {
		return fmt.Errorf("answer of %d bytes is too long", len(b))
	}
	var hdr [4]byte
	binary.LittleEndian.PutUint32(hdr[:], uint32(len(b)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(b)
	return err
}
