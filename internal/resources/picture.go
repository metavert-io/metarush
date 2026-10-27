package resources

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	_ "image/gif" // decoders
	"image/jpeg"
	"image/png"
	"os"
	"sort"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// PictureInfo is what MetaRush learns from a raster picture.
type PictureInfo struct {
	Width, Height int // as shown (EXIF orientation applied)
	HasAlpha      bool
	Colors        []string // dominant colours, most first (#RRGGBB)
	Palette       int      // distinct colour groups covering most of the picture
	// Thumb is a PNG of at most ThumbSize pixels on the long side (nil when the
	// picture is too large to decode in memory).
	Thumb          []byte
	ThumbW, ThumbH int
}

// ThumbSize is the long side of thumbnails.
const ThumbSize = 320

// maxDecodePixels bounds full decoding (a 12k × 12k poster would need 576 MB).
const maxDecodePixels = 60_000_000

// ReadPicture describes a PNG, JPEG, WebP or GIF file.
func ReadPicture(path string) (*PictureInfo, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("this picture couldn't be read: %v", err)
	}
	orient := jpegOrientation(b)
	info := &PictureInfo{Width: cfg.Width, Height: cfg.Height}
	if orient >= 5 && orient <= 8 {
		info.Width, info.Height = cfg.Height, cfg.Width
	}
	if cfg.Width*cfg.Height > maxDecodePixels {
		return info, nil
	}
	img, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return info, nil // size is known; the rest is a nicety
	}
	img = orientImage(img, orient)
	info.HasAlpha = hasAlpha(img)
	// Colours come from the picture's own pixels (nearest neighbour), so a
	// logo's exact brand colours come out, not blends made by smoothing.
	small := scaleWith(img, 96, draw.NearestNeighbor)
	info.Colors, info.Palette = dominantColors(small)
	thumb := scaleTo(img, ThumbSize)
	var buf bytes.Buffer
	if err := png.Encode(&buf, thumb); err == nil {
		info.Thumb = buf.Bytes()
		info.ThumbW, info.ThumbH = thumb.Bounds().Dx(), thumb.Bounds().Dy()
	}
	return info, nil
}

// scaleTo shrinks img to fit max × max (never enlarges), smoothly.
func scaleTo(img image.Image, maxSide int) *image.NRGBA {
	return scaleWith(img, maxSide, draw.CatmullRom)
}

func scaleWith(img image.Image, maxSide int, scaler draw.Scaler) *image.NRGBA {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return image.NewNRGBA(image.Rect(0, 0, 1, 1))
	}
	nw, nh := w, h
	if w > maxSide || h > maxSide {
		if w >= h {
			nw, nh = maxSide, max(1, h*maxSide/w)
		} else {
			nw, nh = max(1, w*maxSide/h), maxSide
		}
	}
	dst := image.NewNRGBA(image.Rect(0, 0, nw, nh))
	scaler.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
	return dst
}

// hasAlpha reports any pixel that is not fully opaque.
func hasAlpha(img image.Image) bool {
	switch m := img.(type) {
	case *image.YCbCr, *image.Gray, *image.CMYK:
		return false
	case *image.NRGBA:
		if m.Opaque() {
			return false
		}
	case *image.RGBA:
		if m.Opaque() {
			return false
		}
	case *image.Paletted:
		if m.Opaque() {
			return false
		}
	}
	b := img.Bounds()
	step := max(1, max(b.Dx(), b.Dy())/400)
	for y := b.Min.Y; y < b.Max.Y; y += step {
		for x := b.Min.X; x < b.Max.X; x += step {
			if _, _, _, a := img.At(x, y).RGBA(); a < 0xF000 {
				return true
			}
		}
	}
	return false
}

// dominantColors groups the visible pixels of a small copy into colour bins
// (4 bits per channel) and returns the most used, skipping near-duplicates,
// plus how many bins cover 95% of the picture (a logo has few).
func dominantColors(img *image.NRGBA) ([]string, int) {
	type bin struct {
		n       int
		r, g, b int
	}
	bins := map[int]*bin{}
	total := 0
	px := img.Pix
	for i := 0; i+3 < len(px); i += 4 {
		if px[i+3] < 128 {
			continue // see-through pixels are not part of the picture's colours
		}
		r, g, b := int(px[i]), int(px[i+1]), int(px[i+2])
		k := (r>>4)<<8 | (g>>4)<<4 | b>>4
		x := bins[k]
		if x == nil {
			x = &bin{}
			bins[k] = x
		}
		x.n++
		x.r += r
		x.g += g
		x.b += b
		total++
	}
	if total == 0 {
		return nil, 0
	}
	list := make([]*bin, 0, len(bins))
	for _, b := range bins {
		list = append(list, b)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].n != list[j].n {
			return list[i].n > list[j].n
		}
		return list[i].r+list[i].g*3+list[i].b*7 < list[j].r+list[j].g*3+list[j].b*7
	})
	covered, palette := 0, 0
	for _, b := range list {
		if covered*100 >= total*95 {
			break
		}
		covered += b.n
		palette++
	}
	var out []string
	var picked []color.NRGBA
	for _, b := range list {
		if len(out) >= 6 || b.n*200 < total { // at most 6, each at least 0.5% of the picture
			break
		}
		c := color.NRGBA{R: uint8(b.r / b.n), G: uint8(b.g / b.n), B: uint8(b.b / b.n), A: 255}
		near := false
		for _, p := range picked {
			if colorDist(p, c) < 48 {
				near = true
				break
			}
		}
		if near {
			continue
		}
		picked = append(picked, c)
		out = append(out, fmt.Sprintf("#%02X%02X%02X", c.R, c.G, c.B))
	}
	return out, palette
}

func colorDist(a, b color.NRGBA) int {
	dr, dg, db := int(a.R)-int(b.R), int(a.G)-int(b.G), int(a.B)-int(b.B)
	return abs(dr) + abs(dg) + abs(db)
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// jpegOrientation reads the EXIF orientation (1–8) of a JPEG; 1 when absent.
func jpegOrientation(b []byte) int {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return 1
	}
	i := 2
	for i+4 <= len(b) {
		if b[i] != 0xFF {
			return 1
		}
		marker := b[i+1]
		size := int(binary.BigEndian.Uint16(b[i+2 : i+4]))
		if marker == 0xDA || size < 2 { // start of scan: no more metadata
			return 1
		}
		seg := b[i+4 : min(len(b), i+2+size)]
		if marker == 0xE1 && len(seg) > 14 && string(seg[:6]) == "Exif\x00\x00" {
			return tiffOrientation(seg[6:])
		}
		i += 2 + size
	}
	return 1
}

func tiffOrientation(t []byte) int {
	if len(t) < 8 {
		return 1
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 1
	}
	off := int(bo.Uint32(t[4:8]))
	if off+2 > len(t) {
		return 1
	}
	n := int(bo.Uint16(t[off : off+2]))
	for k := 0; k < n; k++ {
		e := off + 2 + k*12
		if e+12 > len(t) {
			return 1
		}
		if bo.Uint16(t[e:e+2]) == 0x0112 {
			v := int(bo.Uint16(t[e+8 : e+10]))
			if v >= 1 && v <= 8 {
				return v
			}
			return 1
		}
	}
	return 1
}

// orientImage applies an EXIF orientation (browsers do the same when they draw it).
func orientImage(img image.Image, o int) image.Image {
	if o <= 1 || o > 8 {
		return img
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}
	dst := image.NewNRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var nx, ny int
			switch o {
			case 2:
				nx, ny = w-1-x, y
			case 3:
				nx, ny = w-1-x, h-1-y
			case 4:
				nx, ny = x, h-1-y
			case 5:
				nx, ny = y, x
			case 6:
				nx, ny = h-1-y, x
			case 7:
				nx, ny = h-1-y, w-1-x
			case 8:
				nx, ny = y, w-1-x
			}
			dst.Set(nx, ny, img.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}

// StillJPEG is a picture as a JPEG for looking at it (the on-device visual
// index reads JPEGs): turned upright, at most maxSide pixels on the long side,
// a see-through background laid on white (a logo stays readable).
func StillJPEG(path string, maxSide int) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	if cfg.Width*cfg.Height > maxDecodePixels {
		return nil, fmt.Errorf("the picture is too large to look at (%d × %d)", cfg.Width, cfg.Height)
	}
	img, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	small := scaleTo(orientImage(img, jpegOrientation(b)), maxSide)
	flat := image.NewRGBA(small.Bounds())
	draw.Draw(flat, flat.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(flat, flat.Bounds(), small, small.Bounds().Min, draw.Over)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, flat, &jpeg.Options{Quality: 90}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ThumbFromPNG makes a thumbnail PNG from a picture file (PDF pages).
func ThumbFromPNG(path string) ([]byte, int, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, 0, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, 0, 0, err
	}
	t := scaleTo(img, ThumbSize)
	var buf bytes.Buffer
	if err := png.Encode(&buf, t); err != nil {
		return nil, 0, 0, err
	}
	return buf.Bytes(), t.Bounds().Dx(), t.Bounds().Dy(), nil
}
