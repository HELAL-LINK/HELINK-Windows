package main

// The voice network's inner loops (mdxnet.go): every layer is a matrix product cut in
// tiles of 6 rows by 16 columns. Two shapes:
//
//   - convTile: the 6 rows are packed weights (k values each, interleaved: panel[6p+i]),
//     the 16 columns are read where offs says (one row of 16 floats per p, at base plus
//     offs[p] bytes): a convolution reads its input in place, without copying it.
//   - rowsTile: the 6 rows are rows of a matrix anywhere (k floats each), the 16 columns
//     a packed panel (panel[16p+j]).
//
// Then each tile is finished as tileEp says, row by row: times scale plus bias, the ReLU,
// multiplied by (or added to) the 16 values at drow, written to orow. Plain Go here;
// AVX2 and FMA where the processor has them (mdxkernel_amd64.s).

import "unsafe"

const (
	epReLU = 1 << iota // max(0, x)
	epMul              // times the values at drow
	epAdd              // plus the values at drow
)

// tileEp is how a tile is finished (the assembly reads it by offsets: keep the layout).
type tileEp struct {
	scale [6]float32
	bias  [6]float32
	orow  [6]unsafe.Pointer // where each row's 16 results go
	drow  [6]unsafe.Pointer // the 16 values each row is multiplied by or added to
	flags uintptr
}

var (
	convTile = convTileGo
	rowsTile = rowsTileGo
	// mdxKernel names the loops in use (for the log).
	mdxKernel = "go"
)

func f32s(p unsafe.Pointer, n int) []float32 { return unsafe.Slice((*float32)(p), n) }

func finishTile(acc *[6][16]float32, ep *tileEp) {
	for i := 0; i < 6; i++ {
		s, b := ep.scale[i], ep.bias[i]
		var d []float32
		if ep.flags&(epMul|epAdd) != 0 {
			d = f32s(ep.drow[i], 16)
		}
		out := f32s(ep.orow[i], 16)
		for j := 0; j < 16; j++ {
			v := acc[i][j]*s + b
			if ep.flags&epReLU != 0 && v < 0 {
				v = 0
			}
			if ep.flags&epMul != 0 {
				v *= d[j]
			}
			if ep.flags&epAdd != 0 {
				v += d[j]
			}
			out[j] = v
		}
	}
}

func convTileGo(k int, panel, base *float32, offs *int, ep *tileEp) {
	var acc [6][16]float32
	w := unsafe.Slice(panel, 6*k)
	off := unsafe.Slice(offs, k)
	for p := 0; p < k; p++ {
		row := (*[16]float32)(unsafe.Add(unsafe.Pointer(base), off[p]))
		a := w[6*p : 6*p+6]
		for i := 0; i < 6; i++ {
			ai := a[i]
			for j := 0; j < 16; j++ {
				acc[i][j] += ai * row[j]
			}
		}
	}
	finishTile(&acc, ep)
}

func rowsTileGo(k int, rows *[6]unsafe.Pointer, panel *float32, ep *tileEp) {
	var acc [6][16]float32
	b := unsafe.Slice(panel, 16*k)
	var r [6][]float32
	for i := range r {
		r[i] = f32s(rows[i], k)
	}
	for p := 0; p < k; p++ {
		col := b[16*p : 16*p+16]
		for i := 0; i < 6; i++ {
			ai := r[i][p]
			for j := 0; j < 16; j++ {
				acc[i][j] += ai * col[j]
			}
		}
	}
	finishTile(&acc, ep)
}
