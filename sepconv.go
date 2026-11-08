package main

// HELINK's weight file (sepmodel.go): fp16 tensors by name, written by mdxconv.go from the
// downloaded model; and the protobuf reading that the ONNX model needs.

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
)

type sepBlob struct {
	dims []int
	raw  []byte // little-endian fp16
}

// ---------------------------------------------------------------------------
// protobuf (ONNX, and TensorFlow's checkpoint entries)
// ---------------------------------------------------------------------------

func pbVarint(b []byte, i int) (uint64, int, error) {
	var r uint64
	for s := uint(0); s < 64; s += 7 {
		if i >= len(b) {
			return 0, i, errors.New("protobuf: truncated varint")
		}
		c := b[i]
		i++
		r |= uint64(c&0x7f) << s
		if c < 0x80 {
			return r, i, nil
		}
	}
	return 0, i, errors.New("protobuf: varint too long")
}

type pbField struct {
	num  int
	wire int
	v    uint64 // varint values
	b    []byte // length-delimited and fixed values
}

// pbFields reads the fields of one protobuf message.
func pbFields(b []byte) ([]pbField, error) {
	var out []pbField
	for i := 0; i < len(b); {
		tag, j, err := pbVarint(b, i)
		if err != nil {
			return nil, err
		}
		i = j
		f := pbField{num: int(tag >> 3), wire: int(tag & 7)}
		switch f.wire {
		case 0:
			if f.v, i, err = pbVarint(b, i); err != nil {
				return nil, err
			}
		case 1, 5:
			n := 8
			if f.wire == 5 {
				n = 4
			}
			if i+n > len(b) {
				return nil, errors.New("protobuf: truncated")
			}
			f.b, i = b[i:i+n], i+n
		case 2:
			var ln uint64
			if ln, i, err = pbVarint(b, i); err != nil {
				return nil, err
			}
			if uint64(len(b)-i) < ln {
				return nil, errors.New("protobuf: truncated")
			}
			f.b, i = b[i:i+int(ln)], i+int(ln)
		default:
			return nil, fmt.Errorf("protobuf wire type %d", f.wire)
		}
		out = append(out, f)
	}
	return out, nil
}

// floatToHalf rounds like Python's struct.pack("<e") (PyFloat_Pack2): to nearest, ties to
// even; numbers too large for fp16 are an error.
func floatToHalf(x float64) (uint16, error) {
	var sign uint16
	var e int
	var bits uint16
	switch {
	case x == 0:
		if math.Signbit(x) {
			sign = 1
		}
	case math.IsInf(x, 0):
		if x < 0 {
			sign = 1
		}
		e = 0x1f
	case math.IsNaN(x):
		return 0x7e00, nil
	default:
		if x < 0 {
			sign = 1
			x = -x
		}
		f, ex := math.Frexp(x)
		f *= 2
		e = ex - 1
		switch {
		case e >= 16:
			return 0, errors.New("value too large for fp16")
		case e < -25:
			f, e = 0, 0
		case e < -14:
			f, e = math.Ldexp(f, 14+e), 0
		default:
			e += 15
			f--
		}
		f *= 1024
		bits = uint16(f)
		if d := f - float64(bits); d > 0.5 || (d == 0.5 && bits%2 == 1) {
			bits++
			if bits == 1024 {
				bits = 0
				e++
				if e == 31 {
					return 0, errors.New("value too large for fp16")
				}
			}
		}
	}
	return bits | uint16(e)<<10 | sign<<15, nil
}

func toFP16(values []float32) ([]byte, error) {
	out := make([]byte, 2*len(values))
	for i, v := range values {
		h, err := floatToHalf(float64(v))
		if err != nil {
			return nil, err
		}
		binary.LittleEndian.PutUint16(out[2*i:], h)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// The weight file
// ---------------------------------------------------------------------------

// writeSepModel writes the tensors (by name) and returns the file's SHA-256 (hex).
func writeSepModel(tensors map[string]sepBlob, dst string) (string, error) {
	names := make([]string, 0, len(tensors))
	for n := range tensors {
		names = append(names, n)
	}
	sort.Strings(names)
	type entry struct {
		name  string
		dims  []int
		count int
		raw   []byte
	}
	entries := make([]entry, 0, len(names))
	size := len(sepMagic) + 4
	for _, n := range names {
		t := tensors[n]
		count := 1
		for _, d := range t.dims {
			count *= d
		}
		if count*2 != len(t.raw) {
			return "", fmt.Errorf("%s: %d values but %d bytes", n, count, len(t.raw))
		}
		entries = append(entries, entry{n, t.dims, count, t.raw})
		size += 2 + len(n) + 1 + 4*len(t.dims) + 8
	}
	align := func(n int) int { return (n + 63) / 64 * 64 }
	off := align(size)
	offsets := make([]int, len(entries))
	for i, e := range entries {
		offsets[i] = off
		off = align(off + e.count*2)
	}
	var head bytes.Buffer
	head.Write(sepMagic)
	le := binary.LittleEndian
	_ = binary.Write(&head, le, uint32(len(entries)))
	for i, e := range entries {
		_ = binary.Write(&head, le, uint16(len(e.name)))
		head.WriteString(e.name)
		head.WriteByte(byte(len(e.dims)))
		for _, d := range e.dims {
			_ = binary.Write(&head, le, uint32(d))
		}
		_ = binary.Write(&head, le, uint32(offsets[i]))
		_ = binary.Write(&head, le, uint32(e.count))
	}
	tmp := dst + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	w := bufio.NewWriterSize(io.MultiWriter(f, h), 1<<20)
	pos := 0
	put := func(blob []byte, at int) {
		_, _ = w.Write(make([]byte, at-pos))
		_, _ = w.Write(blob)
		pos = at + len(blob)
	}
	put(head.Bytes(), 0)
	for i, e := range entries {
		put(e.raw, offsets[i])
	}
	_, _ = w.Write(make([]byte, align(pos)-pos))
	err = w.Flush()
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
