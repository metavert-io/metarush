//go:build !darwin && !linux

package onnx

import "errors"

// Available is false: onnxruntime is bound on macOS and Linux only.
const Available = false

// ErrUnsupported is every call on this platform.
var ErrUnsupported = errors.New("running models on this computer needs macOS or Linux")

// Model is unavailable on this platform.
type Model struct {
	Inputs, Outputs []string
}

// Tensor is one output: its values and shape.
type Tensor struct {
	Data  []float32
	Shape []int64
}

// Open is unavailable on this platform.
func Open(lib, path string, threads int) (*Model, error) { return nil, ErrUnsupported }

// Run is unavailable on this platform.
func (m *Model) Run(input []float32, shape []int64) ([]Tensor, error) { return nil, ErrUnsupported }

// Close does nothing here.
func (m *Model) Close() {}

// Version is empty here.
func Version() string { return "" }

// Library finds nothing here.
func Library(string) string { return "" }
