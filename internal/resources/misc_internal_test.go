package resources

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color/palette"
	"image/gif"
	"image/jpeg"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/image/font/gofont/goregular"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/tools"
)

func TestRouteByName(t *testing.T) {
	cases := map[string][3]string{
		"logo.PNG": {RouteResource, "image", "png"}, "photo.jpeg": {RouteResource, "image", "jpeg"}, "a.webp": {RouteResource, "image", "webp"},
		"IMG_1.HEIC": {RouteResource, "image", "heic"}, "b.avif": {RouteResource, "image", "avif"}, "c.gif": {RouteResource, "image", "gif"},
		"mark.svg": {RouteResource, "svg", "svg"}, "deck.pdf": {RouteResource, "pdf", "pdf"}, "a.ttf": {RouteResource, "font", "ttf"},
		"a.otf": {RouteResource, "font", "otf"}, "a.woff": {RouteResource, "font", "woff"}, "a.woff2": {RouteResource, "font", "woff2"},
		"interview.mov": {RouteFootage, "", ""}, "notes": {RouteFootage, "", ""},
	}
	for name, want := range cases {
		s := RouteByName(name)
		if s.Route != want[0] || s.Kind != want[1] || s.Format != want[2] {
			t.Errorf("%s: %+v", name, s)
		}
	}
	if !RouteByName("x.heif").Convert || !RouteByName("x.avif").Convert || RouteByName("x.png").Convert {
		t.Fatal("HEIC and AVIF are converted")
	}
}

// An animated GIF plays like a short video: it is footage; a still GIF is a picture.
func TestAnimatedGIFIsFootage(t *testing.T) {
	dir := t.TempDir()
	frame := func(c uint8) *image.Paletted {
		img := image.NewPaletted(image.Rect(0, 0, 16, 16), palette.Plan9)
		for i := range img.Pix {
			img.Pix[i] = c
		}
		return img
	}
	write := func(name string, g *gif.GIF) string {
		var buf bytes.Buffer
		if err := gif.EncodeAll(&buf, g); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	still := write("still.gif", &gif.GIF{Image: []*image.Paletted{frame(1)}, Delay: []int{0}})
	moving := write("moving.gif", &gif.GIF{Image: []*image.Paletted{frame(1), frame(2), frame(3)}, Delay: []int{10, 10, 10}, LoopCount: 0})
	if s, err := SniffFile(still); err != nil || s.Route != RouteResource || s.Format != "gif" {
		t.Fatalf("still: %+v %v", s, err)
	}
	if s, err := SniffFile(moving); err != nil || s.Route != RouteFootage {
		t.Fatalf("animated: %+v %v", s, err)
	}
	if animatedGIF(filepath.Join(dir, "missing.gif")) || animatedGIF(writeBytes(t, dir, "tiny.gif", []byte("GIF89a"))) {
		t.Fatal("not a GIF")
	}
	if _, err := SniffFile(filepath.Join(dir, "missing.png")); err == nil {
		t.Fatal("a missing file")
	}
}

func writeBytes(t *testing.T, dir, name string, b []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// A WOFF font is read through its compressed name table, like the font it wraps.
func TestReadFontFromTTFAndWOFF(t *testing.T) {
	dir := t.TempDir()
	if fi := ReadFont(writeBytes(t, dir, "go.ttf", goregular.TTF)); fi.Family != "Go" || fi.Style != "Regular" {
		t.Fatalf("ttf: %+v", fi)
	}
	name := sfntTable(goregular.TTF, "name")
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	_, _ = zw.Write(name)
	_ = zw.Close()
	woff := func(data []byte, orig int) []byte {
		h := make([]byte, 44+20)
		copy(h, "wOFF")
		binary.BigEndian.PutUint32(h[4:], 0x00010000)
		binary.BigEndian.PutUint16(h[12:], 1)
		copy(h[44:], "name")
		binary.BigEndian.PutUint32(h[48:], 64)
		binary.BigEndian.PutUint32(h[52:], uint32(len(data)))
		binary.BigEndian.PutUint32(h[56:], uint32(orig))
		out := append(h, data...)
		binary.BigEndian.PutUint32(out[8:], uint32(len(out)))
		return out
	}
	if fi := ReadFont(writeBytes(t, dir, "go.woff", woff(z.Bytes(), len(name)))); fi.Family != "Go" {
		t.Fatalf("compressed woff: %+v", fi)
	}
	if fi := ReadFont(writeBytes(t, dir, "plain.woff", woff(name, len(name)))); fi.Family != "Go" {
		t.Fatalf("uncompressed woff: %+v", fi)
	}
	if fi := ReadFont(writeBytes(t, dir, "broken.woff", woff(z.Bytes(), len(name)+7))); fi.Family != "" {
		t.Fatalf("a table of the wrong length: %+v", fi)
	}
	if fi := ReadFont(writeBytes(t, dir, "x.woff2", append([]byte("wOF2"), make([]byte, 40)...))); fi.Family != "" {
		t.Fatalf("woff2 isn't read: %+v", fi)
	}
	if woffTable([]byte("wOFF"), "name") != nil || woffTable(woff(name, len(name)), "cmap") != nil || (ReadFont(filepath.Join(dir, "none.ttf")) != FontInfo{}) {
		t.Fatal("short or missing")
	}
}

// Pictures the visual index looks at: upright, small, on white.
func TestStillJPEGAndThumb(t *testing.T) {
	dir := t.TempDir()
	logo := writePNG(t, dir, "logo.png", testLogo(400, 200))
	b, err := StillJPEG(logo, 100)
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(b))
	if err != nil || img.Bounds().Dx() != 100 || img.Bounds().Dy() != 50 {
		t.Fatalf("still: %v %v", img.Bounds(), err)
	}
	if r, g, bb, _ := img.At(2, 2).RGBA(); r>>8 < 240 || g>>8 < 240 || bb>>8 < 240 {
		t.Fatal("the see-through background is laid on white")
	}
	if _, err := StillJPEG(filepath.Join(dir, "none.png"), 100); err == nil {
		t.Fatal("a missing file")
	}
	if _, err := StillJPEG(writeBytes(t, dir, "x.png", []byte("not a picture")), 100); err == nil {
		t.Fatal("not a picture")
	}
	thumb, w, h, err := ThumbFromPNG(logo)
	if err != nil || len(thumb) == 0 || w > ThumbSize || h > ThumbSize || w != 2*h {
		t.Fatalf("thumb %dx%d %v", w, h, err)
	}
	if _, _, _, err := ThumbFromPNG(filepath.Join(dir, "none.png")); err == nil {
		t.Fatal("a missing file")
	}
}

func TestTaggers(t *testing.T) {
	ctx := context.Background()
	tags := func(in TagInput) []string {
		var out []string
		for _, tg := range AutoTags(ctx, in) {
			out = append(out, tg.Tag)
		}
		return out
	}
	img := func(w, h int, alpha bool) *models.ResourceVersion {
		return &models.ResourceVersion{Width: w, Height: h, HasAlpha: alpha}
	}
	cases := []struct {
		in   TagInput
		want []string
	}{
		{TagInput{Name: "Acme_logo-final.png", Kind: models.ResourceImage, Version: img(900, 300, true), Palette: 3}, []string{"logo"}},
		{TagInput{Name: "app.png", Kind: models.ResourceImage, Version: img(256, 256, true), Palette: 4}, []string{"icon", "logo"}},
		{TagInput{Name: "x.png", Kind: models.ResourceImage, Version: img(800, 780, true), Palette: 30}, []string{"icon"}},
		{TagInput{Name: "Screen Shot 2026.png", Kind: models.ResourceImage, Version: img(2880, 1800, false), Palette: 40}, []string{"screenshot"}},
		{TagInput{Name: "DSC_0042.jpg", Kind: models.ResourceImage, Version: img(1170, 2532, false)}, []string{"photo", "screenshot"}},
		{TagInput{Name: "q3 deck.pdf", Kind: models.ResourcePDF, Version: &models.ResourceVersion{Pages: []models.ResourcePage{{Width: 1920, Height: 1080}}}}, []string{"slides"}},
		{TagInput{Name: "terms.pdf", Kind: models.ResourcePDF, Version: &models.ResourceVersion{Pages: []models.ResourcePage{{Width: 1240, Height: 1754}}}}, []string{"document"}},
		{TagInput{Name: "mark.svg", Kind: models.ResourceSVG, Version: &models.ResourceVersion{}, Palette: 2}, []string{"logo"}},
		{TagInput{Name: "architecture-diagram.png", Kind: models.ResourceImage, Version: img(0, 0, false)}, []string{"diagram"}},
		{TagInput{Name: "code.png", Kind: models.ResourceImage, Version: &models.ResourceVersion{Width: 1920, Height: 1080, Source: models.ResourceSource{MadeWith: &models.ResourceMadeWith{Kind: MakeCode}}}}, []string{"code"}},
		{TagInput{Name: "d.svg", Kind: models.ResourceSVG, Version: &models.ResourceVersion{Source: models.ResourceSource{MadeWith: &models.ResourceMadeWith{Kind: MakeDiagram}}}}, []string{"diagram"}},
		{TagInput{Name: "card.png", Kind: models.ResourceImage, Version: &models.ResourceVersion{Source: models.ResourceSource{MadeWith: &models.ResourceMadeWith{Kind: MakeStats}}}}, []string{"stats"}},
		{TagInput{Name: "slides keynote.key", Kind: models.ResourceImage}, []string{"slides"}},
	}
	for _, c := range cases {
		if got := tags(c.in); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.in.Name, got, c.want)
		}
	}
	for _, tg := range []Tagger{filenameTagger{}, pictureTagger{}, madeTagger{}} {
		if tg.Name() == "" {
			t.Fatal("taggers have names")
		}
	}
	// What the visual index registers (stand-ins that only answer for one file).
	RegisterTagger(onlyFor{"mark-for-the-test-tagger.png"})
	RegisterTextFinder(onlyFor{"mark-for-the-test-tagger.png"})
	in := TagInput{Name: "mark-for-the-test-tagger.png", Kind: models.ResourceImage, Version: img(10, 10, false)}
	if got := tags(in); !slices.Contains(got, "seen-by-the-index") {
		t.Fatalf("registered tagger: %v", got)
	}
	if FindText(ctx, in) != "SPRING SALE" || FindText(ctx, TagInput{Name: "other.png"}) != "" {
		t.Fatal("registered text finder")
	}
}

type onlyFor struct{ name string }

func (o onlyFor) Name() string { return "test" }
func (o onlyFor) Tags(_ context.Context, in TagInput) []models.ResourceTag {
	if in.Name != o.name {
		return nil
	}
	return []models.ResourceTag{{Tag: " Seen-By-The-Index ", Confidence: 0.9}, {Tag: "  "}}
}
func (o onlyFor) Text(_ context.Context, in TagInput) (string, error) {
	if in.Name != o.name {
		return "", errors.New("not mine")
	}
	return "  SPRING SALE  ", nil
}

func TestSmallResourceWords(t *testing.T) {
	for k, want := range map[string]string{models.ResourceSVG: "vector picture", models.ResourcePDF: "PDF", models.ResourceFont: "font", models.ResourceRepo: "repository",
		models.ResourceWebsite: "web page", models.ResourceImage: "picture"} {
		if KindWord(k) != want {
			t.Errorf("KindWord(%s) = %q", k, KindWord(k))
		}
	}
	for bg, want := range map[string]string{"#FFFFFF": "light", "#f4f1ea": "light", "#101014": "dark", "#2E7D32": "dark", "nope": "dark", "#12345": "dark", "#GGGGGG": "dark"} {
		if themeFor(bg) != want {
			t.Errorf("themeFor(%s) = %q", bg, themeFor(bg))
		}
	}
	code, cut := codeForSlide([]string{"\tif x {  ", "\t\treturn " + strings.Repeat("y", 200), "}"})
	if cut != 1 || !strings.HasPrefix(code, "    if x {\n        return yyy") || !strings.Contains(code, "…\n}") {
		t.Fatalf("code %q (cut %d)", code, cut)
	}
	if expandTabs("a\tb", 4) != "a   b" || expandTabs("plain", 4) != "plain" || clipBytes("abcdef", 3) != "abc" || clipBytes("ab", 3) != "ab" {
		t.Fatal("expandTabs / clipBytes")
	}
	if DiagramFormat("%% a comment\nflowchart LR\n  a --> b") != "mermaid" || DiagramFormat("a -> b: calls") != "d2" {
		t.Fatal("DiagramFormat")
	}
	if got := CleanTags([]string{" Logo ", "logo", "", strings.Repeat("x", 41), "Brand  Kit"}); !slices.Equal(got, []string{"logo", "brand kit"}) {
		t.Fatalf("CleanTags %v", got)
	}
	ts := []models.ResourceTag{{Tag: "logo"}, {Tag: "brand"}, {Tag: "icon"}}
	sortTags(ts)
	if ts[0].Tag != "brand" || ts[2].Tag != "logo" {
		t.Fatalf("sortTags %v", ts)
	}
	if scopeWords(models.ScopeWorkspace) != "the workspace library" || scopeWords(models.ScopeProject) != "this project's resources" {
		t.Fatal("scopeWords")
	}
	for n, want := range map[int64]string{12: "12 bytes", 4096: "4 KB", 3 << 20: "3.0 MB", 5 << 30: "5.0 GB"} {
		if humanBytes(n) != want {
			t.Errorf("humanBytes(%d) = %q", n, humanBytes(n))
		}
	}
	if plainDate(time.Time{}) != "date unknown" || plainDate(time.Date(2026, 10, 3, 23, 0, 0, 0, time.UTC)) != "3 Oct 2026" {
		t.Fatal("plainDate")
	}
	if !isBinaryData([]byte("a\x00b")) || isBinaryData([]byte("plain text, ünïcode")) || !isBinaryData(bytes.Repeat([]byte{0xff, 'a'}, 20)) ||
		isBinaryData([]byte("caf\xe9 au lait, cr\xe8me")) || isBinaryData(append(bytes.Repeat([]byte("x"), 9000), 0)) {
		t.Fatal("isBinaryData")
	}
	if b, err := readSmall(writeBytes(t, t.TempDir(), "s.svg", []byte("<svg/>"))); err != nil || string(b) != "<svg/>" {
		t.Fatal("readSmall")
	}
	if _, err := readSmall(filepath.Join(t.TempDir(), "none")); err == nil {
		t.Fatal("readSmall of nothing")
	}
	if pageNumber("/x/page-07.png") != 7 || firstLine("one\ntwo") != "one" || len(firstLine(strings.Repeat("z", 300))) != 200 {
		t.Fatal("pageNumber / firstLine")
	}
}

// A git failure becomes advice: sign in, no such ref, try again, or what git said.
func TestGitProblem(t *testing.T) {
	r := RepoRef{Owner: "acme", Name: "widget", Ref: "v9"}
	cases := []struct {
		err       error
		permanent bool
		says      string
	}{
		{&tools.ErrFailed{Stderr: "remote: Repository not found.\nfatal: repository 'https://github.com/acme/widget.git/' not found"}, true, "GitHub didn't let this computer read acme/widget."},
		{errors.New("fatal: couldn't find remote ref v9"), true, `acme/widget has no branch, tag or commit called "v9".`},
		{errors.New("fatal: unable to access: Could not resolve host: github.com"), false, "This computer couldn't reach GitHub; MetaRush tries again"},
		{errors.New("fatal: something else entirely\nmore"), false, "MetaRush couldn't get acme/widget from GitHub (fatal: something else entirely)"},
	}
	for _, c := range cases {
		err := GitProblem(r, c.err)
		var p *Permanent
		if errors.As(err, &p) != c.permanent || !strings.HasPrefix(err.Error(), c.says) {
			t.Errorf("%v: %v (permanent %v)", c.err, err, errors.As(err, &p))
		}
	}
	if !FullSHA(strings.Repeat("a", 40)) || FullSHA("abc") || !SafeRef("main") || SafeRef("-x") || !IsLFSPointer([]byte("version https://git-lfs.github.com/spec/v1\noid sha256:ab\nsize 12\n")) {
		t.Fatal("git helpers")
	}
	if CloneURL(RepoRef{Owner: "acme", Name: "widget", Host: "github.com"}) == "" {
		t.Fatal("CloneURL")
	}
}

// What GitHub says about a repository comes through the person's own gh
// (a stand-in here); without gh, or for a test fixture, nothing is asked.
func TestGitHubFactsThroughGH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts")
	}
	t.Setenv("METARUSH_GIT_BASE", "")
	fake := t.TempDir()
	script := `#!/bin/sh
for a in "$@"; do p="$a"; done
case "$p" in
  repos/acme/widget) printf '%s\n' '{"description":"Clips  to cuts","default_branch":"main","stargazers_count":42,"forks_count":3,"private":false,"topics":["video","go"],"license":{"spdx_id":"MIT"}}' ;;
  repos/acme/widget/languages) printf '%s\n' '{"Go":9000,"Shell":100}' ;;
  repos/acme/widget/contributors*) printf '%s\n' '[{"login":"ada","contributions":12}]' ;;
  repos/acme/widget/commits*) printf '%s\n' '[{"sha":"abc","commit":{"message":"Add queue\n\nbody","author":{"name":"Ada Park","date":"2026-10-01T10:00:00Z"}}}]' ;;
  *) exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(fake, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fake+string(os.PathListSeparator)+os.Getenv("PATH"))
	ts := tools.New(config.Tools{})
	if ts.Path(tools.GH) != filepath.Join(fake, "gh") {
		t.Skip("the stand-in gh isn't the one MetaRush would use here")
	}
	ctx := context.Background()
	f := githubFacts(ctx, ts, RepoRef{Host: "github.com", Owner: "acme", Name: "widget"}, "abc")
	if !f.ok || f.description != "Clips to cuts" || f.stars != 42 || f.forks != 3 || f.license != "MIT" || len(f.topics) != 2 || f.languages["Go"] != 9000 ||
		len(f.contributors) != 1 || f.contributors[0].Commits != 12 || len(f.recent) != 1 || f.recent[0].Subject != "Add queue" || f.recent[0].Author != "Ada Park" {
		t.Fatalf("facts: %+v", f)
	}
	if f := githubFacts(ctx, ts, RepoRef{Host: "github.com", Owner: "acme", Name: "gone"}, "abc"); f.ok {
		t.Fatal("gh couldn't answer")
	}
	if f := githubFacts(ctx, ts, RepoRef{Host: "gitlab.com", Owner: "acme", Name: "widget"}, "abc"); f.ok {
		t.Fatal("only GitHub")
	}
	t.Setenv("METARUSH_GIT_BASE", t.TempDir())
	if f := githubFacts(ctx, ts, RepoRef{Host: "github.com", Owner: "acme", Name: "widget"}, "abc"); f.ok {
		t.Fatal("test fixtures never ask GitHub")
	}
	if langs := languagesOf(map[string]int64{"Go": 900, "Shell": 100, "None": 0}); len(langs) != 2 || langs[0].Name != "Go" || langs[0].Share != 0.9 {
		t.Fatalf("languages %+v", langs)
	}
}
