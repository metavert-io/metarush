// Package resources is the resources library (D79): images, logos, SVGs,
// PDFs and fonts a project's graphics use. Files are recognized by their
// content (never only by their extension), converted or cleaned where needed
// (HEIC photos become JPEG, SVGs lose scripts and outside references), stored
// content-addressed in the media store and described (size, transparency,
// dominant colours, a thumbnail, PDF pages as pictures, a font's family),
// then tagged by simple heuristics. Every way in — the Add box, a drop
// anywhere, `metarush resources add`, the watched inbox and linked folders —
// goes through Sniff and AddFile, so they behave the same.
package resources

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Routes: where an added file goes.
const (
	RouteFootage  = "footage"  // video and audio: the footage pipeline (internal/ingest)
	RouteResource = "resource" // images, SVG, PDF, fonts: the resources library
	RouteUnknown  = "unknown"  // nothing MetaRush can use (the footage pipeline decides with ffprobe)
)

// Sniffed is what a file's first bytes say it is.
type Sniffed struct {
	Route string `json:"route" jsonschema:"enum=footage,enum=resource,enum=unknown"`
	// Kind is the resource kind (image, svg, pdf, font) for resources.
	Kind string `json:"kind,omitempty"`
	// Format is the file format: png, jpeg, webp, gif, heic, avif, svg, pdf,
	// ttf, otf, woff, woff2 (resources) or video/audio (footage, roughly).
	Format string `json:"format,omitempty"`
	MIME   string `json:"mime,omitempty"`
	Ext    string `json:"ext,omitempty"`
	// Convert: MetaRush converts it on the way in (HEIC and AVIF photos to JPEG).
	Convert bool `json:"convert,omitempty"`
}

// sniffBytes is how much of a file Sniff reads.
const sniffBytes = 64 << 10

// SniffFile reads the start of a file and says what it is.
func SniffFile(path string) (Sniffed, error) {
	f, err := os.Open(path)
	if err != nil {
		return Sniffed{}, err
	}
	defer f.Close()
	head := make([]byte, sniffBytes)
	n, err := io.ReadFull(f, head)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return Sniffed{}, err
	}
	s := Sniff(filepath.Base(path), head[:n])
	if s.Format == "gif" && animatedGIF(path) {
		// An animated GIF plays like a short video: it is footage.
		return Sniffed{Route: RouteFootage, Format: "gif", MIME: "image/gif", Ext: "gif"}, nil
	}
	return s, nil
}

// Sniff classifies a file from its name and first bytes (at least 512, up
// to 64 KiB for SVGs that start with a long comment).
func Sniff(name string, head []byte) Sniffed {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
	res := func(kind, format, mime, ext string) Sniffed {
		return Sniffed{Route: RouteResource, Kind: kind, Format: format, MIME: mime, Ext: ext}
	}
	switch {
	case bytes.HasPrefix(head, []byte("\x89PNG\r\n\x1a\n")):
		return res("image", "png", "image/png", "png")
	case bytes.HasPrefix(head, []byte{0xFF, 0xD8, 0xFF}):
		return res("image", "jpeg", "image/jpeg", "jpg")
	case len(head) >= 12 && bytes.Equal(head[0:4], []byte("RIFF")) && bytes.Equal(head[8:12], []byte("WEBP")):
		return res("image", "webp", "image/webp", "webp")
	case bytes.HasPrefix(head, []byte("GIF87a")), bytes.HasPrefix(head, []byte("GIF89a")):
		return res("image", "gif", "image/gif", "gif")
	case bytes.HasPrefix(head, []byte("%PDF-")):
		return res("pdf", "pdf", "application/pdf", "pdf")
	case bytes.HasPrefix(head, []byte("OTTO")):
		return res("font", "otf", "font/otf", "otf")
	case bytes.HasPrefix(head, []byte{0x00, 0x01, 0x00, 0x00}) && looksLikeSfnt(head), bytes.HasPrefix(head, []byte("true")) && looksLikeSfnt(head):
		return res("font", "ttf", "font/ttf", "ttf")
	case bytes.HasPrefix(head, []byte("wOF2")):
		return res("font", "woff2", "font/woff2", "woff2")
	case bytes.HasPrefix(head, []byte("wOFF")):
		return res("font", "woff", "font/woff", "woff")
	case len(head) >= 12 && bytes.Equal(head[4:8], []byte("ftyp")):
		brand := string(head[8:12])
		switch brand {
		case "heic", "heix", "hevc", "hevx", "heim", "heis", "mif1", "msf1":
			s := res("image", "heic", "image/heic", "heic")
			s.Convert = true
			return s
		case "avif", "avis":
			s := res("image", "avif", "image/avif", "avif")
			s.Convert = true
			return s
		}
		return Sniffed{Route: RouteFootage, Format: "video"}
	case looksLikeSVG(head, ext):
		return res("svg", "svg", "image/svg+xml", "svg")
	}
	switch ext {
	case "mp4", "mov", "m4v", "mkv", "webm", "avi", "mts", "m2ts", "mxf", "wav", "mp3", "m4a", "aac", "flac", "aif", "aiff", "ogg", "opus":
		return Sniffed{Route: RouteFootage, Format: "media"}
	}
	return Sniffed{Route: RouteUnknown}
}

// RouteByName guesses the route from a file name alone (the dashboard, before
// a file is sent, to say where it goes; the server decides by content).
func RouteByName(name string) Sniffed {
	switch strings.ToLower(strings.TrimPrefix(filepath.Ext(name), ".")) {
	case "png":
		return Sniffed{Route: RouteResource, Kind: "image", Format: "png"}
	case "jpg", "jpeg":
		return Sniffed{Route: RouteResource, Kind: "image", Format: "jpeg"}
	case "webp":
		return Sniffed{Route: RouteResource, Kind: "image", Format: "webp"}
	case "heic", "heif":
		return Sniffed{Route: RouteResource, Kind: "image", Format: "heic", Convert: true}
	case "avif":
		return Sniffed{Route: RouteResource, Kind: "image", Format: "avif", Convert: true}
	case "gif":
		return Sniffed{Route: RouteResource, Kind: "image", Format: "gif"}
	case "svg":
		return Sniffed{Route: RouteResource, Kind: "svg", Format: "svg"}
	case "pdf":
		return Sniffed{Route: RouteResource, Kind: "pdf", Format: "pdf"}
	case "ttf":
		return Sniffed{Route: RouteResource, Kind: "font", Format: "ttf"}
	case "otf":
		return Sniffed{Route: RouteResource, Kind: "font", Format: "otf"}
	case "woff":
		return Sniffed{Route: RouteResource, Kind: "font", Format: "woff"}
	case "woff2":
		return Sniffed{Route: RouteResource, Kind: "font", Format: "woff2"}
	}
	return Sniffed{Route: RouteFootage}
}

// looksLikeSfnt checks a TrueType header's table count is plausible.
func looksLikeSfnt(head []byte) bool {
	if len(head) < 12 {
		return false
	}
	n := int(head[4])<<8 | int(head[5])
	return n > 0 && n < 100
}

// looksLikeSVG: XML whose root element is svg (comments, a declaration and a
// doctype may come first).
func looksLikeSVG(head []byte, ext string) bool {
	s := head
	s = bytes.TrimPrefix(s, []byte("\xEF\xBB\xBF"))
	s = bytes.TrimLeft(s, " \t\r\n")
	if !bytes.HasPrefix(s, []byte("<")) {
		return false
	}
	low := bytes.ToLower(s)
	i := bytes.Index(low, []byte("<svg"))
	if i < 0 {
		return false
	}
	// Only declarations, comments and a doctype may come before <svg.
	pre := low[:i]
	for len(pre) > 0 {
		pre = bytes.TrimLeft(pre, " \t\r\n")
		switch {
		case len(pre) == 0:
		case bytes.HasPrefix(pre, []byte("<?")):
			j := bytes.Index(pre, []byte("?>"))
			if j < 0 {
				return false
			}
			pre = pre[j+2:]
		case bytes.HasPrefix(pre, []byte("<!--")):
			j := bytes.Index(pre, []byte("-->"))
			if j < 0 {
				return false
			}
			pre = pre[j+3:]
		case bytes.HasPrefix(pre, []byte("<!doctype")):
			j := bytes.IndexByte(pre, '>')
			if j < 0 {
				return false
			}
			pre = pre[j+1:]
		default:
			return false
		}
	}
	return true
}

// animatedGIF reports a GIF with more than one frame.
func animatedGIF(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return animatedGIFData(b)
}

// animatedGIFData reports GIF bytes with more than one frame.
func animatedGIFData(b []byte) bool {
	if len(b) < 13 {
		return false
	}
	// Count image descriptors by walking the block structure.
	i := 13
	if b[10]&0x80 != 0 {
		i += 3 * (1 << ((b[10] & 0x07) + 1))
	}
	frames := 0
	for i < len(b) {
		switch b[i] {
		case 0x2C: // image descriptor
			frames++
			if frames > 1 {
				return true
			}
			if i+10 > len(b) {
				return false
			}
			flags := b[i+9]
			i += 10
			if flags&0x80 != 0 {
				i += 3 * (1 << ((flags & 0x07) + 1))
			}
			i++ // LZW minimum code size
			i = skipSubBlocks(b, i)
		case 0x21: // extension
			i += 2
			i = skipSubBlocks(b, i)
		case 0x3B:
			return false
		default:
			return false
		}
	}
	return false
}

func skipSubBlocks(b []byte, i int) int {
	for i < len(b) {
		n := int(b[i])
		i++
		if n == 0 {
			return i
		}
		i += n
	}
	return i
}
