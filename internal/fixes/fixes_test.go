package fixes

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/color"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/tools"
)

// A fixed copy is named by what it does (D152): the same fixes in any order
// are the same copy; another take is another; a fix that makes no copy
// (silent ends of music) has none.
func TestFingerprint(t *testing.T) {
	a := Fingerprint([]string{models.FixHum, models.FixVoices}, 0)
	if a == "" || a != Fingerprint([]string{models.FixVoices, models.FixHum}, 0) {
		t.Fatalf("order: %q", a)
	}
	if a == Fingerprint([]string{models.FixHum, models.FixVoices}, 1) || a == Fingerprint([]string{models.FixHum}, 0) {
		t.Fatal("another take or other fixes must be another copy")
	}
	if h := Fingerprint([]string{models.FixEnds}, 0); h != "" {
		t.Fatalf("trimming silence makes no copy: %q", h)
	}
	if got := Doing([]string{models.FixColour, models.FixSteady}); got != "Steadying the camera and balancing the colour" {
		t.Fatalf("doing: %q", got)
	}
}

// Pictures (D153): a logo on a plain background with room around it is
// marked for both, and small pictures for enlarging; the fixes do what they say.
func TestPictureFixes(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 300, 200))
	for y := 0; y < 200; y++ {
		for x := 0; x < 300; x++ {
			c := color.NRGBA{250, 250, 250, 255}
			if x >= 100 && x < 200 && y >= 70 && y < 130 {
				c = color.NRGBA{200, 30, 40, 255}
			}
			img.SetNRGBA(x, y, c)
		}
	}
	f := pictureFacts(img)
	if f.Plain < PlainBorderFits || f.HasAlpha {
		t.Fatalf("facts %+v", f)
	}
	fits := map[string]bool{}
	for _, m := range pictureMarks(f) {
		fits[m.Fix] = m.Fits
	}
	if !fits[models.FixBackground] || !fits[models.FixEdges] || !fits[models.FixLarger] {
		t.Fatalf("marks %+v", fits)
	}
	cut := removeBackground(img, f.Background)
	if cut.NRGBAAt(2, 2).A != 0 || cut.NRGBAAt(150, 100).A != 255 {
		t.Fatalf("background: corner %v, middle %v", cut.NRGBAAt(2, 2), cut.NRGBAAt(150, 100))
	}
	trimmed := trimEdges(cut, f.Background, true)
	if w, h := trimmed.Bounds().Dx(), trimmed.Bounds().Dy(); w > 120 || h > 80 || w < 100 || h < 60 {
		t.Fatalf("trimmed to %dx%d", w, h)
	}
	big, factor := enlarge(trimmed)
	if factor != 4 || big.Bounds().Dx() != 4*trimmed.Bounds().Dx() {
		t.Fatalf("enlarged %d times to %v", factor, big.Bounds())
	}
}

// fakeFFmpeg writes a program that answers `-filters` with or without
// vid.stab's filters.
func fakeFFmpeg(t *testing.T, dir string, vidstab bool) []byte {
	t.Helper()
	filters := "deshake"
	if vidstab {
		filters = "deshake vidstabdetect vidstabtransform"
	}
	script := []byte("#!/bin/sh\necho ' ... " + filters + " ...'\n")
	if err := os.WriteFile(filepath.Join(dir, "ffmpeg"), script, 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

func zipOf(t *testing.T, name string, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func shaHex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// The better steadying (D153): MetaRush's own ffmpeg is never changed; a
// pinned build is downloaded, checked by SHA-256 (the archive and the program)
// and run before it is used; a build that differs is refused.
func TestStabilizerSetsUpAPinnedBuild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts")
	}
	ctx := context.Background()
	sys := t.TempDir()
	fakeFFmpeg(t, sys, false) // the configured ffmpeg: no vid.stab
	ts := tools.New(config.Tools{FFmpeg: filepath.Join(sys, "ffmpeg")})
	pkg := t.TempDir()
	prog := fakeFFmpeg(t, pkg, true)
	z := zipOf(t, "ffmpeg", prog)
	build := stabBuild{URL: "https://builds.example/ffmpeg.zip", ZipSHA: shaHex(z), BinSHA: shaHex(prog), Entry: "ffmpeg"}

	// Downloaded from its address.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(z) }))
	defer srv.Close()
	home := t.TempDir()
	s := &Stabilizer{Home: home, Tools: ts, Builds: []stabBuild{{URL: srv.URL + "/ffmpeg.zip", ZipSHA: build.ZipSHA, BinSHA: build.BinSHA, Entry: "ffmpeg"}}}
	if st := s.Status(ctx); st.Ready || st.Engine != EngineDeshake || !st.Available {
		t.Fatalf("before: %+v", st)
	}
	st, err := s.Ensure(ctx, nil)
	if err != nil || !st.Ready || st.Engine != EngineVidStab || st.Source != "downloaded" {
		t.Fatalf("after: %+v %v", st, err)
	}
	if ts.Path(tools.FFmpeg) != filepath.Join(sys, "ffmpeg") {
		t.Fatal("the ffmpeg everything else uses must not change")
	}

	// From a copy MetaRush was given (offline), and a build that differs is refused.
	zipPath := filepath.Join(t.TempDir(), "given.zip")
	if err := os.WriteFile(zipPath, z, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("METARUSH_STABILIZER_ZIP", zipPath)
	s2 := &Stabilizer{Home: t.TempDir(), Tools: tools.New(config.Tools{FFmpeg: filepath.Join(sys, "ffmpeg")}), Builds: []stabBuild{build}}
	if st, err := s2.Ensure(ctx, nil); err != nil || !st.Ready {
		t.Fatalf("given copy: %+v %v", st, err)
	}
	bad := build
	bad.BinSHA = shaHex([]byte("another program"))
	s3 := &Stabilizer{Home: t.TempDir(), Tools: tools.New(config.Tools{FFmpeg: filepath.Join(sys, "ffmpeg")}), Builds: []stabBuild{bad}}
	if st, err := s3.Ensure(ctx, nil); err == nil || st.Ready || !strings.Contains(err.Error(), "isn't the build MetaRush expects") {
		t.Fatalf("a different program: %+v %v", st, err)
	}
	if _, err := os.Stat(s3.binary()); err == nil {
		t.Fatal("a refused program must not be kept")
	}
}
