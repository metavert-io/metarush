//go:build darwin || linux

package onnx

import (
	"testing"

	"github.com/metavert-io/metarush/internal/onnx/onnxtest"
)

// The binding runs a model and reads its answer with the answer's own shape.
func TestRunReturnsOutputsWithTheirShape(t *testing.T) {
	lib := onnxtest.Library(t)
	m, err := Open(lib, onnxtest.Identity(t, t.TempDir()), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if len(m.Inputs) != 1 || m.Inputs[0] != "x" || len(m.Outputs) != 1 || m.Outputs[0] != "y" {
		t.Fatalf("names: %v %v", m.Inputs, m.Outputs)
	}
	out, err := m.Run([]float32{1, 2, 3, 4, 5, 6}, []int64{2, 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || len(out[0].Shape) != 2 || out[0].Shape[0] != 2 || out[0].Shape[1] != 3 || len(out[0].Data) != 6 || out[0].Data[5] != 6 {
		t.Fatalf("answer: %+v", out)
	}
	if Version() == "" {
		t.Fatal("the library's version is known once it is loaded")
	}
	if _, err := m.Run(nil, nil); err == nil {
		t.Fatal("no input must be refused")
	}
	m.Close()
	if _, err := m.Run([]float32{1}, []int64{1, 1}); err == nil {
		t.Fatal("a closed model must refuse")
	}
}
