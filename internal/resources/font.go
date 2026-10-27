package resources

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"io"
	"os"
	"strings"
	"unicode/utf16"
)

// FontInfo is a font file's family and style.
type FontInfo struct {
	Family string
	Style  string
}

// ReadFont reads the family and style names from a TrueType or OpenType file
// (and WOFF, which wraps one). WOFF2 compresses its tables with Brotli, which
// MetaRush doesn't read: its family comes from the file name.
func ReadFont(path string) FontInfo {
	b, err := os.ReadFile(path)
	if err != nil || len(b) < 12 {
		return FontInfo{}
	}
	switch {
	case bytes.HasPrefix(b, []byte("wOFF")):
		if name := woffTable(b, "name"); name != nil {
			return nameTable(name)
		}
		return FontInfo{}
	case bytes.HasPrefix(b, []byte("wOF2")):
		return FontInfo{}
	}
	return nameTable(sfntTable(b, "name"))
}

// sfntTable returns one table of a TrueType/OpenType file.
func sfntTable(b []byte, tag string) []byte {
	if len(b) < 12 {
		return nil
	}
	n := int(binary.BigEndian.Uint16(b[4:6]))
	for i := 0; i < n; i++ {
		rec := 12 + i*16
		if rec+16 > len(b) {
			return nil
		}
		if string(b[rec:rec+4]) == tag {
			off := int(binary.BigEndian.Uint32(b[rec+8 : rec+12]))
			ln := int(binary.BigEndian.Uint32(b[rec+12 : rec+16]))
			if off < 0 || ln < 0 || off+ln > len(b) {
				return nil
			}
			return b[off : off+ln]
		}
	}
	return nil
}

// woffTable returns one (decompressed) table of a WOFF file.
func woffTable(b []byte, tag string) []byte {
	if len(b) < 44 {
		return nil
	}
	n := int(binary.BigEndian.Uint16(b[12:14]))
	for i := 0; i < n; i++ {
		rec := 44 + i*20
		if rec+20 > len(b) {
			return nil
		}
		if string(b[rec:rec+4]) != tag {
			continue
		}
		off := int(binary.BigEndian.Uint32(b[rec+4 : rec+8]))
		comp := int(binary.BigEndian.Uint32(b[rec+8 : rec+12]))
		orig := int(binary.BigEndian.Uint32(b[rec+12 : rec+16]))
		if off < 0 || comp < 0 || off+comp > len(b) || orig > 4<<20 {
			return nil
		}
		data := b[off : off+comp]
		if comp == orig {
			return data
		}
		r, err := zlib.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil
		}
		out, err := io.ReadAll(io.LimitReader(r, int64(orig)+1))
		if err != nil || len(out) != orig {
			return nil
		}
		return out
	}
	return nil
}

// nameTable reads family (16, else 1) and style (17, else 2) names,
// preferring Windows English, then any Windows record, then Mac Roman.
func nameTable(t []byte) FontInfo {
	if len(t) < 6 {
		return FontInfo{}
	}
	count := int(binary.BigEndian.Uint16(t[2:4]))
	strOff := int(binary.BigEndian.Uint16(t[4:6]))
	type cand struct {
		score int
		val   string
	}
	best := map[int]cand{}
	for i := 0; i < count; i++ {
		r := 6 + i*12
		if r+12 > len(t) {
			break
		}
		platform := binary.BigEndian.Uint16(t[r : r+2])
		lang := binary.BigEndian.Uint16(t[r+4 : r+6])
		id := int(binary.BigEndian.Uint16(t[r+6 : r+8]))
		ln := int(binary.BigEndian.Uint16(t[r+8 : r+10]))
		off := int(binary.BigEndian.Uint16(t[r+10 : r+12]))
		if id != 1 && id != 2 && id != 16 && id != 17 {
			continue
		}
		s, e := strOff+off, strOff+off+ln
		if s < 0 || e > len(t) || s > e {
			continue
		}
		raw := t[s:e]
		var val string
		score := 0
		switch platform {
		case 3: // Windows: UTF-16BE
			u := make([]uint16, len(raw)/2)
			for k := range u {
				u[k] = binary.BigEndian.Uint16(raw[2*k:])
			}
			val = string(utf16.Decode(u))
			score = 2
			if lang == 0x409 {
				score = 3
			}
		case 1: // Mac Roman (ASCII in practice)
			val = string(raw)
			score = 1
		case 0: // Unicode: UTF-16BE
			u := make([]uint16, len(raw)/2)
			for k := range u {
				u[k] = binary.BigEndian.Uint16(raw[2*k:])
			}
			val = string(utf16.Decode(u))
			score = 2
		default:
			continue
		}
		val = strings.TrimSpace(strings.ToValidUTF8(val, ""))
		if val == "" {
			continue
		}
		if c, ok := best[id]; !ok || score > c.score {
			best[id] = cand{score, val}
		}
	}
	fi := FontInfo{Family: best[16].val, Style: best[17].val}
	if fi.Family == "" {
		fi.Family = best[1].val
	}
	if fi.Style == "" {
		fi.Style = best[2].val
	}
	if len(fi.Family) > 100 {
		fi.Family = fi.Family[:100]
	}
	if len(fi.Style) > 60 {
		fi.Style = fi.Style[:60]
	}
	return fi
}
