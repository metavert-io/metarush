package resources

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/metavert-io/metarush/internal/models"
)

// testLogo draws a two-colour mark on a see-through background.
func testLogo(w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			switch {
			case x > w/5 && x < w*2/5 && y > h/5 && y < h*4/5:
				img.Set(x, y, color.NRGBA{0xE0, 0x40, 0x30, 255})
			case x > w/2 && x < w*4/5 && y > h/3 && y < h*2/3:
				img.Set(x, y, color.NRGBA{0x20, 0x30, 0x90, 255})
			}
		}
	}
	return img
}

// testPhoto is an opaque picture with many colours.
func testPhoto(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 255 / w), uint8(y * 255 / h), uint8((x + y) % 256), 255})
		}
	}
	return img
}

func writePNG(t *testing.T, dir, name string, img image.Image) string {
	t.Helper()
	p := filepath.Join(dir, name)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSniff(t *testing.T) {
	dir := t.TempDir()
	logo := writePNG(t, dir, "x.bin", testLogo(40, 20))
	cases := []struct {
		name  string
		head  []byte
		route string
		kind  string
	}{
		{"a.png", mustRead(t, logo), RouteResource, "image"},
		{"a.jpg", []byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0}, RouteResource, "image"},
		{"a.webp", []byte("RIFF\x00\x00\x00\x00WEBPVP8 "), RouteResource, "image"},
		{"a.pdf", []byte("%PDF-1.7\n"), RouteResource, "pdf"},
		{"a.svg", []byte("\xEF\xBB\xBF<?xml version=\"1.0\"?>\n<!-- made by hand -->\n<!DOCTYPE svg>\n<svg xmlns=\"http://www.w3.org/2000/svg\"/>"), RouteResource, "svg"},
		{"a.txt", []byte("<html><svg></svg></html>"), RouteUnknown, ""},
		{"a.otf", []byte("OTTO\x00\x0b\x00\x80"), RouteResource, "font"},
		{"a.ttf", []byte{0, 1, 0, 0, 0, 12, 0, 128, 0, 3, 0, 32}, RouteResource, "font"},
		{"a.woff2", []byte("wOF2\x00\x01\x00\x00"), RouteResource, "font"},
		{"a.heic", []byte("\x00\x00\x00\x18ftypheic\x00\x00\x00\x00"), RouteResource, "image"},
		{"a.mp4", []byte("\x00\x00\x00\x18ftypisom\x00\x00\x02\x00"), RouteFootage, ""},
		{"a.mov", []byte("\x00\x00\x00\x14ftypqt  "), RouteFootage, ""},
		{"a.wav", []byte("RIFF\x00\x00\x00\x00WAVEfmt "), RouteFootage, ""},
	}
	for _, c := range cases {
		s := Sniff(c.name, c.head)
		if s.Route != c.route || s.Kind != c.kind {
			t.Errorf("%s: got %s/%s, want %s/%s", c.name, s.Route, s.Kind, c.route, c.kind)
		}
	}
	if s := Sniff("a.heic", []byte("\x00\x00\x00\x18ftypheic\x00\x00\x00\x00")); !s.Convert {
		t.Error("HEIC should be converted")
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSanitizeSVGRemovesCodeAndOutsideReferences(t *testing.T) {
	in := `<?xml version="1.0"?>
<?xml-stylesheet href="https://evil.example/x.css"?>
<!DOCTYPE svg [<!ENTITY x "y">]>
<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" width="120" height="40" viewBox="0 0 120 40" onload="alert(1)">
  <!-- a comment -->
  <script>alert('hi')</script>
  <style>@import url(https://evil.example/a.css); .a { fill: #E0A458; background: url("https://evil.example/b.png") } .b{fill:url(#g)}</style>
  <defs><linearGradient id="g"><stop offset="0" stop-color="#112233"/></linearGradient></defs>
  <rect class="a" width="50" height="40" fill="#e0a458" onclick="steal()"/>
  <rect x="60" width="50" height="40" fill="url(#g)" style="fill: #0B0B0E; filter: url(https://evil.example/f.svg#x)"/>
  <image xlink:href="https://evil.example/tracker.png" width="1" height="1"/>
  <image href="data:image/png;base64,iVBORw0KGgo=" width="1" height="1"/>
  <use href="#g"/>
  <a href="javascript:alert(1)"><text>Hi &amp; bye</text></a>
  <foreignObject><div xmlns="http://www.w3.org/1999/xhtml">html</div></foreignObject>
  <set attributeName="href" to="javascript:alert(1)"/>
</svg>`
	out, changes, err := SanitizeSVG([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, bad := range []string{"<script", "alert", "onload", "onclick", "evil.example", "foreignObject", "<!--", "xml-stylesheet", "ENTITY", "javascript"} {
		if strings.Contains(s, bad) {
			t.Errorf("sanitized SVG still contains %q:\n%s", bad, s)
		}
	}
	for _, good := range []string{`fill="url(#g)"`, `href="#g"`, `data:image/png;base64`, `Hi &amp; bye`, `xmlns:xlink=`, `stop-color="#112233"`, `.b{fill:url(#g)}`} {
		if !strings.Contains(s, good) {
			t.Errorf("sanitized SVG lost %q:\n%s", good, s)
		}
	}
	if len(changes) == 0 {
		t.Error("expected the changes to be listed")
	}
	// The cleaned file is still an SVG that reads back the same way.
	again, changes2, err := SanitizeSVG(out)
	if err != nil || len(changes2) != 0 || string(again) != s {
		t.Fatalf("sanitizing twice changed it again (%v): %v", err, changes2)
	}
	info := ReadSVG(out)
	if info.Width != 120 || info.Height != 40 {
		t.Errorf("size %dx%d", info.Width, info.Height)
	}
	if len(info.Colors) == 0 || info.Colors[0] != "#E0A458" {
		t.Errorf("colours %v", info.Colors)
	}
}

func TestSanitizeSVGRefusesNonSVG(t *testing.T) {
	if _, _, err := SanitizeSVG([]byte(`<html><body/></html>`)); err == nil {
		t.Fatal("an HTML file must not pass as an SVG")
	}
	if _, _, err := SanitizeSVG([]byte(`<svg><g></svg>`)); err == nil {
		t.Fatal("broken XML must be refused")
	}
}

func TestSVGSizeFromViewBox(t *testing.T) {
	info := ReadSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 300 150"><path fill="red" d="M0 0"/></svg>`))
	if info.Width != 300 || info.Height != 150 || info.Colors[0] != "#FF0000" {
		t.Fatalf("%+v", info)
	}
	info = ReadSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="10mm" viewBox="0 0 2 1"/>`))
	if info.Width != 38 || info.Height != 19 {
		t.Fatalf("%+v", info)
	}
}

func TestReadPictureAndTags(t *testing.T) {
	dir := t.TempDir()
	logo := writePNG(t, dir, "logo-final.png", testLogo(400, 200))
	pi, err := ReadPicture(logo)
	if err != nil {
		t.Fatal(err)
	}
	if pi.Width != 400 || pi.Height != 200 || !pi.HasAlpha || pi.Palette > 3 || len(pi.Colors) != 2 {
		t.Fatalf("logo: %+v", pi)
	}
	if pi.Colors[0] != "#E04030" && pi.Colors[0] != "#203090" {
		t.Errorf("colours %v", pi.Colors)
	}
	if pi.Thumb == nil || pi.ThumbW != ThumbSize || pi.ThumbH != ThumbSize/2 {
		t.Errorf("thumb %dx%d", pi.ThumbW, pi.ThumbH)
	}
	v := &models.ResourceVersion{Width: pi.Width, Height: pi.Height, HasAlpha: pi.HasAlpha, Colors: pi.Colors}
	tags := AutoTags(context.Background(), TagInput{Name: "brand mark.png", Kind: models.ResourceImage, Version: v, Palette: pi.Palette})
	if !hasTagNamed(tags, "logo") {
		t.Errorf("a see-through two-colour picture should look like a logo: %+v", tags)
	}
	// A screen-sized photo looks like a screenshot; its name can say so too.
	photo := filepath.Join(dir, "Screenshot 2026-10-19 at 09.12.44.jpg")
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, testPhoto(1440, 900), &jpeg.Options{Quality: 80})
	_ = os.WriteFile(photo, buf.Bytes(), 0o644)
	pp, err := ReadPicture(photo)
	if err != nil || pp.HasAlpha || pp.Width != 1440 {
		t.Fatalf("photo %+v %v", pp, err)
	}
	v2 := &models.ResourceVersion{Width: pp.Width, Height: pp.Height}
	tags = AutoTags(context.Background(), TagInput{Name: filepath.Base(photo), Kind: models.ResourceImage, Version: v2, Palette: pp.Palette})
	if !hasTagNamed(tags, "screenshot") {
		t.Errorf("tags %+v", tags)
	}
	for _, tg := range tags {
		if tg.Why == "" || tg.Source == "" {
			t.Errorf("a suggestion must say why and where it came from: %+v", tg)
		}
	}
}

func hasTagNamed(ts []models.ResourceTag, tag string) bool {
	for _, t := range ts {
		if t.Tag == tag {
			return true
		}
	}
	return false
}

func TestJPEGOrientation(t *testing.T) {
	// A tiny EXIF block with orientation 6 (rotate 90°) after the SOI marker.
	tiff := []byte{'M', 'M', 0, 42, 0, 0, 0, 8, 0, 1, 0x01, 0x12, 0, 3, 0, 0, 0, 1, 0, 6, 0, 0, 0, 0, 0, 0}
	app1 := append([]byte("Exif\x00\x00"), tiff...)
	seg := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(seg[2:], uint16(len(app1)+2))
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, testPhoto(60, 20), nil)
	raw := buf.Bytes()
	withExif := append(append(append([]byte{}, raw[:2]...), append(seg, app1...)...), raw[2:]...)
	if o := jpegOrientation(withExif); o != 6 {
		t.Fatalf("orientation %d", o)
	}
	p := filepath.Join(t.TempDir(), "rotated.jpg")
	_ = os.WriteFile(p, withExif, 0o644)
	pi, err := ReadPicture(p)
	if err != nil || pi.Width != 20 || pi.Height != 60 {
		t.Fatalf("a rotated photo is as tall as it is shown: %+v %v", pi, err)
	}
}

// sfnt builds a font file holding only a name table.
func sfnt(family, style string) []byte {
	enc := func(s string) []byte {
		u := utf16.Encode([]rune(s))
		b := make([]byte, 2*len(u))
		for i, x := range u {
			binary.BigEndian.PutUint16(b[2*i:], x)
		}
		return b
	}
	fam, sty := enc(family), enc(style)
	var name bytes.Buffer
	w := func(v uint16) { _ = binary.Write(&name, binary.BigEndian, v) }
	w(0)
	w(2)
	w(6 + 2*12)
	w(3)
	w(1)
	w(0x409)
	w(1)
	w(uint16(len(fam)))
	w(0)
	w(3)
	w(1)
	w(0x409)
	w(2)
	w(uint16(len(sty)))
	w(uint16(len(fam)))
	name.Write(fam)
	name.Write(sty)
	var f bytes.Buffer
	_ = binary.Write(&f, binary.BigEndian, uint32(0x00010000))
	_ = binary.Write(&f, binary.BigEndian, uint16(1))
	f.Write(make([]byte, 6))
	f.WriteString("name")
	_ = binary.Write(&f, binary.BigEndian, uint32(0))
	_ = binary.Write(&f, binary.BigEndian, uint32(12+16))
	_ = binary.Write(&f, binary.BigEndian, uint32(name.Len()))
	f.Write(name.Bytes())
	return f.Bytes()
}

func TestReadFont(t *testing.T) {
	p := filepath.Join(t.TempDir(), "brand.ttf")
	_ = os.WriteFile(p, sfnt("Acme Sans", "Bold"), 0o644)
	if s, _ := SniffFile(p); s.Kind != "font" || s.Format != "ttf" {
		t.Fatalf("sniffed %+v", s)
	}
	fi := ReadFont(p)
	if fi.Family != "Acme Sans" || fi.Style != "Bold" {
		t.Fatalf("%+v", fi)
	}
}

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{"Acme Logo (dark)": "acme-logo-dark", "Équipe été": "equipe-ete", "2026 deck": "r-2026-deck", "!!!": "resource",
		"logo": "logo", strings.Repeat("long ", 20): "long-long-long-long-long-long-long-long"} {
		if got := Slug(in); got != want || !NamePattern.MatchString(got) {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
	if numbered("logo", 2) != "logo-2" || numbered("logo-2", 3) != "logo-3" {
		t.Error("numbering")
	}
}

func TestRouteWebAddress(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/acme/widget":           LinkRepo,
		"https://github.com/acme/widget/tree/main": LinkRepo,
		"https://github.com/acme":                  LinkAmbiguous,
		"https://vimeo.com/123456":                 LinkVideo,
		"https://cdn.example.com/clip.mp4":         LinkVideo,
		"https://example.com/brand/logo.svg":       LinkPage,
		"https://example.com/about":                LinkAmbiguous,
		"https://www.instagram.com/reel/abc":       LinkVideo,
		"https://docs.example.com/whitepaper.pdf":  LinkPage,
	} {
		if got, _ := RouteWebAddress(in); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}
