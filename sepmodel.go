package main

// The music remover's weights in HELINK's weight file (written by sepconv.go): fp16
// tensors by name, in ONNX layout.
//
//	"HLKSEP\0\1"  u32 count
//	count x { u16 name length, name, u8 dims, u32 dim..., u32 data offset, u32 values }
//	data, each tensor 64-byte aligned

import (
	"encoding/binary"
	"errors"
	"math"
	"os"
)

var sepMagic = []byte("HLKSEP\x00\x01")

type sepTensor struct {
	name  string
	dims  []int
	off   int // byte offset of the fp16 values
	count int
}

type sepModel struct {
	data    []byte
	tensors map[string]*sepTensor
}

var errSepDamaged = errors.New("damaged weight file")

// openSepFile reads a weight file without checking which network it holds.
func openSepFile(path string) (*sepModel, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseSepFile(b)
}

func parseSepFile(b []byte) (m *sepModel, err error) {
	defer func() {
		if recover() != nil {
			m, err = nil, errSepDamaged
		}
	}()
	if len(b) < 12 || string(b[:8]) != string(sepMagic) {
		return nil, errors.New("not a HELINK weight file")
	}
	le := binary.LittleEndian
	n := int(le.Uint32(b[8:]))
	pos := 12
	m = &sepModel{data: b, tensors: map[string]*sepTensor{}}
	for i := 0; i < n; i++ {
		ln := int(le.Uint16(b[pos:]))
		pos += 2
		name := string(b[pos : pos+ln])
		pos += ln
		nd := int(b[pos])
		pos++
		dims := make([]int, nd)
		count := 1
		for j := range dims {
			dims[j] = int(le.Uint32(b[pos:]))
			pos += 4
			count *= dims[j]
		}
		off, cnt := int(le.Uint32(b[pos:])), int(le.Uint32(b[pos+4:]))
		pos += 8
		if cnt != count || off < 0 || off&1 != 0 || off+2*cnt > len(b) {
			return nil, errSepDamaged
		}
		m.tensors[name] = &sepTensor{name: name, dims: dims, off: off, count: cnt}
	}
	return m, nil
}

func (m *sepModel) tensor(name string) *sepTensor { return m.tensors[name] }

// read turns a tensor's fp16 values into float32s, into dst.
func (m *sepModel) read(t *sepTensor, dst []float32) {
	src := m.data[t.off : t.off+2*t.count]
	dst = dst[:t.count]
	for i := range dst {
		dst[i] = halfTable[uint16(src[2*i])|uint16(src[2*i+1])<<8]
	}
}

// ---------------------------------------------------------------------------
// fp16
// ---------------------------------------------------------------------------

var halfTable = func() *[65536]float32 {
	var t [65536]float32
	for h := 0; h < 65536; h++ {
		t[h] = halfToFloat(uint16(h))
	}
	return &t
}()

func halfToFloat(h uint16) float32 {
	s, e, m := uint32(h>>15)&1, uint32(h>>10)&0x1f, uint32(h)&0x3ff
	var v float32
	switch {
	case e == 0:
		v = float32(m) * (1.0 / (1 << 24)) // zero or subnormal: m x 2^-24
	case e == 31:
		if m == 0 {
			v = float32(math.Inf(1))
		} else {
			v = float32(math.NaN())
		}
	default:
		return math.Float32frombits(s<<31 | (e-15+127)<<23 | m<<13)
	}
	if s == 1 {
		return -v
	}
	return v
}
