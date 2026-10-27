package resources

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/image/font/gofont/goregular"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/tools"
)

// onePagePDF is a small, valid one-page PDF (a wide page, like a slide).
const onePagePDF = "%PDF-1.4\n1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj\n2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj\n" +
	"3 0 obj<</Type/Page/Parent 2 0 R/MediaBox[0 0 400 225]/Contents 4 0 R>>endobj\n" +
	"4 0 obj<</Length 44>>stream\n0.9 0.3 0.2 rg 20 20 360 185 re f\nendstream endobj\n" +
	"trailer<</Root 1 0 R/Size 5>>\n%%EOF\n"

// Adding files: each kind is described its own way, and what can't be added
// says why.
func TestAddFileKindsAndRefusals(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	dir := t.TempDir()
	film := project(t, a, "Film")

	// A font: its family names it.
	font, err := AddFile(ctx, a, writeBytes(t, dir, "GoRegular.ttf", goregular.TTF), AddOptions{ProjectID: film.ID})
	if err != nil || font.Status != StatusAdded || font.Resource.Kind != models.ResourceFont || font.Resource.Title != "Go" || font.Resource.Latest().Family != "Go" {
		t.Fatalf("font: %+v %v", font, err)
	}
	// An SVG loses what could run code, and keeps the original's fingerprint.
	svg := writeBytes(t, dir, "mark.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 20 10"><script>alert(1)</script><rect width="20" height="10" fill="#c03" onclick="x()"/></svg>`))
	sr, err := AddFile(ctx, a, svg, AddOptions{ProjectID: film.ID})
	if err != nil || len(sr.Changes) == 0 || sr.Resource.Latest().OriginalSHA256 == "" || !sr.Resource.Latest().HasAlpha {
		t.Fatalf("svg: %+v %v", sr, err)
	}
	// A PDF: its pages become pictures where poppler is installed; a note says so where it isn't.
	pdf, err := AddFile(ctx, a, writeBytes(t, dir, "deck.pdf", []byte(onePagePDF)), AddOptions{ProjectID: film.ID})
	if err != nil {
		t.Fatal(err)
	}
	if v := pdf.Resource.Latest(); a.Tools.Has(tools.Pdftoppm) {
		if len(v.Pages) != 1 || v.PageCount != 1 || v.Thumbnail == nil || v.Width <= v.Height {
			t.Fatalf("pdf pages: %+v", v)
		}
		if p, _, err := LocalFile(ctx, a, pdf.Resource, "", 1); err != nil || p == "" {
			t.Fatalf("page 1: %v", err)
		}
	} else if len(pdf.Notes) != 1 || pdf.Notes[0] != PopplerNote {
		t.Fatalf("pdf without poppler: %+v", pdf.Notes)
	}

	// The same bytes already in the workspace library: nothing new for the project.
	shared := add(t, a, "", "shared.png", testLogo(50, 20), AddOptions{})
	again, err := AddFile(ctx, a, writePNG(t, t.TempDir(), "copy.png", testLogo(50, 20)), AddOptions{ProjectID: film.ID})
	if err != nil || again.Status != StatusSame || again.Resource.ID != shared.ID || again.Message != "copy.png is already in the workspace library as shared." {
		t.Fatalf("already in the library: %+v %v", again, err)
	}
	// A name taken by another picture: numbered; asked for exactly and taken by another kind: refused.
	add(t, a, film.ID, "logo.png", testLogo(60, 20), AddOptions{})
	two, err := AddFile(ctx, a, writePNG(t, t.TempDir(), "logo-new.png", testLogo(61, 20)), AddOptions{ProjectID: film.ID, Title: "Logo"})
	if err != nil || two.Resource.Name != "logo-2" {
		t.Fatalf("numbered: %+v %v", two.Resource, err)
	}
	if _, err := AddFile(ctx, a, writePNG(t, t.TempDir(), "x.png", testLogo(62, 20)), AddOptions{ProjectID: film.ID, Name: "mark"}); apperr.From(err).Code != apperr.Conflict {
		t.Fatalf("an exact name another kind has: %v", err)
	}

	refusals := []struct {
		name string
		body []byte
		o    AddOptions
		code string
		says string
	}{
		{"bad.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><rect></svg>`), AddOptions{}, apperr.InvalidInput, "bad.svg: this SVG couldn't be read safely"},
		{"broken.heic", []byte("\x00\x00\x00\x18ftypheic\x00\x00\x00\x00garbage"), AddOptions{}, apperr.InvalidInput, "this photo is in HEIC (or AVIF) format and couldn't be converted"},
		{"ok.png", nil, AddOptions{Name: "Not OK"}, apperr.InvalidInput, `"Not OK" can't be a resource's short name`},
		{"ok.png", nil, AddOptions{Scope: models.ScopeProject}, apperr.InvalidInput, "Say which project it's for."},
		{"ok.png", nil, AddOptions{Scope: "everywhere"}, apperr.InvalidInput, "Choose where it goes: a project, or the workspace library."},
		{"ok.png", nil, AddOptions{ProjectID: ids.New(ids.Project)}, apperr.NotFound, "That project was not found."},
		{"ok.png", nil, AddOptions{ResourceID: ids.New(ids.Resource)}, apperr.NotFound, "The resource this was meant to be a new version of was not found."},
	}
	for _, c := range refusals {
		p := writePNG(t, t.TempDir(), c.name, testPhoto(30+len(c.says)%7, 20))
		if c.body != nil {
			p = writeBytes(t, t.TempDir(), c.name, c.body)
		}
		_, err := AddFile(ctx, a, p, c.o)
		if e := apperr.From(err); e.Code != c.code || !strings.HasPrefix(e.Message, c.says) {
			t.Errorf("%s %+v: %+v", c.name, c.o, e)
		}
	}
	// Not something to add: empty, a folder, words, a missing file.
	if r, err := AddFile(ctx, a, writeBytes(t, dir, "empty.png", nil), AddOptions{}); err != nil || r.Status != StatusSkipped || r.Message != "empty.png is empty or not a file." {
		t.Fatalf("empty: %+v %v", r, err)
	}
	if r, err := AddFile(ctx, a, t.TempDir(), AddOptions{}); err != nil || r.Status != StatusSkipped {
		t.Fatalf("a folder: %+v %v", r, err)
	}
	if r, err := AddFile(ctx, a, writeBytes(t, dir, "notes.txt", []byte("words")), AddOptions{}); !errors.Is(err, ErrNotResource) || r.Message != "notes.txt isn't a picture, SVG, PDF or font." {
		t.Fatalf("words: %+v %v", r, err)
	}
	if _, err := AddFile(ctx, a, filepath.Join(dir, "gone.png"), AddOptions{}); apperr.From(err).Message != "MetaRush can't read gone.png." {
		t.Fatalf("missing: %v", err)
	}
	if _, err := os.Stat(a.Cfg.Scratch.Dir); err != nil {
		t.Fatalf("scratch: %v", err)
	}
}

// A HEIC photo (made here with macOS's sips) becomes a JPEG on the way in.
func TestHEICPhotoIsConverted(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("making a HEIC photo needs macOS's sips")
	}
	sips, err := exec.LookPath("sips")
	if err != nil {
		t.Skip("no sips")
	}
	a := testapp.New(t)
	dir := t.TempDir()
	png := writePNG(t, dir, "photo.png", testPhoto(120, 80))
	heic := filepath.Join(dir, "IMG_0001.HEIC")
	if out, err := exec.Command(sips, "-s", "format", "heic", png, "--out", heic).CombinedOutput(); err != nil {
		t.Skipf("sips can't write HEIC here: %v %s", err, out)
	}
	r, err := AddFile(context.Background(), a, heic, AddOptions{})
	if err != nil || r.Status != StatusAdded || r.Resource.Latest().MIME != "image/jpeg" || len(r.Changes) != 1 || !strings.HasPrefix(r.Changes[0], "converted from HEIC to JPEG") {
		t.Fatalf("converted: %+v %v", r, err)
	}
}
