package main

// The voice network's 3x3 convolutions the Winograd way, F(4x4, 3x3) (Lavin & Gray): each
// 4x4 block of outputs ("tile") from its 6x6 inputs with 36 multiplications per input
// channel instead of 144. A row of tiles is done 32 (or 16) at a time:
//
//  1. every input channel's 6x6 values of each tile become V = Bt d B (36 numbers);
//  2. for each of the 36, a matrix product over the input channels: M = U V, with the
//     filters turned into U = G g Gt beforehand (convTile, mdxkernel.go);
//  3. each output channel's 4x4 values are At M A, plus the bias, ReLU.
//
// The two transforms work on 8 tiles at once (mdxwino_amd64.s, AVX2); without AVX2 the
// network uses the plain convolutions. In fp32 the results differ from those by about
// 2 parts in a million.

import "unsafe"

var (
	// The transforms and products (nil without AVX2: the 3x3 convolutions are then done
	// directly). See devtools/gen_wino.py.
	winoIn   func(src *float32, rs int, dst *float32, xs int, tmp, pf *float32)
	winoOut  func(src *float32, xs int, bias *float32, dst *float32, rs int, tmp *float32)
	winoGemm func(cin int, u *float32, uxs int, v *float32, vrs int, m *float32, mrs int, nb int)
)

var (
	winoBT = [6][6]float64{{4, 0, -5, 0, 1, 0}, {0, -4, -4, 1, 1, 0}, {0, 4, -4, -1, 1, 0},
		{0, -2, -1, 2, 1, 0}, {0, 2, -1, -2, 1, 0}, {0, 4, 0, -5, 0, 1}}
	winoG = [6][3]float64{{1.0 / 4, 0, 0}, {-1.0 / 6, -1.0 / 6, -1.0 / 6}, {-1.0 / 6, 1.0 / 6, -1.0 / 6},
		{1.0 / 24, 1.0 / 12, 1.0 / 6}, {1.0 / 24, -1.0 / 12, 1.0 / 6}, {0, 0, 1}}
	winoAT = [4][6]float64{{1, 1, 1, 1, 1, 0}, {0, 1, -1, 2, -2, 0}, {0, 1, 1, 4, 4, 0}, {0, 1, -1, 8, -8, 1}}
)

// mdxWino is a 3x3 convolution's filters as U, packed for convTile: [36][blocks][cin][6].
type mdxWino struct {
	cin, cout, blocks, nb int
	u                     []float32
	bias                  []float32
}

// newMdxWino turns w (as [cout][cin][3][3]) into U; nb tiles are done at a time.
func newMdxWino(cout, cin int, w, bias []float32, nb int) *mdxWino {
	l := &mdxWino{cin: cin, cout: cout, blocks: (cout + 5) / 6, nb: nb, bias: bias}
	l.u = make([]float32, 36*l.blocks*cin*6)
	for co := 0; co < cout; co++ {
		for ci := 0; ci < cin; ci++ {
			g := w[(co*cin+ci)*9:][:9]
			// U = G g Gt
			var gg [6][3]float64
			for i := 0; i < 6; i++ {
				for b := 0; b < 3; b++ {
					for a := 0; a < 3; a++ {
						gg[i][b] += winoG[i][a] * float64(g[a*3+b])
					}
				}
			}
			for i := 0; i < 6; i++ {
				for j := 0; j < 6; j++ {
					var s float64
					for b := 0; b < 3; b++ {
						s += gg[i][b] * winoG[j][b]
					}
					x := i*6 + j
					l.u[((x*l.blocks+co/6)*cin+ci)*6+co%6] = float32(s)
				}
			}
		}
	}
	return l
}

// winoBatch is how many tiles a row of f bins is done in at a time (0: not this way).
func winoBatch(f int) int {
	switch t := f / 4; {
	case f%4 != 0:
		return 0
	case t%32 == 0:
		return 32
	case t%16 == 0:
		return 16
	}
	return 0
}

// wconv runs the convolution (stride 1, padding 1, with ReLU) from in to out. V and M are
// kept per channel ([channel][36][tile]); out must be 32-byte aligned (mdxAlloc).
func (n *mdxNet) wconv(l *mdxWino, in, out *mdxAct) {
	in.zeroPads()
	nb := l.nb
	cs := 36 * nb // floats per channel in V and M
	perRow := in.f / 4 / nb
	n.par.run(in.t/4*perRow, func(task, wk int) {
		w := n.work[wk]
		ty, tx0 := task/perRow, task%perRow*nb
		v, m := w.wv[:l.cin*cs], w.wm[:l.cout*cs]
		for ci := 0; ci < l.cin; ci++ {
			ahead := min(ci+2, l.cin-1)
			for g := 0; g < nb; g += 8 {
				col := 4*(tx0+g) - 4
				winoIn(&in.d[in.at(ci, 4*ty-1, col)], 4*in.rs, &v[ci*cs+g], 4*nb, &w.wtmp[0], &in.d[in.at(ahead, 4*ty-1, col)])
			}
		}
		for b := 0; b < l.blocks; b++ {
			winoGemm(l.cin, &l.u[b*l.cin*6], 4*l.blocks*l.cin*6, &v[0], 4*cs, &m[6*b*cs], 4*cs, nb)
		}
		for co := 0; co < l.cout; co++ {
			for g := 0; g < nb; g += 8 {
				winoOut(&m[co*cs+g], 4*nb, &l.bias[co], &out.d[out.at(co, 4*ty, 4*(tx0+g))], 4*out.rs, &w.wtmp[0])
			}
		}
		winoFence()
	})
}

// winoInGo is winoIn in plain Go (for the tests).
func winoInGo(src *float32, rs int, dst *float32, xs int, tmp, pf *float32) {
	for m := 0; m < 8; m++ {
		var d [6][6]float64
		for r := 0; r < 6; r++ {
			row := unsafe.Slice((*float32)(unsafe.Add(unsafe.Pointer(src), r*rs)), 40)
			for c := 0; c < 6; c++ {
				d[r][c] = float64(row[3+4*m+c])
			}
		}
		for i := 0; i < 6; i++ {
			for j := 0; j < 6; j++ {
				var s float64
				for r := 0; r < 6; r++ {
					for c := 0; c < 6; c++ {
						s += winoBT[i][r] * d[r][c] * winoBT[j][c]
					}
				}
				*(*float32)(unsafe.Add(unsafe.Pointer(dst), (i*6+j)*xs+4*m)) = float32(s)
			}
		}
	}
}

// winoOutGo is winoOut in plain Go (for the tests).
func winoOutGo(src *float32, xs int, bias *float32, dst *float32, rs int, tmp *float32) {
	for m := 0; m < 8; m++ {
		var mm [6][6]float64
		for x := 0; x < 36; x++ {
			mm[x/6][x%6] = float64(*(*float32)(unsafe.Add(unsafe.Pointer(src), x*xs+4*m)))
		}
		for a := 0; a < 4; a++ {
			row := unsafe.Slice((*float32)(unsafe.Add(unsafe.Pointer(dst), a*rs)), 32)
			for b := 0; b < 4; b++ {
				var s float64
				for i := 0; i < 6; i++ {
					for j := 0; j < 6; j++ {
						s += winoAT[a][i] * mm[i][j] * winoAT[b][j]
					}
				}
				s += float64(*bias)
				if s < 0 {
					s = 0
				}
				row[4*m+b] = float32(s)
			}
		}
	}
}
