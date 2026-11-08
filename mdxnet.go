package main

// The voice network: MDX-Net, KUIELab's U-Net of "TFC-TDF" blocks (Kim_Vocal_2, trained
// by KimberleyJSN, from Ultimate Vocal Remover). It reads the spectrum of about 6 seconds
// of stereo sound (4 planes: left and right, real and imaginary parts; 256 frames of 3072
// bins) and gives the spectrum of the voices in it.
//
// Each block: three 3x3 convolutions (with ReLU), then a TDF: a small dense network along
// the frequencies of every frame (bins → bins/8 → bins, each followed by its batch norm
// and ReLU) added to its input. Five times down (2x2 convolutions of stride 2), a block in
// the middle, five times up (2x2 transposed convolutions, batch norm, ReLU, multiplied by
// the block of the same level on the way down).
//
// Layout: a level's activations are [channel][frame][bin], every frame a row of bins with
// 16 zeros on each side (so rows start on 64-byte boundaries) and a row of zeros above and
// below each channel, so that the 3x3 convolutions read their input in place
// (mdxkernel.go): row stride bins+32.

import (
	"context"
	"fmt"
	"unsafe"
)

// mdxAct is a level's activations in the padded layout, over part of a larger buffer.
type mdxAct struct {
	c, t, f int
	rs, cs  int // row and channel strides
	d       []float32
}

// mdxPad is the zeros on each side of a row.
const mdxPad = 16

func mdxActSize(c, t, f int) int { return c * (t + 2) * (f + 2*mdxPad) }

// mdxAlloc makes n floats starting on a 64-byte boundary (the Winograd convolutions write
// whole aligned vectors past the caches).
func mdxAlloc(n int) []float32 {
	s := make([]float32, n+16)
	off := int(uintptr(unsafe.Pointer(&s[0]))%64) / 4
	if off != 0 {
		off = 16 - off
	}
	return s[off : off+n : off+n]
}

func mdxActOn(buf []float32, c, t, f int) mdxAct {
	a := mdxAct{c: c, t: t, f: f, rs: f + 2*mdxPad, cs: (t + 2) * (f + 2*mdxPad)}
	a.d = buf[:mdxActSize(c, t, f)]
	return a
}

// at is the index of bin f of frame t in channel c.
func (a *mdxAct) at(c, t, f int) int { return c*a.cs + (t+1)*a.rs + mdxPad + f }

// zeroPads clears what surrounds the values (another layer may have used that memory).
func (a *mdxAct) zeroPads() {
	for c := 0; c < a.c; c++ {
		ch := a.d[c*a.cs : (c+1)*a.cs]
		clear(ch[:a.rs])
		clear(ch[(a.t+1)*a.rs:])
		for t := 1; t <= a.t; t++ {
			r := ch[t*a.rs : (t+1)*a.rs]
			clear(r[:mdxPad])
			clear(r[mdxPad+a.f:])
		}
	}
}

// mdxConv is a convolution's weights packed for convTile: blocks of 6 output channels,
// each k values per channel (input channels times taps), interleaved.
type mdxConv struct {
	cin, cout, taps, k, blocks int
	panel                      []float32 // [blocks][k][6]
	scale, bias                [][6]float32
}

// newMdxConv packs w, read as w(co, p) for p = ci*taps + tap.
func newMdxConv(cout, cin, taps int, w func(co, p int) float32, bias func(co int) float32) *mdxConv {
	l := &mdxConv{cin: cin, cout: cout, taps: taps, k: cin * taps, blocks: (cout + 5) / 6}
	l.panel = make([]float32, l.blocks*l.k*6)
	l.scale = make([][6]float32, l.blocks)
	l.bias = make([][6]float32, l.blocks)
	for b := 0; b < l.blocks; b++ {
		for i := 0; i < 6; i++ {
			co := 6*b + i
			if co >= cout {
				continue
			}
			l.scale[b][i] = 1
			l.bias[b][i] = bias(co)
			for p := 0; p < l.k; p++ {
				l.panel[(b*l.k+p)*6+i] = w(co, p)
			}
		}
	}
	return l
}

// mdxTDF is a block's dense network along the frequencies.
type mdxTDF struct {
	f, fb, fbp     int       // bins, bins of the middle layer, that rounded up to 16
	w1, w2         []float32 // packed: [fbp/16][f][16], [f/16][fb][16]
	s1, b1, s2, b2 []float32 // the batch norms, per channel: x*s + b
}

type mdxBlock struct {
	conv [3]*mdxConv
	wino [3]*mdxWino // instead of conv, where the Winograd way is used (mdxwino.go)
	tdf  *mdxTDF
}

type mdxUp struct{ sub [4]*mdxConv } // the transposed convolution, for each of the 2x2 places (BN folded in)

// mdxNet is the network and the memory it works in.
type mdxNet struct {
	dimF, dimT, g, levels, bn int
	first, last               *mdxConv
	enc, dec                  []mdxBlock
	down                      []*mdxConv
	mid                       mdxBlock
	up                        []mdxUp
	c, t, f                   []int // channels, frames, bins of each level (levels+1)

	par         *parRunner
	q0, p0      []float32 // the two large buffers (mdxNet.plan)
	in          mdxAct    // the spectrum given
	out         []float32 // the voices' spectrum, [4][dimT][dimF]
	work        []*mdxWorker
	offCache    map[string][]int
	skips, bufs []mdxAct // per level: the block on the way down, and the other buffer

	// onProgress (if set) learns the share of the network done, after each layer.
	onProgress  func(share float64)
	done, total float64 // the work of the layers so far and of all (multiply-adds, roughly)
}

type mdxWorker struct {
	ep   *tileEp
	junk []float32 // rows of a tile that are not wanted go here
	e    []float32 // a 2x2 convolution's input rows, split into even and odd bins
	tile []float32 // a transposed convolution's results before they are spread
	rows [6]unsafe.Pointer
	// The Winograd convolutions' tiles: V, M (36 per channel and tile), scratch.
	wv, wm, wtmp []float32
}

func (n *mdxNet) checkShapes() error {
	if n.levels < 1 || n.g < 6 || n.dimF%(16<<n.levels) != 0 || n.dimT%(1<<n.levels) != 0 || n.bn < 1 {
		return fmt.Errorf("unexpected network shape %d×%d, %d channels, %d levels", n.dimF, n.dimT, n.g, n.levels)
	}
	return nil
}

// plan lays the levels out over two large buffers: level l works in [skip_l | other_l],
// level l+1 inside other_l (see forward).
func (n *mdxNet) plan(workers int) {
	L := n.levels
	n.c, n.t, n.f = make([]int, L+1), make([]int, L+1), make([]int, L+1)
	for l := 0; l <= L; l++ {
		n.c[l], n.t[l], n.f[l] = n.g*(l+1), n.dimT>>l, n.dimF>>l
	}
	size := func(l int) int { return mdxActSize(n.c[l], n.t[l], n.f[l]) }
	// other_l must hold level l+1's two buffers.
	need := make([]int, L+2) // room for [skip_l | other_l] from level l down
	need[L+1] = 0
	for l := L; l >= 0; l-- {
		need[l] = size(l) + max(size(l), need[l+1])
	}
	n.q0 = mdxAlloc(size(0))
	n.p0 = mdxAlloc(need[0] - size(0))
	n.skips, n.bufs = make([]mdxAct, L+1), make([]mdxAct, L+1)
	n.skips[0] = mdxActOn(n.q0, n.c[0], n.t[0], n.f[0])
	n.bufs[0] = mdxActOn(n.p0, n.c[0], n.t[0], n.f[0])
	room := n.p0
	for l := 1; l <= L; l++ {
		n.skips[l] = mdxActOn(room, n.c[l], n.t[l], n.f[l])
		rest := room[size(l):]
		n.bufs[l] = mdxActOn(rest, n.c[l], n.t[l], n.f[l])
		room = rest
	}
	n.in = mdxActOn(mdxAlloc(mdxActSize(4, n.dimT, n.dimF)), 4, n.dimT, n.dimF)
	n.out = make([]float32, 4*n.dimT*n.dimF)
	n.par = &parRunner{workers: workers}
	n.offCache = map[string][]int{}
	maxE := 0
	for l := 0; l < L; l++ {
		maxE = max(maxE, n.c[l]*4*(n.f[l]/2))
	}
	maxV, maxM := 0, 0
	for _, bl := range append(append([]mdxBlock{n.mid}, n.enc...), n.dec...) {
		for _, w := range bl.wino {
			if w != nil {
				maxV, maxM = max(maxV, 36*w.cin*w.nb), max(maxM, 36*w.cout*w.nb)
			}
		}
	}
	for w := 0; w < workers; w++ {
		wk := &mdxWorker{ep: new(tileEp), junk: make([]float32, 16), e: make([]float32, maxE),
			tile: make([]float32, 2*6*16)}
		if maxV > 0 {
			wk.wv, wk.wm, wk.wtmp = make([]float32, maxV), make([]float32, maxM), make([]float32, 40*8)
		}
		n.work = append(n.work, wk)
	}
	_ = n.pass(context.Background(), false)
	n.total, n.done = n.done, 0
}

func (n *mdxNet) offsets(key string, k int, at func(p int) int) []int {
	if o, ok := n.offCache[key]; ok {
		return o
	}
	o := make([]int, k)
	for p := range o {
		o[p] = 4 * at(p)
	}
	n.offCache[key] = o
	return o
}

func ptr(s []float32, i int) unsafe.Pointer { return unsafe.Pointer(&s[i]) }

// conv runs a 3x3 (taps 9) or 1x1 (taps 1) convolution of stride 1 from in to out (both
// in the padded layout, same frames and bins), with ReLU.
func (n *mdxNet) conv(l *mdxConv, in, out *mdxAct, relu bool) {
	in.zeroPads()
	var offs []int
	if l.taps == 9 {
		offs = n.offsets(fmt.Sprint("c9/", l.cin, in.t, in.f), l.k, func(p int) int {
			ci, tap := p/9, p%9
			return ci*in.cs + (tap/3)*in.rs + tap%3 - 1
		})
	} else {
		offs = n.offsets(fmt.Sprint("c1/", l.cin, in.t, in.f), l.k, func(p int) int { return p*in.cs + in.rs })
	}
	perRow := in.f / 16
	flags := uintptr(0)
	if relu {
		flags = epReLU
	}
	n.par.run(in.t, func(t, wk int) {
		w := n.work[wk]
		ep := w.ep
		ep.flags = flags
		for x := 0; x < perRow; x++ {
			f0 := 16 * x
			base := &in.d[t*in.rs+mdxPad+f0]
			for b := 0; b < l.blocks; b++ {
				for i := 0; i < 6; i++ {
					if co := 6*b + i; co < l.cout {
						ep.orow[i] = ptr(out.d, out.at(co, t, f0))
					} else {
						ep.orow[i] = ptr(w.junk, 0)
					}
				}
				ep.scale, ep.bias = l.scale[b], l.bias[b]
				convTile(l.k, &l.panel[b*l.k*6], base, &offs[0], ep)
			}
		}
	})
}

// tdf adds the block's dense network to x (in place); h is scratch memory. Each task
// takes 4 blocks of 6 rows and goes through the weights' column panels once, each panel
// used for all 24 rows while it is in the cache.
func (n *mdxNet) tdf(d *mdxTDF, x *mdxAct, h []float32) {
	rows := x.c * x.t
	blocks := (rows + 5) / 6
	const per = 4
	rowAt := func(r int) int { return x.at(r/x.t, r%x.t, 0) }
	type blk struct {
		rows        [6]unsafe.Pointer
		scale, bias [6]float32
		orow        [6]int // row index, or -1
	}
	// h = relu(bn1(x · w1))
	n.par.run((blocks+per-1)/per, func(task, wk int) {
		w := n.work[wk]
		ep := w.ep
		ep.flags = epReLU
		var bs [per]blk
		nbk := 0
		for b := per * task; b < min(per*task+per, blocks); b++ {
			k := &bs[nbk]
			nbk++
			for i := 0; i < 6; i++ {
				r := min(6*b+i, rows-1)
				k.rows[i] = ptr(x.d, rowAt(r))
				c := r / x.t
				k.scale[i], k.bias[i] = d.s1[c], d.b1[c]
				k.orow[i] = -1
				if r := 6*b + i; r < rows {
					k.orow[i] = r
				}
			}
		}
		for jb := 0; jb < d.fbp/16; jb++ {
			panel := &d.w1[jb*d.f*16]
			for bi := 0; bi < nbk; bi++ {
				k := &bs[bi]
				for i := 0; i < 6; i++ {
					if k.orow[i] >= 0 {
						ep.orow[i] = ptr(h, k.orow[i]*d.fbp+16*jb)
					} else {
						ep.orow[i] = ptr(w.junk, 0)
					}
				}
				ep.scale, ep.bias = k.scale, k.bias
				w.rows = k.rows
				rowsTile(d.f, &w.rows, panel, ep)
			}
		}
	})
	// x += relu(bn2(h · w2))
	n.par.run((blocks+per-1)/per, func(task, wk int) {
		w := n.work[wk]
		ep := w.ep
		ep.flags = epReLU | epAdd
		var bs [per]blk
		nbk := 0
		for b := per * task; b < min(per*task+per, blocks); b++ {
			k := &bs[nbk]
			nbk++
			for i := 0; i < 6; i++ {
				r := min(6*b+i, rows-1)
				k.rows[i] = ptr(h, r*d.fbp)
				c := r / x.t
				k.scale[i], k.bias[i] = d.s2[c], d.b2[c]
				k.orow[i] = -1
				if r := 6*b + i; r < rows {
					k.orow[i] = rowAt(r)
				}
			}
		}
		for jb := 0; jb < d.f/16; jb++ {
			panel := &d.w2[jb*d.fb*16]
			for bi := 0; bi < nbk; bi++ {
				k := &bs[bi]
				for i := 0; i < 6; i++ {
					if k.orow[i] >= 0 {
						ep.orow[i] = ptr(x.d, k.orow[i]+16*jb)
					} else {
						ep.orow[i] = ptr(w.junk, 0)
					}
					ep.drow[i] = ep.orow[i]
				}
				ep.scale, ep.bias = k.scale, k.bias
				w.rows = k.rows
				rowsTile(d.fb, &w.rows, panel, ep)
			}
		}
	})
}

// conv3 runs the block's i-th 3x3 convolution (with ReLU).
func (n *mdxNet) conv3(bl *mdxBlock, i int, in, out *mdxAct) {
	if bl.wino[i] != nil {
		n.wconv(bl.wino[i], in, out)
	} else {
		n.conv(bl.conv[i], in, out, true)
	}
}

// block runs a TFC-TDF block: a → b → a → b, the TDF added to b.
func (n *mdxNet) block(bl *mdxBlock, a, b *mdxAct, h []float32) {
	n.conv3(bl, 0, a, b)
	n.conv3(bl, 1, b, a)
	n.conv3(bl, 2, a, b)
	n.tdf(bl.tdf, b, h)
}

// downsample: 2x2 convolution of stride 2 with ReLU, from in to out (half the frames and bins).
func (n *mdxNet) downsample(l *mdxConv, in, out *mdxAct) {
	fh := in.f / 2
	offs := n.offsets(fmt.Sprint("d/", l.cin, in.f), l.k, func(p int) int { return p * fh })
	n.par.run(out.t, func(t, wk int) {
		w := n.work[wk]
		// The two input frames, split into even and odd bins: e[ci][dt][df][f].
		for ci := 0; ci < l.cin; ci++ {
			for dt := 0; dt < 2; dt++ {
				src := in.d[in.at(ci, 2*t+dt, 0):][:in.f]
				ev := w.e[((ci*2+dt)*2)*fh:][:fh]
				od := w.e[((ci*2+dt)*2+1)*fh:][:fh]
				for j := 0; j < fh; j++ {
					ev[j], od[j] = src[2*j], src[2*j+1]
				}
			}
		}
		ep := w.ep
		ep.flags = epReLU
		for x := 0; x < fh/16; x++ {
			f0 := 16 * x
			for b := 0; b < l.blocks; b++ {
				for i := 0; i < 6; i++ {
					if co := 6*b + i; co < l.cout {
						ep.orow[i] = ptr(out.d, out.at(co, t, f0))
					} else {
						ep.orow[i] = ptr(w.junk, 0)
					}
				}
				ep.scale, ep.bias = l.scale[b], l.bias[b]
				convTile(l.k, &l.panel[b*l.k*6], &w.e[f0], &offs[0], ep)
			}
		}
	})
}

// upsample: the 2x2 transposed convolution of stride 2 (batch norm folded in) with ReLU,
// multiplied in place into skip (twice the frames and bins of in).
func (n *mdxNet) upsample(u *mdxUp, in, skip *mdxAct) {
	l0 := u.sub[0]
	offs := n.offsets(fmt.Sprint("c1/", l0.cin, in.t, in.f), l0.k, func(p int) int { return p*in.cs + in.rs })
	n.par.run(in.t, func(t, wk int) {
		w := n.work[wk]
		ep := w.ep
		ep.flags = epReLU
		for x := 0; x < in.f/16; x++ {
			f0 := 16 * x
			base := &in.d[t*in.rs+mdxPad+f0]
			for b := 0; b < l0.blocks; b++ {
				for di := 0; di < 2; di++ {
					for dj := 0; dj < 2; dj++ {
						l := u.sub[di*2+dj]
						for i := 0; i < 6; i++ {
							ep.orow[i] = ptr(w.tile, (dj*6+i)*16)
						}
						ep.scale, ep.bias = l.scale[b], l.bias[b]
						convTile(l.k, &l.panel[b*l.k*6], base, &offs[0], ep)
					}
					// Spread the two tiles over the row 2t+di, interleaved, times the skip.
					for i := 0; i < 6; i++ {
						co := 6*b + i
						if co >= l0.cout {
							break
						}
						dst := skip.d[skip.at(co, 2*t+di, 2*f0):][:32]
						a, c := w.tile[i*16:][:16], w.tile[(6+i)*16:][:16]
						for j := 0; j < 16; j++ {
							dst[2*j] *= a[j]
							dst[2*j+1] *= c[j]
						}
					}
				}
			}
		}
	})
}

// forward runs the network on n.in (the caller fills its values) into n.out.
func (n *mdxNet) forward(ctx context.Context) error {
	n.done = 0
	return n.pass(ctx, true)
}

// pass goes through the layers: running them (run), or only adding up their work.
func (n *mdxNet) pass(ctx context.Context, run bool) error {
	layer := func(work float64, f func()) error {
		n.done += work
		if !run {
			return nil
		}
		f()
		if n.onProgress != nil && n.total > 0 {
			n.onProgress(min(1, n.done/n.total))
		}
		return ctx.Err()
	}
	area := func(a *mdxAct) float64 { return float64(a.t * a.f) }
	conv3 := func(bl *mdxBlock, i int, in, out *mdxAct) error {
		c := float64(in.c)
		work := c * c * 9 * area(in)
		if bl.wino[i] != nil {
			work *= 0.45 // about the time it takes, against a plain convolution
		}
		return layer(work, func() { n.conv3(bl, i, in, out) })
	}
	tdf := func(d *mdxTDF, x *mdxAct, h []float32) error {
		return layer(2*float64(x.c)*area(x)*float64(d.fb), func() { n.tdf(d, x, h) })
	}
	block := func(bl *mdxBlock, a, b *mdxAct, h []float32) error {
		for i, io := range [3][2]*mdxAct{{a, b}, {b, a}, {a, b}} {
			if err := conv3(bl, i, io[0], io[1]); err != nil {
				return err
			}
		}
		return tdf(bl.tdf, b, h)
	}
	L := n.levels
	// The spectrum's 4 planes → g channels (ReLU), in level 0's other buffer.
	if err := layer(4*float64(n.g)*area(&n.in), func() { n.conv(n.first, &n.in, &n.bufs[0], true) }); err != nil {
		return err
	}
	for l := 0; l < L; l++ {
		a, b := &n.bufs[l], &n.skips[l]
		if err := block(&n.enc[l], a, b, a.d); err != nil {
			return err
		}
		next := &n.bufs[l+1]
		if err := layer(float64(next.c*b.c*4)*area(next), func() { n.downsample(n.down[l], b, next) }); err != nil {
			return err
		}
	}
	// The middle: its result ends in skips[L].
	if err := block(&n.mid, &n.bufs[L], &n.skips[L], n.bufs[L].d); err != nil {
		return err
	}
	res := &n.skips[L]
	for l := L - 1; l >= 0; l-- {
		skip, other := &n.skips[l], &n.bufs[l]
		in := res
		if err := layer(float64(4*in.c*skip.c)*area(in), func() { n.upsample(&n.up[l], in, skip) }); err != nil {
			return err
		}
		// skip → other → skip → other, the TDF added to other.
		bl := &n.dec[l]
		for i, io := range [3][2]*mdxAct{{skip, other}, {other, skip}, {skip, other}} {
			if err := conv3(bl, i, io[0], io[1]); err != nil {
				return err
			}
		}
		if err := tdf(bl.tdf, other, skip.d); err != nil {
			return err
		}
		res = other
	}
	// g channels → the 4 planes of the voices' spectrum (no ReLU).
	return layer(4*float64(n.g)*area(res), func() { n.final(res) })
}

// final is the last 1x1 convolution, into n.out ([4][frames][bins], not padded).
func (n *mdxNet) final(in *mdxAct) {
	l := n.last
	offs := n.offsets(fmt.Sprint("c1/", l.cin, in.t, in.f), l.k, func(p int) int { return p*in.cs + in.rs })
	T, F := n.dimT, n.dimF
	n.par.run(in.t, func(t, wk int) {
		w := n.work[wk]
		ep := w.ep
		ep.flags = 0
		for x := 0; x < in.f/16; x++ {
			f0 := 16 * x
			for i := 0; i < 6; i++ {
				if i < l.cout {
					ep.orow[i] = ptr(n.out, (i*T+t)*F+f0)
				} else {
					ep.orow[i] = ptr(w.junk, 0)
				}
			}
			ep.scale, ep.bias = l.scale[0], l.bias[0]
			convTile(l.k, &l.panel[0], &in.d[t*in.rs+mdxPad+f0], &offs[0], ep)
		}
	})
}
