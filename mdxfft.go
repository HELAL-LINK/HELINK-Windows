package main

// The Fourier transform of the voice network's spectrum: 7680 points (2^9·3·5), which
// the power-of-two transform of musicremover.go cannot do. A mixed-radix transform
// (radices 4, 2, 3, 5), in place after a digit-reversed copy.

import (
	"math"
	"math/cmplx"
)

type cfft struct {
	n     int
	radix []int // from the first split (outermost) to the last
	perm  []int32
	tw    []complex128 // exp(-2πi j/n)
	w     map[int][]complex128
}

func newCFFT(n int) *cfft {
	f := &cfft{n: n, w: map[int][]complex128{}}
	for m := n; m > 1; {
		r := 0
		switch {
		case m%4 == 0:
			r = 4
		case m%2 == 0:
			r = 2
		case m%3 == 0:
			r = 3
		case m%5 == 0:
			r = 5
		default:
			panic("fft size must be 2^a·3^b·5^c")
		}
		f.radix = append(f.radix, r)
		m /= r
	}
	f.tw = make([]complex128, n)
	for j := range f.tw {
		s, c := math.Sincos(-2 * math.Pi * float64(j) / float64(n))
		f.tw[j] = complex(c, s)
	}
	for _, r := range f.radix {
		if _, ok := f.w[r]; !ok {
			w := make([]complex128, r)
			for j := range w {
				s, c := math.Sincos(-2 * math.Pi * float64(j) / float64(r))
				w[j] = complex(c, s)
			}
			f.w[r] = w
		}
	}
	idx := make([]int32, n)
	for i := range idx {
		idx[i] = int32(i)
	}
	f.perm = make([]int32, 0, n)
	var order func(seq []int32, stride, radixAt int)
	order = func(seq []int32, stride, radixAt int) {
		if radixAt == len(f.radix) {
			f.perm = append(f.perm, seq[0])
			return
		}
		r := f.radix[radixAt]
		for q := 0; q < r; q++ {
			order(seq[q*stride:], stride*r, radixAt+1)
		}
	}
	order(idx, 1, 0)
	return f
}

var (
	sin60          = math.Sqrt(3) / 2
	cos72, sin72   = math.Cos(2 * math.Pi / 5), math.Sin(2 * math.Pi / 5)
	cos144, sin144 = math.Cos(4 * math.Pi / 5), math.Sin(4 * math.Pi / 5)
)

func scale(z complex128, s float64) complex128 { return complex(real(z)*s, imag(z)*s) }

// forward transforms x in place (tmp: scratch of n values).
func (f *cfft) forward(x, tmp []complex128) {
	n := f.n
	for i, p := range f.perm {
		tmp[i] = x[p]
	}
	copy(x, tmp[:n])
	var v [5]complex128
	m := 1
	for s := len(f.radix) - 1; s >= 0; s-- {
		r := f.radix[s]
		size := r * m
		step := n / size
		w := f.w[r]
		for base := 0; base < n; base += size {
			for k := 0; k < m; k++ {
				i0 := base + k
				switch r {
				case 2:
					a, b := x[i0], x[i0+m]*f.tw[k*step]
					x[i0], x[i0+m] = a+b, a-b
				case 4:
					a := x[i0]
					b := x[i0+m] * f.tw[k*step]
					c := x[i0+2*m] * f.tw[2*k*step]
					d := x[i0+3*m] * f.tw[3*k*step]
					t0, t1 := a+c, a-c
					t2, t3 := b+d, b-d
					t3 = complex(imag(t3), -real(t3)) // times -i
					x[i0], x[i0+m], x[i0+2*m], x[i0+3*m] = t0+t2, t1+t3, t0-t2, t1-t3
				case 3:
					a := x[i0]
					b := x[i0+m] * f.tw[k*step]
					c := x[i0+2*m] * f.tw[2*k*step]
					t1 := b + c
					t2 := a - scale(t1, 0.5)
					u := scale(b-c, sin60)
					d := complex(imag(u), -real(u)) // times -i
					x[i0], x[i0+m], x[i0+2*m] = a+t1, t2+d, t2-d
				case 5:
					a := x[i0]
					b := x[i0+m] * f.tw[k*step]
					c := x[i0+2*m] * f.tw[2*k*step]
					d := x[i0+3*m] * f.tw[3*k*step]
					e := x[i0+4*m] * f.tw[4*k*step]
					s1, s2 := b+e, c+d
					d1, d2 := b-e, c-d
					r1 := a + scale(s1, cos72) + scale(s2, cos144)
					r2 := a + scale(s1, cos144) + scale(s2, cos72)
					// -i (sin72 d1 + sin144 d2), -i (sin144 d1 - sin72 d2)
					q1 := scale(d1, sin72) + scale(d2, sin144)
					q2 := scale(d1, sin144) - scale(d2, sin72)
					j1 := complex(imag(q1), -real(q1))
					j2 := complex(imag(q2), -real(q2))
					x[i0], x[i0+m], x[i0+2*m], x[i0+3*m], x[i0+4*m] = a+s1+s2, r1+j1, r2+j2, r2-j2, r1-j1
				default:
					for q := 0; q < r; q++ {
						v[q] = x[i0+q*m]
						if q > 0 {
							v[q] *= f.tw[(q*k*step)%n]
						}
					}
					for p := 0; p < r; p++ {
						sum := v[0]
						for q := 1; q < r; q++ {
							sum += v[q] * w[(q*p)%r]
						}
						x[i0+p*m] = sum
					}
				}
			}
		}
		m = size
	}
}

// inverse is the inverse transform (with the 1/n), in place.
func (f *cfft) inverse(x, tmp []complex128) {
	for i := range x[:f.n] {
		x[i] = cmplx.Conj(x[i])
	}
	f.forward(x, tmp)
	s := 1 / float64(f.n)
	for i := range x[:f.n] {
		x[i] = complex(real(x[i])*s, -imag(x[i])*s)
	}
}
