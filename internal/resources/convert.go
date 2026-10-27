package resources

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/tools"
)

// MaxPDFPages is how many pages of a PDF become pictures (the PDF itself is
// kept whole).
const MaxPDFPages = 60

// pdfPageWidth is the long side of a rendered PDF page, in pixels.
const pdfPageWidth = 1920

// ErrNoPoppler means poppler (pdftoppm, pdfinfo) is not installed: the PDF is
// kept, but its pages can't be used as pictures yet.
var ErrNoPoppler = errors.New("poppler isn't installed")

// PopplerNote is the plain advice when PDF pages can't be made (the command
// that installs poppler is PopplerCommand, for Details).
const PopplerNote = "Its pages aren't pictures yet: showing the pages of a PDF needs poppler, a free PDF tool (about 30 MB). Add it in Settings → Components, then choose Look again on the resource."

// PopplerCommand adds poppler (kept for Details, never in the words): the
// pdf-pages component (D324).
const PopplerCommand = "metarush components add pdf-pages"

// PDFPages renders a PDF's pages (at most MaxPDFPages) into dir as PNGs and
// returns them in order with the document's page count.
func PDFPages(ctx context.Context, t *tools.Set, path, dir string) (files []string, pageCount int, err error) {
	if !t.Has(tools.Pdftoppm) {
		return nil, 0, ErrNoPoppler
	}
	if t.Has(tools.Pdfinfo) {
		res, err := t.Run(ctx, tools.Spec{Tool: tools.Pdfinfo, Args: []string{"--", path}})
		if err == nil {
			if m := regexp.MustCompile(`(?m)^Pages:\s+(\d+)`).FindSubmatch(res.Stdout); m != nil {
				pageCount, _ = strconv.Atoi(string(m[1]))
			}
		}
	}
	last := MaxPDFPages
	if pageCount > 0 && pageCount < last {
		last = pageCount
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, 0, err
	}
	prefix := filepath.Join(dir, "page")
	cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if _, err := t.Run(cctx, tools.Spec{Tool: tools.Pdftoppm, Args: []string{"-png", "-scale-to", strconv.Itoa(pdfPageWidth), "-f", "1", "-l", strconv.Itoa(last), "--", path, prefix}}); err != nil {
		var fe *tools.ErrFailed
		if errors.As(err, &fe) {
			return nil, pageCount, fmt.Errorf("the PDF's pages couldn't be drawn: %s", strings.TrimSpace(firstLine(fe.Stderr)))
		}
		return nil, pageCount, err
	}
	files, _ = filepath.Glob(prefix + "-*.png")
	sort.Slice(files, func(i, j int) bool { return pageNumber(files[i]) < pageNumber(files[j]) })
	if pageCount == 0 {
		pageCount = len(files)
	}
	return files, pageCount, nil
}

func pageNumber(p string) int {
	base := strings.TrimSuffix(filepath.Base(p), ".png")
	i := strings.LastIndexByte(base, '-')
	n, _ := strconv.Atoi(base[i+1:])
	return n
}

// ConvertPhoto turns a HEIC or AVIF photo into a JPEG at out (macOS's sips,
// else ffmpeg). tool names what did it.
func ConvertPhoto(ctx context.Context, t *tools.Set, in, out string) (tool string, err error) {
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if runtime.GOOS == "darwin" && t.Has(tools.Sips) {
		if _, err := t.Run(cctx, tools.Spec{Tool: tools.Sips, Args: []string{"-s", "format", "jpeg", "-s", "formatOptions", "92", in, "--out", out}}); err == nil {
			if st, err := os.Stat(out); err == nil && st.Size() > 0 {
				return "sips", nil
			}
		}
	}
	if t.Has(tools.FFmpeg) {
		if _, err := t.Run(cctx, tools.Spec{Tool: tools.FFmpeg, Args: []string{"-v", "error", "-y", "-i", in, "-frames:v", "1", "-q:v", "2", out}}); err == nil {
			if st, err := os.Stat(out); err == nil && st.Size() > 0 {
				return "ffmpeg", nil
			}
		}
	}
	return "", errors.New("this photo is in HEIC (or AVIF) format and couldn't be converted on this computer; export it as JPEG or PNG and add it again")
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
