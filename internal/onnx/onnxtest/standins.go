package onnxtest

import (
	"encoding/binary"
	"fmt"
	"math"
	"testing"
)

// Stand-ins for the open models that look at what's on screen (D413): each
// has the real model's input and output names and shapes, and answers from
// how bright the picture is, so a test draws white shapes on black and knows
// what the model "sees" — a face where a square is, a line of text where a bar
// is, the subject where a disc is. Nothing is downloaded; they are a few
// hundred bytes, written at test time.

// attrInt is an INT attribute.
func attrInt(name string, v int64) []byte {
	return buf(nil).strField(1, name).intField(3, v).intField(20, 2 /* INT */)
}

// attrInts is an INTS attribute.
func attrInts(name string, vs []int64) []byte {
	b := buf(nil).strField(1, name)
	for _, v := range vs {
		b = b.intField(8, v)
	}
	return b.intField(20, 7 /* INTS */)
}

// floatTensor is a constant (an initializer) of float values.
func floatTensor(name string, dims []int64, vals []float32) []byte {
	b := buf(nil)
	for _, d := range dims {
		b = b.intField(1, d)
	}
	b = b.intField(2, 1 /* FLOAT */)
	packed := make([]byte, 4*len(vals))
	for i, v := range vals {
		binary.LittleEndian.PutUint32(packed[4*i:], math.Float32bits(v))
	}
	return b.bytesField(4, packed).strField(8, name)
}

// valueInfoOwn is valueInfo with its free dimensions named after the value
// (outputs whose sizes differ must not share a dimension's name).
func valueInfoOwn(name string, shape []int64) []byte {
	var sh buf
	for i, d := range shape {
		var dim buf
		if d < 0 {
			dim = dim.strField(2, fmt.Sprintf("%s_%d", name, i))
		} else {
			dim = dim.intField(1, d)
		}
		sh = sh.bytesField(1, dim)
	}
	tensor := buf(nil).intField(1, 1 /* FLOAT */).bytesField(2, sh)
	typ := buf(nil).bytesField(1, tensor)
	return buf(nil).strField(1, name).bytesField(2, typ)
}

// modelOf is a model with one input and any number of outputs (opset 13).
func modelOf(nodes []node, inits [][]byte, in []byte, outs ...[]byte) []byte {
	var g buf
	for i, n := range nodes {
		var nb buf
		for _, s := range n.in {
			nb = nb.strField(1, s)
		}
		for _, s := range n.out {
			nb = nb.strField(2, s)
		}
		nb = nb.strField(3, fmt.Sprintf("n%d", i)).strField(4, n.op)
		for _, a := range n.attrs {
			nb = nb.bytesField(5, a)
		}
		g = g.bytesField(1, nb)
	}
	g = g.strField(2, "standin")
	for _, t := range inits {
		g = g.bytesField(5, t)
	}
	g = g.bytesField(11, in)
	for _, o := range outs {
		g = g.bytesField(12, o)
	}
	opset := buf(nil).strField(1, "").intField(2, 13)
	return buf(nil).intField(1, 7).strField(2, "metarush-test").bytesField(7, g).bytesField(8, opset)
}

// brightness adds nodes that turn input (1×3×H×W) into its mean over the
// channels (1×1×H×W), named out.
func brightness(input, out string) node {
	return node{op: "ReduceMean", in: []string{input}, out: []string{out}, attrs: [][]byte{attrInts("axes", []int64{1}), attrInt("keepdims", 1)}}
}

// FaceFinder writes a stand-in for YuNet (input "input", 1×3×H×W, blue,
// green and red at 0–255; outputs cls_8 … kps_32): at stride 8 a cell's class
// and object scores are how bright the cell is (0–1; nothing at strides 16
// and 32), its box is twelve strides wide and tall around the cell's middle,
// and its five points sit where a face's would in that box (the eyes above,
// the nose, the mouth corners below), so a white square of up to 32 pixels
// is one face (in a picture under 128 pixels, which isn't looked at again at
// half size).
func FaceFinder(t testing.TB, dir string) string {
	t.Helper()
	var nodes []node
	var outs [][]byte
	inits := [][]byte{
		floatTensor("scale", []int64{1}, []float32{1.0 / 255}),
		floatTensor("zero", []int64{1}, []float32{0}),
		int64Tensor("flat", []int64{1, -1, 1}),
		floatTensor("box", []int64{1, 1, 4}, []float32{0.5, 0.5, float32(math.Log(12)), float32(math.Log(12))}),
		floatTensor("kps", []int64{1, 1, 10}, []float32{-0.7, -0.5, 1.7, -0.5, 0.5, 0.9, -0.5, 2.1, 1.5, 2.1}),
	}
	nodes = append(nodes, brightness("input", "bright"))
	for _, s := range []int64{8, 16, 32} {
		p := fmt.Sprint(s)
		nodes = append(nodes,
			node{op: "AveragePool", in: []string{"bright"}, out: []string{"pool" + p}, attrs: [][]byte{attrInts("kernel_shape", []int64{s, s}), attrInts("strides", []int64{s, s})}},
			node{op: "Mul", in: []string{"pool" + p, map[int64]string{8: "scale", 16: "zero", 32: "zero"}[s]}, out: []string{"score" + p}},
			node{op: "Reshape", in: []string{"score" + p, "flat"}, out: []string{"cls_" + p}},
			node{op: "Identity", in: []string{"cls_" + p}, out: []string{"obj_" + p}},
			node{op: "Mul", in: []string{"cls_" + p, "zero"}, out: []string{"none" + p}},
			node{op: "Add", in: []string{"none" + p, "box"}, out: []string{"bbox_" + p}},
			node{op: "Add", in: []string{"none" + p, "kps"}, out: []string{"kps_" + p}},
		)
	}
	for _, k := range []string{"cls", "obj", "bbox", "kps"} {
		w := map[string]int64{"cls": 1, "obj": 1, "bbox": 4, "kps": 10}[k]
		for _, s := range []string{"8", "16", "32"} {
			outs = append(outs, valueInfoOwn(k+"_"+s, []int64{1, -1, w}))
		}
	}
	b := modelOf(nodes, inits, valueInfo("input", []int64{1, 3, -1, -1}), outs...)
	return write(t, dir, "face-finder.onnx", b)
}

// TextFinder writes a stand-in for PaddleOCR's text detector (input "x",
// 1×3×H×W normalized; output 1×1×H×W): how sure a pixel is text is how bright
// it is, so a white bar on black is a line of text.
func TextFinder(t testing.TB, dir string) string {
	t.Helper()
	nodes := []node{
		brightness("x", "bright"),
		{op: "Mul", in: []string{"bright", "gain"}, out: []string{"logit"}},
		{op: "Sigmoid", in: []string{"logit"}, out: []string{"y"}},
	}
	inits := [][]byte{floatTensor("gain", []int64{1}, []float32{4})}
	b := modelOf(nodes, inits, valueInfo("x", []int64{1, 3, -1, -1}), valueInfo("y", []int64{1, 1, -1, -1}))
	return write(t, dir, "text-finder.onnx", b)
}

// TextReader writes a stand-in for PaddleOCR's text recognizer (input "x",
// 1×3×48×W, −1…1; output 1×(W/8)×3, probabilities of blank, the
// dictionary's one character and a space): every 8 pixels across, the
// character where the line is bright and the blank where it is dark, so a
// line of three white bars reads as the character three times.
func TextReader(t testing.TB, dir string) string {
	t.Helper()
	nodes := []node{
		brightness("x", "bright"),
		{op: "AveragePool", in: []string{"bright"}, out: []string{"cols"}, attrs: [][]byte{attrInts("kernel_shape", []int64{48, 8}), attrInts("strides", []int64{48, 8})}},
		{op: "Reshape", in: []string{"cols", "flat"}, out: []string{"steps"}},
		{op: "Mul", in: []string{"steps", "gain"}, out: []string{"char"}},
		{op: "Mul", in: []string{"steps", "neg"}, out: []string{"blank"}},
		{op: "Mul", in: []string{"steps", "zero"}, out: []string{"none"}},
		{op: "Add", in: []string{"none", "low"}, out: []string{"space"}},
		{op: "Concat", in: []string{"blank", "char", "space"}, out: []string{"logits"}, attrs: [][]byte{attrInt("axis", 2)}},
		{op: "Softmax", in: []string{"logits"}, out: []string{"y"}, attrs: [][]byte{attrInt("axis", 2)}},
	}
	inits := [][]byte{
		int64Tensor("flat", []int64{1, -1, 1}),
		floatTensor("gain", []int64{1}, []float32{6}),
		floatTensor("neg", []int64{1}, []float32{-6}),
		floatTensor("zero", []int64{1}, []float32{0}),
		floatTensor("low", []int64{1}, []float32{-8}),
	}
	b := modelOf(nodes, inits, valueInfo("x", []int64{1, 3, 48, -1}), valueInfo("y", []int64{1, -1, 3}))
	return write(t, dir, "text-reader.onnx", b)
}

// TextChars writes the stand-in recognizer's settings: PaddleOCR's
// inference.yml with one character, the letter "I".
func TextChars(t testing.TB, dir string) string {
	t.Helper()
	return write(t, dir, "text-reader.yml", []byte("Global:\n  model_name: standin_rec\nPostProcess:\n  name: CTCLabelDecode\n  character_dict:\n  - I\n"))
}

// Saliency writes a stand-in for U²-Net-p (input "input.1", 1×3×320×320
// normalized; first output 1×1×320×320): what stands out is what is bright.
func Saliency(t testing.TB, dir string) string {
	t.Helper()
	nodes := []node{
		brightness("input.1", "bright"),
		{op: "Mul", in: []string{"bright", "gain"}, out: []string{"logit"}},
		{op: "Sigmoid", in: []string{"logit"}, out: []string{"d0"}},
	}
	inits := [][]byte{floatTensor("gain", []int64{1}, []float32{4})}
	b := modelOf(nodes, inits, valueInfo("input.1", []int64{1, 3, 320, 320}), valueInfo("d0", []int64{1, 1, 320, 320}))
	return write(t, dir, "saliency.onnx", b)
}

// Subject writes a stand-in for BiRefNet (input "input_image", 1×3×1024×1024
// normalized; output "output_image", 1×1×1024×1024, a score per pixel whose
// sigmoid is how sure it is the subject): the subject is what is bright.
func Subject(t testing.TB, dir string) string {
	t.Helper()
	nodes := []node{
		brightness("input_image", "bright"),
		{op: "Mul", in: []string{"bright", "gain"}, out: []string{"output_image"}},
	}
	inits := [][]byte{floatTensor("gain", []int64{1}, []float32{4})}
	b := modelOf(nodes, inits, valueInfo("input_image", []int64{1, 3, 1024, 1024}), valueInfo("output_image", []int64{1, 1, 1024, 1024}))
	return write(t, dir, "subject.onnx", b)
}
