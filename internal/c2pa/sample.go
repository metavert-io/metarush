package c2pa

import (
	_ "embed"
	"os"
)

// sample is a one-second, 64-pixel video MetaRush made (no one's footage):
// what `metarush credentials test` signs to try a certificate.
//
//go:embed sample.mp4
var sample []byte

// WriteSample writes the sample video to path (which must not exist).
func WriteSample(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(sample); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
