package main

import (
	"unsafe"

	"golang.org/x/sys/cpu"
)

//go:noescape
func convTileAVX2(k int, panel, base *float32, offs *int, ep *tileEp)

//go:noescape
func rowsTileAVX2(k int, rows *[6]unsafe.Pointer, panel *float32, ep *tileEp)

func init() {
	if cpu.X86.HasAVX2 && cpu.X86.HasFMA {
		convTile, rowsTile, mdxKernel = convTileAVX2, rowsTileAVX2, "avx2"
	}
}

//go:noescape
func winoInAVX2(src *float32, rs int, dst *float32, xs int, tmp, pf *float32)

//go:noescape
func winoOutAVX2(src *float32, xs int, bias *float32, dst *float32, rs int, tmp *float32)

func winoFence()

//go:noescape
func winoGemmAVX2(cin int, u *float32, uxs int, v *float32, vrs int, m *float32, mrs int, nb int)

func init() {
	if cpu.X86.HasAVX2 && cpu.X86.HasFMA {
		winoIn, winoOut, winoGemm = winoInAVX2, winoOutAVX2, winoGemmAVX2
	}
}
