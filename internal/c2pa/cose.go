package c2pa

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
)

// What a signed copy's manifest store says was signed (D436): each
// manifest's claim signature, a COSE_Sign1 in its c2pa.signature box. Sign
// requires these to be exactly the signatures MetaRush returned, so a copy
// signed by anything or anyone else is never delivered.

// StoreSignatures reads the COSE signature of every manifest in a manifest
// store (JUMBF), in the store's order.
func StoreSignatures(store []byte) ([][]byte, error) {
	top, err := jumbfBoxes(store)
	if err != nil {
		return nil, err
	}
	if len(top) == 0 || top[0].typ != "jumb" {
		return nil, errors.New("c2pa: the manifest store isn't a JUMBF box")
	}
	label, manifests, err := superbox(top[0].body)
	if err != nil {
		return nil, err
	}
	if label != "c2pa" {
		return nil, fmt.Errorf("c2pa: the store is labelled %q", label)
	}
	var out [][]byte
	for _, m := range manifests {
		if m.typ != "jumb" {
			continue
		}
		_, parts, err := superbox(m.body)
		if err != nil {
			return nil, err
		}
		found := 0
		for _, p := range parts {
			if p.typ != "jumb" {
				continue
			}
			plabel, content, err := superbox(p.body)
			if err != nil {
				return nil, err
			}
			if plabel != "c2pa.signature" {
				continue
			}
			for _, c := range content {
				if c.typ != "cbor" {
					continue
				}
				sig, err := coseSignature(c.body)
				if err != nil {
					return nil, err
				}
				out = append(out, sig)
				found++
			}
		}
		if found != 1 {
			return nil, fmt.Errorf("c2pa: a manifest carries %d signatures", found)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("c2pa: the store has no signed manifest")
	}
	return out, nil
}

// manifestStore reads the manifest store an MP4 carries.
func manifestStore(path string) ([]byte, error) {
	var store []byte
	err := eachC2PABox(path, func(body []byte) error {
		s, err := storeOf(body)
		if err != nil {
			return err
		}
		store = s
		return nil
	})
	if err != nil {
		return nil, err
	}
	if store == nil {
		return nil, ErrNoManifest
	}
	return store, nil
}

type jbox struct {
	typ  string
	body []byte
}

// jumbfBoxes splits b into its boxes.
func jumbfBoxes(b []byte) ([]jbox, error) {
	var out []jbox
	for off := 0; off < len(b); {
		if len(b)-off < 8 {
			return nil, errors.New("c2pa: a JUMBF box is cut short")
		}
		size := uint64(binary.BigEndian.Uint32(b[off:]))
		typ := string(b[off+4 : off+8])
		hdr := uint64(8)
		switch size {
		case 1:
			if len(b)-off < 16 {
				return nil, errors.New("c2pa: a JUMBF box is cut short")
			}
			size, hdr = binary.BigEndian.Uint64(b[off+8:]), 16
		case 0:
			size = uint64(len(b) - off)
		}
		if size < hdr || size > uint64(len(b)-off) {
			return nil, errors.New("c2pa: the JUMBF boxes don't add up")
		}
		out = append(out, jbox{typ: typ, body: b[off+int(hdr) : off+int(size)]})
		off += int(size)
	}
	return out, nil
}

// superbox reads a jumb box's body: its description's label and the boxes after it.
func superbox(body []byte) (string, []jbox, error) {
	boxes, err := jumbfBoxes(body)
	if err != nil {
		return "", nil, err
	}
	if len(boxes) == 0 || boxes[0].typ != "jumd" {
		return "", nil, errors.New("c2pa: a JUMBF superbox has no description")
	}
	d := boxes[0].body
	if len(d) < 17 {
		return "", nil, errors.New("c2pa: a JUMBF description is cut short")
	}
	label := ""
	if d[16]&0x02 != 0 { // the label is present
		rest := d[17:]
		end := bytes.IndexByte(rest, 0)
		if end < 0 {
			return "", nil, errors.New("c2pa: a JUMBF label isn't ended")
		}
		label = string(rest[:end])
	}
	return label, boxes[1:], nil
}

// coseSignature is the signature of a COSE_Sign1 (tag 18, optional):
// [protected, unprotected, payload, signature].
func coseSignature(b []byte) ([]byte, error) {
	r := &cborReader{b: b}
	major, n, err := r.head()
	if err != nil {
		return nil, err
	}
	if major == 6 {
		if n != 18 {
			return nil, fmt.Errorf("c2pa: a COSE tag %d, not COSE_Sign1", n)
		}
		if major, n, err = r.head(); err != nil {
			return nil, err
		}
	}
	if major != 4 || n != 4 {
		return nil, errors.New("c2pa: the claim signature isn't a COSE_Sign1")
	}
	if _, err := r.bytes(); err != nil { // the protected header
		return nil, err
	}
	if err := r.skip(0); err != nil { // the unprotected header
		return nil, err
	}
	if err := r.skip(0); err != nil { // the payload (detached: nil)
		return nil, err
	}
	sig, err := r.bytes()
	if err != nil {
		return nil, err
	}
	if len(sig) == 0 {
		return nil, errors.New("c2pa: the claim signature is empty")
	}
	return sig, nil
}

// cborReader reads just enough CBOR (definite lengths) for a COSE_Sign1.
type cborReader struct {
	b []byte
	i int
}

func (r *cborReader) head() (major byte, n uint64, err error) {
	if r.i >= len(r.b) {
		return 0, 0, errors.New("c2pa: CBOR cut short")
	}
	c := r.b[r.i]
	r.i++
	major, info := c>>5, c&0x1f
	switch {
	case info < 24:
		return major, uint64(info), nil
	case info == 24 || info == 25 || info == 26 || info == 27:
		size := 1 << (info - 24)
		if r.i+size > len(r.b) {
			return 0, 0, errors.New("c2pa: CBOR cut short")
		}
		for _, x := range r.b[r.i : r.i+size] {
			n = n<<8 | uint64(x)
		}
		r.i += size
		return major, n, nil
	}
	return 0, 0, errors.New("c2pa: CBOR of indefinite length isn't read here")
}

func (r *cborReader) bytes() ([]byte, error) {
	major, n, err := r.head()
	if err != nil {
		return nil, err
	}
	if major != 2 {
		return nil, errors.New("c2pa: CBOR bytes expected")
	}
	if n > uint64(len(r.b)-r.i) {
		return nil, errors.New("c2pa: CBOR cut short")
	}
	out := r.b[r.i : r.i+int(n)]
	r.i += int(n)
	return out, nil
}

// skip passes over one item, whatever it is.
func (r *cborReader) skip(depth int) error {
	if depth > 32 {
		return errors.New("c2pa: CBOR nested too deep")
	}
	major, n, err := r.head()
	if err != nil {
		return err
	}
	switch major {
	case 0, 1, 7: // integers, simple values and floats (their bytes were the head's)
		return nil
	case 2, 3:
		if n > uint64(len(r.b)-r.i) {
			return errors.New("c2pa: CBOR cut short")
		}
		r.i += int(n)
		return nil
	case 4:
		for k := uint64(0); k < n; k++ {
			if err := r.skip(depth + 1); err != nil {
				return err
			}
		}
		return nil
	case 5:
		for k := uint64(0); k < 2*n; k++ {
			if err := r.skip(depth + 1); err != nil {
				return err
			}
		}
		return nil
	case 6:
		return r.skip(depth + 1)
	}
	return errors.New("c2pa: CBOR not understood")
}
