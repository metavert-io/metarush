//go:build darwin || linux

// Package onnx runs ONNX models in-process through the onnxruntime C API,
// loaded at run time with purego from the library that ships with sherpa-onnx
// (MetaRush already installs it for transcription), so models need no Python
// and no cgo. The voice clarity score (internal/sound) and face recognition
// (internal/faceid) use it.
//
// The API is a versioned table of function pointers (OrtApi); only the
// entries MetaRush needs are bound, by their index in that table (stable:
// onnxruntime only appends to it).
package onnx

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

const ortAPIVersion = 14 // any library from onnxruntime 1.14 on

// Indices into OrtApi (onnxruntime_c_api.h; unchanged since they were added).
const (
	ortGetErrorMessage                = 2
	ortCreateEnv                      = 3
	ortCreateSession                  = 7
	ortRun                            = 9
	ortCreateSessionOptions           = 10
	ortSetSessionLogSeverityLevel     = 22
	ortSetIntraOpNumThreads           = 24
	ortSessionGetInputCount           = 30
	ortSessionGetOutputCount          = 31
	ortSessionGetInputName            = 36
	ortSessionGetOutputName           = 37
	ortCreateTensorWithDataAsOrtValue = 49
	ortGetTensorMutableData           = 51
	ortGetDimensionsCount             = 61
	ortGetDimensions                  = 62
	ortGetTensorShapeElementCount     = 64
	ortGetTensorTypeAndShape          = 65
	ortCreateCpuMemoryInfo            = 69
	ortAllocatorFree                  = 76
	ortGetAllocatorWithDefaultOptions = 78
	ortReleaseEnv                     = 92
	ortReleaseStatus                  = 93
	ortReleaseMemoryInfo              = 94
	ortReleaseSession                 = 95
	ortReleaseValue                   = 96
	ortReleaseTensorTypeAndShapeInfo  = 99
	ortReleaseSessionOptions          = 100
)

type ortAPIBase struct {
	GetAPI           uintptr
	GetVersionString uintptr
}

var ort struct {
	once    sync.Once
	err     error
	lib     string
	version string

	createEnv            func(level int32, logID string, out *uintptr) uintptr
	createSessionOptions func(out *uintptr) uintptr
	setLogLevel          func(opts uintptr, level int32) uintptr
	setIntraOpThreads    func(opts uintptr, n int32) uintptr
	createSession        func(env uintptr, path string, opts uintptr, out *uintptr) uintptr
	run                  func(session, runOpts uintptr, inNames *uintptr, inputs *uintptr, nIn uintptr, outNames *uintptr, nOut uintptr, outputs *uintptr) uintptr
	inputCount           func(session uintptr, out *uintptr) uintptr
	outputCount          func(session uintptr, out *uintptr) uintptr
	inputName            func(session, index, allocator uintptr, out **byte) uintptr
	outputName           func(session, index, allocator uintptr, out **byte) uintptr
	createTensor         func(info uintptr, data *float32, dataLen uintptr, shape *int64, shapeLen uintptr, typ int32, out *uintptr) uintptr
	tensorData           func(value uintptr, out **float32) uintptr
	typeAndShape         func(value uintptr, out *uintptr) uintptr
	dimsCount            func(info uintptr, out *uintptr) uintptr
	dims                 func(info uintptr, out *int64, n uintptr) uintptr
	elementCount         func(info uintptr, out *uintptr) uintptr
	cpuMemoryInfo        func(allocType, memType int32, out *uintptr) uintptr
	defaultAllocator     func(out *uintptr) uintptr
	allocatorFree        func(allocator uintptr, p *byte) uintptr
	errorMessage         func(status uintptr) string
	releaseEnv           func(p uintptr)
	releaseStatus        func(p uintptr)
	releaseMemoryInfo    func(p uintptr)
	releaseSession       func(p uintptr)
	releaseValue         func(p uintptr)
	releaseShapeInfo     func(p uintptr)
	releaseOptions       func(p uintptr)
	env                  uintptr
}

// Available is true on platforms where the library can be bound.
const Available = true

// bind loads the onnxruntime library once per process.
func bind(lib string) error {
	ort.once.Do(func() {
		ort.lib = lib
		h, err := purego.Dlopen(lib, purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			ort.err = fmt.Errorf("load %s: %w", lib, err)
			return
		}
		defer func() {
			if r := recover(); r != nil { // RegisterLibFunc panics on a missing symbol
				ort.err = fmt.Errorf("%s is not an onnxruntime library: %v", lib, r)
			}
		}()
		var getBase func() *ortAPIBase
		purego.RegisterLibFunc(&getBase, h, "OrtGetApiBase")
		base := getBase()
		if base == nil || base.GetAPI == 0 {
			ort.err = errors.New("onnxruntime returned no API")
			return
		}
		var version func() string
		purego.RegisterFunc(&version, base.GetVersionString)
		ort.version = version()
		var getAPI func(v uint32) *[128]uintptr
		purego.RegisterFunc(&getAPI, base.GetAPI)
		table := getAPI(ortAPIVersion)
		if table == nil {
			ort.err = fmt.Errorf("onnxruntime %s is too old (MetaRush needs 1.14 or later)", ort.version)
			return
		}
		reg := func(fptr any, idx int) { purego.RegisterFunc(fptr, table[idx]) }
		reg(&ort.createEnv, ortCreateEnv)
		reg(&ort.createSessionOptions, ortCreateSessionOptions)
		reg(&ort.setLogLevel, ortSetSessionLogSeverityLevel)
		reg(&ort.setIntraOpThreads, ortSetIntraOpNumThreads)
		reg(&ort.createSession, ortCreateSession)
		reg(&ort.run, ortRun)
		reg(&ort.inputCount, ortSessionGetInputCount)
		reg(&ort.outputCount, ortSessionGetOutputCount)
		reg(&ort.inputName, ortSessionGetInputName)
		reg(&ort.outputName, ortSessionGetOutputName)
		reg(&ort.createTensor, ortCreateTensorWithDataAsOrtValue)
		reg(&ort.tensorData, ortGetTensorMutableData)
		reg(&ort.typeAndShape, ortGetTensorTypeAndShape)
		reg(&ort.dimsCount, ortGetDimensionsCount)
		reg(&ort.dims, ortGetDimensions)
		reg(&ort.elementCount, ortGetTensorShapeElementCount)
		reg(&ort.cpuMemoryInfo, ortCreateCpuMemoryInfo)
		reg(&ort.defaultAllocator, ortGetAllocatorWithDefaultOptions)
		reg(&ort.allocatorFree, ortAllocatorFree)
		reg(&ort.errorMessage, ortGetErrorMessage)
		reg(&ort.releaseEnv, ortReleaseEnv)
		reg(&ort.releaseStatus, ortReleaseStatus)
		reg(&ort.releaseMemoryInfo, ortReleaseMemoryInfo)
		reg(&ort.releaseSession, ortReleaseSession)
		reg(&ort.releaseValue, ortReleaseValue)
		reg(&ort.releaseShapeInfo, ortReleaseTensorTypeAndShapeInfo)
		reg(&ort.releaseOptions, ortReleaseSessionOptions)
		if err := status(ort.createEnv(3 /* errors only */, "metarush", &ort.env)); err != nil {
			ort.err = err
		}
	})
	if ort.err == nil && lib != ort.lib {
		return fmt.Errorf("onnxruntime already loaded from %s", ort.lib)
	}
	return ort.err
}

// Version is the loaded library's version ("" before a model was opened).
func Version() string { return ort.version }

// status turns an OrtStatus* into an error (and frees it).
func status(st uintptr) error {
	if st == 0 {
		return nil
	}
	msg := ort.errorMessage(st)
	ort.releaseStatus(st)
	return errors.New("onnxruntime: " + msg)
}

// cString copies a C string.
func cString(p *byte) string {
	if p == nil {
		return ""
	}
	var b []byte
	for i := 0; i < 4096; i++ {
		c := *(*byte)(unsafe.Add(unsafe.Pointer(p), i))
		if c == 0 {
			break
		}
		b = append(b, c)
	}
	return string(b)
}

// Model is one loaded model with a single float input; outputs are read as floats.
type Model struct {
	mu       sync.Mutex
	session  uintptr
	info     uintptr
	inNames  []uintptr // C strings owned by onnxruntime's allocator
	outNames []uintptr
	// Inputs and Outputs are the model's tensor names.
	Inputs, Outputs []string
}

// Open loads a model file with the library at lib.
func Open(lib, path string, threads int) (*Model, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	if err := bind(lib); err != nil {
		return nil, err
	}
	var opts uintptr
	if err := status(ort.createSessionOptions(&opts)); err != nil {
		return nil, err
	}
	defer ort.releaseOptions(opts)
	_ = status(ort.setLogLevel(opts, 3))
	if threads < 1 {
		threads = 1
	}
	_ = status(ort.setIntraOpThreads(opts, int32(threads)))
	m := &Model{}
	if err := status(ort.createSession(ort.env, path, opts, &m.session)); err != nil {
		return nil, err
	}
	if err := status(ort.cpuMemoryInfo(0 /* device allocator */, 0 /* default memory */, &m.info)); err != nil {
		m.Close()
		return nil, err
	}
	var alloc uintptr
	if err := status(ort.defaultAllocator(&alloc)); err != nil {
		m.Close()
		return nil, err
	}
	var nIn, nOut uintptr
	if err := status(ort.inputCount(m.session, &nIn)); err != nil {
		m.Close()
		return nil, err
	}
	if err := status(ort.outputCount(m.session, &nOut)); err != nil {
		m.Close()
		return nil, err
	}
	for i := uintptr(0); i < nIn; i++ {
		var p *byte
		if err := status(ort.inputName(m.session, i, alloc, &p)); err != nil {
			m.Close()
			return nil, err
		}
		m.inNames = append(m.inNames, uintptr(unsafe.Pointer(p)))
		m.Inputs = append(m.Inputs, cString(p))
	}
	for i := uintptr(0); i < nOut; i++ {
		var p *byte
		if err := status(ort.outputName(m.session, i, alloc, &p)); err != nil {
			m.Close()
			return nil, err
		}
		m.outNames = append(m.outNames, uintptr(unsafe.Pointer(p)))
		m.Outputs = append(m.Outputs, cString(p))
	}
	if len(m.inNames) != 1 || len(m.outNames) < 1 {
		m.Close()
		return nil, fmt.Errorf("unexpected model shape (inputs %v, outputs %v)", m.Inputs, m.Outputs)
	}
	return m, nil
}

// Tensor is one output: its values and shape.
type Tensor struct {
	Data  []float32
	Shape []int64
}

// Run feeds one float tensor and returns every output with its shape.
func (m *Model) Run(input []float32, shape []int64) ([]Tensor, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.session == 0 {
		return nil, errors.New("model is closed")
	}
	if len(input) == 0 || len(shape) == 0 {
		return nil, errors.New("no input")
	}
	var in uintptr
	if err := status(ort.createTensor(m.info, &input[0], uintptr(len(input)*4), &shape[0], uintptr(len(shape)), 1 /* float */, &in)); err != nil {
		return nil, err
	}
	defer ort.releaseValue(in)
	inputs := []uintptr{in}
	outputs := make([]uintptr, len(m.outNames))
	err := status(ort.run(m.session, 0, &m.inNames[0], &inputs[0], 1, &m.outNames[0], uintptr(len(m.outNames)), &outputs[0]))
	runtime.KeepAlive(input)
	runtime.KeepAlive(shape)
	if err != nil {
		return nil, err
	}
	out := make([]Tensor, 0, len(outputs))
	defer func() {
		for _, v := range outputs {
			if v != 0 {
				ort.releaseValue(v)
			}
		}
	}()
	for _, v := range outputs {
		if v == 0 {
			out = append(out, Tensor{})
			continue
		}
		t, err := read(v)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// read copies an output value's floats and shape.
func read(v uintptr) (Tensor, error) {
	var info uintptr
	if err := status(ort.typeAndShape(v, &info)); err != nil {
		return Tensor{}, err
	}
	defer ort.releaseShapeInfo(info)
	var nd, n uintptr
	if err := status(ort.dimsCount(info, &nd)); err != nil {
		return Tensor{}, err
	}
	shape := make([]int64, nd)
	if nd > 0 {
		if err := status(ort.dims(info, &shape[0], nd)); err != nil {
			return Tensor{}, err
		}
	}
	if err := status(ort.elementCount(info, &n)); err != nil {
		return Tensor{}, err
	}
	var p *float32
	if err := status(ort.tensorData(v, &p)); err != nil || (p == nil && n > 0) {
		return Tensor{}, fmt.Errorf("could not read the model's answer: %v", err)
	}
	t := Tensor{Shape: shape}
	if n > 0 {
		t.Data = append([]float32(nil), unsafe.Slice(p, int(n))...)
	}
	return t, nil
}

// Close frees the session.
func (m *Model) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.session != 0 {
		ort.releaseSession(m.session)
		m.session = 0
	}
	if m.info != 0 {
		ort.releaseMemoryInfo(m.info)
		m.info = 0
	}
	// The name strings belong to onnxruntime's default allocator for the life of the process.
}

// Library finds onnxruntime next to a sherpa-onnx binary (it ships with it):
// <prefix>/bin/sherpa-onnx-… → <prefix>/lib/libonnxruntime.{dylib,so}.
func Library(sherpaBin string) string {
	if sherpaBin == "" {
		return ""
	}
	p, err := filepath.EvalSymlinks(sherpaBin)
	if err != nil {
		p = sherpaBin
	}
	dir := filepath.Join(filepath.Dir(filepath.Dir(p)), "lib")
	for _, pat := range []string{"libonnxruntime.dylib", "libonnxruntime.so", "libonnxruntime.*.dylib", "libonnxruntime.so.*"} {
		if ms, _ := filepath.Glob(filepath.Join(dir, pat)); len(ms) > 0 {
			sort.Strings(ms)
			return ms[0]
		}
	}
	return ""
}
