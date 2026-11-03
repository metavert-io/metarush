package c2pa

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"os"
)

// boxUUID is the extended type of the box that carries Content Credentials
// in an MP4 (ISO BMFF): d8fec3d6-1b0e-483c-9297-5828877ec481, at the top
// level, a full box whose purpose is "manifest", then an 8-byte offset, then
// the manifest store (JUMBF).
var boxUUID = []byte{0xd8, 0xfe, 0xc3, 0xd6, 0x1b, 0x0e, 0x48, 0x3c, 0x92, 0x97, 0x58, 0x28, 0x87, 0x7e, 0xc4, 0x81}

// ErrNoManifest says a file carries no Content Credentials box.
var ErrNoManifest = errors.New("c2pa: no manifest store in the file")

// ManifestHash is the SHA-256 of the manifest store an MP4 carries (the
// JUMBF bytes of its Content Credentials box): it names exactly the
// credentials a delivered file went out with.
func ManifestHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	size := st.Size()
	var off int64
	head := make([]byte, 16)
	for off+8 <= size {
		if _, err := f.ReadAt(head[:8], off); err != nil {
			return "", err
		}
		boxSize := int64(binary.BigEndian.Uint32(head[:4]))
		typ := string(head[4:8])
		hdr := int64(8)
		switch boxSize {
		case 1:
			if _, err := f.ReadAt(head[8:16], off+8); err != nil {
				return "", err
			}
			boxSize, hdr = int64(binary.BigEndian.Uint64(head[8:16])), 16
		case 0:
			boxSize = size - off
		}
		if boxSize < hdr || off+boxSize > size {
			return "", errors.New("c2pa: the file's boxes don't add up")
		}
		if typ == "uuid" && boxSize >= hdr+16 {
			id := make([]byte, 16)
			if _, err := f.ReadAt(id, off+hdr); err != nil {
				return "", err
			}
			if bytes.Equal(id, boxUUID) {
				return storeHash(io.NewSectionReader(f, off+hdr+16, boxSize-hdr-16))
			}
		}
		off += boxSize
	}
	return "", ErrNoManifest
}

// storeHash reads a Content Credentials box's body (after its extended
// type): version and flags, the purpose, the offset (for a manifest), then
// the store, whose SHA-256 it returns.
func storeHash(r *io.SectionReader) (string, error) {
	const maxStore = 64 << 20
	if r.Size() > maxStore+64 {
		return "", errors.New("c2pa: the manifest store is too large")
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	if len(b) < 5 {
		return "", ErrNoManifest
	}
	b = b[4:] // version and flags
	end := bytes.IndexByte(b, 0)
	if end < 0 || end > 64 {
		return "", ErrNoManifest
	}
	purpose := string(b[:end])
	b = b[end+1:]
	if purpose != "manifest" {
		return "", ErrNoManifest
	}
	if len(b) < 8 {
		return "", ErrNoManifest
	}
	b = b[8:] // the offset to a Merkle tree (0 in a plain MP4)
	if len(b) < 8 || string(b[4:8]) != "jumb" {
		return "", ErrNoManifest
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
