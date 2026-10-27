//go:build !darwin && !linux

package voice

// Extractor is unavailable on this platform (the sherpa-onnx C library is only
// bound on macOS and Linux).
type Extractor struct{}

// Open always reports ErrUnavailable here.
func Open(lib, model string, threads int) (*Extractor, error) { return nil, ErrUnavailable }

// Dim is 0.
func (e *Extractor) Dim() int { return 0 }

// Embed always fails.
func (e *Extractor) Embed([]float32) ([]float32, error) { return nil, ErrUnavailable }

// Close does nothing.
func (e *Extractor) Close() {}
