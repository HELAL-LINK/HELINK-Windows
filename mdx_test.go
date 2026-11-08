package main

// Checks of the voice network against the reference (numpy + ONNX Runtime, made by the
// evaluation scripts):
//   HELINK_SEP_ARCHIVES=dir with Kim_Vocal_2.onnx (UVR's model)
//   HELINK_MDX_REF=dir with chunk_in.f32/chunk_out.f32 (one chunk through ONNX Runtime),
//     mix.f32 and voice_xf.f32 (14 s of a mix and its voices)

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

var (
	sepTestOnce    sync.Once
	sepTestModel   *sepModel
	sepTestWeights string // the converted weight file
	sepTestErr     error
)

func sepArchive(t testing.TB) string {
	dir := os.Getenv("HELINK_SEP_ARCHIVES")
	if dir == "" {
		t.Skip("set HELINK_SEP_ARCHIVES")
	}
	return filepath.Join(dir, sepSources[0].file)
}

// testSepModel converts UVR's model once (into a temporary folder), as the setup does.
func testSepModel(t testing.TB) *sepModel {
	t.Helper()
	src := sepArchive(t)
	sepTestOnce.Do(func() {
		tmp, err := os.MkdirTemp("", "helink-sep")
		if err != nil {
			sepTestErr = err
			return
		}
		w := filepath.Join(tmp, sepWeightsFile)
		sum, err := convertMdxOnnx(src, w)
		if err != nil {
			sepTestErr = err
			return
		}
		if sum != sepSources[0].weightsSHA256 {
			sepTestErr = errors.New("weight file " + sum + ", want " + sepSources[0].weightsSHA256)
			return
		}
		sepTestWeights = w
		sepTestModel, sepTestErr = openSepFile(w)
	})
	if sepTestErr != nil {
		t.Fatal(sepTestErr)
	}
	return sepTestModel
}

// installTestModel puts the music remover in an app's tools folder, as its setup would.
func installTestModel(t *testing.T, toolsDir string) {
	t.Helper()
	testSepModel(t)
	dir := filepath.Join(toolsDir, "sep")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(sepTestWeights, filepath.Join(dir, sepWeightsFile)); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, sepWeightsFile+".ok"), []byte(sepSources[0].weightsSHA256), 0o644)
}

func mdxRefDir(t testing.TB) string {
	d := os.Getenv("HELINK_MDX_REF")
	if d == "" {
		t.Skip("set HELINK_MDX_REF")
	}
	return d
}

func readF32(t testing.TB, p string) []float32 {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]float32, len(b)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return out
}

// compareF32: largest absolute difference, the same relative to the reference's peak, SNR.
func compareF32(got, want []float32) (maxAbs, rel, snr float64) {
	var maxRef, err, sig float64
	for i := range want {
		d := float64(got[i]) - float64(want[i])
		maxAbs = math.Max(maxAbs, math.Abs(d))
		maxRef = math.Max(maxRef, math.Abs(float64(want[i])))
		err += d * d
		sig += float64(want[i]) * float64(want[i])
	}
	return maxAbs, maxAbs / math.Max(1e-30, maxRef), 10 * math.Log10(sig/math.Max(1e-30, err))
}

func TestSepHalfRounding(t *testing.T) {
	cases := map[float64]uint16{0: 0, 1: 0x3c00, -2: 0xc000, 65504: 0x7bff, 6.103515625e-05: 0x0400,
		5.960464477539063e-08: 0x0001, 2.9802322387695312e-08: 0x0000, 2.98023224e-08 * 1.5: 0x0001,
		1.00048828125: 0x3c00, 1.00146484375: 0x3c02, 0.333251953125: 0x3555}
	for x, want := range cases {
		got, err := floatToHalf(x)
		if err != nil || got != want {
			t.Errorf("%v: %#04x %v, want %#04x", x, got, err, want)
		}
		if back := halfToFloat(want); x != 0 && float64(back) != x && want != 0 && x != 2.98023224e-08*1.5 &&
			x != 1.00048828125 && x != 1.00146484375 {
			t.Errorf("%#04x back to %g, want %g", want, back, x)
		}
	}
	if _, err := floatToHalf(70000); err == nil {
		t.Error("70000 should not fit")
	}
}

// netOnChunk runs the network on the reference chunk and gives its output as ONNX lays it
// out ([4][bins][frames]).
func netOnChunk(t *testing.T, net *mdxNet) (got, want []float32, took time.Duration) {
	dir := mdxRefDir(t)
	in := readF32(t, filepath.Join(dir, "chunk_in.f32")) // [4][F][T]
	want = readF32(t, filepath.Join(dir, "chunk_out.f32"))
	T, F := net.dimT, net.dimF
	for c := 0; c < 4; c++ {
		for f := 0; f < F; f++ {
			for tt := 0; tt < T; tt++ {
				net.in.d[net.in.at(c, tt, f)] = in[(c*F+f)*T+tt]
			}
		}
	}
	start := time.Now()
	if err := net.forward(context.Background()); err != nil {
		t.Fatal(err)
	}
	took = time.Since(start)
	got = make([]float32, len(want))
	for c := 0; c < 4; c++ {
		for f := 0; f < F; f++ {
			for tt := 0; tt < T; tt++ {
				got[(c*F+f)*T+tt] = net.out[(c*T+tt)*F+f]
			}
		}
	}
	return got, want, took
}

// fp32Net is the network from UVR's model itself (fp32 weights), to compare with ONNX Runtime.
func fp32Net(t *testing.T, spec mdxSpec) *mdxNet {
	data, err := os.ReadFile(sepArchive(t))
	if err != nil {
		t.Skip(err)
	}
	vals, dims, err := mdxFromOnnx(data)
	if err != nil {
		t.Fatal(err)
	}
	net, err := buildMdxNet(func(name string) ([]float32, []int, bool) {
		v, ok := vals[name]
		return v, dims[name], ok
	}, spec, sepThreads())
	if err != nil {
		t.Fatal(err)
	}
	return net
}

func TestMdxNetMatchesOnnx(t *testing.T) {
	mdxRefDir(t)
	for _, direct := range []bool{false, true} {
		spec := sepSpec
		spec.direct = direct
		net := fp32Net(t, spec)
		got, want, took := netOnChunk(t, net)
		_, _, snr := compareF32(got, want)
		t.Logf("%s, direct %v: one chunk in %s (%s, %d threads); vs ONNX Runtime %.1f dB", net, direct,
			took.Round(time.Millisecond), mdxKernel, sepThreads(), snr)
		if snr < 90 {
			t.Errorf("network output vs ONNX Runtime: %.1f dB", snr)
		}
	}
}

// The plain Go loops (processors without AVX2), on one chunk: some minutes.
func TestMdxNetPlainGo(t *testing.T) {
	if os.Getenv("HELINK_SLOW") == "" {
		t.Skip("set HELINK_SLOW")
	}
	mdxRefDir(t)
	oc, or, oi := convTile, rowsTile, winoIn
	defer func() { convTile, rowsTile, winoIn = oc, or, oi }()
	convTile, rowsTile, winoIn = convTileGo, rowsTileGo, nil
	net := fp32Net(t, sepSpec)
	got, want, took := netOnChunk(t, net)
	_, _, snr := compareF32(got, want)
	t.Logf("plain Go: one chunk in %s; vs ONNX Runtime %.1f dB", took.Round(time.Millisecond), snr)
	if snr < 90 {
		t.Errorf("network output vs ONNX Runtime: %.1f dB", snr)
	}
}

// The whole way: UVR's model converted to HELINK's weight file (fp16), the stream of
// samples in and out, against the reference pipeline (mdx_xf.py).
func TestMdxRemoverMatchesReference(t *testing.T) {
	dir := mdxRefDir(t)
	net, err := loadMdxNet(testSepModel(t), sepSpec, sepThreads())
	if err != nil {
		t.Fatal(err)
	}
	mix, err := os.ReadFile(filepath.Join(dir, "mix.f32"))
	if err != nil {
		t.Fatal(err)
	}
	want := readF32(t, filepath.Join(dir, "voice_xf.f32"))
	var out bytes.Buffer
	r := newMdxRemover(net, sepSpec)
	var calls int
	var last int64
	start := time.Now()
	n, err := r.run(context.Background(), bytes.NewReader(mix), &out, func(done int64) {
		if done < last {
			t.Errorf("progress went back: %d after %d", done, last)
		}
		last = done
		calls++
	})
	if err != nil {
		t.Fatal(err)
	}
	el := time.Since(start)
	if int(n) != len(mix)/8 || out.Len() != len(mix) || last != n {
		t.Fatalf("%d samples in, %d out (%d bytes), progress ended at %d", len(mix)/8, n, out.Len(), last)
	}
	got := make([]float32, len(want))
	b := out.Bytes()
	for i := range got {
		got[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	_, _, snr := compareF32(got, want)
	t.Logf("%.1f s of sound in %s (%d progress reports); voices vs reference: %.1f dB", float64(n)/mrRate, el, calls, snr)
	if snr < 50 {
		t.Errorf("voices vs reference: %.1f dB", snr)
	}
	if calls < 50 {
		t.Errorf("only %d progress reports", calls)
	}
}

// Any length comes out as long as it went in (one sample, the ends of chunks), with
// the sound of a sine; a half sample is refused; stopping stops.
func TestMdxRemoverLengths(t *testing.T) {
	net, err := loadMdxNet(testSepModel(t), sepSpec, sepThreads())
	if err != nil {
		t.Fatal(err)
	}
	r := newMdxRemover(net, sepSpec)
	for _, L := range []int{0, 1, 4096, r.gen - r.ov, r.gen, r.gen + 1} {
		in := make([]byte, 8*L)
		for i := 0; i < L; i++ {
			v := math.Float32bits(float32(0.3 * math.Sin(float64(i)*0.05)))
			binary.LittleEndian.PutUint32(in[8*i:], v)
			binary.LittleEndian.PutUint32(in[8*i+4:], v)
		}
		var out bytes.Buffer
		n, err := r.run(context.Background(), bytes.NewReader(in), &out, nil)
		if err != nil || int(n) != L || out.Len() != 8*L {
			t.Fatalf("length %d: %d out (%d bytes), %v", L, n, out.Len(), err)
		}
		for i := 0; i < out.Len(); i += 4 {
			if v := math.Float32frombits(binary.LittleEndian.Uint32(out.Bytes()[i:])); math.IsNaN(float64(v)) || math.Abs(float64(v)) > 2 {
				t.Fatalf("length %d: value %v", L, v)
			}
		}
	}
	if _, err := r.run(context.Background(), bytes.NewReader(make([]byte, 8*10+3)), io.Discard, nil); !errors.Is(err, errCutSample) {
		t.Errorf("half a sample: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	if _, err := r.run(ctx, bytes.NewReader(make([]byte, 8*3*r.gen)), io.Discard, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("stopped: %v", err)
	}
	if el := time.Since(start); el > 3*time.Second {
		t.Errorf("stopping took %s", el)
	}
}
