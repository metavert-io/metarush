// Package fontface makes the font faces MetaRush draws labels with (contact
// sheets, smart sheets, QA sheets, motion test sheets). A parsed font is shared;
// a face is made for one drawing and closed after it: opentype faces keep
// scratch buffers, and one face shared by two contact sheets drawn at once
// crashed the worker (an index out of range inside image/draw).
package fontface

import (
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
)

var (
	mu    sync.Mutex
	fonts = map[string]*opentype.Font{}
)

// New makes a face of a TrueType font at size points. name is a stable key for
// the font (such as "gomonobold"): it is parsed once. New returns nil when the
// font can't be read; the caller closes the face when the drawing is done.
func New(name string, ttf []byte, size float64) font.Face {
	mu.Lock()
	f, ok := fonts[name]
	if !ok {
		var err error
		if f, err = opentype.Parse(ttf); err != nil {
			mu.Unlock()
			return nil
		}
		fonts[name] = f
	}
	mu.Unlock()
	fc, err := opentype.NewFace(f, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return nil
	}
	return fc
}
