package main

// Removes the music with the voice network (mdxnet.go), as Ultimate Vocal Remover runs
// it: chunks of 261120 samples (about 5.9 seconds), each one's spectrum (7680-point Hann
// windows every 1024 samples, as torch.stft with center=True and reflected edges; the
// lowest 3072 bins, the first 3 of them cleared) through the network, back to sound
// (torch.istft), keeping each chunk's middle 253440 samples (the sound has 3840 zeros in
// front). Two chunks see different surroundings, so their voices differ a little where
// they meet: an audible click every 6 seconds. The kept parts overlap by 8192 samples
// instead, faded from one into the other.
//
// It works as a stream on 44.1 kHz stereo float samples (what ffmpeg decodes to), so an
// hour-long lecture needs no more memory than a song (about 330 MB).

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"strings"
)

// mdxSpec is how a network file is run.
type mdxSpec struct {
	nfft, dimT int
	direct     bool // no Winograd convolutions (for the tests)
}

// mdxWinoMaxC: levels with more channels than this keep the plain 3x3 convolutions (the
// Winograd filters take 4 times the memory, and those levels are a small part of the work).
const mdxWinoMaxC = 144

type mdxRemover struct {
	net                   *mdxNet
	nfft, hop, bins, dimT int
	chunk, trim, gen      int
	ov, step              int       // the kept parts' overlap, the distance between chunks
	fade                  []float64 // the new chunk's share over the overlap
	tail                  [2][]float64
	fft                   *cfft
	win                   []float64
	invEnv                []float64 // 1 / (sum of the squared windows), over the chunk
	in                    [2][]float32
	frames                []float32 // the voices' frames after the inverse transform: [dimT][2][nfft]
	acc                   [2][]float64
	z, tmp                [][]complex128 // per worker
}

// loadMdxNet builds the network from HELINK's weight file (mdxconv.go).
func loadMdxNet(m *sepModel, spec mdxSpec, workers int) (*mdxNet, error) {
	return buildMdxNet(func(name string) ([]float32, []int, bool) {
		t := m.tensor(name)
		if t == nil {
			return nil, nil, false
		}
		v := make([]float32, t.count)
		m.read(t, v)
		return v, t.dims, true
	}, spec, workers)
}

// buildMdxNet builds the network from its tensors (by name, as mdxconv.go names them).
func buildMdxNet(tensor func(name string) ([]float32, []int, bool), spec mdxSpec, workers int) (*mdxNet, error) {
	get := func(name string, dims ...int) ([]float32, error) {
		v, d, ok := tensor(name)
		if !ok {
			return nil, fmt.Errorf("weight file has no %s", name)
		}
		if len(dims) > 0 {
			ok := len(d) == len(dims)
			for i := 0; ok && i < len(dims); i++ {
				ok = dims[i] < 0 || d[i] == dims[i]
			}
			if !ok {
				return nil, fmt.Errorf("unexpected shape of %s: %v", name, d)
			}
		}
		return v, nil
	}
	_, fd, ok1 := tensor("first.w")
	_, wd, ok2 := tensor("enc0.tdf.w1")
	if !ok1 || !ok2 || len(fd) != 4 || len(wd) != 2 {
		return nil, fmt.Errorf("not a voice network file")
	}
	n := &mdxNet{g: fd[0], dimF: wd[0], dimT: spec.dimT}
	for {
		if _, _, ok := tensor(fmt.Sprintf("up%d.w", n.levels)); !ok {
			break
		}
		n.levels++
	}
	if wd[1] > 0 {
		n.bn = n.dimF / wd[1]
	}
	if err := n.checkShapes(); err != nil {
		return nil, err
	}
	g, L := n.g, n.levels
	conv := func(name string, cout, cin, k int) (*mdxConv, error) {
		w, err := get(name+".w", cout, cin, k, k)
		if err != nil {
			return nil, err
		}
		b, err := get(name+".b", cout)
		if err != nil {
			return nil, err
		}
		taps := k * k
		return newMdxConv(cout, cin, taps, func(co, p int) float32 { return w[co*cin*taps+p] },
			func(co int) float32 { return b[co] }), nil
	}
	block := func(name string, c, f int) (mdxBlock, error) {
		var bl mdxBlock
		var err error
		for i := range bl.conv {
			cn := fmt.Sprintf("%s.c%d", name, i)
			if nb := winoBatch(f); winoIn != nil && !spec.direct && nb > 0 && c <= mdxWinoMaxC && c%6 == 0 {
				w, err := get(cn+".w", c, c, 3, 3)
				if err != nil {
					return bl, err
				}
				b, err := get(cn+".b", c)
				if err != nil {
					return bl, err
				}
				bl.wino[i] = newMdxWino(c, c, w, b, nb)
				continue
			}
			if bl.conv[i], err = conv(cn, c, c, 3); err != nil {
				return bl, err
			}
		}
		d := &mdxTDF{f: f, fb: f / n.bn}
		d.fbp = (d.fb + 15) / 16 * 16
		a, err := get(name+".tdf.w1", f, d.fb)
		if err != nil {
			return bl, err
		}
		b, err := get(name+".tdf.w2", d.fb, f)
		if err != nil {
			return bl, err
		}
		d.w1 = make([]float32, d.fbp*f)
		for jb := 0; jb < d.fbp/16; jb++ {
			for p := 0; p < f; p++ {
				for j := 0; j < 16; j++ {
					if col := 16*jb + j; col < d.fb {
						d.w1[(jb*f+p)*16+j] = a[p*d.fb+col]
					}
				}
			}
		}
		d.w2 = make([]float32, f*d.fb)
		for jb := 0; jb < f/16; jb++ {
			for p := 0; p < d.fb; p++ {
				copy(d.w2[(jb*d.fb+p)*16:][:16], b[p*f+16*jb:])
			}
		}
		for _, x := range []struct {
			dst  *[]float32
			name string
		}{{&d.s1, "s1"}, {&d.b1, "b1"}, {&d.s2, "s2"}, {&d.b2, "b2"}} {
			if *x.dst, err = get(name+".tdf."+x.name, c); err != nil {
				return bl, err
			}
		}
		bl.tdf = d
		return bl, nil
	}
	var err error
	if n.first, err = conv("first", g, 4, 1); err != nil {
		return nil, err
	}
	if n.last, err = conv("last", 4, g, 1); err != nil {
		return nil, err
	}
	n.enc, n.dec = make([]mdxBlock, L), make([]mdxBlock, L)
	n.down, n.up = make([]*mdxConv, L), make([]mdxUp, L)
	for l := 0; l < L; l++ {
		c, f := g*(l+1), n.dimF>>l
		if n.enc[l], err = block(fmt.Sprintf("enc%d", l), c, f); err != nil {
			return nil, err
		}
		if n.dec[l], err = block(fmt.Sprintf("dec%d", l), c, f); err != nil {
			return nil, err
		}
		if n.down[l], err = conv(fmt.Sprintf("down%d", l), c+g, c, 2); err != nil {
			return nil, err
		}
		w, err := get(fmt.Sprintf("up%d.w", l), c+g, c, 2, 2)
		if err != nil {
			return nil, err
		}
		b, err := get(fmt.Sprintf("up%d.b", l), c)
		if err != nil {
			return nil, err
		}
		cin := c + g
		for di := 0; di < 2; di++ {
			for dj := 0; dj < 2; dj++ {
				n.up[l].sub[di*2+dj] = newMdxConv(c, cin, 1, func(co, ci int) float32 { return w[((ci*c+co)*2+di)*2+dj] },
					func(co int) float32 { return b[co] })
			}
		}
	}
	if n.mid, err = block("mid", g*(L+1), n.dimF>>L); err != nil {
		return nil, err
	}
	n.plan(workers)
	return n, nil
}

func newMdxRemover(net *mdxNet, spec mdxSpec) *mdxRemover {
	r := &mdxRemover{net: net, nfft: spec.nfft, hop: 1024, bins: net.dimF, dimT: net.dimT}
	r.chunk = r.hop * (r.dimT - 1)
	r.trim = r.nfft / 2
	r.gen = r.chunk - 2*r.trim
	r.ov = 8192
	r.step = r.gen - r.ov
	r.fade = make([]float64, r.ov)
	for i := range r.fade {
		s := math.Sin(math.Pi / 2 * (float64(i) + 0.5) / float64(r.ov))
		r.fade[i] = s * s
	}
	r.fft = newCFFT(r.nfft)
	r.win = make([]float64, r.nfft)
	for i := range r.win {
		r.win[i] = 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(r.nfft)) // periodic Hann
	}
	// torch.istft divides by the windows' squares added up; the same for every chunk.
	env := make([]float64, (r.dimT-1)*r.hop+r.nfft)
	for t := 0; t < r.dimT; t++ {
		for i, w := range r.win {
			env[t*r.hop+i] += w * w
		}
	}
	r.invEnv = make([]float64, r.chunk)
	for i := range r.invEnv {
		r.invEnv[i] = 1 / math.Max(env[i+r.nfft/2], 1e-11)
	}
	for c := 0; c < 2; c++ {
		r.in[c] = make([]float32, r.chunk)
		r.acc[c] = make([]float64, len(env))
		r.tail[c] = make([]float64, r.ov)
	}
	r.frames = make([]float32, r.dimT*2*r.nfft)
	for range net.work {
		r.z = append(r.z, make([]complex128, r.nfft))
		r.tmp = append(r.tmp, make([]complex128, r.nfft))
	}
	return r
}

// spectrum fills the network's input with the chunk's spectrum.
func (r *mdxRemover) spectrum() {
	n, in := r.net, &r.net.in
	half := r.nfft / 2
	r.net.par.run(r.dimT, func(t, wk int) {
		z, tmp := r.z[wk], r.tmp[wk]
		for i := 0; i < r.nfft; i++ {
			p := t*r.hop + i - half
			if p < 0 {
				p = -p
			}
			if p >= r.chunk {
				p = 2*(r.chunk-1) - p
			}
			z[i] = complex(float64(r.in[0][p])*r.win[i], float64(r.in[1][p])*r.win[i])
		}
		r.fft.forward(z, tmp)
		var pl [4][]float32
		for c := range pl {
			pl[c] = in.d[in.at(c, t, 0):][:r.bins]
		}
		for k := 0; k < r.bins; k++ {
			if k < 3 {
				pl[0][k], pl[1][k], pl[2][k], pl[3][k] = 0, 0, 0, 0
				continue
			}
			a, b := real(z[k]), imag(z[k])
			nk := z[r.nfft-k]
			c, d := real(nk), imag(nk)
			// L = (Z[k] + conj Z[n-k]) / 2, R = (Z[k] - conj Z[n-k]) / 2i
			pl[0][k], pl[1][k] = float32((a+c)*0.5), float32((b-d)*0.5)
			pl[2][k], pl[3][k] = float32((b+d)*0.5), float32((c-a)*0.5)
		}
	})
	_ = n
}

// sound turns the network's output back into the chunk's voices (in acc, scaled).
func (r *mdxRemover) sound() {
	T, F, N := r.dimT, r.bins, r.nfft
	out := r.net.out
	r.net.par.run(T, func(t, wk int) {
		z, tmp := r.z[wk], r.tmp[wk]
		clear(z)
		for k := 0; k < F; k++ {
			lr, li := float64(out[(0*T+t)*F+k]), float64(out[(1*T+t)*F+k])
			rr, ri := float64(out[(2*T+t)*F+k]), float64(out[(3*T+t)*F+k])
			if k == 0 {
				li, ri = 0, 0 // a real signal's mean has no imaginary part (irfft drops it)
			}
			// Z = L + iR on both halves of the circle.
			z[k] = complex(lr-ri, li+rr)
			if k > 0 {
				z[N-k] = complex(lr+ri, rr-li)
			}
		}
		r.fft.inverse(z, tmp)
		fr := r.frames[t*2*N:][:2*N]
		for i := 0; i < N; i++ {
			fr[i] = float32(real(z[i]) * r.win[i])
			fr[N+i] = float32(imag(z[i]) * r.win[i])
		}
	})
	for c := 0; c < 2; c++ {
		clear(r.acc[c])
	}
	for t := 0; t < T; t++ {
		fr := r.frames[t*2*N:][:2*N]
		l, rr := r.acc[0][t*r.hop:][:N], r.acc[1][t*r.hop:][:N]
		for i := 0; i < N; i++ {
			l[i] += float64(fr[i])
			rr[i] += float64(fr[N+i])
		}
	}
}

// run reads interleaved little-endian float stereo from src until it ends and writes the
// voices in the same format (exactly as many samples) to dst. progress (if set) gets the
// samples per channel written so far, after every chunk. It returns that count.
func (r *mdxRemover) run(ctx context.Context, src io.Reader, dst io.Writer, progress func(int64)) (int64, error) {
	rd := &mrReader{src: bufio.NewReaderSize(src, 1<<16)}
	clear(r.in[0][:r.trim])
	clear(r.in[1][:r.trim])
	if err := rd.fill(r.in, r.trim, r.chunk-r.trim); err != nil {
		return 0, err
	}
	out := bufio.NewWriterSize(dst, 1<<16)
	var buf [8]byte
	put := func(a, b float64) error {
		binary.LittleEndian.PutUint32(buf[0:], math.Float32bits(float32(a)))
		binary.LittleEndian.PutUint32(buf[4:], math.Float32bits(float32(b)))
		_, err := out.Write(buf[:])
		return err
	}
	var emitted int64
	half := r.nfft / 2
	// Within a chunk too (a chunk can take half a minute on a slow computer): up to where
	// it will have written.
	var upTo int64
	if progress != nil {
		r.net.onProgress = func(share float64) { progress(emitted + int64(share*float64(upTo-emitted))) }
		defer func() { r.net.onProgress = nil }()
	}
	for chunk := int64(0); ; chunk++ {
		if err := ctx.Err(); err != nil {
			return emitted, err
		}
		if rd.ended && emitted >= rd.total {
			break
		}
		upTo = chunk*int64(r.step) + int64(r.gen-r.ov)
		if rd.ended && rd.total <= chunk*int64(r.step)+int64(r.gen) {
			upTo = rd.total
		}
		r.spectrum()
		if err := r.net.forward(ctx); err != nil {
			return emitted, err
		}
		r.sound()
		// This chunk's part: samples [start, start+gen) of the sound, the first ov of them
		// faded in over the previous chunk's last (kept in tail), the last ov kept for the
		// next one, unless the sound ends before.
		start := chunk * int64(r.step)
		last := rd.ended && rd.total <= start+int64(r.gen)
		end := start + int64(r.gen-r.ov)
		if last {
			end = rd.total
		}
		for p := emitted; p < end; p++ {
			i := int(p - start)
			j := r.trim + i
			s := r.invEnv[j]
			a, b := r.acc[0][j+half]*s, r.acc[1][j+half]*s
			if chunk > 0 && i < r.ov {
				f := r.fade[i]
				a, b = r.tail[0][i]*(1-f)+a*f, r.tail[1][i]*(1-f)+b*f
			}
			if err := put(a, b); err != nil {
				return emitted, err
			}
			emitted++
		}
		if err := out.Flush(); err != nil {
			return emitted, err
		}
		if progress != nil {
			progress(emitted)
		}
		if last {
			break
		}
		for i := 0; i < r.ov; i++ {
			j := r.trim + r.gen - r.ov + i
			s := r.invEnv[j]
			r.tail[0][i], r.tail[1][i] = r.acc[0][j+half]*s, r.acc[1][j+half]*s
		}
		// The next chunk: keep the overlap, read the rest.
		for c := 0; c < 2; c++ {
			copy(r.in[c], r.in[c][r.step:])
		}
		if err := rd.fill(r.in, r.chunk-r.step, r.step); err != nil {
			return emitted, err
		}
	}
	return emitted, out.Flush()
}

func (n *mdxNet) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "MDX-Net %d bins × %d frames, %d channels, %d levels", n.dimF, n.dimT, n.g, n.levels)
	return b.String()
}
