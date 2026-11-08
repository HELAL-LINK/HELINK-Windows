package main

import (
	"fmt"
	"math"
	"math/rand"
	"testing"
	"unsafe"
)

func addr(s []float32, i int) unsafe.Pointer { return unsafe.Pointer(&s[i]) }

// The vector loops give what the plain ones give, for every way of finishing a tile.
func TestMdxKernels(t *testing.T) {
	if mdxKernel == "go" {
		t.Skip("no AVX2 here")
	}
	rnd := rand.New(rand.NewSource(7))
	fill := func(n int) []float32 {
		s := make([]float32, n)
		for i := range s {
			s[i] = float32(rnd.NormFloat64())
		}
		return s
	}
	for _, k := range []int{1, 2, 3, 17, 432} {
		panel := fill(6 * k)
		src := fill(64 * k)
		offs := make([]int, k)
		for p := range offs {
			offs[p] = rnd.Intn(48*k) * 4
		}
		bpanel := fill(16 * k)
		rows := fill(6*k + 40)
		var rp [6]unsafe.Pointer
		for i := range rp {
			rp[i] = addr(rows, rnd.Intn(5*k+30))
		}
		for flags := uintptr(0); flags < 8; flags++ {
			if flags&epMul != 0 && flags&epAdd != 0 {
				continue
			}
			d := fill(6 * 16)
			run := func(f func(ep *tileEp), out []float32) {
				ep := new(tileEp)
				for i := 0; i < 6; i++ {
					ep.scale[i], ep.bias[i] = float32(rnd.NormFloat64()), float32(rnd.NormFloat64())
					ep.orow[i], ep.drow[i] = addr(out, 16*i), addr(d, 16*(5-i))
				}
				ep.flags = flags
				f(ep)
			}
			check := func(name string, a, b []float32) {
				for i := range a {
					if math.Abs(float64(a[i]-b[i])) > 1e-4*(1+math.Abs(float64(a[i]))) {
						t.Fatalf("%s k=%d flags=%d: [%d] %g vs %g", name, k, flags, i, a[i], b[i])
					}
				}
			}
			seed := rnd.Int63()
			o1, o2 := make([]float32, 96), make([]float32, 96)
			rnd.Seed(seed)
			run(func(ep *tileEp) { convTileGo(k, &panel[0], &src[0], &offs[0], ep) }, o1)
			rnd.Seed(seed)
			run(func(ep *tileEp) { convTileAVX2(k, &panel[0], &src[0], &offs[0], ep) }, o2)
			check("conv", o1, o2)
			rnd.Seed(seed)
			run(func(ep *tileEp) { rowsTileGo(k, &rp, &bpanel[0], ep) }, o1)
			rnd.Seed(seed)
			run(func(ep *tileEp) { rowsTileAVX2(k, &rp, &bpanel[0], ep) }, o2)
			check("rows", o1, o2)
		}
	}
}

func BenchmarkMdxConvTile(b *testing.B) {
	k := 432
	panel := make([]float32, 6*k)
	src := make([]float32, 64*k)
	offs := make([]int, k)
	for p := range offs {
		offs[p] = (p % 48) * 64 * 4
	}
	out := make([]float32, 96)
	ep := new(tileEp)
	for i := range ep.orow {
		ep.orow[i] = addr(out, 16*i)
	}
	for i := 0; i < b.N; i++ {
		convTile(k, &panel[0], &src[0], &offs[0], ep)
	}
	b.ReportMetric(float64(96*k)*float64(b.N)/b.Elapsed().Seconds()/1e9, "GMAC/s")
}

func BenchmarkMdxRowsTile(b *testing.B) {
	k := 3072
	rows := make([]float32, 6*k)
	panel := make([]float32, 16*k)
	var rp [6]unsafe.Pointer
	for i := range rp {
		rp[i] = addr(rows, i*k)
	}
	out := make([]float32, 96)
	ep := new(tileEp)
	for i := range ep.orow {
		ep.orow[i] = addr(out, 16*i)
	}
	for i := 0; i < b.N; i++ {
		rowsTile(k, &rp, &panel[0], ep)
	}
	b.ReportMetric(float64(96*k)*float64(b.N)/b.Elapsed().Seconds()/1e9, "GMAC/s")
}

func TestWinoTransforms(t *testing.T) {
	if winoIn == nil {
		t.Skip("no AVX2 here")
	}
	rnd := rand.New(rand.NewSource(11))
	rs := 4 * 64 // bytes between rows
	src := make([]float32, 6*64)
	for i := range src {
		src[i] = float32(rnd.NormFloat64())
	}
	xs := 4 * 24
	a, b := make([]float32, 36*24), make([]float32, 36*24)
	tmp := make([]float32, 40*8)
	winoInGo(&src[0], rs, &a[0], xs, &tmp[0], &src[0])
	winoInAVX2(&src[0], rs, &b[0], xs, &tmp[0], &src[0])
	for x := 0; x < 36; x++ {
		for m := 0; m < 8; m++ {
			i := x*24 + m
			if math.Abs(float64(a[i]-b[i])) > 1e-4*(1+math.Abs(float64(a[i]))) {
				t.Fatalf("in: V[%d] tile %d: %g vs %g", x, m, a[i], b[i])
			}
		}
	}
	bias := float32(0.3)
	o1, o2 := make([]float32, 4*48), mdxAlloc(4*48)
	winoOutGo(&a[0], xs, &bias, &o1[0], 4*48, &tmp[0])
	winoOutAVX2(&a[0], xs, &bias, &o2[0], 4*48, &tmp[0])
	for r := 0; r < 4; r++ {
		for c := 0; c < 32; c++ {
			i := r*48 + c
			if math.Abs(float64(o1[i]-o2[i])) > 1e-3*(1+math.Abs(float64(o1[i]))) {
				t.Fatalf("out: row %d col %d: %g vs %g", r, c, o1[i], o2[i])
			}
		}
	}
}

func BenchmarkWinoIn(b *testing.B) {
	if winoIn == nil {
		b.Skip()
	}
	src := make([]float32, 6*3088)
	dst := make([]float32, 36*32)
	tmp := make([]float32, 320)
	for i := 0; i < b.N; i++ {
		winoIn(&src[0], 4*3088, &dst[0], 4*32, &tmp[0], &src[0])
	}
}

func BenchmarkWinoOut(b *testing.B) {
	if winoOut == nil {
		b.Skip()
	}
	src := make([]float32, 36*32)
	dst := mdxAlloc(4 * 3088)
	tmp := make([]float32, 320)
	bias := float32(0)
	for i := 0; i < b.N; i++ {
		winoOut(&src[0], 4*32, &bias, &dst[0], 4*3088, &tmp[0])
	}
}

// One Winograd convolution of level 0's size, on one thread, the three steps timed apart.
func BenchmarkWinoLayer(b *testing.B) {
	if winoIn == nil {
		b.Skip()
	}
	c, T, F := 48, 256, 3072
	rnd := rand.New(rand.NewSource(1))
	w := make([]float32, c*c*9)
	for i := range w {
		w[i] = float32(rnd.NormFloat64() * 0.05)
	}
	l := newMdxWino(c, c, w, make([]float32, c), 32)
	n := &mdxNet{offCache: map[string][]int{}, par: &parRunner{workers: 1}}
	n.work = []*mdxWorker{{ep: new(tileEp), junk: make([]float32, 16), wv: make([]float32, 36*c*32), wm: make([]float32, 36*c*32), wtmp: make([]float32, 320)}}
	inb, outb := mdxAlloc(mdxActSize(c, T, F)), mdxAlloc(mdxActSize(c, T, F))
	for i := range inb {
		inb[i] = float32(rnd.NormFloat64())
	}
	in, out := mdxActOn(inb, c, T, F), mdxActOn(outb, c, T, F)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		n.wconv(l, &in, &out)
	}
	b.ReportMetric(float64(c*c*9*T*F)*float64(b.N)/b.Elapsed().Seconds()/1e9, "direct-GMAC/s")
}

func BenchmarkMdxConvTileK48(b *testing.B) {
	k := 48
	panel := make([]float32, 6*k)
	src := make([]float32, 36*32*k)
	offs := make([]int, k)
	for p := range offs {
		offs[p] = p * 36 * 32 * 4
	}
	out := make([]float32, 6*36*32)
	ep := new(tileEp)
	for i := range ep.orow {
		ep.orow[i] = addr(out, i*36*32)
	}
	for i := 0; i < b.N; i++ {
		convTile(k, &panel[0], &src[0], &offs[0], ep)
	}
	b.ReportMetric(float64(96*k)*float64(b.N)/b.Elapsed().Seconds()/1e9, "GMAC/s")
}

func BenchmarkWinoGemm(b *testing.B) {
	if winoGemm == nil {
		b.Skip()
	}
	cin, nb := 48, 32
	cs := 36 * nb
	u := make([]float32, 36*8*cin*6)
	v := make([]float32, cin*cs)
	m := make([]float32, 6*cs)
	for i := 0; i < b.N; i++ {
		winoGemm(cin, &u[0], 4*8*cin*6, &v[0], 4*cs, &m[0], 4*cs, nb)
	}
	b.ReportMetric(float64(36*6*cin*nb)*float64(b.N)/b.Elapsed().Seconds()/1e9, "GMAC/s")
}

func BenchmarkWinoGemmStride(b *testing.B) {
	if winoGemm == nil {
		b.Skip()
	}
	for _, extra := range []int{0, 16, 48, 112} {
		b.Run(fmt.Sprint(extra), func(b *testing.B) {
			cin, nb := 48, 32
			cs := 36*nb + extra
			u := make([]float32, 36*8*cin*6)
			v := make([]float32, cin*cs)
			m := make([]float32, 6*cs)
			for i := 0; i < b.N; i++ {
				winoGemm(cin, &u[0], 4*8*cin*6, &v[0], 4*cs, &m[0], 4*cs, nb)
			}
			b.ReportMetric(float64(36*6*cin*nb)*float64(b.N)/b.Elapsed().Seconds()/1e9, "GMAC/s")
		})
	}
}
