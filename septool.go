package main

// The music remover's model, set up the first time music is to be removed: the voice
// network Kim_Vocal_2 (MDX-Net, 67 MB, as Ultimate Vocal Remover publishes it on GitHub)
// is downloaded, checked, turned into HELINK's weight file (mdxconv.go: 33 MB) and checked
// again. Kept in %LOCALAPPDATA%\HELINK\tools\sep; read for each file whose music is
// removed. HELINK 2.1's model (Spleeter) is removed: it left far more music behind.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	sepWeightsFile = "kim-vocal-2.hlw"
	sepWeightsSize = 33354944
)

// sepSpec is how the network is run: UVR's settings for Kim_Vocal_2 (n_fft 7680,
// dim_t 2^8).
var sepSpec = mdxSpec{nfft: 7680, dimT: 256}

// sepOldFiles are HELINK 2.1's music remover (Spleeter), deleted when found.
var sepOldFiles = []string{"spleeter-2stems.hlw", "spleeter-2stems.hlw.ok", "spleeter-2stems.hlw.new",
	"sherpa-onnx-spleeter-2stems-fp16.tar.bz2", "sherpa-onnx-spleeter-2stems-fp16.tar.bz2.part",
	"2stems.tar.gz", "2stems.tar.gz.part"}

type sepSource struct {
	file          string
	size          int64
	sha256        string
	weightsSHA256 string
	urls          []string
}

// sepSources: where the model comes from, in the order tried (tests replace them). The
// second address is python-audio-separator's copy of the same file.
var sepSources = []sepSource{
	{"Kim_Vocal_2.onnx", 66759214,
		"ce74ef3b6a6024ce44211a07be9cf8bc6d87728cc852a68ab34eb8e58cde9c8b",
		"d0b06e512a683a749185fc178c70a55d6c0ceb567761345e2f8042c82fd91dc2",
		[]string{"https://github.com/TRvlvr/model_repo/releases/download/all_public_uvr_models/Kim_Vocal_2.onnx",
			"https://github.com/nomadkaraoke/python-audio-separator/releases/download/model-configs/Kim_Vocal_2.onnx"}},
}

type sepTool struct {
	dir   string
	tools *Tools

	installMu sync.Mutex // one setup at a time
	loadMu    sync.Mutex
	model     *sepModel
}

func newSepTool(toolsRoot string, tools *Tools) *sepTool {
	return &sepTool{dir: filepath.Join(toolsRoot, "sep"), tools: tools}
}

func (s *sepTool) weights() string { return filepath.Join(s.dir, sepWeightsFile) }
func (s *sepTool) marker() string  { return s.weights() + ".ok" }

// ready says whether the model is in place (checked when it was set up; here its size
// and mark only).
func (s *sepTool) ready() bool {
	fi, err := os.Stat(s.weights())
	if err != nil || fi.Size() != sepWeightsSize {
		return false
	}
	b, err := os.ReadFile(s.marker())
	if err != nil {
		return false
	}
	mark := strings.TrimSpace(string(b))
	for _, src := range sepSources {
		if src.weightsSHA256 == mark {
			return true
		}
	}
	return false
}

// sepDownloadSize is what the first setup downloads (for the window).
func sepDownloadSize() int64 { return sepSources[0].size }

// errSepSetup: setting the model up failed for a reason other than the connection or
// the space (a damaged file, a conversion that went wrong): the next try starts over.
var errSepSetup = errors.New("setting up the music remover failed")

// install downloads and converts the model if it is not there yet. Others asking at the
// same time wait for it. onBytes reports the download.
func (s *sepTool) install(ctx context.Context, onBytes func(done, total int64)) error {
	locked := make(chan struct{})
	go func() {
		s.installMu.Lock()
		close(locked)
	}()
	select {
	case <-locked:
	case <-ctx.Done():
		// Release the lock as soon as it is ours (nobody waits for it here any more).
		go func() {
			<-locked
			s.installMu.Unlock()
		}()
		return ctx.Err()
	}
	defer s.installMu.Unlock()
	if s.ready() {
		return nil
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	_ = os.Remove(s.marker())
	need := sepSources[0].size + sepWeightsSize + 8<<20
	if free, ok := freeSpace(s.dir); ok && free < need-s.partSize() {
		return &DownloadError{Code: "disk", Need: need, Free: free, Err: errors.New("no space for the music remover")}
	}
	var last error
	for _, src := range sepSources {
		archive := filepath.Join(s.dir, src.file)
		if fi, err := os.Stat(archive); err != nil || fi.Size() != src.size {
			err := s.tools.download(ctx, src.urls, archive, func(done, total int64) {
				if total <= 0 {
					total = src.size
				}
				onBytes(done, total)
			})
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				last = err
				var he *httpStatusError
				if errors.As(err, &he) {
					log.Printf("music remover source %s: %v", src.file, err)
					continue // gone or moved: the next source
				}
				return err // the connection: trying again continues the same file
			}
		}
		if sum, err := fileSHA256(archive); err != nil || sum != src.sha256 {
			log.Printf("music remover source %s: checksum %s %v", src.file, sum, err)
			_ = os.Remove(archive)
			last = fmt.Errorf("%w: %s changed", errSepSetup, src.file)
			continue
		}
		if err := s.convert(archive, src); err != nil {
			return err
		}
		s.cleanup()
		return nil
	}
	if last == nil {
		last = errSepSetup
	}
	return last
}

func (s *sepTool) partSize() int64 {
	var n int64
	for _, src := range sepSources {
		for _, p := range []string{filepath.Join(s.dir, src.file), filepath.Join(s.dir, src.file+".part")} {
			if fi, err := os.Stat(p); err == nil {
				n += fi.Size()
			}
		}
	}
	return n
}

func (s *sepTool) convert(archive string, src sepSource) error {
	tmp := filepath.Join(s.dir, sepWeightsFile+".new")
	_ = os.Remove(tmp)
	sum, err := convertMdxOnnx(archive, tmp)
	if err != nil || sum != src.weightsSHA256 {
		_ = os.Remove(tmp)
		if err != nil && isNoSpace(err) {
			return &DownloadError{Code: "disk", Err: err}
		}
		// The archive checked out, so a bad result means something else: start over next time.
		_ = os.Remove(archive)
		return fmt.Errorf("%w: converting gave %s: %v", errSepSetup, sum, err)
	}
	// What is on disk must be exactly what was checked.
	if real, err := fileSHA256(tmp); err != nil || real != src.weightsSHA256 {
		_ = os.Remove(tmp)
		return fmt.Errorf("%w: the weight file is damaged on disk", errSepSetup)
	}
	_ = os.Remove(s.weights())
	if err := renameRetry(tmp, s.weights()); err != nil {
		return err
	}
	return os.WriteFile(s.marker(), []byte(src.weightsSHA256), 0o644)
}

// cleanup removes the downloads (the weight file is all that is needed now).
func (s *sepTool) cleanup() {
	s.removeOld()
	for _, src := range sepSources {
		_ = os.Remove(filepath.Join(s.dir, src.file))
		_ = os.Remove(filepath.Join(s.dir, src.file+".part"))
	}
	_ = os.Remove(filepath.Join(s.dir, sepWeightsFile+".new"))
	_ = os.Remove(filepath.Join(s.dir, sepWeightsFile+".new.tmp"))
}

// removeOld deletes the files of the music remover HELINK used before (39 MB).
func (s *sepTool) removeOld() {
	for _, n := range sepOldFiles {
		if err := os.Remove(filepath.Join(s.dir, n)); err == nil {
			log.Printf("removed the old music remover's %s", n)
		}
	}
}

// load opens the weight file (33 MB).
func (s *sepTool) load() (*sepModel, error) {
	s.loadMu.Lock()
	defer s.loadMu.Unlock()
	if s.model != nil {
		return s.model, nil
	}
	m, err := openSepFile(s.weights())
	if err != nil {
		// Damaged: set it up again next time.
		_ = os.Remove(s.marker())
		return nil, fmt.Errorf("%w: %v", errSepSetup, err)
	}
	s.model = m
	return m, nil
}

// drop forgets the model (it is read again for the next file: a moment, against 33 MB kept
// while HELINK waits in the tray).
func (s *sepTool) drop() {
	s.loadMu.Lock()
	s.model = nil
	s.loadMu.Unlock()
}

// forget makes the next file set the model up again (its weight file did not make the
// network).
func (s *sepTool) forget() {
	s.drop()
	_ = os.Remove(s.marker())
}

func fileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func isNoSpace(err error) bool {
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "no space") || strings.Contains(m, "not enough space") || strings.Contains(m, "disk is full")
}
