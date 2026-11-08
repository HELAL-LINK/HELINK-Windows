package main

import (
	"math"
	"math/cmplx"
	"math/rand"
	"testing"
)

func TestMixedFFT(t *testing.T) {
	rnd := rand.New(rand.NewSource(3))
	for _, n := range []int{1, 2, 3, 4, 5, 6, 8, 12, 60, 96, 7680, 6144} {
		f := newCFFT(n)
		x := make([]complex128, n)
		for i := range x {
			x[i] = complex(rnd.NormFloat64(), rnd.NormFloat64())
		}
		y := append([]complex128(nil), x...)
		tmp := make([]complex128, n)
		f.forward(y, tmp)
		worst := 0.0
		for k := 0; k < n; k += max(1, n/97) {
			var s complex128
			for j := 0; j < n; j++ {
				s += x[j] * cmplx.Exp(complex(0, -2*math.Pi*float64(j*k%n)/float64(n)))
			}
			worst = math.Max(worst, cmplx.Abs(s-y[k])/math.Sqrt(float64(n)))
		}
		if worst > 1e-9 {
			t.Errorf("n=%d: off by %g", n, worst)
		}
		f.inverse(y, tmp)
		for i := range x {
			if cmplx.Abs(x[i]-y[i]) > 1e-9 {
				t.Fatalf("n=%d: inverse off at %d", n, i)
			}
		}
	}
}

func BenchmarkMixedFFT7680(b *testing.B) {
	f := newCFFT(7680)
	x := make([]complex128, 7680)
	tmp := make([]complex128, 7680)
	for i := 0; i < b.N; i++ {
		f.forward(x, tmp)
	}
}
