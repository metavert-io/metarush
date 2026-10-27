// Package onnxtest writes tiny ONNX models for tests (no model downloads):
// the protobuf wire format of a ModelProto with one or two operators, and
// finds the onnxruntime library a test can use.
package onnxtest

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// Library is the onnxruntime library tests use: METARUSH_ORT_LIB, else the one
// that ships with a sherpa-onnx install in ~/.local/opt (skips the test when
// there is none).
func Library(t testing.TB) string {
	t.Helper()
	if p := os.Getenv("METARUSH_ORT_LIB"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	for _, name := range []string{"libonnxruntime.dylib", "libonnxruntime.so"} {
		p := filepath.Join(home, ".local", "opt", "sherpa-onnx", "current", "lib", name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Skip("onnxruntime is not installed here (set METARUSH_ORT_LIB)")
	return ""
}

type buf []byte

func (b buf) varint(v uint64) buf { return binary.AppendUvarint(b, v) }
func (b buf) key(field, wire int) buf {
	return b.varint(uint64(field<<3 | wire))
}
func (b buf) intField(field int, v int64) buf { return b.key(field, 0).varint(uint64(v)) }
func (b buf) bytesField(field int, v []byte) buf {
	return append(b.key(field, 2).varint(uint64(len(v))), v...)
}
func (b buf) strField(field int, s string) buf { return b.bytesField(field, []byte(s)) }

// valueInfo is a float tensor's name and shape (a negative size is a named,
// free dimension).
func valueInfo(name string, shape []int64) []byte {
	var sh buf
	for i, d := range shape {
		var dim buf
		if d < 0 {
			dim = dim.strField(2, "d"+string(rune('0'+i)))
		} else {
			dim = dim.intField(1, d)
		}
		sh = sh.bytesField(1, dim)
	}
	tensor := buf(nil).intField(1, 1 /* FLOAT */).bytesField(2, sh)
	typ := buf(nil).bytesField(1, tensor)
	return buf(nil).strField(1, name).bytesField(2, typ)
}

// int64Tensor is a constant (an initializer) of int64 values.
func int64Tensor(name string, vals []int64) []byte {
	b := buf(nil).intField(1, int64(len(vals))).intField(2, 7 /* INT64 */)
	var packed buf
	for _, v := range vals {
		packed = packed.varint(uint64(v))
	}
	return b.bytesField(7, packed).strField(8, name)
}

type node struct {
	op      string
	in, out []string
	attrs   [][]byte
}

func model(nodes []node, inits [][]byte, in, out []byte) []byte {
	var g buf
	for i, n := range nodes {
		var nb buf
		for _, s := range n.in {
			nb = nb.strField(1, s)
		}
		for _, s := range n.out {
			nb = nb.strField(2, s)
		}
		nb = nb.strField(3, "n"+string(rune('0'+i))).strField(4, n.op)
		for _, a := range n.attrs {
			nb = nb.bytesField(5, a)
		}
		g = g.bytesField(1, nb)
	}
	g = g.strField(2, "test")
	for _, t := range inits {
		g = g.bytesField(5, t)
	}
	g = g.bytesField(11, in).bytesField(12, out)
	opset := buf(nil).strField(1, "").intField(2, 13)
	return buf(nil).intField(1, 7).strField(2, "metarush-test").bytesField(7, g).bytesField(8, opset)
}

// Identity writes a model whose output y is its input x (any 2-D shape).
func Identity(t testing.TB, dir string) string {
	t.Helper()
	b := model([]node{{op: "Identity", in: []string{"x"}, out: []string{"y"}}}, nil, valueInfo("x", []int64{-1, -1}), valueInfo("y", []int64{-1, -1}))
	return write(t, dir, "identity.onnx", b)
}

// FirstValues writes a model that reads a 1×3×112×112 picture (input
// "data") and answers its first n values (output "fc1", 1×n): a stand-in for
// the face model with the same names and shapes.
func FirstValues(t testing.TB, dir string, n int64) string {
	t.Helper()
	axis := buf(nil).strField(1, "axis").intField(3, 1).intField(20, 2 /* INT */)
	nodes := []node{
		{op: "Flatten", in: []string{"data"}, out: []string{"flat"}, attrs: [][]byte{axis}},
		{op: "Slice", in: []string{"flat", "starts", "ends", "axes"}, out: []string{"fc1"}},
	}
	inits := [][]byte{int64Tensor("starts", []int64{0}), int64Tensor("ends", []int64{n}), int64Tensor("axes", []int64{1})}
	b := model(nodes, inits, valueInfo("data", []int64{1, 3, 112, 112}), valueInfo("fc1", []int64{1, n}))
	return write(t, dir, "first-values.onnx", b)
}

func write(t testing.TB, dir, name string, b []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}
