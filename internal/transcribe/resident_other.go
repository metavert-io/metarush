//go:build !darwin && !linux

package transcribe

import "fmt"

// The sherpa-onnx C library is only bound on macOS and Linux; elsewhere the
// CLI runs once per batch.

type localModel struct{}

func residentSupported(lib string) error {
	return fmt.Errorf("%w: not supported on this platform", ErrResidentUnavailable)
}

func openLocal(lib, dir string, threads int) (*localModel, error) {
	return nil, residentSupported(lib)
}

func (m *localModel) decode(chunks [][]float32) ([]string, error) { return nil, residentSupported("") }

func (m *localModel) close() {}

func libraryVersion() string { return "" }

func maxRSS() int64 { return 0 }
