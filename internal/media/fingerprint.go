package media

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

// FingerprintSpan is how many bytes from each end of a file the fingerprint reads.
const FingerprintSpan = 1 << 20

// FingerprintPrefix versions the fingerprint format.
const FingerprintPrefix = "fp1:"

// Fingerprint is a cheap identity for duplicate detection before an upload:
// "fp1:" + hex SHA-256 of (the first MiB || the last MiB). Files shorter than a
// MiB hash themselves twice. The dashboard computes the same value with
// WebCrypto. A match is only a strong hint; the full SHA-256 after upload stays
// authoritative.
func Fingerprint(head, tail []byte) string {
	h := sha256.New()
	h.Write(head)
	h.Write(tail)
	return FingerprintPrefix + hex.EncodeToString(h.Sum(nil))
}

func spans(size int64) (headLen, tailOff, tailLen int64) {
	headLen = min(size, FingerprintSpan)
	tailLen = min(size, FingerprintSpan)
	return headLen, size - tailLen, tailLen
}

// FingerprintFile fingerprints a local file.
func FingerprintFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	size := st.Size()
	hl, to, tl := spans(size)
	head := make([]byte, hl)
	if _, err := f.ReadAt(head, 0); err != nil && err != io.EOF {
		return "", 0, err
	}
	tail := make([]byte, tl)
	if _, err := f.ReadAt(tail, to); err != nil && err != io.EOF {
		return "", 0, err
	}
	return Fingerprint(head, tail), size, nil
}

// Sampled verification hash: SHA-256 over SampleCount evenly spaced SampleSize
// chunks (the whole file when it is small). A fingerprint match is confirmed
// with it before a file is treated as a duplicate, so two different files that
// share their size and first/last MiB (WAVs that start and end in silence, say)
// are not mistaken for each other.
const (
	SampleCount  = 16
	SampleSize   = 64 << 10
	SamplePrefix = "sp1:"
)

// SampleOffsets are the chunk offsets for a file of size bytes (nil = hash the
// whole file). off_i = floor(i*(size-SampleSize)/(SampleCount-1)).
func SampleOffsets(size int64) []int64 {
	if size <= SampleCount*SampleSize {
		return nil
	}
	out := make([]int64, SampleCount)
	for i := range out {
		out[i] = int64(i) * (size - SampleSize) / (SampleCount - 1)
	}
	return out
}

func sampleRead(size int64, read func(off, n int64) ([]byte, error)) (string, error) {
	h := sha256.New()
	offs := SampleOffsets(size)
	if offs == nil {
		b, err := read(0, size)
		if err != nil {
			return "", err
		}
		h.Write(b)
	}
	for _, off := range offs {
		b, err := read(off, SampleSize)
		if err != nil {
			return "", err
		}
		h.Write(b)
	}
	return SamplePrefix + hex.EncodeToString(h.Sum(nil)), nil
}

// SampleFile computes the sampled verification hash of a local file.
func SampleFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	return sampleRead(st.Size(), func(off, n int64) ([]byte, error) {
		b := make([]byte, n)
		if _, err := f.ReadAt(b, off); err != nil && err != io.EOF {
			return nil, err
		}
		return b, nil
	})
}

// SampleObject computes the sampled verification hash of a stored object with ranged reads.
func SampleObject(ctx context.Context, s Store, key string, size int64) (string, error) {
	return sampleRead(size, func(off, n int64) ([]byte, error) { return readRange(ctx, s, key, off, n) })
}

func readRange(ctx context.Context, s Store, key string, off, n int64) ([]byte, error) {
	rc, _, err := s.Get(ctx, key, off, n)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, n))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) != n {
		return nil, fmt.Errorf("short read of %s: %d of %d bytes", key, len(b), n)
	}
	return b, nil
}

// FingerprintObject fingerprints a stored object with two ranged reads (used to
// backfill assets ingested before fingerprints existed).
func FingerprintObject(ctx context.Context, s Store, key string, size int64) (string, error) {
	hl, to, tl := spans(size)
	head, err := readRange(ctx, s, key, 0, hl)
	if err != nil {
		return "", err
	}
	tail, err := readRange(ctx, s, key, to, tl)
	if err != nil {
		return "", err
	}
	return Fingerprint(head, tail), nil
}
