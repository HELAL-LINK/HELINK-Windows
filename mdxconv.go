package main

// Turns the voice network as UVR publishes it (an ONNX file, fp32) into HELINK's weight
// file: the graph is walked and checked against the network mdxnet.go runs, the batch
// norms after the transposed convolutions are folded into them, those of the TDFs kept as
// a scale and a shift per channel, and every value is stored as fp16 (half the size, and
// the same voices: the difference is 66 dB below them).

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
)

type onnxAttr struct {
	f    float32
	i    int64
	ints []int64
}

type onnxNode struct {
	op      string
	inputs  []string
	outputs []string
	attrs   map[string]onnxAttr
}

type onnxInit struct {
	dims []int
	data []float32
}

// readOnnx reads the nodes (in order) and the fp32 initializers of an ONNX model.
func readOnnx(data []byte) ([]onnxNode, map[string]onnxInit, error) {
	top, err := pbFields(data)
	if err != nil {
		return nil, nil, err
	}
	var graph []byte
	for _, f := range top {
		if f.num == 7 && f.wire == 2 {
			graph = f.b
		}
	}
	if graph == nil {
		return nil, nil, errors.New("no graph in the model")
	}
	gf, err := pbFields(graph)
	if err != nil {
		return nil, nil, err
	}
	var nodes []onnxNode
	inits := map[string]onnxInit{}
	ints := func(f pbField) ([]int64, error) {
		if f.wire == 0 {
			return []int64{int64(f.v)}, nil
		}
		var out []int64
		for j := 0; j < len(f.b); {
			d, k, err := pbVarint(f.b, j)
			if err != nil {
				return nil, err
			}
			out, j = append(out, int64(d)), k
		}
		return out, nil
	}
	for _, f := range gf {
		switch {
		case f.num == 5 && f.wire == 2: // TensorProto
			tf, err := pbFields(f.b)
			if err != nil {
				return nil, nil, err
			}
			var t onnxInit
			name, dtype := "", uint64(1)
			var raw []byte
			var floats []float32
			for _, g := range tf {
				switch g.num {
				case 1:
					d, err := ints(g)
					if err != nil {
						return nil, nil, err
					}
					for _, x := range d {
						t.dims = append(t.dims, int(x))
					}
				case 2:
					dtype = g.v
				case 4:
					if g.wire == 2 {
						for j := 0; j+4 <= len(g.b); j += 4 {
							floats = append(floats, math.Float32frombits(binary.LittleEndian.Uint32(g.b[j:])))
						}
					} else if g.wire == 5 {
						floats = append(floats, math.Float32frombits(binary.LittleEndian.Uint32(g.b)))
					}
				case 8:
					name = string(g.b)
				case 9:
					raw = g.b
				}
			}
			if dtype != 1 {
				continue // not fp32: not a weight
			}
			count := 1
			for _, d := range t.dims {
				count *= d
			}
			switch {
			case raw != nil && len(raw) == 4*count:
				t.data = make([]float32, count)
				for j := range t.data {
					t.data[j] = math.Float32frombits(binary.LittleEndian.Uint32(raw[4*j:]))
				}
			case len(floats) == count:
				t.data = floats
			default:
				return nil, nil, fmt.Errorf("tensor %s: %d values expected", name, count)
			}
			inits[name] = t
		case f.num == 1 && f.wire == 2: // NodeProto
			nf, err := pbFields(f.b)
			if err != nil {
				return nil, nil, err
			}
			n := onnxNode{attrs: map[string]onnxAttr{}}
			for _, g := range nf {
				switch g.num {
				case 1:
					n.inputs = append(n.inputs, string(g.b))
				case 2:
					n.outputs = append(n.outputs, string(g.b))
				case 4:
					n.op = string(g.b)
				case 5:
					af, err := pbFields(g.b)
					if err != nil {
						return nil, nil, err
					}
					var a onnxAttr
					an := ""
					for _, h := range af {
						switch h.num {
						case 1:
							an = string(h.b)
						case 2:
							if h.wire == 5 {
								a.f = math.Float32frombits(binary.LittleEndian.Uint32(h.b))
							}
						case 3:
							a.i = int64(h.v)
						case 8:
							v, err := ints(h)
							if err != nil {
								return nil, nil, err
							}
							a.ints = append(a.ints, v...)
						}
					}
					n.attrs[an] = a
				}
			}
			nodes = append(nodes, n)
		}
	}
	return nodes, inits, nil
}

// mdxGraph walks the nodes, checking each is what the network has there.
type mdxGraph struct {
	nodes []onnxNode
	inits map[string]onnxInit
	at    int
	out   map[string][]float32 // HELINK's tensors
	dims  map[string][]int
}

func (g *mdxGraph) next(op string) (*onnxNode, error) {
	if g.at >= len(g.nodes) {
		return nil, fmt.Errorf("the model ends where %s was expected", op)
	}
	n := &g.nodes[g.at]
	if n.op != op {
		return nil, fmt.Errorf("node %d is %s, not %s", g.at, n.op, op)
	}
	g.at++
	return n, nil
}

func (g *mdxGraph) weight(n *onnxNode, i int, dims ...int) (onnxInit, error) {
	if i >= len(n.inputs) {
		return onnxInit{}, fmt.Errorf("%s has no input %d", n.op, i)
	}
	t, ok := g.inits[n.inputs[i]]
	if !ok {
		return onnxInit{}, fmt.Errorf("%s input %s is not a weight", n.op, n.inputs[i])
	}
	if len(dims) > 0 {
		ok := len(t.dims) == len(dims)
		for j := 0; ok && j < len(dims); j++ {
			ok = dims[j] < 0 || t.dims[j] == dims[j]
		}
		if !ok {
			return onnxInit{}, fmt.Errorf("%s weight %s has shape %v, want %v", n.op, n.inputs[i], t.dims, dims)
		}
	}
	return t, nil
}

func (g *mdxGraph) put(name string, dims []int, data []float32) {
	g.out[name] = data
	g.dims[name] = dims
}

// conv takes a Conv node (kernel k, stride s, padding p) and its ReLU (if relu).
func (g *mdxGraph) conv(name string, cout, cin, k, s, p int, relu bool) error {
	n, err := g.next("Conv")
	if err != nil {
		return err
	}
	w, err := g.weight(n, 1, cout, cin, k, k)
	if err != nil {
		return err
	}
	b, err := g.weight(n, 2, cout)
	if err != nil {
		return err
	}
	if st := n.attrs["strides"].ints; len(st) == 2 && (st[0] != int64(s) || st[1] != int64(s)) {
		return fmt.Errorf("%s: strides %v", name, st)
	}
	pads := n.attrs["pads"].ints
	for _, x := range pads {
		if x != int64(p) {
			return fmt.Errorf("%s: pads %v", name, pads)
		}
	}
	if gr := n.attrs["group"].i; gr > 1 {
		return fmt.Errorf("%s: groups", name)
	}
	g.put(name+".w", w.dims, w.data)
	g.put(name+".b", b.dims, b.data)
	if relu {
		if _, err := g.next("Relu"); err != nil {
			return err
		}
	}
	return nil
}

// bn takes a BatchNormalization node as a scale and a shift per channel.
func (g *mdxGraph) bn(c int) (scale, shift []float32, err error) {
	n, err := g.next("BatchNormalization")
	if err != nil {
		return nil, nil, err
	}
	var t [4]onnxInit
	for i := range t {
		if t[i], err = g.weight(n, i+1, c); err != nil {
			return nil, nil, err
		}
	}
	eps := float64(1e-5)
	if a, ok := n.attrs["epsilon"]; ok && a.f > 0 {
		eps = float64(a.f)
	}
	scale, shift = make([]float32, c), make([]float32, c)
	for i := 0; i < c; i++ {
		s := float64(t[0].data[i]) / math.Sqrt(float64(t[3].data[i])+eps)
		scale[i] = float32(s)
		shift[i] = float32(float64(t[1].data[i]) - float64(t[2].data[i])*s)
	}
	return scale, shift, nil
}

// block takes a TFC-TDF block of c channels on f bins; it returns the name of its result.
func (g *mdxGraph) block(name string, c, f int) (string, error) {
	for i := 0; i < 3; i++ {
		if err := g.conv(fmt.Sprintf("%s.c%d", name, i), c, c, 3, 1, 1, true); err != nil {
			return "", err
		}
	}
	m1, err := g.next("MatMul")
	if err != nil {
		return "", err
	}
	w1, err := g.weight(m1, 1, f, -1)
	if err != nil {
		return "", err
	}
	fb := w1.dims[1]
	s1, b1, err := g.bn(c)
	if err != nil {
		return "", err
	}
	if _, err := g.next("Relu"); err != nil {
		return "", err
	}
	m2, err := g.next("MatMul")
	if err != nil {
		return "", err
	}
	w2, err := g.weight(m2, 1, fb, f)
	if err != nil {
		return "", err
	}
	s2, b2, err := g.bn(c)
	if err != nil {
		return "", err
	}
	if _, err := g.next("Relu"); err != nil {
		return "", err
	}
	add, err := g.next("Add")
	if err != nil {
		return "", err
	}
	if len(add.inputs) != 2 || (add.inputs[0] != m1.inputs[0] && add.inputs[1] != m1.inputs[0]) {
		return "", fmt.Errorf("%s: the TDF is not added to its input", name)
	}
	g.put(name+".tdf.w1", w1.dims, w1.data)
	g.put(name+".tdf.w2", w2.dims, w2.data)
	g.put(name+".tdf.s1", []int{c}, s1)
	g.put(name+".tdf.b1", []int{c}, b1)
	g.put(name+".tdf.s2", []int{c}, s2)
	g.put(name+".tdf.b2", []int{c}, b2)
	return add.outputs[0], nil
}

// mdxFromOnnx reads UVR's ONNX model into HELINK's tensors (fp32, by name).
func mdxFromOnnx(data []byte) (map[string][]float32, map[string][]int, error) {
	nodes, inits, err := readOnnx(data)
	if err != nil {
		return nil, nil, err
	}
	g := &mdxGraph{nodes: nodes, inits: inits, out: map[string][]float32{}, dims: map[string][]int{}}
	levels := 0
	for _, n := range nodes {
		if n.op == "ConvTranspose" {
			levels++
		}
	}
	if levels < 1 || len(nodes) == 0 || nodes[0].op != "Conv" {
		return nil, nil, errors.New("not an MDX-Net model")
	}
	first, err := g.weight(&nodes[0], 1, -1, 4, 1, 1)
	if err != nil {
		return nil, nil, err
	}
	ch := first.dims[0]
	if err := g.conv("first", ch, 4, 1, 1, 0, true); err != nil {
		return nil, nil, err
	}
	if _, err := g.next("Transpose"); err != nil {
		return nil, nil, err
	}
	// The bins: the first TDF's input size.
	f := 0
	for _, n := range nodes {
		if n.op == "MatMul" {
			if w, ok := inits[n.inputs[1]]; ok && len(w.dims) == 2 {
				f = w.dims[0]
			}
			break
		}
	}
	if f == 0 || f%(16<<levels) != 0 {
		return nil, nil, fmt.Errorf("unexpected number of bins %d", f)
	}
	skips := make([]string, levels)
	for l := 0; l < levels; l++ {
		c := ch * (l + 1)
		if skips[l], err = g.block(fmt.Sprintf("enc%d", l), c, f>>l); err != nil {
			return nil, nil, err
		}
		if err := g.conv(fmt.Sprintf("down%d", l), c+ch, c, 2, 2, 0, true); err != nil {
			return nil, nil, err
		}
	}
	if _, err := g.block("mid", ch*(levels+1), f>>levels); err != nil {
		return nil, nil, err
	}
	for l := levels - 1; l >= 0; l-- {
		c := ch * (l + 1)
		n, err := g.next("ConvTranspose")
		if err != nil {
			return nil, nil, err
		}
		w, err := g.weight(n, 1, c+ch, c, 2, 2)
		if err != nil {
			return nil, nil, err
		}
		b, err := g.weight(n, 2, c)
		if err != nil {
			return nil, nil, err
		}
		if st := n.attrs["strides"].ints; len(st) != 2 || st[0] != 2 || st[1] != 2 {
			return nil, nil, fmt.Errorf("up%d: strides %v", l, st)
		}
		s, sh, err := g.bn(c)
		if err != nil {
			return nil, nil, err
		}
		if _, err := g.next("Relu"); err != nil {
			return nil, nil, err
		}
		mul, err := g.next("Mul")
		if err != nil {
			return nil, nil, err
		}
		if len(mul.inputs) != 2 || mul.inputs[1] != skips[l] {
			return nil, nil, fmt.Errorf("up%d is not multiplied by the level's block", l)
		}
		wf := make([]float32, len(w.data))
		for ci := 0; ci < c+ch; ci++ {
			for co := 0; co < c; co++ {
				for k := 0; k < 4; k++ {
					i := (ci*c+co)*4 + k
					wf[i] = w.data[i] * s[co]
				}
			}
		}
		bf := make([]float32, c)
		for co := range bf {
			bf[co] = b.data[co]*s[co] + sh[co]
		}
		g.put(fmt.Sprintf("up%d.w", l), w.dims, wf)
		g.put(fmt.Sprintf("up%d.b", l), []int{c}, bf)
		if _, err := g.block(fmt.Sprintf("dec%d", l), c, f>>l); err != nil {
			return nil, nil, err
		}
	}
	if _, err := g.next("Transpose"); err != nil {
		return nil, nil, err
	}
	if err := g.conv("last", 4, ch, 1, 1, 0, false); err != nil {
		return nil, nil, err
	}
	if g.at != len(nodes) {
		return nil, nil, fmt.Errorf("%d nodes left after the network", len(nodes)-g.at)
	}
	return g.out, g.dims, nil
}

// convertMdxOnnx writes the weight file for the ONNX model at src; it returns the file's
// SHA-256.
func convertMdxOnnx(src, dst string) (string, error) {
	data, err := os.ReadFile(src)
	if err != nil {
		return "", err
	}
	vals, dims, err := mdxFromOnnx(data)
	if err != nil {
		return "", err
	}
	blobs := map[string]sepBlob{}
	for name, v := range vals {
		raw, err := toFP16(v)
		if err != nil {
			return "", fmt.Errorf("%s: %w", name, err)
		}
		blobs[name] = sepBlob{dims: dims[name], raw: raw}
	}
	return writeSepModel(blobs, dst)
}
